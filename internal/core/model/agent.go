package model

import "time"

// AgentReport is a single per-node snapshot pushed by an agent to the manager.
// It carries the node's aggregate resource load plus per-task metrics for the
// tasks running on that node at collection time. The manager keys reports by
// NodeID, so a second report for the same node replaces the first — a node can
// never be double-counted, even if two agent tasks briefly overlap.
type AgentReport struct {
	NodeID    string       // Swarm node ID — the manager's dedup key
	NodeName  string       // hostname, for log/metric labels
	Timestamp time.Time    // when the agent collected the sample (informational)
	Node      NodeLoad     // node-level aggregate load and capacity
	Tasks     []TaskMetric // per-task load for tasks on this node
}

// NodeLoad is a node's aggregate resource utilization and capacity at report
// time. Percentages follow docker stats semantics (0..100 per task, summed
// across the node's tasks), so a busy multi-core node can report CPUPercent
// above 100 — treat it as a utilization signal, not a hard bound.
type NodeLoad struct {
	CPUPercent    float64 // aggregate CPU utilization across the node's tasks
	MemPercent    float64 // aggregate memory utilization across the node's tasks
	TotalCPU      int     // logical CPUs on the node (capacity)
	TotalMemBytes int64   // total memory on the node in bytes (capacity)
	TaskCount     int     // number of running tasks sampled
}

// TaskMetric is one task's resource load, measured locally by the agent on the
// node where the task runs.
type TaskMetric struct {
	TaskID     string
	ServiceID  string
	CPUPercent float64 // 0..100 (may exceed 100 on multi-core, like docker stats)
	MemPercent float64 // 0..100
}
