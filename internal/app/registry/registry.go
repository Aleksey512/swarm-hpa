// Package registry is the manager-side store of agent reports. It is the data
// layer that guarantees "no two agents from one node": reports are keyed by node
// ID, so a second report for the same node replaces the first (last-writer-wins)
// and a node can never be double-counted — even if two agent tasks briefly
// overlap during a rolling update. Stale entries are evicted so a dead node
// stops influencing scaling/rebalancing decisions. It is safe for concurrent use
// by ingest HTTP handlers (writers) and the reconcile loop (reader).
package registry

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// Recorder observes registry events for self-metrics. A nil recorder is replaced
// with a no-op. The concrete Prometheus recorder implements this in the
// observability adapter.
type Recorder interface {
	AgentConnected(node string)
	AgentDisconnected(node string)
	AgentReportReceived(node string)
	AgentDuplicate(node string)
	NodeLoad(node string, cpuPct, memPct float64)
}

type nopRecorder struct{}

func (nopRecorder) AgentConnected(string)             {}
func (nopRecorder) AgentDisconnected(string)          {}
func (nopRecorder) AgentReportReceived(string)        {}
func (nopRecorder) AgentDuplicate(string)             {}
func (nopRecorder) NodeLoad(string, float64, float64) {}

// entry wraps a report with the manager's receive time. LastSeen drives
// staleness from the MANAGER's clock, not the agent's Timestamp, so clock skew
// between nodes cannot make a live agent look stale (or vice versa).
type entry struct {
	report   model.AgentReport
	lastSeen time.Time
	source   string // opaque sender identity (e.g. remote addr) for duplicate detection
}

// Registry holds the latest report per node.
type Registry struct {
	mu       sync.Mutex
	entries  map[string]entry
	stale    time.Duration
	clock    port.Clock
	recorder Recorder
	logger   *slog.Logger
}

// compile-time proof the registry satisfies the core ingest port.
var _ port.ReportSink = (*Registry)(nil)

// New constructs a Registry. stale is how long a report stays usable after
// receipt. A nil recorder/clock/logger fall back to a no-op, the system clock,
// and slog.Default.
func New(stale time.Duration, clock port.Clock, recorder Recorder, logger *slog.Logger) *Registry {
	if recorder == nil {
		recorder = nopRecorder{}
	}
	if clock == nil {
		clock = port.SystemClock{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{
		entries:  make(map[string]entry),
		stale:    stale,
		clock:    clock,
		recorder: recorder,
		logger:   logger,
	}
}

// Ingest records a report, keyed by node ID (last-writer-wins). source is the
// opaque identity of the sender, used only to detect a second live agent
// reporting for the same node (a global-mode misconfiguration). It returns an
// error only for a malformed report (empty node ID).
//
// Duplicate note: an agent restart or reschedule briefly presents a new source
// for the same node while the previous entry is still fresh; that is flagged as
// a (harmless) duplicate and is expected — dedup still keeps exactly one entry.
func (r *Registry) Ingest(report model.AgentReport, source string) error {
	if report.NodeID == "" {
		return fmt.Errorf("registry: report has empty node id")
	}

	now := r.clock.Now()

	r.mu.Lock()
	prev, existed := r.entries[report.NodeID]
	duplicate := existed && prev.source != "" && source != "" &&
		prev.source != source && now.Sub(prev.lastSeen) <= r.stale
	r.entries[report.NodeID] = entry{report: report, lastSeen: now, source: source}
	r.mu.Unlock()

	// Emit events after releasing the lock to keep the critical section tight.
	if !existed {
		r.recorder.AgentConnected(report.NodeID)
		r.logger.Info("agent connected", "node", report.NodeID, "name", report.NodeName, "source", source)
	}
	r.recorder.AgentReportReceived(report.NodeID)
	r.recorder.NodeLoad(report.NodeID, report.Node.CPUPercent, report.Node.MemPercent)
	if duplicate {
		r.recorder.AgentDuplicate(report.NodeID)
		r.logger.Warn("duplicate agent for node from a distinct source (misconfigured non-global agent?)",
			"node", report.NodeID, "previous_source", prev.source, "source", source)
	}
	r.logger.Debug("agent report ingested",
		"node", report.NodeID, "tasks", len(report.Tasks), "replaced", existed,
		"cpu_pct", report.Node.CPUPercent, "mem_pct", report.Node.MemPercent)
	return nil
}

// Snapshot returns the current non-stale reports and evicts stale entries as a
// side effect. The returned slice is a copy the caller may retain.
func (r *Registry) Snapshot() []model.AgentReport {
	now := r.clock.Now()

	r.mu.Lock()
	live := make([]model.AgentReport, 0, len(r.entries))
	var evicted []string
	for id, e := range r.entries {
		if now.Sub(e.lastSeen) > r.stale {
			delete(r.entries, id)
			evicted = append(evicted, id)
			continue
		}
		live = append(live, e.report)
	}
	r.mu.Unlock()

	for _, id := range evicted {
		r.recorder.AgentDisconnected(id)
		r.logger.Info("agent evicted (stale — no recent report)", "node", id, "stale_after", r.stale)
	}
	return live
}

// Len returns the number of currently-tracked agents WITHOUT evicting (it may
// include entries that Snapshot would drop). Intended for tests and diagnostics.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}
