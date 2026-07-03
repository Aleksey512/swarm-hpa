package reconciler

import (
	"sync"
	"time"
)

// Stabilizer dampens scale-downs the way the Kubernetes HPA scale-down
// stabilization window does: it keeps a short per-service history of raw
// recommendations and, for a scale-down, holds the service at the largest
// recommendation seen within the window — so a brief metric dip never shrinks
// it. Scale-ups (and holds) pass through immediately; the window only dampens
// shrinking. A window of 0 disables it.
//
// State is in-memory and time is injected via Recommend's `now`, so it is
// deterministic and restart-safe (the daemon re-observes before acting).
type Stabilizer struct {
	window time.Duration

	mu      sync.Mutex
	history map[string][]recSample
}

// recSample is one recorded recommendation with its timestamp.
type recSample struct {
	at  time.Time
	rec uint64
}

// NewStabilizer returns a stabilizer with the given scale-down window.
func NewStabilizer(window time.Duration) *Stabilizer {
	return &Stabilizer{
		window:  window,
		history: make(map[string][]recSample),
	}
}

// Recommend records the raw desired recommendation for serviceID at `now` and
// returns the stabilized value to act on. For a scale-down (desired < current)
// it returns the largest recommendation within the window, capped at current so
// stabilization never turns a shrink into a grow; otherwise it returns desired
// unchanged.
func (s *Stabilizer) Recommend(serviceID string, current, desired uint64, now time.Time) uint64 {
	if s.window <= 0 {
		return desired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Append this recommendation and prune entries older than the window
	// (in-place compaction reusing the backing array).
	cutoff := now.Add(-s.window)
	kept := s.history[serviceID][:0]
	for _, e := range s.history[serviceID] {
		if !e.at.Before(cutoff) {
			kept = append(kept, e)
		}
	}
	kept = append(kept, recSample{at: now, rec: desired})
	s.history[serviceID] = kept

	if desired >= current {
		// Scale-up or hold: act immediately; the window only dampens shrink.
		return desired
	}

	// Scale-down: hold at the largest recommendation in the window, but never
	// grow (cap at current).
	maxRec := desired
	for _, e := range kept {
		if e.rec > maxRec {
			maxRec = e.rec
		}
	}
	if maxRec > current {
		return current
	}
	return maxRec
}
