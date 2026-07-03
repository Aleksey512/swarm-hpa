package model

import (
	"testing"
	"time"
)

func TestAgentReportZeroValue(t *testing.T) {
	var r AgentReport
	if r.NodeID != "" || r.NodeName != "" {
		t.Errorf("zero AgentReport should have empty identity, got %+v", r)
	}
	if !r.Timestamp.IsZero() {
		t.Error("zero AgentReport timestamp should be the zero time")
	}
	if len(r.Tasks) != 0 {
		t.Errorf("zero AgentReport should have no tasks, got %d", len(r.Tasks))
	}
	if r.Node != (NodeLoad{}) {
		t.Errorf("zero AgentReport should have a zero NodeLoad, got %+v", r.Node)
	}
}

func TestAgentReportConstruction(t *testing.T) {
	ts := time.Unix(1_700_000_000, 0)
	r := AgentReport{
		NodeID:    "node-a",
		NodeName:  "worker-1",
		Timestamp: ts,
		Node:      NodeLoad{CPUPercent: 65.5, MemPercent: 40, TotalCPU: 8, TotalMemBytes: 16 << 30, TaskCount: 2},
		Tasks: []TaskMetric{
			{TaskID: "t1", ServiceID: "svc-web", CPUPercent: 50, MemPercent: 30},
			{TaskID: "t2", ServiceID: "svc-web", CPUPercent: 15.5, MemPercent: 10},
		},
	}
	if r.NodeID != "node-a" || r.NodeName != "worker-1" || !r.Timestamp.Equal(ts) {
		t.Errorf("identity/timestamp not carried through: %+v", r)
	}
	if r.Node.TotalCPU != 8 || r.Node.TaskCount != 2 {
		t.Errorf("node capacity not carried through: %+v", r.Node)
	}
	if len(r.Tasks) != 2 || r.Tasks[0].ServiceID != "svc-web" {
		t.Errorf("task metrics not carried through: %+v", r.Tasks)
	}
}

func TestManagedServiceRebalanceDefaultsOff(t *testing.T) {
	var ms ManagedService
	if ms.Rebalance || ms.Autoscale || ms.Heal {
		t.Errorf("zero ManagedService must not opt into anything, got %+v", ms)
	}
}
