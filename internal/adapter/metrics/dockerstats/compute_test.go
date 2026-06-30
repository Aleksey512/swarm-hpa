package dockerstats

import (
	"testing"

	"github.com/docker/docker/api/types/container"
)

func approx(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}

func TestCPUPercent(t *testing.T) {
	// cpuDelta=200, sysDelta=1000, onlineCPUs=2 -> 200/1000 * 2 * 100 = 40
	s := container.StatsResponse{
		CPUStats: container.CPUStats{
			CPUUsage:    container.CPUUsage{TotalUsage: 1200},
			SystemUsage: 11000,
			OnlineCPUs:  2,
		},
		PreCPUStats: container.CPUStats{
			CPUUsage:    container.CPUUsage{TotalUsage: 1000},
			SystemUsage: 10000,
		},
	}
	got, ok := cpuPercent(s)
	if !ok || !approx(got, 40) {
		t.Errorf("cpuPercent = %v ok=%v, want 40/true", got, ok)
	}
}

func TestCPUPercentNonPositiveDelta(t *testing.T) {
	if _, ok := cpuPercent(container.StatsResponse{}); ok {
		t.Error("zero deltas must yield ok=false")
	}
}

func TestMemPercent(t *testing.T) {
	s := container.StatsResponse{MemoryStats: container.MemoryStats{Usage: 500, Limit: 1000}}
	got, ok := memPercent(s)
	if !ok || !approx(got, 50) {
		t.Errorf("memPercent = %v ok=%v, want 50/true", got, ok)
	}
}

func TestMemPercentExcludesInactiveCache(t *testing.T) {
	s := container.StatsResponse{MemoryStats: container.MemoryStats{
		Usage: 500, Limit: 1000, Stats: map[string]uint64{"inactive_file": 100},
	}}
	got, ok := memPercent(s)
	if !ok || !approx(got, 40) {
		t.Errorf("memPercent with cache = %v, want 40", got)
	}
}

func TestMemPercentZeroLimit(t *testing.T) {
	if _, ok := memPercent(container.StatsResponse{MemoryStats: container.MemoryStats{Usage: 100}}); ok {
		t.Error("zero limit must yield ok=false")
	}
}
