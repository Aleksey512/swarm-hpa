package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	dswarm "github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/api/types/system"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

type fakeDockerAPI struct {
	info       system.Info
	infoErr    error
	tasks      []dswarm.Task
	statsByCID map[string]container.StatsResponse
	statsErr   map[string]error
}

func (f *fakeDockerAPI) Info(context.Context) (system.Info, error) { return f.info, f.infoErr }

func (f *fakeDockerAPI) TaskList(context.Context, dswarm.TaskListOptions) ([]dswarm.Task, error) {
	return f.tasks, nil
}

func (f *fakeDockerAPI) ContainerStats(_ context.Context, cid string, _ bool) (container.StatsResponseReader, error) {
	if err := f.statsErr[cid]; err != nil {
		return container.StatsResponseReader{}, err
	}
	b, _ := json.Marshal(f.statsByCID[cid])
	// Emit two frames (ReadStats reads up to two; both carry precpu here).
	buf := append(append([]byte{}, b...), '\n')
	buf = append(buf, b...)
	return container.StatsResponseReader{Body: io.NopCloser(bytes.NewReader(buf))}, nil
}

// statsSample builds a StatsResponse yielding a known CPU% (via cpu/pre deltas)
// and a known memory usage/limit.
func statsSample(memUsage, memLimit uint64) container.StatsResponse {
	return container.StatsResponse{
		CPUStats: container.CPUStats{
			CPUUsage:    container.CPUUsage{TotalUsage: 1200},
			SystemUsage: 11000,
			OnlineCPUs:  2,
		},
		PreCPUStats: container.CPUStats{
			CPUUsage:    container.CPUUsage{TotalUsage: 1000},
			SystemUsage: 10000,
		},
		MemoryStats: container.MemoryStats{Usage: memUsage, Limit: memLimit},
	}
	// CPU%: (200/1000)*2*100 = 40
}

func taskOn(taskID, serviceID, cid string) dswarm.Task {
	return dswarm.Task{
		ID:        taskID,
		ServiceID: serviceID,
		Status:    dswarm.TaskStatus{ContainerStatus: &dswarm.ContainerStatus{ContainerID: cid}},
	}
}

func approx(a, b float64) bool { d := a - b; return d < 1e-9 && d > -1e-9 }

func TestCollectAggregatesNodeAndTasks(t *testing.T) {
	fake := &fakeDockerAPI{
		info: system.Info{NCPU: 4, MemTotal: 2000, Name: "worker-1", Swarm: dswarm.Info{NodeID: "node-a"}},
		tasks: []dswarm.Task{
			taskOn("t1", "svc-web", "c1"),
			taskOn("t2", "svc-api", "c2"),
		},
		statsByCID: map[string]container.StatsResponse{
			"c1": statsSample(500, 1000), // cpu 40, mem 50%, 500 bytes
			"c2": statsSample(300, 1000), // cpu 40, mem 30%, 300 bytes
		},
	}
	c := &Collector{cli: fake, clock: fakeClock{t: time.Unix(1000, 0)}, logger: discardLogger()}

	report, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.NodeID != "node-a" || report.NodeName != "worker-1" {
		t.Errorf("identity = %q/%q, want node-a/worker-1", report.NodeID, report.NodeName)
	}
	if !report.Timestamp.Equal(time.Unix(1000, 0)) {
		t.Errorf("timestamp = %v, want injected clock time", report.Timestamp)
	}
	if len(report.Tasks) != 2 {
		t.Fatalf("tasks = %d, want 2", len(report.Tasks))
	}
	// per-task
	if report.Tasks[0].ServiceID != "svc-web" || !approx(report.Tasks[0].CPUPercent, 40) || !approx(report.Tasks[0].MemPercent, 50) {
		t.Errorf("task0 = %+v", report.Tasks[0])
	}
	// node aggregate: sumCPU=80 / NCPU=4 = 20 ; sumMemBytes=800 / 2000 * 100 = 40
	if !approx(report.Node.CPUPercent, 20) {
		t.Errorf("node CPUPercent = %v, want 20", report.Node.CPUPercent)
	}
	if !approx(report.Node.MemPercent, 40) {
		t.Errorf("node MemPercent = %v, want 40", report.Node.MemPercent)
	}
	if report.Node.TotalCPU != 4 || report.Node.TotalMemBytes != 2000 || report.Node.TaskCount != 2 {
		t.Errorf("node capacity = %+v", report.Node)
	}
}

func TestCollectSkipsUnreadableTaskButKeepsOthers(t *testing.T) {
	fake := &fakeDockerAPI{
		info: system.Info{NCPU: 2, MemTotal: 1000, Name: "w", Swarm: dswarm.Info{NodeID: "n"}},
		tasks: []dswarm.Task{
			taskOn("t1", "svc", "c1"),
			taskOn("t2", "svc", "remote"),
			taskOn("t3", "svc", ""), // no container yet — silently skipped
		},
		statsByCID: map[string]container.StatsResponse{"c1": statsSample(400, 1000)},
		statsErr:   map[string]error{"remote": errors.New("container stats unavailable")},
	}
	c := &Collector{cli: fake, clock: fakeClock{}, logger: discardLogger()}

	report, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Node.TaskCount != 1 || len(report.Tasks) != 1 {
		t.Fatalf("want 1 sampled task, got count=%d len=%d", report.Node.TaskCount, len(report.Tasks))
	}
	if report.Tasks[0].TaskID != "t1" {
		t.Errorf("kept task = %q, want t1", report.Tasks[0].TaskID)
	}
}

func TestCollectNodeIDOverride(t *testing.T) {
	fake := &fakeDockerAPI{
		info: system.Info{NCPU: 1, MemTotal: 100, Swarm: dswarm.Info{NodeID: "auto"}},
	}
	c := &Collector{cli: fake, nodeIDOverride: "forced", clock: fakeClock{}, logger: discardLogger()}

	report, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.NodeID != "forced" {
		t.Errorf("NodeID = %q, want the override 'forced'", report.NodeID)
	}
}

func TestCollectErrors(t *testing.T) {
	t.Run("info error", func(t *testing.T) {
		c := &Collector{cli: &fakeDockerAPI{infoErr: errors.New("no daemon")}, clock: fakeClock{}, logger: discardLogger()}
		if _, err := c.Collect(context.Background()); err == nil {
			t.Error("want an error when docker info fails")
		}
	})
	t.Run("node id unavailable", func(t *testing.T) {
		c := &Collector{cli: &fakeDockerAPI{info: system.Info{}}, clock: fakeClock{}, logger: discardLogger()}
		if _, err := c.Collect(context.Background()); err == nil {
			t.Error("want an error when the node id cannot be resolved")
		}
	})
}
