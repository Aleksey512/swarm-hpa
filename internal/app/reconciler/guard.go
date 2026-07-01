package reconciler

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// Guard is the single chokepoint for Swarm mutations: every scale and heal flows
// through here and is gated by dry-run and per-service cooldown. It is the ONLY
// caller of the SwarmController mutation methods.
//
// In dry-run mode an intended action is logged and the cooldown is recorded so
// the "would..." log is throttled to once per window (the record gates the LOG,
// not a real action).
type Guard struct {
	swarm    port.SwarmController
	cooldown *Cooldown
	windows  Cooldowns
	dryRun   bool
	recorder port.Recorder
	logger   *slog.Logger
}

// NewGuard constructs a Guard. windows holds the per-action cooldown windows
// (scale-up / scale-down / heal). A nil recorder falls back to a no-op and a nil
// logger to slog.Default.
func NewGuard(swarm port.SwarmController, cooldown *Cooldown, windows Cooldowns, dryRun bool, recorder port.Recorder, logger *slog.Logger) *Guard {
	if logger == nil {
		logger = slog.Default()
	}
	if recorder == nil {
		recorder = port.NopRecorder{}
	}
	return &Guard{swarm: swarm, cooldown: cooldown, windows: windows, dryRun: dryRun, recorder: recorder, logger: logger}
}

// Scale moves a replicated service toward `desired`, gated by dry-run + cooldown.
// It is a no-op for non-replicated services and when no change is needed.
func (g *Guard) Scale(ctx context.Context, svc model.ManagedService, desired uint64) error {
	id, name := svc.Ref.ID, svc.Ref.Name
	switch {
	case !svc.Replicated:
		g.logger.Debug("skip scale: not a replicated service", "service", name)
		return nil
	case desired == svc.Replicas:
		g.logger.Debug("skip scale: no change", "service", name, "replicas", desired)
		return nil
	}

	// Pick the cooldown window by direction: scale-ups and scale-downs are gated
	// independently so a service can react fast one way and slowly the other.
	window, direction := g.windows.ScaleDown, "down"
	if desired > svc.Replicas {
		window, direction = g.windows.ScaleUp, "up"
	}
	if !g.cooldown.Allowed(id, window) {
		g.logger.Info("scale suppressed by cooldown", "service", name, "direction", direction)
		g.recorder.ActionSuppressed("scale", "cooldown")
		return nil
	}

	if g.dryRun {
		g.logger.Info("dry-run: would scale", "service", name, "from", svc.Replicas, "to", desired, "direction", direction)
		g.cooldown.Record(id)
		g.recorder.ActionSuppressed("scale", "dry_run")
		return nil
	}

	if err := g.swarm.Scale(ctx, id, desired); err != nil {
		g.recorder.Error("scale")
		return fmt.Errorf("scale %s: %w", name, err)
	}
	g.cooldown.Record(id)
	g.recorder.ScaleApplied(name)
	g.logger.Info("scaled", "service", name, "from", svc.Replicas, "to", desired, "direction", direction)
	return nil
}

// Heal force-updates a service to unstick its tasks, gated by dry-run + cooldown.
func (g *Guard) Heal(ctx context.Context, svc model.ManagedService) error {
	id, name := svc.Ref.ID, svc.Ref.Name
	if !g.cooldown.Allowed(id, g.windows.Heal) {
		g.logger.Info("heal suppressed by cooldown", "service", name)
		g.recorder.ActionSuppressed("heal", "cooldown")
		return nil
	}

	if g.dryRun {
		g.logger.Info("dry-run: would force-update (heal)", "service", name)
		g.cooldown.Record(id)
		g.recorder.ActionSuppressed("heal", "dry_run")
		return nil
	}

	if err := g.swarm.ForceUpdate(ctx, id); err != nil {
		g.recorder.Error("heal")
		return fmt.Errorf("heal %s: %w", name, err)
	}
	g.cooldown.Record(id)
	g.recorder.HealApplied(name)
	g.logger.Info("healed (forced reschedule)", "service", name)
	return nil
}

// Rebalance force-updates a service to redistribute its tasks across nodes,
// relieving a busy node. It is gated by the per-service rebalance opt-in
// (svc.Rebalance), dry-run, and a dedicated (long) cooldown. The recommendation
// is ALWAYS logged — even when suppressed — so operators can see what the
// rebalancer wanted regardless of dry-run.
//
// Mechanism note: Swarm has no load-aware task-move API, so the only available
// lever is force-update, which re-cycles ALL of the service's replicas so the
// scheduler can place them afresh. That is disruptive, which is exactly why
// rebalancing is opt-in, dry-run by default, and behind a long cooldown.
// Targeted per-task relocation is a future enhancement.
func (g *Guard) Rebalance(ctx context.Context, svc model.ManagedService, from, to string) error {
	id, name := svc.Ref.ID, svc.Ref.Name
	if !svc.Rebalance {
		g.logger.Debug("skip rebalance: service not opted in", "service", name)
		return nil
	}
	if !g.cooldown.Allowed(id, g.windows.Rebalance) {
		g.logger.Info("rebalance suppressed by cooldown", "service", name, "from_node", from, "to_node", to)
		g.recorder.ActionSuppressed("rebalance", "cooldown")
		return nil
	}

	if g.dryRun {
		g.logger.Info("dry-run: would rebalance (force-update)", "service", name, "from_node", from, "to_node", to)
		g.cooldown.Record(id)
		g.recorder.ActionSuppressed("rebalance", "dry_run")
		return nil
	}

	if err := g.swarm.ForceUpdate(ctx, id); err != nil {
		g.recorder.Error("rebalance")
		return fmt.Errorf("rebalance %s: %w", name, err)
	}
	g.cooldown.Record(id)
	g.recorder.RebalanceApplied(name)
	g.logger.Info("rebalanced (forced reschedule)", "service", name, "from_node", from, "to_node", to)
	return nil
}
