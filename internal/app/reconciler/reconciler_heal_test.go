package reconciler

import (
	"context"
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// healFake serves reads (one constrained service, its tasks, and the cluster
// nodes) and counts ForceUpdate calls, so heal decisions and per-service dedup
// can be asserted without a live daemon.
type healFake struct {
	svc        model.ManagedService
	tasks      []model.TaskView
	nodes      []model.NodeView
	forceCalls int
}

func (h *healFake) ManagedServices(context.Context) ([]model.ManagedService, error) {
	return []model.ManagedService{h.svc}, nil
}
func (h *healFake) Tasks(context.Context, string) ([]model.TaskView, error) { return h.tasks, nil }
func (h *healFake) Nodes(context.Context) ([]model.NodeView, error)         { return h.nodes, nil }
func (h *healFake) Scale(context.Context, string, uint64) error             { return nil }
func (h *healFake) ForceUpdate(context.Context, string) error {
	h.forceCalls++
	return nil
}

// healReconciler wires a Reconciler around a healFake with the given clock, heal
// threshold and dry-run setting; cooldown is disabled and metrics report no data.
func healReconciler(hf *healFake, clk *fakeClock, threshold time.Duration, dryRun bool) *Reconciler {
	logger := discardLogger()
	guard := NewGuard(hf, NewCooldown(0, clk), dryRun, logger)
	return New(hf, fakeProvider{err: model.ErrNoMetricData}, guard, clk, threshold, logger)
}

func constrainedSvc() model.ManagedService {
	return model.ManagedService{
		Ref:         model.ServiceRef{ID: "s1", Name: "web"},
		Replicated:  true,
		Constraints: []string{"node.labels.nodeNum==1"},
		Policy:      model.ServicePolicy{Enabled: true, Min: 1, Max: 5, Metric: "cpu", Target: 80},
	}
}

func activeNodeN1() model.NodeView {
	return model.NodeView{
		ID: "n1", Name: "host-1",
		Availability: model.NodeAvailabilityActive,
		State:        model.NodeStateReady,
		Labels:       map[string]string{"nodeNum": "1"},
	}
}

func downNodeN1() model.NodeView {
	n := activeNodeN1()
	n.State = "down"
	return n
}

func TestObserveHealDecision(t *testing.T) {
	clk := newFakeClock()
	// pending builds a pending task that entered pending `age` before now.
	pending := func(id string, age time.Duration) model.TaskView {
		return model.TaskView{
			ID: id, ServiceID: "s1",
			State: model.TaskStatePending, DesiredState: model.TaskStateRunning,
			Since: clk.now.Add(-age),
		}
	}
	running := model.TaskView{ID: "r1", ServiceID: "s1", State: model.TaskStateRunning, DesiredState: model.TaskStateRunning}

	noConstraintSvc := constrainedSvc()
	noConstraintSvc.Constraints = nil

	const threshold = 2 * time.Minute
	const longPending = 10 * time.Minute
	const shortPending = 30 * time.Second

	cases := []struct {
		name   string
		svc    model.ManagedService
		tasks  []model.TaskView
		nodes  []model.NodeView
		dryRun bool
		want   int
	}{
		{
			name:  "active recovered node + long pending heals once",
			svc:   constrainedSvc(),
			tasks: []model.TaskView{pending("t1", longPending)},
			nodes: []model.NodeView{activeNodeN1()},
			want:  1,
		},
		{
			name:  "three pending tasks heal the service only once",
			svc:   constrainedSvc(),
			tasks: []model.TaskView{pending("t1", longPending), pending("t2", longPending), pending("t3", longPending)},
			nodes: []model.NodeView{activeNodeN1()},
			want:  1,
		},
		{
			name:  "node still down does not heal",
			svc:   constrainedSvc(),
			tasks: []model.TaskView{pending("t1", longPending)},
			nodes: []model.NodeView{downNodeN1()},
			want:  0,
		},
		{
			name:   "dry-run suppresses the force-update",
			svc:    constrainedSvc(),
			tasks:  []model.TaskView{pending("t1", longPending)},
			nodes:  []model.NodeView{activeNodeN1()},
			dryRun: true,
			want:   0,
		},
		{
			name:  "service without constraints does not heal",
			svc:   noConstraintSvc,
			tasks: []model.TaskView{pending("t1", longPending)},
			nodes: []model.NodeView{activeNodeN1()},
			want:  0,
		},
		{
			name:  "pending below threshold does not heal",
			svc:   constrainedSvc(),
			tasks: []model.TaskView{pending("t1", shortPending)},
			nodes: []model.NodeView{activeNodeN1()},
			want:  0,
		},
		{
			name:  "running task does not heal",
			svc:   constrainedSvc(),
			tasks: []model.TaskView{running},
			nodes: []model.NodeView{activeNodeN1()},
			want:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hf := &healFake{svc: tc.svc, tasks: tc.tasks, nodes: tc.nodes}
			rec := healReconciler(hf, clk, threshold, tc.dryRun)
			rec.observe(context.Background())
			if hf.forceCalls != tc.want {
				t.Errorf("forceCalls = %d, want %d", hf.forceCalls, tc.want)
			}
		})
	}
}
