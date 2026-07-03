# Plan: GitOps SOPS secrets + config/secret rotation (v0.4.0 M3)

- **Branch:** none (`git.create_branches: false`) — work on `main`
- **Created:** 2026-07-03
- **Mode:** Full
- **Scope:** v0.4.0 milestone **3 (SOPS secrets + config/secret rotation)**. Extends the gitopsync deploy pipeline to reach swarm-cd parity for the secret/config lifecycle.

## Settings

- **Testing:** yes — unit tests per adapter/stage + integration test under `//go:build integration`
- **Logging:** verbose (DEBUG per stage, INFO decisions, WARN decrypt/rotate failures; **never log plaintext secrets**)
- **Docs:** yes — mandatory docs checkpoint (Task 7) via `/aif-docs`
- **Config style:** drop-in `stacks.yaml` (`sops_files`, `sops_secrets_discovery`) + global `--gitops-auto-rotate` flag/env

## Roadmap Linkage

- **Milestone:** v0.4.0 → "SOPS secrets + config/secret rotation"
- **Rationale:** completes the deploy-a-real-stack story — without SOPS/rotation, encrypted-in-repo secrets and content-versioned configs/secrets (which swarm-cd users rely on) can't be managed. Builds directly on the foundation slice (commit `5c6f4f7`). Do **not** mark M3 complete from this plan — that belongs to `/aif-implement` + `/aif-verify`.

## Design Context

1. **Pipeline extension.** The current loop is `Sync → Render → (dry-run) → Deploy (carry-forward + docker stack deploy)`. M3 inserts two stages after Render and before Deploy:
   `Sync → Render → **Decrypt sops** → **Rotate configs/secrets** → (dry-run) → Deploy`.
2. **SOPS decrypt is in-place on disk** (parity with swarm-cd `util.DecryptFile`): the sops-encrypted files referenced by the compose are overwritten with plaintext in the repo worktree under `repos_path`. Backends are chosen by the **sops library** from env (`SOPS_AGE_KEY_FILE`, `SOPS_GPG_PRIVATE_KEY_FILE`/`SOPS_GPG_PRIVATE_KEY`) — we never parse those envs. **Security note:** this writes plaintext secrets to the (ephemeral) worktree on disk; document it and recommend `repos_path` on ephemeral storage. Never log secret contents.
3. **Rotation by content hash** (parity with swarm-cd `rotateObjects`): for compose `configs:` / `secrets:` objects with a `file:`, read the (now-decrypted) content, md5 → first 8 hex, rename the object `<stack>-<name>-<hash>`. Swarm configs/secrets are immutable, so a changed hash = a new object name = Swarm re-deploys with the new content.
4. **Dry-run skips prepare.** Decrypt writes plaintext to disk (a side effect), so in dry-run the loop logs intent and skips decrypt+rotate+deploy (unchanged from today's dry-run = log only).
5. **Worktree-path seam.** Decrypt needs on-disk paths, so `GitSource` gains `WorktreePath(stack)` (= `repos_path/<repo>`). Rotation reuses the existing `ReadFile` (content reads) via an injected resolver, keeping the transform pure/testable.
6. **Dep to add:** `github.com/getsops/sops/v3` (verify build — sops/v3 is self-contained, low skew risk vs the docker/docker stack).

## Tasks

### Phase 0 — Foundations
- [x] **M3-T1 — Config + model for SOPS/discovery/auto-rotate** *(done; StackConfig.SopsFiles/Discovery, stacks.yaml parse, --gitops-auto-rotate default true; tests green)*
  `model.StackConfig` += `SopsFiles []string`, `SopsSecretsDiscovery bool`. `config/gitops.go` parses `sops_files`/`sops_secrets_discovery` from `stacks.yaml`. `config/config.go` adds global `GitOpsAutoRotate` (`--gitops-auto-rotate`/`GITOPS_AUTO_ROTATE`, **default true**) + validation + LogValue. Files: `internal/core/model/gitops.go`, `internal/config/gitops.go`, `internal/config/config.go`.
- [x] **M3-T2 — SOPS decrypt adapter** *(done; port.SecretDecrypter + adapter/sops via decrypt.File seam; format-from-ext, in-place overwrite, stops on first error; race-clean)*
  New dep `getsops/sops/v3`. Port `SecretDecrypter{Decrypt(ctx, worktree, files)}` in `core/port`. Adapter `internal/adapter/sops/` (age+gpg via sops-library env, in-place decrypt). Tests: age-key fixture → plaintext; missing-key → error; no plaintext in logs.
- [x] **M3-T3 — Discovery + rotation transforms** *(done; fileObjects/DiscoverSecretFiles + ApplyRotation md5→name; pure via injected resolver; race-clean; docker/cli re-pinned v28.5.2 after tidy bumped it to v29)*
  `internal/adapter/stackdeploy/discovery.go` (`getFileObjects`, `DiscoverSecretFiles`) + `rotation.go` (`RotateObjects` / `ApplyRotation` — md5 content → `<stack>-<name>-<hash>` rename; pure via injected file resolver). Tests: discovery (map+list forms), rotation rename + stable/changed hash, non-file objects untouched.

### Phase 1 — Pipeline + wiring
- [ ] **M3-T4 — Integrate decrypt+rotation into the loop** *(blocked by: T2, T3)*
  `port.GitSource.WorktreePath(stack)` + git adapter impl. `loop.go`: after Render, resolve sops files (discovery OR `SopsFiles`), `Decrypt` then `ApplyRotation` (when `autoRotate`), each error → `SyncError` + continue; dry-run skips prepare (logs intent). `New` gains `sopsDecrypter` + `autoRotate` params. Files: `core/port/gitops.go`, `adapter/git/git.go`, `app/gitopsync/loop.go`, `loop_test.go`.
- [ ] **M3-T5 — Wire into runManager** *(blocked by: T4)*
  `cmd/swarm-hpa/main.go` (--gitops block): `sops.New(logger)` + pass `autoRotate` into `gitopsync.New`; startup INFO log (sops stacks count, auto_rotate). Finalize `New` signature + update tests. Verify build+`-race`.

### Phase 2 — Hardening & docs
- [ ] **M3-T6 — Integration test + goleak** *(blocked by: T5)*
  `integration_test.go`: seed repo with a file-backed secret that is age-encrypted; assert decrypt→rotate→carry-forward produces a `<stack>-<name>-<hash>` rename whose hash = md5(plaintext)[:8], carry-forward still preserves autoscaled replicas, and `autoRotate=false` skips rename. goleak (TestMain) green. Fixture under `internal/adapter/sops/testdata/` if needed.
- [ ] **M3-T7 — Docs checkpoint (mandatory) via `/aif-docs`** *(blocked by: T6)*
  `docs/gitops.md` += "Secrets (SOPS)" + "Config/secret rotation" subsections + updated config tables + migration sketch; README one-liner; DESCRIPTION tech-stack (sops/v3) + feature; ARCHITECTURE tree (`adapter/sops/`, `SecretDecrypter`, extended pipeline in principle 7); AGENTS.md structure map.

## Commit Plan

1. **After T1–T3** — `feat(gitops): sops decrypt + config/secret rotation transforms`
2. **After T4–T5** — `feat(gitops): wire sops+rotation into the sync pipeline`
3. **After T6** — `test(gitops): sops+rotation integration`
4. **After T7** — `docs(gitops): sops secrets + rotation`

## Acceptance signals

- A stack with sops-encrypted secrets decrypts (age/gpg) and deploys; its configs/secrets rotate by content hash so Swarm picks up changes; non-secret services unaffected; carry-forward still preserves autoscaled replicas (no M3 regression).
- `auto_rotate=false` disables rotation; no `sops_files`/discovery disables decrypt.
- `--dry-run` logs intent without decrypting (no plaintext on disk) or deploying.
- `go test ./... -race` + `go test -tags integration ./...` green; `goleak` clean; `golangci-lint` clean.
- Docs checkpoint complete.

## Next step

Run `/aif-implement` to execute (reads this plan + `/tasks`). **Recommend `/clear` first** — the session is long; the plan + ROADMAP persist.
