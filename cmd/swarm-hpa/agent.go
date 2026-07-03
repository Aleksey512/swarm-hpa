package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/docker/docker/client"

	"github.com/Aleksey512/swarm-hpa/internal/adapter/agent/collector"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/agent/reporter"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/observability"
	"github.com/Aleksey512/swarm-hpa/internal/app/agentloop"
	"github.com/Aleksey512/swarm-hpa/internal/config"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// runAgent runs the per-node agent role: it collects local per-task and
// per-node CPU/memory load and pushes it to the manager's ingest endpoint on
// each report interval. It also serves a minimal /healthz and an agent-scoped
// /metrics on MetricsAddr. It returns a process exit code and stops gracefully
// when ctx is cancelled.
func runAgent(ctx context.Context, cfg config.Config, cli *client.Client, logger *slog.Logger) int {
	coll := collector.New(cli, cfg.NodeID, port.SystemClock{}, logger)
	rep := reporter.New(cfg.ManagerURL, cfg.IngestToken, logger)
	arec := observability.NewAgentRecorder(version, logger)
	loop := agentloop.New(coll, rep, arec, port.SystemClock{}, logger)

	// Minimal self-observability server: /metrics + /healthz. Best-effort — a
	// serve failure is logged but never stops the report loop.
	mux := http.NewServeMux()
	mux.Handle("/metrics", arec.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	srv := &http.Server{Addr: cfg.MetricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		logger.Info("agent metrics endpoint listening", "addr", srv.Addr, "path", "/metrics")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("agent metrics server failed", "err", err)
		}
	}()

	logger.Info("agent mode starting",
		"manager_url", cfg.ManagerURL,
		"report_interval", cfg.ReportInterval,
		"node_id_override", cfg.NodeID,
	)

	runErr := loop.Run(ctx, cfg.ReportInterval)

	// Stop the metrics server with a bounded grace period.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("agent metrics server shutdown failed", "err", err)
	}

	if runErr != nil {
		logger.Error("agent report loop failed", "err", runErr)
		return 1
	}
	logger.Info("shutdown complete")
	return 0
}
