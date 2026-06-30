package reconciler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// recordingController counts mutation calls so the guard's gating can be asserted.
type recordingController struct {
	scaleCalls int
	forceCalls int
	scaleErr   error
}

func (r *recordingController) ManagedServices(context.Context) ([]model.ManagedService, error) {
	return nil, nil
}
func (r *recordingController) Tasks(context.Context, string) ([]model.TaskView, error) {
	return nil, nil
}
func (r *recordingController) Nodes(context.Context) ([]model.NodeView, error) { return nil, nil }
func (r *recordingController) Scale(context.Context, string, uint64) error {
	r.scaleCalls++
	return r.scaleErr
}
func (r *recordingController) ForceUpdate(context.Context, string) error {
	r.forceCalls++
	return nil
}

func replicatedSvc(replicas uint64) model.ManagedService {
	return model.ManagedService{
		Ref:        model.ServiceRef{ID: "s1", Name: "web"},
		Replicas:   replicas,
		Replicated: true,
		Policy:     model.ServicePolicy{Enabled: true, Min: 1, Max: 5, Metric: "cpu", Target: 80},
	}
}

func TestGuardDryRunMakesNoMutation(t *testing.T) {
	rc := &recordingController{}
	g := NewGuard(rc, NewCooldown(0, newFakeClock()), true, port.NopRecorder{}, discardLogger())

	if err := g.Scale(context.Background(), replicatedSvc(2), 4); err != nil {
		t.Fatal(err)
	}
	if err := g.Heal(context.Background(), replicatedSvc(2)); err != nil {
		t.Fatal(err)
	}
	if rc.scaleCalls != 0 || rc.forceCalls != 0 {
		t.Errorf("dry-run must make zero mutations, got scale=%d force=%d", rc.scaleCalls, rc.forceCalls)
	}
}

func TestGuardAppliesWhenEnabled(t *testing.T) {
	rc := &recordingController{}
	g := NewGuard(rc, NewCooldown(0, newFakeClock()), false, port.NopRecorder{}, discardLogger())

	if err := g.Scale(context.Background(), replicatedSvc(2), 4); err != nil {
		t.Fatal(err)
	}
	if rc.scaleCalls != 1 {
		t.Errorf("want exactly 1 scale call, got %d", rc.scaleCalls)
	}
}

func TestGuardCooldownSuppressesSecondAction(t *testing.T) {
	rc := &recordingController{}
	g := NewGuard(rc, NewCooldown(time.Minute, newFakeClock()), false, port.NopRecorder{}, discardLogger())
	svc := replicatedSvc(2)

	_ = g.Scale(context.Background(), svc, 4) // applies + records
	_ = g.Scale(context.Background(), svc, 5) // within cooldown -> suppressed
	if rc.scaleCalls != 1 {
		t.Errorf("second scale must be suppressed by cooldown, got %d calls", rc.scaleCalls)
	}
}

func TestGuardNoOpAndNonReplicated(t *testing.T) {
	rc := &recordingController{}
	g := NewGuard(rc, NewCooldown(0, newFakeClock()), false, port.NopRecorder{}, discardLogger())

	_ = g.Scale(context.Background(), replicatedSvc(3), 3) // no change
	global := model.ManagedService{Ref: model.ServiceRef{ID: "g", Name: "glob"}, Replicated: false}
	_ = g.Scale(context.Background(), global, 5) // non-replicated

	if rc.scaleCalls != 0 {
		t.Errorf("no-op and non-replicated must not call Scale, got %d", rc.scaleCalls)
	}
}

func TestGuardScaleErrorPropagates(t *testing.T) {
	rc := &recordingController{scaleErr: errors.New("boom")}
	g := NewGuard(rc, NewCooldown(0, newFakeClock()), false, port.NopRecorder{}, discardLogger())
	if err := g.Scale(context.Background(), replicatedSvc(2), 4); err == nil {
		t.Error("expected the scale error to propagate")
	}
}
