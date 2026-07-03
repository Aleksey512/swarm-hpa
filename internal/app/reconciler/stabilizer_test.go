package reconciler

import (
	"testing"
	"time"
)

func TestStabilizerDisabledPassesThrough(t *testing.T) {
	s := NewStabilizer(0)
	now := time.Unix(1_000, 0)
	if got := s.Recommend("s", 5, 2, now); got != 2 {
		t.Errorf("a disabled stabilizer must return desired, got %d", got)
	}
}

func TestStabilizerScaleUpImmediate(t *testing.T) {
	s := NewStabilizer(time.Minute)
	now := time.Unix(1_000, 0)
	if got := s.Recommend("s", 2, 5, now); got != 5 {
		t.Errorf("a scale-up must pass through, got %d", got)
	}
}

func TestStabilizerHoldsScaleDownWithinWindow(t *testing.T) {
	s := NewStabilizer(time.Minute)
	t0 := time.Unix(1_000, 0)
	s.Recommend("s", 5, 5, t0) // recently wanted 5
	// A dip recommends 2 while current is still 5; within the window → hold at 5.
	if got := s.Recommend("s", 5, 2, t0.Add(10*time.Second)); got != 5 {
		t.Errorf("scale-down within the window must hold at the recent max (5), got %d", got)
	}
}

func TestStabilizerAllowsScaleDownAfterWindow(t *testing.T) {
	s := NewStabilizer(time.Minute)
	t0 := time.Unix(1_000, 0)
	s.Recommend("s", 5, 5, t0) // wanted 5
	// After the window elapses the old high recommendation is pruned.
	if got := s.Recommend("s", 5, 2, t0.Add(2*time.Minute)); got != 2 {
		t.Errorf("after the window, scale-down should proceed to 2, got %d", got)
	}
}

func TestStabilizerNeverGrowsOnScaleDown(t *testing.T) {
	s := NewStabilizer(time.Minute)
	t0 := time.Unix(1_000, 0)
	s.Recommend("s", 8, 8, t0) // wanted 8 when current was 8
	// Current later sits at 6; a dip recommends 3. Max-over-window is 8, but the
	// result is capped at current — stabilization never turns a shrink into growth.
	if got := s.Recommend("s", 6, 3, t0.Add(5*time.Second)); got != 6 {
		t.Errorf("scale-down must cap at current (6), got %d", got)
	}
}

func TestStabilizerPerServiceIsolation(t *testing.T) {
	s := NewStabilizer(time.Minute)
	t0 := time.Unix(1_000, 0)
	s.Recommend("a", 9, 9, t0) // service a recently wanted 9
	// service b's scale-down must not be affected by a's history.
	if got := s.Recommend("b", 5, 2, t0.Add(time.Second)); got != 2 {
		t.Errorf("service b scale-down should be 2 (a's history must not leak), got %d", got)
	}
}
