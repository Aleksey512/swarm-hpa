package observability

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRecorderCountersAndGauge(t *testing.T) {
	r := NewRecorder("1.2.3", discardLogger())

	r.ReconcileTick()
	r.ReconcileTick()
	r.ObservedServices(3)
	r.ScaleApplied("web")
	r.HealApplied("api")
	r.ActionSuppressed("scale", "dry_run")
	r.Error("tasks")

	if got := testutil.ToFloat64(r.reconcileTotal); got != 2 {
		t.Errorf("reconcile_total = %v, want 2", got)
	}
	if got := testutil.ToFloat64(r.managedServices); got != 3 {
		t.Errorf("managed_services = %v, want 3", got)
	}
	if got := testutil.ToFloat64(r.scalesTotal.WithLabelValues("web")); got != 1 {
		t.Errorf("scales_total{web} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(r.healsTotal.WithLabelValues("api")); got != 1 {
		t.Errorf("heals_total{api} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(r.suppressedTotal.WithLabelValues("scale", "dry_run")); got != 1 {
		t.Errorf("actions_suppressed_total{scale,dry_run} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(r.errorsTotal.WithLabelValues("tasks")); got != 1 {
		t.Errorf("errors_total{tasks} = %v, want 1", got)
	}
}

// TestRecorderExpandedGauges asserts the v0.5.0 decision/pending/cooldown/stack
// gauges are recorded with the right values and labels, and that a decision
// change clears the stale last_decision series.
func TestRecorderExpandedGauges(t *testing.T) {
	r := NewRecorder("1.2.3", discardLogger())

	r.ServiceDecision("web", 2, 4, 160, "scale_up")
	r.ServicePendingTasks("web", 1)
	r.ServiceCooldown("web", "scale_up", true, 30)
	r.StackReplicas("demoapp", "db", 1, 1)

	if got := testutil.ToFloat64(r.currentReplicas.WithLabelValues("web")); got != 2 {
		t.Errorf("current_replicas{web} = %v, want 2", got)
	}
	if got := testutil.ToFloat64(r.desiredReplicas.WithLabelValues("web")); got != 4 {
		t.Errorf("desired_replicas{web} = %v, want 4", got)
	}
	if got := testutil.ToFloat64(r.metricValue.WithLabelValues("web")); got != 160 {
		t.Errorf("metric_value{web} = %v, want 160", got)
	}
	if got := testutil.ToFloat64(r.lastDecision.WithLabelValues("web", "scale_up")); got != 1 {
		t.Errorf("last_decision{web,scale_up} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(r.pendingTasks.WithLabelValues("web")); got != 1 {
		t.Errorf("pending_tasks{web} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(r.inCooldown.WithLabelValues("web", "scale_up")); got != 1 {
		t.Errorf("in_cooldown{web,scale_up} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(r.cooldownRemaining.WithLabelValues("web", "scale_up")); got != 30 {
		t.Errorf("cooldown_remaining_seconds{web,scale_up} = %v, want 30", got)
	}
	if got := testutil.ToFloat64(r.stackDesiredReplicas.WithLabelValues("demoapp", "db")); got != 1 {
		t.Errorf("stack_desired_replicas{demoapp,db} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(r.stackLiveReplicas.WithLabelValues("demoapp", "db")); got != 1 {
		t.Errorf("stack_live_replicas{demoapp,db} = %v, want 1", got)
	}

	// Decision change scale_up → hold must clear the stale scale_up series.
	r.ServiceDecision("web", 3, 3, 80, "hold")
	if got := testutil.ToFloat64(r.lastDecision.WithLabelValues("web", "hold")); got != 1 {
		t.Errorf("last_decision{web,hold} = %v, want 1 after change", got)
	}
	if got := testutil.ToFloat64(r.lastDecision.WithLabelValues("web", "scale_up")); got != 0 {
		t.Errorf("stale last_decision{web,scale_up} = %v, want 0 (cleared on change)", got)
	}
}

func TestRecorderRebalanceAndAgentMetrics(t *testing.T) {
	r := NewRecorder("1.0.0", discardLogger())

	r.RebalanceApplied("web")
	r.AgentConnected("node-a")
	r.AgentConnected("node-b")
	r.AgentReportReceived("node-a")
	r.AgentReportReceived("node-a")
	r.AgentDuplicate("node-b")
	r.NodeLoad("node-a", 42, 30)

	if got := testutil.ToFloat64(r.rebalancesTotal.WithLabelValues("web")); got != 1 {
		t.Errorf("rebalances_total{web} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(r.agentsConnected); got != 2 {
		t.Errorf("agents_connected = %v, want 2", got)
	}
	if got := testutil.ToFloat64(r.agentReportsTotal.WithLabelValues("node-a")); got != 2 {
		t.Errorf("agent_reports_total{node-a} = %v, want 2", got)
	}
	if got := testutil.ToFloat64(r.agentDuplicateTotal.WithLabelValues("node-b")); got != 1 {
		t.Errorf("agent_duplicate_total{node-b} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(r.nodeCPUPct.WithLabelValues("node-a")); got != 42 {
		t.Errorf("node_cpu_pct{node-a} = %v, want 42", got)
	}

	// Eviction drops the node gauges and decrements the connected count.
	r.AgentDisconnected("node-a")
	if got := testutil.ToFloat64(r.agentsConnected); got != 1 {
		t.Errorf("agents_connected after evict = %v, want 1", got)
	}
	if n := testutil.CollectAndCount(r.nodeCPUPct); n != 0 {
		t.Errorf("node_cpu_pct series after evict = %d, want 0 (gauge dropped)", n)
	}
}

func TestRecorderHandlerExposition(t *testing.T) {
	r := NewRecorder("9.9.9", discardLogger())
	r.ReconcileTick()
	r.ObservedServices(2)
	r.ScaleApplied("web")

	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)

	for _, want := range []string{
		`swarm_hpa_build_info{version="9.9.9"} 1`,
		"swarm_hpa_reconcile_total 1",
		"swarm_hpa_managed_services 2",
		`swarm_hpa_scales_total{service="web"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("exposition missing %q\n---\n%s", want, text)
		}
	}
}

// TestRecorderTaskErrorAndOrphanMetrics asserts the v0.9.0 task-error and
// orphan metrics: values, labels, and the stale-series deletion when a
// (service,class) drops out of the window.
func TestRecorderTaskErrorAndOrphanMetrics(t *testing.T) {
	r := NewRecorder("1.0.0", discardLogger())

	r.TaskErrorsWindow("web", "vxlan_file_exists", 2)
	r.TaskErrorsWindow("web", "other", 1)
	r.OrphanServices(4)
	r.StackTaskErrors("admin", "admin_web", "vxlan_file_exists", 2)
	r.DeployNetworkErrors("admin", 2)

	if got := testutil.ToFloat64(r.taskErrorsWindow.WithLabelValues("web", "vxlan_file_exists")); got != 2 {
		t.Errorf("task_errors_window{web,vxlan} = %v, want 2", got)
	}
	if got := testutil.ToFloat64(r.taskErrorsWindow.WithLabelValues("web", "other")); got != 1 {
		t.Errorf("task_errors_window{web,other} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(r.orphanServices); got != 4 {
		t.Errorf("orphan_services = %v, want 4", got)
	}
	if got := testutil.ToFloat64(r.stackTaskErrors.WithLabelValues("admin", "admin_web", "vxlan_file_exists")); got != 2 {
		t.Errorf("stack_task_errors_total = %v, want 2", got)
	}
	if got := testutil.ToFloat64(r.deployNetErrors.WithLabelValues("admin")); got != 2 {
		t.Errorf("deploy_network_errors_total = %v, want 2", got)
	}

	// Dropping to 0 must DELETE the series, not leave it lapping at the last
	// value (testutil.ToFloat64 panics on a deleted series — the intended
	// signal — so assert via the collected exposition instead).
	r.TaskErrorsWindow("web", "other", 0)
	families, err := r.registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	found := false
	for _, mf := range families {
		if mf.GetName() != "swarm_hpa_task_errors_window" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := m.GetLabel()
			if len(labels) == 2 && labels[0].GetValue() == "web" && labels[1].GetValue() == "other" {
				found = true
			}
		}
	}
	if found {
		t.Error("task_errors_window{web,other} must be deleted after dropping to 0")
	}
}
