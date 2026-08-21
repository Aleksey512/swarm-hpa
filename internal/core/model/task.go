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
	Slot         int       // replica slot the task occupies (0 for non-replicated)
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

// TaskErrorEvent is one classified task status error, ready for the sliding
// window tracker. Class is a plain string (not core/taskerrors.Class) so this
// package does not import a sibling core package; the classifier assigns the
// class at construction time. Slot+TaskID identify the failing task instance:
// the same task's error must count once in the window, no matter how many
// observe ticks see it.
type TaskErrorEvent struct {
	ServiceID   string
	ServiceName string // joined from the service listing (AllTasks has IDs only)
	Slot        int
	TaskID      string
	Class       string // core/taskerrors.Class value
	Since       time.Time
	Err         string // raw error text for logs only — never a metric label
}
