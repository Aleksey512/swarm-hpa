# Implementation Plan: Stuck-Task Healer

Branch: none (git not initialized; `git.enabled: false`)
Created: 2026-06-30

## Settings
- Testing: yes
- Logging: verbose
- Docs: no  # warn-only; documentation deferred to a later milestone

## Roadmap Linkage
Milestone: "Stuck-task healer"
Rationale: Milestone 6 — replace the coarse pending+constraints heuristic with the precise 5-point stuck-pending signature (moby/moby#42215) so the daemon only force-updates services whose constrained node has genuinely recovered.

## Scope & Non-Goals
**In scope:** the precise stuck-task detection in `core/healer` (constraint parsing + node-active resolution + pending-duration threshold), node labels in `NodeView`, a `heal_threshold` config knob, and wiring it into observe → `Guard.Heal`. The healing *mechanism* (Guard.Heal, adapter ForceUpdate, cooldown) already exists from milestone 3 — this milestone makes the *decision* precise.

**Out of scope:** Prometheus (milestone 7), `/metrics` (8). Constraint parsing covers simple equality forms (`==`/`!=` on labels/hostname/id); complex expressions degrade conservatively.

## Architecture Notes
- Detection is **pure** (`core/healer`): given a service, its tasks, the cluster nodes, a threshold, and `now`, it returns a stuck verdict + reason. No I/O, fully table-testable.
- The Guard remains the sole mutation chokepoint; force-update stays dry-run-suppressed by default and per-service cooldown-gated.
- **Conservative bias:** a false positive force-restarts a healthy prod service, so the signature requires ALL of: placement constraints present, a task pending+desired-running beyond the threshold, AND a constraint-satisfying node that is now Active+Ready. Unparseable constraints never *exclude* a node (we don't silently widen exclusion), but the Active-node requirement keeps the bar high.

## Commit Plan
<!-- 5 tasks; checkpoints below. Applies once git is initialized (git.enabled is currently false). -->
- **Commit 1** (after tasks 28-30): `feat: node labels, stuck-task detection, heal threshold config`
- **Commit 2** (after tasks 31-32): `feat: wire precise healer into reconcile loop + tests`

## Tasks

### Phase 1: Data, Detection, Config
- [x] Task 28: Add `Labels` to `NodeView` + adapter mapping (node spec labels). (independent)
- [x] Task 29: `core/healer.Detect` — constraint parsing + long-pending + constraint-satisfying-node-Active → stuck verdict; pure. (depends on 28)
- [x] Task 30: Add `HealThreshold` to config (flag/env/default 2m, validate). (independent)
<!-- Commit checkpoint: tasks 28-30 -->

### Phase 2: Wiring & Tests
- [x] Task 31: Wire `healer.Detect` into observe (list `Nodes()` once/tick; replace the coarse heuristic; route to `Guard.Heal`); `main` passes the threshold. (depends on 29, 30)
- [x] Task 32: Tests — `nodeSatisfies` + `Detect` signature matrix (threshold / active / down / no-constraint / running) + reconciler heal-decision (active node → 1 ForceUpdate; down node → 0; dry-run → 0) + config threshold. (depends on 31)
<!-- Commit checkpoint: tasks 31-32 -->

## Definition of Done
- `go build ./...`, `go vet ./...` pass; `make test` green.
- `Detect` returns stuck **only** when constraints exist, a task is pending+desired-running beyond `heal_threshold`, and a constraint-satisfying node is Active+Ready; it returns not-stuck when the node is still Down, the task is below threshold, there are no constraints, or the task is running.
- With dry-run disabled, a genuinely stuck service triggers exactly one `ForceUpdate` per cooldown window; with dry-run enabled (default), zero.
- `internal/core/*` (incl. healer) imports nothing from `internal/adapter` or the Docker SDK (purity holds via `go list -deps`).
