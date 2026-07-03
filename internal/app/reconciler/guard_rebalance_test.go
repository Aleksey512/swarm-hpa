package reconciler

import (
	"context"
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func rebalanceSvc() model.ManagedService {
	return model.ManagedService{
		Ref:        model.ServiceRef{ID: "s1", Name: "web"},
		Replicas:   3,
		Replicated: true,
		Rebalance:  true,
	}
}

func TestGuardRebalanceAppliesWhenEnabled(t *testing.T) {
	rc := &recordingController{}
	fr := &fakeRecorder{}
	g := NewGuard(rc, NewCooldown(newFakeClock()), Cooldowns{}, false, fr, discardLogger())

	if err := g.Rebalance(context.Background(), rebalanceSvc(), "hot", "cold"); err != nil {
		t.Fatal(err)
	}
	if rc.forceCalls != 1 {
		t.Errorf("force-update calls = %d, want 1", rc.forceCalls)
	}
	if len(fr.rebalances) != 1 || fr.rebalances[0] != "web" {
		t.Errorf("rebalances = %v, want [web]", fr.rebalances)
	}
}

func TestGuardRebalanceOptOutDoesNothing(t *testing.T) {
	rc := &recordingController{}
	fr := &fakeRecorder{}
	g := NewGuard(rc, NewCooldown(newFakeClock()), Cooldowns{}, false, fr, discardLogger())

	svc := rebalanceSvc()
	svc.Rebalance = false // not opted in
	if err := g.Rebalance(context.Background(), svc, "hot", "cold"); err != nil {
		t.Fatal(err)
	}
	if rc.forceCalls != 0 {
		t.Errorf("a non-opted-in service must not be force-updated, got %d calls", rc.forceCalls)
	}
	if len(fr.rebalances) != 0 {
		t.Errorf("no rebalance should be recorded, got %v", fr.rebalances)
	}
}

func TestGuardRebalanceDryRunMakesNoMutation(t *testing.T) {
	rc := &recordingController{}
	fr := &fakeRecorder{}
	g := NewGuard(rc, NewCooldown(newFakeClock()), Cooldowns{}, true, fr, discardLogger())

	if err := g.Rebalance(context.Background(), rebalanceSvc(), "hot", "cold"); err != nil {
		t.Fatal(err)
	}
	if rc.forceCalls != 0 {
		t.Errorf("dry-run must make zero mutations, got %d", rc.forceCalls)
	}
	if !contains(fr.suppressed, "rebalance:dry_run") {
		t.Errorf("suppressed = %v, want rebalance:dry_run", fr.suppressed)
	}
}

func TestGuardRebalanceCooldownSuppresses(t *testing.T) {
	rc := &recordingController{}
	fr := &fakeRecorder{}
	g := NewGuard(rc, NewCooldown(newFakeClock()), Cooldowns{Rebalance: time.Hour}, false, fr, discardLogger())

	svc := rebalanceSvc()
	_ = g.Rebalance(context.Background(), svc, "hot", "cold") // applied
	_ = g.Rebalance(context.Background(), svc, "hot", "cold") // within cooldown -> suppressed
	if rc.forceCalls != 1 {
		t.Errorf("second rebalance must be suppressed by cooldown, got %d force calls", rc.forceCalls)
	}
	if !contains(fr.suppressed, "rebalance:cooldown") {
		t.Errorf("suppressed = %v, want rebalance:cooldown", fr.suppressed)
	}
}
