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
	rebalancesTotal *prometheus.CounterVec
	suppressedTotal *prometheus.CounterVec
	errorsTotal     *prometheus.CounterVec

	// Agent-fleet metrics (populated on the manager as agents report in).
	agentsConnected     prometheus.Gauge
	agentReportsTotal   *prometheus.CounterVec
	agentDuplicateTotal *prometheus.CounterVec
	nodeCPUPct          *prometheus.GaugeVec
	nodeMemPct          *prometheus.GaugeVec

	logger *slog.Logger
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
		rebalancesTotal: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace, Name: "rebalances_total",
			Help: "Rebalance (force-update) actions applied, by service.",
		}, []string{"service"}),
		suppressedTotal: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace, Name: "actions_suppressed_total",
			Help: "Intended actions not applied, by action and reason.",
		}, []string{"action", "reason"}),
		errorsTotal: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace, Name: "errors_total",
			Help: "Recoverable errors, by pipeline stage.",
		}, []string{"stage"}),
		agentsConnected: f.NewGauge(prometheus.GaugeOpts{
			Namespace: metricNamespace, Name: "agents_connected",
			Help: "Live agents currently reporting to the manager.",
		}),
		agentReportsTotal: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace, Name: "agent_reports_total",
			Help: "Agent reports ingested, by node.",
		}, []string{"node"}),
		agentDuplicateTotal: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace, Name: "agent_duplicate_total",
			Help: "Duplicate/conflicting agent reports for a node from a distinct source, by node.",
		}, []string{"node"}),
		nodeCPUPct: f.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: metricNamespace, Name: "node_cpu_pct",
			Help: "Latest reported node CPU utilization (0..100), by node.",
		}, []string{"node"}),
		nodeMemPct: f.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: metricNamespace, Name: "node_mem_pct",
			Help: "Latest reported node memory utilization (0..100), by node.",
		}, []string{"node"}),
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

// ReconcileTick increments the completed-observe-pass counter.
func (r *Recorder) ReconcileTick() { r.reconcileTotal.Inc() }

// ObservedServices sets the managed-services gauge to the number seen this pass.
func (r *Recorder) ObservedServices(n int) { r.managedServices.Set(float64(n)) }

// ScaleApplied increments the applied-scales counter for the service.
func (r *Recorder) ScaleApplied(service string) {
	r.scalesTotal.WithLabelValues(service).Inc()
}

// HealApplied increments the applied-heals counter for the service.
func (r *Recorder) HealApplied(service string) {
	r.healsTotal.WithLabelValues(service).Inc()
}

// RebalanceApplied increments the applied-rebalances counter for the service.
func (r *Recorder) RebalanceApplied(service string) {
	r.rebalancesTotal.WithLabelValues(service).Inc()
}

// ActionSuppressed increments the suppressed-actions counter, labeled by action and reason.
func (r *Recorder) ActionSuppressed(action, reason string) {
	r.suppressedTotal.WithLabelValues(action, reason).Inc()
}

// Error increments the recoverable-error counter for the given pipeline stage.
func (r *Recorder) Error(stage string) {
	r.errorsTotal.WithLabelValues(stage).Inc()
}

// --- agent-fleet events (satisfies app/registry.Recorder) ---

// AgentConnected records a newly-seen agent joining the fleet.
func (r *Recorder) AgentConnected(string) { r.agentsConnected.Inc() }

// AgentDisconnected records an agent evicted as stale and drops its node gauges
// so a dead node's load does not linger in the metrics.
func (r *Recorder) AgentDisconnected(node string) {
	r.agentsConnected.Dec()
	r.nodeCPUPct.DeleteLabelValues(node)
	r.nodeMemPct.DeleteLabelValues(node)
}

// AgentReportReceived records one ingested report for a node.
func (r *Recorder) AgentReportReceived(node string) {
	r.agentReportsTotal.WithLabelValues(node).Inc()
}

// AgentDuplicate records a duplicate/conflicting agent for a node.
func (r *Recorder) AgentDuplicate(node string) {
	r.agentDuplicateTotal.WithLabelValues(node).Inc()
}

// NodeLoad sets a node's latest reported CPU/memory utilization gauges.
func (r *Recorder) NodeLoad(node string, cpuPct, memPct float64) {
	r.nodeCPUPct.WithLabelValues(node).Set(cpuPct)
	r.nodeMemPct.WithLabelValues(node).Set(memPct)
}
