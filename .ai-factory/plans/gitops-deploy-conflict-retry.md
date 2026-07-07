# Plan — Retry GitOps deploy + harden mutation on "update out of sequence"

- **Branch:** `feature/gitops-deploy-conflict-retry`
- **Created:** 2026-07-06
- **Type:** fix (concurrency / optimistic-locking resilience)

## Summary

A GitOps deploy (`docker stack deploy --with-registry-auth`) fails with
`rpc error: code = Unknown desc = update out of sequence` when the reconcile loop
(autoscaler `Scale` / healer `ForceUpdate` / rebalancer `ForceUpdate`) mutates a
service in the same Swarm daemon between docker/cli's internal spec read and its
`ServiceUpdate`. The two loops run as independent goroutines with no shared lock
(`cmd/swarm-hpa/main.go:188` gitops vs `cmd/swarm-hpa/main.go:195` reconcile).

The deploy path is the weak link: `docker stack deploy` (via docker/cli) does **not**
retry on a stale `Version.Index`, so it surfaces the collision. The autoscaler's own
`ServiceUpdate` retries on `errdefs.IsConflict` (`internal/adapter/swarm/mutate.go:74`),
but the real error arrives as gRPC `code = Unknown`, which `errdefs.IsConflict` likely
does **not** classify as a conflict — so that retry may also miss this specific error.

Carry-forward (`internal/adapter/stackdeploy/carryforward.go`) prevents the *replica-value*
fight; it does **not** prevent the *version-timing* race. The loop already self-heals a
failed deploy on the next tick (`lastDeployedOK`, `internal/app/gitopsync/loop.go`), but
`GITOPS_INTERVAL` (120s default) is too slow and noisy.

## Approach (chosen: A — retry + harden both paths)

Add a small shared `dockererr.IsVersionConflict(err)` helper that catches **both**
`errdefs.IsConflict` **and** the `update out of sequence` string. Use it in:

1. A new `WithRetry` decorator around the `DeployFunc` seam in `stackdeploy` — fast
   intra-deploy retry (~seconds), bounded to 3 attempts. The existing next-tick retry
   stays as the outer safety net.
2. `mutate.go` (autoscaler/healer/rebalancer) — symmetric resilience, replacing the bare
   `errdefs.IsConflict` check.

`docker stack deploy` is idempotent and carry-forward clamps replicas to `[min,max]`, so
re-running it on conflict converges safely. Non-conflict errors fail fast (real cause surfaced).

Rejected: a shared `sync.Mutex` between the reconcile and gitops loops (Approach C) — a
multi-second deploy would block scale/heal across **all** services every tick, regressing
the autoscaler's time-sensitive reactions.

## Settings

- **Testing:** yes — table-driven unit tests for the helper, the retry decorator, and the
  hardened mutate path (using the *real* `update out of sequence` string as a test fixture,
  which doubles as the empirical confirmation that the helper covers the `code = Unknown` case).
- **Logging:** verbose — DEBUG for happy path, WARN for each retry with attempt number + error,
  INFO on eventual success-after-retry.
- **Docs:** yes — `docs/gitops.md` (deploy retry behavior + `update out of sequence`
  troubleshooting, incl. external-second-writer diagnosis). Closes the doc gap from the
  prior explore session.

## Roadmap Linkage

- **Milestone:** none
- **Rationale:** The GitOps milestones in `ROADMAP.md` are already checked off; this is a
  resilience hardening of shipped functionality, not a new milestone. Skipped by design.

## Context (root cause — no RESEARCH.md was persisted; captured here for /aif-implement)

- Two concurrent mutators, no shared lock: gitops deploy vs reconcile (autoscale/heal/rebalance).
- Asymmetry: autoscaler retries on conflict (`mutate.go`), deploy does not (`dockercli.go`).
- `errdefs.IsConflict` may not catch `code = Unknown` → both paths can fail-fast on this error.
- Deploy is idempotent + carry-forward clamps replicas → whole-deploy retry is safe & converges.
- Loop already retries failed deploys next tick (`lastDeployedOK`); the new fast retry closes
  the seconds-vs-minutes gap.

## Key files

- **New** `internal/adapter/dockererr/dockererr.go` + `_test.go` — shared `IsVersionConflict`.
- **New** `internal/adapter/stackdeploy/retry.go` + tests — `WithRetry` decorator.
- `internal/adapter/stackdeploy/dockercli.go` — unchanged (decorator composes around `DockerCLIDeploy`).
- `cmd/swarm-hpa/main.go:167` — wrap `DockerCLIDeploy(...)` with `stackdeploy.WithRetry(..., logger)`.
- `internal/adapter/swarm/mutate.go:74` — `errdefs.IsConflict(err)` → `dockererr.IsVersionConflict(err)`; drop now-unused `errdefs` import + its `//nolint:staticcheck`.
- `internal/adapter/swarm/mutate_test.go` — add a case using the real error string.
- `docs/gitops.md` — concurrency + troubleshooting section.

## Tasks

### Phase 1 — Shared conflict detection

**✅ Task 1 — `dockererr.IsVersionConflict` helper + tests** (done: package + table-driven tests green)
- Create `internal/adapter/dockererr/dockererr.go`:
  ```go
  // IsVersionConflict reports whether err is a Swarm optimistic-concurrency rejection
  // (stale Version.Index). Transient: re-inspect/re-deploy and retry.
  // errdefs.IsConflict covers the SDK 409 path; the string match covers the raft-store
  // "update out of sequence" error, which arrives as gRPC code=Unknown and is not always
  // mapped to a Conflict.
  func IsVersionConflict(err error) bool {
      if err == nil { return false }
      if errdefs.IsConflict(err) { return true }
      return strings.Contains(err.Error(), "update out of sequence")
  }
  ```
- Tests (`dockererr_test.go`, table-driven): nil → false; `errdefs.Conflict(...)` → true;
  `errors.New("rpc error: code = Unknown desc = update out of sequence")` → true (key case);
  generic network error → false.
- **Logging:** none (pure helper). **Deliverable:** package compiles, tests green.

### Phase 2 — Retry the deploy (the actual symptom)

**✅ Task 2 — `stackdeploy.WithRetry` decorator + wiring + tests** (done: retry.go + main.go wiring + retry_test.go green)
- Create `internal/adapter/stackdeploy/retry.go`:
  - `const deployMaxAttempts = 3` (parity with `mutate.go` `maxUpdateAttempts`).
  - `WithRetry(deploy DeployFunc, logger *slog.Logger) DeployFunc` — loops up to
    `deployMaxAttempts`; on `dockererr.IsVersionConflict(err)`: WARN log
    (`stack`, `attempt`, `err`), backoff `time.Duration(attempt)*100*time.Millisecond`
    honoring `ctx.Done()`; non-conflict → return immediately; exhaustion → wrapped error.
    INFO log on success-after-retry (`attempts`).
- Wire in `cmd/swarm-hpa/main.go:167`:
  `stackdeploy.New(swarmCtl, stackdeploy.WithRetry(stackdeploy.DockerCLIDeploy(dockerCli), logger), logger)`.
- Tests (`retry_test.go`, table-driven, inject a recording fake `DeployFunc`):
  - fail twice with the real out-of-sequence string then succeed → nil, 3 calls.
  - fail with non-conflict → fail fast, 1 call, original error wrapped.
  - always conflict → error after 3 calls.
  - `ctx` cancelled mid-backoff → returns `ctx.Err()`.
- **Logging:** WARN per retry, INFO on success-after-retry. **Deliverable:** deploy retries
  on version conflict, fails fast otherwise.

### Phase 3 — Harden the autoscaler/healer/rebalancer path (symmetry)

**✅ Task 3 — Use `IsVersionConflict` in `mutate.go` + test** (done: mutate.go uses dockererr.IsVersionConflict; errdefs import + nolint removed; TestAdapterScaleRetriesOnUpdateOutOfSequence green)
- `internal/adapter/swarm/mutate.go:74`: replace `if !errdefs.IsConflict(err)` with
  `if !dockererr.IsVersionConflict(err)`. Remove the now-unused
  `"github.com/docker/docker/errdefs"` import and its `//nolint:staticcheck` comment;
  add `"github.com/Aleksey512/swarm-hpa/internal/adapter/dockererr"`.
- Update the warn log message wording if it says "version conflict" (still accurate).
- Add `TestAdapterScaleRetriesOnUpdateOutOfSequence` in `mutate_test.go`: fake returns
  `errors.New("rpc error: code = Unknown desc = update out of sequence")` on first update,
  succeeds on second → asserts 2 inspects + 2 updates. Proves the hardened path catches it.
- **Logging:** existing WARN ("service version conflict, retrying") retained.
  **Deliverable:** autoscaler retries on the real error string, not just `errdefs.Conflict`.

### Phase 4 — Docs

**✅ Task 4 — `docs/gitops.md`: deploy retry + troubleshooting** (done: "Concurrency with the autoscaler (deploy retry)" + "Troubleshooting: update out of sequence" sections added)
- Add a short "## Concurrency with the autoscaler" subsection under "How it works":
  - Deploy runs concurrently with autoscale/heal/rebalance in the same process; on a version
    conflict the deploy retries up to 3× (~seconds); the per-tick retry is the outer safety net.
  - Carry-forward prevents the replica-value fight, not the version-timing race.
- Add a "## Troubleshooting: `update out of sequence`" subsection:
  - Transient/episodic → internal race, self-heals; watch `sync_errors_total` / `deploys_total`.
  - Persistent → look for an external second writer (2nd swarm-hpa instance, swarm-cd still
    running, Portainer/CI/manual `docker service update`); cross-link `migrating-from-swarm-cd.md`.
- **Deliverable:** behavior + diagnosis documented; closes the doc gap.

### Phase 5 — Gate

**✅ Task 5 — Full test suite + lint** (done: gofmt/vet clean, `go test -race ./...` green, golangci-lint 0 issues — fixed 2 staticcheck findings: `errdefs` SA1019 nolint + `errOutOfSequence` rename)
- `gofmt -l .` (empty), `go vet ./...`, `go test ./...` (incl. `-race` for the adapter packages),
  `golangci-lint run` (project CI set). Fix any fallout from the new imports/refs.
- **Deliverable:** clean local CI mirror; branch ready for `/aif-verify` + review.

## Commit Plan

5 tasks → 2 logical commit checkpoints:

- **Commit 1 — after Task 3** (core fix):
  `fix(swarm): retry stack deploy and harden ServiceUpdate on 'update out of sequence'`
  (Tasks 1–3 + their tests: shared `dockererr.IsVersionConflict`, `WithRetry` decorator +
  wiring, `mutate.go` hardening).
- **Commit 2 — after Task 5** (docs + gate):
  `docs(gitops): document deploy retry + update-out-of-sequence troubleshooting`
  (Tasks 4–5).

## Out of scope (explicit non-goals)

- New `Recorder`/Prometheus metric for deploy retries (e.g. `deploy_retries_total`) — would
  expand the `port.Recorder` interface + all fakes for marginal value; retries are already
  WARN-logged and final failures already hit `SyncError("deploy")`. Revisit if observability
  needs it.
- Shared lock / serialization between reconcile and gitops loops (Approach C) — rejected,
  harms autoscaler latency.
- Changing `deployMaxAttempts` / backoff to be configurable — constant parity with
  `mutate.go` is sufficient; no flag/env needed.
