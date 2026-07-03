package port

import "github.com/Aleksey512/swarm-hpa/internal/core/model"

// StackStatusStore holds the per-stack GitOps status that the sync loop writes and
// the GET /stacks API reads. Implementations MUST be safe for concurrent use: the
// loop's worker pool writes from multiple goroutines while the HTTP handler reads.
type StackStatusStore interface {
	// SetStatus records the latest status for a stack. Called by the loop after
	// every sync attempt (success or failure).
	SetStatus(name string, status model.StackStatus)

	// Snapshot returns the status of every known stack, sorted by name. The
	// returned slice and its maps MUST be safe for the caller to mutate without
	// affecting the store (deep copy).
	Snapshot() []model.StackStatus
}
