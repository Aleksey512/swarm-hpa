package reconciler

import (
	"sync"
	"testing"
	"time"
)

// TestStabilizerConcurrentRecommend runs Recommend from many goroutines over
// several services with a non-zero window (the mutex-guarded path that mutates
// the per-service history) to prove it is race-free. Run under `go test -race`.
//
// Recommend takes `now` as a parameter, so no shared clock is mutated: each call
// supplies its own timestamp and the only shared state under test is the
// Stabilizer's history map.
func TestStabilizerConcurrentRecommend(t *testing.T) {
	t.Parallel()

	const (
		window     = 5 * time.Minute
		current    = uint64(10)
		workers    = 40
		iterations = 250
	)
	s := NewStabilizer(window)
	base := time.Unix(2_000_000, 0)
	services := []string{"svc1", "svc2", "svc3", "svc4"}

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				svc := services[(w+i)%len(services)]
				now := base.Add(time.Duration(i) * time.Second)
				desired := uint64((w + i) % 20) // spans scale-up, hold, and scale-down
				got := s.Recommend(svc, current, desired, now)

				// Invariant: a scale-down is never stabilized above current, and
				// a scale-up/hold passes through unchanged.
				if desired < current && got > current {
					t.Errorf("scale-down stabilized above current: desired=%d current=%d got=%d", desired, current, got)
				}
				if desired >= current && got != desired {
					t.Errorf("scale-up/hold changed: desired=%d got=%d", desired, got)
				}
			}
		}(w)
	}
	wg.Wait()
}
