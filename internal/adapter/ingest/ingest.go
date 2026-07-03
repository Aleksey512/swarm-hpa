// Package ingest is the manager-side inbound HTTP adapter that receives agent
// reports (POST /v1/report) and hands them to a core ReportSink. It authenticates
// with the shared INGEST_TOKEN bearer (constant-time compare) and cross-checks
// the reported node ID against the live Swarm node list, rejecting reports from
// unknown nodes — defense-in-depth behind the registry's node-ID dedup.
package ingest

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

const (
	// ReportPath is the route agents POST reports to. It must match the agent
	// reporter's path.
	ReportPath = "/v1/report"

	// maxBodyBytes caps a report body to bound memory per request.
	maxBodyBytes = 1 << 20 // 1 MiB

	// nodesTimeout bounds the Swarm node-list cross-check.
	nodesTimeout = 5 * time.Second
)

// nodeLister is the read the handler needs to validate reported node IDs.
// port.SwarmController satisfies it.
type nodeLister interface {
	Nodes(ctx context.Context) ([]model.NodeView, error)
}

// Handler serves POST /v1/report. Mount it at ReportPath.
type Handler struct {
	sink   port.ReportSink
	token  string
	nodes  nodeLister
	logger *slog.Logger
}

// compile-time proof it is an http.Handler.
var _ http.Handler = (*Handler)(nil)

// New builds an ingest Handler. When token is empty the endpoint is
// unauthenticated (logged at WARN once here) — acceptable only on a trusted
// overlay network.
func New(sink port.ReportSink, token string, nodes nodeLister, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if strings.TrimSpace(token) == "" {
		logger.Warn("ingest endpoint is UNAUTHENTICATED (no INGEST_TOKEN set); restrict it to a trusted network")
	}
	return &Handler{sink: sink, token: token, nodes: nodes, logger: logger}
}

// ServeHTTP handles a single report submission.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		h.reject(w, r, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}
	if !h.authorized(r) {
		h.reject(w, r, http.StatusUnauthorized, "bad or missing token", nil)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	var report model.AgentReport
	if err := dec.Decode(&report); err != nil {
		// MaxBytesReader surfaces an oversized body here too.
		h.reject(w, r, http.StatusBadRequest, "invalid report body", err)
		return
	}
	if strings.TrimSpace(report.NodeID) == "" {
		h.reject(w, r, http.StatusBadRequest, "report has empty node id", nil)
		return
	}

	known, err := h.isKnownNode(r.Context(), report.NodeID)
	if err != nil {
		// Can't validate right now (transient Swarm API error) — ask the agent to
		// retry rather than accept an unvalidated report.
		h.reject(w, r, http.StatusServiceUnavailable, "node validation unavailable", err)
		return
	}
	if !known {
		h.reject(w, r, http.StatusForbidden, "reported node id is not a known swarm node", nil)
		return
	}

	if err := h.sink.Ingest(report, sourceOf(r)); err != nil {
		h.reject(w, r, http.StatusBadRequest, "report rejected", err)
		return
	}

	h.logger.Debug("ingest: report accepted",
		"node", report.NodeID, "name", report.NodeName, "tasks", len(report.Tasks), "source", sourceOf(r))
	w.WriteHeader(http.StatusNoContent)
}

// authorized reports whether the request carries the expected bearer token. When
// no token is configured, all requests are authorized.
func (h *Handler) authorized(r *http.Request) bool {
	if h.token == "" {
		return true
	}
	const prefix = "Bearer "
	got := r.Header.Get("Authorization")
	if !strings.HasPrefix(got, prefix) {
		return false
	}
	presented := strings.TrimPrefix(got, prefix)
	return subtle.ConstantTimeCompare([]byte(presented), []byte(h.token)) == 1
}

// isKnownNode reports whether nodeID is present in the live Swarm node list.
func (h *Handler) isKnownNode(ctx context.Context, nodeID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, nodesTimeout)
	defer cancel()

	nodes, err := h.nodes.Nodes(ctx)
	if err != nil {
		return false, err
	}
	for _, n := range nodes {
		if n.ID == nodeID {
			return true, nil
		}
	}
	return false, nil
}

// reject writes an error status and logs the reason at WARN (a request-level
// rejection is expected traffic, not a daemon fault).
func (h *Handler) reject(w http.ResponseWriter, r *http.Request, status int, reason string, err error) {
	h.logger.Warn("ingest: rejected report",
		"status", status, "reason", reason, "source", sourceOf(r), "err", err)
	http.Error(w, reason, status)
}

// sourceOf returns an opaque sender identity for duplicate detection.
func sourceOf(r *http.Request) string {
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "unknown"
}
