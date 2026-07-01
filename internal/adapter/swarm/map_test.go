package swarm

import (
	"testing"

	dswarm "github.com/docker/docker/api/types/swarm"
)

func TestToTaskViewStates(t *testing.T) {
	cases := []struct {
		name        string
		state       dswarm.TaskState
		desired     dswarm.TaskState
		wantState   string
		wantDesired string
		wantPending bool
	}{
		{"pending desired running", dswarm.TaskStatePending, dswarm.TaskStateRunning, "pending", "running", true},
		{"running", dswarm.TaskStateRunning, dswarm.TaskStateRunning, "running", "running", false},
		{"failed desired running", dswarm.TaskStateFailed, dswarm.TaskStateRunning, "failed", "running", false},
		{"pending desired shutdown", dswarm.TaskStatePending, dswarm.TaskStateShutdown, "pending", "shutdown", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := toTaskView(dswarm.Task{
				ID: "t", ServiceID: "s", NodeID: "n",
				DesiredState: tc.desired,
				Status:       dswarm.TaskStatus{State: tc.state},
			})
			if v.State != tc.wantState || v.DesiredState != tc.wantDesired {
				t.Errorf("state=%q desired=%q, want %q/%q", v.State, v.DesiredState, tc.wantState, tc.wantDesired)
			}
			if v.IsPending() != tc.wantPending {
				t.Errorf("IsPending = %v, want %v", v.IsPending(), tc.wantPending)
			}
			if v.ID != "t" || v.ServiceID != "s" || v.NodeID != "n" {
				t.Errorf("identity fields not mapped: %+v", v)
			}
		})
	}
}

func TestToNodeViewAvailability(t *testing.T) {
	cases := []struct {
		name         string
		availability dswarm.NodeAvailability
		state        dswarm.NodeState
		wantActive   bool
	}{
		{"active + ready", dswarm.NodeAvailabilityActive, dswarm.NodeStateReady, true},
		{"active + down", dswarm.NodeAvailabilityActive, dswarm.NodeStateDown, false},
		{"pause + ready", dswarm.NodeAvailabilityPause, dswarm.NodeStateReady, false},
		{"drain + ready", dswarm.NodeAvailabilityDrain, dswarm.NodeStateReady, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := toNodeView(dswarm.Node{
				ID:          "n1",
				Description: dswarm.NodeDescription{Hostname: "h"},
				Spec:        dswarm.NodeSpec{Availability: tc.availability},
				Status:      dswarm.NodeStatus{State: tc.state},
			})
			if v.IsActive() != tc.wantActive {
				t.Errorf("IsActive(avail=%q state=%q) = %v, want %v", v.Availability, v.State, v.IsActive(), tc.wantActive)
			}
		})
	}
}

func TestToNodeViewNilLabels(t *testing.T) {
	v := toNodeView(dswarm.Node{ID: "n1", Description: dswarm.NodeDescription{Hostname: "h"}})
	if v.Labels != nil {
		t.Errorf("labels = %v, want nil pass-through when node has none", v.Labels)
	}
}

func TestToManagedServiceModes(t *testing.T) {
	reps := uint64(4)
	cases := []struct {
		name            string
		mode            dswarm.ServiceMode
		placement       *dswarm.Placement
		wantReplicated  bool
		wantReplicas    uint64
		wantConstraints int
	}{
		{"replicated with replicas", dswarm.ServiceMode{Replicated: &dswarm.ReplicatedService{Replicas: &reps}}, nil, true, 4, 0},
		{"replicated nil replicas", dswarm.ServiceMode{Replicated: &dswarm.ReplicatedService{}}, nil, true, 0, 0},
		{"global (nil replicated)", dswarm.ServiceMode{Global: &dswarm.GlobalService{}}, nil, false, 0, 0},
		{"placement present, empty constraints", dswarm.ServiceMode{Replicated: &dswarm.ReplicatedService{Replicas: &reps}}, &dswarm.Placement{}, true, 4, 0},
		{"placement with constraints", dswarm.ServiceMode{Replicated: &dswarm.ReplicatedService{Replicas: &reps}}, &dswarm.Placement{Constraints: []string{"node.labels.nodeNum==1", "node.hostname==h"}}, true, 4, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := dswarm.Service{
				ID: "svc",
				Spec: dswarm.ServiceSpec{
					Annotations:  dswarm.Annotations{Name: "web", Labels: optedInLabels()},
					Mode:         tc.mode,
					TaskTemplate: dswarm.TaskSpec{Placement: tc.placement},
				},
			}
			ms, err := toManagedService(svc)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ms.Replicated != tc.wantReplicated || ms.Replicas != tc.wantReplicas {
				t.Errorf("replicated=%v replicas=%d, want %v/%d", ms.Replicated, ms.Replicas, tc.wantReplicated, tc.wantReplicas)
			}
			if len(ms.Constraints) != tc.wantConstraints {
				t.Errorf("constraints=%v, want %d entries", ms.Constraints, tc.wantConstraints)
			}
		})
	}
}
