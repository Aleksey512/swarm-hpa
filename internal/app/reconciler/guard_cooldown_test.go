package reconciler

import (
	"context"
	"testing"
	"time"
)

// TestGuardCooldownDirectionMatrix exercises the Guard's direction-aware cooldown
// as a table. The contract under test (see cooldown.go / guard.go):
//
//   - There is ONE last-action timestamp per service (shared across directions).
//   - The Guard selects WHICH window to compare against by the action's direction
//     (ScaleUp for a scale-up, ScaleDown for a scale-down, Heal for a heal).
//   - A window of 0 always allows, regardless of any recorded timestamp.
//   - A no-op (desired == current) neither mutates nor records a timestamp.
//
// dryRun is false throughout so real mutations are observable; a fakeRecorder
// captures ActionSuppressed emissions.
func TestGuardCooldownDirectionMatrix(t *testing.T) {
	type step struct {
		advance time.Duration // advance the fake clock before this action
		action  string        // "up" | "down" | "heal"
		from    uint64        // current replicas (for scales)
		to      uint64        // desired replicas (for scales)
	}
	cases := []struct {
		name           string
		windows        Cooldowns
		steps          []step
		wantScale      int
		wantForce      int
		wantSuppressed []string
	}{
		{
			name:           "scale-up suppressed within ScaleUp window",
			windows:        Cooldowns{ScaleUp: time.Minute},
			steps:          []step{{action: "up", from: 2, to: 4}, {action: "up", from: 4, to: 5}},
			wantScale:      1,
			wantSuppressed: []string{"scale:cooldown"},
		},
		{
			name:           "scale-down suppressed within ScaleDown window",
			windows:        Cooldowns{ScaleDown: time.Minute},
			steps:          []step{{action: "down", from: 5, to: 3}, {action: "down", from: 3, to: 2}},
			wantScale:      1,
			wantSuppressed: []string{"scale:cooldown"},
		},
		{
			name:      "up uses ScaleUp(0) window: allowed right after another action",
			windows:   Cooldowns{ScaleUp: 0, ScaleDown: 5 * time.Minute},
			steps:     []step{{action: "down", from: 5, to: 3}, {action: "up", from: 3, to: 5}},
			wantScale: 2,
		},
		{
			name:           "down uses ScaleDown window: a recent up blocks it (shared timestamp)",
			windows:        Cooldowns{ScaleUp: 0, ScaleDown: 5 * time.Minute},
			steps:          []step{{action: "up", from: 2, to: 5}, {action: "down", from: 5, to: 3}},
			wantScale:      1,
			wantSuppressed: []string{"scale:cooldown"},
		},
		{
			name:      "cooldown elapses then allows",
			windows:   Cooldowns{ScaleUp: time.Minute},
			steps:     []step{{action: "up", from: 2, to: 4}, {advance: time.Minute, action: "up", from: 4, to: 5}},
			wantScale: 2,
		},
		{
			name:      "no-op does not consume cooldown",
			windows:   Cooldowns{ScaleUp: time.Minute, ScaleDown: time.Minute},
			steps:     []step{{action: "up", from: 3, to: 3}, {action: "down", from: 3, to: 1}},
			wantScale: 1,
		},
		{
			name:      "heal window independent of scale (Heal=0)",
			windows:   Cooldowns{ScaleDown: 5 * time.Minute, Heal: 0},
			steps:     []step{{action: "down", from: 5, to: 3}, {action: "heal"}},
			wantScale: 1,
			wantForce: 1,
		},
		{
			name:           "heal suppressed within Heal window",
			windows:        Cooldowns{Heal: time.Minute},
			steps:          []step{{action: "heal"}, {action: "heal"}},
			wantForce:      1,
			wantSuppressed: []string{"heal:cooldown"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := newFakeClock()
			rc := &recordingController{}
			fr := &fakeRecorder{}
			g := NewGuard(rc, NewCooldown(clk), tc.windows, false, fr, discardLogger())

			for i, s := range tc.steps {
				if s.advance > 0 {
					clk.advance(s.advance)
				}
				var err error
				if s.action == "heal" {
					err = g.Heal(context.Background(), replicatedSvc(1))
				} else {
					err = g.Scale(context.Background(), replicatedSvc(s.from), s.to)
				}
				if err != nil {
					t.Fatalf("step %d (%s): unexpected error: %v", i, s.action, err)
				}
			}

			if rc.scaleCalls != tc.wantScale {
				t.Errorf("scale calls = %d, want %d", rc.scaleCalls, tc.wantScale)
			}
			if rc.forceCalls != tc.wantForce {
				t.Errorf("force calls = %d, want %d", rc.forceCalls, tc.wantForce)
			}
			for _, want := range tc.wantSuppressed {
				if !contains(fr.suppressed, want) {
					t.Errorf("suppressed = %v, want to contain %q", fr.suppressed, want)
				}
			}
		})
	}
}
