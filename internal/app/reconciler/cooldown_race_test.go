package reconciler

import (
	"sync"
	"testing"
	"time"
)

// TestCooldownConcurrentAccess hammers Allowed/Record from many goroutines
// across several service IDs to prove the mutex keeps the per-service map safe.
// Run under `go test -race` (make test-race) to detect data races.
//
// The clock is fixed for the whole run (never advanced), so fakeClock.Now is a
// read-only access from every goroutine — the only shared mutable state under
// test is Cooldown's internal map, which is exactly what we want -race to watch.
func TestCooldownConcurrentAccess(t *testing.T) {
	t.Parallel()

	cd := NewCooldown(newFakeClock())
	const window = time.Minute
	services := []string{"a", "b", "c", "d", "e"}

	const (
		workers    = 50
		iterations = 200
	)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				svc := services[(w+i)%len(services)]
				cd.Allowed(svc, window) // windowed path (reads the map)
				cd.Record(svc)          // writes the map
				cd.Allowed(svc, 0)      // window 0 always-allow fast path
			}
		}(w)
	}
	wg.Wait()

	// Invariant: with the clock never advanced, every service was Recorded at
	// the current time, so a windowed Allowed must report "still cooling down".
	for _, svc := range services {
		if cd.Allowed(svc, window) {
			t.Errorf("service %q: expected within cooldown (clock never advanced after Record)", svc)
		}
	}
}
