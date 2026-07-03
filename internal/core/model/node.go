package model

// Node availability and state values this daemon checks, kept as plain strings
// so the core stays Docker-free.
const (
	NodeAvailabilityActive = "active"
	NodeStateReady         = "ready"
)

// NodeView is a read-only projection of a Swarm node.
type NodeView struct {
	ID           string
	Name         string            // hostname, matched by node.hostname constraints
	Availability string            // active | pause | drain
	State        string            // ready | down | unknown | ...
	Labels       map[string]string // node spec labels, matched by node.labels.* constraints
}

// IsActive reports whether the node is schedulable (active and ready).
func (n NodeView) IsActive() bool {
	return n.Availability == NodeAvailabilityActive && n.State == NodeStateReady
}
