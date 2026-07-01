// Package agentloop orchestrates the agent role: on each tick it collects the
// local node's load and pushes it to the manager. It is the agent-side analogue
// of app/reconciler — it depends only on small interfaces (a collector, a
// reporter, a recorder), so the composition root injects the concrete adapters
// and tests inject fakes plus a deterministic tick source.
package agentloop

import (
	"context"
	"log/slog"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// Collector samples the local node once, producing a report.
type Collector interface {
	Collect(ctx context.Context) (model.AgentReport, error)
}

// Reporter pushes a report to the manager.
type Reporter interface {
	Report(ctx context.Context, report model.AgentReport) error
}

// Recorder observes the agent's own report outcomes (self-metrics). A nil
// recorder is replaced with a no-op.
type Recorder interface {
	ReportSent(at time.Time)
	ReportError()
}

// TickSource produces the channel Run selects on for its periodic reports, plus
// a stop function. Production wraps time.NewTicker; tests inject a source they
// fire synchronously to step the loop.
type TickSource func(interval time.Duration) (ticks <-chan time.Time, stop func())

// Option configures optional Loop behavior, applied after required deps are set.
type Option func(*Loop)

// WithTickSource overrides how Run obtains ticks (tests drive it deterministically).
// A nil source is ignored.
func WithTickSource(src TickSource) Option {
	return func(l *Loop) {
		if src != nil {
			l.tickSource = src
		}
	}
}

func defaultTickSource(interval time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(interval)
	return t.C, t.Stop
}

type nopRecorder struct{}

func (nopRecorder) ReportSent(time.Time) {}
func (nopRecorder) ReportError()         {}

// Loop is the agent's collect-and-report control loop.
type Loop struct {
	collector  Collector
	reporter   Reporter
	recorder   Recorder
	clock      port.Clock
	logger     *slog.Logger
	tickSource TickSource
}

// New constructs a Loop. A nil recorder/clock/logger fall back to a no-op,
// the system clock, and slog.Default respectively.
func New(collector Collector, reporter Reporter, recorder Recorder, clock port.Clock, logger *slog.Logger, opts ...Option) *Loop {
	if recorder == nil {
		recorder = nopRecorder{}
	}
	if clock == nil {
		clock = port.SystemClock{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	l := &Loop{
		collector:  collector,
		reporter:   reporter,
		recorder:   recorder,
		clock:      clock,
		logger:     logger,
		tickSource: defaultTickSource,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Run reports immediately and then on every interval tick until ctx is cancelled
// (a graceful stop, returning nil). A single failed cycle never stops the loop.
func (l *Loop) Run(ctx context.Context, interval time.Duration) error {
	l.logger.Info("agent report loop started", "interval", interval)

	ticks, stop := l.tickSource(interval)
	defer stop()

	l.reportOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			l.logger.Info("agent report loop stopping", "reason", ctx.Err())
			return nil
		case <-ticks:
			l.reportOnce(ctx)
		}
	}
}

// reportOnce collects and pushes a single report, logging and recording the
// outcome. It never returns an error: a bad cycle is logged and the loop waits
// for the next tick (mirroring the reconciler's resilience posture).
func (l *Loop) reportOnce(ctx context.Context) {
	report, err := l.collector.Collect(ctx)
	if err != nil {
		l.logger.Warn("agent: collect failed", "err", err)
		l.recorder.ReportError()
		return
	}
	if err := l.reporter.Report(ctx, report); err != nil {
		l.logger.Warn("agent: report failed", "node", report.NodeID, "err", err)
		l.recorder.ReportError()
		return
	}
	l.recorder.ReportSent(l.clock.Now())
	l.logger.Debug("agent: report cycle complete",
		"node", report.NodeID, "tasks", report.Node.TaskCount,
		"cpu_pct", report.Node.CPUPercent, "mem_pct", report.Node.MemPercent)
}
