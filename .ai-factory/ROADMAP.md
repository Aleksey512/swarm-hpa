# Project Roadmap

> A Go daemon for Docker Swarm that adds horizontal autoscaling (HPA) for opt-in services and auto-heals tasks stuck in `pending` under placement constraints after node recovery — opt-in, dry-run by default, fully logged.

## Milestones

- [x] **Project scaffold & tooling** — `go.mod`, `cmd/`+`internal/{core,app,adapter,config}` layout, Makefile, golangci-lint, `slog` setup, flag/env config parsing, graceful-shutdown skeleton
- [x] **Swarm read layer** — Docker SDK adapter: list/inspect services and tasks, parse `swarm.autoscaler.*` labels into `core/model`; read-only, no mutations
- [x] **Reconcile loop + dry-run safety** — `app/reconciler`: periodic loop with the single guarded mutation path (dry-run + opt-in labels + cooldown) and structured decision logging; mutations still suppressed
- [x] **Docker stats metrics provider** — `adapter/metrics/dockerstats` implementing `port.MetricsProvider` (per-task CPU/memory baseline, no external deps)
- [x] **Autoscaler (HPA) core + apply** — `core/autoscaler` decision logic (desired replicas, clamp to min/max) wired into the reconciler; real `Scale` via `SwarmController` when enabled — HPA loop end-to-end on Docker stats
- [x] **Stuck-task healer** — `core/healer` detection (5-point pending signature) + force-update via `SwarmController` with cooldown; recovers the moby#42215 stall automatically
- [x] **Prometheus metrics provider** — `adapter/metrics/prometheus` (PromQL signals), provider selection per service via labels/config (closest to K8s custom/external metrics)
- [x] **Self-observability `/metrics`** — `prometheus/client_golang` endpoint exposing the daemon's decisions, scales applied, tasks healed, and errors; finalize structured slog fields
- [x] **Scale stabilization** — separate scale-up/scale-down cooldowns, step limits, and stabilization windows to prevent flapping
- [ ] **Testing & resilience hardening** — table-driven tests for decision logic, fakes for ports, transient Docker/Prometheus error tolerance, goroutine-leak checks, integration test harness
- [ ] **Packaging & deployment** — Dockerfile, least-privilege run/stack example, README/docs, build-time version embedding

## Completed

| Milestone | Date |
|-----------|------|
| Project scaffold & tooling | 2026-06-30 |
| Swarm read layer | 2026-06-30 |
| Reconcile loop + dry-run safety | 2026-06-30 |
| Docker stats metrics provider | 2026-06-30 |
| Autoscaler (HPA) core + apply | 2026-06-30 |
| Stuck-task healer | 2026-06-30 |
| Prometheus metrics provider | 2026-06-30 |
| Self-observability /metrics | 2026-06-30 |
| Scale stabilization | 2026-06-30 |
