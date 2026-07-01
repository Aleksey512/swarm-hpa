# AGENTS.md

> Structural map for AI agents and new developers. Keep it factual — describe only
> what exists. Update when the project layout changes significantly. The
> Documentation section is maintained by `/aif-docs`.

## Project Overview

A Go daemon for Docker Swarm that adds horizontal autoscaling (HPA) for opt-in
services and auto-heals tasks stuck in `pending` under placement constraints after
node recovery. Mutating actions are opt-in (labels), dry-run by default, and
logged. Full spec: `.ai-factory/DESCRIPTION.md`.

## Tech Stack

- **Programming language:** Go
- **Framework:** None (stdlib-oriented); CLI via stdlib `flag` + env vars
- **Docker access:** Official Docker Go SDK (`github.com/docker/docker/client`)
- **Metrics (in):** Docker Engine stats API + Prometheus (PromQL), behind a
  `MetricsProvider` interface
- **Metrics (out):** `prometheus/client_golang` exposing `/metrics`
- **Logging:** `log/slog` (structured; level via `LOG_LEVEL`)
- **Database / ORM:** None (state reconciled from Swarm; in-memory cooldowns)

## Project Structure

Scaffold is in place (milestone "Project scaffold & tooling" is complete). The
source layout follows the Explicit Architecture (ports & adapters) in
`.ai-factory/ARCHITECTURE.md`:

```
.
├── cmd/swarm-hpa/main.go    # composition root: config load, logger, signal ctx, run loop
├── internal/
│   ├── core/                # PURE domain (no Docker/Prometheus/adapter imports)
│   │   ├── model/           # domain types: ServicePolicy, ManagedService, TaskView, NodeView
│   │   ├── port/            # interfaces: SwarmController (read), Clock (MetricsProvider later)
│   │   ├── autoscaler/      # scaling decision: metric+policy → desired replicas (clamp, tolerance)
│   │   └── healer/          # stuck-task detection logic (placeholder)
│   ├── app/
│   │   └── reconciler/      # reconcile loop + Guard (the single dry-run + cooldown mutation chokepoint)
│   ├── adapter/
│   │   ├── swarm/           # Docker SDK adapter: read-only list/inspect services, tasks, nodes
│   │   ├── metrics/         # provider factory + dockerstats (cpu/mem); prometheus → milestone 7
│   │   └── observability/   # slog logging setup (live); /metrics endpoint (future)
│   └── config/              # flag/env config + swarm.autoscaler.* label parsing
├── Makefile                 # build/run/test/test-race/test-integration/lint/fmt/cover + docker-build/run/push
├── .golangci.yml            # golangci-lint v2 config
├── go.mod                   # module github.com/Aleksey512/swarm-hpa
├── Dockerfile               # multi-stage image (alpine, non-root, CGO-free static)
├── .dockerignore            # trims the build context to go source
├── deploy/                  # docker stack examples for the DAEMON: stack.yml (direct socket) + stack.proxy.yml (least-privilege)
├── examples/                # runnable demo WORKLOADS: cpu-autoscale (+loadgen.sh), prometheus-autoscale (+prometheus.yml), healer
├── .github/workflows/       # ci.yml (fmt/vet/lint/test/hadolint/build/examples-validate) + release.yml (GHCR image on v* tags)
├── .ai-factory/             # AI Factory context (DESCRIPTION, ARCHITECTURE, ROADMAP, config, rules, plans)
├── .claude/skills/          # aif-*, golang-*, prometheus-label-strategy, docker-swarm-go-sdk
└── skills-lock.json         # skills.sh lockfile
```

> Packages marked "placeholder" contain only a `doc.go` and are filled in by
> later roadmap milestones.

## Key Entry Points

| File | Purpose |
|------|---------|
| `.ai-factory/DESCRIPTION.md` | Project specification and scope (source of truth) |
| `.ai-factory/ARCHITECTURE.md` | Architecture pattern, folder structure, dependency rules |
| `.ai-factory/config.yaml` | AI Factory settings (language, paths, rules) |
| `.ai-factory/rules/base.md` | Project coding conventions |
| `cmd/swarm-hpa/main.go` | Daemon entry point: config load, logger, signal context, reconcile loop |
| `internal/config/config.go` | Runtime config (flags+env, `dry_run` defaults true) |
| `internal/app/reconciler/reconciler.go` | Reconcile loop; observes services, routes stuck-pending to the Guard |
| `internal/app/reconciler/guard.go` | Single guarded mutation path: dry-run + cooldown gate for scale/heal |
| `internal/adapter/swarm/mutate.go` | Version-indexed `ServiceUpdate` scale/force-update (optimistic retry) |
| `internal/adapter/metrics/factory.go` | Selects the `MetricsProvider` (dockerstats now; prometheus later) |
| `internal/adapter/metrics/dockerstats/` | Node-local CPU/memory metrics from the Docker stats API |
| `internal/core/autoscaler/autoscaler.go` | HPA decision: desired replicas from metric+policy (clamp + tolerance) |
| `internal/adapter/swarm/swarm.go` | Read-only Docker SDK adapter (implements `port.SwarmController`) |
| `internal/config/labels.go` | `swarm.autoscaler.*` label → `ServicePolicy` parsing |
| `Makefile` | Build/test/lint developer tasks |

## Documentation

| Document | Path | Description |
|----------|------|-------------|
| README | README.md | Project landing page |
| Getting Started | docs/getting-started.md | Prerequisites, build, run, verify |
| Examples | examples/README.md | Runnable autoscaling + healer demo stacks |
| Configuration | docs/configuration.md | Daemon flags/env and service labels |
| Metrics Providers | docs/metrics-providers.md | Docker stats vs Prometheus, routing, PromQL |
| Observability | docs/observability.md | The daemon's own /metrics endpoint |
| Development | docs/development.md | Build, test, integration harness, CI |
| Deployment | docs/deployment.md | Container image, Swarm stack, hardening |
| Specification | .ai-factory/DESCRIPTION.md | What the daemon does and why |

## AI Context Files

| File | Purpose |
|------|---------|
| AGENTS.md | Project structure map for AI agents (this file) |
| .ai-factory/DESCRIPTION.md | Detailed project specification |
| .ai-factory/ARCHITECTURE.md | Architecture guidelines and dependency rules |
| .ai-factory/rules/base.md | Coding conventions for this project |

### Relevant skills

- `docker-swarm-go-sdk` — Swarm operations via the Go SDK (list/scale/heal, version-index concurrency)
- `golang-observability` — `slog` + Prometheus `/metrics` (core to this daemon)
- `golang-concurrency`, `golang-context` — reconcile loop, goroutines, graceful shutdown
- `golang-cli`, `golang-project-layout` — daemon CLI and `cmd/internal` layout
- `golang-error-handling`, `golang-safety`, `golang-security`, `golang-testing` — production quality
- `golang-how-to` — orchestrator that auto-loads the relevant `golang-*` skills
- `prometheus-label-strategy` — label/cardinality design for the `/metrics` endpoint

## Agent Rules

- This daemon manages **real production services** — treat mutating actions
  (`ServiceUpdate`: scale, force-update) as predictable, opt-in, and logged. Never
  act on a service that lacks the explicit `swarm.autoscaler.*` labels; respect
  dry-run and cooldown.
- Decompose combined shell commands into separate steps rather than chaining with
  `&&`, so each step's outcome is visible. (Git is not initialized in this project
  yet — `git.enabled: false` in config.)
