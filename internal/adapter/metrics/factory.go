// Package metrics selects and constructs the daemon's MetricsProvider based on
// configuration. The result is a Router that dispatches per service.
package metrics

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/docker/docker/client"

	"github.com/Aleksey512/swarm-hpa/internal/adapter/metrics/distributed"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/metrics/dockerstats"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/metrics/prometheus"
	"github.com/Aleksey512/swarm-hpa/internal/config"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// New builds the daemon's metrics provider: a Router over dockerstats (always
// available), prometheus (built only when a Prometheus URL is configured), and
// the agents provider (built only when a snapshot source is supplied — i.e. in
// manager mode), defaulting unlabeled services to cfg.MetricsProvider.
//
// snapshot is the agent registry (nil outside manager mode). A nil snapshot with
// metrics_provider=agents is a misconfiguration and returns an error.
func New(cfg config.Config, cli *client.Client, snapshot distributed.Snapshotter, logger *slog.Logger) (port.MetricsProvider, error) {
	switch cfg.MetricsProvider {
	case config.ProviderDockerStats, config.ProviderPrometheus, config.ProviderAgents:
		// known default source
	default:
		return nil, fmt.Errorf("metrics: unknown provider %q", cfg.MetricsProvider)
	}

	ds := dockerstats.New(cli, logger)

	var pm port.MetricsProvider
	if url := strings.TrimSpace(cfg.PrometheusURL); url != "" {
		p, err := prometheus.New(url, cfg.PrometheusTimeout, logger)
		if err != nil {
			return nil, fmt.Errorf("metrics: build prometheus provider: %w", err)
		}
		pm = p
	} else if cfg.MetricsProvider == config.ProviderPrometheus {
		return nil, fmt.Errorf("metrics: metrics_provider=%s requires a prometheus URL", config.ProviderPrometheus)
	}

	var agents port.MetricsProvider
	if snapshot != nil {
		agents = distributed.New(snapshot, logger)
	} else if cfg.MetricsProvider == config.ProviderAgents {
		return nil, fmt.Errorf("metrics: metrics_provider=%s requires the agent registry (manager mode)", config.ProviderAgents)
	}

	return NewRouter(ds, pm, agents, cfg.MetricsProvider, logger), nil
}
