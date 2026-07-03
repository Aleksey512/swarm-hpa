//go:build integration

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/app/reconciler"
	"github.com/Aleksey512/swarm-hpa/internal/config"
	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// This file is compiled only under `-tags integration`. It exercises the whole
// daemon lifecycle (buildApp -> app.run: metrics server + reconcile loop ->
// graceful shutdown) with fakes and an injected tick source — no Docker socket,
// no live Prometheus. The package's TestMain (main_test.go) runs it under
// goleak, so a clean shutdown must leave no goroutines behind.

func integTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeSwarm is a minimal port.SwarmController: it serves one opted-in service
// and counts ManagedServices calls so we can confirm the loop actually ticked.
type fakeSwarm struct {
	mu       sync.Mutex
	svcCalls int
}

func (f *fakeSwarm) ManagedServices(context.Context) ([]model.ManagedService, error) {
	f.mu.Lock()
	f.svcCalls++
	f.mu.Unlock()
	return []model.ManagedService{{
		Ref:        model.ServiceRef{ID: "s1", Name: "web"},
		Replicas:   2,
		Replicated: true,
		Policy:     model.ServicePolicy{Enabled: true, Min: 1, Max: 5, Metric: "cpu", Target: 80},
	}}, nil
}
func (f *fakeSwarm) Tasks(context.Context, string) ([]model.TaskView, error) { return nil, nil }
func (f *fakeSwarm) Nodes(context.Context) ([]model.NodeView, error)         { return nil, nil }
func (f *fakeSwarm) Scale(context.Context, string, uint64) error             { return nil }
func (f *fakeSwarm) ForceUpdate(context.Context, string) error               { return nil }

func (f *fakeSwarm) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.svcCalls
}

// nopMetrics reports "no data" so the loop makes no scaling decision — the test
// is about lifecycle, not scaling math.
type nopMetrics struct{}

func (nopMetrics) Value(context.Context, model.ManagedService) (float64, error) {
	return 0, model.ErrNoMetricData
}

func TestDaemonLifecycleIntegration(t *testing.T) {
	// Injected tick source we fire by hand, so the loop steps deterministically.
	ticks := make(chan time.Time)
	src := func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }

	fs := &fakeSwarm{}
	cfg := config.Config{
		PollInterval: time.Hour,     // ignored: ticks are injected
		MetricsAddr:  "127.0.0.1:0", // ephemeral port, no conflicts
		DryRun:       true,          // safety: no mutations regardless
	}

	application, err := buildApp(cfg, appDeps{
		swarm:          fs,
		metrics:        nopMetrics{},
		clock:          port.SystemClock{},
		recorder:       port.NopRecorder{},
		metricsHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		logger:         integTestLogger(),
		reconcilerOpts: []reconciler.Option{reconciler.WithTickSource(src)},
	})
	if err != nil {
		t.Fatalf("buildApp: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	exit := make(chan int, 1)
	go func() { exit <- application.run(ctx) }()

	// Drive several ticks. A blocking send on the unbuffered channel only
	// completes once the loop is back at its select, so each observe finishes
	// before the next tick is delivered.
	const numTicks = 3
	for i := 0; i < numTicks; i++ {
		select {
		case ticks <- time.Now():
		case <-time.After(2 * time.Second):
			t.Fatal("reconcile loop did not consume a tick within 2s")
		}
	}

	cancel() // graceful stop (SIGTERM analogue)

	select {
	case code := <-exit:
		if code != 0 {
			t.Errorf("app.run exit code = %d, want 0 (clean shutdown)", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("app.run did not return within 3s after context cancel")
	}

	// Run observes once immediately, then once per tick.
	if got := fs.calls(); got < numTicks+1 {
		t.Errorf("ManagedServices called %d times, want >= %d (1 initial + %d ticks)", got, numTicks+1, numTicks)
	}
}
