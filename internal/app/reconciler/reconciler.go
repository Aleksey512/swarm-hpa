package reconciler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	apptaskerrors "github.com/Aleksey512/swarm-hpa/internal/app/taskerrors"
	"github.com/Aleksey512/swarm-hpa/internal/core/autoscaler"
	"github.com/Aleksey512/swarm-hpa/internal/core/healer"
	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
	"github.com/Aleksey512/swarm-hpa/internal/core/rebalancer"
	coretaskerrors "github.com/Aleksey512/swarm-hpa/internal/core/taskerrors"
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
	tickSource    TickSource
	customTick    bool

	// Rebalancing (optional; enabled via WithRebalancing). loads is nil when no
	// agent fleet is wired, which disables the rebalance branch entirely.
	loads              LoadSource
	rebalanceThreshold float64

	// Task-error observation (optional; enabled via WithTaskErrors). A nil
	// errorSink disables the whole branch — the loop then never issues the
	// cluster-wide AllTasks/AllServices reads. errWindow is how long a recorded
	// error stays in the sliding window (also used for the per-tick gauge
	// snapshot, so the metric and any alert read the same window).
	errorSink *apptaskerrors.Tracker
	errRead   port.SwarmRead
	errWindow time.Duration
}

// New constructs a Reconciler. healThreshold is the minimum time a task must be
// pending before the healer treats the service as stuck; stabilizer dampens
// scale-downs and maxStep caps a scaling action's magnitude (0 = unlimited). A
// nil recorder falls back to a no-op, a nil stabilizer to a disabled one, a nil
// logger to slog.Default, and a nil clock to the system clock. Optional opts
// (e.g. WithTickSource) are applied last so they can override defaults.
func New(swarm port.SwarmController, metrics port.MetricsProvider, guard *Guard, clock port.Clock, healThreshold time.Duration, recorder port.Recorder, stabilizer *Stabilizer, maxStep uint64, logger *slog.Logger, opts ...Option) *Reconciler {
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
	r := &Reconciler{
		swarm:         swarm,
		metrics:       metrics,
		guard:         guard,
		clock:         clock,
		healThreshold: healThreshold,
		recorder:      recorder,
		stabilizer:    stabilizer,
		maxStep:       maxStep,
		logger:        logger,
		tickSource:    defaultTickSource,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Run observes Swarm immediately and then on every interval tick, until ctx is
// cancelled (a graceful stop, returning nil). A single observation error never
// stops the loop.
func (r *Reconciler) Run(ctx context.Context, interval time.Duration) error {
	r.logger.Info("reconcile loop started", "interval", interval)
	if r.customTick {
		r.logger.Debug("custom tick source injected (non-default)")
	}

	ticks, stop := r.tickSource(interval)
	defer stop()

	r.observe(ctx)
	for {
		select {
		case <-ctx.Done():
			r.logger.Info("reconcile loop stopping", "reason", ctx.Err())
			return nil
		case <-ticks:
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

	// Task-error observation — cluster-wide, once per tick, before the
	// per-service loop. Pure observation: classify every erroring task and
	// feed the sliding window the metrics exporter and the post-deploy GitOps
	// alert read. A read failure degrades this feature for the tick only.
	r.observeTaskErrors(ctx)

	// tasksByNode collects rebalance-eligible services' tasks so the cluster-level
	// rebalance decision (after the per-service loop) can find a task to move off a
	// hot node. Only populated for opted-in services.
	tasksByNode := make(map[string][]model.TaskView)

	flaggedForHeal := 0
	for _, svc := range services {
		tasks, err := r.swarm.Tasks(ctx, svc.Ref.ID)
		if err != nil {
			r.logger.Error("observe: listing tasks failed", "service", svc.Ref.Name, "err", err)
			r.recorder.Error("tasks")
			continue
		}
		if svc.Rebalance {
			for _, t := range tasks {
				if t.NodeID != "" {
					tasksByNode[t.NodeID] = append(tasksByNode[t.NodeID], t)
				}
			}
		}
		pending, running := countStates(tasks)
		r.recorder.ServicePendingTasks(svc.Ref.Name, pending)
		for action, remaining := range r.guard.CooldownRemaining(svc.Ref.ID, now) {
			r.recorder.ServiceCooldown(svc.Ref.Name, action, remaining > 0, remaining)
		}
		r.logger.Debug("observed service",
			"service", svc.Ref.Name,
			"replicas", svc.Replicas,
			"running", running,
			"pending", pending,
			"autoscale", svc.Autoscale,
			"heal", svc.Heal,
			"min", svc.Policy.Min,
			"max", svc.Policy.Max,
			"metric", svc.Policy.Metric,
		)

		// Autoscaling branch — only for services opted into scaling. Read the
		// metric, turn it into a desired replica count, and apply it through the
		// Guard (dry-run + cooldown + no-op). Missing data is normal, not an error.
		// Heal-only services skip this entirely (no pointless metric reads).
		if svc.Autoscale {
			if val, err := r.metrics.Value(ctx, svc); err == nil {
				// Desired (proportional + tolerance) → stabilize (dampen scale-down)
				// → ClampStep (cap magnitude) → guard (dry-run + per-direction cooldown).
				desired := autoscaler.Desired(svc.Replicas, val, svc.Policy)
				stabilized := r.stabilizer.Recommend(svc.Ref.ID, svc.Replicas, desired, now)
				final := autoscaler.ClampStep(svc.Replicas, stabilized, r.maxStep)
				decision := "hold"
				switch {
				case final > svc.Replicas:
					decision = "scale_up"
				case final < svc.Replicas:
					decision = "scale_down"
				}
				r.recorder.ServiceDecision(svc.Ref.Name, svc.Replicas, final, val, decision)
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
		} else {
			r.logger.Debug("autoscale disabled (heal-only); skipping metric read", "service", svc.Ref.Name)
		}

		// Healing branch — only for services opted into healing (the heal label, or
		// an autoscaled service that has not opted out via heal=false). Precise
		// stuck-pending detection (moby/moby#42215): heal only when the full
		// signature holds — constraints present, a task pending beyond the
		// threshold, and a constraint-satisfying node now Active+Ready. Heal the
		// service ONCE per tick, never once per pending task.
		if !svc.Heal {
			r.logger.Debug("heal disabled; skipping heal", "service", svc.Ref.Name)
			continue
		}
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

	r.rebalance(ctx, services, tasksByNode, nodes)
}

// observeTaskErrors is the cluster-wide task-error observation pass: one
// unfiltered TaskList, one unfiltered ServiceList (for the ServiceID→name
// join), classify every task carrying a status error, and record the events
// in the sliding-window tracker. It is a no-op when the feature is not wired,
// and a WARN (never a crash) when the reads fail — the loop's resilience
// contract.
func (r *Reconciler) observeTaskErrors(ctx context.Context) {
	if r.errorSink == nil || r.errRead == nil {
		return
	}
	tasks, err := r.errRead.AllTasks(ctx)
	if err != nil {
		r.logger.Warn("task errors: cluster task list failed; skipping this tick", "err", err)
		r.recorder.Error("tasks")
		return
	}
	services, err := r.errRead.AllServices(ctx)
	if err != nil {
		r.logger.Warn("task errors: cluster service list failed; skipping this tick", "err", err)
		r.recorder.Error("services")
		return
	}

	events, byClass := joinTaskEvents(tasks, services)
	if len(events) > 0 {
		r.errorSink.Record(events)
		r.logger.Info("task errors observed",
			"erroring_tasks", len(events),
			"vxlan_file_exists", byClass[coretaskerrors.ClassVxlanFileExists],
			"network_sandbox_join_failed", byClass[coretaskerrors.ClassNetworkSandboxJoin],
			"other", byClass[coretaskerrors.ClassOther])
	}

	// Publish the windowed per-service/class gauge every tick — including the
	// zero-error case, so services whose errors aged out get their series
	// deleted (see Recorder.TaskErrorsWindow) instead of lingering.
	snapshot := r.errorSink.WindowSnapshot(r.clock.Now(), r.errWindow)
	for service, classes := range snapshot {
		for class, n := range classes {
			r.recorder.TaskErrorsWindow(service, class, n)
		}
	}
	if len(events) == 0 {
		r.logger.Debug("task errors: none observed", "tasks_scanned", len(tasks))
	}
}

// joinTaskEvents maps erroring tasks to classified events, joining task
// ServiceIDs to service names via the cluster-wide service listing (one map
// build — no per-task API calls). A task whose service vanished from the
// listing keeps the raw ServiceID as its name: its window entry is about to
// age out anyway, and dropping it would hide a just-deleted service's errors.
func joinTaskEvents(tasks []model.TaskView, services []model.LiveService) ([]model.TaskErrorEvent, map[coretaskerrors.Class]int) {
	idToName := make(map[string]string, len(services))
	for _, svc := range services {
		idToName[svc.ID] = svc.Name
	}

	var events []model.TaskErrorEvent
	byClass := make(map[coretaskerrors.Class]int)
	for _, t := range tasks {
		if t.Err == "" {
			continue
		}
		name := t.ServiceID
		if n, ok := idToName[t.ServiceID]; ok {
			name = n
		}
		class := coretaskerrors.Classify(t.Err)
		byClass[class]++
		events = append(events, model.TaskErrorEvent{
			ServiceID:   t.ServiceID,
			ServiceName: name,
			Slot:        t.Slot,
			TaskID:      t.ID,
			Class:       string(class),
			Since:       t.Since,
			Err:         t.Err,
		})
	}
	return events, byClass
}

// rebalance runs the cluster-level, load-aware rebalance decision once per tick
// and routes any recommended move through the Guard (opt-in + dry-run + long
// cooldown). It is a no-op when rebalancing is not wired (no agent fleet) or no
// service opted in. The recommendation is always logged, even in dry-run.
func (r *Reconciler) rebalance(ctx context.Context, services []model.ManagedService, tasksByNode map[string][]model.TaskView, nodes []model.NodeView) {
	if r.loads == nil {
		return // rebalancing not enabled (no agent registry wired)
	}
	if !anyRebalanceOptIn(services) {
		return
	}

	states := buildNodeStates(nodes, r.loads.Snapshot())
	plan := rebalancer.Recommend(states, services, tasksByNode, r.rebalanceThreshold)
	if len(plan.Moves) == 0 {
		r.logger.Debug("rebalance: no move recommended", "nodes_with_load", len(states))
		return
	}

	byID := make(map[string]model.ManagedService, len(services))
	for _, svc := range services {
		byID[svc.Ref.ID] = svc
	}
	for _, m := range plan.Moves {
		svc, ok := byID[m.ServiceID]
		if !ok {
			continue
		}
		r.logger.Info("rebalance recommendation",
			"service", m.ServiceName, "from_node", m.FromNodeID, "to_node", m.ToNodeID,
			"from_load", m.FromLoad, "to_load", m.ToLoad, "reason", m.Reason)
		if err := r.guard.Rebalance(ctx, svc, m.FromNodeID, m.ToNodeID); err != nil {
			r.logger.Error("rebalance failed", "service", m.ServiceName, "err", err)
		}
	}
}

// buildNodeStates joins each node's placement view with its latest reported load
// (CPU%, the primary load signal). Only nodes with a live agent report are
// included — a node without agent data has unknown load and is never assumed idle.
func buildNodeStates(nodes []model.NodeView, reports []model.AgentReport) []rebalancer.NodeState {
	load := make(map[string]float64, len(reports))
	for _, rep := range reports {
		load[rep.NodeID] = rep.Node.CPUPercent
	}
	states := make([]rebalancer.NodeState, 0, len(nodes))
	for _, n := range nodes {
		if l, ok := load[n.ID]; ok {
			states = append(states, rebalancer.NodeState{View: n, Load: l})
		}
	}
	return states
}

// anyRebalanceOptIn reports whether any managed service opted into rebalancing.
func anyRebalanceOptIn(services []model.ManagedService) bool {
	for _, svc := range services {
		if svc.Rebalance {
			return true
		}
	}
	return false
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
