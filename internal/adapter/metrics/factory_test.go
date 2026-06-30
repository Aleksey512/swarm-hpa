package metrics

import (
	"testing"

	"github.com/wmid/swarm-hpa/internal/config"
)

func TestFactoryDockerStats(t *testing.T) {
	p, err := New(config.Config{MetricsProvider: config.ProviderDockerStats}, nil, nil)
	if err != nil || p == nil {
		t.Fatalf("dockerstats: provider=%v err=%v", p, err)
	}
}

func TestFactoryPrometheusNotImplemented(t *testing.T) {
	if _, err := New(config.Config{MetricsProvider: config.ProviderPrometheus}, nil, nil); err == nil {
		t.Error("prometheus provider must return a not-implemented error")
	}
}

func TestFactoryUnknownProvider(t *testing.T) {
	if _, err := New(config.Config{MetricsProvider: "bogus"}, nil, nil); err == nil {
		t.Error("unknown provider must return an error")
	}
}
