package swarm

import (
	"context"
	"errors"
	"testing"

	dswarm "github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/errdefs"
)

func replicatedInspect(reps uint64) dswarm.Service {
	return dswarm.Service{
		ID: "s1",
		Spec: dswarm.ServiceSpec{
			Annotations: dswarm.Annotations{Name: "web"},
			Mode:        dswarm.ServiceMode{Replicated: &dswarm.ReplicatedService{Replicas: &reps}},
		},
	}
}

func TestAdapterScaleSucceeds(t *testing.T) {
	fake := &fakeDockerAPI{inspect: replicatedInspect(2)}
	a := &Adapter{cli: fake, logger: discardLogger()}

	if err := a.Scale(context.Background(), "s1", 4); err != nil {
		t.Fatal(err)
	}
	if fake.inspectCalls != 1 || fake.updateCalls != 1 {
		t.Errorf("inspect=%d update=%d, want 1/1", fake.inspectCalls, fake.updateCalls)
	}
}

func TestAdapterScaleRetriesOnConflict(t *testing.T) {
	fake := &fakeDockerAPI{
		inspect:    replicatedInspect(2),
		updateErrs: []error{errdefs.Conflict(errors.New("version mismatch"))}, // 1st conflicts, 2nd succeeds
	}
	a := &Adapter{cli: fake, logger: discardLogger()}

	if err := a.Scale(context.Background(), "s1", 4); err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}
	if fake.updateCalls != 2 {
		t.Errorf("want 2 ServiceUpdate calls (conflict + success), got %d", fake.updateCalls)
	}
	if fake.inspectCalls != 2 {
		t.Errorf("want 2 inspects (re-inspect before retry), got %d", fake.inspectCalls)
	}
}

func TestAdapterScaleNonReplicatedErrors(t *testing.T) {
	fake := &fakeDockerAPI{inspect: dswarm.Service{ID: "g"}} // no Replicated mode
	a := &Adapter{cli: fake, logger: discardLogger()}

	if err := a.Scale(context.Background(), "g", 3); err == nil {
		t.Error("scaling a non-replicated service must error")
	}
	if fake.updateCalls != 0 {
		t.Errorf("must not call ServiceUpdate for a non-replicated service, got %d", fake.updateCalls)
	}
}

func TestAdapterForceUpdate(t *testing.T) {
	fake := &fakeDockerAPI{
		inspect:    dswarm.Service{ID: "s1"},
		updateResp: dswarm.ServiceUpdateResponse{Warnings: []string{"image pinned by digest"}},
	}
	a := &Adapter{cli: fake, logger: discardLogger()}

	if err := a.ForceUpdate(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if fake.updateCalls != 1 {
		t.Errorf("want 1 ServiceUpdate call, got %d", fake.updateCalls)
	}
}
