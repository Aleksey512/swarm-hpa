package metrics

import (
	"testing"

	"github.com/Aleksey512/swarm-hpa/internal/config"
	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// fakeSnapshot satisfies distributed.Snapshotter for the agents-provider path.
type fakeSnapshot struct{}

func (fakeSnapshot) Snapshot() []model.AgentReport { return nil }

func TestFactoryDockerStats(t *testing.T) {
	p, err := New(config.Config{MetricsProvider: config.ProviderDockerStats}, nil, nil, nil)
	if err != nil || p == nil {
		t.Fatalf("dockerstats: provider=%v err=%v", p, err)
	}
}

func TestFactoryPrometheusRequiresURL(t *testing.T) {
	// prometheus as the global default with no URL is a misconfiguration.
	if _, err := New(config.Config{MetricsProvider: config.ProviderPrometheus}, nil, nil, nil); err == nil {
		t.Error("prometheus default without a URL must return an error")
	}
}

func TestFactoryPrometheusWithURL(t *testing.T) {
	p, err := New(config.Config{
		MetricsProvider: config.ProviderPrometheus,
		PrometheusURL:   "http://prometheus:9090",
	}, nil, nil, nil)
	if err != nil || p == nil {
		t.Fatalf("prometheus with URL: provider=%v err=%v", p, err)
	}
}

func TestFactoryAgentsRequiresRegistry(t *testing.T) {
	// agents as the global default with no snapshot source is a misconfiguration.
	if _, err := New(config.Config{MetricsProvider: config.ProviderAgents}, nil, nil, nil); err == nil {
		t.Error("agents default without a registry must return an error")
	}
}

func TestFactoryAgentsWithRegistry(t *testing.T) {
	p, err := New(config.Config{MetricsProvider: config.ProviderAgents}, nil, fakeSnapshot{}, nil)
	if err != nil || p == nil {
		t.Fatalf("agents with registry: provider=%v err=%v", p, err)
	}
}

func TestFactoryUnknownProvider(t *testing.T) {
	if _, err := New(config.Config{MetricsProvider: "bogus"}, nil, nil, nil); err == nil {
		t.Error("unknown provider must return an error")
	}
}
