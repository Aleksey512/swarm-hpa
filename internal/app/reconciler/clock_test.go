package reconciler

import "time"

// fakeClock is a controllable port.Clock for deterministic cooldown/guard tests.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_000_000, 0)} }
