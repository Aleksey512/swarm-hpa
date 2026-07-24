# Project Roadmap

> A Go daemon for Docker Swarm that adds horizontal autoscaling (HPA) for opt-in services, auto-heals tasks stuck in `pending` under placement constraints after node recovery, and declaratively syncs stacks from Git (autoscaler-aware GitOps) — opt-in, dry-run by default, fully logged.

## Milestones

- [x] **Project scaffold & tooling** — `go.mod`, `cmd/`+`internal/{core,app,adapter,config}` layout, Makefile, golangci-lint, `slog` setup, flag/env config parsing, graceful-shutdown skeleton
- [x] **Swarm read layer** — Docker SDK adapter: list/inspect services and tasks, parse `swarm.autoscaler.*` labels into `core/model`; read-only, no mutations
- [x] **Reconcile loop + dry-run safety** — `app/reconciler`: periodic loop with the single guarded mutation path (dry-run + opt-in labels + cooldown) and structured decision logging; mutations still suppressed
- [x] **Docker stats metrics provider** — `adapter/metrics/dockerstats` implementing `port.MetricsProvider` (per-task CPU/memory baseline, no external deps)
- [x] **Autoscaler (HPA) core + apply** — `core/autoscaler` decision logic (desired replicas, clamp to min/max) wired into the reconciler; real `Scale` via `SwarmController` when enabled — HPA loop end-to-end on Docker stats
- [x] **Stuck-task healer** — `core/healer` detection (5-point pending signature) + force-update via `SwarmController` with cooldown; recovers the moby#42215 stall automatically
- [x] **Prometheus metrics provider** — `adapter/metrics/prometheus` (PromQL signals), provider selection per service via labels/config (closest to K8s custom/external metrics)
- [x] **Self-observability `/metrics`** — `prometheus/client_golang` endpoint exposing the daemon's decisions, scales applied, tasks healed, and errors; finalize structured slog fields
- [x] **Scale stabilization** — separate scale-up/scale-down cooldowns, step limits, and stabilization windows to prevent flapping
- [x] **Testing & resilience hardening** — table-driven tests for decision logic, fakes for ports, transient Docker/Prometheus error tolerance, goroutine-leak checks, integration test harness
- [x] **Packaging & deployment** — Dockerfile, least-privilege run/stack example, README/docs, build-time version embedding

## v0.2.0

- [x] **Heal-only opt-in** — `swarm.autoscaler.heal` label decouples stuck-pending healing from autoscaling: heal-only (no autoscaler policy) for placement-pinned stateful singletons, `heal=false` to opt an autoscaled service out of healing; backward compatible

## v0.3.0

- [x] **Manager/Agent split + load-aware rebalancing** — `--mode manager|agent` splits the daemon into a manager (reconcile + report ingest + rebalancer) and per-node agents (`mode: global`) that push local per-task CPU/memory. The manager dedups agents by node ID, aggregates them into the `agents` metrics source for cluster-wide Docker-stats autoscaling, and adds opt-in load-aware task rebalancing (`swarm.autoscaler.rebalance`, dry-run by default). Backward compatible: default manager mode + dockerstats/prometheus unchanged; agents, `source=agents`, and rebalancing are additive and opt-in

## v0.4.0

- [x] **GitOps source & git sync** — declarative `repos` (url + password / `password_file` auth) and `stacks` (repo, branch, compose file) config; per-stack branch clone + pull with a per-repo lock; `DOCKER_HOST` / remote-socket (docker-socket-proxy) support. *(parity: repos.yaml / stacks.yaml, git sync)*
- [x] **Stack rendering pipeline** — read + parse compose, Go `text/template` rendering against a `values_file` (`Values`), producing the deployable stack map. *(parity: compose templating)*
- [x] **SOPS secrets + config/secret rotation** — age + gpg decryption (env-mounted keys), per-stack `sops_files`, automatic secret discovery from compose `secrets:` (global + per-stack, with plugin/external-secret exclusion), and `auto_rotate` config/secret rotation by content hash (`<stack>-<name>-<hash>` rename) so Swarm picks up changed content. *(parity: sops, discovery, rotation)*
- [x] **Autoscaler-aware stack deploy** — deploy via `docker stack deploy --with-registry-auth` with a configurable image pull policy (`always` / `changed`; per-stack override added in v0.5.0). **The differentiator:** because the same project owns both sync and scale, it never overwrites `replicas` of any `swarm.autoscaler.*` service — the swarm-cd↔HPA conflict dissolves by construction (no carry-forward hack, no two-controller fight). Dry-run-aware and logged. *(parity: deploy, image pull policy + the HPA-aware win)*
- [x] **Concurrent scheduler & loop integration** — worker-pool concurrency (per-repo locking) and a configurable `update_interval`, integrated alongside the existing autoscale/heal reconcile loop and its single guarded mutation path. *(parity: concurrency, interval)*
- [x] **Status, drift, web UI & API** — per-stack revision / last-error status, drift detection (live vs desired), `/metrics` for sync actions, and a `GET /stacks` JSON + static UI surface mirroring swarm-cd. *(parity: status API/UI + drift addition)*
- [x] **swarm-cd migration & docs** — config mapping / compatibility with `repos.yaml` + `stacks.yaml`, cut-over guide from m-adawi/swarm-cd, deploy example, and docs so the move is documented and reversible.

## v0.5.0

- [x] **Per-stack image pull policy** — add a `pull_policy: always|changed` field to `stacks.yaml` that overrides the global `--gitops-pull-policy` for a single stack; validated (`always|changed`) and threaded through the sync loop into `DeployOptions`, falling back to the global default when unset. Backward compatible (omit the field → current global behavior unchanged). *(delivers the per-stack override on top of the v0.4.0 global-only pull policy; the "global + per-stack" capability is now complete)*
- [x] **Expanded self-observability metrics** — surface what the daemon *observes and decides*, not just action counters: per-service gauges for current replicas, desired replicas, the observed metric value, and last decision (scale-up/scale-down/hold); per-service cooldown / in-cooldown state; a stuck-pending task-count gauge per service; and per-stack drift gauges (desired vs live replicas) so drift is alertable beyond the `/stacks` UI.

## v0.6.0

- [x] **Multiple compose files per stack + per-file pull policy** — a stack may declare several `compose_file`s, deployed in list order (one `docker stack deploy` each; additive — Swarm does not prune). Each file can carry its own `pull_policy` (precedence: file → stack → global), enabling a per-file pull split (e.g. a dev app pulls `always` while postgres pulls `changed`). `compose_file` is polymorphic and backward compatible (scalar string | list of strings | list of `{file, pull_policy}` objects; mixed lists allowed). Files are deployed as-is (not merged), so each must be self-contained. No port/adapter changes — the feature flows through the existing `DeployOpts.PullPolicy`.

## Completed

| Milestone | Date |
|-----------|------|
| Project scaffold & tooling | 2026-06-30 |
| Swarm read layer | 2026-06-30 |
| Reconcile loop + dry-run safety | 2026-06-30 |
| Docker stats metrics provider | 2026-06-30 |
| Autoscaler (HPA) core + apply | 2026-06-30 |
| Stuck-task healer | 2026-06-30 |
| Prometheus metrics provider | 2026-06-30 |
| Self-observability /metrics | 2026-06-30 |
| Scale stabilization | 2026-06-30 |
| Testing & resilience hardening | 2026-07-01 |
| Packaging & deployment | 2026-07-01 |
| Heal-only opt-in (v0.2.0) | 2026-07-01 |
| Manager/Agent split + load-aware rebalancing (v0.3.0) | 2026-07-01 |
| Concurrent scheduler & loop integration (v0.4.0) | 2026-07-03 |
| GitOps source & git sync (v0.4.0) | 2026-07-03 |
| Stack rendering pipeline (v0.4.0) | 2026-07-03 |
| SOPS secrets + config/secret rotation (v0.4.0) | 2026-07-03 |
| Autoscaler-aware stack deploy (v0.4.0) | 2026-07-03 |
| Status, drift, web UI & API (v0.4.0) | 2026-07-03 |
| swarm-cd migration & docs (v0.4.0) | 2026-07-03 |
| Per-stack image pull policy (v0.5.0) | 2026-07-14 |
| Expanded self-observability metrics (v0.5.0) | 2026-07-15 |
| Multiple compose files per stack + per-file pull policy (v0.6.0) | 2026-07-24 |
