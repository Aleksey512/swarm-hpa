// Package metrics selects and constructs the daemon's MetricsProvider based on
// configuration.
package metrics

import (
	"fmt"
	"log/slog"

	"github.com/docker/docker/client"

	"github.com/Aleksey512/swarm-hpa/internal/adapter/metrics/dockerstats"
	"github.com/Aleksey512/swarm-hpa/internal/config"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// New constructs the metrics provider named by cfg.MetricsProvider.
func New(cfg config.Config, cli *client.Client, logger *slog.Logger) (port.MetricsProvider, error) {
	switch cfg.MetricsProvider {
	case config.ProviderDockerStats:
		return dockerstats.New(cli, logger), nil
	case config.ProviderPrometheus:
		return nil, fmt.Errorf("metrics: prometheus provider not implemented yet (planned for a later milestone)")
	default:
		return nil, fmt.Errorf("metrics: unknown provider %q", cfg.MetricsProvider)
	}
}
