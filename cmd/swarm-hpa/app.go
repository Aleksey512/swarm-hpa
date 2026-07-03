package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/app/reconciler"
	"github.com/Aleksey512/swarm-hpa/internal/config"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// appDeps carries the composition-root dependencies buildApp wires into the
// daemon. The real path fills these from the Docker client (see run); tests
// inject fakes so the whole daemon lifecycle can be exercised without a Docker
// socket or a live Prometheus.
type appDeps struct {
	swarm          port.SwarmController
	metrics        port.MetricsProvider
	clock          port.Clock
	recorder       port.Recorder
	metricsHandler http.Handler          // served at /metrics (recorder.Handler() on the real path)
	loads          reconciler.LoadSource // agent registry; nil disables rebalancing
	logger         *slog.Logger
	reconcilerOpts []reconciler.Option // e.g. reconciler.WithTickSource for deterministic tests
}

// app is the fully wired daemon: the reconcile loop plus its /metrics server.
// It owns nothing external — run(ctx) drives the lifecycle and returns a process
// exit code.
type app struct {
	rec        *reconciler.Reconciler
	metricsSrv *http.Server
	interval   time.Duration
	logger     *slog.Logger
}

// buildApp wires deps + cfg into a runnable daemon. It performs no I/O and never
// touches the network, so it is safe to call from tests with fakes. Required
// ports (swarm, metrics, recorder, metricsHandler) must be non-nil; nil logger
// and clock fall back to sane defaults.
func buildApp(cfg config.Config, deps appDeps) (*app, error) {
	if deps.swarm == nil || deps.metrics == nil || deps.recorder == nil || deps.metricsHandler == nil {
		return nil, errors.New("buildApp: swarm, metrics, recorder, and metricsHandler are required")
	}
	logger := deps.logger
	if logger == nil {
		logger = slog.Default()
	}
	clock := deps.clock
	if clock == nil {
		clock = port.SystemClock{}
	}

	cooldown := reconciler.NewCooldown(clock)
	cooldowns := reconciler.Cooldowns{
		ScaleUp:   cfg.ScaleUpCooldown,
		ScaleDown: cfg.ScaleDownCooldown,
		Heal:      cfg.Cooldown,
		Rebalance: cfg.RebalanceCooldown,
	}
	guard := reconciler.NewGuard(deps.swarm, cooldown, cooldowns, cfg.DryRun, deps.recorder, logger)
	stabilizer := reconciler.NewStabilizer(cfg.ScaleDownStabilizationWindow)

	opts := deps.reconcilerOpts
	if deps.loads != nil {
		opts = append(opts, reconciler.WithRebalancing(deps.loads, cfg.RebalanceThreshold))
	}
	rec := reconciler.New(deps.swarm, deps.metrics, guard, clock, cfg.HealThreshold, deps.recorder, stabilizer, cfg.MaxScaleStep, logger, opts...)

	mux := http.NewServeMux()
	mux.Handle("/metrics", deps.metricsHandler)
	metricsSrv := &http.Server{Addr: cfg.MetricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	return &app{
		rec:        rec,
		metricsSrv: metricsSrv,
		interval:   cfg.PollInterval,
		logger:     logger,
	}, nil
}

// run drives the daemon lifecycle: it starts the best-effort /metrics server,
// runs the reconcile loop until ctx is cancelled (a graceful stop), then shuts
// the metrics server down with a bounded grace period. It returns a process exit
// code (0 = clean stop, 1 = loop failure). Cancelling ctx is the only way to
// stop it, which is exactly what a test needs.
func (a *app) run(ctx context.Context) int {
	// Serve the daemon's own /metrics endpoint. Best-effort: a serve failure is
	// logged but never stops the reconcile loop (the daemon's core job is
	// scaling/healing).
	go func() {
		a.logger.Info("metrics endpoint listening", "addr", a.metricsSrv.Addr, "path", "/metrics")
		if err := a.metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.logger.Error("metrics server failed", "err", err)
		}
	}()

	if err := a.rec.Run(ctx, a.interval); err != nil {
		a.logger.Error("reconcile loop failed", "err", err)
		return 1
	}

	// Stop the metrics server with a bounded grace period.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.metricsSrv.Shutdown(shutdownCtx); err != nil {
		a.logger.Error("metrics server shutdown failed", "err", err)
	}

	a.logger.Info("shutdown complete")
	return 0
}
