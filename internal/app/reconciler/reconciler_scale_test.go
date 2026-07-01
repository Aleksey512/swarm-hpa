package reconciler

import (
	"context"
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// seqProvider returns a scripted sequence of metric values, repeating the last.
type seqProvider struct {
	vals []float64
	i    int
}

func (p *seqProvider) Value(context.Context, model.ManagedService) (float64, error) {
	v := p.vals[p.i]
	if p.i < len(p.vals)-1 {
		p.i++
	}
	return v, nil
}

// scaleFake serves one managed service (no tasks) and records Scale calls so the
// decision -> apply path can be asserted end-to-end.
type scaleFake struct {
	svc        model.ManagedService
	scaleCalls int
	lastTo     uint64
}

func (s *scaleFake) ManagedServices(context.Context) ([]model.ManagedService, error) {
	return []model.ManagedService{s.svc}, nil
}
func (s *scaleFake) Tasks(context.Context, string) ([]model.TaskView, error) { return nil, nil }
func (s *scaleFake) Nodes(context.Context) ([]model.NodeView, error)         { return nil, nil }
func (s *scaleFake) Scale(_ context.Context, _ string, replicas uint64) error {
	s.scaleCalls++
	s.lastTo = replicas
	return nil
}
func (s *scaleFake) ForceUpdate(context.Context, string) error { return nil }

func scaleSvc(replicas uint64) model.ManagedService {
	return model.ManagedService{
		Ref:        model.ServiceRef{ID: "s1", Name: "web"},
		Replicas:   replicas,
		Replicated: true,
		Policy:     model.ServicePolicy{Enabled: true, Min: 1, Max: 10, Metric: "cpu", Target: 80},
		Autoscale:  true,
		Heal:       true,
	}
}

func TestObserveAppliesScalingDecision(t *testing.T) {
	sf := &scaleFake{svc: scaleSvc(2)}
	mp := &observeProvider{val: 160} // 160/80 = 2x -> desired 4
	logger := discardLogger()
	guard := NewGuard(sf, NewCooldown(port.SystemClock{}), Cooldowns{}, false, port.NopRecorder{}, logger) // dry-run OFF
	rec := New(sf, mp, guard, port.SystemClock{}, testHealThreshold, port.NopRecorder{}, nil, 0, logger)

	rec.observe(context.Background())

	if sf.scaleCalls != 1 {
		t.Fatalf("expected exactly one Scale call, got %d", sf.scaleCalls)
	}
	if sf.lastTo != 4 {
		t.Errorf("scaled to %d, want 4 (autoscaler-computed)", sf.lastTo)
	}
}

func TestObserveAppliesStepLimit(t *testing.T) {
	sf := &scaleFake{svc: scaleSvc(2)}
	mp := &observeProvider{val: 160} // 160/80 = 2x -> desired 4
	logger := discardLogger()
	guard := NewGuard(sf, NewCooldown(port.SystemClock{}), Cooldowns{}, false, port.NopRecorder{}, logger)
	// maxStep = 1: the 2 -> 4 jump is capped to 2 -> 3.
	rec := New(sf, mp, guard, port.SystemClock{}, testHealThreshold, port.NopRecorder{}, NewStabilizer(0), 1, logger)

	rec.observe(context.Background())

	if sf.scaleCalls != 1 || sf.lastTo != 3 {
		t.Errorf("step-limited scale: calls=%d lastTo=%d, want 1/3", sf.scaleCalls, sf.lastTo)
	}
}

func TestObserveStabilizesScaleDown(t *testing.T) {
	clk := newFakeClock()
	sf := &scaleFake{svc: scaleSvc(5)}
	// tick 1: ratio 2 -> wants 10 (scale up, applied); tick 2: ratio 0.5 -> wants 3
	// (scale down), but stabilization holds it at the recent max within the window.
	mp := &seqProvider{vals: []float64{160, 40}}
	logger := discardLogger()
	guard := NewGuard(sf, NewCooldown(clk), Cooldowns{}, false, port.NopRecorder{}, logger)
	rec := New(sf, mp, guard, clk, testHealThreshold, port.NopRecorder{}, NewStabilizer(time.Minute), 0, logger)

	rec.observe(context.Background()) // scales up to 10
	rec.observe(context.Background()) // scale-down held by the stabilization window

	if sf.scaleCalls != 1 {
		t.Errorf("the scale-down should be held by stabilization; got %d scale calls", sf.scaleCalls)
	}
	if sf.lastTo != 10 {
		t.Errorf("last applied target = %d, want 10 (scale-down was held)", sf.lastTo)
	}
}

func TestObserveDryRunSuppressesScale(t *testing.T) {
	sf := &scaleFake{svc: scaleSvc(2)}
	mp := &observeProvider{val: 160}
	logger := discardLogger()
	guard := NewGuard(sf, NewCooldown(port.SystemClock{}), Cooldowns{}, true, port.NopRecorder{}, logger) // dry-run ON
	rec := New(sf, mp, guard, port.SystemClock{}, testHealThreshold, port.NopRecorder{}, nil, 0, logger)

	rec.observe(context.Background())

	if sf.scaleCalls != 0 {
		t.Errorf("dry-run must make zero Scale calls, got %d", sf.scaleCalls)
	}
}
