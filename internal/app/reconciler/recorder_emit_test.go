package reconciler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// fakeRecorder records every port.Recorder call so emission can be asserted.
type fakeRecorder struct {
	ticks      int
	observed   []int
	scales     []string
	heals      []string
	suppressed []string // "action:reason"
	errors     []string
}

// compile-time proof the fake satisfies the port.
var _ port.Recorder = (*fakeRecorder)(nil)

func (f *fakeRecorder) ReconcileTick()         { f.ticks++ }
func (f *fakeRecorder) ObservedServices(n int) { f.observed = append(f.observed, n) }
func (f *fakeRecorder) ScaleApplied(s string)  { f.scales = append(f.scales, s) }
func (f *fakeRecorder) HealApplied(s string)   { f.heals = append(f.heals, s) }
func (f *fakeRecorder) ActionSuppressed(action, reason string) {
	f.suppressed = append(f.suppressed, action+":"+reason)
}
func (f *fakeRecorder) Error(stage string) { f.errors = append(f.errors, stage) }

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// emitController is a configurable SwarmController for observe() emission tests.
// It returns one managed service unless svcErr is set; Tasks/Nodes errors are
// injectable.
type emitController struct {
	svcErr   error
	tasksErr error
	nodesErr error
}

func (c emitController) ManagedServices(context.Context) ([]model.ManagedService, error) {
	if c.svcErr != nil {
		return nil, c.svcErr
	}
	return []model.ManagedService{{
		Ref:        model.ServiceRef{ID: "s1", Name: "web"},
		Replicated: true,
		Policy:     model.ServicePolicy{Enabled: true, Min: 1, Max: 3, Metric: "cpu", Target: 80},
		Autoscale:  true,
		Heal:       true,
	}}, nil
}
func (c emitController) Tasks(context.Context, string) ([]model.TaskView, error) {
	return nil, c.tasksErr
}
func (c emitController) Nodes(context.Context) ([]model.NodeView, error) { return nil, c.nodesErr }
func (emitController) Scale(context.Context, string, uint64) error       { return nil }
func (emitController) ForceUpdate(context.Context, string) error         { return nil }

func observeWith(c port.SwarmController, mp port.MetricsProvider, fr *fakeRecorder) {
	guard := NewGuard(c, NewCooldown(newFakeClock()), Cooldowns{}, true, fr, discardLogger())
	rec := New(c, mp, guard, newFakeClock(), testHealThreshold, fr, nil, 0, discardLogger())
	rec.observe(context.Background())
}

func TestReconcilerEmitsTickAndObserved(t *testing.T) {
	fr := &fakeRecorder{}
	observeWith(emitController{}, fakeProvider{err: model.ErrNoMetricData}, fr)
	if fr.ticks != 1 {
		t.Errorf("ticks = %d, want 1", fr.ticks)
	}
	if len(fr.observed) != 1 || fr.observed[0] != 1 {
		t.Errorf("observed = %v, want [1]", fr.observed)
	}
	if len(fr.errors) != 0 {
		t.Errorf("errors = %v, want none", fr.errors)
	}
}

func TestReconcilerEmitsErrorsByStage(t *testing.T) {
	cases := []struct {
		name      string
		ctrl      emitController
		provider  port.MetricsProvider
		wantStage string
	}{
		{name: "services", ctrl: emitController{svcErr: errors.New("boom")}, provider: fakeProvider{err: model.ErrNoMetricData}, wantStage: "services"},
		{name: "tasks", ctrl: emitController{tasksErr: errors.New("boom")}, provider: fakeProvider{err: model.ErrNoMetricData}, wantStage: "tasks"},
		{name: "nodes", ctrl: emitController{nodesErr: errors.New("boom")}, provider: fakeProvider{err: model.ErrNoMetricData}, wantStage: "nodes"},
		{name: "metric", ctrl: emitController{}, provider: fakeProvider{err: errors.New("boom")}, wantStage: "metric"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRecorder{}
			observeWith(tc.ctrl, tc.provider, fr)
			if !contains(fr.errors, tc.wantStage) {
				t.Errorf("errors = %v, want to contain %q", fr.errors, tc.wantStage)
			}
			if fr.ticks != 1 {
				t.Errorf("ticks = %d, want 1 (recorded even on error)", fr.ticks)
			}
		})
	}
}

func TestGuardEmitsAppliedActions(t *testing.T) {
	fr := &fakeRecorder{}
	g := NewGuard(&recordingController{}, NewCooldown(newFakeClock()), Cooldowns{}, false, fr, discardLogger())
	_ = g.Scale(context.Background(), replicatedSvc(2), 4)
	_ = g.Heal(context.Background(), replicatedSvc(2))
	if len(fr.scales) != 1 || fr.scales[0] != "web" {
		t.Errorf("scales = %v, want [web]", fr.scales)
	}
	if len(fr.heals) != 1 || fr.heals[0] != "web" {
		t.Errorf("heals = %v, want [web]", fr.heals)
	}
}

func TestGuardEmitsDryRunSuppressed(t *testing.T) {
	fr := &fakeRecorder{}
	g := NewGuard(&recordingController{}, NewCooldown(newFakeClock()), Cooldowns{}, true, fr, discardLogger())
	_ = g.Scale(context.Background(), replicatedSvc(2), 4)
	_ = g.Heal(context.Background(), replicatedSvc(2))
	if !contains(fr.suppressed, "scale:dry_run") || !contains(fr.suppressed, "heal:dry_run") {
		t.Errorf("suppressed = %v, want scale:dry_run and heal:dry_run", fr.suppressed)
	}
}

func TestGuardEmitsCooldownSuppressed(t *testing.T) {
	fr := &fakeRecorder{}
	g := NewGuard(&recordingController{}, NewCooldown(newFakeClock()), Cooldowns{ScaleUp: time.Minute, ScaleDown: time.Minute, Heal: time.Minute}, false, fr, discardLogger())
	svc := replicatedSvc(2)
	_ = g.Scale(context.Background(), svc, 4) // applied
	_ = g.Scale(context.Background(), svc, 5) // within cooldown -> suppressed
	if !contains(fr.suppressed, "scale:cooldown") {
		t.Errorf("suppressed = %v, want scale:cooldown", fr.suppressed)
	}
}
