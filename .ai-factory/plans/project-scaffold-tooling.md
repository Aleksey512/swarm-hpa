# Implementation Plan: Project Scaffold & Tooling

Branch: none (git not initialized; `git.enabled: false`)
Created: 2026-06-30

## Settings
- Testing: yes
- Logging: verbose
- Docs: yes  # mandatory docs checkpoint in /aif-implement

## Roadmap Linkage
Milestone: "Project scaffold & tooling"
Rationale: This is the foundational milestone — it establishes the module, the ports-and-adapters layout, tooling, logging, config, and a graceful-shutdown run loop that every later milestone builds on.

## Scope & Non-Goals
**In scope:** Go module, directory skeleton (per ARCHITECTURE.md), build/lint tooling, structured logging (slog), config loading (flags+env, dry-run default true), daemon entrypoint with a no-op reconcile ticker and graceful shutdown, and tests for config + shutdown.

**Out of scope (later milestones):** Docker SDK adapter, MetricsProvider implementations, real autoscaling/healing decision logic, the `/metrics` endpoint. Packages for those are created as empty (`doc.go`) placeholders only.

## Architecture Notes
- Dependency rule (from ARCHITECTURE.md): `internal/core/*` must not import `internal/adapter/*`, Docker SDK, or any Prometheus client. This milestone only touches `cmd`, `internal/config`, `internal/observability`, and creates empty core/adapter packages.
- Safety: `dry_run` config defaults to **true**. No mutating Swarm code exists yet.

## Commit Plan
<!-- 6 tasks; checkpoints below. Applies once git is initialized — git.enabled is currently false. -->
- **Commit 1** (after tasks 1-2): `chore: scaffold module, layout, and build tooling`
- **Commit 2** (after tasks 3-4): `feat: add structured logging and config loading`
- **Commit 3** (after tasks 5-6): `feat: daemon entrypoint with graceful shutdown + tests`

## Tasks

### Phase 1: Module & Tooling
- [x] Task 1: Init Go module and repo layout — `go.mod`, `.gitignore`, and the `cmd/` + `internal/{core,app,adapter,config}` tree with `doc.go` placeholders so `go build ./...` succeeds.
- [x] Task 2: Add build tooling — `Makefile` (build/run/test/test-race/lint/fmt/vet/tidy/cover/clean) and `.golangci.yml`. (depends on 1)
<!-- Commit checkpoint: tasks 1-2 -->

### Phase 2: Runtime Foundations
- [x] Task 3: Set up structured logging (slog) — `internal/observability/logging.go`, level from `LOG_LEVEL`, single place that configures slog. (depends on 1)
- [x] Task 4: Implement config loading — `internal/config/config.go` with flag > env > default precedence, `dry_run` default **true**, `Validate()`; logs effective config at INFO. (depends on 1, 3)
<!-- Commit checkpoint: tasks 3-4 -->

### Phase 3: Entrypoint & Tests
- [x] Task 5: Build daemon entrypoint with graceful shutdown — `cmd/swarm-hpa/main.go`: `signal.NotifyContext`, placeholder ticker reconcile loop selecting on `ctx.Done()`, clean exit. (depends on 3, 4)
- [x] Task 6: Add tests for config and shutdown loop — table tests for config precedence/validation; loop-exits-on-cancel test (extract loop into a testable func), optional `goleak`. (depends on 4, 5)
<!-- Commit checkpoint: tasks 5-6 -->

## Definition of Done
- `go build ./...` and `go vet ./...` pass; `make lint` is clean.
- `make test` passes, including config precedence and shutdown-loop-exit tests.
- Running the binary logs effective config (with `dry_run=true`), ticks the no-op reconcile loop, and exits cleanly on SIGINT/SIGTERM.
- No `internal/core/*` package imports an adapter, the Docker SDK, or a Prometheus client.
