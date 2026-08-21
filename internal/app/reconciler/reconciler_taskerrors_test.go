package reconciler

import (
	"context"
	"errors"
	"testing"
	"time"

	apptaskerrors "github.com/Aleksey512/swarm-hpa/internal/app/taskerrors"
	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
	coretaskerrors "github.com/Aleksey512/swarm-hpa/internal/core/taskerrors"
)

// fakeSwarmRead is a configurable port.SwarmRead for the task-error tests.
type fakeSwarmRead struct {
	tasks    []model.TaskView
	services []model.LiveService
	tasksErr error
	svcsErr  error
}

func (f fakeSwarmRead) AllTasks(context.Context) ([]model.TaskView, error) {
	return f.tasks, f.tasksErr
}

func (f fakeSwarmRead) AllServices(context.Context) ([]model.LiveService, error) {
	return f.services, f.svcsErr
}

// newTaskErrorsReconciler wires a Reconciler with the task-error feature on.
func newTaskErrorsReconciler(fc port.SwarmController, read port.SwarmRead, sink *apptaskerrors.Tracker) *Reconciler {
	logger := discardLogger()
	guard := NewGuard(fc, NewCooldown(port.SystemClock{}), Cooldowns{}, true, port.NopRecorder{}, logger)
	return New(fc, fakeProvider{err: model.ErrNoMetricData}, guard, port.SystemClock{},
		testHealThreshold, port.NopRecorder{}, nil, 0, logger,
		WithTaskErrors(sink, read))
}

func TestObserveTaskErrorsRecordsVxlan(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	read := fakeSwarmRead{
		tasks: []model.TaskView{
			{ID: "t-ok", ServiceID: "s1", Slot: 1, State: "running", DesiredState: "running"},
			{ID: "t-vxlan", ServiceID: "s1", Slot: 2, State: "rejected", DesiredState: "shutdown",
				Err:   `network sandbox join failed: subnet sandbox join failed for "10.0.18.0/24": error creating vxlan interface: file exists`,
				Since: now.Add(-time.Minute)},
		},
		services: []model.LiveService{
			{ID: "s1", Name: "admin_analytics", StackNamespace: "admin"},
		},
	}
	sink := apptaskerrors.NewTracker(port.SystemClock{}, discardLogger())
	rec := newTaskErrorsReconciler(fakeController{services: []model.ManagedService{}}, read, sink)

	rec.observeTaskErrors(context.Background())

	got := sink.WindowSnapshot(now, 5*time.Minute)
	want := map[string]map[string]int{
		"admin_analytics": {string(coretaskerrors.ClassVxlanFileExists): 1},
	}
	if len(got) != 1 || got["admin_analytics"][string(coretaskerrors.ClassVxlanFileExists)] != 1 {
		t.Errorf("snapshot = %v, want %v", got, want)
	}
}

func TestObserveTaskErrorsCleanCluster(t *testing.T) {
	read := fakeSwarmRead{
		tasks: []model.TaskView{{ID: "t1", ServiceID: "s1", State: "running", DesiredState: "running"}},
	}
	sink := apptaskerrors.NewTracker(port.SystemClock{}, discardLogger())
	rec := newTaskErrorsReconciler(fakeController{}, read, sink)

	rec.observeTaskErrors(context.Background())

	got := sink.WindowSnapshot(time.Now(), 5*time.Minute)
	if len(got) != 0 {
		t.Errorf("clean cluster must record nothing, got %v", got)
	}
}

func TestObserveTaskErrorsTaskListFailureDegrades(t *testing.T) {
	read := fakeSwarmRead{tasksErr: errors.New("docker down")}
	sink := apptaskerrors.NewTracker(port.SystemClock{}, discardLogger())
	rec := newTaskErrorsReconciler(fakeController{}, read, sink)

	// Must not panic; the tracker stays empty and the loop continues.
	rec.observeTaskErrors(context.Background())

	got := sink.WindowSnapshot(time.Now(), 5*time.Minute)
	if len(got) != 0 {
		t.Errorf("failed read must record nothing, got %v", got)
	}
}

func TestObserveTaskErrorsServiceListFailureDegrades(t *testing.T) {
	read := fakeSwarmRead{
		tasks:   []model.TaskView{{ID: "t1", ServiceID: "s1", Err: "boom", Since: time.Now()}},
		svcsErr: errors.New("docker down"),
	}
	sink := apptaskerrors.NewTracker(port.SystemClock{}, discardLogger())
	rec := newTaskErrorsReconciler(fakeController{}, read, sink)

	rec.observeTaskErrors(context.Background())

	got := sink.WindowSnapshot(time.Now(), 5*time.Minute)
	if len(got) != 0 {
		t.Errorf("failed service read must record nothing, got %v", got)
	}
}

func TestObserveTaskErrorsDisabledWithoutOption(t *testing.T) {
	// Without WithTaskErrors the reconciler must not touch the reader at all.
	logger := discardLogger()
	guard := NewGuard(fakeController{}, NewCooldown(port.SystemClock{}), Cooldowns{}, true, port.NopRecorder{}, logger)
	rec := New(fakeController{}, fakeProvider{err: model.ErrNoMetricData}, guard,
		port.SystemClock{}, testHealThreshold, port.NopRecorder{}, nil, 0, logger)

	rec.observeTaskErrors(context.Background()) // no panic, no reads = pass
}

func TestJoinTaskEventsUnknownServiceFallsBackToID(t *testing.T) {
	tasks := []model.TaskView{
		{ID: "t1", ServiceID: "ghost-svc", Slot: 1, Err: "boom", Since: time.Now()},
	}
	events, byClass := joinTaskEvents(tasks, nil) // service listing empty
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if events[0].ServiceName != "ghost-svc" {
		t.Errorf("ServiceName = %q, want fallback to ServiceID", events[0].ServiceName)
	}
	if byClass[coretaskerrors.ClassOther] != 1 {
		t.Errorf("byClass = %v, want other=1", byClass)
	}
}

func TestJoinTaskEventsSlotsAndClasses(t *testing.T) {
	now := time.Now()
	tasks := []model.TaskView{
		{ID: "t1", ServiceID: "s1", Slot: 1, Err: "error creating vxlan interface: file exists", Since: now},
		{ID: "t2", ServiceID: "s1", Slot: 1, Err: "error creating vxlan interface: file exists", Since: now},
		{ID: "t3", ServiceID: "s2", Slot: 2, Err: "No such container: x", Since: now},
	}
	services := []model.LiveService{
		{ID: "s1", Name: "stack_web"},
		{ID: "s2", Name: "stack_api"},
	}
	events, byClass := joinTaskEvents(tasks, services)
	if len(events) != 3 {
		t.Fatalf("want 3 events, got %d", len(events))
	}
	if events[0].ServiceName != "stack_web" || events[2].ServiceName != "stack_api" {
		t.Errorf("join wrong: %+v / %+v", events[0], events[2])
	}
	if byClass[coretaskerrors.ClassVxlanFileExists] != 2 || byClass[coretaskerrors.ClassOther] != 1 {
		t.Errorf("byClass = %v", byClass)
	}
}
