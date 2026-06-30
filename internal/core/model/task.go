package model

import "time"

// Task lifecycle states this daemon reasons about. These are a subset of
// Swarm's task states, kept as plain strings so the core stays Docker-free; the
// adapter maps the SDK's typed states onto them.
const (
	TaskStatePending = "pending"
	TaskStateRunning = "running"
)

// TaskView is a read-only projection of a Swarm task.
type TaskView struct {
	ID           string
	ServiceID    string
	State        string    // actual state (e.g. pending, running)
	DesiredState string    // the state Swarm wants (e.g. running)
	NodeID       string    // node the task is assigned to, if any
	Err          string    // task status error/message — the real reason it is stuck
	Since        time.Time // last status timestamp
}

// IsPending reports whether the task is pending while Swarm wants it running —
// the precondition for the stuck-task signature (acted on in a later milestone).
func (t TaskView) IsPending() bool {
	return t.State == TaskStatePending && t.DesiredState == TaskStateRunning
}
