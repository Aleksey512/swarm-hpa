package reconciler

import (
	"sync"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// Cooldown is a per-service "last action" timestamp tracker. The window to
// enforce is supplied per call (Allowed), so the same tracker can serve
// direction-aware scale cooldowns and the heal cooldown — the Guard chooses the
// window. A window of 0 always allows.
type Cooldown struct {
	clock port.Clock

	mu   sync.Mutex
	last map[string]time.Time
}

// NewCooldown returns a cooldown tracker. A nil clock falls back to the system clock.
func NewCooldown(clock port.Clock) *Cooldown {
	if clock == nil {
		clock = port.SystemClock{}
	}
	return &Cooldown{
		clock: clock,
		last:  make(map[string]time.Time),
	}
}

// Allowed reports whether an action on serviceID is permitted now given window:
// never acted before, or at least `window` has elapsed since the last action. A
// window of 0 always allows.
func (c *Cooldown) Allowed(serviceID string, window time.Duration) bool {
	if window <= 0 {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	last, ok := c.last[serviceID]
	if !ok {
		return true
	}
	return c.clock.Now().Sub(last) >= window
}

// Record stamps the current time as serviceID's last action.
func (c *Cooldown) Record(serviceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last[serviceID] = c.clock.Now()
}

// Cooldowns holds the per-action cooldown windows the Guard enforces: scale-ups
// and scale-downs each get their own window, while heal and rebalance each use
// their own. All actions on a service share one "last action" timestamp, so a
// recent mutation of any kind delays the next one — avoiding stacked disruption.
type Cooldowns struct {
	ScaleUp   time.Duration
	ScaleDown time.Duration
	Heal      time.Duration
	Rebalance time.Duration
}
