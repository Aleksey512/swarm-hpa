package reconciler

import (
	"context"
	"errors"
	"testing"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// flakyController fails the first `failFirst` calls of one targeted stage with a
// transient error, then behaves normally. It serves a single scalable service
// and records Scale calls so recovery (a scale actually applied on a good tick)
// can be asserted.
type flakyController struct {
	stage      string // "services" | "tasks" | "nodes"
	failFirst  int
	calls      int
	svc        model.ManagedService
	scaleCalls int
}

func newFlaky(stage string, failFirst int, svc model.ManagedService) *flakyController {
	return &flakyController{stage: stage, failFirst: failFirst, svc: svc}
}

// failStage reports whether the given stage should return a transient error on
// this call, advancing the counter only for the targeted stage.
func (f *flakyController) failStage(stage string) bool {
	if f.stage != stage {
		return false
	}
	f.calls++
	return f.calls <= f.failFirst
}

func (f *flakyController) ManagedServices(context.Context) ([]model.ManagedService, error) {
	if f.failStage("services") {
		return nil, errors.New("transient: service list")
	}
	return []model.ManagedService{f.svc}, nil
}

func (f *flakyController) Tasks(context.Context, string) ([]model.TaskView, error) {
	if f.failStage("tasks") {
		return nil, errors.New("transient: task list")
	}
	return nil, nil
}

func (f *flakyController) Nodes(context.Context) ([]model.NodeView, error) {
	if f.failStage("nodes") {
		return nil, errors.New("transient: node list")
	}
	return nil, nil
}

func (f *flakyController) Scale(context.Context, string, uint64) error {
	f.scaleCalls++
	return nil
}

func (f *flakyController) ForceUpdate(context.Context, string) error { return nil }

// flakyProvider errors on its first `failFirst` calls, then returns val.
type flakyProvider struct {
	failFirst int
	calls     int
	val       float64
}

func (p *flakyProvider) Value(context.Context, model.ManagedService) (float64, error) {
	p.calls++
	if p.calls <= p.failFirst {
		return 0, errors.New("transient: metric")
	}
	return p.val, nil
}

func count(ss []string, s string) int {
	n := 0
	for _, x := range ss {
		if x == s {
			n++
		}
	}
	return n
}

// newResilienceReconciler builds a live-mutation reconciler (dry-run off, no
// cooldown) over the given controller/provider, recording into fr.
func newResilienceReconciler(c port.SwarmController, mp port.MetricsProvider, fr *fakeRecorder) *Reconciler {
	logger := discardLogger()
	guard := NewGuard(c, NewCooldown(port.SystemClock{}), Cooldowns{}, false, fr, logger)
	return New(c, mp, guard, port.SystemClock{}, testHealThreshold, fr, nil, 0, logger)
}

func TestObserveToleratesServicesBurstThenRecovers(t *testing.T) {
	fc := newFlaky("services", 3, scaleSvc(2))
	fr := &fakeRecorder{}
	rec := newResilienceReconciler(fc, fakeProvider{val: 160}, fr) // 160/80 = 2x -> scale to 4

	for i := 0; i < 4; i++ { // 3 failing ticks + 1 recovering tick
		rec.observe(context.Background())
	}

	if got := count(fr.errors, "services"); got != 3 {
		t.Errorf("recorder.Error(services) = %d, want 3", got)
	}
	if fc.scaleCalls != 1 {
		t.Errorf("expected recovery to apply exactly one scale on the good tick, got %d", fc.scaleCalls)
	}
	if fr.ticks != 4 {
		t.Errorf("ReconcileTick = %d, want 4 (recorded every tick, incl. failures)", fr.ticks)
	}
}

func TestObserveToleratesNodesErrorStillScales(t *testing.T) {
	fc := newFlaky("nodes", 1000, scaleSvc(2)) // Nodes always errors this run
	fr := &fakeRecorder{}
	rec := newResilienceReconciler(fc, fakeProvider{val: 160}, fr)

	rec.observe(context.Background())

	if !contains(fr.errors, "nodes") {
		t.Errorf("expected recorder.Error(nodes), got %v", fr.errors)
	}
	if fc.scaleCalls != 1 {
		t.Errorf("a Nodes error disables healing for the tick but must not block scaling; got %d scale calls", fc.scaleCalls)
	}
}

func TestObserveToleratesTasksBurstThenRecovers(t *testing.T) {
	fc := newFlaky("tasks", 2, scaleSvc(2))
	fr := &fakeRecorder{}
	rec := newResilienceReconciler(fc, fakeProvider{val: 160}, fr)

	for i := 0; i < 3; i++ { // 2 failing ticks + 1 recovering tick
		rec.observe(context.Background())
	}

	if got := count(fr.errors, "tasks"); got != 2 {
		t.Errorf("recorder.Error(tasks) = %d, want 2", got)
	}
	if fc.scaleCalls != 1 {
		t.Errorf("expected recovery to scale once after tasks stopped erroring, got %d", fc.scaleCalls)
	}
}

func TestObserveToleratesMetricBurstThenRecovers(t *testing.T) {
	sf := &scaleFake{svc: scaleSvc(2)}
	fr := &fakeRecorder{}
	mp := &flakyProvider{failFirst: 2, val: 160}
	logger := discardLogger()
	guard := NewGuard(sf, NewCooldown(port.SystemClock{}), Cooldowns{}, false, fr, logger)
	rec := New(sf, mp, guard, port.SystemClock{}, testHealThreshold, fr, nil, 0, logger)

	for i := 0; i < 3; i++ { // 2 metric errors + 1 good read
		rec.observe(context.Background())
	}

	if got := count(fr.errors, "metric"); got != 2 {
		t.Errorf("recorder.Error(metric) = %d, want 2", got)
	}
	if sf.scaleCalls != 1 || sf.lastTo != 4 {
		t.Errorf("expected one scale to 4 after the metric recovered, got calls=%d lastTo=%d", sf.scaleCalls, sf.lastTo)
	}
}
