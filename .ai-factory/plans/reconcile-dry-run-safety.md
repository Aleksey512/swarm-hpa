# Implementation Plan: Reconcile Dry-Run Safety (Guarded Mutation Path)

Branch: none (git not initialized; `git.enabled: false`)
Created: 2026-06-30

## Settings
- Testing: yes
- Logging: verbose
- Docs: no  # warn-only; documentation deferred to a later milestone

## Roadmap Linkage
Milestone: "Reconcile loop + dry-run safety"
Rationale: Milestone 3 — build the single guarded mutation path (dry-run + opt-in + cooldown) that every later Swarm mutation (scale, heal) must flow through, with mutations suppressed by the dry_run default.

## Scope & Non-Goals
**In scope:** the raw Swarm mutation primitives (`Scale`/`ForceUpdate` on the port + adapter, with version-index optimistic concurrency), a per-service cooldown tracker, a `cooldown` config knob, and the `Guard` chokepoint (dry-run + cooldown). The already-detected stuck-pending candidate is routed through `Guard.Heal` to exercise the path end-to-end — and the `dry_run=true` default suppresses any real mutation.

**Out of scope:** scaling math / metric-driven decisions (milestone 5), the full 5-point stuck-task signature (milestone 6), and separate up/down stabilization windows (milestone 9). This milestone proves the *safety mechanism*, not the decisions that drive it.

## Architecture Notes
- The Guard is the **only** place that calls the SwarmController mutation methods. dry-run, cooldown, and no-op checks live there — not scattered at call sites (per ARCHITECTURE.md "one guarded mutation chokepoint").
- Adapter mutations use re-inspect → mutate the inspected spec → version-indexed `ServiceUpdate`, retrying on `errdefs.IsConflict` (docker-swarm-go-sdk skill). Never build a fresh `ServiceSpec`.
- `internal/core/*` stays Docker-free; the Guard/Cooldown live in `internal/app/reconciler` and depend on `port` interfaces + `port.Clock`.
- Safety invariant: with `dry_run=true` (the default), a stuck-pending candidate logs "dry-run: would force-update (heal)" and applies nothing.

## Commit Plan
<!-- 6 tasks; checkpoints below. Applies once git is initialized (git.enabled is currently false). -->
- **Commit 1** (after tasks 14-16): `feat: swarm mutation primitives, cooldown tracker, cooldown config`
- **Commit 2** (after tasks 17-18): `feat: guarded mutation path (dry-run + cooldown) wired into reconciler`
- **Commit 3** (after task 19): `test: cover cooldown, guard suppression, and adapter mutations`

## Tasks

### Phase 1: Primitives, Cooldown, Config
- [x] Task 14: Add `Scale`/`ForceUpdate` to `port.SwarmController` + adapter impl (version-indexed `ServiceUpdate` + optimistic retry on `errdefs.IsConflict` — confirmed valid at v28); log `ServiceUpdateResponse.Warnings`; update dockerAPI seam (`ServiceInspectWithRaw`/`ServiceUpdate`) and fakes.
- [x] Task 15: Per-service `Cooldown` tracker (`Allowed`/`Record`) using `port.Clock`. (independent)
- [x] Task 16: Add `Cooldown` duration to config (flag/env/default, validate >= 0). (independent)
<!-- Commit checkpoint: tasks 14-16 -->

### Phase 2: Guard & Wiring
- [x] Task 17: Implement `Guard` — dry-run + cooldown + no-op gate; skips non-replicated services; dry-run intent throttled via cooldown `Record` (no per-tick spam); the sole caller of SwarmController mutations. (depends on 14, 15, 16)
- [x] Task 18: Wire `Guard` into the reconciler + `main`; route stuck-pending candidates through `Guard.Heal` — at most **one Heal per affected service per tick** (dry-run-suppressed). (depends on 14, 17)
<!-- Commit checkpoint: tasks 17-18 -->

### Phase 3: Tests
- [x] Task 19: Tests — reusable `fakeClock`, cooldown windows, guard suppression matrix (dry-run / cooldown / no-op / non-replicated / applied), per-service Heal dedup, config cooldown, adapter mutation retry-on-conflict + nil-Replicated error + surfaced warnings. (depends on 17, 18)
<!-- Commit checkpoint: task 19 -->

## Definition of Done
- `go build ./...`, `go vet ./...` pass; `make test` green (cooldown, guard matrix, config, adapter mutations).
- With `dry_run=true` (default), a stuck-pending candidate produces a "dry-run: would force-update" log and **zero** `ServiceUpdate` calls (verified by a recording fake in the guard test).
- With dry-run disabled and outside cooldown, exactly one mutation call is made and the cooldown is recorded; within cooldown it is suppressed.
- `internal/core/*` still imports nothing from `internal/adapter` or the Docker SDK (purity holds via `go list -deps`).
- Adapter Scale/ForceUpdate re-inspect for a fresh version and retry once on a simulated conflict; `ServiceUpdateResponse.Warnings` are surfaced in logs.
- A service with multiple stuck-pending tasks triggers exactly **one** `Heal` call per tick (per-service dedup), and `Guard.Scale` is a no-op for non-replicated services.
