package reconciler

import (
	"context"
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// stuckPending builds a task pending long enough to trip the healer threshold.
func stuckPending(now time.Time) model.TaskView {
	return model.TaskView{
		ID: "t1", ServiceID: "s1",
		State: model.TaskStatePending, DesiredState: model.TaskStateRunning,
		Since: now.Add(-10 * time.Minute),
	}
}

// A heal-only service (swarm.autoscaler.heal=true, no autoscaler policy) must be
// healed when stuck but must never read metrics or attempt scaling.
func TestObserveHealOnlyHealsWithoutScaling(t *testing.T) {
	clk := newFakeClock()
	svc := constrainedSvc()
	svc.Autoscale = false              // heal-only
	svc.Policy = model.ServicePolicy{} // carries no autoscaler policy

	hf := &healFake{svc: svc, tasks: []model.TaskView{stuckPending(clk.now)}, nodes: []model.NodeView{activeNodeN1()}}
	mp := &observeProvider{val: 999} // would scale hard IF autoscale were on
	logger := discardLogger()
	guard := NewGuard(hf, NewCooldown(clk), Cooldowns{}, false, port.NopRecorder{}, logger) // dry-run OFF
	rec := New(hf, mp, guard, clk, 2*time.Minute, port.NopRecorder{}, nil, 0, logger)

	rec.observe(context.Background())

	if mp.called != 0 {
		t.Errorf("heal-only service must not read metrics, got %d reads", mp.called)
	}
	if hf.forceCalls != 1 {
		t.Errorf("heal-only stuck service should be healed once, got %d", hf.forceCalls)
	}
}

// An autoscaled service that opted out of healing (heal=false) must still read
// its metric but must never be force-updated, even when the stuck signature holds.
func TestObserveAutoscaleOnlyDoesNotHeal(t *testing.T) {
	clk := newFakeClock()
	svc := constrainedSvc() // valid policy, Autoscale=true
	svc.Heal = false        // explicit heal opt-out

	hf := &healFake{svc: svc, tasks: []model.TaskView{stuckPending(clk.now)}, nodes: []model.NodeView{activeNodeN1()}}
	mp := &observeProvider{err: model.ErrNoMetricData} // metric read attempted; no scale
	logger := discardLogger()
	guard := NewGuard(hf, NewCooldown(clk), Cooldowns{}, false, port.NopRecorder{}, logger)
	rec := New(hf, mp, guard, clk, 2*time.Minute, port.NopRecorder{}, nil, 0, logger)

	rec.observe(context.Background())

	if hf.forceCalls != 0 {
		t.Errorf("heal=false service must not be healed even when stuck, got %d", hf.forceCalls)
	}
	if mp.called != 1 {
		t.Errorf("autoscale-only service should read the metric once, got %d", mp.called)
	}
}
