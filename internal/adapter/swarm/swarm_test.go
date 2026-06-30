package swarm

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	dswarm "github.com/docker/docker/api/types/swarm"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func optedInLabels() map[string]string {
	return map[string]string{
		"swarm.autoscaler.enabled": "true",
		"swarm.autoscaler.min":     "1",
		"swarm.autoscaler.max":     "5",
		"swarm.autoscaler.metric":  "cpu",
		"swarm.autoscaler.target":  "80",
	}
}

// fakeDockerAPI implements the unexported dockerAPI interface for tests.
// updateErrs is consumed one-per-ServiceUpdate-call so retry-on-conflict can be
// exercised; updateCalls / inspectCalls count invocations.
type fakeDockerAPI struct {
	services []dswarm.Service
	tasks    []dswarm.Task
	nodes    []dswarm.Node
	err      error

	inspect      dswarm.Service
	inspectErr   error
	inspectCalls int
	updateResp   dswarm.ServiceUpdateResponse
	updateErrs   []error
	updateCalls  int
}

func (f *fakeDockerAPI) ServiceList(context.Context, dswarm.ServiceListOptions) ([]dswarm.Service, error) {
	return f.services, f.err
}

func (f *fakeDockerAPI) ServiceInspectWithRaw(context.Context, string, dswarm.ServiceInspectOptions) (dswarm.Service, []byte, error) {
	f.inspectCalls++
	return f.inspect, nil, f.inspectErr
}

func (f *fakeDockerAPI) ServiceUpdate(context.Context, string, dswarm.Version, dswarm.ServiceSpec, dswarm.ServiceUpdateOptions) (dswarm.ServiceUpdateResponse, error) {
	i := f.updateCalls
	f.updateCalls++
	var err error
	if i < len(f.updateErrs) {
		err = f.updateErrs[i]
	}
	return f.updateResp, err
}

func (f *fakeDockerAPI) TaskList(context.Context, dswarm.TaskListOptions) ([]dswarm.Task, error) {
	return f.tasks, f.err
}

func (f *fakeDockerAPI) NodeList(context.Context, dswarm.NodeListOptions) ([]dswarm.Node, error) {
	return f.nodes, f.err
}

func TestToManagedServiceReplicated(t *testing.T) {
	reps := uint64(3)
	svc := dswarm.Service{
		ID: "svc1",
		Spec: dswarm.ServiceSpec{
			Annotations: dswarm.Annotations{Name: "web", Labels: optedInLabels()},
			Mode:        dswarm.ServiceMode{Replicated: &dswarm.ReplicatedService{Replicas: &reps}},
			TaskTemplate: dswarm.TaskSpec{
				Placement: &dswarm.Placement{Constraints: []string{"node.labels.nodeNum==1"}},
			},
		},
	}
	ms, err := toManagedService(svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ms.Ref.ID != "svc1" || ms.Ref.Name != "web" {
		t.Errorf("ref = %+v", ms.Ref)
	}
	if !ms.Replicated || ms.Replicas != 3 {
		t.Errorf("replicated=%v replicas=%d, want true/3", ms.Replicated, ms.Replicas)
	}
	if len(ms.Constraints) != 1 || ms.Constraints[0] != "node.labels.nodeNum==1" {
		t.Errorf("constraints = %v", ms.Constraints)
	}
	if ms.Policy.Max != 5 || ms.Policy.Target != 80 {
		t.Errorf("policy = %+v", ms.Policy)
	}
}

func TestToManagedServiceNilReplicas(t *testing.T) {
	// Global service (or replicated with nil Replicas) must not panic.
	svc := dswarm.Service{
		ID:   "g",
		Spec: dswarm.ServiceSpec{Annotations: dswarm.Annotations{Name: "glob", Labels: optedInLabels()}},
	}
	ms, err := toManagedService(svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ms.Replicated {
		t.Error("Replicated should be false when Mode.Replicated is nil")
	}
	if ms.Replicas != 0 {
		t.Errorf("replicas = %d, want 0", ms.Replicas)
	}
}

func TestToManagedServiceMisconfigured(t *testing.T) {
	svc := dswarm.Service{
		Spec: dswarm.ServiceSpec{
			Annotations: dswarm.Annotations{Labels: map[string]string{"swarm.autoscaler.enabled": "true"}},
		},
	}
	if _, err := toManagedService(svc); err == nil {
		t.Error("expected an error for opted-in service missing min/max/target/metric")
	}
}

func TestToTaskView(t *testing.T) {
	task := dswarm.Task{
		ID:           "t1",
		ServiceID:    "s1",
		NodeID:       "n1",
		DesiredState: dswarm.TaskStateRunning,
		Status:       dswarm.TaskStatus{State: dswarm.TaskStatePending, Err: "no suitable node"},
	}
	v := toTaskView(task)
	if v.State != "pending" || v.DesiredState != "running" {
		t.Errorf("state=%q desired=%q", v.State, v.DesiredState)
	}
	if !v.IsPending() {
		t.Error("IsPending should be true (pending + desired running)")
	}
	if v.Err != "no suitable node" {
		t.Errorf("err = %q", v.Err)
	}
}

func TestToNodeView(t *testing.T) {
	n := dswarm.Node{
		ID:          "n1",
		Description: dswarm.NodeDescription{Hostname: "host-1"},
		Spec: dswarm.NodeSpec{
			Annotations:  dswarm.Annotations{Labels: map[string]string{"nodeNum": "1"}},
			Availability: dswarm.NodeAvailabilityActive,
		},
		Status: dswarm.NodeStatus{State: dswarm.NodeStateReady},
	}
	v := toNodeView(n)
	if v.Name != "host-1" || v.Availability != "active" || v.State != "ready" {
		t.Errorf("node = %+v", v)
	}
	if v.Labels["nodeNum"] != "1" {
		t.Errorf("node spec labels not mapped: %+v", v.Labels)
	}
	if !v.IsActive() {
		t.Error("IsActive should be true (active + ready)")
	}
}

func TestAdapterManagedServicesSkipsMisconfigured(t *testing.T) {
	reps := uint64(2)
	good := dswarm.Service{
		ID: "good",
		Spec: dswarm.ServiceSpec{
			Annotations: dswarm.Annotations{Name: "web", Labels: optedInLabels()},
			Mode:        dswarm.ServiceMode{Replicated: &dswarm.ReplicatedService{Replicas: &reps}},
		},
	}
	bad := dswarm.Service{
		ID: "bad",
		Spec: dswarm.ServiceSpec{
			Annotations: dswarm.Annotations{Name: "broken", Labels: map[string]string{"swarm.autoscaler.enabled": "true"}},
		},
	}
	a := &Adapter{cli: &fakeDockerAPI{services: []dswarm.Service{good, bad}}, logger: discardLogger()}

	got, err := a.ManagedServices(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Ref.Name != "web" {
		t.Fatalf("want only the valid service, got %+v", got)
	}
}

func TestAdapterManagedServicesPropagatesError(t *testing.T) {
	a := &Adapter{cli: &fakeDockerAPI{err: errors.New("boom")}, logger: discardLogger()}
	if _, err := a.ManagedServices(context.Background()); err == nil {
		t.Error("expected ServiceList error to propagate")
	}
}

func TestAdapterTasksAndNodes(t *testing.T) {
	a := &Adapter{cli: &fakeDockerAPI{
		tasks: []dswarm.Task{{ID: "t1", ServiceID: "s1", DesiredState: dswarm.TaskStateRunning,
			Status: dswarm.TaskStatus{State: dswarm.TaskStateRunning}}},
		nodes: []dswarm.Node{{ID: "n1", Description: dswarm.NodeDescription{Hostname: "h1"},
			Spec: dswarm.NodeSpec{Availability: dswarm.NodeAvailabilityActive}, Status: dswarm.NodeStatus{State: dswarm.NodeStateReady}}},
	}, logger: discardLogger()}

	tasks, err := a.Tasks(context.Background(), "s1")
	if err != nil || len(tasks) != 1 || tasks[0].ID != "t1" {
		t.Fatalf("tasks = %+v, err = %v", tasks, err)
	}
	nodes, err := a.Nodes(context.Background())
	if err != nil || len(nodes) != 1 || !nodes[0].IsActive() {
		t.Fatalf("nodes = %+v, err = %v", nodes, err)
	}
}
