package reconciler

import (
	"context"
	"testing"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// rebalCtrl is a SwarmController that presents one rebalance-opted service whose
// task sits on the hot node, plus two active nodes. ForceUpdate calls are counted.
type rebalCtrl struct {
	forceCalls int
	svc        model.ManagedService
	tasks      []model.TaskView
	nodes      []model.NodeView
}

func (c *rebalCtrl) ManagedServices(context.Context) ([]model.ManagedService, error) {
	return []model.ManagedService{c.svc}, nil
}
func (c *rebalCtrl) Tasks(context.Context, string) ([]model.TaskView, error) { return c.tasks, nil }
func (c *rebalCtrl) Nodes(context.Context) ([]model.NodeView, error)         { return c.nodes, nil }
func (c *rebalCtrl) Scale(context.Context, string, uint64) error             { return nil }
func (c *rebalCtrl) ForceUpdate(context.Context, string) error               { c.forceCalls++; return nil }

type fakeLoads struct{ reports []model.AgentReport }

func (f fakeLoads) Snapshot() []model.AgentReport { return f.reports }

func activeNode(id string) model.NodeView {
	return model.NodeView{ID: id, Name: id, Availability: model.NodeAvailabilityActive, State: model.NodeStateReady}
}

// skewedCluster returns a controller with a rebalance-only service on the hot
// node, plus a load source reporting an 80/10 CPU skew across two active nodes.
func skewedCluster() (*rebalCtrl, fakeLoads) {
	ctrl := &rebalCtrl{
		svc: model.ManagedService{
			Ref:        model.ServiceRef{ID: "s1", Name: "web"},
			Replicas:   2,
			Replicated: true,
			Rebalance:  true,
		},
		tasks: []model.TaskView{{ID: "t1", ServiceID: "s1", NodeID: "hot"}},
		nodes: []model.NodeView{activeNode("hot"), activeNode("cold")},
	}
	loads := fakeLoads{reports: []model.AgentReport{
		{NodeID: "hot", Node: model.NodeLoad{CPUPercent: 80}},
		{NodeID: "cold", Node: model.NodeLoad{CPUPercent: 10}},
	}}
	return ctrl, loads
}

func newRebalanceReconciler(ctrl *rebalCtrl, loads LoadSource, fr *fakeRecorder, dryRun bool) *Reconciler {
	guard := NewGuard(ctrl, NewCooldown(newFakeClock()), Cooldowns{}, dryRun, fr, discardLogger())
	return New(ctrl, fakeProvider{err: model.ErrNoMetricData}, guard, newFakeClock(), testHealThreshold,
		fr, nil, 0, discardLogger(), WithRebalancing(loads, 0.30))
}

func TestReconcilerRebalanceDryRunLogsButDoesNotMutate(t *testing.T) {
	ctrl, loads := skewedCluster()
	fr := &fakeRecorder{}
	rec := newRebalanceReconciler(ctrl, loads, fr, true) // dry-run

	rec.observe(context.Background())

	if ctrl.forceCalls != 0 {
		t.Errorf("dry-run must not force-update, got %d", ctrl.forceCalls)
	}
	if !contains(fr.suppressed, "rebalance:dry_run") {
		t.Errorf("suppressed = %v, want rebalance:dry_run (recommendation reached the guard)", fr.suppressed)
	}
}

func TestReconcilerRebalanceAppliesWhenEnabled(t *testing.T) {
	ctrl, loads := skewedCluster()
	fr := &fakeRecorder{}
	rec := newRebalanceReconciler(ctrl, loads, fr, false) // apply

	rec.observe(context.Background())

	if ctrl.forceCalls != 1 {
		t.Errorf("skewed cluster must trigger exactly one force-update, got %d", ctrl.forceCalls)
	}
	if len(fr.rebalances) != 1 || fr.rebalances[0] != "web" {
		t.Errorf("rebalances = %v, want [web]", fr.rebalances)
	}
}

func TestReconcilerRebalanceDisabledWithoutLoadSource(t *testing.T) {
	ctrl, _ := skewedCluster()
	fr := &fakeRecorder{}
	// No WithRebalancing → r.loads is nil → the rebalance branch is skipped.
	guard := NewGuard(ctrl, NewCooldown(newFakeClock()), Cooldowns{}, false, fr, discardLogger())
	rec := New(ctrl, fakeProvider{err: model.ErrNoMetricData}, guard, newFakeClock(), testHealThreshold,
		fr, nil, 0, discardLogger())

	rec.observe(context.Background())

	if ctrl.forceCalls != 0 {
		t.Errorf("rebalancing must be off without a load source, got %d force calls", ctrl.forceCalls)
	}
}

func TestReconcilerRebalanceBalancedClusterNoMutation(t *testing.T) {
	ctrl, _ := skewedCluster()
	balanced := fakeLoads{reports: []model.AgentReport{
		{NodeID: "hot", Node: model.NodeLoad{CPUPercent: 55}},
		{NodeID: "cold", Node: model.NodeLoad{CPUPercent: 45}}, // spread 10 < 30
	}}
	fr := &fakeRecorder{}
	rec := newRebalanceReconciler(ctrl, balanced, fr, false)

	rec.observe(context.Background())

	if ctrl.forceCalls != 0 {
		t.Errorf("a balanced cluster must not rebalance, got %d force calls", ctrl.forceCalls)
	}
}
