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
	store  port.StackStatusStore
	live   port.StackStateReader
	logger *slog.Logger
}

// New builds the handler. A nil logger falls back to slog.Default. The store is
// the one the GitOps loop writes; live reads Swarm for on-demand drift.
func New(store port.StackStatusStore, live port.StackStateReader, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{store: store, live: live, logger: logger}
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

// serveJSON writes the per-stack status (with on-demand drift) as JSON.
func (h *Handler) serveJSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{"stacks": h.buildResponses()}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.logger.Warn("stackapi: json encode failed", "err", err)
	}
}

// serveUI renders the read-only HTML table. Refresh to update.
func (h *Handler) serveUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := uiTmpl.Execute(w, h.buildResponses()); err != nil {
		h.logger.Warn("stackapi: ui render failed", "err", err)
	}
}

// buildResponses joins stored status with fresh on-demand drift. A failed Swarm
// read for one stack degrades that stack's drift to an error note (never a 5xx
// for the whole payload). Drift is skipped when there is no desired snapshot yet
// (before the first successful render) or no live reader is wired.
func (h *Handler) buildResponses() []stackResponse {
	statuses := h.store.Snapshot()
	out := make([]stackResponse, 0, len(statuses))
	for _, st := range statuses {
		resp := stackResponse{
			Name:            st.Name,
			Revision:        st.Revision,
			OK:              st.OK,
			ErrorStage:      st.ErrorStage,
			ErrorMessage:    st.ErrorMessage,
			LastSync:        st.LastSync,
			DeployCount:     st.DeployCount,
			DesiredReplicas: st.DesiredReplicas,
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
	return out
}

// stackResponse is the JSON shape for one stack.
type stackResponse struct {
	Name            string            `json:"name"`
	Revision        string            `json:"revision"`
	OK              bool              `json:"ok"`
	ErrorStage      string            `json:"error_stage,omitempty"`
	ErrorMessage    string            `json:"error_message,omitempty"`
	LastSync        time.Time         `json:"last_sync"`
	DeployCount     uint64            `json:"deploy_count"`
	DesiredReplicas map[string]uint64 `json:"desired_replicas,omitempty"`
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
