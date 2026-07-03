# Plan: GitOps stack status API + drift detection + UI (v0.4.0)

- **Branch:** none (`git.create_branches: false`) — work on `main`
- **Created:** 2026-07-03
- **Mode:** Full
- **Scope:** v0.4.0 milestone **"Status, drift, web UI & API"**. Adds an in-memory per-stack status store, on-demand drift detection (non-autoscaled replicas only), a `GET /stacks` JSON API, and a read-only HTML UI on the manager metrics server.

## Settings

- **Testing:** yes — table-driven tests for the pure drift fn + the store + the handler (fake store / fake `StackStateReader`); `-race` for the store and any concurrency; existing GitOps integration tests stay green
- **Logging:** verbose (DEBUG per status write + per API request; INFO startup; **never log secret contents** — unchanged)
- **Docs:** yes — mandatory docs checkpoint (S-T6) via `/aif-docs`
- **HTTP:** rides on the existing metrics listener (`--metrics-addr`, default `:9095`) — **no new flag**; only registered when `--gitops` is enabled (status is GitOps-scoped)

## Roadmap Linkage

- **Milestone:** v0.4.0 → "Status, drift, web UI & API"
- **Rationale:** the last substantive v0.4.0 feature — gives operators a swarm-cd-parity view of stack health (revision / last-error / drift) plus the drift addition swarm-cd lacks. `/metrics` for sync actions already exists (`sync_total`, `deploys_total`, `sync_errors_total`, `last_sync_timestamp_seconds`), so this plan focuses on the status store + drift + API + UI. Do **not** mark the milestone complete from this plan — that belongs to `/aif-implement` + `/aif-verify`.

## Design Context

1. **Drift = live vs desired, non-autoscaled replicas only.** `Desired` = replicas from the last rendered compose for services that are **not** `mode: global` and **not** `swarm.autoscaler.enabled=true`. `Live` = `port.StackStateReader.StackServices(ctx, stack)` (real impl in `adapter/swarm`). Autoscaled services are excluded because the HPA intentionally changes their replicas (carry-forward preserves it) — their replica delta is *not* drift. Global services have no replica count. This mirrors the detection already in `adapter/stackdeploy/carryforward.go`.
2. **Drift is on-demand.** Computed per `GET /stacks` request from **fresh** live state + the `DesiredReplicas` snapshot the loop stored at last render — so it never goes stale against an autoscaler move, and the loop stays focused on sync/deploy. Cost: one Swarm read per stack per request (N small). A short per-call timeout (~2s) + best-effort handling (drift field `nil` with an `error` note on failure, never a 5xx for the whole payload).
3. **New infrastructure: an in-memory `StackStatusStore`.** Nothing queryable exists today — the `Recorder` is a Prometheus-only sink (counters/gauges), and the loop's `lastDeployedRev/OK` maps aren't exposed. The store is a new `core/port.StackStatusStore` (`SetStatus`, `Snapshot`), implemented by `adapter/statusstore` (RWMutex, deep-copy snapshot). The loop **writes**; the API handler **reads**.
4. **Pure drift in the core.** `core/stackstatus.Drift(desired, live) []ServiceDrift` is a deterministic, stdlib-only function — table-testable, no I/O. Keeps the decision logic in the testable center (architecture principle 1).
5. **HTTP on the metrics mux.** `GET /stacks` (JSON) + `GET /` (server-rendered HTML, no client JS — refresh to update) register on the existing metrics `ServeMux` in `cmd/swarm-hpa/app.go`, next to `/metrics`. No new listen address, no new flag. Gated on `--gitops` (nil handler → not registered when gitops is off).
6. **Read-only, no new mutating channel.** Status/UI is a pure read surface. It does not route through the reconciler `Guard` (no mutations) and does not touch the single guarded mutation path (architecture principle 3/7 intact).

## Tasks

### Phase 0 — Core + store
- [x] **S-T1 — Core: status model + pure `Drift` + `StackStatusStore` port** *(done; model.StackStatus/ServiceDrift, pure Drift sorted+autscaled/global-excluded, port.StackStatusStore; -race green)*
  `internal/core/model/stackstatus.go` (`StackStatus`, `ServiceDrift`), `internal/core/stackstatus/drift.go` (pure `Drift(desired map[string]uint64, live []model.StackService) []ServiceDrift` — skip missing/global live, `Drifted = live != desired`, sorted), `internal/core/port/stackstatus.go` (`StackStatusStore{ SetStatus; Snapshot }`), + table-driven `drift_test.go`. Pure core, no logging. `#blocked-by: none`
- [x] **S-T2 — Adapter: in-memory `StackStatusStore`** *(done; RWMutex store, Snapshot deep-copies + sorts, SetStatus copies input; -race incl. concurrency green)*
  `internal/adapter/statusstore/store.go` (RWMutex; `Snapshot` returns a deep copy sorted by name), `New(logger)`, DEBUG logs on set/snapshot; `store_test.go` (round-trip, copy-independence, `-race`). `#blocked-by: S-T1`

### Phase 1 — Loop
- [x] **S-T3 — Loop: write per-stack status + desired-replicas snapshot** *(done; syncStack tracks outcome via deferred recordStatus, desiredReplicas excludes autoscaled/global, New += statusStore param, all call sites updated, WritesSuccess/WritesFailure tests green; main.go passes nil until S-T5)*
  `internal/app/gitopsync/loop.go`: add `statusStore port.StackStatusStore` to `New` (nil-safe); after each `syncStack` outcome write `StackStatus` (revision, OK, error stage/msg, last-sync, deploy count) + `DesiredReplicas` (non-autoscaled, non-global, mirroring carry-forward detection) computed from the rendered compose. Update all `New` call sites (main.go + tests). DEBUG "gitops: status updated". `#blocked-by: S-T2`

### Phase 2 — API + UI
- [x] **S-T4 — Adapter: `GET /stacks` JSON + drift UI handler** *(done; http.Handler with /stacks JSON + // /ui HTML (go:embed), on-demand drift w/ 2s timeout, best-effort degrade; 5 handler tests -race green)*
  `internal/adapter/stackapi/handler.go` (`http.Handler`): `/stacks` → JSON of `store.Snapshot()` enriched with on-demand drift (`live.StackServices` + `stackstatus.Drift`, ~2s timeout, best-effort); `/` and `/ui` → server-rendered HTML (`html/template`, no client JS). `ui.html` via `//go:embed`. `handler_test.go` (fake store + fake `StackStateReader`: drift shape, autoscaled excluded, 200/404/405). DEBUG per request. `#blocked-by: S-T2`

### Phase 3 — Wiring + docs
- [x] **S-T5 — Wire store + stackapi into cmd (metrics mux)** *(done; statusstore+stackapi built when gitops enabled, threaded via appDeps.stackAPI, registered on metrics mux at /stacks + /; same store fed to gitopsync.New; build/vet/test -race(unit+integration)/lint green)*
  `cmd/swarm-hpa/main.go` gitops block: `statusstore.New` → `gitopsync.New` + `stackapi.New(statusStore, swarmCtl, logger)`; `cmd/swarm-hpa/app.go`: register `/stacks` + `/` on the metrics mux (gated on gitops). Startup INFO. Verify build/vet/`-race`(unit+integration)/lint green. `#blocked-by: S-T3, S-T4`
- [x] **S-T6 — Docs checkpoint (mandatory) via `/aif-docs`** *(done; docs/gitops.md Status/drift/UI section + migration notes; ARCHITECTURE tree+principle 7; AGENTS tree; README+DESCRIPTION feature deltas)*
  `docs/gitops.md` "Status, drift & UI" section + JSON shape + on-demand non-autoscaled drift semantics + migration pending-list update; README one-liner; DESCRIPTION feature delta; ARCHITECTURE tree (`statusstore`, `stackapi`, `core/stackstatus`, new ports) + read-only-surface note; AGENTS.md structure map. Do NOT edit RULES.md. `#blocked-by: S-T5`

## Commit Plan

1. **After S-T1–S-T3** — `feat(gitops): per-stack status store + non-autoscaled drift model` *(core + store + loop writes)*
2. **After S-T4–S-T5** — `feat(gitops): GET /stacks status API + drift UI` *(handler + wiring)*
3. **After S-T6** — `docs(gitops): document stack status API + drift`

## Acceptance signals

- `GET /stacks` returns JSON: one entry per configured stack with revision, ok/last-error, last-sync, deploy count, and drift (`[]ServiceDrift` + `Drifted bool`).
- Drift flags only non-autoscaled, non-global services whose live replicas ≠ desired; autoscaled/global services never appear as drift; a fresh live read is used per request (on-demand).
- `GET /` returns a read-only HTML table of the same data (refresh to update); no client JS, no new dependencies.
- A failed `StackServices` for one stack degrades that stack's drift to an error note (never 5xx the whole payload).
- The store is concurrency-safe (`-race` clean); `Snapshot` returns an independent copy.
- Existing GitOps behavior unchanged: sync/deploy/carry-forward/dry-run/sops/rotation all still green; `go test ./... -race` + `-tags integration -race` + `golangci-lint` green.
- No new CLI flag and no new listen address (rides on `--metrics-addr`); status surface only registered when `--gitops`.

## Next step

Run `/aif-implement` to execute (reads this plan + `/tasks`). **Strongly recommend `/clear` first** — this session is very long (the whole concurrent-scheduler milestone + verify + roadmap + push + merge already happened here); the plan + ROADMAP persist across `/clear`.
