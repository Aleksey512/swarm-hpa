package swarm

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/docker/docker/api/types/filters"
	dswarm "github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"

	"github.com/Aleksey512/swarm-hpa/internal/config"
	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// callTimeout bounds every Docker API call so a hung daemon cannot stall the loop.
const callTimeout = 10 * time.Second

// dockerAPI is the subset of the Docker client this adapter uses. Narrowing it
// to an interface lets tests substitute a fake without a live daemon.
type dockerAPI interface {
	ServiceList(ctx context.Context, options dswarm.ServiceListOptions) ([]dswarm.Service, error)
	ServiceInspectWithRaw(ctx context.Context, serviceID string, opts dswarm.ServiceInspectOptions) (dswarm.Service, []byte, error)
	ServiceUpdate(ctx context.Context, serviceID string, version dswarm.Version, service dswarm.ServiceSpec, options dswarm.ServiceUpdateOptions) (dswarm.ServiceUpdateResponse, error)
	TaskList(ctx context.Context, options dswarm.TaskListOptions) ([]dswarm.Task, error)
	NodeList(ctx context.Context, options dswarm.NodeListOptions) ([]dswarm.Node, error)
}

// Adapter implements the read methods of port.SwarmController over the Docker
// Go SDK. It depends inward on internal/core.
type Adapter struct {
	cli    dockerAPI
	logger *slog.Logger
}

// compile-time proof the adapter satisfies the core port.
var _ port.SwarmController = (*Adapter)(nil)

// New returns a read-only swarm adapter over the given Docker client.
func New(cli *client.Client, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{cli: cli, logger: logger}
}

// ManagedServices lists services opted into autoscaling (swarm.autoscaler.enabled
// =true), healing (swarm.autoscaler.heal=true), OR rebalancing
// (swarm.autoscaler.rebalance=true), parses each, and maps them to
// model.ManagedService. A service that opted in but is misconfigured is logged at
// WARN and skipped (never fatal).
//
// A single Docker label filter cannot OR values, so the three opt-in labels are
// queried separately and merged, deduped by service ID (a service may carry more
// than one).
func (a *Adapter) ManagedServices(ctx context.Context) ([]model.ManagedService, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	enabledSvcs, err := a.listByLabel(ctx, config.LabelEnabled+"=true")
	if err != nil {
		return nil, err
	}
	healSvcs, err := a.listByLabel(ctx, config.LabelHeal+"=true")
	if err != nil {
		return nil, err
	}
	rebalanceSvcs, err := a.listByLabel(ctx, config.LabelRebalance+"=true")
	if err != nil {
		return nil, err
	}

	total := len(enabledSvcs) + len(healSvcs) + len(rebalanceSvcs)
	seen := make(map[string]struct{}, total)
	managed := make([]model.ManagedService, 0, total)
	for _, group := range [][]dswarm.Service{enabledSvcs, healSvcs, rebalanceSvcs} {
		for _, svc := range group {
			if _, dup := seen[svc.ID]; dup {
				continue
			}
			seen[svc.ID] = struct{}{}
			ms, err := toManagedService(svc)
			if err != nil {
				a.logger.Warn("skipping misconfigured managed service",
					"service", svc.Spec.Name, "id", svc.ID, "err", err)
				continue
			}
			a.logger.Debug("observed managed service",
				"service", ms.Ref.Name, "replicas", ms.Replicas,
				"autoscale", ms.Autoscale, "heal", ms.Heal, "rebalance", ms.Rebalance,
				"min", ms.Policy.Min, "max", ms.Policy.Max, "metric", ms.Policy.Metric)
			managed = append(managed, ms)
		}
	}
	a.logger.Debug("managed services observed", "count", len(managed))
	return managed, nil
}

// listByLabel returns the services carrying an exact "key=value" label.
func (a *Adapter) listByLabel(ctx context.Context, label string) ([]dswarm.Service, error) {
	f := filters.NewArgs(filters.Arg("label", label))
	services, err := a.cli.ServiceList(ctx, dswarm.ServiceListOptions{Filters: f})
	if err != nil {
		return nil, fmt.Errorf("service list (%s): %w", label, err)
	}
	return services, nil
}

// Tasks returns the desired-state-running tasks of a single service.
func (a *Adapter) Tasks(ctx context.Context, serviceID string) ([]model.TaskView, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	f := filters.NewArgs(
		filters.Arg("service", serviceID),
		filters.Arg("desired-state", "running"),
	)
	tasks, err := a.cli.TaskList(ctx, dswarm.TaskListOptions{Filters: f})
	if err != nil {
		return nil, fmt.Errorf("task list (service %s): %w", serviceID, err)
	}

	views := make([]model.TaskView, 0, len(tasks))
	for _, t := range tasks {
		views = append(views, toTaskView(t))
	}
	return views, nil
}

// Nodes returns the cluster's nodes.
func (a *Adapter) Nodes(ctx context.Context) ([]model.NodeView, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	nodes, err := a.cli.NodeList(ctx, dswarm.NodeListOptions{})
	if err != nil {
		return nil, fmt.Errorf("node list: %w", err)
	}

	views := make([]model.NodeView, 0, len(nodes))
	for _, n := range nodes {
		views = append(views, toNodeView(n))
	}
	return views, nil
}

// toManagedService maps an SDK service to a model.ManagedService, parsing its
// policy from labels. It is pure (no client, no logging) so it is unit-testable.
func toManagedService(svc dswarm.Service) (model.ManagedService, error) {
	policy, autoscale, heal, rebalance, err := config.ParsePolicy(svc.Spec.Labels)
	if err != nil {
		return model.ManagedService{}, err
	}
	if !autoscale && !heal && !rebalance {
		return model.ManagedService{}, fmt.Errorf("not opted in (%s/%s/%s)",
			config.LabelEnabled, config.LabelHeal, config.LabelRebalance)
	}

	var replicas uint64
	replicated := false
	if svc.Spec.Mode.Replicated != nil {
		replicated = true
		if svc.Spec.Mode.Replicated.Replicas != nil {
			replicas = *svc.Spec.Mode.Replicated.Replicas
		}
	}

	var constraints []string
	if svc.Spec.TaskTemplate.Placement != nil {
		constraints = svc.Spec.TaskTemplate.Placement.Constraints
	}

	return model.ManagedService{
		Ref:         model.ServiceRef{ID: svc.ID, Name: svc.Spec.Name},
		Replicas:    replicas,
		Replicated:  replicated,
		Policy:      policy,
		Constraints: constraints,
		Autoscale:   autoscale,
		Heal:        heal,
		Rebalance:   rebalance,
	}, nil
}

// toTaskView maps an SDK task to a model.TaskView (pure).
func toTaskView(t dswarm.Task) model.TaskView {
	return model.TaskView{
		ID:           t.ID,
		ServiceID:    t.ServiceID,
		State:        string(t.Status.State),
		DesiredState: string(t.DesiredState),
		NodeID:       t.NodeID,
		Err:          t.Status.Err,
		Since:        t.Status.Timestamp,
	}
}

// toNodeView maps an SDK node to a model.NodeView (pure). Node spec labels
// (set via `docker node update --label-add`) are what placement constraints of
// the form `node.labels.<key>` match against, so they are carried through.
func toNodeView(n dswarm.Node) model.NodeView {
	return model.NodeView{
		ID:           n.ID,
		Name:         n.Description.Hostname,
		Availability: string(n.Spec.Availability),
		State:        string(n.Status.State),
		Labels:       n.Spec.Labels,
	}
}
