# Implementation Plan: Prometheus Metrics Provider

Branch: main (git.enabled: true; git.create_branches: false — plan stays on the current branch)
Created: 2026-06-30

## Settings
- Testing: yes
- Logging: verbose
- Docs: yes  # mandatory documentation checkpoint at completion (route via /aif-docs)

## Roadmap Linkage
Milestone: "Prometheus metrics provider"
Rationale: Milestone 7 — add a PromQL-backed `MetricsProvider` (RPS, p95 latency, queue depth, custom app metrics) and let each service pick its metric source via labels, the analogue of K8s custom/external metrics.

## Scope & Non-Goals
**In scope:** a `prometheus` adapter implementing `port.MetricsProvider` via the official `client_golang` API (instant PromQL queries); per-service metric source selection (`swarm.autoscaler.source`) and per-service PromQL query (`swarm.autoscaler.query`) through a `Router` provider; a `PrometheusTimeout` config knob; tests with a fake query API; a docs checkpoint.

**Out of scope:** self-observability `/metrics` endpoint (milestone 8), scale stabilization (milestone 9). Range queries and alerting are not added — only instant queries reduced to a single scalar. Query result must reduce to a scalar or a single-series vector; multi-series results are an operator misconfiguration (descriptive error, service skipped).

## Architecture Notes
- The provider is an **adapter** (`internal/adapter/metrics/prometheus`): it depends inward on `core/port` + `core/model` and never leaks the Prometheus client into the core. A narrow `queryAPI` interface (satisfied by `v1.API`) keeps `Value` unit-testable without a live server — the same narrowing pattern as `dockerAPI` / `statsAPI`.
- **Provider routing stays in the adapter layer.** A `Router` (`adapter/metrics`) composes the two concrete providers and dispatches per service by `Policy.Source`, falling back to the global `cfg.MetricsProvider`. The core port (`MetricsProvider.Value(ctx, svc)`) is unchanged; `main.go` needs no change because `Router` *is* a `MetricsProvider`.
- **Policy parsing stays pure & provider-agnostic.** `config.ParsePolicy` learns two optional labels (`query`, `source`) but enforces no provider-specific requirement (e.g. "query required") — that belongs to the provider, which errors clearly at query time. `source` is validated to `dockerstats|prometheus` only when explicitly set.
- **Error semantics match dockerstats:** an unavailable/empty/NaN result returns `model.ErrNoMetricData` (the reconciler skips the service for this tick); HTTP, parse, multi-series, and empty-query problems return descriptive wrapped errors (logged, service skipped). The loop never crashes on a provider error.
- **Config:** `PrometheusURL` already exists and is required when the *global* provider is `prometheus`. With per-service routing, the prometheus provider is also built whenever `PrometheusURL` is set; a `source=prometheus` service with no configured URL yields a descriptive error, never a silent wrong scale.

## Commit Plan
<!-- 5 tasks; checkpoints below. git.enabled is true, so these apply. -->
- **Commit 1** (after tasks 33-35): `feat: prometheus metrics provider (PromQL) + per-service query/source labels`
- **Commit 2** (after tasks 36-37): `feat: per-service metrics routing + tests`

## Tasks

### Phase 1: Model, Labels, Config
- [x] Task 33: Add `Query` + `Source` to `ServicePolicy`; add `swarm.autoscaler.query` / `swarm.autoscaler.source` labels; parse in `ParsePolicy` (query verbatim/optional; source validated to `dockerstats|prometheus` when present). Update `labels_test.go`. (independent)
- [x] Task 34: Add `PrometheusTimeout` to config (flag `--prometheus-timeout` / env `PROMETHEUS_TIMEOUT` / default 10s, validate `> 0`, `LogValue`). Update `config_test.go`. (independent)
<!-- Commit checkpoint: 33-35 -->

### Phase 2: Provider
- [x] Task 35: Implement `prometheus.Provider` over `client_golang` `api` + `api/prometheus/v1` (narrow `queryAPI` iface; `New(url, timeout, logger)`; instant query from `Policy.Query` with `$SERVICE`/`$SERVICE_ID` substitution; scalar / single-vector → float; empty/NaN → `ErrNoMetricData`; multi-series / empty-query / HTTP error → wrapped error). Add the `prometheus/client_golang` dependency. (depends on 33, 34)

### Phase 3: Routing & Wiring
- [x] Task 36: Add `Router` provider dispatching by `Policy.Source` with global fallback; rewire `metrics.New` (dockerstats always + prometheus when URL set + Router). Replace the not-implemented branch; update `factory_test.go`. (depends on 35)
<!-- Commit checkpoint: 36-37 -->

### Phase 4: Tests
- [x] Task 37: Tests — provider via fake `queryAPI` (scalar / single vector / empty→`ErrNoMetricData` / NaN→`ErrNoMetricData` / multi-series→error / empty-query→error; optional `httptest` smoke test) + router (dispatch / default fallback / prometheus-unconfigured error) + labels (query/source) + config timeout. (depends on 36)

## Definition of Done
- `go build ./...`, `go vet ./...` pass; `make test` green.
- `METRICS_PROVIDER=prometheus` selects the provider daemon-wide; a service with `swarm.autoscaler.source=prometheus` + `swarm.autoscaler.query=<PromQL>` is scaled from the query's scalar value while a `source=dockerstats` (or unset, on a dockerstats default) service keeps using container stats — in the same daemon.
- A query returning no data / NaN skips the service (`ErrNoMetricData`); multi-series, empty-query, HTTP failure, or `source=prometheus` without a configured URL are logged errors that skip the service without crashing the loop.
- `internal/core/*` imports nothing from `internal/adapter` or the Prometheus client (purity holds via `go list -deps`).
- Documentation checkpoint run at completion (Docs: yes).
