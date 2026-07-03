// Package reporter is the agent-side adapter that pushes a model.AgentReport to
// the manager's ingest endpoint over HTTP. It authenticates with the shared
// INGEST_TOKEN bearer, bounds each request with a timeout, and retries transient
// failures with exponential backoff — never crashing the caller's loop, which
// simply logs and waits for the next report tick.
package reporter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

const (
	// reportPath is the manager route agents POST reports to.
	reportPath = "/v1/report"

	defaultTimeout  = 5 * time.Second
	defaultAttempts = 3
	defaultBackoff  = 500 * time.Millisecond
)

// Reporter posts reports to a single manager endpoint.
type Reporter struct {
	endpoint    string
	token       string
	client      *http.Client
	maxAttempts int
	backoff     time.Duration
	sleep       func(ctx context.Context, d time.Duration) error
	logger      *slog.Logger
}

// New builds a Reporter targeting managerURL (its base URL; reportPath is
// appended). token, when non-empty, is sent as a bearer credential.
func New(managerURL, token string, logger *slog.Logger) *Reporter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Reporter{
		endpoint:    strings.TrimRight(managerURL, "/") + reportPath,
		token:       token,
		client:      &http.Client{Timeout: defaultTimeout},
		maxAttempts: defaultAttempts,
		backoff:     defaultBackoff,
		sleep:       sleepCtx,
		logger:      logger,
	}
}

// sleepCtx sleeps for d unless ctx is cancelled first (then it returns ctx.Err).
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Report encodes and posts a single report, retrying transient failures
// (network errors and 5xx/429 responses) with exponential backoff. It returns
// nil on the first success, a context error if ctx is cancelled during backoff,
// or the last error once attempts are exhausted or a permanent (4xx) failure is
// seen. The caller logs the result and waits for the next tick.
func (r *Reporter) Report(ctx context.Context, report model.AgentReport) error {
	body, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("reporter: encode report: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < r.maxAttempts; attempt++ {
		if attempt > 0 {
			wait := r.backoff << (attempt - 1) // 500ms, 1s, 2s, ...
			r.logger.Debug("reporter: backing off before retry",
				"attempt", attempt, "wait", wait, "err", lastErr)
			if err := r.sleep(ctx, wait); err != nil {
				return err // context cancelled — stop retrying
			}
		}

		retryable, err := r.postOnce(ctx, body)
		if err == nil {
			r.logger.Debug("reporter: report accepted",
				"node", report.NodeID, "tasks", len(report.Tasks), "bytes", len(body))
			return nil
		}
		lastErr = err
		r.logger.Warn("reporter: report attempt failed",
			"node", report.NodeID, "attempt", attempt+1, "retryable", retryable, "err", err)
		if !retryable {
			return fmt.Errorf("reporter: permanent failure: %w", err)
		}
	}
	return fmt.Errorf("reporter: all %d attempts failed: %w", r.maxAttempts, lastErr)
}

// postOnce performs a single POST. It reports whether the failure (if any) is
// worth retrying: transport errors and 5xx/429 are retryable; other non-2xx
// responses (e.g. 401 bad token, 400 bad body) are permanent.
func (r *Reporter) postOnce(ctx context.Context, body []byte) (retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return true, fmt.Errorf("post %s: %w", r.endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body) // drain for connection reuse

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return true, fmt.Errorf("manager returned %s", resp.Status)
	default:
		return false, fmt.Errorf("manager returned %s", resp.Status)
	}
}
