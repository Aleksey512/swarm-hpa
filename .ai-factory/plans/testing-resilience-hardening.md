# Implementation Plan: Testing & Resilience Hardening

Branch: main (git.enabled: true; git.create_branches: false — plan stays on the current branch)
Created: 2026-07-01

## Settings
- Testing: yes
- Logging: verbose
- Docs: yes  # mandatory documentation checkpoint at completion (route via /aif-docs)

## Roadmap Linkage
Milestone: "Testing & resilience hardening"
Rationale: Milestone 10 — closes the remaining testing gaps (table-driven decision tests, goroutine-leak checks, an integration test harness) and proves the already-implemented transient Docker/Prometheus error tolerance, plus a CI gate to keep it green.

## Current State (why this is gap-closing, not greenfield)
The codebase is **already heavily tested**: ~88 `Test*` funcs across 23 files, comprehensive port fakes (`fakeController`, `fakeProvider`, `seqProvider`, `fakeClock`, `fakeRecorder`, `fakeDockerAPI`, ...), table-driven decision tests for `autoscaler.Desired`/`ClampStep` and `healer.nodeSatisfies`, an injectable `port.Clock`, and "log-and-continue" transient-error handling wired through every branch of `reconciler.observe` (`services`/`nodes`/`tasks`/`metric` stages each log + `recorder.Error(stage)` + skip, never abort). Pure stdlib — **zero third-party test deps** today.

So this plan targets the concrete gaps the milestone still names:
- **goroutine-leak checks** — not present (no `goleak`, no `TestMain` leak guard).
- **integration test harness** — not present; the one non-injectable seam is `time.NewTicker` in `Run()` (reconciler.go:67), so today only `observe()` can be exercised, not `Run()`'s full lifecycle. There is no build-tagged integration test.
- **transient-error tolerance** — implemented but lacks a dedicated burst-error → recovery test.
- **table-driven decision tests** — a few pure functions are only exercised transitively (`healer.parseConstraint`, `healer.longestPending`, `model.IsPending`/`IsActive`, `autoscaler.clamp`, swarm mappers) and the `Guard` direction-aware cooldown path.
- **CI** — none exists; add a GitHub Actions gate (`make test-race`/`lint`/`cover` + integration).

## Scope & Non-Goals
**In scope:** two small, behavior-preserving production seams to make the loop testable (injectable tick source; a `cmd` composition split); goroutine-leak checks via `go.uber.org/goleak`; race tests for the mutex-guarded `Cooldown`/`Stabilizer`; direct table-driven tests for the untested pure functions and the `Guard` cooldown; a transient-error resilience suite; a `//go:build integration` end-to-end harness; and a GitHub Actions CI workflow with a `make test-integration` target.

**Out of scope / non-goals:** Dockerfile, deployment stack, versioning, and README/ops docs (that is Milestone 11 — "Packaging & deployment"). No new runtime features. No change to scaling/healing behavior. Adding `goleak` is the **only** new dependency, and it is test-scope — `internal/core` purity is untouched. The two production changes (Tasks 49–50) must be fully behavior-preserving on the default/real path (same logs, same exit codes, tick source defaults to `time.NewTicker`).

## Architecture Notes
- **Testability changes obey the existing layering.** The tick-source option lives in `app/reconciler`; the composition split lives only in `cmd/swarm-hpa` (the composition root). `internal/core/*` is not touched — purity holds via `go list -deps`.
- **Injectable ticks, not injectable clock-for-ticks.** `Clock` already drives cooldown/stabilization/heal timing; the missing seam is the *tick trigger*. A functional option supplies the tick channel (default `time.NewTicker(interval)`), so an integration test can step `Run()` synchronously instead of sleeping.
- **`cmd` seam = build vs run.** Split `run()` into `buildApp(cfg, deps)` (wire ports) + `app.run(ctx)` (own the metrics-server goroutine + loop + graceful shutdown). The real path keeps building deps from the Docker client exactly as today; tests inject fakes and get a real lifecycle with no Docker socket.
- **goleak sits at the goroutine boundaries.** Leak guards go in `internal/app/reconciler` (loop lifecycle) and `cmd/swarm-hpa` (metrics HTTP server + loop). These are the only packages that start goroutines (main.go:86 metrics server; `Run` on the main goroutine; `signal.NotifyContext` stdlib-managed).
- **Resilience is asserted, not assumed.** The resilience suite scripts fakes to emit consecutive errors per stage and asserts logged-and-swallowed + `recorder.Error(stage)` counts + recovery on the next clean tick — locking in the current guarantee against regressions.

## Commit Plan
<!-- 9 tasks; checkpoints below. git.enabled is true; git.create_branches is false (commits land on the current branch). -->
- **Commit 1** (after tasks 49-50): `refactor: injectable tick source + testable cmd composition seam (behavior-preserving)`
- **Commit 2** (after tasks 51-52): `test: goroutine-leak checks (goleak) + race tests for cooldown & stabilizer`
- **Commit 3** (after tasks 53-55): `test: table-driven coverage for healer/model/autoscaler/swarm + guard cooldown`
- **Commit 4** (after tasks 56-57): `test: transient-error resilience + integration harness; ci: github actions gate`

## Tasks

### Phase 1: Harness enablers (minimal, behavior-preserving production changes)
- [x] Task 49: Make `Reconciler.Run`'s tick source injectable via a functional option (`WithTickSource`, default `time.NewTicker(interval)`); thread `...Option` through `New`. Enables deterministic tick-driven `Run()` tests. `internal/app/reconciler/reconciler.go` (+ `options.go`). Verbose: keep lifecycle INFO logs; DEBUG note only when a custom source is injected. (independent)
- [x] Task 50: Split `cmd/swarm-hpa` `run()` into `buildApp(cfg, deps) (*app, error)` + `(*app) run(ctx) int` so the daemon can be built with injected fakes and driven start→tick→shutdown without a Docker socket. Real path + all logs + exit codes unchanged. `cmd/swarm-hpa/main.go` (+ `app.go`). (independent)
<!-- Commit checkpoint: 49-50 -->

### Phase 2: goroutine-leak & race coverage
- [x] Task 51: Add `go.uber.org/goleak`; add `TestMain` (`goleak.VerifyTestMain`) leak guards to `internal/app/reconciler` and `cmd/swarm-hpa`; narrow `IgnoreTopFunction` only where a documented stdlib goroutine trips it. `internal/app/reconciler/main_test.go`, `cmd/swarm-hpa/main_test.go`, `go.mod`, `go.sum`. (depends on 49, 50)
- [x] Task 52: Race/concurrency tests for `Cooldown` (`Allowed`/`Record`) and `Stabilizer` (`Recommend`) — N goroutines over multiple service IDs against `fakeClock`, clean under `make test-race`. `internal/app/reconciler/cooldown_race_test.go`, `stabilizer_race_test.go`. (independent)
<!-- Commit checkpoint: 51-52 -->

### Phase 3: Fill pure decision-logic table-test gaps
- [x] Task 53: Direct table-driven tests for `healer.parseConstraint` (spacing / operator order / empty sides / no-operator), `healer.longestPending` (empty / all-running / at-threshold boundary / first-wins), and `nodeSatisfies`+`nodeValue` (unknown key skipped, missing-label semantics). `internal/core/healer/healer_parse_test.go`. (independent)
- [x] Task 54: Table-driven tests for `model.TaskView.IsPending` & `model.NodeView.IsActive` (state/availability matrices incl. empty strings), `autoscaler.clamp` edge cases (`v==lo==hi`, `lo>hi`), and swarm mappers `toManagedService`/`toTaskView`/`toNodeView` (global/non-replicated, nil `Placement`, empty constraints, task/node availability states). `internal/core/model/{task,node}_test.go`, extend `autoscaler_test.go`, `internal/adapter/swarm/map_test.go`. (independent)
- [x] Task 55: Table-driven test for `Guard.Scale` direction-aware cooldown — up/down window suppression, cross-direction independence, no-op on `desired==current`, heal window independent; assert `ActionSuppressed` emission via `fakeRecorder`. `internal/app/reconciler/guard_cooldown_test.go`. (independent)
<!-- Commit checkpoint: 53-55 -->

### Phase 4: Resilience harness + CI
- [ ] Task 56: Transient-error resilience suite (burst errors per stage services/nodes/tasks/metric → logged-and-swallowed + `recorder.Error(stage)` counts → recovery on next clean tick) plus a `//go:build integration` end-to-end harness wiring the daemon via the cmd seam with fakes + injected tick source: start→ticks→cancel→clean exit, no leaked goroutines. `internal/app/reconciler/resilience_test.go`, `cmd/swarm-hpa/integration_test.go`. (depends on 49, 50, 51)
- [ ] Task 57: GitHub Actions CI (`.github/workflows/ci.yml`, push + PR): gofmt check, `make vet`, `make lint`, `make test-race`, `make cover`, and `go test -tags integration ./...`; add a `make test-integration` target. `.github/workflows/ci.yml`, `Makefile`. (depends on 56)
<!-- Commit checkpoint: 56-57 -->

## Definition of Done
- `go build ./...`, `go vet ./...`, and `make lint` pass; `make test` and `make test-race` are green; `make test-integration` is green.
- `goleak` reports **no leaked goroutines** after `Run()` returns on context cancel and after the metrics-server `Shutdown`.
- New direct table-driven tests cover the previously-transitive pure functions (`parseConstraint`, `longestPending`, `IsPending`, `IsActive`, `clamp`, swarm mappers) and the `Guard` direction-aware cooldown, including boundary cases.
- A burst of consecutive Docker/Prometheus errors on any stage is proven to be logged-and-swallowed (matching `recorder.Error(stage)` counts) with the loop resuming normal scale/heal decisions on the next clean tick.
- **Production behavior unchanged** on the default/real path: tick source defaults to `time.NewTicker(interval)`; the `cmd` split preserves every existing startup/shutdown log and exit code.
- `internal/core/*` imports nothing from `internal/adapter`/`internal/app` or the Docker/Prometheus clients (purity holds via `go list -deps`); `goleak` is a test-scope dependency only.
- CI runs fmt/vet/lint/test-race/cover + integration on push and PR.
- Documentation checkpoint run at completion (Docs: yes) — document `make test-integration`, the `integration` build tag, the `goleak` guards, and the CI workflow.
