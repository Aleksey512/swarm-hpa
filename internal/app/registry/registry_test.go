package registry

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// testClock is an advanceable, concurrency-safe clock.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type countingRecorder struct {
	mu                                            sync.Mutex
	connected, disconnected, received, duplicates int
}

func (r *countingRecorder) AgentConnected(string)      { r.bump(&r.connected) }
func (r *countingRecorder) AgentDisconnected(string)   { r.bump(&r.disconnected) }
func (r *countingRecorder) AgentReportReceived(string) { r.bump(&r.received) }
func (r *countingRecorder) AgentDuplicate(string)      { r.bump(&r.duplicates) }

func (r *countingRecorder) bump(p *int) {
	r.mu.Lock()
	*p++
	r.mu.Unlock()
}

func (r *countingRecorder) snapshot() (int, int, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.connected, r.disconnected, r.received, r.duplicates
}

func report(nodeID string, cpu float64) model.AgentReport {
	return model.AgentReport{NodeID: nodeID, NodeName: nodeID, Node: model.NodeLoad{CPUPercent: cpu, TaskCount: 1}}
}

func TestIngestDedupesByNodeID(t *testing.T) {
	clk := &testClock{t: time.Unix(1000, 0)}
	rec := &countingRecorder{}
	r := New(time.Minute, clk, rec, discardLogger())

	// Two reports for the SAME node from the SAME source: one entry, last wins.
	if err := r.Ingest(report("node-a", 10), "src1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Ingest(report("node-a", 55), "src1"); err != nil {
		t.Fatal(err)
	}
	if r.Len() != 1 {
		t.Fatalf("want 1 entry after dedup, got %d", r.Len())
	}
	snap := r.Snapshot()
	if len(snap) != 1 || snap[0].Node.CPUPercent != 55 {
		t.Fatalf("last-writer-wins failed: %+v", snap)
	}
	if connected, _, received, _ := rec.snapshot(); connected != 1 || received != 2 {
		t.Errorf("connected=%d received=%d, want 1/2", connected, received)
	}
}

func TestSnapshotEvictsStale(t *testing.T) {
	clk := &testClock{t: time.Unix(1000, 0)}
	rec := &countingRecorder{}
	r := New(30*time.Second, clk, rec, discardLogger())

	r.Ingest(report("old", 10), "s")
	clk.advance(20 * time.Second)
	r.Ingest(report("fresh", 20), "s")

	// Advance so "old" is stale (>30s since its report) but "fresh" is not.
	clk.advance(15 * time.Second) // old: 35s ago (stale), fresh: 15s ago (live)
	snap := r.Snapshot()
	if len(snap) != 1 || snap[0].NodeID != "fresh" {
		t.Fatalf("stale eviction failed, got %+v", snap)
	}
	if r.Len() != 1 {
		t.Errorf("stale entry not evicted from the map, len=%d", r.Len())
	}
	if _, disconnected, _, _ := rec.snapshot(); disconnected != 1 {
		t.Errorf("want 1 disconnect event, got %d", disconnected)
	}
}

func TestDuplicateSourceDetected(t *testing.T) {
	clk := &testClock{t: time.Unix(1000, 0)}
	rec := &countingRecorder{}
	r := New(time.Minute, clk, rec, discardLogger())

	r.Ingest(report("node-a", 10), "container-1")
	clk.advance(2 * time.Second)
	// A second, distinct source reports for the same node while the first is fresh.
	r.Ingest(report("node-a", 12), "container-2")

	if _, _, _, dup := rec.snapshot(); dup != 1 {
		t.Errorf("want 1 duplicate detection, got %d", dup)
	}
	// Still exactly one entry — dedup protects correctness regardless.
	if r.Len() != 1 {
		t.Errorf("duplicate must still dedup to one entry, len=%d", r.Len())
	}
}

func TestSameSourceReReportIsNotDuplicate(t *testing.T) {
	clk := &testClock{t: time.Unix(1000, 0)}
	rec := &countingRecorder{}
	r := New(time.Minute, clk, rec, discardLogger())

	r.Ingest(report("node-a", 10), "container-1")
	clk.advance(15 * time.Second)
	r.Ingest(report("node-a", 12), "container-1") // same source: normal re-report

	if _, _, _, dup := rec.snapshot(); dup != 0 {
		t.Errorf("same-source re-report must not be a duplicate, got %d", dup)
	}
}

func TestEmptyNodeIDRejected(t *testing.T) {
	r := New(time.Minute, &testClock{}, nil, discardLogger())
	if err := r.Ingest(report("", 1), "s"); err == nil {
		t.Error("empty node id must be rejected")
	}
}

func TestConcurrentIngestAndSnapshot(t *testing.T) {
	clk := &testClock{t: time.Unix(1000, 0)}
	r := New(time.Minute, clk, &countingRecorder{}, discardLogger())

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = r.Ingest(report("node", float64(j)), "s")
				_ = r.Snapshot()
			}
		}(i)
	}
	wg.Wait()

	// All writes targeted one node id → exactly one entry survives.
	if r.Len() != 1 {
		t.Errorf("want 1 entry after concurrent same-node writes, got %d", r.Len())
	}
}
