package model

// ServiceRef identifies a Swarm service.
type ServiceRef struct {
	ID   string
	Name string
}

// ServicePolicy is the autoscaling policy parsed from a service's
// swarm.autoscaler.* labels. The zero value is an unmanaged, disabled policy.
type ServicePolicy struct {
	Enabled bool
	Min     uint64
	Max     uint64
	Metric  string
	Target  float64
}

// ManagedService is an opted-in service together with its current state and
// parsed policy. It is a read-only projection produced by the swarm adapter.
type ManagedService struct {
	Ref         ServiceRef
	Replicas    uint64 // current desired replicas (replicated services)
	Replicated  bool   // false for global-mode services
	Policy      ServicePolicy
	Constraints []string // placement constraints, e.g. "node.labels.nodeNum==1"
}
