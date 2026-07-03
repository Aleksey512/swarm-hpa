# Plan: GitOps concurrent scheduler — worker-pool sync + per-repo serialization (v0.4.0)

- **Branch:** none (`git.create_branches: false`) — work on `main`
- **Created:** 2026-07-03
- **Mode:** Full
- **Scope:** v0.4.0 milestone **"Concurrent scheduler & loop integration"**. Adds bounded worker-pool concurrency to the GitOps stack-sync loop with per-repo serialization, reaching swarm-cd parity for the concurrency model.

> **⚠️ Scope correction (read first).** The original milestone text mentions three things — worker-pool concurrency, a configurable `update_interval`, and integration alongside the autoscale/heal loop. Code review shows **two of the three already exist** (added by the foundation slice, commit `5c6f4f7`):
>
> - ✅ **`update_interval` already exists** as `GitOpsInterval` (`internal/config/config.go`: default `120s`, `--gitops-interval` / `GITOPS_INTERVAL`, validated `> 0`, in `LogValue`), already passed to `gitLoop.Run(ctx, cfg.GitOpsInterval)` in `cmd/swarm-hpa/main.go`.
> - ✅ **Loop integration already done** — the gitops loop runs in its own goroutine alongside the reconciler (`application.run(ctx)`), on the same `signal.NotifyContext` ctx (graceful shutdown), with its own dry-run gate (ARCHITECTURE principle 7) and **not** routed through the reconciler `Guard`.
>
> This plan therefore does **not** re-add the interval or re-do the integration. It implements the one genuinely missing piece — **worker-pool concurrency** — plus a new `--gitops-concurrency` knob, concurrency hardening (race + goleak + integration test), and docs. It completes the milestone.

## Settings

- **Testing:** yes — table-driven unit tests (concurrency bound, same-repo serialization, cross-repo parallelism, fault isolation, clamp) + an integration test under `//go:build integration`; `go test ./... -race` and `go test -tags integration ./... -race` must be green
- **Logging:** verbose (DEBUG per sync pass + per-stack repo-lock acquire; INFO startup with `concurrency`; **never log plaintext secrets** — unchanged)
- **Docs:** yes — mandatory docs checkpoint (M-T6) via `/aif-docs`
- **Config style:** drop-in addition to `config.go` (`GitOpsConcurrency`, `--gitops-concurrency` / `GITOPS_CONCURRENCY`, default `4`, `>= 1`)

## Roadmap Linkage

- **Milestone:** v0.4.0 → "Concurrent scheduler & loop integration"
- **Rationale:** the last missing sub-part of the milestone — swarm-cd syncs stacks with worker-pool concurrency and per-repo locking ("stacks on different repos proceed concurrently"). Today `syncAll` iterates stacks sequentially. Do **not** mark the milestone complete from this plan — that belongs to `/aif-implement` + `/aif-verify`. (The other two sub-parts — `update_interval` and loop integration — are already satisfied as noted above; this plan completes the milestone.)

## Design Context

1. **The real work is the worker pool.** `Loop.syncAll` (`internal/app/gitopsync/loop.go`) currently does `for _, st := range l.stacks { l.syncStack(ctx, st) }` — fully sequential. This plan replaces that with a bounded fan-out. The `update_interval` and "runs alongside the reconcile loop" properties are unchanged (already true).
2. **Shared per-repo worktree is the crux.** The git adapter (`internal/adapter/git/git.go`) keeps **one on-disk worktree per repo** at `reposPath/<repo>`, shared by every stack on that repo. Its per-repo `sync.Mutex` (`repoLock`) only guards `Sync`. The rest of `syncStack` — `ReadFile`, sops `Decrypt` (writes plaintext **in place**), `ApplyRotation` (reads via the worktree), `Deploy` — runs unlocked. Under sequential execution that is harmless; under a worker pool, two stacks on the same repo would interleave these steps and corrupt the shared worktree (e.g., one stack's decrypt overwriting files another stack is reading/deploying).
3. **Per-repo serialization must span the whole `syncStack`.** Therefore the `Loop` gains its own per-repo lock map (`repoLocks`, mirroring the adapter's pattern) and `syncStack` holds that lock for its **entire** body. Net effect: stacks on **different** repos parallelize up to the pool size; stacks on the **same** repo serialize end-to-end — exactly swarm-cd's model. The adapter's existing lock is left in place as defense-in-depth (the adapter must stay correct for any caller, not assume the loop holds a lock); `git.go` is **not** modified.
4. **Fault isolation is preserved.** One stack's failure or panic must never cancel the others. So we use plain goroutines + a counting semaphore + `sync.WaitGroup` — **not** `errgroup` with cancel-on-first-error. Each `syncStack` already `recover()`s and records via the shared `Recorder`. No new dependency.
5. **Already concurrency-safe — leave as-is.** `lastDeployedRev` / `lastDeployedOK` are guarded by `l.mu` (`unchangedSinceLastSuccess`, `markDeploy`). The shared `Recorder` uses goroutine-safe prometheus primitives. The git adapter's `repoLock`/`repoLocks` map is mutex-guarded. No changes needed there — only confirm in review.
6. **New config knob.** `GitOpsConcurrency` (default `4`, `--gitops-concurrency` / `GITOPS_CONCURRENCY`, validated `>= 1`). The loop clamps `< 1` to `1` defensively so a misconfiguration can never panic.

## Tasks

### Phase 0 — Config
- [x] **M-T1 — Config: `GitOpsConcurrency` field** *(done; default 4, --gitops-concurrency/GITOPS_CONCURRENCY, validated >= 1 inside GitOpsEnabled block, LogValue; config test green -race)*
  Add `GitOpsConcurrency int` to `Config` in `internal/config/config.go`, mirroring the `GitOpsInterval` pattern: default `4`; env `GITOPS_CONCURRENCY` (atoi + error wrap); flag `--gitops-concurrency`; validation `>= 1`; `slog.Int("gitops_concurrency", ...)` in `LogValue`. Do **not** re-add `GitOpsInterval` (it exists). Tests: table-driven config test (default, env, flag, `< 1` error). `#blocked-by: none`

### Phase 1 — Loop
- [x] **M-T2 — Worker-pool concurrency + per-repo serialization in loop** *(done; bounded semaphore+WaitGroup in syncAll, per-repo lock spans whole syncStack, New += concurrency clamped >=1, adapter untouched)*
  In `internal/app/gitopsync/loop.go`: (1) add `repoLocks map[string]*sync.Mutex` (+ lazy allocator) to `Loop`, init in `New`; (2) in `syncStack`, acquire the per-repo lock for `st.Repo` and hold it via `defer` for the **entire** body (Sync→ReadFile→Render→Decrypt→Rotate→Deploy), with the existing `recover()` remaining outermost; (3) rewrite `syncAll` as a bounded fan-out — `sem := make(chan struct{}, l.concurrency)` + `sync.WaitGroup`, one goroutine per stack, `wg.Wait()` before return, **no errgroup / no cancel-on-error**, keep the single `defer l.recorder.SyncRun()` per pass; (4) add `concurrency int` to `New` after `autoRotate bool`, before `logger` (clamp `< 1` → `1`); (5) startup INFO log += `"concurrency"`, DEBUG log at `syncAll` start. Do **not** touch `internal/adapter/git/git.go`. `#blocked-by: M-T1`
- [x] **M-T3 — Unit tests for concurrency + update `New` call sites** *(done; fakeGit made goroutine-safe, 6 New call sites updated, trackingDeployer + 4 concurrency tests added; -race x3 green, golangci-lint 0 issues)*
  In `internal/app/gitopsync/loop_test.go`: update every `New(...)` call for the new `concurrency` arg (existing tests pass `1`); add tests — concurrency bound (peak in-flight ≤ N; =1 serial), same-repo serialization (no overlap), cross-repo parallelism (overlap), one-failure-does-not-stop-others, `New` clamp (`0`/`-1` → serial). Extend fakes with an enter/exit hook for overlap detection, keep them mutex-guarded. `go test ./internal/app/gitopsync/ -race` + goleak green. `#blocked-by: M-T2`

### Phase 2 — Wiring + integration
- [x] **M-T4 — Wire `GitOpsConcurrency` into `main.go` + verify build** *(done; cfg.GitOpsConcurrency threaded into New + startup log; go build ./... green, only 1 prod call site)*
  In `cmd/swarm-hpa/main.go` gitops block: pass `cfg.GitOpsConcurrency` to `gitopsync.New(...)` (new arg slot) and add `"concurrency"` to the `"gitops enabled"` INFO log. Update any other `New` call site. Verify `go build ./...`, `go vet ./...`, `go test ./... -race`, `golangci-lint run` green. `#blocked-by: M-T2` (and M-T3 for a green `-race` run)
- [x] **M-T5 — Integration test: parallel sync across repos (`//go:build integration`)** *(done; 3 stacks/2 repos via real git+renderer+carry-forward; asserts same-repo serialize, cross-repo parallel, carry-forward under concurrency, concurrency=1 serial; -tags integration -race x3 green)*
  In `internal/app/gitopsync/integration_test.go`: seed two real git repos — R1 backing two stacks (same-repo serialization), R2 backing one stack (cross-repo parallelism) — using the existing real-git + real-renderer + capture-deployer harness. Assert: same-repo stacks never overlap across the full Sync→Deploy window; an R1 stack overlaps the R2 stack (parallel); carry-forward still preserves autoscaled replicas under `concurrency >= 2`; `concurrency=1` = fully serial; goleak + `-tags integration -race` green. Update integration `New` call sites for the new arg. `#blocked-by: M-T3, M-T4`

### Phase 3 — Docs
- [x] **M-T6 — Docs checkpoint (mandatory) via `/aif-docs`** *(done; docs/gitops.md Concurrency section + config table row + migration-note fix; DESCRIPTION + ARCHITECTURE principle 7 deltas; README/AGENTS unchanged — no gitops knobs / no new folders)*
  `docs/gitops.md` += "Concurrency" subsection (worker pool, `--gitops-concurrency` default 4, per-repo serialization, shared-worktree rationale, `--gitops-interval` is the separate cadence) + config-table entry + migration one-liner; README one-liner only if it lists gitops knobs; DESCRIPTION feature delta; ARCHITECTURE principle 7 += one sentence (bounded pool, same-repo serialize, no new folder); AGENTS.md only if its knob/structure map is now incomplete. Do **not** edit RULES.md. `#blocked-by: M-T5`

## Commit Plan

1. **After M-T1–M-T3** — `feat(gitops): worker-pool sync concurrency with per-repo serialization` *(config + loop + unit tests — one coherent feature slice)*
2. **After M-T4–M-T5** — `test(gitops): concurrency integration test` *(wiring + integration test)*
3. **After M-T6** — `docs(gitops): document sync concurrency`

## Acceptance signals

- Stacks on different repos sync in parallel (bounded by `--gitops-concurrency`); stacks sharing a repo serialize end-to-end (verified by an overlap detector in both unit and integration tests).
- One stack's failure/panic never stops the other stacks and never cancels in-flight syncs (no errgroup cancel-on-error).
- Carry-forward still preserves autoscaled replicas under concurrent deploys (no M3/regression).
- `--gitops-concurrency=1` reproduces today's fully-sequential behavior; `0`/`-1` clamp to `1` (no panic).
- `go test ./... -race` + `go test -tags integration ./... -race` green; `goleak` clean; `golangci-lint` clean.
- The existing `--gitops-interval` (update cadence) and the loop's separate-goroutine integration with the reconcile loop are **unchanged**.
- Docs checkpoint complete.

## Next step

Run `/aif-implement` to execute (reads this plan + `/tasks`). **Recommend `/clear` first** — this session is long; the plan + ROADMAP persist.
