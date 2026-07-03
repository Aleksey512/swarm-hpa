# Implementation Plan: Docker Stats Metrics Provider

Branch: none (git not initialized; `git.enabled: false`)
Created: 2026-06-30

## Settings
- Testing: yes
- Logging: verbose
- Docs: no  # warn-only; documentation deferred to a later milestone

## Roadmap Linkage
Milestone: "Docker stats metrics provider"
Rationale: Milestone 4 — give the daemon a baseline `MetricsProvider` (per-task CPU/memory from the Docker stats API) that milestone 5's autoscaler will turn into scaling decisions.

## Scope & Non-Goals
**In scope:** the `MetricsProvider` port + metric constants, CPU%/memory% math from Docker container stats, a `dockerstats` provider implementing it, a provider factory (selecting by `metrics_provider` config), and surfacing the current metric value per managed service in the observe loop (read-only).

**Out of scope:** scaling math / `Guard.Scale` decisions (milestone 5), the Prometheus provider (milestone 7 — the factory returns a clear "not implemented" error for it now), and the `/metrics` endpoint (milestone 8).

## Architecture Notes
- **Node-local limitation (important):** Docker's `ContainerStats` is served by the local daemon, so in a multi-node Swarm the manager cannot read stats for containers on other nodes. The `dockerstats` provider therefore covers only tasks whose containers run on the daemon's node and returns `model.ErrNoMetricData` when a service has no locally-readable stats. **Cross-node coverage is the Prometheus provider (milestone 7).** This is exactly why the project supports pluggable providers.
- `MetricsProvider` is a new `core/port` interface; the core stays Docker-free. The `dockerstats` adapter imports the Docker SDK (allowed under `internal/adapter`).
- CPU% needs a delta (current vs previous sample) → read 2 stream frames; memory% needs one sample. The math lives in pure, unit-tested helpers.
- The reconciler consumes the provider through the `port.MetricsProvider` interface (injected from `main`); on `ErrNoMetricData` it logs and continues.

## Commit Plan
<!-- 5 tasks; checkpoints below. Applies once git is initialized (git.enabled is currently false). -->
- **Commit 1** (after tasks 20-22): `feat: MetricsProvider port + dockerstats provider (cpu/mem)`
- **Commit 2** (after tasks 23-24): `feat: provider factory + observe metric value; tests`

## Tasks

### Phase 1: Port, Math, Adapter
- [x] Task 20: `MetricsProvider` port (`Value(ctx, svc) (float64, error)`) + metric constants + `ErrNoMetricData` (core, pure).
- [x] Task 21: Pure CPU%/memory% helpers from `container.StatsResponse` (delta formula; ok=false on bad input). (independent)
- [x] Task 22: `dockerstats` provider — list tasks, read node-local container stats (2 frames for CPU), aggregate per service, skip remote/unavailable, `ErrNoMetricData` when none. (depends on 20, 21)
<!-- Commit checkpoint: tasks 20-22 -->

### Phase 2: Factory, Wiring, Tests
- [x] Task 23: Provider factory (dockerstats now; prometheus → "not implemented (milestone 7)") + wire into reconciler observe (log metric value vs target, read-only) + main. (depends on 20, 22)
- [x] Task 24: Tests — CPU/mem math edge cases, dockerstats aggregation/skip/no-data with a fake stats API, factory selection, observe handles value + ErrNoMetricData. (depends on 22, 23)
<!-- Commit checkpoint: tasks 23-24 -->

## Definition of Done
- `go build ./...`, `go vet ./...` pass; `make test` green (math edge cases, aggregation, factory, observe).
- `cpuPercent`/`memPercent` produce the standard Docker percentages and return ok=false on zero/negative deltas or zero limit.
- The `dockerstats` provider averages the metric across tasks with readable stats, skips remote/unavailable containers, and returns `model.ErrNoMetricData` when none are readable.
- The factory returns a working provider for `metrics_provider=dockerstats` and a clear error for `prometheus` (milestone 7).
- The observe loop logs each managed service's current metric value vs its target, and tolerates `ErrNoMetricData` without stopping.
- `internal/core/*` still imports nothing from `internal/adapter` or the Docker SDK (purity holds via `go list -deps`).
