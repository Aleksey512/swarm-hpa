// Command swarm-hpa is a Docker Swarm autoscaler and stuck-task healer daemon.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/adapter/metrics"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/observability"
	swarmadapter "github.com/Aleksey512/swarm-hpa/internal/adapter/swarm"
	"github.com/Aleksey512/swarm-hpa/internal/app/reconciler"
	"github.com/Aleksey512/swarm-hpa/internal/config"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	// Bootstrap a logger from the environment so configuration parsing and any
	// resulting errors are visible before the full config is resolved.
	observability.Setup(observability.Options{
		Level:  os.Getenv("LOG_LEVEL"),
		Format: observability.Format(os.Getenv("LOG_FORMAT")),
	})

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", "err", err)
		return 1
	}

	// Re-install the logger with the final, validated settings.
	logger := observability.Setup(observability.Options{
		Level:  cfg.LogLevel,
		Format: observability.Format(cfg.LogFormat),
	})

	logger.Info("starting swarm-hpa",
		"version", version,
		"dry_run", cfg.DryRun,
		"metrics_provider", cfg.MetricsProvider,
	)
	if cfg.DryRun {
		logger.Info("dry-run is enabled: no Swarm mutations will be applied")
	}

	// Build the Docker client and the read-only swarm adapter (composition root).
	cli, err := swarmadapter.NewClient()
	if err != nil {
		logger.Error("failed to create docker client", "err", err)
		return 1
	}
	defer cli.Close()

	swarmCtl := swarmadapter.New(cli, logger)
	metricsProvider, err := metrics.New(cfg, cli, logger)
	if err != nil {
		logger.Error("failed to build metrics provider", "err", err)
		return 1
	}
	recorder := observability.NewRecorder(version, logger)
	clock := port.SystemClock{}
	cooldown := reconciler.NewCooldown(clock)
	cooldowns := reconciler.Cooldowns{ScaleUp: cfg.ScaleUpCooldown, ScaleDown: cfg.ScaleDownCooldown, Heal: cfg.Cooldown}
	guard := reconciler.NewGuard(swarmCtl, cooldown, cooldowns, cfg.DryRun, recorder, logger)
	stabilizer := reconciler.NewStabilizer(cfg.ScaleDownStabilizationWindow)
	rec := reconciler.New(swarmCtl, metricsProvider, guard, clock, cfg.HealThreshold, recorder, stabilizer, cfg.MaxScaleStep, logger)

	// Serve the daemon's own /metrics endpoint. Best-effort: a serve failure is
	// logged but never stops the reconcile loop (the daemon's core job is
	// scaling/healing).
	mux := http.NewServeMux()
	mux.Handle("/metrics", recorder.Handler())
	metricsSrv := &http.Server{Addr: cfg.MetricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		logger.Info("metrics endpoint listening", "addr", cfg.MetricsAddr, "path", "/metrics")
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics server failed", "err", err)
		}
	}()

	// Root context cancelled on SIGINT/SIGTERM for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := rec.Run(ctx, cfg.PollInterval); err != nil {
		logger.Error("reconcile loop failed", "err", err)
		return 1
	}

	// Stop the metrics server with a bounded grace period.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("metrics server shutdown failed", "err", err)
	}

	logger.Info("shutdown complete")
	return 0
}
