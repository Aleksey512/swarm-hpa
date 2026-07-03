# Plan: swarm-cd migration & docs (v0.4.0 — closing milestone)

- **Branch:** none (`git.create_branches: false`) — work on `main`
- **Created:** 2026-07-03
- **Mode:** Full
- **Scope:** v0.4.0 milestone **"swarm-cd migration & docs"** (the final v0.4.0 item). Documents a documented, reversible cut-over from `m-adawi/swarm-cd` and locks the drop-in config-compat claim with a test. Mostly docs + one small test.

> **Already done (by the GitOps arc):** `docs/gitops.md` has a 20-line "Migrating from swarm-cd" sketch; `config.LoadGitOps` reads `repos.yaml`/`stacks.yaml` (drop-in shapes); `deploy/stack.yml` deploys swarm-hpa itself. This plan turns the sketch into a real guide, proves the drop-in claim, and wires it in — closing the milestone.

## Settings

- **Testing:** yes — one config drop-in compat test (MIG-T2); no production code, so no logging
- **Logging:** N/A — docs + one test; no new production code paths
- **Docs:** yes — this milestone IS the docs (the deliverable is the migration guide)
- **Decisions (confirmed):** dedicated guide page `docs/migrating-from-swarm-cd.md`; add a config-compat test; reference the existing `deploy/stack.yml` (no new deploy file)

## Roadmap Linkage

- **Milestone:** v0.4.0 → "swarm-cd migration & docs"
- **Rationale:** the closing v0.4.0 milestone — makes the move from swarm-cd documented and reversible, completing GitOps parity. After this, all 19 roadmap milestones are done. Do **not** mark the milestone complete from this plan — that belongs to `/aif-implement` + `/aif-verify`.

## Design Context

1. **Docs-led milestone.** No new modules, no production code, no flags. The deliverables are a migration guide, a config-compat test, and doc wiring.
2. **Drop-in is the key claim.** swarm-cd users keep their `repos.yaml` + `stacks.yaml` verbatim. MIG-T2 makes that claim executable: a swarm-cd-style yaml must parse into swarm-hpa's config with the expected field mapping. If a future refactor breaks it, the test fails.
3. **One guide page, not two.** The detailed cut-over lives in `docs/migrating-from-swarm-cd.md`; `docs/gitops.md`'s sketch becomes a short pointer to it (avoid duplicate maintenance).
4. **Deploy example = existing `deploy/stack.yml`.** It already deploys swarm-hpa (where swarm-cd ran). No new example file — the guide references it.
5. **Honesty in the parity matrix.** Call out what swarm-hpa does NOT yet have vs swarm-cd (e.g. SSH auth is HTTP-basic-only so far) so migrants aren't surprised.

## Tasks

- [x] **MIG-T1 — Migration guide: `docs/migrating-from-swarm-cd.md`** *(done; why-migrate, parity matrix w/ honest gaps, config mapping tables, 7-step cut-over, rollback, see-also)*
  Create the page (via `/aif-docs` or directly): Why migrate; feature parity matrix (incl. gaps); config mapping table (repos.yaml/stacks.yaml fields → swarm-hpa); numbered cut-over (stop swarm-cd → point swarm-hpa at the same config → SOPS env → dry-run confirm via logs + `GET /stacks` → disable dry-run → watch `/metrics`); deploy example = `deploy/stack.yml`; rollback; See Also. `#blocked-by: none`
- [x] **MIG-T2 — Config drop-in compat test** *(done; TestLoadGitOps_SwarmCDCompat — swarm-cd-style repos.yaml/stacks.yaml parses, all fields map; public-repo case; -race green)*
  `internal/config/config_test.go`: `TestLoadGitOps_SwarmCDCompat` — seed a swarm-cd-style `repos.yaml` (private repo: url+username+password_file) + `stacks.yaml` (repo/branch/compose_file/values_file/sops_files/sops_secrets_discovery), assert `LoadGitOps` parses + every field maps; plus the public-repo (no-auth) case. Executable drop-in guarantee. `#blocked-by: none`
- [x] **MIG-T3 — Wire the guide into README + gitops.md** *(done; README docs-table row + "sketch"→"guide" link; gitops.md sketch → pointer + v0.4.0 blockquote updated)*
  `README.md`: add the guide to the docs table; change "migration sketch" → "migration guide" + link. `docs/gitops.md`: replace the 20-line sketch with a short pointer to the new page; update the v0.4.0 "so far covers" blockquote to include the migration guide (drops the "follow-ups" framing). `#blocked-by: MIG-T1`

## Commit Plan

Single commit (3 tasks, docs-led + one test):

- After MIG-T1–MIG-T3 — `docs(gitops): swarm-cd migration guide + drop-in compat test`

## Acceptance signals

- A swarm-cd operator can follow `docs/migrating-from-swarm-cd.md` end-to-end: stop swarm-cd, reuse their existing `repos.yaml`/`stacks.yaml`, dry-run, then cut over — and roll back by reversing.
- The drop-in claim is backed by a passing test: a swarm-cd-style yaml parses with the documented field mapping.
- The guide is discoverable from the README docs table and from `docs/gitops.md`.
- The parity matrix honestly notes gaps (e.g. SSH auth).
- `go test ./... -race` green; no production code changed.
- ROADMAP "swarm-cd migration & docs" can be marked `[x]` after `/aif-verify`.

## Next step

Run `/aif-implement` to execute (reads this plan + `/tasks`). **Strongly recommend `/clear` first** — this session has already delivered two milestones (concurrency + status/drift/UI); the plan + roadmap persist across `/clear`.
