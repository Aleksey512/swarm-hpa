package metrics

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Aleksey512/swarm-hpa/internal/config"
	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// Router is the top-level MetricsProvider injected into the reconciler. It
// dispatches each service to a concrete provider chosen by its
// swarm.autoscaler.source label, falling back to the daemon's global default
// when the label is empty. This is the per-service "which metric source" knob;
// the core port stays a single MetricsProvider.
type Router struct {
	dockerstats   port.MetricsProvider
	prometheus    port.MetricsProvider // nil when no Prometheus URL is configured
	agents        port.MetricsProvider // nil outside manager mode (no agent registry)
	defaultSource string
	logger        *slog.Logger
}

// compile-time proof the router satisfies the core port.
var _ port.MetricsProvider = (*Router)(nil)

// NewRouter builds a Router over the available providers. prometheus/agents may
// be nil (no Prometheus URL configured; no agent registry); a service then
// requesting that source gets a descriptive error rather than a silent wrong
// scale. A nil logger falls back to slog.Default.
func NewRouter(dockerstats, prometheus, agents port.MetricsProvider, defaultSource string, logger *slog.Logger) *Router {
	if logger == nil {
		logger = slog.Default()
	}
	return &Router{
		dockerstats:   dockerstats,
		prometheus:    prometheus,
		agents:        agents,
		defaultSource: defaultSource,
		logger:        logger,
	}
}

// Value resolves the service's metric source (its label, else the global
// default) and delegates to the matching provider.
func (r *Router) Value(ctx context.Context, svc model.ManagedService) (float64, error) {
	source := svc.Policy.Source
	if source == "" {
		source = r.defaultSource
	}
	r.logger.Debug("metrics: routing service", "service", svc.Ref.Name, "source", source)

	switch source {
	case config.ProviderDockerStats:
		if r.dockerstats == nil {
			return 0, fmt.Errorf("metrics: dockerstats provider unavailable for service %s", svc.Ref.Name)
		}
		return r.dockerstats.Value(ctx, svc)
	case config.ProviderPrometheus:
		if r.prometheus == nil {
			return 0, fmt.Errorf("metrics: service %s requests source=prometheus but PROMETHEUS_URL is not configured", svc.Ref.Name)
		}
		return r.prometheus.Value(ctx, svc)
	case config.ProviderAgents:
		if r.agents == nil {
			return 0, fmt.Errorf("metrics: service %s requests source=agents but the agent registry is unavailable (manager mode only)", svc.Ref.Name)
		}
		return r.agents.Value(ctx, svc)
	default:
		return 0, fmt.Errorf("metrics: service %s has unknown source %q", svc.Ref.Name, source)
	}
}
