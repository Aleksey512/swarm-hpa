package reconciler

import (
	"context"
	"testing"

	"github.com/wmid/swarm-hpa/internal/core/model"
	"github.com/wmid/swarm-hpa/internal/core/port"
)

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
	}
}

func TestObserveAppliesScalingDecision(t *testing.T) {
	sf := &scaleFake{svc: scaleSvc(2)}
	mp := &observeProvider{val: 160} // 160/80 = 2x -> desired 4
	logger := discardLogger()
	guard := NewGuard(sf, NewCooldown(0, port.SystemClock{}), false, logger) // dry-run OFF
	rec := New(sf, mp, guard, port.SystemClock{}, testHealThreshold, logger)

	rec.observe(context.Background())

	if sf.scaleCalls != 1 {
		t.Fatalf("expected exactly one Scale call, got %d", sf.scaleCalls)
	}
	if sf.lastTo != 4 {
		t.Errorf("scaled to %d, want 4 (autoscaler-computed)", sf.lastTo)
	}
}

func TestObserveDryRunSuppressesScale(t *testing.T) {
	sf := &scaleFake{svc: scaleSvc(2)}
	mp := &observeProvider{val: 160}
	logger := discardLogger()
	guard := NewGuard(sf, NewCooldown(0, port.SystemClock{}), true, logger) // dry-run ON
	rec := New(sf, mp, guard, port.SystemClock{}, testHealThreshold, logger)

	rec.observe(context.Background())

	if sf.scaleCalls != 0 {
		t.Errorf("dry-run must make zero Scale calls, got %d", sf.scaleCalls)
	}
}
