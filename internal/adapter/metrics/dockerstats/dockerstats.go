package dockerstats

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dswarm "github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"

	"github.com/Aleksey512/swarm-hpa/internal/adapter/statsutil"
	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// callTimeout bounds each Docker API call.
const callTimeout = 10 * time.Second

// statsAPI is the subset of the Docker client this provider uses. Narrowing it
// to an interface lets tests substitute a fake without a live daemon.
type statsAPI interface {
	TaskList(ctx context.Context, options dswarm.TaskListOptions) ([]dswarm.Task, error)
	ContainerStats(ctx context.Context, containerID string, stream bool) (container.StatsResponseReader, error)
}

// Provider implements port.MetricsProvider using the Docker container stats API.
//
// IMPORTANT: ContainerStats is served by the local daemon, so in a multi-node
// Swarm this provider only sees containers on the daemon's node. It returns
// model.ErrNoMetricData when a service has no locally-readable stats;
// cross-node coverage is the Prometheus provider (a later milestone).
type Provider struct {
	cli    statsAPI
	logger *slog.Logger
}

// compile-time proof the provider satisfies the core port.
var _ port.MetricsProvider = (*Provider)(nil)

// New returns a dockerstats metrics provider over the given Docker client.
func New(cli *client.Client, logger *slog.Logger) *Provider {
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{cli: cli, logger: logger}
}

// Value averages the service's scaling metric across its locally-readable tasks.
func (p *Provider) Value(ctx context.Context, svc model.ManagedService) (float64, error) {
	compute, err := statsutil.ComputeFor(svc.Policy.Metric)
	if err != nil {
		return 0, err
	}

	listCtx, cancel := context.WithTimeout(ctx, callTimeout)
	f := filters.NewArgs(
		filters.Arg("service", svc.Ref.ID),
		filters.Arg("desired-state", "running"),
	)
	tasks, err := p.cli.TaskList(listCtx, dswarm.TaskListOptions{Filters: f})
	cancel()
	if err != nil {
		return 0, fmt.Errorf("dockerstats: task list for %s: %w", svc.Ref.Name, err)
	}

	var sum float64
	var n int
	for _, t := range tasks {
		cid := statsutil.ContainerID(t)
		if cid == "" {
			continue
		}
		stats, err := statsutil.ReadStats(ctx, p.cli, cid, callTimeout)
		if err != nil {
			p.logger.Debug("dockerstats: skipping task (stats unavailable, likely remote node)",
				"service", svc.Ref.Name, "container", cid, "err", err)
			continue
		}
		val, ok := compute(stats)
		if !ok {
			p.logger.Debug("dockerstats: skipping task (metric not computable)",
				"service", svc.Ref.Name, "container", cid)
			continue
		}
		p.logger.Debug("dockerstats: task metric",
			"service", svc.Ref.Name, "container", cid, "metric", svc.Policy.Metric, "value", val)
		sum += val
		n++
	}

	if n == 0 {
		return 0, model.ErrNoMetricData
	}
	avg := sum / float64(n)
	p.logger.Debug("dockerstats: service metric",
		"service", svc.Ref.Name, "metric", svc.Policy.Metric, "value", avg, "tasks", n)
	return avg, nil
}
