package stackapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeStore is a minimal port.StackStatusStore for handler tests.
type fakeStore struct{ items []model.StackStatus }

func (f *fakeStore) SetStatus(string, model.StackStatus) {}
func (f *fakeStore) Snapshot() []model.StackStatus       { return f.items }

// fakeLive is a minimal port.StackStateReader.
type fakeLive struct {
	services map[string][]model.StackService
	err      error
}

func (f *fakeLive) StackServices(_ context.Context, stack string) ([]model.StackService, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.services[stack], nil
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestHandler_StacksJSON_Drift(t *testing.T) {
	store := &fakeStore{items: []model.StackStatus{{
		Name:            "web",
		Revision:        "abc12345",
		OK:              true,
		LastSync:        time.Unix(1751500000, 0),
		DeployCount:     3,
		DesiredReplicas: map[string]uint64{"worker": 2},
	}}}
	live := &fakeLive{services: map[string][]model.StackService{
		"web": {{Name: "worker", Replicas: 5, Replicated: true}}, // drifted: desired 2, live 5
	}}
	h := New(store, live, testLogger())

	rr := do(h, http.MethodGet, "/stacks")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got struct {
		Stacks []struct {
			Name    string `json:"name"`
			OK      bool   `json:"ok"`
			Drifted bool   `json:"drifted"`
			Drift   []struct {
				Service string `json:"service"`
				Desired uint64 `json:"desired"`
				Live    uint64 `json:"live"`
				Drifted bool   `json:"drifted"`
			} `json:"drift"`
		} `json:"stacks"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Stacks) != 1 || got.Stacks[0].Name != "web" {
		t.Fatalf("got stacks = %+v", got.Stacks)
	}
	s := got.Stacks[0]
	if !s.OK {
		t.Error("OK=false, want true")
	}
	if !s.Drifted {
		t.Error("Drifted=false, want true (worker 2→5)")
	}
	if len(s.Drift) != 1 || s.Drift[0].Service != "worker" || !s.Drift[0].Drifted {
		t.Errorf("drift = %+v, want worker drifted", s.Drift)
	}
	if s.Drift[0].Desired != 2 || s.Drift[0].Live != 5 {
		t.Errorf("drift values = desired %d live %d, want 2/5", s.Drift[0].Desired, s.Drift[0].Live)
	}
}

func TestHandler_StacksJSON_LiveErrorDegrades(t *testing.T) {
	store := &fakeStore{items: []model.StackStatus{{
		Name: "web", OK: true, DesiredReplicas: map[string]uint64{"worker": 2},
	}}}
	live := &fakeLive{err: errors.New("swarm unreachable")}
	h := New(store, live, testLogger())

	rr := do(h, http.MethodGet, "/stacks")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (drift error must not 5xx the payload)", rr.Code)
	}
	var got struct {
		Stacks []struct {
			DriftError string `json:"drift_error"`
			Drifted    bool   `json:"drifted"`
		} `json:"stacks"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Stacks[0].DriftError == "" {
		t.Error("expected drift_error set on live failure")
	}
	if got.Stacks[0].Drifted {
		t.Error("Drifted should be false when live failed")
	}
}

func TestHandler_NoDesiredSkipsDrift(t *testing.T) {
	// DesiredReplicas empty (no render yet) → drift skipped, no Swarm call.
	store := &fakeStore{items: []model.StackStatus{{Name: "web", OK: true}}}
	calls := 0
	live := &liveCounter{fn: func(string) ([]model.StackService, error) { calls++; return nil, nil }}
	h := New(store, live, testLogger())

	rr := do(h, http.MethodGet, "/stacks")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if calls != 0 {
		t.Errorf("live was called %d times, want 0 (no desired → skip drift)", calls)
	}
}

func TestHandler_UI(t *testing.T) {
	store := &fakeStore{items: []model.StackStatus{{Name: "web-prod", Revision: "abc", OK: true, DesiredReplicas: map[string]uint64{"worker": 2}}}}
	live := &fakeLive{services: map[string][]model.StackService{"web-prod": {{Name: "worker", Replicas: 2, Replicated: true}}}}
	h := New(store, live, testLogger())

	rr := do(h, http.MethodGet, "/")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(rr.Body.String(), "web-prod") {
		t.Errorf("HTML body missing stack name; body=%q", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "in sync") {
		t.Errorf("expected 'in sync' for a non-drifted stack; body=%q", rr.Body.String())
	}
	// /ui alias too
	if do(h, http.MethodGet, "/ui").Code != http.StatusOK {
		t.Error("/ui should also serve the UI")
	}
}

func TestHandler_Routing(t *testing.T) {
	h := New(&fakeStore{}, &fakeLive{}, testLogger())
	if rr := do(h, http.MethodGet, "/nope"); rr.Code != http.StatusNotFound {
		t.Errorf("unknown path = %d, want 404", rr.Code)
	}
	if rr := do(h, http.MethodPost, "/stacks"); rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rr.Code)
	}
}

// liveCounter wraps a func to assert whether StackServices was called.
type liveCounter struct {
	fn func(stack string) ([]model.StackService, error)
	n  int
}

func (l *liveCounter) StackServices(_ context.Context, stack string) ([]model.StackService, error) {
	l.n++
	return l.fn(stack)
}
