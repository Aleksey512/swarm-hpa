# Implementation Plan: First Release (v0.1.0) + Docker Hub Image

Branch: none (git.create_branches=false — work happens on `main`)
Created: 2026-07-01

## Summary

All 11 ROADMAP milestones are complete and there are no git tags yet. This plan
cuts the **first release, `v0.1.0`**, and makes the release pipeline publish the
multi-arch image to **both GHCR (primary) and Docker Hub** (`docker.io/mrframe/swarm-hpa`).

Decisions (confirmed with user):
- **Version:** `v0.1.0` (conservative first cut; room for breaking changes pre-1.0).
- **Docker Hub target:** `docker.io/mrframe/swarm-hpa` (login `mrframe`).
- **Registry strategy:** dual-publish; **GHCR stays the default** in docs / deploy
  stacks / Makefile, Docker Hub is a mirror/alternative.
- **Docs + release check:** update docs & deploy examples, document required
  secrets, add post-release image verification.

The daemon code is untouched — this is a CI / packaging / docs / release-cut change.

## Settings
- Testing: no (no Go code changes; validation = `actionlint` on the workflow + the
  existing CI `docker-build` smoke job + post-release `docker run --version` check)
- Logging: n/a (workflow YAML, Makefile, docs, deploy manifests — no runtime code).
  Requirement instead: keep Actions step names descriptive and verify both registry
  pushes appear in the release run log.
- Docs: yes — mandatory docs update (deployment.md, README, deploy stacks).

## Roadmap Linkage
Milestone: "Packaging & deployment"
Rationale: This operationalizes the completed packaging milestone by cutting the
first tagged release and extending the publish target to Docker Hub; no new roadmap
milestone is required (all are already checked).

## Prerequisites (manual, before Task 6)

Docker Hub credentials must exist as GitHub Actions secrets or the Docker Hub push
step in the release workflow will fail:
- Docker Hub repo `mrframe/swarm-hpa` created (or allowed to auto-create on push).
- Docker Hub access token (Read & Write).
- GitHub secrets `DOCKERHUB_USERNAME=mrframe` and `DOCKERHUB_TOKEN=<token>` set.

See **Task 1** for the exact steps. The AI implementer must not handle the raw token —
the user runs the `gh secret set` / Docker Hub UI steps.

## Commit Plan
- **Commit 1** (after tasks 2-3): `ci: publish release image to Docker Hub alongside GHCR`
  — release.yml dual-registry + Makefile Docker Hub override note.
- **Commit 2** (after tasks 4-5): `docs: document dual GHCR/Docker Hub publishing and release verification`
  — docs/deployment.md, README.md, deploy/stack*.yml.
- **Release** (task 6): no source commit — an annotated `v0.1.0` tag pushed to `main`
  triggers the release workflow. (Task 1 is external config: GitHub secrets, no commit.)

## Tasks

### Phase 1: Prerequisites (manual, user-owned)
- [ ] Task 1: Configure Docker Hub credentials & GitHub secrets — create the
  `mrframe/swarm-hpa` repo + a Read/Write access token, set `DOCKERHUB_USERNAME`
  and `DOCKERHUB_TOKEN` GitHub Actions secrets (`gh secret set ...`). DoD:
  `gh secret list` shows both. No files changed (external config).

### Phase 2: CI / build wiring
- [x] Task 2: Extend `.github/workflows/release.yml` to dual-publish — add a Docker Hub
  `docker/login-action@v3` step and add `docker.io/mrframe/swarm-hpa` to the
  `docker/metadata-action` `images:` list (GHCR stays first). Existing tag rules
  (`{{version}}`, `{{major}}.{{minor}}`, `latest`) fan out to both registries; one
  build, two pushes. Validate with `actionlint` if available.
- [x] Task 3: Document the Docker Hub `IMAGE=` override in `Makefile` (comment near
  `IMAGE ?= ghcr.io/aleksey512/swarm-hpa`). GHCR default unchanged; the existing
  `IMAGE=...` override already reaches docker-build/run/push.
<!-- Commit checkpoint: tasks 2-3 → Commit 1 -->

### Phase 3: Docs & deploy
- [x] Task 4: Update `docs/deployment.md` + `README.md` (depends on 2) — describe
  dual GHCR/Docker Hub publishing, the required `DOCKERHUB_*` secrets, the
  `git tag v0.1.0` → both-registry mapping, and a "Verify the release"
  (`docker pull ... && docker run --rm ... --version`) snippet. GHCR remains the
  default in deploy/upgrade examples.
- [x] Task 5: Add the Docker Hub image alternative to `deploy/stack.yml` +
  `deploy/stack.proxy.yml` (depends on 2) — second commented
  `IMAGE=docker.io/mrframe/swarm-hpa` deploy example in each header; default
  `${IMAGE:-ghcr.io/aleksey512/swarm-hpa}` untouched.
<!-- Commit checkpoint: tasks 4-5 → Commit 2 -->

### Phase 4: Release cut
- [x] Task 6: Cut `v0.1.0` (depends on 1, 2, 3, 4, 5) — **outward-facing; confirm with
  the user before pushing the tag.** Pre-flight (secrets set, changes on `main`, CI
  green, no existing `v0.1.0`), then `git tag -a v0.1.0` + `git push origin v0.1.0`,
  watch the release workflow, and verify `:0.1.0` / `:0.1` / `:latest` on both
  `ghcr.io/aleksey512/swarm-hpa` and `docker.io/mrframe/swarm-hpa` via
  `docker run --rm <img>:0.1.0 --version`. Optional: `gh release create v0.1.0
  --generate-notes`.
<!-- Commit checkpoint: task 6 → tag push (no source commit) -->

## Risks & Notes
- **Secrets missing → release fails.** If `DOCKERHUB_*` secrets are absent when the
  tag is pushed, the Docker Hub login step fails and the whole workflow fails (GHCR
  push may or may not have run first depending on step order). Order the Docker Hub
  login before build so the failure is early and clean; optionally guard it with
  `if: ${{ secrets.DOCKERHUB_USERNAME != '' }}` for fork-safety.
- **`latest` on a 0.x tag.** `type=raw,value=latest` tags v0.1.0 as `:latest` on both
  registries — intended for a first release. Revisit if a pre-release channel is
  wanted later.
- **Tag is hard to unpublish.** Pushed public images can be pulled immediately; treat
  Task 6 as a point of no easy return. Delete-tag + re-tag only fixes the tag, not
  already-pulled images.
- **GHCR owner vs Docker Hub login differ** (`aleksey512` vs `mrframe`) — this is
  intentional; keep the two image paths distinct everywhere.
