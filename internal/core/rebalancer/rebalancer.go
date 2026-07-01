// Package rebalancer decides, purely, whether load is skewed enough across nodes
// to warrant moving a task — the load-aware rebalancing Swarm lacks (its
// scheduler spreads by task count, not load). Like core/healer it is a
// deterministic function of its inputs: no I/O, no clock, no mutation. It only
// RECOMMENDS; the app layer's guarded path decides whether and how to act.
//
// It is deliberately conservative for an MVP: it proposes at most one move per
// call — relieving the single busiest node by shifting one opted-in service's
// task to the idlest node that can actually run it (placement constraints
// respected). A long cooldown and dry-run default (enforced by the caller) keep
// the disruptive force-reschedule mechanism in check.
package rebalancer

import (
	"fmt"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/placement"
)

// NodeState pairs a node's identity/placement view with its current load metric
// (0..100, percent). The caller supplies only nodes for which a live agent load
// is known — a node without agent data is omitted, never assumed idle.
type NodeState struct {
	View model.NodeView
	Load float64
}

// Move is a single recommended task relocation.
type Move struct {
	ServiceID   string
	ServiceName string
	TaskID      string  // the specific task on the hot node (informational)
	FromNodeID  string  // busiest node
	ToNodeID    string  // idlest node that can run the service
	FromLoad    float64 // hot node load, percent
	ToLoad      float64 // cold node load, percent
	Reason      string
}

// Plan is the rebalancer's recommendation (zero or one move in this MVP).
type Plan struct {
	Moves []Move
}

// Recommend returns a rebalance plan. threshold is the node-load spread, as a
// fraction in (0,1], at or above which a move is proposed (e.g. 0.30 = 30
// percentage points between the busiest and idlest node). Only replicated
// services carrying the rebalance opt-in are eligible; a global service already
// runs on every node, so moving it is meaningless.
func Recommend(nodes []NodeState, services []model.ManagedService, tasksByNode map[string][]model.TaskView, threshold float64) Plan {
	active := activeNodes(nodes)
	if len(active) < 2 {
		return Plan{} // need at least two comparable nodes
	}

	hot, cold := extremes(active)
	spread := hot.Load - cold.Load
	if spread < threshold*100 {
		return Plan{} // balanced enough
	}

	for _, svc := range services {
		if !svc.Rebalance || !svc.Replicated {
			continue
		}
		taskID, ok := firstTaskOf(tasksByNode[hot.View.ID], svc.Ref.ID)
		if !ok {
			continue // this service has no task on the hot node
		}
		if !placement.NodeSatisfies(cold.View, svc.Constraints) {
			continue // the idle node cannot run this service
		}
		return Plan{Moves: []Move{{
			ServiceID:   svc.Ref.ID,
			ServiceName: svc.Ref.Name,
			TaskID:      taskID,
			FromNodeID:  hot.View.ID,
			ToNodeID:    cold.View.ID,
			FromLoad:    hot.Load,
			ToLoad:      cold.Load,
			Reason: fmt.Sprintf("node load spread %.1f%% >= %.1f%% threshold; relieve %s by moving %s to %s",
				spread, threshold*100, hot.View.Name, svc.Ref.Name, cold.View.Name),
		}}}
	}

	return Plan{} // imbalance exists but no eligible service can be moved
}

// activeNodes keeps only schedulable nodes (Active+Ready).
func activeNodes(nodes []NodeState) []NodeState {
	out := make([]NodeState, 0, len(nodes))
	for _, n := range nodes {
		if n.View.IsActive() {
			out = append(out, n)
		}
	}
	return out
}

// extremes returns the highest- and lowest-load nodes. It assumes len(nodes)>=1.
func extremes(nodes []NodeState) (hot, cold NodeState) {
	hot, cold = nodes[0], nodes[0]
	for _, n := range nodes[1:] {
		if n.Load > hot.Load {
			hot = n
		}
		if n.Load < cold.Load {
			cold = n
		}
	}
	return hot, cold
}

// firstTaskOf returns the ID of the first task belonging to serviceID among the
// node's tasks, and whether one was found.
func firstTaskOf(tasks []model.TaskView, serviceID string) (string, bool) {
	for _, t := range tasks {
		if t.ServiceID == serviceID {
			return t.ID, true
		}
	}
	return "", false
}
