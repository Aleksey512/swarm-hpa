// Command swarm-hpa is a Docker Swarm autoscaler and stuck-task healer daemon.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/docker/docker/client"

	"github.com/Aleksey512/swarm-hpa/internal/adapter/ingest"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/metrics"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/observability"
	swarmadapter "github.com/Aleksey512/swarm-hpa/internal/adapter/swarm"
	"github.com/Aleksey512/swarm-hpa/internal/app/registry"
	"github.com/Aleksey512/swarm-hpa/internal/config"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	// --version / -v: print the build banner and exit before any config or
	// Docker-client work, so it works without a socket (e.g. in a container).
	if wantsVersion(os.Args[1:]) {
		fmt.Println(versionString())
		return 0
	}

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
		"mode", cfg.Mode,
		"dry_run", cfg.DryRun,
		"metrics_provider", cfg.MetricsProvider,
	)
	if cfg.DryRun {
		logger.Info("dry-run is enabled: no Swarm mutations will be applied")
	}

	// Build the Docker client shared by both roles: the manager talks to the
	// Swarm API (manager-only), the agent reads local task/node stats.
	cli, err := swarmadapter.NewClient()
	if err != nil {
		logger.Error("failed to create docker client", "err", err)
		return 1
	}
	defer func() { _ = cli.Close() }()

	// Root context cancelled on SIGINT/SIGTERM for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch cfg.Mode {
	case config.ModeAgent:
		return runAgent(ctx, cfg, cli, logger)
	default:
		return runManager(ctx, cfg, cli, logger)
	}
}

// runManager wires and runs the manager role: the reconcile loop plus its
// /metrics server, and the agent-report ingest endpoint that feeds the agent
// registry. buildApp does no I/O, so the only failure here is a programming
// error (a nil required dep).
func runManager(ctx context.Context, cfg config.Config, cli *client.Client, logger *slog.Logger) int {
	swarmCtl := swarmadapter.New(cli, logger)
	recorder := observability.NewRecorder(version, logger)
	reg := registry.New(cfg.AgentStaleTimeout, port.SystemClock{}, nil, logger)

	metricsProvider, err := metrics.New(cfg, cli, logger)
	if err != nil {
		logger.Error("failed to build metrics provider", "err", err)
		return 1
	}

	application, err := buildApp(cfg, appDeps{
		swarm:          swarmCtl,
		metrics:        metricsProvider,
		clock:          port.SystemClock{},
		recorder:       recorder,
		metricsHandler: recorder.Handler(),
		logger:         logger,
	})
	if err != nil {
		logger.Error("failed to build application", "err", err)
		return 1
	}

	// Start the agent-report ingest server on its own address so it can be
	// scoped to the internal overlay network rather than exposed for scraping.
	ingestSrv := newIngestServer(cfg, reg, swarmCtl, logger)
	go func() {
		logger.Info("ingest endpoint listening", "addr", ingestSrv.Addr, "path", ingest.ReportPath)
		if err := ingestSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("ingest server failed", "err", err)
		}
	}()

	rc := application.run(ctx)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ingestSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("ingest server shutdown failed", "err", err)
	}
	return rc
}

// newIngestServer builds the HTTP server that receives agent reports.
func newIngestServer(cfg config.Config, reg *registry.Registry, swarmCtl *swarmadapter.Adapter, logger *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.Handle(ingest.ReportPath, ingest.New(reg, cfg.IngestToken, swarmCtl, logger))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return &http.Server{Addr: cfg.IngestAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
}
