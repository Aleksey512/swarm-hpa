# Implementation Plan: Examples & Demo Workloads

Branch: main (git.enabled: true; git.create_branches: false — plan stays on the current branch)
Created: 2026-07-01

## Settings
- Testing: yes  # scoped: `docker stack config` parse of every example stack via `make examples-validate` + a CI job. Examples are YAML/shell/docs — no Go logic to unit-test.
- Logging: verbose  # No daemon code changes. "Logging" here means: shell scripts echo their actions, and every stack/README documents the exact daemon log lines the user should expect.
- Docs: yes  # mandatory documentation checkpoint at completion (route via /aif-docs)

## Roadmap Linkage
Milestone: "none"
Rationale: Skipped by user — the roadmap's 11 milestones are all complete; examples/ is a post-roadmap onboarding/docs addition, and ROADMAP.md is owned by /aif-roadmap (not edited here).

## Problem / Motivation
The daemon ships with `deploy/stack.yml` + `deploy/stack.proxy.yml`, but those only deploy **the daemon itself**. There is no runnable example of a **target workload** opting into autoscaling/healing via the `swarm.autoscaler.*` labels. The docs describe the labels; nothing lets a user `docker stack deploy` a working demo and watch the daemon decide. This plan adds a self-contained `examples/` tree covering the three capabilities (CPU autoscale, Prometheus autoscale, stuck-task healer) plus a walkthrough README, docs wiring, and stack validation.

## Current State (from reconnaissance)
- No `examples/` directory. `deploy/` has only `stack.yml` (direct socket) and `stack.proxy.yml` (socket-proxy) for the daemon.
- Docs are in place: `docs/{getting-started,configuration,metrics-providers,observability,deployment,development}.md`, all cross-linked with a `[← Prev] · [Back to README] · [Next →]` breadcrumb.
- `Makefile` uses the `## `-comment help convention; `make help` prints a `%-12s` column. `docker stack config` is already the DoD parse-check in the packaging plan.
- CI (`.github/workflows/ci.yml`) has three jobs: `build-test`, `dockerfile-lint`, `docker-build` (ubuntu-latest, Docker available), `permissions: contents: read`.

## Key Correctness Facts (verified in source — bake these into every example)
1. **Managed = opt-in + valid policy.** `adapter/swarm` lists only services with `swarm.autoscaler.enabled=true` (server-side filter), and `config.ParsePolicy` requires **valid** `min`, `max`, `metric`, `target` — otherwise the service is skipped (logged). Every example service (including the **healer** one) must carry the full valid policy label set.
2. **Labels are SERVICE-level.** The daemon reads `svc.Spec.Labels`. In stack files that means `deploy.labels`, **not** the top-level container `labels`. This is the single most common mistake — call it out in comments everywhere.
3. **Placement constraints** come from `svc.Spec.TaskTemplate.Placement.Constraints` → `deploy.placement.constraints` in stack files. The healer signature needs a constraint present.
4. **Healer signature** (docs/configuration.md): placement constraints present **AND** a task `pending` ≥ `--heal-threshold` (while Swarm wants it running) **AND** a constraint-satisfying node is now Active+Ready. Healing is gated by dry-run + `--cooldown`.
5. **Prometheus path:** `source=prometheus` + `query` (PromQL). `$SERVICE`/`$SERVICE_ID` are expanded; the result must reduce to a scalar/single series or the tick is skipped. `metric` is just a logical name. Target semantics are per-replica: `desired = clamp(current × value/target, min, max)`. The daemon needs `--prometheus-url` even if the global `--metrics-provider` stays `dockerstats`.
6. **dockerstats is local-node only** — on a multi-node swarm the daemon sees only tasks on its node; the CPU demo is most reliable on a single-node lab.

## Decisions (from planning)
- **Scope:** full set — `cpu-autoscale/` (+ loadgen), `prometheus-autoscale/` (+ prometheus.yml), `healer/`, and a walkthrough `examples/README.md`.
- **DRY daemon:** examples deploy the **target workloads** (and Prometheus where needed); the daemon is run once alongside (local `make run` or in-cluster `deploy/stack.yml`). The README documents the exact flags per example. This avoids duplicating the daemon config three times and teaches the real pattern (labels on the workload, daemon deployed once).
- **Validation = parse-only.** `make examples-validate` runs `docker stack config -c` per stack (no deploy, no live swarm needed). CI runs the same target. Optional best-effort `promtool`/`shellcheck` guarded by `command -v`.
- **Recommended images** (implementer verifies availability): CPU demo target = a small HTTP image that does per-request work (e.g. `containous/whoami` or `nginx`) with a low CPU `target` so modest load crosses it; Prometheus demo app = one that exposes `http_requests_total` (e.g. `quay.io/brancz/prometheus-example-app`); Prometheus = `prom/prometheus`; healer = `nginx:alpine`.

## Scope & Non-Goals
**In scope:** `examples/{cpu-autoscale,prometheus-autoscale,healer}/` stacks + supporting files (loadgen.sh, prometheus.yml); `examples/README.md` walkthrough; docs wiring (README table, getting-started, metrics-providers/configuration See-Also, AGENTS.md); `make examples-validate`; a CI validation step.

**Non-goals:** no changes to daemon behavior or the Go source (examples are YAML/shell/docs only); no new labels or flags; no Kubernetes/Helm; no bespoke demo application source (public images only); no `docker stack deploy` in CI (parse-only — CI has no live swarm).

## Architecture Notes
Everything lives **outside** the Go layers (`examples/`, `Makefile`, `.github/workflows/ci.yml`, plus doc files). `internal/core` purity and all daemon behavior are untouched — there are **zero** source changes. The examples are validated against the daemon's real contract (opt-in labels under `deploy.labels`, placement constraints, PromQL routing) as verified in reconnaissance.

## Commit Plan
<!-- 9 tasks; checkpoints every ~3. git.enabled true; create_branches false (commits land on main). -->
- **Commit 1** (after tasks 65-66): `docs: cpu-autoscale example (dockerstats stack + loadgen)`
- **Commit 2** (after tasks 67-69): `docs: prometheus-autoscale + healer examples`
- **Commit 3** (after tasks 70-73): `docs: examples walkthrough + wiring; build: examples-validate target + CI`

## Tasks

### Phase 1: CPU autoscaling example (dockerstats)
- [x] Task 65: `examples/cpu-autoscale/stack.yml` — demo target service (small HTTP image, per-request CPU work) with the full opt-in policy under `deploy.labels` (`enabled=true`, `min=2`, `max=8`, `metric=cpu`, `target=30`), `replicas: 2`, CPU reservations/limits, published port. Comments: labels are service-level (`deploy.labels`), dockerstats is local-node only, how to run the daemon (dry-run → `--dry-run=false`), expected logs. (independent)
- [x] Task 66: `examples/cpu-autoscale/loadgen.sh` — POSIX `sh`, `set -eu`, executable; parameterized (TARGET_URL / CONCURRENCY / DURATION) load generator (load-tool container or portable curl fan-out) that pushes CPU above target; echoes config/progress/summary + a hint to watch the daemon's `scaling decision`/`would scale` logs; `-h` usage; fail fast if TARGET_URL unreachable. (depends on 65)
<!-- Commit checkpoint: 65-66 -->

### Phase 2: Prometheus autoscaling example (PromQL)
- [x] Task 67: `examples/prometheus-autoscale/prometheus.yml` — minimal scrape config (`scrape_interval: 5s`) with a job scraping the demo app's `/metrics`; ensure scraped series carry a `service` label equal to the Swarm service name (so `{service="$SERVICE"}` matches). Consistent with Task 68 (service name/port, metric `http_requests_total`). (independent)
- [x] Task 68: `examples/prometheus-autoscale/stack.yml` — `prometheus` (mounts prometheus.yml, `:9090`) + `demo-app` exposing `http_requests_total` (`replicas: 3`); demo-app `deploy.labels`: `enabled=true`, `min=3`, `max=20`, `metric=rps`, `target=50`, `source=prometheus`, templated `query=sum(rate(http_requests_total{service="$SERVICE"}[1m]))/scalar(count(up{service="$SERVICE"}))`. Comments: daemon needs `--prometheus-url`, `$SERVICE` expansion, single-value result rule, per-replica target semantics, `deploy.labels` requirement. (depends on 67)
<!-- Commit checkpoint: 67-69 -->

### Phase 3: Healer example (stuck-pending)
- [x] Task 69: `examples/healer/stack.yml` — service with `deploy.placement.constraints: ["node.labels.nodeNum == 1"]` **and** the full opt-in policy under `deploy.labels` (`enabled=true`, `min=1`, `max=1`, `metric=cpu`, `target=…`) so it is a ManagedService yet autoscaling is a no-op. Header comment: exact reproduction steps (label node → deploy → drain/stop node → recover), the heal signature, single-node limitation, and expected `would force-update (heal)` log. (independent)
<!-- Commit checkpoint: 67-69 -->

### Phase 4: Walkthrough + docs wiring (docs checkpoint)
- [x] Task 70: `examples/README.md` — walkthrough: intro (labels on the service), prerequisites (`docker swarm init` lab, dry-run default), running the daemon alongside (local `make run` vs `deploy/stack.yml`; per-example flags), one subsection per example (purpose, deploy command, how to drive it, expected daemon logs), safety + teardown, links to docs. (depends on 65, 66, 68, 69)
- [x] Task 71: Wire `examples/` into docs — README Documentation table row → `examples/README.md`; link from `docs/getting-started.md`; See-Also pointers in `docs/metrics-providers.md` + `docs/configuration.md`; add `examples/` to the `AGENTS.md` repo map. Keep breadcrumb style; verify no broken relative links. (depends on 70)
<!-- Commit checkpoint: 70-73 -->

### Phase 5: Validation (stack parse-check)
- [x] Task 72: `make examples-validate` — loop `docker stack config -c` over `examples/*/stack.yml` (parse-only, no swarm needed); optional guarded `promtool check config` + `shellcheck`; register in `.PHONY` and `make help`; echo `Validating <file>` + PASS summary; non-zero on failure. (independent of 65-71, but validates their output)
- [x] Task 73: CI wiring — add a `examples-validate` step/job to `.github/workflows/ci.yml` (ubuntu-latest, checkout@v4, `run: make examples-validate`); parse-only (no deploy/push), `permissions: contents: read`, valid YAML. (depends on 72)
<!-- Commit checkpoint: 70-73 -->

## Definition of Done
- `examples/` contains `cpu-autoscale/{stack.yml,loadgen.sh}`, `prometheus-autoscale/{stack.yml,prometheus.yml}`, `healer/stack.yml`, and `README.md`.
- Every example service carries a **valid** opt-in policy under `deploy.labels` (enabled + min/max/metric/target); the Prometheus one adds `source=prometheus` + a templated `query`; the healer one adds a placement constraint. No `swarm.autoscaler.*` label is placed under a top-level container `labels` key.
- `docker stack config -c examples/*/stack.yml` parses for all three; `make examples-validate` exits 0 locally and in CI.
- `loadgen.sh` is executable, POSIX-clean, self-documents its config, and points the user at the daemon logs.
- `examples/README.md` is runnable end-to-end (copy-pasteable deploy commands, daemon flags per example, teardown), and all relative links resolve.
- README Documentation table, `docs/getting-started.md`, `docs/metrics-providers.md`/`docs/configuration.md` See-Also, and `AGENTS.md` all reference `examples/`; no broken links.
- `ci.yml` remains valid YAML with a parse-only examples validation step; no live-swarm or push requirement introduced.
- **Zero Go source changes** — `internal/core/*` purity and daemon behavior untouched; `go build ./...`, `make lint`, `make test-race` stay green.
- Documentation checkpoint run at completion (Docs: yes).
