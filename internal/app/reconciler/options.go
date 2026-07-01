package reconciler

import "time"

// TickSource produces the channel Run selects on for its periodic observations,
// together with a stop function that releases the source's resources. The
// production default wraps time.NewTicker; tests inject a source they can fire
// synchronously so the loop can be stepped deterministically through Run().
type TickSource func(interval time.Duration) (ticks <-chan time.Time, stop func())

// Option configures optional Reconciler behavior. Options are applied by New
// after the required dependencies are set, so they can override defaults.
type Option func(*Reconciler)

// WithTickSource overrides how Run obtains its ticks. Intended for tests that
// drive the loop deterministically; production leaves this unset and uses the
// default time.NewTicker source. A nil source is ignored (the default stays).
func WithTickSource(src TickSource) Option {
	return func(r *Reconciler) {
		if src == nil {
			return
		}
		r.tickSource = src
		r.customTick = true
	}
}

// defaultTickSource is the production tick source: a real time.Ticker whose
// channel and Stop are handed back to Run.
func defaultTickSource(interval time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(interval)
	return t.C, t.Stop
}
