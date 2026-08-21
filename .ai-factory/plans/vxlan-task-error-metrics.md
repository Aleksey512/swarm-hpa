# Implementation Plan: Task-Error Observability (vxlan alert) + Orphan Services

Branch: feature/vxlan-task-error-metrics
Created: 2026-08-21

Two production observability features for the daemon:

1. **Task-error metrics with a sliding window** — classify task errors from
   Docker (the headline class: `network sandbox join failed: … error creating
   vxlan interface: file exists`, an old Swarm networking bug) and expose
   per-service Prometheus metrics over the last N minutes (default 5), so an
   alert (e.g. `increase(...) > 0` right after a deploy) catches it.
2. **Orphan services** — live Swarm services that are not declared in any
   configured stack (via `stacks.yaml`) nor managed by the autoscaler/healer,
   surfaced in the `GET /stacks` JSON + HTML UI with an on-demand scan.

## Settings

- Testing: yes
- Logging: verbose
- Docs: yes  # mandatory docs checkpoint in /aif-implement

## Roadmap Linkage

Milestone: "Task-error observability + orphan services (v0.9.0)"  # new milestone; add to ROADMAP.md in the docs task
Rationale: Both are production-observability features on the read-only surface, a natural v0.9.0 after the /stacks UI work in v0.8.0.

## Commit Plan

- **Commit 1** (after tasks 1–3): `feat(core): task-error classification + all-cluster read ports`
- **Commit 2** (after tasks 4–6): `feat(app): sliding-window task-error tracker + orphan scan in loops`
- **Commit 3** (after tasks 7–9): `feat(observability,stackapi): task-error + orphan metrics, /stacks surface`
- **Commit 4** (after tasks 10–12): `feat(config): deploy-alert + window flags, README/docs`

## Design Decisions

- **Classification, not raw strings.** Error strings are classified into a
  bounded set (`vxlan_file_exists`, `network_sandbox_join_failed`, `other`).
  Raw error text never becomes a Prometheus label (cardinality discipline —
  same precedent as `LastRevision` refusing Git revisions as labels,
  `observability/metrics.go:262`).
- **Cluster-wide, not per-managed-service.** The vxlan bug hits *any* service
  task; the existing `Tasks()` read is per-managed-service AND filters
  `desired-state=running`, which hides superseded failed tasks. New
  unfiltered reads: `AllTasks` and `AllServices` on the swarm adapter +
  `port.SwarmRead` port. The reconciler keeps its per-service flow untouched.
- **One-pass classification, two consumers.** The same classified task list
  feeds (a) the window tracker → Prometheus gauges and (b) a ring the
  status-API reads on demand (5-minute view for the UI/deploy alert) — no
  duplicate TaskList calls.
- **Window tracker in `app/taskerrors`.** A bounded memory structure
  (last error per (service, slot, taskID) — dedup, not per-tick append;
  bounded by distinct failed tasks, naturally small) with pure pruning by
  `now.Sub(t.Since) >= window` via the injected `Clock`.
- **Orphan definition.** Live service is an orphan iff: its
  `com.docker.stack.namespace` label is not a configured stack name (covers
  both `docker service create` leftovers and stacks deployed outside this
  daemon's config) AND it does not carry `swarm.autoscaler.*` management
  labels. Autoscaled/global services are excluded from `DesiredReplicas` by
  design (`gitopsync/loop.go:586`), so stack-membership via the namespace
  label — not the desired map — is the authoritative check.
- **Orphan scan placement.** On demand in the `/stacks` handler (same pattern
  as drift: computed per request under a timeout, never persisted). It needs
  only the stacks config (names), not sync state — no coupling to the GitOps
  loop tick.
- **Read-only features.** No new mutations, no routing through the reconciler
  Guard; both features are pure observations on the existing read surface.

## Tasks

### Phase 1: Core — classification + ports

- [x] Task 1: Pure task-error classification in `core/taskerrors`
  Create `internal/core/taskerrors/classify.go` (new package, pure, stdlib
  only): `type Class string` with constants (`ClassVxlanFileExists = "vxlan_file_exists"`,
  `ClassNetworkSandboxJoin = "network_sandbox_join_failed"`, `ClassOther = "other"`);
  `func Classify(errText string) Class` — substring match, order matters:
  `error creating vxlan interface: file exists` (optionally wrapped in the
  `network sandbox join failed` context) → vxlan; other `network sandbox join
  failed` → sandbox; everything else → other. Nil-safe for empty strings.

  Files: `internal/core/taskerrors/classify.go`
  Tests: `internal/core/taskerrors/classify_test.go` — table-driven: the real
  Swarm error from the ticket, both substrings alone, empty string, unrelated
  errors ("No such container: …", "task: non-zero exit"), case sensitivity.
  LOGGING: none (pure core; the app layer logs classifications).

- [x] Task 2: Pure orphan-detection rule in `core/stackstatus`
  Create `internal/core/stackstatus/orphans.go`:
  `func Orphans(configuredStacks []string, live []model.LiveService) []model.LiveService`
  where `model.LiveService` is a new minimal view (`Name`, `StackNamespace`
  from the `com.docker.stack.namespace` label, `Labels map[string]string`).
  Pure rule: keep services whose namespace label is empty or not in the
  configured set AND that carry no `swarm.autoscaler.`-prefixed label.
  Deterministic order (sort by name).

  Files: `internal/core/stackstatus/orphans.go`, `internal/core/model/gitops.go` (add `LiveService`)
  Tests: `internal/core/stackstatus/orphans_test.go` — table-driven: stack
  service (not orphan), autoscaler-managed non-stack service (not orphan),
  `docker service create` leftover (orphan), service of an unknown stack
  namespace (orphan), empty inputs.
  LOGGING: none (pure core).

- [x] Task 3: `SwarmRead` port + swarm-adapter implementations
  Add `internal/core/port/swarm.go`:
  `type SwarmRead interface { AllTasks(ctx) ([]model.TaskView, error); AllServices(ctx) ([]model.LiveService, error) }`.
  Implement in `internal/adapter/swarm/swarm.go`: `AllTasks` = TaskList with
  NO filters (see note: the `desired-state=running` filter hides superseded
  failed tasks — exactly the ones carrying vxlan errors); `AllServices` =
  ServiceList unfiltered, mapping to `model.LiveService` (Name =
  `svc.Spec.Name`, StackNamespace from the namespace label, Labels =
  `svc.Spec.Labels`). 10s `callTimeout` + error wrapping like existing
  methods; DEBUG log counts. Compile-time `var _ port.SwarmRead = (*Adapter)(nil)`.

  Files: `internal/core/port/swarm.go`, `internal/adapter/swarm/swarm.go`
  Tests: extend the swarm-adapter fake-client test — fake TaskList/ServiceList
  returns → verify no filter args, mapping correctness (namespace label
  extraction, label map copy), error wrapping.
  LOGGING: DEBUG "all tasks observed" / "all services observed" with counts.

### Phase 2: App — window tracker + wiring

- [x] Task 4: Sliding-window error tracker in `app/taskerrors`
  Create `internal/app/taskerrors/tracker.go`: `type Tracker struct` with
  `Record(events []model.TaskErrorEvent)` (dedup key `(ServiceName, Slot,
  TaskID)` — the same task's error counts once; re-recording refreshes
  nothing) and `WindowSnapshot(now time.Time, window time.Duration)
  map[ServiceName]map[taskerrors.Class]int` — prunes entries with
  `now.Sub(Since) >= window`, pure over injected `port.Clock`. Bounded by
  distinct failed tasks (a service's superseded tasks); a hard cap
  (e.g. 10_000 entries, drop-oldest) guards pathological cases.

  Files: `internal/app/taskerrors/tracker.go`
  Tests: `internal/app/taskerrors/tracker_test.go` — table-driven with a fake
  Clock: dedup (same task twice → 1), window expiry (error at T, snapshot at
  T+5m+1s → gone), multi-service/multi-class counting, cap eviction.
  LOGGING: DEBUG per Record (service, class, count) and window pruning stats.

- [x] Task 5: `TaskErrorEvent` model + service-name join
  Add `model.TaskErrorEvent` (`ServiceID`, `ServiceName`, `Slot`, `TaskID`,
  `Class string` — a plain string in the model so `core/model` does not import
  `core/taskerrors`; the typed constants live in `core/taskerrors` and are
  assigned at classification time; `Since time.Time`, `Err string` for
  logging). Since `AllTasks` returns TaskView keyed by ServiceID only, the
  tracker wiring joins against `AllServices` (one map build, no N+1).

  Files: `internal/core/model/task.go`
  Tests: covered by Task 6 wiring tests.
  LOGGING: none (model).

- [x] Task 6: Reconciler hook — observe + record task errors each tick
  In `internal/app/reconciler/reconciler.go` `observe()` (before the
  per-service loop, ~line 127): one `AllTasks` call; filter tasks with
  non-empty `Err`; classify; build events joined with service names; feed the
  tracker; DEBUG log per tick (`task_errors_observed`, counts per class).
  Tracker is a new optional reconciler dependency via the existing options
  pattern (`options.go`, like `WithRebalancing`) — nil tracker = feature off
  (keeps agent mode / tests unchanged).

  Files: `internal/app/reconciler/reconciler.go`, `internal/app/reconciler/options.go`
  Tests: fake SwarmRead in reconciler tests — vxlan task present → tracker
  records 1 event; clean cluster → 0; AllTasks error → loop continues (log
  WARN, no panic), feature degrades gracefully.
  LOGGING: DEBUG per tick with per-class counts; WARN on AllTasks failure
  (once per tick, loop continues — matches existing resilience pattern).

### Phase 3: Orphan scan + deploy alert

- [x] Task 7: Orphan scan in the `/stacks` handler (on demand)
  In `internal/adapter/stackapi/handler.go` `buildPayload` (~line 114): after
  drift, call the new `AllServices` read under the same 2s-timeout pattern;
  run `stackstatus.Orphans(stackNames, live)`; degrade to `orphans_error`
  field on failure (mirror `drift_error`). Payload gains `orphans:
  [{name, namespace}]`, `orphans_count`; `uiSummary` gains an orphan total.

  Files: `internal/adapter/stackapi/handler.go`
  Tests: handler test with fake SwarmRead — orphan listed; stack + autoscaler
  services excluded; read failure → `orphans_error`, HTTP 200 (drift_error
  precedent).
  LOGGING: DEBUG per request (scan duration, orphan count); WARN on read failure.

- [x] Task 8: HTML UI — orphan section + task-error badges
  In `internal/adapter/stackapi/ui.html`: a collapsible "Orphan services"
  section after the stacks table (~line 76): table (name, detected namespace,
  hint "not in any configured stack"), empty-state line "No orphan services —
  every live service belongs to a configured stack or the autoscaler". Plus a
  summary chip `orphans: N` next to the existing summary line (line ~33).
  Follow the existing CSS classes (lines 14–24) and the drift-cell badge
  pattern (lines 67–72).

  Files: `internal/adapter/stackapi/ui.html`
  Tests: UI smoke via handler test (render with orphan payload → strings
  `Orphan services`, service name present; zero orphans → empty-state text).
  LOGGING: none (template).

- [x] Task 9: Post-deploy vxlan alert in the GitOps loop
  In `internal/app/gitopsync/loop.go` after each successful group deploy
  (~line 440, after the dry-run gate): schedule a delayed check (goroutine
  with `time.After(checkDelay)`, default 90s, cancellation via loop ctx; NOT a
  new loop) — read errors from the shared tracker's window snapshot; if any
  `vxlan_file_exists`/`network_sandbox_join_failed` events exist for services
  of the just-deployed stack (match by namespace label → stack name), log
  ERROR `gitops deploy: network sandbox (vxlan) task errors detected` with
  stack, services, counts + record a `StackTaskErrors` recorder signal. The
  tracker is shared app state injected via option (`WithTaskErrorTracker`).
  This is the "alert при деплое" hook — the ERROR log is alertable by any log
  pipeline; the metric below is the Prometheus-native alert.

  Files: `internal/app/gitopsync/loop.go`, `internal/app/gitopsync/options.go`
  Tests: fake tracker + fake clock: deploy OK + vxlan event in window → ERROR
  logged (assert via slog handler capture), recorder called; clean window →
  no ERROR; deploy failure → no check scheduled.
  LOGGING: INFO "post-deploy error check scheduled" (stack, delay); ERROR on
  detection with full context (this is the alert line).

### Phase 4: Metrics + config + docs

- [x] Task 10: Prometheus metrics for task errors + orphans
  In `internal/adapter/observability/metrics.go`: gauges
  `swarm_hpa_task_errors_window{service,class}` (windowed count, from the
  tracker snapshot — updated by a new `Recorder.TaskErrorsWindow(service,
  class string, n int)` port method) and `swarm_hpa_orphan_services` (gauge,
  set by `Recorder.OrphanServices(n int)` from the /stacks scan — cheap to
  also update per scan). Stale-series hygiene the `lastDecisionState` way
  (`metrics.go:304`): track the previous per-(service,class) set,
  `DeleteLabelValues`/`DeletePartialMatch` for combinations that dropped to
  zero or vanished. Extend `port.Recorder` + `NopRecorder` in lockstep.

  Files: `internal/adapter/observability/metrics.go`, `internal/core/port/recorder.go`
  Tests: observability tests — record, gather, assert series/labels/values;
  service error clears → series deleted; NopRecorder compiles.
  LOGGING: DEBUG on metric updates.

- [x] Task 11: Config flags + wiring in the composition root
  `internal/config/config.go` following the `HEAL_THRESHOLD` pattern
  (struct field → default → env → flag → validate → `LogValue`):
  `--task-errors-window` / `TASK_ERRORS_WINDOW` (duration, default `5m`),
  `--deploy-check-delay` / `DEPLOY_CHECK_DELAY` (duration, default `90s`),
  `--orphans-scan` / `ORPHANS_SCAN` (bool, default true — read-only, on by
  default). Thread through `cmd/swarm-hpa/app.go` (tracker construction,
  `reconciler.WithTaskErrors(tracker, window)`, gitopsync
  `WithTaskErrorTracker`, stackapi orphan scan toggle). Startup log prints
  effective values (existing "effective configuration" pattern).

  Files: `internal/config/config.go`, `cmd/swarm-hpa/app.go`, `cmd/swarm-hpa/main.go`
  Tests: config LoadArgs table (flag/env/default precedence, invalid duration
  rejected in Validate); wiring smoke — flags reach the loops (constructor
  args in a test harness).
  LOGGING: INFO at startup with effective window/delay/scan settings.

- [x] Task 12: Docs — README + ROADMAP v0.9.0
  README observability section: the two new metric families with example
  alerting rules (`increase(swarm_hpa_task_errors_window{class=
  "vxlan_file_exists"}[5m]) > 0` right after deploys; `swarm_hpa_orphan_services
  > 0`), the `/stacks` UI orphan section, new flags with env vars and
  defaults. Add the v0.9.0 milestone to `.ai-factory/ROADMAP.md` (unchecked
  until /aif-implement completes; the docs task checks it off with date).

  Files: `README.md`, `docs/` (wherever flags/metrics are documented — check
  `docs/` layout), `.ai-factory/ROADMAP.md`
  Tests: none (docs). Verify: `make lint` + `make test` pass; `grep -r
  "docker/docker" internal/core/` still clean (core purity).
  LOGGING: n/a.
