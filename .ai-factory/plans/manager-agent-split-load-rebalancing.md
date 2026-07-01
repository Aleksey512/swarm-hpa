# Implementation Plan: Manager/Agent split + load-aware task rebalancing → v0.3.0

Branch: none (git.create_branches=false — work on `main`)
Created: 2026-07-01

## Summary

Today swarm-hpa is a **single manager-bound daemon**. Its `dockerstats` metrics
provider can only read `ContainerStats` for tasks on the **local (manager) node** —
remote-node tasks are silently skipped (`internal/adapter/metrics/dockerstats/dockerstats.go:79`).
So Docker-stats autoscaling is effectively single-node, and there is no way to see
per-node load. Swarm also has **no load-aware rebalancing**: the scheduler spreads by
task *count*, so one worker can sit at 10% while another runs at 70%.

**Feature:** split the binary into two cooperating roles (one binary, `--mode`):

- **Manager** (`mode=manager`, on a manager node): the existing reconcile loop
  **plus** an ingest endpoint that receives agent reports, a distributed metrics
  provider (cluster-wide autoscaling), and a new load-aware **rebalancer**.
- **Agent** (`mode=agent`, deployed `mode: global` → exactly one per node): collects
  **local** per-task/per-node CPU/mem via the local Docker socket and **reports** them
  to the manager.

The manager keeps an **agent registry keyed by node ID** — this is the correctness
mechanism the user asked for: two reports from the same node ID update one entry
(last-writer-wins), so a node can never be double-counted ("no 2 agents from one
node"). Global-mode deployment enforces one agent per node at the orchestration
layer; the node-ID key + ingest node-ID cross-check enforce it at the data layer.

Backward compatible: default `mode=manager` + `dockerstats`/`prometheus` keep working
exactly as v0.2.0; agents, the `agents` metrics source, and rebalancing are additive
and opt-in.

## Settings
- **Testing: yes** — table-driven tests for the pure core (rebalancer, distributed
  aggregation, registry dedup/eviction) + goleak/concurrency (`-race`) for registry,
  ingest, reporter, and run loops. Matches project convention (stdlib `testing`, port
  fakes, `go.uber.org/goleak`, injectable clock).
- **Logging: verbose (DEBUG)** — DEBUG what each agent reports, how metrics aggregate,
  and why a rebalance was decided; INFO on agent connect/evict + applied scale/heal/
  rebalance. Core stays pure (no slog); adapters/app log via `log/slog` with existing
  fields (`service`, `node`, `reason`). Configurable via `LOG_LEVEL`.
- **Docs: yes** — mandatory docs checkpoint (`/aif-docs`) at completion: new
  manager/agent architecture + diagram, `MODE` and new env/flags, `swarm.autoscaler.rebalance`
  + `source=agents`, migration from the single daemon, and the Swarm rebalancing
  limitation + safety posture.

## Roadmap Linkage
Milestone: "Manager/Agent split + load-aware rebalancing" — new **v0.3.0** milestone
Rationale: First feature past the v0.2.0 baseline. Introduces the agent fleet that
fixes cluster-wide Docker-stats autoscaling and adds the load-aware rebalancer Swarm
lacks. (ROADMAP's formal owner is /aif-roadmap; the v0.3.0 entry is added as part of
the docs task / completion.)

## Design decisions (resolved)
- **Transport = push (agent → manager HTTP).** Agents actively POST `AgentReport` to
  the manager (`POST /v1/report`). Self-contained; no hard Prometheus dependency
  (the downstream swarmcd/followpulse cluster has no Prometheus). Matches the user's
  "agents report to the manager" framing and the dedup/registration requirement.
- **Rebalancing scope = recommend + opt-in force-reschedule.** MVP detects node-load
  imbalance, always logs/exposes the recommendation, and MAY act via the single
  guarded path using `swarm.ForceUpdate` (Swarm's only built-in reschedule lever).
  Opt-in per service (`swarm.autoscaler.rebalance=true`), **dry-run by default**, long
  dedicated cooldown. Honest limitation documented in-code + docs: force-update
  re-cycles ALL of the service's replicas; targeted per-task relocation is a future
  enhancement.
- **Agent identity.** The agent reads its own node ID from the local daemon
  (`cli.Info().Swarm.NodeID`) — works on workers. The registry keys on it; the ingest
  handler cross-checks it against `NodeList` and rejects unknown nodes (auth = shared
  `INGEST_TOKEN` bearer, constant-time compare).
- **Architecture preserved.** `core` stays pure (rebalancer is a pure decision like
  the healer, injected `Clock`). The reconciler's **single guarded mutation chokepoint**
  (dry-run + cooldown + opt-in) gains a `Rebalance` method — no second mutation path.
  New ports (`ReportSink`) live in `core/port`; adapters depend inward.

## Commit Plan
- **Commit 1** (T1–T2): `feat: add --mode + AgentReport/NodeLoad model + rebalance label`
  — mode discriminator, manager/agent config, distributed-metrics model vocabulary.
- **Commit 2** (T3–T4): `feat: agent role — local collector + reporter run loop`
  — per-node collection and push to the manager, with tests.
- **Commit 3** (T5–T6): `feat: manager ingest + agent registry (dedup by node ID)`
  — the ReportSink/registry backbone + authenticated ingest endpoint.
- **Commit 4** (T7): `feat: distributed metrics provider (source=agents)`
  — cluster-wide Docker-stats autoscaling via the agent fleet.
- **Commit 5** (T8–T9): `feat: load-aware rebalancer (opt-in, dry-run, guarded)`
  — pure decision core + guarded force-reschedule integration.
- **Commit 6** (T10–T11): `feat: agent/node/rebalance metrics + manager/agent deploy & docs`
  — observability surface, two-service stack, mandatory docs checkpoint.

## Tasks

### Phase 0: Foundations (mode + config + model)
- [x] T1: Runtime mode discriminator + manager/agent config (`internal/config`,
  `cmd/swarm-hpa/main.go`) — `--mode manager|agent`, ingest/rebalance/agent flags,
  validation. Tests.
- [x] T2: Domain model — `AgentReport`/`NodeLoad`/`TaskMetric` + `Rebalance` flag +
  `swarm.autoscaler.rebalance` label (`internal/core/model`, `internal/config/labels.go`).
  Tests. (depends on T1)
<!-- Commit checkpoint: T1–T2 → Commit 1 -->

### Phase 1: Agent role
- [x] T3: Agent-side local collector adapter (`internal/adapter/agent/collector`) —
  local node ID + capacity + per-task ContainerStats → `AgentReport`; shared
  compute extracted to `internal/adapter/statsutil`. Tests. (depends on T2)
- [x] T4: Agent reporter + `runAgent` loop (`internal/adapter/agent/reporter`,
  `internal/app/agentloop`, `cmd/swarm-hpa/agent.go`) — HTTP push with token/backoff,
  testable collect→report loop (injectable tick source), minimal `/healthz`+`/metrics`
  (`observability.AgentRecorder`). Tests + goleak + race. (depends on T3, T1, T2)
<!-- Commit checkpoint: T3–T4 → Commit 2 -->

### Phase 2: Manager ingest + registry
- [x] T5: Agent registry — dedup by node ID + stale eviction (`internal/app/registry`) —
  concurrency-safe, last-writer-wins per node, `Snapshot()`, duplicate detection,
  injected clock. Tests (dedup/eviction/race/goleak). (depends on T2)
- [x] T6: Manager ingest HTTP adapter + wiring (`internal/core/port/report.go`,
  `internal/adapter/ingest`, `cmd/swarm-hpa`) — `POST /v1/report`, token auth,
  node-ID cross-check, dedicated ingest server. Tests. (depends on T5, T1)
<!-- Commit checkpoint: T5–T6 → Commit 3 -->

### Phase 3: Distributed metrics provider
- [x] T7: Distributed metrics provider (`internal/adapter/metrics/distributed`) —
  implements `port.MetricsProvider` by aggregating the registry snapshot per service;
  `source=agents` routing in router/factory. Fixes cluster-wide autoscaling. Tests.
  (depends on T5, T2)
<!-- Commit checkpoint: T7 → Commit 4 -->

### Phase 4: Rebalancer
- [ ] T8: Pure core rebalancer (`internal/core/rebalancer`) — load-imbalance detection
  → `RebalancePlan` respecting opt-in + placement constraints; pure, table-tested.
  (depends on T2)
- [ ] T9: Reconciler rebalance integration (`internal/app/reconciler`) — `guard.Rebalance`
  (opt-in + dry-run + dedicated cooldown → `ForceUpdate`); rebalance branch in
  `observe`; always logs the recommendation. Tests. (depends on T8, T6)
<!-- Commit checkpoint: T8–T9 → Commit 5 -->

### Phase 5: Observability, deploy, docs
- [ ] T10: Observability — agent/node/rebalance metrics + `Recorder` extension
  (`internal/core/port/recorder.go`, `internal/adapter/observability`) —
  `agents_connected`, `agent_reports_total`, `agent_duplicate_total`, `node_cpu/mem_pct`,
  `rebalances_total`. Tests. (depends on T5, T6, T9)
- [ ] T11: Deploy (manager+agent stack) + mandatory docs checkpoint (`deploy/`, `README.md`,
  `docs/`) — two-service `stack.yml` (manager + global agent), proxy notes, `/aif-docs`.
  (depends on T4, T7, T9, T10)
<!-- Commit checkpoint: T10–T11 → Commit 6 -->

## Risks & Notes
- **Swarm has no native load-aware task move.** The only safe built-in lever is
  `docker service update --force` (= `ForceUpdate`), which re-cycles ALL of a
  service's replicas. Rebalancing is therefore opt-in, dry-run by default, and behind
  a long cooldown; the plan is honest about this and documents targeted per-task
  relocation as future work.
- **"No 2 agents from one node" is defended at two layers.** Orchestration: `mode: global`
  gives one agent task per node. Data: the registry keys on node ID (dedup) and the
  ingest handler rejects reports whose node ID is not in `NodeList`; a duplicate from a
  distinct source raises a WARN + `agent_duplicate_total` metric.
- **`ContainerStats` is local-only — by design here.** The whole point of the agent is
  that it runs ON each node, so its local `ContainerStats` always succeeds. The manager
  no longer tries (and fails) to read remote stats; it aggregates agent reports instead.
- **Registry concurrency.** Written by ingest HTTP goroutines, read by the reconcile
  loop and distributed provider → RWMutex + `-race`/goleak tests (see `golang-concurrency`).
- **Socket-proxy vs agent.** The hardened `stack.proxy.yml` proxy whitelist does not
  expose `ContainerStats`; the agent needs a direct read-only local socket. Documented
  as a deliberate tradeoff in T11.
- **Backward compatibility is a hard requirement** — default manager mode + existing
  providers must behave exactly as v0.2.0. Agents/rebalancing are additive and opt-in.
- **Core purity** — the rebalancer must not import Docker/Prometheus; verify with
  `grep -r "docker/docker" internal/core/` staying clean.
