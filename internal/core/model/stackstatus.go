package model

import "time"

// StackStatus is the runtime status of one GitOps stack, written by the sync loop
// after each sync attempt and read by the GET /stacks API. DesiredReplicas is the
// non-autoscaled, non-global replica snapshot taken from the last successful render
// (empty before the first render); the drift check compares it against live Swarm
// state on demand.
type StackStatus struct {
	Name            string
	Revision        string
	OK              bool
	ErrorStage      string // failing stage when OK is false: git|render|secrets|rotate|deploy
	ErrorMessage    string
	LastSync        time.Time
	DeployCount     uint64
	DesiredReplicas map[string]uint64 // service name → desired replicas (non-autoscaled, non-global)
}

// ServiceDrift is the per-service result of comparing desired replicas against
// live Swarm replicas. Only non-autoscaled, non-global services are evaluated:
// autoscaler-owned services intentionally diverge (the HPA scales them) and global
// services have no replica count, so neither is reported as drift.
type ServiceDrift struct {
	Service string
	Desired uint64
	Live    uint64
	Drifted bool
}
