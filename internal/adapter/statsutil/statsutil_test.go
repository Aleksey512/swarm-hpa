package statsutil

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
	got, ok := CPUPercent(s)
	if !ok || !approx(got, 40) {
		t.Errorf("CPUPercent = %v ok=%v, want 40/true", got, ok)
	}
}

func TestCPUPercentNonPositiveDelta(t *testing.T) {
	if _, ok := CPUPercent(container.StatsResponse{}); ok {
		t.Error("zero deltas must yield ok=false")
	}
}

func TestMemPercent(t *testing.T) {
	s := container.StatsResponse{MemoryStats: container.MemoryStats{Usage: 500, Limit: 1000}}
	got, ok := MemPercent(s)
	if !ok || !approx(got, 50) {
		t.Errorf("MemPercent = %v ok=%v, want 50/true", got, ok)
	}
}

func TestMemPercentExcludesInactiveCache(t *testing.T) {
	s := container.StatsResponse{MemoryStats: container.MemoryStats{
		Usage: 500, Limit: 1000, Stats: map[string]uint64{"inactive_file": 100},
	}}
	got, ok := MemPercent(s)
	if !ok || !approx(got, 40) {
		t.Errorf("MemPercent with cache = %v, want 40", got)
	}
}

func TestMemPercentZeroLimit(t *testing.T) {
	if _, ok := MemPercent(container.StatsResponse{MemoryStats: container.MemoryStats{Usage: 100}}); ok {
		t.Error("zero limit must yield ok=false")
	}
}

func TestComputeFor(t *testing.T) {
	for _, metric := range []string{"cpu", "", "memory", "mem", "CPU"} {
		if _, err := ComputeFor(metric); err != nil {
			t.Errorf("ComputeFor(%q) unexpected error: %v", metric, err)
		}
	}
	if _, err := ComputeFor("disk"); err == nil {
		t.Error("ComputeFor(disk) must error")
	}
}
