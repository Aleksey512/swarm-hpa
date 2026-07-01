package reporter

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func sampleReport() model.AgentReport {
	return model.AgentReport{
		NodeID:   "node-a",
		NodeName: "worker-1",
		Node:     model.NodeLoad{CPUPercent: 20, TaskCount: 1},
		Tasks:    []model.TaskMetric{{TaskID: "t1", ServiceID: "svc", CPUPercent: 40}},
	}
}

// instantSleep makes backoff waits return immediately (still honoring cancel).
func instantSleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func TestReportSuccess(t *testing.T) {
	var gotAuth, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := New(srv.URL, "s3cr3t", discardLogger())
	if err := r.Report(context.Background(), sampleReport()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer s3cr3t" {
		t.Errorf("Authorization = %q, want Bearer s3cr3t", gotAuth)
	}
	if gotPath != reportPath {
		t.Errorf("path = %q, want %q", gotPath, reportPath)
	}
	var decoded model.AgentReport
	if err := json.Unmarshal([]byte(gotBody), &decoded); err != nil || decoded.NodeID != "node-a" {
		t.Errorf("body did not round-trip: err=%v node=%q", err, decoded.NodeID)
	}
}

func TestReportNoTokenOmitsHeader(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	r := New(srv.URL, "", discardLogger())
	if err := r.Report(context.Background(), sampleReport()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hadAuth {
		t.Error("no Authorization header should be sent when token is empty")
	}
}

func TestReportPermanentErrorNoRetry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized) // 401 = permanent
	}))
	defer srv.Close()

	r := New(srv.URL, "bad", discardLogger())
	r.sleep = instantSleep
	if err := r.Report(context.Background(), sampleReport()); err == nil {
		t.Fatal("want an error for a 401 response")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("permanent failure must not retry: got %d calls, want 1", got)
	}
}

func TestReportRetriesTransientThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable) // 503 = retryable
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := New(srv.URL, "", discardLogger())
	r.sleep = instantSleep // no real backoff delay
	if err := r.Report(context.Background(), sampleReport()); err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("want 3 attempts (2 x 503 then 200), got %d", got)
	}
}

func TestReportStopsOnContextCancel(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable) // always retryable
	}))
	defer srv.Close()

	r := New(srv.URL, "", discardLogger())
	// A backoff that reports cancellation stops the retry loop after the first try.
	r.sleep = func(context.Context, time.Duration) error { return context.Canceled }
	if err := r.Report(context.Background(), sampleReport()); err == nil {
		t.Fatal("want an error when backoff is cancelled")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("only the first attempt should run before cancel, got %d", got)
	}
}

func TestReportEncodeToNowhereIsPermanent(t *testing.T) {
	// A closed server makes Do fail; with instant sleep it retries maxAttempts
	// times and then returns a wrapped error (not a panic / not a hang).
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // now unreachable → transport error (retryable, exhausts attempts)

	r := New(url, "", discardLogger())
	r.sleep = instantSleep
	if err := r.Report(context.Background(), sampleReport()); err == nil {
		t.Fatal("want an error when the manager is unreachable")
	}
}
