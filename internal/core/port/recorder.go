package port

// Recorder records the daemon's own behavior as observability signals (counters
// and gauges). The app layer (reconciler, guard) depends on this interface; the
// concrete, Prometheus-backed implementation lives in adapter/observability, so
// the core never imports a metrics client.
type Recorder interface {
	// ReconcileTick records one completed observe pass.
	ReconcileTick()
	// ObservedServices records how many managed services were seen this pass.
	ObservedServices(n int)
	// ScaleApplied records a real scale applied to a service.
	ScaleApplied(service string)
	// HealApplied records a real force-update (heal) applied to a service.
	HealApplied(service string)
	// ActionSuppressed records an action that was intended but not applied.
	// action is "scale" or "heal"; reason is "dry_run" or "cooldown".
	ActionSuppressed(action, reason string)
	// Error records a recoverable error by pipeline stage (for example
	// "services", "tasks", "nodes", "metric", "scale", "heal").
	Error(stage string)
}

// NopRecorder is a Recorder that does nothing. It is the safe default when no
// metrics sink is wired (tests, or a future metrics-disabled mode), so call
// sites never need a nil check.
type NopRecorder struct{}

// compile-time proof the no-op satisfies the interface.
var _ Recorder = NopRecorder{}

// ReconcileTick does nothing.
func (NopRecorder) ReconcileTick() {}

// ObservedServices does nothing.
func (NopRecorder) ObservedServices(int) {}

// ScaleApplied does nothing.
func (NopRecorder) ScaleApplied(string) {}

// HealApplied does nothing.
func (NopRecorder) HealApplied(string) {}

// ActionSuppressed does nothing.
func (NopRecorder) ActionSuppressed(string, string) {}

// Error does nothing.
func (NopRecorder) Error(string) {}
