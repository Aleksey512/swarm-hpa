# Implementation Plan: Heal-only opt-in label (`swarm.autoscaler.heal`) → v0.2.0

Branch: none (git.create_branches=false — work on `main`)
Created: 2026-07-01

## Summary

Today the reconciler only considers services labeled `swarm.autoscaler.enabled=true`,
and mapping requires a full autoscaler policy (`min/max/metric/target`). That means a
placement-pinned stateful singleton (e.g. `rabbitmq-0N` constrained to a node) cannot be
**healed** by the daemon without giving it a fake autoscaler policy. This was surfaced by a
real multi-node cluster (swarmcd/followpulse) where the healer is the highest-value fit but
the pinned singletons have no business being autoscaled.

**Feature:** a new optional label `swarm.autoscaler.heal` that decouples healing from
autoscaling:

| Labels on service | Behaviour |
|---|---|
| `enabled=true` (+ policy) | autoscale **+** heal (unchanged — `heal` defaults to `enabled`) |
| `heal=true` alone | **heal-only**, no autoscaler policy required, service never scaled |
| `enabled=true` + `heal=false` | autoscale only, healing disabled (opt-out) |
| `heal=false` alone / no labels | not managed |

Backward compatible: existing `enabled=true` services keep autoscale+heal.

## Settings
- Testing: yes — table-driven tests for label parsing, swarm mapping, and the reconciler
  heal-only / autoscale-only branches (project convention: stdlib `testing` + port fakes).
- Logging: standard — INFO on opt-in with effective mode (autoscale/heal/both), DEBUG on
  skipped branches; parsing/core stay pure (no slog), the reconciler logs. `log/slog` fields
  as elsewhere (`service`, `reason`).
- Docs: yes — mandatory docs update (configuration.md + README) for the new label.

## Roadmap Linkage
Milestone: "Heal-only opt-in (swarm.autoscaler.heal)" — new **v0.2.0** milestone
Rationale: First feature past the v0.1.0 baseline; decouples the healer from the autoscaler
opt-in so pinned stateful singletons can be healed. (ROADMAP's formal owner is /aif-roadmap;
Task 12 adds the entry as part of the v0.2.0 cut.)

## Design notes (semantics resolution)
- `Autoscale = (enabled == true)` and still requires a valid policy; invalid policy on an
  enabled service → descriptive error, service skipped (unchanged).
- `Heal = heal-label-present ? parseBool(heal) : enabled` (so absence preserves today's
  behaviour, presence overrides).
- Managed set (considered at all) = `Autoscale || Heal`.
- Architecture preserved: `core` stays pure; the single guarded mutation chokepoint (Guard:
  dry-run + cooldown) is untouched; only opt-in filtering (adapter) and branch routing
  (reconciler) change.

## Commit Plan
- **Commit 1** (after tasks 7-10): `feat: heal-only opt-in via swarm.autoscaler.heal label`
  — config parsing + model flags + adapter union-filter + reconciler branch guards, with tests.
- **Commit 2** (after tasks 11-12): `docs: document swarm.autoscaler.heal + v0.2.0 roadmap`
  — configuration.md, README.md, ROADMAP.md.
- **Release** (task 13): annotated `v0.2.0` tag → existing dual-registry pipeline (no source commit).

## Tasks

### Phase 1: Core opt-in model
- [x] Task 7: Add `LabelHeal` + policy/flags resolution in `internal/config/labels.go`
  (heal-only needs no policy; `heal` defaults to `enabled`; invalid heal → error). Tests.
- [x] Task 8: Add `Autoscale`/`Heal` flags to `model.ManagedService` (`internal/core/model`);
  zero-value policy safe for heal-only. Keep the type pure.
- [x] Task 9: `ManagedServices` union of `enabled=true` ∪ `heal=true` (two filtered
  ServiceList calls, dedupe by ID) + tolerant `toManagedService` for heal-only
  (`internal/adapter/swarm/swarm.go`). Tests. (depends on 7, 8)
<!-- Commit checkpoint continues into Phase 2 -->

### Phase 2: Reconciler wiring
- [x] Task 10: Guard the scale branch by `Autoscale` and the heal branch by `Heal` in
  `internal/app/reconciler/reconciler.go`; standard opt-in/skip logging. Tests for
  heal-only (no metric/scale) and enabled+heal=false (no heal). (depends on 8, 9)
<!-- Commit checkpoint: tasks 7-10 → Commit 1 -->

### Phase 3: Docs & roadmap
- [x] Task 11: Document `swarm.autoscaler.heal` in `docs/configuration.md` (label table +
  "Healing opt-in" note) and `README.md`. (depends on 10)
- [x] Task 12: Add the v0.2.0 milestone to `.ai-factory/ROADMAP.md`. (depends on 10)
<!-- Commit checkpoint: tasks 11-12 → Commit 2 -->

### Phase 4: Release
- [ ] Task 13: Cut `v0.2.0` — **outward-facing; confirm before pushing the tag.** Pre-flight
  (CI green, secrets present, no existing tag), `git tag -a v0.2.0` + push, watch release.yml,
  verify `:0.2.0`/`:0.2`/`:latest` on GHCR + Docker Hub. (depends on 11, 12)
<!-- Commit checkpoint: task 13 → tag push (no source commit) -->

## Risks & Notes
- **Union filter correctness.** Two label-filtered ServiceList calls must dedupe by service ID
  (a service may carry both `enabled=true` and `heal=true`). Missing dedupe = double work /
  double heal attempts (cooldown would still gate, but avoid it).
- **Heal-only must not require a policy.** The tolerant `toManagedService` path is the crux —
  a heal-only service has no `min/max/metric/target`; parsing/mapping must not error on that,
  while still erroring for `enabled=true` with an invalid policy.
- **No metric reads for heal-only.** Guarding the scale branch by `Autoscale` avoids no-data
  metric noise on pinned singletons.
- **Backward compatibility is a hard requirement** — every existing `enabled=true` service must
  behave exactly as in v0.1.0 (autoscale + heal). Covered by a regression test in Task 10.
- **swarmcd integration is downstream** — once v0.2.0 ships, the followpulse dev stacks get
  clean `swarm.autoscaler.heal=true` labels on rabbitmq/stateful services (separate work, that
  repo, no commit without confirmation).
