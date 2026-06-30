package reconciler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/autoscaler"
	"github.com/Aleksey512/swarm-hpa/internal/core/healer"
	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// Reconciler runs the periodic control loop. It observes Swarm (read-only) and
// routes stuck-pending services through the Guard (which suppresses mutations
// while dry-run is enabled). Scaling decisions arrive in a later milestone.
type Reconciler struct {
	swarm         port.SwarmController
	metrics       port.MetricsProvider
	guard         *Guard
	clock         port.Clock
	healThreshold time.Duration
	recorder      port.Recorder
	stabilizer    *Stabilizer
	maxStep       uint64
	logger        *slog.Logger
}

// New constructs a Reconciler. healThreshold is the minimum time a task must be
// pending before the healer treats the service as stuck; stabilizer dampens
// scale-downs and maxStep caps a scaling action's magnitude (0 = unlimited). A
// nil recorder falls back to a no-op, a nil stabilizer to a disabled one, a nil
// logger to slog.Default, and a nil clock to the system clock.
func New(swarm port.SwarmController, metrics port.MetricsProvider, guard *Guard, clock port.Clock, healThreshold time.Duration, recorder port.Recorder, stabilizer *Stabilizer, maxStep uint64, logger *slog.Logger) *Reconciler {
	if logger == nil {
		logger = slog.Default()
	}
	if clock == nil {
		clock = port.SystemClock{}
	}
	if recorder == nil {
		recorder = port.NopRecorder{}
	}
	if stabilizer == nil {
		stabilizer = NewStabilizer(0)
	}
	return &Reconciler{
		swarm:         swarm,
		metrics:       metrics,
		guard:         guard,
		clock:         clock,
		healThreshold: healThreshold,
		recorder:      recorder,
		stabilizer:    stabilizer,
		maxStep:       maxStep,
		logger:        logger,
	}
}

// Run observes Swarm immediately and then on every interval tick, until ctx is
// cancelled (a graceful stop, returning nil). A single observation error never
// stops the loop.
func (r *Reconciler) Run(ctx context.Context, interval time.Duration) error {
	r.logger.Info("reconcile loop started", "interval", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	r.observe(ctx)
	for {
		select {
		case <-ctx.Done():
			r.logger.Info("reconcile loop stopping", "reason", ctx.Err())
			return nil
		case <-ticker.C:
			r.observe(ctx)
		}
	}
}

// observe lists managed services and their tasks, logs the picture, and routes
// genuinely stuck-pending services through Guard.Heal — at most once per service
// per tick. The stuck decision is the precise healer.Detect signature (placement
// constraints + long-pending task + recovered constraint-satisfying node). With
// dry-run enabled (the default), Heal only logs the intended action. Errors are
// logged and swallowed so the loop survives transient API failures.
func (r *Reconciler) observe(ctx context.Context) {
	defer r.recorder.ReconcileTick()

	services, err := r.swarm.ManagedServices(ctx)
	if err != nil {
		r.logger.Error("observe: listing managed services failed", "err", err)
		r.recorder.Error("services")
		return
	}
	r.logger.Info("observed managed services", "count", len(services))
	r.recorder.ObservedServices(len(services))

	// List nodes once per tick: healer.Detect needs them to check whether a
	// constraint-satisfying node has recovered. On error we conservatively
	// disable healing for this tick (nil nodes => Detect finds no Active node)
	// while still allowing scaling to proceed.
	nodes, err := r.swarm.Nodes(ctx)
	if err != nil {
		r.logger.Error("observe: listing nodes failed; healing disabled this tick", "err", err)
		r.recorder.Error("nodes")
		nodes = nil
	}
	now := r.clock.Now()

	flaggedForHeal := 0
	for _, svc := range services {
		tasks, err := r.swarm.Tasks(ctx, svc.Ref.ID)
		if err != nil {
			r.logger.Error("observe: listing tasks failed", "service", svc.Ref.Name, "err", err)
			r.recorder.Error("tasks")
			continue
		}
		pending, running := countStates(tasks)
		r.logger.Debug("observed service",
			"service", svc.Ref.Name,
			"replicas", svc.Replicas,
			"running", running,
			"pending", pending,
			"min", svc.Policy.Min,
			"max", svc.Policy.Max,
			"metric", svc.Policy.Metric,
		)

		// Read the metric, turn it into a desired replica count, and apply it
		// through the Guard (which enforces dry-run + cooldown + no-op).
		// Missing data is normal, not an error.
		if val, err := r.metrics.Value(ctx, svc); err == nil {
			// Desired (proportional + tolerance) → stabilize (dampen scale-down)
			// → ClampStep (cap magnitude) → guard (dry-run + per-direction cooldown).
			desired := autoscaler.Desired(svc.Replicas, val, svc.Policy)
			stabilized := r.stabilizer.Recommend(svc.Ref.ID, svc.Replicas, desired, now)
			final := autoscaler.ClampStep(svc.Replicas, stabilized, r.maxStep)
			r.logger.Info("scaling decision",
				"service", svc.Ref.Name, "metric", svc.Policy.Metric,
				"value", val, "target", svc.Policy.Target,
				"current", svc.Replicas, "desired", desired,
				"stabilized", stabilized, "final", final)
			if err := r.guard.Scale(ctx, svc, final); err != nil {
				r.logger.Error("scale failed", "service", svc.Ref.Name, "err", err)
			}
		} else if errors.Is(err, model.ErrNoMetricData) {
			r.logger.Debug("no metric data (skipping scale)", "service", svc.Ref.Name)
		} else {
			r.logger.Error("metric read failed", "service", svc.Ref.Name, "err", err)
			r.recorder.Error("metric")
		}

		// Precise stuck-pending detection (moby/moby#42215): heal only when the
		// full signature holds — constraints present, a task pending beyond the
		// threshold, and a constraint-satisfying node now Active+Ready. Heal the
		// service ONCE per tick, never once per pending task.
		verdict := healer.Detect(svc, tasks, nodes, r.healThreshold, now)
		if !verdict.Stuck {
			r.logger.Debug("not stuck (skipping heal)", "service", svc.Ref.Name, "reason", verdict.Reason)
			continue
		}
		r.logger.Warn("stuck-pending detected",
			"service", svc.Ref.Name, "task", verdict.TaskID,
			"node", verdict.NodeID, "reason", verdict.Reason)
		if err := r.guard.Heal(ctx, svc); err != nil {
			r.logger.Error("heal failed", "service", svc.Ref.Name, "err", err)
			continue
		}
		flaggedForHeal++
	}
	if flaggedForHeal > 0 {
		r.logger.Debug("services routed through heal", "count", flaggedForHeal)
	}
}

func countStates(tasks []model.TaskView) (pending, running int) {
	for _, t := range tasks {
		switch t.State {
		case model.TaskStatePending:
			pending++
		case model.TaskStateRunning:
			running++
		}
	}
	return pending, running
}
