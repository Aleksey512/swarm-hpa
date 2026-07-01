package main

import (
	"context"
	"log/slog"

	"github.com/docker/docker/client"

	"github.com/Aleksey512/swarm-hpa/internal/config"
)

// runAgent runs the per-node agent role: it collects local per-task and
// per-node CPU/memory load and pushes it to the manager's ingest endpoint.
//
// This is the T1 placeholder that establishes the mode branch and shutdown
// contract; the collector + reporter run loop is wired in a later step. For now
// it logs the effective agent configuration and idles until the context is
// cancelled so the container has a well-defined lifecycle.
func runAgent(ctx context.Context, cfg config.Config, cli *client.Client, logger *slog.Logger) int {
	logger.Info("agent mode starting",
		"manager_url", cfg.ManagerURL,
		"report_interval", cfg.ReportInterval,
		"node_id_override", cfg.NodeID,
	)
	logger.Warn("agent role is not fully wired yet; idling until shutdown")

	<-ctx.Done()

	logger.Info("shutdown complete")
	return 0
}
