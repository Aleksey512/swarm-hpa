// Package stackapi serves the read-only GitOps status surface: GET /stacks
// (JSON of per-stack status with on-demand drift) and a server-rendered HTML UI
// at GET / and /ui. It reads from the in-memory status store the sync loop writes
// and the live Swarm state via port.StackStateReader; it performs no mutations.
package stackapi

import (
	"context"
	_ "embed"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
	"github.com/Aleksey512/swarm-hpa/internal/core/stackstatus"
)

//go:embed ui.html
var uiHTML string

// uiTmpl is parsed once from the embedded HTML; html/template auto-escapes stack
// and service names so a malicious name can't inject markup.
var uiTmpl = template.Must(template.New("ui").Parse(uiHTML))

// liveTimeout bounds each per-stack Swarm read during drift computation so one
// slow/unreachable Swarm can't stall the whole /stacks response.
const liveTimeout = 2 * time.Second

// compile-time proof the handler satisfies http.Handler.
var _ http.Handler = (*Handler)(nil)

// Handler serves the read-only GitOps status surface.
type Handler struct {
	store       port.StackStatusStore
	live        port.StackStateReader
	reader      port.SwarmRead // optional: cluster-wide reads for orphan detection
	stackNames  []string       // configured stack names (stacks.yaml); nil ⇒ no orphan scan
	recorder    port.Recorder  // optional: publishes the orphan count as a metric
	logger      *slog.Logger
	concurrency int // configured --gitops-concurrency; 0 ⇒ hide the cap in the UI summary
}

// New builds the handler. A nil logger falls back to slog.Default. The store is
// the one the GitOps loop writes; live reads Swarm for on-demand drift.
// concurrency is the daemon's --gitops-concurrency (0 hides the "concurrency →
// ≤K parallel" line in the UI summary, e.g. when GitOps is off).
func New(store port.StackStatusStore, live port.StackStateReader, logger *slog.Logger, concurrency int) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{store: store, live: live, logger: logger, concurrency: concurrency}
}

// WithOrphanScan enables the orphan-services section: live Swarm services that
// belong to no configured stack and carry no swarm.autoscaler.* labels. The
// scan runs on demand per request (like drift), reading the cluster-wide
// service listing through reader. A nil reader or empty stack names disables
// it. recorder (optional, nil-safe) publishes the orphan count as the
// swarm_hpa_orphan_services gauge on every successful scan. Returns the
// receiver for chaining at the composition root.
func (h *Handler) WithOrphanScan(reader port.SwarmRead, stackNames []string, recorder port.Recorder) *Handler {
	if reader == nil || len(stackNames) == 0 {
		return h
	}
	h.reader = reader
	h.stackNames = stackNames
	if recorder != nil {
		h.recorder = recorder
	}
	return h
}

// ServeHTTP routes GET /stacks (JSON), GET / and /ui (HTML); 404 otherwise, 405
// for non-GET.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.logger.Debug("stackapi: request", "path", r.URL.Path, "method", r.Method)
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/stacks":
		h.serveJSON(w, r)
	case "/", "/ui":
		h.serveUI(w, r)
	default:
		http.NotFound(w, r)
	}
}

// serveJSON writes the per-stack status (with on-demand drift) plus a summary as JSON.
func (h *Handler) serveJSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(h.buildPayload()); err != nil {
		h.logger.Warn("stackapi: json encode failed", "err", err)
	}
}

// serveUI renders the read-only HTML table. Refresh to update.
func (h *Handler) serveUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := uiTmpl.Execute(w, h.buildPayload()); err != nil {
		h.logger.Warn("stackapi: ui render failed", "err", err)
	}
}

// payload is the JSON/UI shape: the per-stack rows plus a one-line summary header
// (total stacks, distinct repos, currently syncing/waiting, and the concurrency
// cap when wired). Both /stacks and / render the same payload.
type payload struct {
	Stacks  []stackResponse `json:"stacks"`
	Summary uiSummary       `json:"summary"`
	// Orphans are live services in no configured stack and under no
	// swarm.autoscaler.* management (on-demand scan; omitted when the scan
	// is not wired). OrphansError carries a read failure instead.
	Orphans      []orphanItem `json:"orphans,omitempty"`
	OrphansError string       `json:"orphans_error,omitempty"`
}

// orphanItem is the JSON shape for one orphan service.
type orphanItem struct {
	Name string `json:"name"`
	// Namespace is the stack namespace the service claims — a stack name that
	// is not in stacks.yaml (empty when the service was created outside any
	// stack, e.g. bare `docker service create`).
	Namespace string `json:"namespace,omitempty"`
}

// uiSummary is the one-line aggregate above the table. Syncing/Waiting are the
// counts of stacks currently in those live states (a Snapshot during a sync pass
// catches one instant). MaxParallel is min(concurrency, distinct repos); 0 when
// concurrency is not wired (GitOps off). Orphans is the orphan-services count
// from the on-demand scan (-1 when the scan failed, so the UI can say so).
type uiSummary struct {
	Stacks      int  `json:"stacks"`
	Repos       int  `json:"repos"`
	Syncing     int  `json:"syncing"`
	Waiting     int  `json:"waiting"`
	Orphans     int  `json:"orphans"`
	OrphansFail bool `json:"orphans_fail,omitempty"`
	Concurrency int  `json:"concurrency,omitempty"`
	MaxParallel int  `json:"max_parallel,omitempty"`
}

// buildPayload joins stored status with fresh on-demand drift and folds in the
// aggregate summary. A failed Swarm read for one stack degrades that stack's drift
// to an error note (never a 5xx for the whole payload). Drift is skipped when
// there is no desired snapshot yet (before the first successful render) or no live
// reader is wired.
func (h *Handler) buildPayload() payload {
	statuses := h.store.Snapshot()
	out := make([]stackResponse, 0, len(statuses))
	repos := map[string]struct{}{}
	var syncing, waiting int
	for _, st := range statuses {
		resp := stackResponse{
			Name:            st.Name,
			Repo:            st.Repo,
			State:           st.State,
			Revision:        st.Revision,
			OK:              st.OK,
			ErrorStage:      st.ErrorStage,
			ErrorMessage:    st.ErrorMessage,
			LastSync:        st.LastSync,
			DeployCount:     st.DeployCount,
			DesiredReplicas: st.DesiredReplicas,
			Files:           toFileResps(st.Files),
		}
		if st.Repo != "" {
			repos[st.Repo] = struct{}{}
		}
		switch st.State {
		case "syncing":
			syncing++
		case "waiting":
			waiting++
		}
		if h.live != nil && len(st.DesiredReplicas) > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
			live, err := h.live.StackServices(ctx, st.Name)
			cancel()
			if err != nil {
				resp.DriftError = err.Error()
			} else {
				resp.Drift = toDriftItems(stackstatus.Drift(st.DesiredReplicas, live))
				for _, d := range resp.Drift {
					if d.Drifted {
						resp.Drifted = true
					}
				}
			}
		}
		out = append(out, resp)
	}
	sum := uiSummary{
		Stacks:      len(out),
		Repos:       len(repos),
		Syncing:     syncing,
		Waiting:     waiting,
		Concurrency: h.concurrency,
	}
	if h.concurrency > 0 {
		sum.MaxParallel = h.concurrency
		if len(repos) > 0 && len(repos) < h.concurrency {
			sum.MaxParallel = len(repos)
		}
	}

	p := payload{Stacks: out, Summary: sum}
	h.scanOrphans(&p)
	return p
}

// scanOrphans fills the orphan section of the payload: one cluster-wide
// service read, then the pure stackstatus.Orphans rule against the configured
// stack names. Like drift, a read failure degrades to an error note instead of
// failing the response. A no-op when the scan is not wired.
func (h *Handler) scanOrphans(p *payload) {
	if h.reader == nil {
		return
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
	services, err := h.reader.AllServices(ctx)
	cancel()
	if err != nil {
		h.logger.Warn("stackapi: orphan scan failed", "err", err)
		p.OrphansError = err.Error()
		p.Summary.OrphansFail = true
		return
	}

	found := stackstatus.Orphans(h.stackNames, services)
	items := make([]orphanItem, 0, len(found))
	for _, svc := range found {
		items = append(items, orphanItem{Name: svc.Name, Namespace: svc.StackNamespace})
	}
	p.Orphans = items
	p.Summary.Orphans = len(items)
	if h.recorder != nil {
		h.recorder.OrphanServices(len(items))
	}
	h.logger.Debug("stackapi: orphan scan done",
		"orphans", len(items), "services_scanned", len(services),
		"took", time.Since(started).String())
}

// stackResponse is the JSON shape for one stack.
type stackResponse struct {
	Name            string            `json:"name"`
	Repo            string            `json:"repo,omitempty"`
	State           string            `json:"state,omitempty"`
	Revision        string            `json:"revision"`
	OK              bool              `json:"ok"`
	ErrorStage      string            `json:"error_stage,omitempty"`
	ErrorMessage    string            `json:"error_message,omitempty"`
	LastSync        time.Time         `json:"last_sync"`
	DeployCount     uint64            `json:"deploy_count"`
	DesiredReplicas map[string]uint64 `json:"desired_replicas,omitempty"`
	Files           []fileStatusResp  `json:"files,omitempty"`
	Drift           []driftItem       `json:"drift,omitempty"`
	Drifted         bool              `json:"drifted"`
	DriftError      string            `json:"drift_error,omitempty"`
}

// driftItem is the JSON shape for one service's drift (detached from
// model.ServiceDrift so the HTTP layer owns its serialization).
type driftItem struct {
	Service string `json:"service"`
	Desired uint64 `json:"desired"`
	Live    uint64 `json:"live"`
	Drifted bool   `json:"drifted"`
}

func toDriftItems(in []model.ServiceDrift) []driftItem {
	out := make([]driftItem, len(in))
	for i, d := range in {
		out[i] = driftItem{Service: d.Service, Desired: d.Desired, Live: d.Live, Drifted: d.Drifted}
	}
	return out
}

// fileStatusResp is the JSON shape for one merge group's deploy outcome in a
// (possibly multi-file) stack — detached from model.StackFileStatus so the HTTP
// layer owns its serialization. One entry = one `docker stack deploy`: File plus
// the Overrides merged into it. Status is ""|ok|failed|skipped (see the model's
// state machine).
type fileStatusResp struct {
	File       string   `json:"file"`
	Overrides  []string `json:"overrides,omitempty"`
	PullPolicy string   `json:"pull_policy,omitempty"`
	Status     string   `json:"status"`
	Error      string   `json:"error,omitempty"`
}

// toFileResps maps the stored per-group status to the JSON shape (nil in, nil out
// so an empty slice stays absent via omitempty).
func toFileResps(in []model.StackFileStatus) []fileStatusResp {
	if len(in) == 0 {
		return nil
	}
	out := make([]fileStatusResp, len(in))
	for i, f := range in {
		out[i] = fileStatusResp{
			File:       f.File,
			Overrides:  f.Overrides,
			PullPolicy: f.PullPolicy,
			Status:     f.Status,
			Error:      f.Error,
		}
	}
	return out
}
