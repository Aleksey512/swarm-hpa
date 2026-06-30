package healer

import (
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func TestNodeSatisfies(t *testing.T) {
	node := model.NodeView{
		ID:           "n1",
		Name:         "host-1",
		Availability: model.NodeAvailabilityActive,
		State:        model.NodeStateReady,
		Labels:       map[string]string{"nodeNum": "1", "tier": "gpu"},
	}

	cases := []struct {
		name        string
		constraints []string
		want        bool
	}{
		{"label eq match", []string{"node.labels.nodeNum==1"}, true},
		{"label eq match spaced", []string{"node.labels.nodeNum == 1"}, true},
		{"label eq mismatch", []string{"node.labels.nodeNum==2"}, false},
		{"label eq missing label excludes", []string{"node.labels.zone==eu"}, false},
		{"label neq holds", []string{"node.labels.nodeNum!=2"}, true},
		{"label neq violated", []string{"node.labels.nodeNum!=1"}, false},
		{"label neq missing label holds", []string{"node.labels.zone!=eu"}, true},
		{"hostname eq", []string{"node.hostname==host-1"}, true},
		{"hostname mismatch", []string{"node.hostname==host-2"}, false},
		{"id eq", []string{"node.id==n1"}, true},
		{"unparseable skipped", []string{"node.labels.nodeNum"}, true},
		{"unknown key skipped", []string{"node.role==manager"}, true},
		{"engine labels skipped", []string{"engine.labels.foo==bar"}, true},
		{"multiple all satisfied", []string{"node.labels.nodeNum==1", "node.hostname==host-1"}, true},
		{"multiple one fails", []string{"node.labels.nodeNum==1", "node.hostname==host-2"}, false},
		{"unknown key never widens exclusion", []string{"node.role==worker", "node.labels.nodeNum==1"}, true},
		{"unknown key plus failing parseable still fails", []string{"node.role==worker", "node.labels.nodeNum==2"}, false},
		{"no constraints vacuously true", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nodeSatisfies(node, tc.constraints); got != tc.want {
				t.Errorf("nodeSatisfies(%v) = %v, want %v", tc.constraints, got, tc.want)
			}
		})
	}
}

// healer test fixtures.
var (
	// t0 is the moment the pending task entered pending.
	t0        = time.Unix(1_700_000_000, 0)
	threshold = 2 * time.Minute
	nowBeyond = t0.Add(3 * time.Minute) // pending duration above threshold
	nowBelow  = t0.Add(1 * time.Minute) // pending duration below threshold
)

func svcWith(constraints ...string) model.ManagedService {
	return model.ManagedService{
		Ref:         model.ServiceRef{ID: "s1", Name: "web"},
		Replicated:  true,
		Constraints: constraints,
		Policy:      model.ServicePolicy{Enabled: true, Min: 1, Max: 5, Metric: "cpu", Target: 80},
	}
}

func activeNode() model.NodeView {
	return model.NodeView{
		ID: "n1", Name: "host-1",
		Availability: model.NodeAvailabilityActive,
		State:        model.NodeStateReady,
		Labels:       map[string]string{"nodeNum": "1"},
	}
}

func downNode() model.NodeView {
	n := activeNode()
	n.State = "down" // node not yet recovered
	return n
}

func pendingSince(id string, since time.Time) model.TaskView {
	return model.TaskView{
		ID: id, ServiceID: "s1",
		State: model.TaskStatePending, DesiredState: model.TaskStateRunning,
		Since: since,
	}
}

func TestDetectStuckSignature(t *testing.T) {
	v := Detect(
		svcWith("node.labels.nodeNum==1"),
		[]model.TaskView{pendingSince("t9", t0)},
		[]model.NodeView{activeNode()},
		threshold, nowBeyond,
	)
	if !v.Stuck {
		t.Fatalf("expected stuck verdict, got %+v", v)
	}
	if v.TaskID != "t9" {
		t.Errorf("TaskID = %q, want t9", v.TaskID)
	}
	if v.NodeID != "n1" {
		t.Errorf("NodeID = %q, want n1", v.NodeID)
	}
}

func TestDetectNotStuck(t *testing.T) {
	cases := []struct {
		name  string
		svc   model.ManagedService
		tasks []model.TaskView
		nodes []model.NodeView
		now   time.Time
	}{
		{
			name:  "node still down",
			svc:   svcWith("node.labels.nodeNum==1"),
			tasks: []model.TaskView{pendingSince("t1", t0)},
			nodes: []model.NodeView{downNode()},
			now:   nowBeyond,
		},
		{
			name:  "pending below threshold",
			svc:   svcWith("node.labels.nodeNum==1"),
			tasks: []model.TaskView{pendingSince("t1", t0)},
			nodes: []model.NodeView{activeNode()},
			now:   nowBelow,
		},
		{
			name:  "no placement constraints",
			svc:   svcWith(),
			tasks: []model.TaskView{pendingSince("t1", t0)},
			nodes: []model.NodeView{activeNode()},
			now:   nowBeyond,
		},
		{
			name: "task running not pending",
			svc:  svcWith("node.labels.nodeNum==1"),
			tasks: []model.TaskView{{
				ID: "t1", State: model.TaskStateRunning, DesiredState: model.TaskStateRunning,
			}},
			nodes: []model.NodeView{activeNode()},
			now:   nowBeyond,
		},
		{
			name:  "active node does not satisfy constraint",
			svc:   svcWith("node.labels.nodeNum==2"),
			tasks: []model.TaskView{pendingSince("t1", t0)},
			nodes: []model.NodeView{activeNode()},
			now:   nowBeyond,
		},
		{
			name:  "no nodes at all",
			svc:   svcWith("node.labels.nodeNum==1"),
			tasks: []model.TaskView{pendingSince("t1", t0)},
			nodes: nil,
			now:   nowBeyond,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Detect(tc.svc, tc.tasks, tc.nodes, threshold, tc.now)
			if v.Stuck {
				t.Errorf("expected not-stuck, got stuck verdict: %+v", v)
			}
			if v.Reason == "" {
				t.Error("a not-stuck verdict must carry a reason")
			}
		})
	}
}
