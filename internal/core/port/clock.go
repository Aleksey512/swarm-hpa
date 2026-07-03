package port

import "time"

// Clock provides the current time. It is injected so cooldown and
// "pending too long" logic can be tested without real time.
type Clock interface {
	Now() time.Time
}

// SystemClock is the production Clock backed by time.Now.
type SystemClock struct{}

// Now returns the current wall-clock time.
func (SystemClock) Now() time.Time { return time.Now() }
