package stackdeploy

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/adapter/dockererr"
)

// deployMaxAttempts bounds optimistic-concurrency retries of `docker stack deploy`.
// It mirrors the autoscaler's ServiceUpdate retry bound (adapter/swarm
// maxUpdateAttempts): a deploy fails with "update out of sequence" when the
// autoscaler/healer/rebalancer mutated a service between docker/cli's internal spec
// read and its ServiceUpdate. Re-running the deploy converges — it is idempotent and
// carry-forward clamps replicas to [min,max] — so a bounded retry closes the race in
// ~seconds instead of waiting for the next sync tick (GITOPS_INTERVAL, default 120s).
const deployMaxAttempts = 3

// WithRetry wraps a DeployFunc so a deploy that fails with a transient Swarm version
// conflict (dockererr.IsVersionConflict) is re-invoked up to deployMaxAttempts times,
// with a small backoff that honors ctx. Any other error fails fast so the real cause is
// surfaced rather than masked as a retryable blip. A nil logger falls back to slog.Default.
//
// This is the fast, intra-deploy retry; the GitOps loop's per-tick retry
// (lastDeployedOK) remains the outer safety net.
func WithRetry(deploy DeployFunc, logger *slog.Logger) DeployFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, name, composeFile, pullPolicy string) error {
		var lastErr error
		for attempt := 1; attempt <= deployMaxAttempts; attempt++ {
			err := deploy(ctx, name, composeFile, pullPolicy)
			if err == nil {
				if attempt > 1 {
					logger.Info("stackdeploy: deploy succeeded after retry",
						"stack", name, "attempts", attempt)
				}
				return nil
			}
			lastErr = err
			if !dockererr.IsVersionConflict(err) {
				return err // non-conflict: surface the real cause immediately
			}
			logger.Warn("stackdeploy: deploy version conflict, retrying",
				"stack", name, "attempt", attempt, "err", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 100 * time.Millisecond):
			}
		}
		return fmt.Errorf("stackdeploy: deploy %q: exhausted %d attempts: %w", name, deployMaxAttempts, lastErr)
	}
}
