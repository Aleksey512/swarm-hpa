// Package statusstore is the in-memory implementation of port.StackStatusStore.
// The GitOps loop writes per-stack status from its worker goroutines while the
// GET /stacks API reads a snapshot. All access is guarded by a sync.RWMutex, and
// Snapshot returns a deep copy so API callers can never mutate the store.
package statusstore

import (
	"log/slog"
	"maps"
	"sort"
	"sync"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// compile-time proof the store satisfies the core port.
var _ port.StackStatusStore = (*Store)(nil)

// Store is the concurrency-safe, in-memory per-stack status store.
type Store struct {
	mu     sync.RWMutex
	items  map[string]model.StackStatus
	logger *slog.Logger
}

// New builds an empty status store. A nil logger falls back to slog.Default.
func New(logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{
		items:  make(map[string]model.StackStatus),
		logger: logger,
	}
}

// SetStatus records the latest status for a stack. It deep-copies the
// DesiredReplicas map so the caller may keep mutating its own copy afterward.
// The transient State is reset to "" (idle): a finished sync leaves the stack
// not-in-flight, and the last result (Revision/OK/...) stays visible until the
// next pass.
func (s *Store) SetStatus(name string, status model.StackStatus) {
	status.Name = name
	status.State = "" // end of pass → idle; live waiting/syncing is SetState's job
	status.DesiredReplicas = copyReplicas(status.DesiredReplicas)
	status.Files = copyFiles(status.Files)
	s.mu.Lock()
	s.items[name] = status
	s.mu.Unlock()
	s.logger.Debug("statusstore: status set",
		"stack", name, "repo", status.Repo, "revision", status.Revision,
		"ok", status.OK, "stage", status.ErrorStage)
}

// SetState is the PARTIAL update for the transient sync state: it sets only
// Repo and State, preserving the last result a full SetStatus wrote. The loop
// uses it to flip a stack to "waiting"/"syncing" mid-pass so the /stacks UI can
// show live parallelism without clobbering Revision/OK/ErrorStage/drift between
// ticks. If no status exists yet (the API reads before the first sync finishes),
// a minimal {Name, Repo, State} entry is seeded so the in-flight state is still
// observable.
func (s *Store) SetState(name, repo, state string) {
	s.mu.Lock()
	st, ok := s.items[name]
	if !ok {
		st = model.StackStatus{Name: name}
	}
	st.Repo = repo
	st.State = state
	s.items[name] = st
	s.mu.Unlock()
	s.logger.Debug("statusstore: state set", "stack", name, "repo", repo, "state", state)
}

// Snapshot returns the status of every known stack, sorted by name. The returned
// slice and its maps are deep copies — mutating them does not affect the store.
func (s *Store) Snapshot() []model.StackStatus {
	s.mu.RLock()
	out := make([]model.StackStatus, 0, len(s.items))
	for _, st := range s.items {
		st.DesiredReplicas = copyReplicas(st.DesiredReplicas)
		st.Files = copyFiles(st.Files)
		out = append(out, st)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	s.logger.Debug("statusstore: snapshot", "stacks", len(out))
	return out
}

// copyReplicas returns a detached copy of m (nil in, nil out) so stored and
// returned status values share no map with callers.
func copyReplicas(m map[string]uint64) map[string]uint64 {
	if m == nil {
		return nil
	}
	cp := make(map[string]uint64, len(m))
	maps.Copy(cp, m)
	return cp
}

// copyFiles returns a detached copy of f (nil in, nil out) so stored and returned
// status values share no slice with callers. StackFileStatus is a value type (no
// pointers/maps), so a shallow element copy is a full deep copy.
func copyFiles(f []model.StackFileStatus) []model.StackFileStatus {
	if f == nil {
		return nil
	}
	cp := make([]model.StackFileStatus, len(f))
	copy(cp, f)
	return cp
}
