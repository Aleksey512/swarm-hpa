package agentloop

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// TestMain guards against goroutine leaks: Run and its ticker must stop when the
// context is cancelled.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeClock struct{}

func (fakeClock) Now() time.Time { return time.Unix(0, 0) }

type fakeCollector struct {
	report model.AgentReport
	err    error
}

func (f fakeCollector) Collect(context.Context) (model.AgentReport, error) {
	return f.report, f.err
}

type fakeReporter struct {
	mu  sync.Mutex
	got []model.AgentReport
	err error
}

func (f *fakeReporter) Report(_ context.Context, r model.AgentReport) error {
	f.mu.Lock()
	f.got = append(f.got, r)
	f.mu.Unlock()
	return f.err
}

func (f *fakeReporter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

// fakeRecorder signals on every outcome so tests can synchronize on completed
// report cycles without sleeping.
type fakeRecorder struct {
	mu     sync.Mutex
	sent   int
	errs   int
	signal chan struct{}
}

func (r *fakeRecorder) ReportSent(time.Time) { r.mu.Lock(); r.sent++; r.mu.Unlock(); r.ping() }
func (r *fakeRecorder) ReportError()         { r.mu.Lock(); r.errs++; r.mu.Unlock(); r.ping() }

func (r *fakeRecorder) ping() {
	select {
	case r.signal <- struct{}{}:
	default:
	}
}

func (r *fakeRecorder) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sent, r.errs
}

// driveLoop starts Run with a manual tick source and returns the ticks channel,
// a cancel func, and a done channel carrying Run's return value.
func driveLoop(l *Loop) (chan<- time.Time, context.CancelFunc, <-chan error) {
	ticks := make(chan time.Time)
	l.tickSource = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx, time.Hour) }()
	return ticks, cancel, done
}

func waitSignal(t *testing.T, sig <-chan struct{}) {
	t.Helper()
	select {
	case <-sig:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a report cycle")
	}
}

func TestRunReportsImmediatelyAndOnTick(t *testing.T) {
	rec := &fakeRecorder{signal: make(chan struct{}, 8)}
	rep := &fakeReporter{}
	col := fakeCollector{report: model.AgentReport{NodeID: "n", Node: model.NodeLoad{TaskCount: 1}}}
	l := New(col, rep, rec, fakeClock{}, discardLogger())

	ticks, cancel, done := driveLoop(l)

	waitSignal(t, rec.signal) // immediate report on start
	ticks <- time.Now()       // one more cycle
	waitSignal(t, rec.signal)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil on cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	if rep.count() < 2 {
		t.Errorf("reporter got %d reports, want >= 2", rep.count())
	}
	if sent, errs := rec.counts(); sent < 2 || errs != 0 {
		t.Errorf("recorder sent=%d errs=%d, want sent>=2 errs=0", sent, errs)
	}
}

func TestRunRecordsCollectError(t *testing.T) {
	rec := &fakeRecorder{signal: make(chan struct{}, 8)}
	rep := &fakeReporter{}
	col := fakeCollector{err: errors.New("no daemon")}
	l := New(col, rep, rec, fakeClock{}, discardLogger())

	_, cancel, done := driveLoop(l)
	waitSignal(t, rec.signal) // immediate cycle records an error
	cancel()
	<-done

	if rep.count() != 0 {
		t.Errorf("reporter must not be called when collect fails, got %d", rep.count())
	}
	if sent, errs := rec.counts(); sent != 0 || errs < 1 {
		t.Errorf("recorder sent=%d errs=%d, want sent=0 errs>=1", sent, errs)
	}
}

func TestRunRecordsReportError(t *testing.T) {
	rec := &fakeRecorder{signal: make(chan struct{}, 8)}
	rep := &fakeReporter{err: errors.New("manager down")}
	col := fakeCollector{report: model.AgentReport{NodeID: "n"}}
	l := New(col, rep, rec, fakeClock{}, discardLogger())

	_, cancel, done := driveLoop(l)
	waitSignal(t, rec.signal)
	cancel()
	<-done

	if sent, errs := rec.counts(); sent != 0 || errs < 1 {
		t.Errorf("recorder sent=%d errs=%d, want sent=0 errs>=1", sent, errs)
	}
}

func TestNewNilRecorderIsSafe(t *testing.T) {
	// A nil recorder must not panic (falls back to a no-op).
	l := New(fakeCollector{report: model.AgentReport{NodeID: "n"}}, &fakeReporter{}, nil, nil, nil)
	l.reportOnce(context.Background())
}
