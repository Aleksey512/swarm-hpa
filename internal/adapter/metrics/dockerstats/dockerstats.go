package dockerstats

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dswarm "github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"

	"github.com/wmid/swarm-hpa/internal/core/model"
	"github.com/wmid/swarm-hpa/internal/core/port"
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
	compute, err := computeFor(svc.Policy.Metric)
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
		cid := containerID(t)
		if cid == "" {
			continue
		}
		stats, err := p.readStats(ctx, cid)
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

// computeFor maps a policy metric name to a stats compute function.
func computeFor(metric string) (func(container.StatsResponse) (float64, bool), error) {
	switch strings.ToLower(strings.TrimSpace(metric)) {
	case model.MetricCPU, "":
		return cpuPercent, nil
	case model.MetricMemory, "mem":
		return memPercent, nil
	default:
		return nil, fmt.Errorf("dockerstats: unsupported metric %q (want cpu|memory)", metric)
	}
}

func containerID(t dswarm.Task) string {
	if t.Status.ContainerStatus == nil {
		return ""
	}
	return t.Status.ContainerStatus.ContainerID
}

// readStats reads up to two stream frames for a container. The second frame
// carries PreCPUStats (the previous sample), which the CPU% formula needs;
// memory% needs only one. Returns the most recent frame read.
func (p *Provider) readStats(ctx context.Context, containerID string) (container.StatsResponse, error) {
	statsCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	resp, err := p.cli.ContainerStats(statsCtx, containerID, true)
	if err != nil {
		return container.StatsResponse{}, err
	}
	defer resp.Body.Close()

	dec := json.NewDecoder(resp.Body)
	var last container.StatsResponse
	for i := 0; i < 2; i++ {
		var s container.StatsResponse
		if err := dec.Decode(&s); err != nil {
			if i == 0 {
				return container.StatsResponse{}, err
			}
			break // at least one frame decoded
		}
		last = s
	}
	return last, nil
}
