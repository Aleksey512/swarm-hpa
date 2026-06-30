# Implementation Plan: Scale Stabilization

Branch: main (git.enabled: true; git.create_branches: false — plan stays on the current branch)
Created: 2026-06-30

## Settings
- Testing: yes
- Logging: verbose
- Docs: yes  # mandatory documentation checkpoint at completion (route via /aif-docs)

## Roadmap Linkage
Milestone: "Scale stabilization"
Rationale: Milestone 9 — prevent replica flapping with separate scale-up/scale-down cooldowns, an absolute per-action step limit, and a scale-down stabilization window (max-over-window), the K8s HPA stabilization analogue.

## Scope & Non-Goals
**In scope:** a pure absolute step limiter in `core/autoscaler`; direction-aware cooldowns (separate scale-up / scale-down windows) in the guard; a scale-down stabilization window (in-memory, max-over-window) in the app layer; the global config knobs for all three; wiring into the reconcile loop; and tests.

**Out of scope:** packaging (milestone 11). Step limit is **absolute replicas** (not percentage). Settings are **global daemon config** (not per-service labels). Defaults preserve current behavior — operators opt into stabilization by setting non-default values.

## Architecture Notes
- **Pure step limit in the core.** `autoscaler.ClampStep(current, desired, maxStep)` caps the per-action change to `maxStep` replicas (`0` = unlimited). Deterministic, table-testable, no I/O — it sits in `core/autoscaler` next to `Desired`.
- **Direction-aware cooldown is app-layer.** `Cooldown` becomes a pure per-service timestamp tracker (`Allowed(id, window)` / `Record(id)`); the guard holds a `Cooldowns{ScaleUp, ScaleDown, Heal}` policy and picks the window by the proposed direction. Heal keeps using the general cooldown. This keeps the single guarded mutation chokepoint and the existing `port.Clock` injection.
- **Stabilization window is stateful app state.** A `Stabilizer` keeps a short in-memory history of per-service recommendations (clock-injected, mutex-guarded) and, for a scale-down, returns the **max** recommendation within the window — so a brief metric dip never shrinks the service. Scale-ups pass through immediately. Restart-safe by design (history resets; the daemon re-observes before acting).
- **Decision order in the loop:** `Desired` (proportional + tolerance) → `Stabilizer.Recommend` (scale-down hold) → `ClampStep` (cap magnitude) → `Guard.Scale` (dry-run + direction-aware cooldown). This mirrors the K8s recommend → stabilize → rate-limit pipeline.
- **Behavior-preserving defaults.** `ScaleUpCooldown`/`ScaleDownCooldown` default to the current `3m`; `MaxScaleStep` and `ScaleDownStabilizationWindow` default to `0` (disabled). Nothing changes until an operator opts in; docs recommend flapping-resistant values.

## Commit Plan
<!-- 6 tasks; checkpoints below. git.enabled is true. -->
- **Commit 1** (after tasks 43-45): `feat: absolute step limit + per-direction scale cooldowns + config`
- **Commit 2** (after tasks 46-48): `feat: scale-down stabilization window + wiring + tests`

## Tasks

### Phase 1: Core + Config
- [x] Task 43: `autoscaler.ClampStep(current, desired, maxStep)` — cap per-action change to `maxStep` replicas (`0` = unlimited). Pure. (independent)
- [x] Task 44: Config knobs — `ScaleUpCooldown` / `ScaleDownCooldown` (default `3m`), `MaxScaleStep` (uint, default `0`), `ScaleDownStabilizationWindow` (default `0`); keep `Cooldown` for heal. Flags/env, validation, `LogValue`; update `config_test.go`. (independent)
<!-- Commit checkpoint: 43-45 -->

### Phase 2: App
- [x] Task 45: Direction-aware cooldown — `Cooldown` becomes a pure timestamp tracker (`Allowed(id, window)`); guard holds `Cooldowns{ScaleUp, ScaleDown, Heal}` and gates by direction. Update `NewGuard` + all call sites. (depends on 44)
- [ ] Task 46: `Stabilizer` — in-memory per-service recommendation history; `Recommend(id, current, desired, now)` returns the max recommendation within the down-window for scale-downs, immediate otherwise; prune old entries; `NewStabilizer(window, clock)`. (depends on 44)

### Phase 3: Wiring + Tests
- [ ] Task 47: Wire into `reconciler.observe` (`Desired` → `Stabilizer.Recommend` → `ClampStep` → `Guard.Scale`); `main.go` builds the stabilizer and passes the step limit + cooldown policy; update constructors/test sites. (depends on 43, 45, 46)
- [ ] Task 48: Tests — `ClampStep`, direction-aware cooldown, stabilizer (max-over-window / post-window / scale-up / pruning), config, reconciler integration. (depends on 47)
<!-- Commit checkpoint: 46-48 -->

## Definition of Done
- `go build ./...`, `go vet ./...` pass; `make test` green.
- With defaults, behavior is unchanged. With `MAX_SCALE_STEP=N`, a single scaling action never moves replicas by more than `N`. With `SCALE_DOWN_STABILIZATION=W`, a scale-down is held until the recommendation has stayed low for `W` (max-over-window); scale-ups are unaffected. Scale-up and scale-down honor their own cooldown windows; heal still uses the general cooldown.
- `internal/core/*` imports nothing from `internal/adapter`/`internal/app` or the Docker/Prometheus clients (purity holds via `go list -deps`).
- Documentation checkpoint run at completion (Docs: yes).
