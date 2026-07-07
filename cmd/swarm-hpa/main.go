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

	"github.com/Aleksey512/swarm-hpa/internal/adapter/git"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/ingest"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/metrics"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/observability"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/sops"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/stackapi"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/stackdeploy"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/stackrender"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/statusstore"
	swarmadapter "github.com/Aleksey512/swarm-hpa/internal/adapter/swarm"
	"github.com/Aleksey512/swarm-hpa/internal/app/gitopsync"
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
	// recorder also satisfies registry.Recorder (agent-fleet metrics).
	reg := registry.New(cfg.AgentStaleTimeout, port.SystemClock{}, recorder, logger)

	metricsProvider, err := metrics.New(cfg, cli, reg, logger)
	if err != nil {
		logger.Error("failed to build metrics provider", "err", err)
		return 1
	}

	// GitOps status surface (per-stack status API + drift UI) rides on the metrics
	// server alongside /metrics. Built only when gitops is enabled; a nil stackAPI
	// leaves the metrics mux with just /metrics. The same statusStore is fed to the
	// gitops loop below (the writer) and the stackapi handler (the reader).
	var statusStore port.StackStatusStore
	var stackAPI http.Handler
	if cfg.GitOpsEnabled {
		statusStore = statusstore.New(logger)
		stackAPI = stackapi.New(statusStore, swarmCtl, logger)
	}

	application, err := buildApp(cfg, appDeps{
		swarm:          swarmCtl,
		metrics:        metricsProvider,
		clock:          port.SystemClock{},
		recorder:       recorder,
		metricsHandler: recorder.Handler(),
		stackAPI:       stackAPI,
		loads:          reg,
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

	// Optional GitOps stack-sync loop (replaces swarm-cd). Runs alongside the
	// reconcile loop on the same ctx; deploys are autoscaler-aware (carry-forward
	// preserves replicas of swarm.autoscaler.* services) and dry-run-gated.
	if cfg.GitOpsEnabled {
		repos, stacks, err := config.LoadGitOps(cfg.GitOpsConfigsPath)
		if err != nil {
			logger.Error("failed to load gitops stacks", "err", err)
			return 1
		}
		if len(stacks) == 0 {
			logger.Warn("gitops enabled but no stacks defined in stacks.yaml")
		}
		dockerCli, err := stackdeploy.NewDockerCli(logger)
		if err != nil {
			logger.Error("failed to initialize docker cli for gitops", "err", err)
			return 1
		}
		// Wrap the deploy in a bounded retry: a `docker stack deploy` fails with
		// "update out of sequence" when the autoscaler/healer mutates a service
		// mid-deploy; re-running converges (idempotent, carry-forward clamps
		// replicas). The loop's per-tick retry remains the outer safety net.
		deployer := stackdeploy.New(swarmCtl, stackdeploy.WithRetry(stackdeploy.DockerCLIDeploy(dockerCli), logger), logger)
		gitLoop := gitopsync.New(
			git.New(cfg.GitOpsReposPath, repos, logger),
			stackrender.New(logger),
			deployer,
			sops.New(logger),
			recorder, statusStore, stacks,
			cfg.GitOpsPullPolicy, cfg.DryRun, cfg.GitOpsAutoRotate, cfg.GitOpsConcurrency, logger,
		)
		sopsStacks := 0
		for _, s := range stacks {
			if s.SopsSecretsDiscovery || len(s.SopsFiles) > 0 {
				sopsStacks++
			}
		}
		logger.Info("gitops enabled",
			"stacks", len(stacks), "interval", cfg.GitOpsInterval,
			"repos_path", cfg.GitOpsReposPath, "pull_policy", cfg.GitOpsPullPolicy,
			"dry_run", cfg.DryRun, "auto_rotate", cfg.GitOpsAutoRotate,
			"concurrency", cfg.GitOpsConcurrency, "sops_stacks", sopsStacks,
			"status_api", cfg.MetricsAddr+"{/stacks JSON, / UI}")
		go func() {
			if err := gitLoop.Run(ctx, cfg.GitOpsInterval); err != nil {
				logger.Error("gitops loop failed", "err", err)
			}
		}()
	}

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
