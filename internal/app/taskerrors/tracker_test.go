package taskerrors

import (
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
	"github.com/Aleksey512/swarm-hpa/internal/core/taskerrors"
)

type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func vxlanEvent(service, taskID string, at time.Time) model.TaskErrorEvent {
	return model.TaskErrorEvent{
		ServiceID:   "svc-id-" + service,
		ServiceName: service,
		Slot:        1,
		TaskID:      taskID,
		Class:       string(taskerrors.ClassVxlanFileExists),
		Since:       at,
		Err:         `network sandbox join failed: error creating vxlan interface: file exists`,
	}
}

func TestTrackerDedupSameTask(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	tr := NewTracker(fakeClock{now}, testLogger())
	ev := vxlanEvent("admin_analytics", "task-1", now.Add(-time.Minute))

	tr.Record([]model.TaskErrorEvent{ev})
	tr.Record([]model.TaskErrorEvent{ev}) // same task seen on the next tick
	tr.Record([]model.TaskErrorEvent{ev}) // and again

	got := tr.WindowSnapshot(now, 5*time.Minute)
	want := map[string]map[string]int{
		"admin_analytics": {"vxlan_file_exists": 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot = %v, want %v", got, want)
	}
}

func TestTrackerWindowExpiry(t *testing.T) {
	start := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	tr := NewTracker(fakeClock{start}, testLogger())
	tr.Record([]model.TaskErrorEvent{vxlanEvent("svc", "t-old", start.Add(-6*time.Minute))})
	tr.Record([]model.TaskErrorEvent{vxlanEvent("svc", "t-new", start.Add(-time.Minute))})

	// 5m window at `start`: t-old (6m ago) is out, t-new (1m ago) is in.
	got := tr.WindowSnapshot(start, 5*time.Minute)
	if got["svc"]["vxlan_file_exists"] != 1 {
		t.Errorf("snapshot = %v, want exactly the fresh event", got)
	}

	// The expired event must be pruned, not just filtered: a later snapshot
	// over a wider window must not resurrect it.
	got = tr.WindowSnapshot(start.Add(time.Hour), time.Hour)
	if got["svc"]["vxlan_file_exists"] != 0 {
		t.Errorf("expired event resurrected: %v", got)
	}
}

func TestTrackerMultiServiceMultiClass(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	tr := NewTracker(fakeClock{now}, testLogger())
	at := now.Add(-time.Minute)
	events := []model.TaskErrorEvent{
		vxlanEvent("admin_analytics", "t1", at),
		vxlanEvent("admin_analytics", "t2", at), // same service, second task
		{ServiceName: "admin_api", Slot: 2, TaskID: "t3", Class: string(taskerrors.ClassNetworkSandboxJoin), Since: at},
		{ServiceName: "admin_api", Slot: 3, TaskID: "t4", Class: string(taskerrors.ClassOther), Since: at},
	}
	tr.Record(events)

	got := tr.WindowSnapshot(now, 5*time.Minute)
	want := map[string]map[string]int{
		"admin_analytics": {"vxlan_file_exists": 2},
		"admin_api":       {"network_sandbox_join_failed": 1, "other": 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot = %v, want %v", got, want)
	}
}

func TestTrackerSameSlotDifferentTaskCounts(t *testing.T) {
	// A superseded task and its replacement can share a slot; they are
	// distinct task IDs and must count separately.
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	tr := NewTracker(fakeClock{now}, testLogger())
	at := now.Add(-time.Minute)
	tr.Record([]model.TaskErrorEvent{
		{ServiceName: "svc", Slot: 1, TaskID: "old", Class: string(taskerrors.ClassOther), Since: at},
		{ServiceName: "svc", Slot: 1, TaskID: "new", Class: string(taskerrors.ClassOther), Since: at},
	})
	got := tr.WindowSnapshot(now, 5*time.Minute)
	if got["svc"]["other"] != 2 {
		t.Errorf("slot-sharing tasks must count separately, got %v", got)
	}
}

func TestTrackerCapEvictsOldest(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	tr := NewTracker(fakeClock{now}, testLogger())

	setCap := func(n int) func() {
		original := capForTest
		capForTest = func() int { return n }
		return func() { capForTest = original }
	}
	defer setCap(3)()

	at := now.Add(-time.Minute)
	tr.Record([]model.TaskErrorEvent{
		{ServiceName: "svc", Slot: 0, TaskID: "oldest", Class: "other", Since: at.Add(-10 * time.Second)},
		{ServiceName: "svc", Slot: 1, TaskID: "mid", Class: "other", Since: at.Add(-5 * time.Second)},
		{ServiceName: "svc", Slot: 2, TaskID: "young", Class: "other", Since: at},
	})
	// One over capacity: "oldest" must be evicted.
	tr.Record([]model.TaskErrorEvent{
		{ServiceName: "svc", Slot: 3, TaskID: "newest", Class: "other", Since: at},
	})

	got := tr.WindowSnapshot(now, 5*time.Minute)
	if got["svc"]["other"] != 3 {
		t.Errorf("after cap eviction want 3 events, got %v", got)
	}
}

func TestTrackerRecordEmpty(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	tr := NewTracker(fakeClock{now}, testLogger())
	tr.Record(nil)
	got := tr.WindowSnapshot(now, 5*time.Minute)
	if len(got) != 0 {
		t.Errorf("empty record must yield empty snapshot, got %v", got)
	}
}

func TestTrackerDefaults(t *testing.T) {
	// Nil clock and nil logger must not panic (constructor falls back).
	tr := NewTracker(nil, nil)
	if _, ok := tr.clock.(port.SystemClock); !ok {
		t.Error("nil clock must fall back to the system clock")
	}
	if tr.logger == nil {
		t.Error("nil logger must fall back to slog.Default")
	}
}
