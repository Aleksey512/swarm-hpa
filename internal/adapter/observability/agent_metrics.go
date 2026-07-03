package observability

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// AgentRecorder is the agent role's minimal self-metrics surface, served on its
// own private registry (independent of the manager Recorder). It exposes how many
// reports the agent has pushed, how many failed, and when the last one was sent
// (as a unix-seconds gauge, so Prometheus can derive report age via time()-gauge).
type AgentRecorder struct {
	registry     *prometheus.Registry
	reportsTotal prometheus.Counter
	errorsTotal  prometheus.Counter
	lastReportTS prometheus.Gauge
	logger       *slog.Logger
}

// NewAgentRecorder builds an AgentRecorder on a private registry and records
// build info for the given version. A nil logger falls back to slog.Default.
func NewAgentRecorder(version string, logger *slog.Logger) *AgentRecorder {
	if logger == nil {
		logger = slog.Default()
	}
	reg := prometheus.NewRegistry()
	f := promauto.With(reg)

	a := &AgentRecorder{
		registry: reg,
		reportsTotal: f.NewCounter(prometheus.CounterOpts{
			Namespace: metricNamespace, Name: "agent_reports_sent_total",
			Help: "Reports successfully pushed to the manager by this agent.",
		}),
		errorsTotal: f.NewCounter(prometheus.CounterOpts{
			Namespace: metricNamespace, Name: "agent_report_errors_total",
			Help: "Report cycles that failed to reach the manager.",
		}),
		lastReportTS: f.NewGauge(prometheus.GaugeOpts{
			Namespace: metricNamespace, Name: "agent_last_report_timestamp_seconds",
			Help: "Unix time of the last successful report (0 if none yet).",
		}),
		logger: logger,
	}

	f.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace, Name: "build_info",
		Help: "Build information; constant 1 with the version label.",
	}, []string{"version"}).WithLabelValues(version).Set(1)

	logger.Debug("observability: agent metrics recorder configured", "version", version)
	return a
}

// Handler serves the agent's metrics in the Prometheus text exposition format.
func (a *AgentRecorder) Handler() http.Handler {
	return promhttp.HandlerFor(a.registry, promhttp.HandlerOpts{})
}

// ReportSent records a successful report at time at.
func (a *AgentRecorder) ReportSent(at time.Time) {
	a.reportsTotal.Inc()
	a.lastReportTS.Set(float64(at.Unix()))
}

// ReportError records a failed report cycle.
func (a *AgentRecorder) ReportError() { a.errorsTotal.Inc() }
