# Implementation Plan: Self-Observability /metrics

Branch: main (git.enabled: true; git.create_branches: false — plan stays on the current branch)
Created: 2026-06-30

## Settings
- Testing: yes
- Logging: verbose
- Docs: yes  # mandatory documentation checkpoint at completion (route via /aif-docs)

## Roadmap Linkage
Milestone: "Self-observability /metrics"
Rationale: Milestone 8 — expose a Prometheus `/metrics` endpoint for the daemon's own behavior (reconcile ticks, managed services, scales/heals applied, suppressed actions, errors) so operators can see what the daemon observed, decided, and did.

## Scope & Non-Goals
**In scope:** a `Recorder` port in `core/port` (with a `NopRecorder` default), a `prometheus/client_golang`-backed recorder in `adapter/observability` exposing an `http.Handler`, a `/metrics` HTTP server in `main` on `--metrics-addr`, metric emission at the reconciler/guard decision/action points, and tests.

**Out of scope:** scale stabilization (milestone 9), packaging (milestone 11). No new dependency — `prometheus/client_golang` arrived with milestone 7. No tracing/exemplars; counters + gauges only. Per-service labels are used (the opt-in service set is bounded → low cardinality).

## Architecture Notes
- **Pure core, adapter does I/O.** The recording seam is a `core/port.Recorder` interface (plus a `NopRecorder` no-op, like `SystemClock`). The reconciler and guard depend only on this interface; `internal/core/*` never imports `prometheus/client_golang`. The concrete recorder lives in `adapter/observability` and is injected from `main`.
- **Self-metrics ≠ HPA metrics.** This is the daemon's *own* observability (metrics **out**), distinct from `adapter/metrics` `MetricsProvider` (HPA signals **in**). Named `Recorder` to avoid confusion with `MetricsProvider`.
- **Private registry.** The recorder registers on its own `*prometheus.Registry` (not the global default) — testable with `httptest`, and free of duplicate-registration panics.
- **Emission points** mirror the existing structured logs: reconcile tick, observed-services gauge, errors by stage (`services|tasks|nodes|metric|scale|heal`), and from the guard `ScaleApplied`/`HealApplied`, `ActionSuppressed{action,reason}` for dry-run/cooldown.
- **Best-effort endpoint.** The `/metrics` server runs in a goroutine and shuts down gracefully on context cancel; a bind/serve failure is logged but never crashes the reconcile loop (the daemon's core job is scaling/healing).
- **Default-safe.** A nil recorder resolves to `NopRecorder`, so tests and any future "metrics disabled" path work without branches at every call site.

## Commit Plan
<!-- 5 tasks; checkpoints below. git.enabled is true. -->
- **Commit 1** (after tasks 38-40): `feat: metrics recorder port + observability adapter + emission`
- **Commit 2** (after tasks 41-42): `feat: serve /metrics endpoint + tests`

## Tasks

### Phase 1: Port + Recorder adapter
- [x] Task 38: Define `core/port.Recorder` interface (`ReconcileTick`, `ObservedServices(n)`, `ScaleApplied(service)`, `HealApplied(service)`, `ActionSuppressed(action,reason)`, `Error(stage)`) + `NopRecorder` no-op. Pure. (independent)
- [x] Task 39: Implement `observability.Recorder` over `prometheus/client_golang` on a private registry — `swarm_hpa_{reconcile_total, managed_services, scales_total{service}, heals_total{service}, actions_suppressed_total{action,reason}, errors_total{stage}, build_info{version}}`; `NewRecorder(version)` + `Handler() http.Handler`. (depends on 38)

### Phase 2: Emission + wiring
- [x] Task 40: Inject `port.Recorder` into `reconciler.New` and `NewGuard` (nil → `NopRecorder`); emit at the decision/action points in `reconciler.observe` and the guard. Update existing test constructors. (depends on 38)
<!-- Commit checkpoint: 38-40 -->
- [ ] Task 41: Wire `main.go` — build the recorder, serve `/metrics` on `--metrics-addr` (goroutine + graceful shutdown; bind failure non-fatal), inject the recorder into the guard and reconciler. (depends on 39, 40)

### Phase 3: Tests
- [ ] Task 42: Tests — recorder counters/gauge + `Handler()` exposes expected metric names (`httptest`); `NopRecorder` safe; reconciler/guard emit the right events (fake recorder). (depends on 41)
<!-- Commit checkpoint: 41-42 -->

## Definition of Done
- `go build ./...`, `go vet ./...` pass; `make test` green.
- `GET <metrics-addr>/metrics` returns Prometheus text exposition including `swarm_hpa_build_info` and the reconcile/scale/heal/suppressed/error families; counters move when the corresponding action happens (verified via a fake recorder in the reconciler/guard and via the real handler in the adapter test).
- A bind/serve failure of the metrics server is logged and does not stop the reconcile loop.
- `internal/core/*` imports nothing from `internal/adapter` or `prometheus/client_golang` (purity holds via `go list -deps`); the app layer depends only on `port.Recorder`.
- Documentation checkpoint run at completion (Docs: yes).
