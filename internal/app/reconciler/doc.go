// Package reconciler runs the periodic control loop and is the single guarded
// mutation path: every Swarm change (scale, force-update) passes through here
// and is gated by dry-run, opt-in labels, and per-service cooldown.
package reconciler
