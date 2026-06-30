package dockerstats

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/docker/docker/api/types/container"
	dswarm "github.com/docker/docker/api/types/swarm"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeStatsAPI implements the unexported statsAPI for tests (no live daemon).
type fakeStatsAPI struct {
	tasks      []dswarm.Task
	statsByCID map[string]container.StatsResponse
	statsErr   map[string]error
}

func (f *fakeStatsAPI) TaskList(context.Context, dswarm.TaskListOptions) ([]dswarm.Task, error) {
	return f.tasks, nil
}

func (f *fakeStatsAPI) ContainerStats(_ context.Context, cid string, _ bool) (container.StatsResponseReader, error) {
	if err := f.statsErr[cid]; err != nil {
		return container.StatsResponseReader{}, err
	}
	b, _ := json.Marshal(f.statsByCID[cid])
	// Emit two frames (the reader reads up to two; the second carries precpu).
	buf := append(append([]byte{}, b...), '\n')
	buf = append(buf, b...)
	return container.StatsResponseReader{Body: io.NopCloser(bytes.NewReader(buf))}, nil
}

func memStats(usage, limit uint64) container.StatsResponse {
	return container.StatsResponse{MemoryStats: container.MemoryStats{Usage: usage, Limit: limit}}
}

func taskWithContainer(cid string) dswarm.Task {
	return dswarm.Task{Status: dswarm.TaskStatus{ContainerStatus: &dswarm.ContainerStatus{ContainerID: cid}}}
}

func memSvc() model.ManagedService {
	return model.ManagedService{Ref: model.ServiceRef{ID: "s1", Name: "web"}, Policy: model.ServicePolicy{Metric: "memory"}}
}

func TestProviderValueAverages(t *testing.T) {
	p := &Provider{cli: &fakeStatsAPI{
		tasks: []dswarm.Task{taskWithContainer("c1"), taskWithContainer("c2")},
		statsByCID: map[string]container.StatsResponse{
			"c1": memStats(400, 1000), // 40%
			"c2": memStats(600, 1000), // 60%
		},
	}, logger: discardLogger()}

	got, err := p.Value(context.Background(), memSvc())
	if err != nil {
		t.Fatal(err)
	}
	if !approx(got, 50) {
		t.Errorf("avg = %v, want 50", got)
	}
}

func TestProviderSkipsUnavailableTask(t *testing.T) {
	p := &Provider{cli: &fakeStatsAPI{
		tasks:      []dswarm.Task{taskWithContainer("c1"), taskWithContainer("remote")},
		statsByCID: map[string]container.StatsResponse{"c1": memStats(500, 1000)},
		statsErr:   map[string]error{"remote": errors.New("no such container (remote node)")},
	}, logger: discardLogger()}

	got, err := p.Value(context.Background(), memSvc())
	if err != nil {
		t.Fatal(err)
	}
	if !approx(got, 50) {
		t.Errorf("avg of available task only = %v, want 50", got)
	}
}

func TestProviderNoMetricData(t *testing.T) {
	p := &Provider{cli: &fakeStatsAPI{
		tasks:    []dswarm.Task{taskWithContainer("remote")},
		statsErr: map[string]error{"remote": errors.New("unavailable")},
	}, logger: discardLogger()}

	if _, err := p.Value(context.Background(), memSvc()); !errors.Is(err, model.ErrNoMetricData) {
		t.Errorf("want ErrNoMetricData, got %v", err)
	}
}

func TestProviderUnsupportedMetric(t *testing.T) {
	p := &Provider{cli: &fakeStatsAPI{}, logger: discardLogger()}
	svc := model.ManagedService{Policy: model.ServicePolicy{Metric: "disk"}}
	if _, err := p.Value(context.Background(), svc); err == nil {
		t.Error("an unsupported metric must error")
	}
}
