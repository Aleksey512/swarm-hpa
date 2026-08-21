// Package taskerrors holds the sliding-window task-error tracker: it
// remembers classified task errors (deduplicated per task instance) and
// answers "how many errors of each class did each service have in the last N
// minutes". The reconciler records; the metrics exporter, the /stacks API and
// the post-deploy GitOps alert read window snapshots.
package taskerrors

import (
	"log/slog"
	"sync"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// maxEvents bounds tracked distinct task errors. The natural bound is the
// number of distinct failed tasks in the window (small); the cap exists so a
// pathological cluster (or a Swarm reporting far-future timestamps) cannot
// grow the tracker without limit. Oldest entries are evicted first.
const maxEvents = 10_000

// capForTest lets tests shrink the cap without racing every Record call.
// Production reads the constant; tests swap the function.
var capForTest = func() int { return maxEvents }

// key identifies one failing task instance: the same task's error must count
// once no matter how many observe ticks see it. ServiceName (not ID) keys the
// key struct because consumers reason in names; the ID is not needed once the
// join happened.
type key struct {
	ServiceName string
	Slot        int
	TaskID      string
}

// Tracker is a concurrency-safe sliding window over classified task errors.
// Record is called once per reconciler tick with ALL currently-erroring
// tasks; WindowSnapshot prunes by age and aggregates per service and class.
type Tracker struct {
	mu     sync.Mutex
	events map[key]model.TaskErrorEvent
	clock  port.Clock
	logger *slog.Logger
}

// NewTracker returns a Tracker over the given clock. Nil clock falls back to
// the system clock, nil logger to slog.Default.
func NewTracker(clock port.Clock, logger *slog.Logger) *Tracker {
	if clock == nil {
		clock = port.SystemClock{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Tracker{
		events: make(map[key]model.TaskErrorEvent),
		clock:  clock,
		logger: logger,
	}
}

// Record ingests classified error events. Deduplication is by task instance
// (service, slot, task ID): re-recording the same task neither grows the
// window nor refreshes its timestamp — the task entered the window when Swarm
// first stamped its status error, and leaves it when that stamp ages out.
// When the map is at capacity, the oldest entries are evicted to make room.
func (t *Tracker) Record(events []model.TaskErrorEvent) {
	if len(events) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	added := 0
	for _, e := range events {
		k := key{ServiceName: e.ServiceName, Slot: e.Slot, TaskID: e.TaskID}
		if _, exists := t.events[k]; exists {
			continue
		}
		if len(t.events) >= capForTest() {
			t.evictOldestLocked()
		}
		t.events[k] = e
		added++
	}
	if added > 0 {
		t.logger.Debug("task error events recorded",
			"recorded", added, "already_tracked", len(events)-added,
			"tracked_total", len(t.events))
	}
}

// evictOldestLocked removes the single oldest entry. Caller holds the lock.
func (t *Tracker) evictOldestLocked() {
	var oldestKey key
	var oldest time.Time
	first := true
	for k, e := range t.events {
		if first || e.Since.Before(oldest) {
			oldestKey, oldest, first = k, e.Since, false
		}
	}
	if !first {
		delete(t.events, oldestKey)
		t.logger.Warn("task error tracker at capacity; evicted oldest event",
			"evicted_service", oldestKey.ServiceName, "evicted_task", oldestKey.TaskID,
			"cap", capForTest())
	}
}

// WindowSnapshot returns the per-service, per-class error counts for events
// newer than window (relative to now), pruning everything older. The map is
// freshly allocated — callers own it. Pruning happens here (not in Record) so
// the window boundary is evaluated against the READER's clock, matching what
// the reader will report.
func (t *Tracker) WindowSnapshot(now time.Time, window time.Duration) map[string]map[string]int {
	t.mu.Lock()
	defer t.mu.Unlock()

	pruned := 0
	snapshot := make(map[string]map[string]int)
	for k, e := range t.events {
		age := now.Sub(e.Since)
		if age >= window {
			delete(t.events, k)
			pruned++
			continue
		}
		if snapshot[e.ServiceName] == nil {
			snapshot[e.ServiceName] = make(map[string]int)
		}
		snapshot[e.ServiceName][e.Class]++
	}
	if pruned > 0 {
		t.logger.Debug("task error window pruned", "pruned", pruned,
			"window", window.String(), "remaining", len(t.events))
	}
	return snapshot
}
