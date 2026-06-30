# Implementation Plan: Swarm Read Layer (Docker SDK adapter)

Branch: none (git not initialized; `git.enabled: false`)
Created: 2026-06-30

## Settings
- Testing: yes
- Logging: verbose
- Docs: no  # warn-only; documentation deferred to a later milestone

## Roadmap Linkage
Milestone: "Swarm read layer"
Rationale: Milestone 2 — give the daemon a read-only view of Swarm (managed services, their tasks/nodes, parsed opt-in policy) that every later decision milestone (autoscaler, healer) consumes.

## Scope & Non-Goals
**In scope:** the first external dependency (Docker Go SDK), the core read model + ports, `swarm.autoscaler.*` label parsing, a read-only `SwarmController` adapter (list/inspect services, tasks, nodes), and wiring the reconciler to observe-and-log each tick.

**Out of scope (later milestones):** any mutation (`ServiceUpdate`, scale, force-update), the `MetricsProvider` port and providers (milestone 4), scaling math (milestone 5), and healer force-update (milestone 6). Stuck-pending tasks are only *logged* as candidates here, never acted on.

## Architecture Notes
- `internal/core/model` and `internal/core/port` stay pure (stdlib + model only); the Docker SDK is imported **only** under `internal/adapter/swarm`.
- Label parsing lives in `internal/config` (per ARCHITECTURE.md), producing `model.ServicePolicy`.
- The reconciler depends on the `port.SwarmController` interface; `main` injects the concrete adapter. The loop must tolerate transient API errors (log + continue), never crash.
- Use the `docker-swarm-go-sdk` skill for the SDK surface; classic `github.com/docker/docker` module (`NewClientWithOpts`, `types.ServiceListOptions`).

## Commit Plan
<!-- 7 tasks; checkpoints below. Applies once git is initialized (git.enabled is currently false). -->
- **Commit 1** (after tasks 7-9): `feat: add Docker SDK dep, core read model and ports`
- **Commit 2** (after tasks 10-11): `feat: label parsing + read-only swarm adapter`
- **Commit 3** (after tasks 12-13): `feat: reconciler observes swarm + tests`

## Tasks

### Phase 1: Dependency, Model & Ports
- [x] Task 7: Add Docker Go SDK dependency — `go get github.com/docker/docker/client@v27`, `go mod tidy`; build stays green.
- [x] Task 8: Define core domain model types — `ServiceRef`, `ServicePolicy`, `ManagedService`, `TaskView`, `NodeView` in `internal/core/model` (pure).
- [x] Task 9: Define core ports — `SwarmController` (read methods) + `Clock` in `internal/core/port`. (depends on 8)
<!-- Commit checkpoint: tasks 7-9 -->

### Phase 2: Parsing & Adapter
- [x] Task 10: Implement `swarm.autoscaler.*` label parsing — `config.ParsePolicy` → `model.ServicePolicy`, pure + validated. (depends on 8)
- [x] Task 11: Implement read-only Docker SDK adapter — `internal/adapter/swarm` implements `port.SwarmController` (services/tasks/nodes), pure mapping helpers, context timeouts, error wrapping. (depends on 7, 8, 9, 10)
<!-- Commit checkpoint: tasks 10-11 -->

### Phase 3: Wiring & Tests
- [x] Task 12: Wire read layer into reconciler observe step — per-tick observe + structured logging, stuck-pending candidates logged only; `main` builds client + injects adapter. (depends on 9, 11)
- [x] Task 13: Add tests — label parsing table tests, SDK→model mapping tests with fixtures, reconciler observe test with a fake `SwarmController`. (depends on 10, 11, 12)
<!-- Commit checkpoint: tasks 12-13 -->

## Definition of Done
- `go build ./...`, `go vet ./...` pass; `make test` green (label parsing, mapping, observe).
- The daemon, pointed at a Swarm manager, logs the managed services it observed, their tasks, and any stuck-pending candidates — and applies **no** mutations (dry-run irrelevant here: this layer is read-only).
- `internal/core/*` still imports nothing from `internal/adapter` or the Docker SDK (purity invariant holds via `go list -deps`).
- A transient Docker API error logs ERROR and the loop continues to the next tick.
