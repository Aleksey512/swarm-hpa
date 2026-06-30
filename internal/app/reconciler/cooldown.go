package reconciler

import (
	"sync"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// Cooldown rate-limits mutations per service: a service may be acted on at most
// once per window. A window of 0 disables rate-limiting. Separate scale-up /
// scale-down windows are a later (stabilization) milestone.
type Cooldown struct {
	window time.Duration
	clock  port.Clock

	mu   sync.Mutex
	last map[string]time.Time
}

// NewCooldown returns a cooldown tracker. A nil clock falls back to the system clock.
func NewCooldown(window time.Duration, clock port.Clock) *Cooldown {
	if clock == nil {
		clock = port.SystemClock{}
	}
	return &Cooldown{
		window: window,
		clock:  clock,
		last:   make(map[string]time.Time),
	}
}

// Allowed reports whether an action on serviceID is permitted now — never acted
// before, or at least `window` has elapsed since the last action. A zero window
// always allows.
func (c *Cooldown) Allowed(serviceID string) bool {
	if c.window <= 0 {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	last, ok := c.last[serviceID]
	if !ok {
		return true
	}
	return c.clock.Now().Sub(last) >= c.window
}

// Record stamps the current time as serviceID's last action.
func (c *Cooldown) Record(serviceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last[serviceID] = c.clock.Now()
}
