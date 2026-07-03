# Plan: Autoscaler-aware GitOps stack sync (v0.4.0 foundation)

- **Branch:** none (`git.create_branches: false`) — work on `main`
- **Created:** 2026-07-03
- **Mode:** Full
- **Scope:** v0.4.0 milestones **1 (GitOps source & git sync)** + **2 (Stack rendering pipeline)** + **4 (Autoscaler-aware stack deploy)** — the "foundation slice": a working, conflict-free GitOps deploy end-to-end. Milestones 3 (SOPS/rotation), 5 (concurrency), 6 (status/UI/drift), 7 (migration docs) are follow-up plans.

## Settings

- **Testing:** yes — unit tests per adapter + integration test under `//go:build integration`
- **Logging:** verbose (DEBUG per stage, INFO decisions, WARN clamps/auth, ERROR failures)
- **Docs:** yes — mandatory docs checkpoint (Task 8) routed through `/aif-docs`
- **Config style:** drop-in `repos.yaml` / `stacks.yaml` compatibility (swarm-cd struct shapes), daemon flags/env for enable + tuning

## Roadmap Linkage

- **Milestones:** v0.4.0 → "GitOps source & git sync" + "Stack rendering pipeline" + "Autoscaler-aware stack deploy"
- **Rationale:** this plan delivers the working foundation that replaces the third-party swarm-cd dependency and resolves the swarm-cd↔HPA replicas conflict. Secrets/rotation, concurrency, status/UI/drift, and the cut-over guide land in subsequent plans. Do **not** mark any v0.4.0 milestone complete from this plan — that belongs to `/aif-implement` + `/aif-verify`.

## Design Context (decisions made during /aif-explore + planning)

1. **Conflict root cause.** swarm-cd runs `docker stack deploy` (full reapply from compose). `docker stack deploy` always sets `replicas` (compose value, default 1 when omitted — confirmed in `docker/cli@v27 cli/compose/convert/service.go:634`). So every swarm-cd tick clobbers the HPA's scale → oscillation + a capacity-loss window. "Just omit replicas" does **not** work.
2. **Fix lives in swarm-hpa** (not a swarm-cd fork). Folding GitOps into the same project as the autoscaler is the chosen architecture — one process owns all Swarm mutations.
3. **BUT folding alone doesn't prevent clobbering** if we still use `docker stack deploy` (full reapply). So the deploy adapter does **carry-forward**: before deploy, for each `swarm.autoscaler.enabled=true` service, rewrite `deploy.replicas` to the **live** replicas clamped to `[min,max]` (min/max read from the about-to-deploy compose labels, respecting in-flight Git changes). Non-autoscaled services stay compose-owned. This is cheap and correct here because the manager already tracks live state. → `docker stack deploy` becomes a no-op for autoscaled replicas; the conflict is gone.
4. **Future enhancement (out of scope):** a native granular `ServiceUpdate` deploy (field-level "don't write replicas") would eliminate carry-forward entirely. Carry-forward is isolated in `carryforward.go` so it's swappable later.
5. **Mutation channel.** A stack deploy is bulk and doesn't fit the per-service cooldown model of `reconciler/guard.go`. The gitops loop applies its own **dry-run gate** (logs intent + `Recorder.SyncSuppressed("dry_run")`) rather than routing through the `Guard`. Observability stays unified via the shared `Recorder`.
6. **Deps to add:** `github.com/go-git/go-git/v5` (git), `github.com/goccy/go-yaml` (compose/values — match swarm-cd), `github.com/docker/cli` v27 (stack deploy cobra command; coexists with the existing `docker/docker` SDK v28, as swarm-cd demonstrates — verify the build).

### Architecture (folds into existing Ports & Adapters)

```
cmd/swarm-hpa/main.go ── runManager (app.go)
   ├── reconciler.Run(ctx)          # existing: autoscale/heal/rebalance via Guard
   └── gitopsync.Loop.Run(ctx)      # NEW, opt-in (--gitops), same ctx/graceful-shutdown
          ├── git.GitSource         (adapter/git)      clone/open, auth, pull, revision
          ├── stackrender.Renderer  (adapter/stackrender) text/template{Values}+yaml
          └── stackdeploy.Deployer  (adapter/stackdeploy) carry-forward + docker stack deploy
```
New core ports (`internal/core/port`): `GitSource`, `StackRenderer`, `StackDeployer`. New model (`internal/core/model`): `RepoConfig`, `StackConfig`, `StackDef`. `Recorder` gains sync metrics. **Core stays pure** — no go-git/docker-cli/yaml imports under `internal/core`.

## Tasks

### Phase 0 — Foundations
- [x] **T1 — Core model + ports + recorder** *(done; deps added per-adapter: go-git/go-yaml with T2/T3, docker/cli with T4)*
  Add go-git/go-yaml/docker-cli deps. `internal/core/model/gitops.go`: `RepoConfig`, `StackConfig` (mirror swarm-cd for drop-in compat), `StackDef`. `internal/core/port/gitops.go`: `GitSource{Sync, ComposeBytes}`, `StackRenderer{Render}`, `StackDeployer{Deploy}`. Extend `Recorder` (`SyncRun`, `DeployApplied(stack)`, `SyncSuppressed(reason)`, `SyncError(stage)`, `LastRevision(stack,rev)`) + implement as prom metrics in `observability`. Compile-time `var _ port.X = (*Y)(nil)` proofs land with each adapter.

### Phase 1 — Adapters (milestones 1 & 2 & 4)
- [x] **T2 — git adapter (`GitSource`)** *(done; clone/open, basic auth, fetch+checkout, revision; race-clean)*
  `internal/adapter/git/`: go-git PlainClone/PlainOpen under `repos_path/<repo>`; per-repo `sync.Mutex`; HTTP basic auth (nil for public, `Password` or `PasswordFile`); force-checkout remote branch + pull; ignore `NoErrAlreadyUpToDate`; map "authentication required"→"failed"; `revision = Head().Hash()[:8]`; `ComposeBytes` reads `<repo>/<compose_file>`. Tests: tmpdir bare repo, auth-error mapping, already-up-to-date, per-repo lock serialization.
- [x] **T3 — stackrender adapter (`StackRenderer`)** *(done; text/template{Values}+goccy yaml, lenient for parity)*
  `internal/adapter/stackrender/`: optional `text/template` with `{"Values": valuesMap}` (swarm-cd contract — wrap in `Values`), `goccy/go-yaml` → `map[string]any`. Tests: plain parse, `{{.Values.x}}` substitution, missing-key error, invalid-YAML error.
- [x] **T4 — stackdeploy adapter (`StackDeployer`) — THE feature** *(done; carry-forward isolated + tested, docker/cli v28 deploy seam; dep-coexistence resolved)*
  `internal/adapter/stackdeploy/carryforward.go` (isolated, swappable): for each compose service with `swarm.autoscaler.enabled=true`, set `deploy.replicas = clamp(liveReplicas, min, max)` from live Swarm state + compose labels; non-autoscaled untouched; global skipped; absent→compose value kept. `deploy.go`: marshal adjusted map → tmp compose → `docker/cli stack.NewStackCommand` (`deploy --detach --with-registry-auth --resolve-image always|changed`), null output streams, `SilenceErrors/Usage`. pullPolicy global+per-stack (swarm-cd default `always`). Tests (stub SwarmController + a commandBuilder seam — no real Execute): carry-forward preserves live=7 over compose=3; clamp to [min,max]; plain stays 1; global untouched; absent keeps compose.

### Phase 2 — Loop & wiring
- [x] **T5 — gitopsync loop** *(done; injectable tick, dry-run gate, skip-on-unchanged + retry-on-error, per-stack recover; race-clean)*
  `internal/app/gitopsync/loop.go`: per tick, per stack (per-repo lock): `Sync`→record `SyncRun`+`LastRevision`; skip deploy if revision unchanged; `ComposeBytes`→`Render`; **dry-run gate** (log + `SyncSuppressed("dry_run")`, skip deploy); else `Deploy`→`DeployApplied`; `SyncError("deploy")` + continue on error; `recover()` per stack; injectable tick/clock for tests; honors ctx. Tests with port fakes: new-rev→deploy, unchanged→skip, dry-run→no-deploy, error→continues, ctx-cancel→returns.
- [x] **T6 — config + wire into runManager** *(done; --gitops flags/env, repos.yaml/stacks.yaml loader, swarm adapter StackServices, runManager opt-in loop; full suite green)*
  `internal/config`: `GitOpsEnabled` (`--gitops`), `GitOpsConfigsPath` (`CONFIGS_PATH`), `GitOpsReposPath`, `GitOpsInterval` (120s), `GitOpsPullPolicy` (`always`); load `repos.yaml`+`stacks.yaml` (goccy/go-yaml) into swarm-cd-compatible maps; validate. `cmd/swarm-hpa/app.go`/`main.go`: when enabled, build git/renderer/deployer + `gitopsync.Loop`, run in a goroutine alongside `reconciler` (same ctx); construct docker/cli `dockerCli` once for the deployer; startup INFO log of effective gitops config.

### Phase 3 — Hardening & docs
- [x] **T7 — integration test + goleak** *(done; real git+render+deployer carry-forward E2E, dry-run, goleak via TestMain; race-clean)*
  `internal/app/gitopsync/integration_test.go` (`//go:build integration`): tmpdir git repo with one autoscaled svc (live=7 via stubbed SwarmController) + one plain svc; capture deploy map via commandBuilder seam; assert autoscaled→7 (not 3), plain→1, global untouched; dry-run→deploy seam not called + `SyncSuppressed` recorded; `goleak.VerifyNone` after ctx cancel.
- [x] **T8 — docs checkpoint (mandatory) via `/aif-docs`** *(done; README section+table row, docs/gitops.md page, nav fixes, AGENTS.md + DESCRIPTION + ARCHITECTURE updated)*
  README "GitOps stack sync (autoscaler-aware)" section + example; docs/ page (config ref, carry-forward mechanism, swarm-cd migration sketch); DESCRIPTION feature/tech-stack update; ARCHITECTURE folder-diagram + new ports/packages + dry-run-gate note. Verify gofmt/vet/lint/test green.

## Commit Plan

Group logically; conventional-commit messages.

1. **After T1–T3** — `feat(gitops): core ports/model/recorder + git & stackrender adapters`
   Foundations + the two read-side adapters with unit tests.
2. **After T4–T5** — `feat(gitops): autoscaler-aware stack deploy + sync loop`
   The carry-forward deploy adapter (conflict resolution) + the control loop.
3. **After T6–T7** — `feat(gitops): wire manager --gitops + integration tests`
   Config/loading, manager wiring, end-to-end + leak coverage.
4. **After T8** — `docs(gitops): autoscaler-aware stack sync (replaces swarm-cd)`
   Docs checkpoint.

## Acceptance signals

- `./bin/swarm-hpa --gitops` syncs stacks from Git and deploys them; autoscaled services keep their HPA-set replica count across syncs (no clobber); plain services reconcile to Git.
- `--dry-run` (default) logs deploy intent without mutating.
- `/metrics` exposes `swarm_hpa_sync_total`, `swarm_hpa_deploys_total{stack}`, `swarm_hpa_sync_suppressed_total{reason}`, `swarm_hpa_git_revision{stack}`.
- `go test ./... -race` + `go test -tags integration ./...` green; `goleak` clean; `golangci-lint` clean.
- Docs checkpoint complete.

## Next step

Run `/aif-implement` to execute (it reads this plan + `/tasks`).
