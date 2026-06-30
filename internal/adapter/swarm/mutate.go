package swarm

import (
	"context"
	"fmt"
	"time"

	dswarm "github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/errdefs"
)

// maxUpdateAttempts bounds optimistic-concurrency retries on ServiceUpdate.
const maxUpdateAttempts = 3

// Scale sets a replicated service's desired replica count via a version-indexed
// ServiceUpdate. Errors if the service is not replicated.
func (a *Adapter) Scale(ctx context.Context, serviceID string, replicas uint64) error {
	return a.updateService(ctx, serviceID, "scale", func(spec *dswarm.ServiceSpec) error {
		if spec.Mode.Replicated == nil {
			return fmt.Errorf("service %s is not replicated; refusing to scale", serviceID)
		}
		spec.Mode.Replicated.Replicas = &replicas
		return nil
	})
}

// ForceUpdate bumps the service's ForceUpdate counter — the SDK equivalent of
// `docker service update --force` — to reschedule its tasks with no spec change.
func (a *Adapter) ForceUpdate(ctx context.Context, serviceID string) error {
	return a.updateService(ctx, serviceID, "force-update", func(spec *dswarm.ServiceSpec) error {
		spec.TaskTemplate.ForceUpdate++
		return nil
	})
}

// updateService re-inspects the service for a fresh Version.Index, applies
// mutate to the inspected spec, and ServiceUpdates, retrying on optimistic-
// concurrency conflicts. mutate must be idempotent (it runs again on each retry).
func (a *Adapter) updateService(
	ctx context.Context,
	serviceID, action string,
	mutate func(*dswarm.ServiceSpec) error,
) error {
	var lastErr error
	for attempt := 1; attempt <= maxUpdateAttempts; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)

		svc, _, err := a.cli.ServiceInspectWithRaw(callCtx, serviceID, dswarm.ServiceInspectOptions{})
		if err != nil {
			cancel()
			return fmt.Errorf("%s: inspect %s: %w", action, serviceID, err)
		}
		a.logger.Debug("re-inspected service for mutation",
			"action", action, "service", serviceID, "version", svc.Version.Index)

		spec := svc.Spec
		if err := mutate(&spec); err != nil {
			cancel()
			return err
		}

		resp, err := a.cli.ServiceUpdate(callCtx, serviceID, svc.Version, spec, dswarm.ServiceUpdateOptions{})
		cancel()
		if err == nil {
			for _, w := range resp.Warnings {
				a.logger.Warn("service update warning", "action", action, "service", serviceID, "warning", w)
			}
			a.logger.Info("service mutated", "action", action, "service", serviceID)
			return nil
		}

		lastErr = err
		if !errdefs.IsConflict(err) {
			return fmt.Errorf("%s %s: %w", action, serviceID, err)
		}
		a.logger.Warn("service version conflict, retrying",
			"action", action, "service", serviceID, "attempt", attempt)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * 100 * time.Millisecond):
		}
	}
	return fmt.Errorf("%s %s: exhausted %d attempts: %w", action, serviceID, maxUpdateAttempts, lastErr)
}
