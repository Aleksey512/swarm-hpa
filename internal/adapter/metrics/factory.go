// Package metrics selects and constructs the daemon's MetricsProvider based on
// configuration. The result is a Router that dispatches per service.
package metrics

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/docker/docker/client"

	"github.com/Aleksey512/swarm-hpa/internal/adapter/metrics/dockerstats"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/metrics/prometheus"
	"github.com/Aleksey512/swarm-hpa/internal/config"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// New builds the daemon's metrics provider: a Router over dockerstats (always
// available) and prometheus (built only when a Prometheus URL is configured),
// defaulting unlabeled services to cfg.MetricsProvider.
func New(cfg config.Config, cli *client.Client, logger *slog.Logger) (port.MetricsProvider, error) {
	switch cfg.MetricsProvider {
	case config.ProviderDockerStats, config.ProviderPrometheus:
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

	return NewRouter(ds, pm, cfg.MetricsProvider, logger), nil
}
