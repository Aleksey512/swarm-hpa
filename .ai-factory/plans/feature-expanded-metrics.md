# Expanded self-observability metrics

- **Branch:** `feature/expanded-metrics` (from `main`)
- **Created:** 2026-07-15
- **Mode:** Full

## Summary

Surface, on `/metrics`, **what the daemon observes and decides** — not just action
counters. Today `/metrics` has `scales_total`/`heals_total`/`managed_services` etc.
(counters of *actions*), but nothing shows, per service: the current vs desired
replica count, the observed metric value, the last decision, cooldown state, the
pending-task count, or per-stack GitOps drift. This plan adds those gauges so
operators can dashboard and alert on the autoscaler's reasoning — including what it
*would* do under dry-run/cooldown.

Four metric groups (all selected): per-service decision, cooldown state,
pending-task count, per-stack drift.

## Settings

- **Testing:** yes — capturing fake recorder + observability registry assertions +
  gitops drift assertion; `go test -race` must stay green.
- **Logging:** verbose (default) — no new log lines required; the gauges ARE the
  persistent signal. Existing `scaling decision` / `observed service` DEBUG logs stay.
- **Docs:** yes (mandatory checkpoint via `/aif-docs`) — document the new gauges in
  `docs/observability.md`.

## Roadmap Linkage

- **Milestone:** "Expanded self-observability metrics (v0.5.0)"
- **Rationale:** Delivers the second v0.5.0 milestone exactly.
- **Caveat:** this branch was cut from `main`, which does **not** yet contain the
  `## v0.5.0` ROADMAP section (it lives on `feature/per-stack-pull-policy`, not yet
  merged). The linkage is by milestone name. Recommended: merge the pull-policy PR to
  `main` before/alongside this one so the milestone exists on `main`; otherwise
  `/aif-implement` will add the `v0.5.0` section on this branch (minor ROADMAP
  converge at merge).

## Design (confirmed against current code)

- **Recorder interface** (`internal/core/port/recorder.go`) is implemented by
  `observability.Recorder` + `port.NopRecorder`. All test fakes pass
  `port.NopRecorder{}` directly (or embed it), so **adding interface methods only
  requires updating `NopRecorder` (no-op) + the observability impl** — no fake breaks.
- **Decision data is in scope** in `reconciler.go` observe loop (`svc.Replicas`,
  `val`, `desired`, `final` at lines 170-177); `pending` from `countStates` (149).
- **Cooldown** tracker (`cooldown.go`) holds one shared last-action timestamp per
  service; direction windows live in `Cooldowns` on the Guard. A read-only
  `Until(serviceID)` accessor + a Guard helper mapping service→per-action remaining
  is the clean exposure path.
- **Drift** source: `desiredReplicas(composeMap)` (`loop.go:368`) → `setDesired`
  (`247`); live replicas come from the `StackStateReader` already used by
  carry-forward/deploy.
- **Cardinality:** `service`, `action` (4 fixed), `stack`, `decision` (3 fixed) are
  all bounded. `metric_value` is a gauge value, not a label. Stale `last_decision`
  series avoided by resetting the service's previous decision label each tick.

## Tasks

### Phase 1 — Metric surface
- [x] **5. Recorder surface: new gauges (interface + NopRecorder + observability impl)**
  (`internal/core/port/recorder.go`, `internal/adapter/observability/metrics.go`).
  Methods: `ServiceDecision`, `ServicePendingTasks`, `ServiceCooldown`,
  `StackReplicas`. Register GaugeVecs; document each with `Help:`. **blockedBy:** none.

### Phase 2 — Wiring
- [x] **6. Reconciler: per-service decision + pending gauges**
  (`internal/app/reconciler/reconciler.go`). Record decision (scale_up/down/hold from
  `final` vs `svc.Replicas`) inside the autoscale branch; record `pending` per service.
  **blockedBy:** 5.
- [x] **7. Cooldown: accessor + per-action in_cooldown/remaining gauges**
  (`cooldown.go`, `guard.go`, `reconciler.go`). Add `Cooldown.Until`; Guard helper
  `CooldownRemaining`; record per action. **blockedBy:** 5.
- [x] **8. GitOps: record per-stack desired vs live replica drift gauges**
  (`internal/app/gitopsync/loop.go`, `internal/core/model/stackstatus.go`). Record
  `StackReplicas` from compose desired + live-state read. **blockedBy:** 5.

### Phase 3 — Tests & docs
- [x] **9. Tests** (reconciler, observability, gitops). Assert recorded values + that
  the new metric families are exposed. **blockedBy:** 6, 7, 8.
- [x] **10. Docs** (`docs/observability.md`). Document the new gauges.
  **blockedBy:** 5.

## Commit Plan

6 tasks → checkpoints every 3-5.

- **Checkpoint A (after Task 7):** metric surface + all wiring (5–7). Message:
  `feat(observability): per-service decision, cooldown, pending gauges`
- **Checkpoint B (after Task 8):** gitops drift. Message:
  `feat(gitops): per-stack desired/live replica drift gauges`
- **Checkpoint C (after Task 10):** tests + docs. Message:
  `test(observability): cover new gauges` and `docs(observability): expanded /metrics`

## Verification

- `go build ./...`
- `go test ./internal/...`
- `go test -race ./internal/app/reconciler/... ./internal/app/gitopsync/... ./internal/adapter/observability/...`
- `go vet -tags integration ./internal/app/gitopsync/...`
- `golangci-lint run ./...`
- Manual: `curl localhost:9095/metrics | grep swarm_hpa_` and confirm the new gauges
  appear with `service`/`action`/`stack` labels for a running managed service.

## Next Steps

```
/aif-implement   # execute tasks 5→10 in dependency order
/aif-verify      # confirm completeness + gates
```
