package gitopsync

import (
	"bytes"
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/app/taskerrors"
	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
	coretaskerrors "github.com/Aleksey512/swarm-hpa/internal/core/taskerrors"
)

// recordingRecorder captures the v0.9.0 recorder calls for assertions.
type recordingRecorder struct {
	port.NopRecorder
	mu        sync.Mutex
	taskErrs  []string // "stack/service/class=count"
	netErrors []string // "stack=count"
}

func (r *recordingRecorder) StackTaskErrors(stack, service, class string, count int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.taskErrs = append(r.taskErrs, stack+"/"+service+"/"+class+"="+itoa(count))
}

func (r *recordingRecorder) DeployNetworkErrors(stack string, count int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.netErrors = append(r.netErrors, stack+"="+itoa(count))
}

func itoa(n int) string { return strconv.Itoa(n) }

// bufLogger returns a logger writing to buf so ERROR lines can be asserted.
func bufLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func newCheckLoop(t *testing.T, tracker *taskerrors.Tracker, rec port.Recorder, logBuf *bytes.Buffer) *Loop {
	t.Helper()
	git := &fakeGit{revs: []string{"r1"}, files: map[string][]byte{"c.yml": {}}}
	l := New(git, fakeRenderer{}, &fakeDeployer{}, nil, rec, nil,
		stacks("admin", "c.yml"), "changed", false, false, 1, bufLogger(logBuf),
		WithTaskErrorTracker(tracker, 10*time.Millisecond, 5*time.Minute))
	return l
}

func TestPostDeployCheckDetectsVxlan(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	tracker := taskerrors.NewTracker(fakeFixedClock{now}, testLogger())
	tracker.Record([]model.TaskErrorEvent{
		{ServiceName: "admin_analytics", Slot: 1, TaskID: "t1",
			Class: string(coretaskerrors.ClassVxlanFileExists), Since: now.Add(-time.Minute)},
		{ServiceName: "other_api", Slot: 1, TaskID: "t2",
			Class: string(coretaskerrors.ClassVxlanFileExists), Since: now.Add(-time.Minute)},
	})

	var logBuf bytes.Buffer
	rec := &recordingRecorder{}
	l := newCheckLoop(t, tracker, rec, &logBuf)
	l.checkClock = func() time.Time { return now } // match the tracker's pinned clock

	l.runPostDeployCheck("admin")

	out := logBuf.String()
	if !strings.Contains(out, "network sandbox (vxlan) task errors detected") {
		t.Errorf("ERROR alert line missing in log: %s", out)
	}
	if !strings.Contains(out, "admin_analytics") {
		t.Errorf("alert must name the affected service: %s", out)
	}
	if len(rec.taskErrs) != 1 || !strings.Contains(rec.taskErrs[0], "admin/admin_analytics/vxlan_file_exists") {
		t.Errorf("StackTaskErrors calls = %v", rec.taskErrs)
	}
	if len(rec.netErrors) != 1 || !strings.Contains(rec.netErrors[0], "admin=") {
		t.Errorf("DeployNetworkErrors calls = %v", rec.netErrors)
	}
}

func TestPostDeployCheckCleanWindow(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	tracker := taskerrors.NewTracker(fakeFixedClock{now}, testLogger())
	// Only an unrelated "other"-class error for the stack — not a network error.
	tracker.Record([]model.TaskErrorEvent{
		{ServiceName: "admin_api", Slot: 1, TaskID: "t1",
			Class: string(coretaskerrors.ClassOther), Since: now.Add(-time.Minute)},
	})

	var logBuf bytes.Buffer
	rec := &recordingRecorder{}
	l := newCheckLoop(t, tracker, rec, &logBuf)
	l.checkClock = func() time.Time { return now }

	l.runPostDeployCheck("admin")

	if strings.Contains(logBuf.String(), "task errors detected") {
		t.Errorf("no network errors: alert must not fire, log: %s", logBuf.String())
	}
	if len(rec.taskErrs) != 0 || len(rec.netErrors) != 0 {
		t.Errorf("recorder calls = %v / %v, want none", rec.taskErrs, rec.netErrors)
	}
}

func TestPostDeployCheckSkipsOtherStacks(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	tracker := taskerrors.NewTracker(fakeFixedClock{now}, testLogger())
	tracker.Record([]model.TaskErrorEvent{
		{ServiceName: "monitoring_grafana", Slot: 1, TaskID: "t1",
			Class: string(coretaskerrors.ClassVxlanFileExists), Since: now.Add(-time.Minute)},
	})

	var logBuf bytes.Buffer
	rec := &recordingRecorder{}
	l := newCheckLoop(t, tracker, rec, &logBuf)
	l.checkClock = func() time.Time { return now }

	l.runPostDeployCheck("admin") // the error belongs to monitoring, not admin

	if strings.Contains(logBuf.String(), "task errors detected") {
		t.Errorf("error belongs to another stack; alert must not fire: %s", logBuf.String())
	}
	if len(rec.netErrors) != 0 {
		t.Errorf("recorder calls = %v, want none", rec.netErrors)
	}
}

func TestSchedulePostDeployCheckFiresAndCancels(t *testing.T) {
	// Real clock on both sides: the loop's checkClock and the event stamps must
	// agree, or the window pruning evicts the event before the check sees it.
	tracker := taskerrors.NewTracker(port.SystemClock{}, testLogger())
	tracker.Record([]model.TaskErrorEvent{
		{ServiceName: "admin_api", Slot: 1, TaskID: "t1",
			Class: string(coretaskerrors.ClassVxlanFileExists), Since: time.Now().Add(-time.Minute)},
	})

	var logBuf bytes.Buffer
	rec := &recordingRecorder{}
	l := newCheckLoop(t, tracker, rec, &logBuf)
	l.checkDelay = 20 * time.Millisecond

	// Fires after the delay.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l.schedulePostDeployCheck(ctx, "admin")
	deadline := time.After(2 * time.Second)
	for {
		rec.mu.Lock()
		fired := len(rec.netErrors) > 0
		rec.mu.Unlock()
		if fired {
			break
		}
		select {
		case <-deadline:
			t.Fatal("scheduled check did not fire within 2s")
		case <-time.After(5 * time.Millisecond):
		}
	}

	// Cancelled context: no fire, no panic.
	var logBuf2 bytes.Buffer
	l2 := newCheckLoop(t, tracker, rec, &logBuf2)
	l2.checkDelay = time.Hour
	ctx2, cancel2 := context.WithCancel(context.Background())
	l2.schedulePostDeployCheck(ctx2, "admin")
	cancel2()
	if !strings.Contains(logBuf2.String(), "post-deploy error check scheduled") {
		t.Errorf("scheduling INFO line missing: %s", logBuf2.String())
	}
}

func TestSchedulePostDeployCheckDisabledWithoutTracker(t *testing.T) {
	var logBuf bytes.Buffer
	git := &fakeGit{revs: []string{"r1"}, files: map[string][]byte{"c.yml": {}}}
	l := New(git, fakeRenderer{}, &fakeDeployer{}, nil, port.NopRecorder{}, nil,
		stacks("admin", "c.yml"), "changed", false, false, 1, bufLogger(&logBuf))

	l.schedulePostDeployCheck(context.Background(), "admin") // no panic, nothing scheduled

	if strings.Contains(logBuf.String(), "scheduled") {
		t.Error("no tracker wired: nothing must be scheduled")
	}
}

// fakeFixedClock is a port.Clock pinned to one instant.
type fakeFixedClock struct{ now time.Time }

func (c fakeFixedClock) Now() time.Time { return c.now }
