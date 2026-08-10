package port

import "github.com/Aleksey512/swarm-hpa/internal/core/model"

// StackStatusStore holds the per-stack GitOps status that the sync loop writes and
// the GET /stacks API reads. Implementations MUST be safe for concurrent use: the
// loop's worker pool writes from multiple goroutines while the HTTP handler reads.
type StackStatusStore interface {
	// SetStatus records the latest status for a stack. Called by the loop after
	// every sync attempt (success or failure). It resets the transient State to
	// "" (idle) and persists Repo.
	SetStatus(name string, status model.StackStatus)

	// SetState is a PARTIAL update that sets only the transient Repo and State
	// fields of a stack's status, preserving the rest (Revision/OK/ErrorStage/
	// DesiredReplicas/...). The loop calls it mid-pass to flip a stack to
	// "waiting" (before the repo lock) and "syncing" (after acquiring it) so the
	// /stacks UI can show live parallelism without clobbering the last result.
	// If no status exists yet (the API races the first tick), a minimal entry
	// {Name, Repo, State} is seeded.
	SetState(name, repo, state string)

	// Snapshot returns the status of every known stack, sorted by name. The
	// returned slice and its maps MUST be safe for the caller to mutate without
	// affecting the store (deep copy).
	Snapshot() []model.StackStatus
}
