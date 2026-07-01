package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeSink struct {
	mu      sync.Mutex
	got     []model.AgentReport
	sources []string
	err     error
}

func (f *fakeSink) Ingest(r model.AgentReport, source string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, r)
	f.sources = append(f.sources, source)
	return nil
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

type fakeNodes struct {
	nodes []model.NodeView
	err   error
}

func (f fakeNodes) Nodes(context.Context) ([]model.NodeView, error) { return f.nodes, f.err }

func knownNodes() fakeNodes {
	return fakeNodes{nodes: []model.NodeView{{ID: "node-a", Name: "worker-1"}}}
}

func reportJSON(nodeID string) []byte {
	b, _ := json.Marshal(model.AgentReport{NodeID: nodeID, NodeName: "worker-1"})
	return b
}

func do(h *Handler, method, token string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, ReportPath, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIngestAcceptsValidReport(t *testing.T) {
	sink := &fakeSink{}
	h := New(sink, "s3cr3t", knownNodes(), discardLogger())

	rec := do(h, http.MethodPost, "s3cr3t", reportJSON("node-a"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if sink.count() != 1 {
		t.Fatalf("sink got %d reports, want 1", sink.count())
	}
	if !strings.Contains(sink.sources[0], "192.0.2.1") { // httptest default RemoteAddr
		t.Errorf("source = %q, want the request RemoteAddr", sink.sources[0])
	}
}

func TestIngestRejectsBadToken(t *testing.T) {
	sink := &fakeSink{}
	h := New(sink, "right", knownNodes(), discardLogger())

	if rec := do(h, http.MethodPost, "wrong", reportJSON("node-a")); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad token status = %d, want 401", rec.Code)
	}
	if rec := do(h, http.MethodPost, "", reportJSON("node-a")); rec.Code != http.StatusUnauthorized {
		t.Errorf("missing token status = %d, want 401", rec.Code)
	}
	if sink.count() != 0 {
		t.Errorf("unauthorized reports must not reach the sink, got %d", sink.count())
	}
}

func TestIngestNoTokenAcceptsUnauthenticated(t *testing.T) {
	sink := &fakeSink{}
	h := New(sink, "", knownNodes(), discardLogger())

	if rec := do(h, http.MethodPost, "", reportJSON("node-a")); rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204 when no token is configured", rec.Code)
	}
}

func TestIngestRejectsWrongMethod(t *testing.T) {
	h := New(&fakeSink{}, "", knownNodes(), discardLogger())
	rec := do(h, http.MethodGet, "", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	if rec.Header().Get("Allow") != http.MethodPost {
		t.Errorf("Allow = %q, want POST", rec.Header().Get("Allow"))
	}
}

func TestIngestRejectsBadBodyAndEmptyNode(t *testing.T) {
	h := New(&fakeSink{}, "", knownNodes(), discardLogger())

	if rec := do(h, http.MethodPost, "", []byte("{not json")); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json status = %d, want 400", rec.Code)
	}
	if rec := do(h, http.MethodPost, "", reportJSON("")); rec.Code != http.StatusBadRequest {
		t.Errorf("empty node status = %d, want 400", rec.Code)
	}
}

func TestIngestRejectsUnknownNode(t *testing.T) {
	sink := &fakeSink{}
	h := New(sink, "", knownNodes(), discardLogger())

	rec := do(h, http.MethodPost, "", reportJSON("ghost-node"))
	if rec.Code != http.StatusForbidden {
		t.Errorf("unknown node status = %d, want 403", rec.Code)
	}
	if sink.count() != 0 {
		t.Errorf("unknown-node report must not reach the sink, got %d", sink.count())
	}
}

func TestIngestUnavailableWhenNodeLookupFails(t *testing.T) {
	h := New(&fakeSink{}, "", fakeNodes{err: errors.New("swarm api down")}, discardLogger())
	rec := do(h, http.MethodPost, "", reportJSON("node-a"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when validation is unavailable", rec.Code)
	}
}

func TestIngestRejectsOversizedBody(t *testing.T) {
	h := New(&fakeSink{}, "", knownNodes(), discardLogger())
	huge := append([]byte(`{"NodeID":"node-a","NodeName":"`), bytes.Repeat([]byte("a"), 2<<20)...)
	huge = append(huge, []byte(`"}`)...)
	rec := do(h, http.MethodPost, "", huge)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("oversized body status = %d, want 400", rec.Code)
	}
}

func TestIngestSinkErrorIsBadRequest(t *testing.T) {
	h := New(&fakeSink{err: errors.New("malformed")}, "", knownNodes(), discardLogger())
	rec := do(h, http.MethodPost, "", reportJSON("node-a"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when sink rejects", rec.Code)
	}
}
