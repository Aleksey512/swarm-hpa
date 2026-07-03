[← Metrics Providers](metrics-providers.md) · [Back to README](../README.md) · [Observability →](observability.md)

# Agents & Rebalancing

Two v0.3.0 capabilities turn the single-node daemon into a **cluster-aware** one:
a per-node **agent fleet** that feeds the manager cluster-wide load, and a
**load-aware rebalancer** that relieves an overloaded node. Both are **additive
and opt-in** — see [Migration from v0.2.0](#migration-from-v020).

## Manager / agent split

One binary, two roles, selected by `--mode` / `MODE` (default `manager`):

| Role | `MODE` | Deploy | Docker socket | What it does |
|------|--------|--------|---------------|--------------|
| **Manager** | `manager` (default) | one replica, **manager-pinned** | manager API (services/tasks/nodes + `ServiceUpdate`) | Runs the reconcile loop (autoscale + heal + rebalance), ingests agent reports, serves `/metrics`. |
| **Agent** | `agent` | `mode: global` (one per node) | **local** socket, read-only | Samples the LOCAL node's per-task CPU/memory and POSTs it to the manager. Never mutates Swarm. |

Agents connect **to** the manager over the overlay network (`MANAGER_URL`), so
the manager opens no outbound connections to nodes. The manager must run on a
manager node; agents run everywhere.

### Why agents exist

The Docker stats API is served by the **local** daemon only. A manager-only
daemon using [`dockerstats`](metrics-providers.md#docker-stats-provider-dockerstats)
therefore reads CPU/memory for tasks **on its own node** and silently skips
remote-node tasks. Agents close that gap: each node reports its own tasks, and
the manager aggregates the fleet into a cluster-wide view.

### Agent registry (dedup by node ID)

The manager keys every report by **node ID**, last-writer-wins, so a node is
never double-counted even if two agents briefly overlap during a rolling update.
`mode: global` already guarantees one agent per node; the registry is
defense-in-depth. Two more guards:

- the ingest handler **rejects** a report whose node ID is not a known Swarm node
  (cross-checked against the live node list);
- a report older than `--agent-stale-timeout` (default `45s`) is **evicted**, so
  a dead node stops influencing decisions and its `node_*` gauges are dropped.

### Ingest endpoint

| | |
|---|---|
| Route | `POST /v1/report` on the manager |
| Address | `--ingest-addr` / `INGEST_ADDR` (default `:9096`) |
| Auth | `INGEST_TOKEN` bearer — an **env-only** shared secret (constant-time compare). Unset = unauthenticated, logged as a warning. |

Keep `INGEST_ADDR` on the internal overlay (unpublished). See
[Configuration](configuration.md) for every agent/manager flag.

## The `agents` metrics provider

Selecting `agents` makes the autoscaler read the **aggregated fleet** instead of
local stats — the self-contained (no Prometheus) path to **multi-node**
Docker-stats autoscaling. Select it globally with `METRICS_PROVIDER=agents` or
per service with `swarm.autoscaler.source=agents`. Full contrast with
`dockerstats` in [Metrics Providers](metrics-providers.md#agents-provider-agents).

## Load-aware rebalancing

Swarm's scheduler spreads tasks by **count**, not load, so one worker can sit
idle while another is saturated. The manager detects the node-CPU skew from agent
reports and can **force-reschedule** a service to relieve the busy node.

- **Opt-in per service** via `swarm.autoscaler.rebalance=true` — independent of
  `enabled`/`heal`, defaults false, so it never touches a service that did not
  opt in (even one that autoscales or heals).
- **Dry-run by default** (like scale/heal) and gated by a dedicated **long
  cooldown** (`--rebalance-cooldown`, default `10m`). It **always logs the
  recommendation**, even in dry-run.
- Triggers when the CPU spread between the busiest and idlest node reaches
  `--rebalance-threshold` (default `0.30` = 30 percentage points).
- Only **replicated** services are eligible (a global service already runs
  everywhere), and a move is proposed only to a node that satisfies the service's
  **placement constraints**.

### Honest limitation

Swarm has **no load-aware task-move API**. The only lever is
`docker service update --force`, which re-cycles **all** of the service's
replicas — not just the one task on the hot node. That bluntness is exactly why
rebalancing is opt-in, dry-run by default, and on a long cooldown. Targeted
per-task relocation is a documented **future enhancement**.

```bash
# Make "web" eligible for rebalancing (still dry-run until you set DRY_RUN=false):
docker service update --label-add swarm.autoscaler.rebalance=true web
```

A dry-run recommendation logs both the finding and the (suppressed) action:

```
level=INFO msg="rebalance recommendation" service=web from_node=node-a to_node=node-c ...
level=INFO msg="dry-run: would rebalance (force-update)" service=web from_node=node-a to_node=node-c
```

## Migration from v0.2.0

Defaults are **100% backward compatible**: `mode=manager` +
`dockerstats`/`prometheus` behave **exactly** as v0.2.0. Agents, `source=agents`,
and rebalancing are all additive and opt-in.

| You want | Do |
|----------|-----|
| Nothing to change | Upgrade the image — a single-node / manager-only deployment keeps working unchanged. |
| Cluster-wide stats autoscaling | Add the `swarm-hpa-agent` service (`mode: global`) and set `METRICS_PROVIDER=agents` (or `source=agents` per service). |
| Load-aware rebalancing | Add agents (above) and label services `swarm.autoscaler.rebalance=true`. |

See [Deployment](deployment.md) for the two-service manager/agent stacks and
`INGEST_TOKEN`.

## See Also

- [Metrics Providers](metrics-providers.md) — the `agents` provider vs `dockerstats`/`prometheus`.
- [Configuration](configuration.md) — every manager/agent flag and the `rebalance` label.
- [Deployment](deployment.md) — the manager + agent Swarm stacks and `INGEST_TOKEN`.
- [Observability](observability.md) — the agent/node/rebalance metric series.
</content>
