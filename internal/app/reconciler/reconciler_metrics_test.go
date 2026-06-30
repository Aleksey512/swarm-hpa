package reconciler

import (
	"context"
	"errors"
	"testing"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// observeProvider is a metrics provider whose Value is recorded so the observe
// loop's metric-read branch can be asserted.
type observeProvider struct {
	val    float64
	err    error
	called int
}

func (p *observeProvider) Value(context.Context, model.ManagedService) (float64, error) {
	p.called++
	return p.val, p.err
}

func metricSvcFake() *healFake {
	return &healFake{
		svc: model.ManagedService{
			Ref:        model.ServiceRef{ID: "s1", Name: "web"},
			Replicated: true,
			Policy:     model.ServicePolicy{Enabled: true, Min: 1, Max: 5, Metric: "cpu", Target: 80},
		},
	}
}

func TestObserveReadsMetricValue(t *testing.T) {
	hf := metricSvcFake()
	mp := &observeProvider{val: 42}
	logger := discardLogger()
	guard := NewGuard(hf, NewCooldown(0, port.SystemClock{}), true, logger)
	rec := New(hf, mp, guard, port.SystemClock{}, testHealThreshold, logger)

	rec.observe(context.Background())
	if mp.called != 1 {
		t.Errorf("expected the metric to be read once per service, got %d", mp.called)
	}
}

func TestObserveToleratesNoMetricData(t *testing.T) {
	hf := metricSvcFake()
	mp := &observeProvider{err: model.ErrNoMetricData}
	logger := discardLogger()
	guard := NewGuard(hf, NewCooldown(0, port.SystemClock{}), true, logger)
	rec := New(hf, mp, guard, port.SystemClock{}, testHealThreshold, logger)

	rec.observe(context.Background()) // must not panic or stop
	if mp.called != 1 {
		t.Errorf("expected one metric read, got %d", mp.called)
	}
}

func TestObserveToleratesMetricError(t *testing.T) {
	hf := metricSvcFake()
	mp := &observeProvider{err: errors.New("provider boom")}
	logger := discardLogger()
	guard := NewGuard(hf, NewCooldown(0, port.SystemClock{}), true, logger)
	rec := New(hf, mp, guard, port.SystemClock{}, testHealThreshold, logger)

	rec.observe(context.Background()) // a provider error must not stop observation
	if mp.called != 1 {
		t.Errorf("expected one metric read, got %d", mp.called)
	}
}
