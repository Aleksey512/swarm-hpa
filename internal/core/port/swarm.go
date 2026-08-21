package port

import (
	"context"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// SwarmController is the core's interface to Docker Swarm. It currently exposes
// only read operations; mutation methods (scale, force-update) are added in
// later milestones. The swarm adapter implements it, and the core depends only
// on this interface — never on the Docker SDK directly.
type SwarmController interface {
	// ManagedServices returns the services opted in via swarm.autoscaler.*
	// labels, each with its parsed policy and current state.
	ManagedServices(ctx context.Context) ([]model.ManagedService, error)

	// Tasks returns the desired-state-running tasks of a single service.
	Tasks(ctx context.Context, serviceID string) ([]model.TaskView, error)

	// Nodes returns the cluster's nodes.
	Nodes(ctx context.Context) ([]model.NodeView, error)

	// Scale sets a replicated service's desired replica count. Raw mutation —
	// callers must route through the guarded path (dry-run + cooldown).
	Scale(ctx context.Context, serviceID string, replicas uint64) error

	// ForceUpdate reschedules a service's tasks without a spec change (the SDK
	// equivalent of `docker service update --force`). Raw mutation — gate it.
	ForceUpdate(ctx context.Context, serviceID string) error
}

// SwarmRead is the cluster-wide, unfiltered read surface: everything the
// observability features (task-error monitor, orphan detection) need. Split
// from SwarmController so read-only consumers depend on exactly what they use
// and never see the mutation methods. The swarm adapter implements it.
type SwarmRead interface {
	// AllTasks returns every task in the cluster, unfiltered by service or
	// desired state. Unlike SwarmController.Tasks (per-service,
	// desired-state=running), this surfaces superseded/rejected tasks — the
	// ones carrying the task status errors the error monitor classifies.
	AllTasks(ctx context.Context) ([]model.TaskView, error)

	// AllServices returns every service in the cluster, unfiltered by label.
	// Unlike ManagedServices (autoscaler opt-in) and StackServices (one
	// stack's namespace), this is the cluster-wide view orphan detection
	// needs.
	AllServices(ctx context.Context) ([]model.LiveService, error)
}
