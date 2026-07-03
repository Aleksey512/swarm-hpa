// Package collector is the agent-side adapter that samples the LOCAL node's
// per-task and aggregate CPU/memory load and builds a model.AgentReport for the
// manager. It runs on every node (deployed mode: global), so it enumerates task
// containers via the LOCAL container API (ContainerList) — NOT the Swarm TaskList
// API, which is manager-only and would fail on worker nodes — and reads their
// stats locally.
package collector

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/client"

	"github.com/Aleksey512/swarm-hpa/internal/adapter/statsutil"
	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// callTimeout bounds each Docker API call so a hung local daemon cannot stall a
// report cycle.
const callTimeout = 10 * time.Second

// Swarm stamps these labels on every task container; the agent reads them to map
// a local container back to its service and task without the manager-only Swarm
// API.
const (
	labelServiceID = "com.docker.swarm.service.id"
	labelTaskID    = "com.docker.swarm.task.id"
)

// dockerAPI is the subset of the Docker client the collector uses against the
// LOCAL daemon. Every call here is served by any node's daemon (manager or
// worker) — no Swarm cluster-state APIs. Narrowing it to an interface lets tests
// substitute a fake.
type dockerAPI interface {
	Info(ctx context.Context) (system.Info, error)
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	ContainerStats(ctx context.Context, containerID string, stream bool) (container.StatsResponseReader, error)
}

// Collector samples the local node's load. It is constructed with the Docker
// client for the local socket; nodeIDOverride is normally empty (auto-detect).
type Collector struct {
	cli            dockerAPI
	nodeIDOverride string
	clock          port.Clock
	logger         *slog.Logger
}

// New returns a collector over the given local Docker client. nodeIDOverride, if
// non-empty, is reported instead of the auto-detected node ID (config NODE_ID).
func New(cli *client.Client, nodeIDOverride string, clock port.Clock, logger *slog.Logger) *Collector {
	if logger == nil {
		logger = slog.Default()
	}
	if clock == nil {
		clock = port.SystemClock{}
	}
	return &Collector{cli: cli, nodeIDOverride: nodeIDOverride, clock: clock, logger: logger}
}

// Collect samples the local node once and returns a report. It never fails on a
// single unreadable container (that container is logged at WARN and skipped); it
// returns an error only when the node identity or the container listing cannot
// be resolved at all.
func (c *Collector) Collect(ctx context.Context) (model.AgentReport, error) {
	infoCtx, cancel := context.WithTimeout(ctx, callTimeout)
	info, err := c.cli.Info(infoCtx)
	cancel()
	if err != nil {
		return model.AgentReport{}, fmt.Errorf("collector: docker info: %w", err)
	}

	nodeID := c.nodeIDOverride
	if nodeID == "" {
		nodeID = info.Swarm.NodeID
	}
	if nodeID == "" {
		return model.AgentReport{}, fmt.Errorf("collector: node id unavailable (is this node part of a swarm?)")
	}

	containers, err := c.listLocalTaskContainers(ctx)
	if err != nil {
		return model.AgentReport{}, err
	}

	metrics := make([]model.TaskMetric, 0, len(containers))
	var sumCPU, sumMemBytes float64
	for _, ct := range containers {
		taskID := ct.Labels[labelTaskID]
		serviceID := ct.Labels[labelServiceID]

		stats, err := statsutil.ReadStats(ctx, c.cli, ct.ID, callTimeout)
		if err != nil {
			c.logger.Warn("collector: skipping container (local stats unavailable)",
				"node", nodeID, "task", taskID, "container", ct.ID, "err", err)
			continue
		}

		cpu, cpuOK := statsutil.CPUPercent(stats)
		mem, memOK := statsutil.MemPercent(stats)
		metrics = append(metrics, model.TaskMetric{
			TaskID:     taskID,
			ServiceID:  serviceID,
			CPUPercent: cpu,
			MemPercent: mem,
		})
		if cpuOK {
			sumCPU += cpu
		}
		if memOK {
			sumMemBytes += statsutil.MemUsageBytes(stats)
		}
		c.logger.Debug("collector: task metric",
			"node", nodeID, "task", taskID, "service", serviceID,
			"cpu_pct", cpu, "mem_pct", mem)
	}

	node := model.NodeLoad{
		TotalCPU:      info.NCPU,
		TotalMemBytes: info.MemTotal,
		TaskCount:     len(metrics),
	}
	if info.NCPU > 0 {
		// Task CPU% is scaled so 100 == one full core; dividing the node's summed
		// task CPU% by the core count yields node utilization on a 0..100 scale.
		node.CPUPercent = sumCPU / float64(info.NCPU)
	}
	if info.MemTotal > 0 {
		node.MemPercent = sumMemBytes / float64(info.MemTotal) * 100.0
	}

	report := model.AgentReport{
		NodeID:    nodeID,
		NodeName:  info.Name,
		Timestamp: c.clock.Now(),
		Node:      node,
		Tasks:     metrics,
	}
	c.logger.Debug("collector: node report",
		"node", nodeID, "name", info.Name, "tasks", node.TaskCount,
		"cpu_pct", node.CPUPercent, "mem_pct", node.MemPercent,
		"total_cpu", node.TotalCPU, "total_mem_bytes", node.TotalMemBytes)
	return report, nil
}

// listLocalTaskContainers returns the running Swarm task containers on this node,
// via the LOCAL container API (works on manager and worker nodes alike). Only
// containers carrying the Swarm task-id label are returned, so plain (non-Swarm)
// containers on the host are ignored.
func (c *Collector) listLocalTaskContainers(ctx context.Context) ([]container.Summary, error) {
	listCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	f := filters.NewArgs(
		filters.Arg("status", "running"),
		filters.Arg("label", labelTaskID),
	)
	containers, err := c.cli.ContainerList(listCtx, container.ListOptions{Filters: f})
	if err != nil {
		return nil, fmt.Errorf("collector: container list: %w", err)
	}
	return containers, nil
}
