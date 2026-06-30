# Implementation Plan: Autoscaler Core + Apply

Branch: none (git not initialized; `git.enabled: false`)
Created: 2026-06-30

## Settings
- Testing: yes
- Logging: verbose
- Docs: no  # warn-only; documentation deferred to a later milestone

## Roadmap Linkage
Milestone: "Autoscaler (HPA) core + apply"
Rationale: Milestone 5 — close the HPA loop: turn the observed metric into a desired replica count and apply it through the guarded mutation path (still dry-run by default).

## Scope & Non-Goals
**In scope:** the pure scaling decision (`core/autoscaler.Desired`: proportional formula + tolerance deadband + clamp to `[min,max]`) and wiring observe → decide → `Guard.Scale`, closing the HPA loop end-to-end on the dockerstats metric.

**Out of scope:** separate scale-up/down stabilization windows (milestone 9), Prometheus metrics (milestone 7), and the `/metrics` endpoint (milestone 8). The Guard already enforces dry-run + cooldown + non-replicated/no-op, so this milestone adds the *decision*, not new safety machinery.

## Architecture Notes
- The decision is **pure** (`core/autoscaler`), depending only on `core/model` + `math`; it stays Docker-free and is table-testable.
- The reconciler composes: `metrics.Value` → `autoscaler.Desired` → `Guard.Scale`. The Guard remains the sole mutation chokepoint (dry-run default keeps prod safe).
- A 10% tolerance deadband (like K8s HPA) avoids flapping near the target.
- Addresses review note #4: documents that metric value and target share the metric's units (dockerstats CPU is per-core; 100 == one full core).

## Tasks

### Phase 1: Decision, Wiring, Tests
- [x] Task 25: `core/autoscaler.Desired(current, value, policy)` — proportional + tolerance + clamp; pure; documents CPU%/target units.
- [x] Task 26: Wire observe → `autoscaler.Desired` → `Guard.Scale` (dry-run/cooldown gated); log the decision inputs. (depends on 25)
- [x] Task 27: Tests — `Desired` math (up/down/tolerance/clamp/current=0/target=0/idle) + reconciler decision path (computed desired reaches `swarm.Scale` when enabled; dry-run suppresses). (depends on 25, 26)

## Definition of Done
- `go build ./...`, `go vet ./...` pass; `make test` green (autoscaler math + decision wiring).
- `Desired` scales up/down proportionally, holds within the tolerance band, clamps to `[min,max]`, brings a zero-replica service up to `min`, and never acts when `target<=0`.
- With dry-run disabled and outside cooldown, the autoscaler-computed replica count reaches `SwarmController.Scale` exactly once; with dry-run enabled (default), zero `Scale` calls are made.
- `internal/core/*` (including the new autoscaler) imports nothing from `internal/adapter` or the Docker SDK (purity holds via `go list -deps`).
