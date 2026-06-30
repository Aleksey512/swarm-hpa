package observability

import (
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// metricNamespace prefixes every self-metric the daemon exposes.
const metricNamespace = "swarm_hpa"

// Recorder is the Prometheus-backed port.Recorder. It registers the daemon's
// own metrics on a private registry (not the global default), so it stays
// testable and free of duplicate-registration panics, and serves them through
// Handler(). It is the metrics-out counterpart to the metric providers (in).
type Recorder struct {
	registry        *prometheus.Registry
	reconcileTotal  prometheus.Counter
	managedServices prometheus.Gauge
	scalesTotal     *prometheus.CounterVec
	healsTotal      *prometheus.CounterVec
	suppressedTotal *prometheus.CounterVec
	errorsTotal     *prometheus.CounterVec
	logger          *slog.Logger
}

// compile-time proof the recorder satisfies the core port.
var _ port.Recorder = (*Recorder)(nil)

// NewRecorder builds a Recorder, registers its metrics on a private registry,
// and records build info for the given version. A nil logger falls back to
// slog.Default.
func NewRecorder(version string, logger *slog.Logger) *Recorder {
	if logger == nil {
		logger = slog.Default()
	}
	reg := prometheus.NewRegistry()
	f := promauto.With(reg)

	r := &Recorder{
		registry: reg,
		reconcileTotal: f.NewCounter(prometheus.CounterOpts{
			Namespace: metricNamespace, Name: "reconcile_total",
			Help: "Number of completed reconcile passes.",
		}),
		managedServices: f.NewGauge(prometheus.GaugeOpts{
			Namespace: metricNamespace, Name: "managed_services",
			Help: "Opted-in services observed in the last reconcile pass.",
		}),
		scalesTotal: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace, Name: "scales_total",
			Help: "Scale actions applied, by service.",
		}, []string{"service"}),
		healsTotal: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace, Name: "heals_total",
			Help: "Heal (force-update) actions applied, by service.",
		}, []string{"service"}),
		suppressedTotal: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace, Name: "actions_suppressed_total",
			Help: "Intended actions not applied, by action and reason.",
		}, []string{"action", "reason"}),
		errorsTotal: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace, Name: "errors_total",
			Help: "Recoverable errors, by pipeline stage.",
		}, []string{"stage"}),
		logger: logger,
	}

	// build_info is a constant 1 carrying the version as a label.
	f.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace, Name: "build_info",
		Help: "Build information; constant 1 with the version label.",
	}, []string{"version"}).WithLabelValues(version).Set(1)

	logger.Debug("observability: metrics recorder configured", "version", version)
	return r
}

// Handler serves the recorder's metrics in the Prometheus text exposition format.
func (r *Recorder) Handler() http.Handler {
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{})
}

func (r *Recorder) ReconcileTick()         { r.reconcileTotal.Inc() }
func (r *Recorder) ObservedServices(n int) { r.managedServices.Set(float64(n)) }
func (r *Recorder) ScaleApplied(service string) {
	r.scalesTotal.WithLabelValues(service).Inc()
}
func (r *Recorder) HealApplied(service string) {
	r.healsTotal.WithLabelValues(service).Inc()
}
func (r *Recorder) ActionSuppressed(action, reason string) {
	r.suppressedTotal.WithLabelValues(action, reason).Inc()
}
func (r *Recorder) Error(stage string) {
	r.errorsTotal.WithLabelValues(stage).Inc()
}
