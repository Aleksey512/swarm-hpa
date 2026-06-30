# Swarm HPA & Task Healer Daemon

## Overview

A Go daemon for Docker Swarm that adds two capabilities Swarm lacks out of the
box:

1. **Service autoscaling (HPA).** Automatically adjusts the replica count of
   selected services based on load — a Horizontal Pod Autoscaler analogue from
   Kubernetes, but for Swarm.
2. **Healing of "stuck" tasks.** Works around a known Swarm scheduler bug: when a
   service has a placement constraint (e.g. `node.labels.nodeNum == 1`), the
   target node goes down, and the task stays `pending` forever — even after the
   node becomes active again. Today this is fixed only manually via
   `docker service update <service> --force`. The daemon detects such tasks and
   recovers them on its own.

The daemon manages real production services, so every mutating action must be
**predictable, opt-in, and logged**. By default nothing is touched: only
services explicitly marked for management are acted upon. Transparency beats
"magic" — it is always clear what the daemon observed, what it decided, and why.

## Core Features

- **Horizontal autoscaling** of opt-in Swarm services between configured
  `min`/`max` replica bounds, driven by a target metric and threshold.
- **Pluggable metrics providers** behind a single `MetricsProvider` interface:
  - **Docker stats** — per-task CPU/memory from the Docker Engine API, no
    external dependencies (baseline provider).
  - **Prometheus** — arbitrary PromQL signals (RPS, p95 latency, queue depth,
    custom app metrics), for HPA decisions closest to K8s custom/external
    metrics.
- **Scale stabilization** — cooldown windows and step limits to prevent
  flapping (separate scale-up / scale-down cooldowns, analogous to K8s HPA
  stabilization windows).
- **Stuck-task healer** — detects tasks stuck in `pending` due to the placement
  constraint + node-recovery scheduler bug and force-updates the affected
  service to unstick them.
- **Explicit opt-in via Docker service labels** — a service is managed only when
  it carries the agreed `swarm.autoscaler.*` labels (and/or healing labels).
- **Dry-run by default** — out of the box the daemon only logs intended actions;
  mutating actions (scale, force-update) are enabled explicitly.
- **Self-observability** — structured logging (`log/slog`) plus a Prometheus
  `/metrics` endpoint exposing the daemon's own decisions and actions.

## Tech Stack

- **Programming language:** Go
- **Framework:** None (standard library oriented); CLI via stdlib `flag` + env
  vars
- **Docker access:** Official Docker Go SDK (`github.com/docker/docker/client`, pinned `v28.5.2+incompatible`; option types in `api/types/swarm`)
- **Metrics in (HPA signals):** Docker Engine stats API + Prometheus HTTP API
  (PromQL), behind a `MetricsProvider` interface
- **Metrics out (self-observability):** `prometheus/client_golang` exposing
  `/metrics`
- **Logging:** `log/slog` (structured, configurable via `LOG_LEVEL`)
- **Database / ORM:** None — desired state is reconciled from Swarm on each loop;
  short-lived state (cooldowns, last-scaled timestamps) is kept in memory
- **Configuration:** Docker service labels (`swarm.autoscaler.*`) for per-service
  policy; daemon-level flags/env for poll interval, Prometheus URL, dry-run, log
  level

## Architecture Notes

- **Reconciliation loop.** A single control loop on a fixed interval: observe
  (list services/tasks + labels), decide (per managed service), act (only when
  not in dry-run and outside cooldown). The loop is the unit of transparency:
  each iteration logs what was seen and what was decided.
- **Two independent controllers** sharing the loop and the Docker client:
  - `autoscaler` — reads policy labels, queries the active metrics provider,
    computes a desired replica count, applies it within `min`/`max` and cooldown
    constraints.
  - `healer` — scans tasks for the stuck-`pending` signature and force-updates
    the owning service.
- **MetricsProvider interface.** Providers (`dockerstats`, `prometheus`) are
  pluggable and selected per service via labels (e.g.
  `swarm.autoscaler.metric=cpu` vs a PromQL-backed metric). This keeps the
  decision logic provider-agnostic.
- **Opt-in boundary.** The label namespace (`swarm.autoscaler.*`) is the only
  thing that brings a service under management. Services without the labels are
  never mutated.
- **Safety boundary.** All mutating Docker calls (`ServiceUpdate` for scaling and
  for force-update healing) flow through a single guarded path that respects the
  global dry-run flag and per-service cooldown, and emits a structured log +
  metric for every action and every suppressed-by-dry-run intent.
- **No external state store.** Restart-safe by design: the daemon rederives the
  world from Swarm each loop; in-memory cooldown state is allowed to reset on
  restart (conservative: a fresh start simply re-observes before acting).

## Architecture

See `.ai-factory/ARCHITECTURE.md` for detailed architecture guidelines.
Pattern: Explicit Architecture (Ports & Adapters) — a lightweight hexagonal layout
with a pure, testable decision core and swappable infrastructure adapters.

## Non-Functional Requirements

- **Predictability:** every mutating action is opt-in (labels) and gated by
  dry-run + cooldown; no implicit or surprising changes to production services.
- **Transparency / observability:** structured logging (`log/slog`, level via
  `LOG_LEVEL`) for every observation and decision; Prometheus `/metrics` for the
  daemon's own behavior (decisions made, scales applied, tasks healed, errors).
- **Safety:** dry-run is the default; mutations require explicit enablement;
  replica changes are clamped to `min`/`max` and rate-limited by cooldowns.
- **Resilience:** the daemon recovers stuck tasks automatically and tolerates
  transient Docker/Prometheus API errors without crashing the loop (log and
  continue).
- **Least privilege:** requires Docker API access (manager node / socket);
  document and minimize required permissions; never run mutating actions it was
  not explicitly granted.
- **Operability:** configurable poll interval, log level, dry-run, and metrics
  provider endpoints via flags/env; clear startup log of the effective
  configuration.
