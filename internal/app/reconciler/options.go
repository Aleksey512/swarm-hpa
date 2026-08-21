package reconciler

import (
	"time"

	apptaskerrors "github.com/Aleksey512/swarm-hpa/internal/app/taskerrors"
	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// LoadSource yields the latest per-node agent reports the rebalancer compares.
// Implemented by app/registry.Registry. When unset (nil), rebalancing is
// disabled (e.g. no agent fleet wired).
type LoadSource interface {
	Snapshot() []model.AgentReport
}

// WithRebalancing enables the load-aware rebalance branch. loads supplies the
// per-node load snapshot and threshold is the node-load spread fraction (0,1]
// at/above which a move is proposed. A nil loads leaves rebalancing disabled.
func WithRebalancing(loads LoadSource, threshold float64) Option {
	return func(r *Reconciler) {
		if loads != nil {
			r.loads = loads
			r.rebalanceThreshold = threshold
		}
	}
}

// WithTaskErrors enables the cluster-wide task-error observation pass: once
// per tick the reconciler reads ALL tasks (unfiltered — superseded/rejected
// tasks included) plus ALL services (for the ServiceID→name join), classifies
// every task status error, and records it in the shared sliding-window
// tracker. The tracker is the same instance the metrics exporter and the
// post-deploy GitOps alert read. A nil sink or nil read leaves the feature
// off — no extra Docker calls are issued.
func WithTaskErrors(sink *apptaskerrors.Tracker, read port.SwarmRead) Option {
	return func(r *Reconciler) {
		if sink == nil || read == nil {
			return
		}
		r.errorSink = sink
		r.errRead = read
	}
}

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
