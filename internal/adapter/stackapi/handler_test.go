package stackapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeStore is a minimal port.StackStatusStore for handler tests.
type fakeStore struct{ items []model.StackStatus }

func (f *fakeStore) SetStatus(string, model.StackStatus) {}
func (f *fakeStore) SetState(string, string, string)     {}
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
	h := New(store, live, testLogger(), 0)

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
	h := New(store, live, testLogger(), 0)

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
	h := New(store, live, testLogger(), 0)

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
	h := New(store, live, testLogger(), 0)

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
	h := New(&fakeStore{}, &fakeLive{}, testLogger(), 0)
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

func TestHandler_StacksJSON_Files(t *testing.T) {
	store := &fakeStore{items: []model.StackStatus{{
		Name: "web",
		OK:   true,
		Files: []model.StackFileStatus{
			{File: "app.yaml", PullPolicy: "always", Status: "ok"},
			{File: "postgres.yaml", PullPolicy: "changed", Status: "failed", Error: "image pull denied"},
		},
	}}}
	h := New(store, nil, testLogger(), 0)

	rr := do(h, http.MethodGet, "/stacks")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got struct {
		Stacks []struct {
			Name  string `json:"name"`
			Files []struct {
				File       string `json:"file"`
				PullPolicy string `json:"pull_policy"`
				Status     string `json:"status"`
				Error      string `json:"error"`
			} `json:"files"`
		} `json:"stacks"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Stacks) != 1 || len(got.Stacks[0].Files) != 2 {
		t.Fatalf("got stacks/files = %+v", got.Stacks)
	}
	f0 := got.Stacks[0].Files[0]
	if f0.File != "app.yaml" || f0.PullPolicy != "always" || f0.Status != "ok" {
		t.Errorf("files[0] = %+v, want app.yaml/always/ok", f0)
	}
	f1 := got.Stacks[0].Files[1]
	if f1.File != "postgres.yaml" || f1.Status != "failed" || f1.Error != "image pull denied" {
		t.Errorf("files[1] = %+v, want postgres.yaml/failed/image pull denied", f1)
	}
}

func TestHandler_UI_Files(t *testing.T) {
	store := &fakeStore{items: []model.StackStatus{{
		Name:         "web",
		OK:           false,
		ErrorStage:   "deploy",
		ErrorMessage: "image pull denied",
		Files: []model.StackFileStatus{
			{File: "app.yaml", PullPolicy: "always", Status: "ok"},
			{File: "postgres.yaml", PullPolicy: "changed", Status: "failed", Error: "image pull denied"},
			{File: "monitor.yaml", Status: "skipped"},
		},
	}}}
	h := New(store, nil, testLogger(), 0)

	rr := do(h, http.MethodGet, "/")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"app.yaml", "postgres.yaml", "monitor.yaml", "always", "changed", "failed: image pull denied", "skipped"} {
		if !strings.Contains(body, want) {
			t.Errorf("HTML body missing %q; body=%q", want, body)
		}
	}
}

// A merge group's overrides ride along with its base file in the JSON, in `-c`
// order, and the key is absent for a plain single-file deploy.
func TestHandler_StacksJSON_Overrides(t *testing.T) {
	store := &fakeStore{items: []model.StackStatus{{
		Name: "monitoring",
		OK:   true,
		Files: []model.StackFileStatus{
			{File: "base.yaml", Overrides: []string{"prod.yaml", "env.override.yaml"}, PullPolicy: "always", Status: "ok"},
			{File: "traefik.yaml", PullPolicy: "changed", Status: "ok"},
		},
	}}}
	h := New(store, nil, testLogger(), 0)

	rr := do(h, http.MethodGet, "/stacks")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	raw := rr.Body.String()
	var got struct {
		Stacks []struct {
			Files []struct {
				File      string   `json:"file"`
				Overrides []string `json:"overrides"`
			} `json:"files"`
		} `json:"stacks"`
	}
	if err := json.NewDecoder(strings.NewReader(raw)).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Stacks) != 1 || len(got.Stacks[0].Files) != 2 {
		t.Fatalf("got stacks/files = %+v", got.Stacks)
	}
	want := []string{"prod.yaml", "env.override.yaml"}
	if !reflect.DeepEqual(got.Stacks[0].Files[0].Overrides, want) {
		t.Errorf("files[0].overrides = %v, want %v (in -c order)", got.Stacks[0].Files[0].Overrides, want)
	}
	if got.Stacks[0].Files[1].Overrides != nil {
		t.Errorf("files[1].overrides = %v, want absent for a plain single-file deploy", got.Stacks[0].Files[1].Overrides)
	}
	// omitempty must actually drop the key, not emit "overrides":null.
	if strings.Contains(raw, `"overrides":null`) {
		t.Errorf("JSON emits a null overrides key; want it omitted. body=%q", raw)
	}
}

// The UI lists a group's override files under its base file, HTML-escaped.
func TestHandler_UI_Overrides(t *testing.T) {
	store := &fakeStore{items: []model.StackStatus{{
		Name: "monitoring",
		OK:   true,
		Files: []model.StackFileStatus{
			{File: "base.yaml", Overrides: []string{"prod.yaml", "<script>.yaml"}, Status: "ok"},
		},
	}}}
	h := New(store, nil, testLogger(), 0)

	rr := do(h, http.MethodGet, "/")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "prod.yaml") {
		t.Errorf("HTML body missing the override file; body=%q", body)
	}
	if strings.Contains(body, "<script>.yaml") {
		t.Errorf("override path was not HTML-escaped; body=%q", body)
	}
	if !strings.Contains(body, "&lt;script&gt;.yaml") {
		t.Errorf("HTML body missing the escaped override path; body=%q", body)
	}
}

// --- repo + live state (v0.8.0 /stacks UI) ---

func TestHandler_StacksJSON_RepoAndState(t *testing.T) {
	store := &fakeStore{items: []model.StackStatus{
		{Name: "web", Repo: "myapp", State: "syncing", OK: true, Revision: "aaa"},
		{Name: "api", Repo: "myapp", State: "waiting", OK: true, Revision: "aaa"},
		{Name: "blog", Repo: "blogrepo", State: "", OK: true, Revision: "bbb"},
	}}
	h := New(store, nil, testLogger(), 4)

	rr := do(h, http.MethodGet, "/stacks")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got struct {
		Stacks []struct {
			Name  string `json:"name"`
			Repo  string `json:"repo"`
			State string `json:"state"`
		} `json:"stacks"`
		Summary struct {
			Stacks      int `json:"stacks"`
			Repos       int `json:"repos"`
			Syncing     int `json:"syncing"`
			Waiting     int `json:"waiting"`
			Concurrency int `json:"concurrency"`
			MaxParallel int `json:"max_parallel"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byName := map[string]struct{ repo, state string }{}
	for _, s := range got.Stacks {
		byName[s.Name] = struct{ repo, state string }{s.Repo, s.State}
	}
	if w := byName["web"]; w.repo != "myapp" || w.state != "syncing" {
		t.Errorf("web = repo %q state %q, want myapp/syncing", w.repo, w.state)
	}
	if w := byName["api"]; w.repo != "myapp" || w.state != "waiting" {
		t.Errorf("api = repo %q state %q, want myapp/waiting", w.repo, w.state)
	}
	if w := byName["blog"]; w.repo != "blogrepo" || w.state != "" {
		t.Errorf("blog = repo %q state %q, want blogrepo/empty", w.repo, w.state)
	}
	if got.Summary.Stacks != 3 || got.Summary.Repos != 2 || got.Summary.Syncing != 1 || got.Summary.Waiting != 1 {
		t.Errorf("summary = %+v, want stacks=3 repos=2 syncing=1 waiting=1", got.Summary)
	}
	if got.Summary.Concurrency != 4 || got.Summary.MaxParallel != 2 {
		t.Errorf("concurrency/maxparallel = %d/%d, want 4/2 (min(4,2 repos))", got.Summary.Concurrency, got.Summary.MaxParallel)
	}
}

// Concurrency 0 (GitOps off) must omit the cap from the summary and the payload.
func TestHandler_SummaryOmitsConcurrencyWhenZero(t *testing.T) {
	store := &fakeStore{items: []model.StackStatus{{Name: "s", Repo: "r", OK: true}}}
	h := New(store, nil, testLogger(), 0)

	rr := do(h, http.MethodGet, "/stacks")
	raw := rr.Body.String()
	if strings.Contains(raw, "max_parallel") {
		t.Errorf("max_parallel must be omitted when concurrency=0; body=%q", raw)
	}
	if strings.Contains(raw, "concurrency") {
		t.Errorf("concurrency must be omitted when 0; body=%q", raw)
	}
}

func TestHandler_UI_RepoStateAndSummary(t *testing.T) {
	store := &fakeStore{items: []model.StackStatus{
		{Name: "web", Repo: "myapp", State: "syncing", OK: true, Revision: "aaa"},
		{Name: "api", Repo: "myapp", State: "waiting", OK: true, Revision: "aaa"},
		{Name: "blog", Repo: "blogrepo", State: "", OK: false, ErrorStage: "git", ErrorMessage: "down"},
	}}
	h := New(store, nil, testLogger(), 4)

	rr := do(h, http.MethodGet, "/")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	// Repo column values.
	for _, want := range []string{"myapp", "blogrepo"} {
		if !strings.Contains(body, want) {
			t.Errorf("HTML body missing repo %q; body=%q", want, body)
		}
	}
	// State badges.
	if !strings.Contains(body, "● syncing") {
		t.Errorf("body missing syncing badge; body=%q", body)
	}
	if !strings.Contains(body, "⏸ waiting") {
		t.Errorf("body missing waiting badge; body=%q", body)
	}
	// Summary header counts + concurrency cap line. Counts render inside spans.
	if !strings.Contains(body, `class="syncing">1</span>`) || !strings.Contains(body, `class="waiting">1</span>`) {
		t.Errorf("summary missing live counts; body=%q", body)
	}
	if !strings.Contains(body, "concurrency: 4") || !strings.Contains(body, "≤2 parallel") {
		t.Errorf("summary missing concurrency cap; body=%q", body)
	}
	// A stack with empty State still renders the legacy ok/err status.
	if !strings.Contains(body, "git: down") {
		t.Errorf("idle stack must still render its error status; body=%q", body)
	}
}
