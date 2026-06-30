package metrics

import (
	"testing"

	"github.com/Aleksey512/swarm-hpa/internal/config"
)

func TestFactoryDockerStats(t *testing.T) {
	p, err := New(config.Config{MetricsProvider: config.ProviderDockerStats}, nil, nil)
	if err != nil || p == nil {
		t.Fatalf("dockerstats: provider=%v err=%v", p, err)
	}
}

func TestFactoryPrometheusRequiresURL(t *testing.T) {
	// prometheus as the global default with no URL is a misconfiguration.
	if _, err := New(config.Config{MetricsProvider: config.ProviderPrometheus}, nil, nil); err == nil {
		t.Error("prometheus default without a URL must return an error")
	}
}

func TestFactoryPrometheusWithURL(t *testing.T) {
	p, err := New(config.Config{
		MetricsProvider: config.ProviderPrometheus,
		PrometheusURL:   "http://prometheus:9090",
	}, nil, nil)
	if err != nil || p == nil {
		t.Fatalf("prometheus with URL: provider=%v err=%v", p, err)
	}
}

func TestFactoryUnknownProvider(t *testing.T) {
	if _, err := New(config.Config{MetricsProvider: "bogus"}, nil, nil); err == nil {
		t.Error("unknown provider must return an error")
	}
}
