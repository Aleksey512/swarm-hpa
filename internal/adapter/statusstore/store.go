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
func (s *Store) SetStatus(name string, status model.StackStatus) {
	status.Name = name
	status.DesiredReplicas = copyReplicas(status.DesiredReplicas)
	s.mu.Lock()
	s.items[name] = status
	s.mu.Unlock()
	s.logger.Debug("statusstore: status set",
		"stack", name, "revision", status.Revision, "ok", status.OK, "stage", status.ErrorStage)
}

// Snapshot returns the status of every known stack, sorted by name. The returned
// slice and its maps are deep copies — mutating them does not affect the store.
func (s *Store) Snapshot() []model.StackStatus {
	s.mu.RLock()
	out := make([]model.StackStatus, 0, len(s.items))
	for _, st := range s.items {
		st.DesiredReplicas = copyReplicas(st.DesiredReplicas)
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
