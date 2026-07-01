# Implementation Plan: Packaging & Deployment

Branch: main (git.enabled: true; git.create_branches: false — plan stays on the current branch)
Created: 2026-07-01

## Settings
- Testing: yes  # scoped: a `--version` unit test + hadolint Dockerfile lint in CI (this milestone is infra-heavy, not logic-heavy)
- Logging: verbose
- Docs: yes  # mandatory documentation checkpoint at completion (route via /aif-docs)

## Roadmap Linkage
Milestone: "Packaging & deployment"
Rationale: Milestone 11 (final) — ship the daemon as a small, hardened container image with a build-time version, a `docker stack deploy` example (direct socket + least-privilege socket-proxy), a GHCR release workflow, and a deployment guide.

## Current State (from reconnaissance)
Everything packaging-related is **absent**: no `Dockerfile`, `.dockerignore`, compose/stack files, release workflow, or `--version` flag. The build foundation is solid and ready:
- **Version embedding done**: `Makefile` builds with `-ldflags "-s -w -X main.version=$(VERSION)"`, `VERSION` from `git describe --tags --always --dirty`; `main.version` (cmd/swarm-hpa/main.go:19) is logged at startup and exposed as the `swarm_hpa` build-info metric label.
- **CGO-free**: no cgo imports → static cross-compile, small alpine/scratch image viable.
- **In-memory daemon**: no file writes, no state dir → safe for a **read-only rootfs** and a **non-root** user.
- **Graceful**: SIGINT/SIGTERM cancel the loop then drain the `/metrics` server (5s grace).
- **Runtime needs**: a Docker socket on a **manager** node; the adapter calls exactly `ServiceList`, `ServiceInspectWithRaw`, `ServiceUpdate`, `TaskList`, `NodeList` (the least-privilege boundary). Metrics on `:9095` (`METRICS_ADDR`), path `/metrics`. Docker client uses `client.FromEnv` (honors `DOCKER_HOST`).

## Decisions (from planning)
- **Final image base:** `alpine:3.20` (busybox shell for a `wget`-based HEALTHCHECK; small but with a real shell for ops).
- **Least-privilege:** ship **both** a simple direct-socket stack and a hardened `docker-socket-proxy` variant. Note the socket `:ro` caveat — a read-only bind of `/var/run/docker.sock` does **not** restrict API writes (`ServiceUpdate` still works); genuine least-privilege requires the proxy.
- **Release:** GitHub Actions workflow builds and pushes a multi-arch image to **GHCR** on `v*` tags.
- **Version at runtime:** add a `--version` flag so `docker run <image> --version` works without a socket.

## Scope & Non-Goals
**In scope:** `--version` flag; multi-stage `Dockerfile` (alpine final, non-root, healthcheck); `.dockerignore`; `make docker-build`/`docker-run`/`docker-push`; two Swarm stack examples (`deploy/stack.yml`, `deploy/stack.proxy.yml`); GHCR `release.yml` + a hadolint step in CI; `docs/deployment.md` wired into the docs set.

**Non-goals:** no Kubernetes/Helm; no goreleaser binary archives (image-only publish); no new runtime features or scaling/healing logic changes; the daemon's config surface is unchanged. Image owner path `ghcr.io/aleksey512/swarm-hpa` is a documented default and overridable via `IMAGE`.

## Architecture Notes
- Packaging lives entirely **outside** the Go layers: `Dockerfile`, `.dockerignore`, `deploy/*.yml`, `.github/workflows/*`, `Makefile`. The only source change is the `--version` flag in the composition root (`cmd/swarm-hpa`) — it must run **before** `config.Load`/Docker-client creation so it never needs a socket. `internal/core` is untouched (purity holds).
- The HEALTHCHECK uses the daemon's own `/metrics` endpoint (`:9095`) — the process is "healthy" when it is serving, which the best-effort metrics server already does.
- Reinforce the safety default in the image (`ENV DRY_RUN=true`) so a bare `docker run` never mutates a live Swarm.

## Commit Plan
<!-- 7 tasks; checkpoints below. git.enabled true; create_branches false (commits land on main). -->
- **Commit 1** (after tasks 58-60): `feat: --version flag + multi-stage Dockerfile + .dockerignore`
- **Commit 2** (after tasks 61-62): `build: docker make targets + Swarm stack examples (direct + socket-proxy)`
- **Commit 3** (after tasks 63-64): `ci: GHCR release workflow + hadolint; docs: deployment guide`

## Tasks

### Phase 1: CLI + image
- [x] Task 58: `--version`/`-v` flag — testable `versionString()` (version + go/os/arch), handled early in `run()` before config/Docker; plain stdout. `cmd/swarm-hpa/{main.go,version.go,version_test.go}`. (independent)
- [x] Task 59: Multi-stage `Dockerfile` — `golang:1.25-alpine` builder (`CGO_ENABLED=0`, `-trimpath`, `-ldflags -X main.version=$VERSION`, `ARG TARGETARCH`), `alpine:3.20` final with `ca-certificates`, non-root user, `EXPOSE 9095`, `ENV DRY_RUN=true`, `HEALTHCHECK` on `/metrics`, `ENTRYPOINT ["swarm-hpa"]`. `Dockerfile`. (independent)
- [x] Task 60: `.dockerignore` — drop `.git`/`.github`/`.ai-factory`/`docs`/`bin`/`deploy`/`*.md`; keep `go.mod`/`go.sum`/`cmd`/`internal`. `.dockerignore`. (independent)
<!-- Commit checkpoint: 58-60 -->

### Phase 2: Build + Swarm deploy
- [x] Task 61: Makefile docker targets — `IMAGE ?= ghcr.io/aleksey512/swarm-hpa`, `docker-build`/`docker-run`/`docker-push` reusing `VERSION`; update `.PHONY`/help. `Makefile`. (depends on 59)
- [x] Task 62: Swarm stack examples — `deploy/stack.yml` (manager placement, `read_only`, non-root, `cap_drop [ALL]`, `no-new-privileges`, env incl. `DRY_RUN=true`, socket `:ro` + caveat) and `deploy/stack.proxy.yml` (`tecnativa/docker-socket-proxy` with `SERVICES/TASKS/NODES/POST=1`, `DOCKER_HOST=tcp://docker-socket-proxy:2375`, no socket on the daemon). `deploy/stack.yml`, `deploy/stack.proxy.yml`. (depends on 59)
<!-- Commit checkpoint: 61-62 -->

### Phase 3: Release + docs
- [ ] Task 63: GHCR release workflow + hadolint — `.github/workflows/release.yml` (tags `v*` → buildx multi-arch `linux/amd64,linux/arm64` → push to `ghcr.io`, version from tag, `packages: write`); add a hadolint step (and optional push-less PR build) to `ci.yml`. `.github/workflows/{release.yml,ci.yml}`. (depends on 59)
- [ ] Task 64: `docs/deployment.md` — build/publish, `docker stack deploy`, least-privilege (socket `:ro` caveat + proxy, exact API surface), config link, hardening recap, upgrade/rollback; wire into README table + `development.md` nav + `AGENTS.md`. (depends on 61, 62, 63)
<!-- Commit checkpoint: 63-64 -->

## Definition of Done
- `docker build --build-arg VERSION=test -t swarm-hpa:test .` succeeds; `docker run --rm swarm-hpa:test --version` prints the version; the image runs as **non-root** with a working `/metrics` HEALTHCHECK.
- `make docker-build` tags the image with the git-derived `VERSION`; `.dockerignore` keeps the build context minimal.
- `docker stack config -c deploy/stack.yml` and `-c deploy/stack.proxy.yml` parse; both pin the daemon to `node.role == manager` and default to `DRY_RUN=true`.
- `release.yml` and the extended `ci.yml` are valid YAML; hadolint passes on the `Dockerfile`; `go build ./...`, `make lint`, and `make test-race` stay green (the `--version` flag has a unit test).
- `internal/core/*` remains pure (no new imports); the only Go change is the `--version` path in `cmd/swarm-hpa`, behavior-preserving for normal startup.
- `docs/deployment.md` exists, is wired into README/nav/AGENTS, and has no broken links.
- Documentation checkpoint run at completion (Docs: yes).
