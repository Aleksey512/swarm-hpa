package distributed

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func approx(a, b float64) bool { d := a - b; return d < 1e-9 && d > -1e-9 }

type fakeSnapshot struct{ reports []model.AgentReport }

func (f fakeSnapshot) Snapshot() []model.AgentReport { return f.reports }

func svc(id, metric string) model.ManagedService {
	return model.ManagedService{Ref: model.ServiceRef{ID: id, Name: id}, Policy: model.ServicePolicy{Metric: metric}}
}

func TestValueAggregatesAcrossNodes(t *testing.T) {
	// svc "web" runs one task on node-a (cpu 30) and two on node-b (cpu 50, 70).
	snap := fakeSnapshot{reports: []model.AgentReport{
		{NodeID: "node-a", Tasks: []model.TaskMetric{
			{TaskID: "t1", ServiceID: "web", CPUPercent: 30, MemPercent: 10},
			{TaskID: "x", ServiceID: "other", CPUPercent: 99},
		}},
		{NodeID: "node-b", Tasks: []model.TaskMetric{
			{TaskID: "t2", ServiceID: "web", CPUPercent: 50, MemPercent: 20},
			{TaskID: "t3", ServiceID: "web", CPUPercent: 70, MemPercent: 60},
		}},
	}}
	p := New(snap, discardLogger())

	got, err := p.Value(context.Background(), svc("web", "cpu"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !approx(got, (30+50+70)/3.0) { // 50
		t.Errorf("cpu avg = %v, want 50", got)
	}

	got, err = p.Value(context.Background(), svc("web", "memory"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !approx(got, (10+20+60)/3.0) { // 30
		t.Errorf("mem avg = %v, want 30", got)
	}
}

func TestValueNoLiveTasksIsErrNoMetricData(t *testing.T) {
	p := New(fakeSnapshot{reports: []model.AgentReport{
		{NodeID: "n", Tasks: []model.TaskMetric{{ServiceID: "other", CPUPercent: 10}}},
	}}, discardLogger())

	if _, err := p.Value(context.Background(), svc("web", "cpu")); !errors.Is(err, model.ErrNoMetricData) {
		t.Errorf("want ErrNoMetricData when no task matches the service, got %v", err)
	}
}

func TestValueEmptySnapshotIsErrNoMetricData(t *testing.T) {
	p := New(fakeSnapshot{}, discardLogger())
	if _, err := p.Value(context.Background(), svc("web", "cpu")); !errors.Is(err, model.ErrNoMetricData) {
		t.Errorf("want ErrNoMetricData for an empty snapshot, got %v", err)
	}
}

func TestValueDefaultsToCPU(t *testing.T) {
	p := New(fakeSnapshot{reports: []model.AgentReport{
		{NodeID: "n", Tasks: []model.TaskMetric{{ServiceID: "web", CPUPercent: 42}}},
	}}, discardLogger())
	got, err := p.Value(context.Background(), svc("web", "")) // empty metric → cpu
	if err != nil || !approx(got, 42) {
		t.Errorf("empty metric should default to cpu: got=%v err=%v", got, err)
	}
}

func TestValueUnsupportedMetricErrors(t *testing.T) {
	p := New(fakeSnapshot{reports: []model.AgentReport{
		{NodeID: "n", Tasks: []model.TaskMetric{{ServiceID: "web", CPUPercent: 42}}},
	}}, discardLogger())
	if _, err := p.Value(context.Background(), svc("web", "disk")); err == nil {
		t.Error("unsupported metric must error")
	}
}
