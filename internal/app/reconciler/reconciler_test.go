package reconciler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/wmid/swarm-hpa/internal/core/model"
	"github.com/wmid/swarm-hpa/internal/core/port"
)

// fakeController is a configurable port.SwarmController for tests (no live daemon).
type fakeController struct {
	services []model.ManagedService
	tasks    map[string][]model.TaskView
	svcErr   error
}

func (f fakeController) ManagedServices(context.Context) ([]model.ManagedService, error) {
	return f.services, f.svcErr
}

func (f fakeController) Tasks(_ context.Context, serviceID string) ([]model.TaskView, error) {
	return f.tasks[serviceID], nil
}

func (f fakeController) Nodes(context.Context) ([]model.NodeView, error) { return nil, nil }

func (f fakeController) Scale(context.Context, string, uint64) error { return nil }
func (f fakeController) ForceUpdate(context.Context, string) error   { return nil }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeProvider is a configurable port.MetricsProvider for tests.
type fakeProvider struct {
	val float64
	err error
}

func (f fakeProvider) Value(context.Context, model.ManagedService) (float64, error) {
	return f.val, f.err
}

// testHealThreshold is the heal threshold used across reconciler tests where the
// exact value is irrelevant to the assertion.
const testHealThreshold = time.Minute

// newTestReconciler builds a Reconciler with a dry-run guard, no cooldown, and a
// provider that reports no metric data.
func newTestReconciler(fc port.SwarmController) *Reconciler {
	logger := discardLogger()
	guard := NewGuard(fc, NewCooldown(0, port.SystemClock{}), true, logger)
	return New(fc, fakeProvider{err: model.ErrNoMetricData}, guard, port.SystemClock{}, testHealThreshold, logger)
}

// runUntilCancel runs rec.Run with a long interval, cancels immediately, and
// returns the loop's error (expected nil) or fails on timeout.
func runUntilCancel(t *testing.T, rec *Reconciler) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rec.Run(ctx, time.Hour) }()
	cancel()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after context cancel")
		return nil
	}
}

func TestRunExitsOnCancel(t *testing.T) {
	rec := newTestReconciler(fakeController{})
	if err := runUntilCancel(t, rec); err != nil {
		t.Errorf("Run returned %v, want nil on cancel", err)
	}
}

func TestCountStates(t *testing.T) {
	tasks := []model.TaskView{
		{State: model.TaskStatePending},
		{State: model.TaskStateRunning},
		{State: model.TaskStateRunning},
		{State: "failed"},
	}
	pending, running := countStates(tasks)
	if pending != 1 || running != 2 {
		t.Errorf("pending=%d running=%d, want 1/2", pending, running)
	}
}

func TestRunObservesServicesAndExits(t *testing.T) {
	fc := fakeController{
		services: []model.ManagedService{{
			Ref:         model.ServiceRef{ID: "s1", Name: "web"},
			Replicas:    2,
			Replicated:  true,
			Policy:      model.ServicePolicy{Enabled: true, Min: 1, Max: 5, Metric: "cpu", Target: 80},
			Constraints: []string{"node.labels.nodeNum==1"},
		}},
		tasks: map[string][]model.TaskView{
			"s1": {
				{ID: "t1", ServiceID: "s1", State: model.TaskStatePending, DesiredState: model.TaskStateRunning, Err: "no suitable node"},
				{ID: "t2", ServiceID: "s1", State: model.TaskStateRunning, DesiredState: model.TaskStateRunning},
			},
		},
	}
	rec := newTestReconciler(fc)
	if err := runUntilCancel(t, rec); err != nil {
		t.Errorf("Run returned %v, want nil", err)
	}
}

func TestRunToleratesObserveError(t *testing.T) {
	fc := fakeController{svcErr: errors.New("boom")}
	rec := newTestReconciler(fc)
	if err := runUntilCancel(t, rec); err != nil {
		t.Errorf("observe error must not propagate to Run; got %v", err)
	}
}
