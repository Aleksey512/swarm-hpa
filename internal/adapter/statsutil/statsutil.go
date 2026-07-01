// Package statsutil holds the Docker container-stats computations shared by the
// manager's dockerstats metrics provider and the agent's local collector. Both
// read the same StatsResponse frames from the Docker Engine API and derive the
// same CPU/memory percentages; keeping the formulas in one place avoids drift.
//
// It is infrastructure (it imports the Docker SDK) and depends inward only on
// internal/core/model, so it never pulls the core toward Docker types.
package statsutil

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	dswarm "github.com/docker/docker/api/types/swarm"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// StatsReader is the single Docker call used to sample a container's stats. Both
// the dockerstats provider and the agent collector satisfy it via the Docker
// client; tests substitute a fake.
type StatsReader interface {
	ContainerStats(ctx context.Context, containerID string, stream bool) (container.StatsResponseReader, error)
}

// ComputeFunc derives a percentage metric from a stats sample. ok is false when
// the value cannot be computed from the given sample.
type ComputeFunc func(container.StatsResponse) (float64, bool)

// ComputeFor maps a policy metric name to its stats compute function. An empty
// metric defaults to CPU (the baseline autoscaling signal).
func ComputeFor(metric string) (ComputeFunc, error) {
	switch strings.ToLower(strings.TrimSpace(metric)) {
	case model.MetricCPU, "":
		return CPUPercent, nil
	case model.MetricMemory, "mem":
		return MemPercent, nil
	default:
		return nil, fmt.Errorf("statsutil: unsupported metric %q (want cpu|memory)", metric)
	}
}

// CPUPercent computes CPU utilization from a stats sample that carries both the
// current and previous CPU readings (the standard `docker stats` formula). The
// result can exceed 100 on multi-core containers (100 == one full core). ok is
// false when the deltas are non-positive and a percentage cannot be derived.
func CPUPercent(s container.StatsResponse) (float64, bool) {
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	if cpuDelta <= 0 || sysDelta <= 0 {
		return 0, false
	}

	onlineCPUs := float64(s.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	if onlineCPUs == 0 {
		onlineCPUs = 1
	}

	return (cpuDelta / sysDelta) * onlineCPUs * 100.0, true
}

// MemPercent computes memory utilization (0..100) as usage/limit. To match
// `docker stats` it excludes the page cache (inactive_file) from usage when the
// cgroup reports it. ok is false when the limit is unknown (zero).
func MemPercent(s container.StatsResponse) (float64, bool) {
	limit := float64(s.MemoryStats.Limit)
	if limit <= 0 {
		return 0, false
	}

	usage := float64(s.MemoryStats.Usage)
	if cache, ok := s.MemoryStats.Stats["inactive_file"]; ok && float64(cache) < usage {
		usage -= float64(cache)
	}

	return usage / limit * 100.0, true
}

// MemUsageBytes returns a container's cache-adjusted memory usage in bytes (the
// numerator of MemPercent, excluding the inactive_file page cache to match
// `docker stats`). Unlike MemPercent it does not divide by the cgroup limit, so
// it is the right quantity for aggregating node-level memory against total host
// memory. Returns 0 when the sample reports no usage.
func MemUsageBytes(s container.StatsResponse) float64 {
	usage := float64(s.MemoryStats.Usage)
	if cache, ok := s.MemoryStats.Stats["inactive_file"]; ok && float64(cache) < usage {
		usage -= float64(cache)
	}
	return usage
}

// ContainerID returns the container ID backing a task, or "" when the task has
// no container yet (e.g. still pending).
func ContainerID(t dswarm.Task) string {
	if t.Status.ContainerStatus == nil {
		return ""
	}
	return t.Status.ContainerStatus.ContainerID
}

// ReadStats reads up to two stream frames for a container. The second frame
// carries PreCPUStats (the previous sample), which the CPU% formula needs;
// memory% needs only one. It returns the most recent frame read. The call is
// bounded by timeout.
func ReadStats(ctx context.Context, cli StatsReader, containerID string, timeout time.Duration) (container.StatsResponse, error) {
	statsCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := cli.ContainerStats(statsCtx, containerID, true)
	if err != nil {
		return container.StatsResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()

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
