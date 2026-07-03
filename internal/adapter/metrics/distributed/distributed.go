// Package distributed implements port.MetricsProvider by aggregating the
// per-task metrics reported by the per-node agent fleet (held in the manager's
// agent registry). Unlike the dockerstats provider — which can only read
// ContainerStats for tasks on the manager's own node — it sees tasks on ALL
// nodes, so Docker-stats-style autoscaling works across the whole cluster.
package distributed

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// Snapshotter yields the current live agent reports. Implemented by
// app/registry.Registry; taken as an interface so this adapter does not import
// the app package.
type Snapshotter interface {
	Snapshot() []model.AgentReport
}

// Provider aggregates agent-reported task metrics per service.
type Provider struct {
	source Snapshotter
	logger *slog.Logger
}

// compile-time proof the provider satisfies the core port.
var _ port.MetricsProvider = (*Provider)(nil)

// New returns a distributed metrics provider backed by the given snapshot source.
func New(source Snapshotter, logger *slog.Logger) *Provider {
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{source: source, logger: logger}
}

// Value averages the service's scaling metric across every task the agents have
// reported for it, cluster-wide. It returns model.ErrNoMetricData when no live
// task metrics exist for the service (so the reconciler skips it, consistent
// with the other providers), and an error only for an unsupported metric.
func (p *Provider) Value(_ context.Context, svc model.ManagedService) (float64, error) {
	selector, err := metricSelector(svc.Policy.Metric)
	if err != nil {
		return 0, err
	}

	reports := p.source.Snapshot()
	var sum float64
	var n, nodes int
	for _, rep := range reports {
		var contributed bool
		for _, tm := range rep.Tasks {
			if tm.ServiceID != svc.Ref.ID {
				continue
			}
			sum += selector(tm)
			n++
			contributed = true
		}
		if contributed {
			nodes++
		}
	}

	if n == 0 {
		p.logger.Debug("distributed: no live task metrics for service",
			"service", svc.Ref.Name, "agents", len(reports))
		return 0, model.ErrNoMetricData
	}
	avg := sum / float64(n)
	p.logger.Debug("distributed: service metric",
		"service", svc.Ref.Name, "metric", svc.Policy.Metric, "value", avg, "tasks", n, "nodes", nodes)
	return avg, nil
}

// metricSelector returns the field extractor for a policy metric name. An empty
// metric defaults to CPU (the baseline signal), matching the dockerstats provider.
func metricSelector(metric string) (func(model.TaskMetric) float64, error) {
	switch strings.ToLower(strings.TrimSpace(metric)) {
	case model.MetricCPU, "":
		return func(tm model.TaskMetric) float64 { return tm.CPUPercent }, nil
	case model.MetricMemory, "mem":
		return func(tm model.TaskMetric) float64 { return tm.MemPercent }, nil
	default:
		return nil, fmt.Errorf("distributed: unsupported metric %q (want cpu|memory)", metric)
	}
}
