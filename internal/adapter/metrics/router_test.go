package metrics

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/Aleksey512/swarm-hpa/internal/config"
	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// recordProvider records whether it was called so router dispatch is observable.
type recordProvider struct {
	val    float64
	err    error
	called bool
}

func (r *recordProvider) Value(context.Context, model.ManagedService) (float64, error) {
	r.called = true
	return r.val, r.err
}

func svcSource(source string) model.ManagedService {
	return model.ManagedService{
		Ref:    model.ServiceRef{ID: "s1", Name: "web"},
		Policy: model.ServicePolicy{Source: source},
	}
}

func TestRouterDispatch(t *testing.T) {
	cases := []struct {
		name          string
		source        string
		defaultSource string
		wantDocker    bool
		wantProm      bool
		wantAgents    bool
	}{
		{name: "explicit dockerstats", source: config.ProviderDockerStats, defaultSource: config.ProviderPrometheus, wantDocker: true},
		{name: "explicit prometheus", source: config.ProviderPrometheus, defaultSource: config.ProviderDockerStats, wantProm: true},
		{name: "explicit agents", source: config.ProviderAgents, defaultSource: config.ProviderDockerStats, wantAgents: true},
		{name: "empty falls back to default dockerstats", source: "", defaultSource: config.ProviderDockerStats, wantDocker: true},
		{name: "empty falls back to default prometheus", source: "", defaultSource: config.ProviderPrometheus, wantProm: true},
		{name: "empty falls back to default agents", source: "", defaultSource: config.ProviderAgents, wantAgents: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ds, pm, ag := &recordProvider{val: 1}, &recordProvider{val: 2}, &recordProvider{val: 3}
			r := NewRouter(ds, pm, ag, tc.defaultSource, discardLogger())
			if _, err := r.Value(context.Background(), svcSource(tc.source)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ds.called != tc.wantDocker {
				t.Errorf("dockerstats called = %v, want %v", ds.called, tc.wantDocker)
			}
			if pm.called != tc.wantProm {
				t.Errorf("prometheus called = %v, want %v", pm.called, tc.wantProm)
			}
			if ag.called != tc.wantAgents {
				t.Errorf("agents called = %v, want %v", ag.called, tc.wantAgents)
			}
		})
	}
}

func TestRouterPrometheusUnconfigured(t *testing.T) {
	r := NewRouter(&recordProvider{}, nil, nil, config.ProviderDockerStats, discardLogger())
	if _, err := r.Value(context.Background(), svcSource(config.ProviderPrometheus)); err == nil {
		t.Error("source=prometheus with no prometheus provider must error")
	}
}

func TestRouterAgentsUnconfigured(t *testing.T) {
	r := NewRouter(&recordProvider{}, nil, nil, config.ProviderDockerStats, discardLogger())
	if _, err := r.Value(context.Background(), svcSource(config.ProviderAgents)); err == nil {
		t.Error("source=agents with no agents provider must error")
	}
}

func TestRouterUnknownSource(t *testing.T) {
	r := NewRouter(&recordProvider{}, &recordProvider{}, &recordProvider{}, config.ProviderDockerStats, discardLogger())
	if _, err := r.Value(context.Background(), svcSource("graphite")); err == nil {
		t.Error("unknown source must error")
	}
}

func TestRouterPropagatesProviderError(t *testing.T) {
	ds := &recordProvider{err: errors.New("boom")}
	r := NewRouter(ds, nil, nil, config.ProviderDockerStats, discardLogger())
	if _, err := r.Value(context.Background(), svcSource(config.ProviderDockerStats)); err == nil {
		t.Error("provider error must propagate through the router")
	}
}
