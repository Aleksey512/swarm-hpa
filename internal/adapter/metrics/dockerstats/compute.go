package dockerstats

import "github.com/docker/docker/api/types/container"

// cpuPercent computes CPU utilization from a stats sample that carries both the
// current and previous CPU readings (the standard `docker stats` formula). The
// result can exceed 100 on multi-core containers (100 == one full core). ok is
// false when the deltas are non-positive and a percentage cannot be derived.
func cpuPercent(s container.StatsResponse) (float64, bool) {
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

// memPercent computes memory utilization (0..100) as usage/limit. To match
// `docker stats` it excludes the page cache (inactive_file) from usage when the
// cgroup reports it. ok is false when the limit is unknown (zero).
func memPercent(s container.StatsResponse) (float64, bool) {
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
