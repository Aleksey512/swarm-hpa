package rebalancer

import (
	"testing"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func node(id, name string, load float64, labels map[string]string) NodeState {
	return NodeState{
		View: model.NodeView{
			ID:           id,
			Name:         name,
			Availability: model.NodeAvailabilityActive,
			State:        model.NodeStateReady,
			Labels:       labels,
		},
		Load: load,
	}
}

func drained(id, name string, load float64) NodeState {
	n := node(id, name, load, nil)
	n.View.Availability = "drain"
	return n
}

func svc(id, name string, replicated, rebalance bool, constraints []string) model.ManagedService {
	return model.ManagedService{
		Ref:         model.ServiceRef{ID: id, Name: name},
		Replicated:  replicated,
		Rebalance:   rebalance,
		Constraints: constraints,
	}
}

func task(id, serviceID, nodeID string) model.TaskView {
	return model.TaskView{ID: id, ServiceID: serviceID, NodeID: nodeID}
}

const threshold = 0.30 // 30 percentage points

func TestRecommendMovesTaskOffHotNode(t *testing.T) {
	nodes := []NodeState{
		node("hot", "worker-hot", 70, nil),
		node("cold", "worker-cold", 20, nil),
	}
	services := []model.ManagedService{svc("web", "web", true, true, nil)}
	tasks := map[string][]model.TaskView{
		"hot":  {task("t1", "web", "hot")},
		"cold": {task("t2", "web", "cold")},
	}

	plan := Recommend(nodes, services, tasks, threshold)
	if len(plan.Moves) != 1 {
		t.Fatalf("want 1 move, got %d", len(plan.Moves))
	}
	m := plan.Moves[0]
	if m.ServiceID != "web" || m.FromNodeID != "hot" || m.ToNodeID != "cold" || m.TaskID != "t1" {
		t.Errorf("unexpected move: %+v", m)
	}
}

func TestRecommendBalancedClusterNoMove(t *testing.T) {
	nodes := []NodeState{node("a", "a", 55, nil), node("b", "b", 45, nil)} // spread 10 < 30
	services := []model.ManagedService{svc("web", "web", true, true, nil)}
	tasks := map[string][]model.TaskView{"a": {task("t1", "web", "a")}}

	if plan := Recommend(nodes, services, tasks, threshold); len(plan.Moves) != 0 {
		t.Errorf("balanced cluster must yield no moves, got %+v", plan.Moves)
	}
}

func TestRecommendThresholdBoundary(t *testing.T) {
	services := []model.ManagedService{svc("web", "web", true, true, nil)}
	tasks := map[string][]model.TaskView{"hot": {task("t1", "web", "hot")}}

	// spread exactly at threshold (30) → move
	at := []NodeState{node("hot", "hot", 70, nil), node("cold", "cold", 40, nil)}
	if plan := Recommend(at, services, tasks, threshold); len(plan.Moves) != 1 {
		t.Errorf("spread == threshold should move, got %d moves", len(plan.Moves))
	}
	// spread just below threshold (29) → no move
	below := []NodeState{node("hot", "hot", 70, nil), node("cold", "cold", 41, nil)}
	if plan := Recommend(below, services, tasks, threshold); len(plan.Moves) != 0 {
		t.Errorf("spread < threshold should not move, got %d moves", len(plan.Moves))
	}
}

func TestRecommendRespectsOptIn(t *testing.T) {
	nodes := []NodeState{node("hot", "hot", 80, nil), node("cold", "cold", 10, nil)}
	tasks := map[string][]model.TaskView{"hot": {task("t1", "web", "hot")}}

	// rebalance=false → not eligible
	notOptedIn := []model.ManagedService{svc("web", "web", true, false, nil)}
	if plan := Recommend(nodes, notOptedIn, tasks, threshold); len(plan.Moves) != 0 {
		t.Errorf("a service without rebalance opt-in must not move, got %+v", plan.Moves)
	}
	// global (non-replicated) → not eligible
	global := []model.ManagedService{svc("web", "web", false, true, nil)}
	if plan := Recommend(nodes, global, tasks, threshold); len(plan.Moves) != 0 {
		t.Errorf("a global service must not be rebalanced, got %+v", plan.Moves)
	}
}

func TestRecommendRespectsPlacementConstraints(t *testing.T) {
	// The hot node has a GPU task; the cold node is not a GPU node → no valid target.
	nodes := []NodeState{
		node("hot", "hot", 90, map[string]string{"tier": "gpu"}),
		node("cold", "cold", 10, map[string]string{"tier": "cpu"}),
	}
	services := []model.ManagedService{svc("ml", "ml", true, true, []string{"node.labels.tier==gpu"})}
	tasks := map[string][]model.TaskView{"hot": {task("t1", "ml", "hot")}}

	if plan := Recommend(nodes, services, tasks, threshold); len(plan.Moves) != 0 {
		t.Errorf("must not move to a node that violates constraints, got %+v", plan.Moves)
	}
}

func TestRecommendNoTaskOnHotNode(t *testing.T) {
	// Imbalance exists, but the opted-in service's task is on the COLD node.
	nodes := []NodeState{node("hot", "hot", 80, nil), node("cold", "cold", 10, nil)}
	services := []model.ManagedService{svc("web", "web", true, true, nil)}
	tasks := map[string][]model.TaskView{"cold": {task("t1", "web", "cold")}}

	if plan := Recommend(nodes, services, tasks, threshold); len(plan.Moves) != 0 {
		t.Errorf("nothing to relieve on the hot node, got %+v", plan.Moves)
	}
}

func TestRecommendNeedsTwoActiveNodes(t *testing.T) {
	services := []model.ManagedService{svc("web", "web", true, true, nil)}
	tasks := map[string][]model.TaskView{"hot": {task("t1", "web", "hot")}}

	// only one node
	one := []NodeState{node("hot", "hot", 90, nil)}
	if plan := Recommend(one, services, tasks, threshold); len(plan.Moves) != 0 {
		t.Errorf("a single node cannot be rebalanced, got %+v", plan.Moves)
	}
	// two nodes but the idle one is drained (not active) → not a valid target
	withDrained := []NodeState{node("hot", "hot", 90, nil), drained("cold", "cold", 5)}
	if plan := Recommend(withDrained, services, tasks, threshold); len(plan.Moves) != 0 {
		t.Errorf("a drained node is not a valid rebalance target, got %+v", plan.Moves)
	}
}

func TestRecommendPicksHottestAndColdest(t *testing.T) {
	// Three nodes: the move must relieve the hottest and target the coldest.
	nodes := []NodeState{
		node("mid", "mid", 50, nil),
		node("hot", "hot", 95, nil),
		node("cold", "cold", 5, nil),
	}
	services := []model.ManagedService{svc("web", "web", true, true, nil)}
	tasks := map[string][]model.TaskView{"hot": {task("t1", "web", "hot")}}

	plan := Recommend(nodes, services, tasks, threshold)
	if len(plan.Moves) != 1 {
		t.Fatalf("want 1 move, got %d", len(plan.Moves))
	}
	if plan.Moves[0].FromNodeID != "hot" || plan.Moves[0].ToNodeID != "cold" {
		t.Errorf("want hot->cold, got %s->%s", plan.Moves[0].FromNodeID, plan.Moves[0].ToNodeID)
	}
}
