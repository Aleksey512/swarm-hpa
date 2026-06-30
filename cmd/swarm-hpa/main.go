// Command swarm-hpa is a Docker Swarm autoscaler and stuck-task healer daemon.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/wmid/swarm-hpa/internal/adapter/metrics"
	"github.com/wmid/swarm-hpa/internal/adapter/observability"
	swarmadapter "github.com/wmid/swarm-hpa/internal/adapter/swarm"
	"github.com/wmid/swarm-hpa/internal/app/reconciler"
	"github.com/wmid/swarm-hpa/internal/config"
	"github.com/wmid/swarm-hpa/internal/core/port"
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
	clock := port.SystemClock{}
	cooldown := reconciler.NewCooldown(cfg.Cooldown, clock)
	guard := reconciler.NewGuard(swarmCtl, cooldown, cfg.DryRun, logger)
	rec := reconciler.New(swarmCtl, metricsProvider, guard, clock, cfg.HealThreshold, logger)

	// Root context cancelled on SIGINT/SIGTERM for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := rec.Run(ctx, cfg.PollInterval); err != nil {
		logger.Error("reconcile loop failed", "err", err)
		return 1
	}

	logger.Info("shutdown complete")
	return 0
}
