package reconciler

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/wmid/swarm-hpa/internal/core/model"
	"github.com/wmid/swarm-hpa/internal/core/port"
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
	dryRun   bool
	logger   *slog.Logger
}

// NewGuard constructs a Guard. A nil logger falls back to slog.Default.
func NewGuard(swarm port.SwarmController, cooldown *Cooldown, dryRun bool, logger *slog.Logger) *Guard {
	if logger == nil {
		logger = slog.Default()
	}
	return &Guard{swarm: swarm, cooldown: cooldown, dryRun: dryRun, logger: logger}
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
	case !g.cooldown.Allowed(id):
		g.logger.Info("scale suppressed by cooldown", "service", name)
		return nil
	}

	if g.dryRun {
		g.logger.Info("dry-run: would scale", "service", name, "from", svc.Replicas, "to", desired)
		g.cooldown.Record(id)
		return nil
	}

	if err := g.swarm.Scale(ctx, id, desired); err != nil {
		return fmt.Errorf("scale %s: %w", name, err)
	}
	g.cooldown.Record(id)
	g.logger.Info("scaled", "service", name, "from", svc.Replicas, "to", desired)
	return nil
}

// Heal force-updates a service to unstick its tasks, gated by dry-run + cooldown.
func (g *Guard) Heal(ctx context.Context, svc model.ManagedService) error {
	id, name := svc.Ref.ID, svc.Ref.Name
	if !g.cooldown.Allowed(id) {
		g.logger.Info("heal suppressed by cooldown", "service", name)
		return nil
	}

	if g.dryRun {
		g.logger.Info("dry-run: would force-update (heal)", "service", name)
		g.cooldown.Record(id)
		return nil
	}

	if err := g.swarm.ForceUpdate(ctx, id); err != nil {
		return fmt.Errorf("heal %s: %w", name, err)
	}
	g.cooldown.Record(id)
	g.logger.Info("healed (forced reschedule)", "service", name)
	return nil
}
