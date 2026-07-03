[← Getting Started](getting-started.md) · [Back to README](../README.md) · [Metrics Providers →](metrics-providers.md)

# Configuration

The daemon is configured at two levels: **daemon-level** flags/env (how the loop
runs) and **per-service** labels (which services are managed and how). Daemon
values resolve with the precedence **flag > environment > default**.

The binary runs in one of two **roles**, selected by `--mode` / `MODE` (default
`manager`). The table below covers `--mode`, the shared logging/metrics flags,
and the manager's reconcile loop; the two tables after it add the manager-only
and agent-only flags. See [Agents & Rebalancing](agents-and-rebalancing.md) for
what each role does.

## Daemon flags & environment

The reconcile/scaling/metric flags here are consulted only in `manager` mode;
`--mode`, `--log-*`, and `--metrics-addr` apply to both roles.

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--mode` | `MODE` | `manager` | Runtime role: `manager` (reconcile loop + ingest + rebalancer) or `agent` (per-node stats reporter). |
| `--poll-interval` | `POLL_INTERVAL` | `15s` | Reconcile loop interval. Must be `> 0`. |
| `--cooldown` | `COOLDOWN` | `3m` | Minimum interval between **heal** actions on the same service. `0` disables. |
| `--scale-up-cooldown` | `SCALE_UP_COOLDOWN` | `3m` | Minimum interval between **scale-up** actions on the same service. `0` disables. |
| `--scale-down-cooldown` | `SCALE_DOWN_COOLDOWN` | `3m` | Minimum interval between **scale-down** actions on the same service. `0` disables. |
| `--max-scale-step` | `MAX_SCALE_STEP` | `0` | Maximum replicas a single scaling action may change. `0` = unlimited. |
| `--scale-down-stabilization` | `SCALE_DOWN_STABILIZATION` | `0` | Hold a scale-down until the recommendation has stayed low for this long (max-over-window). `0` disables. |
| `--heal-threshold` | `HEAL_THRESHOLD` | `2m` | Minimum time a task must stay `pending` before the healer force-updates the service. `0` disables the duration gate. |
| `--dry-run` | `DRY_RUN` | `true` | Log intended mutations without applying them. **The safety default.** |
| `--log-level` | `LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error`. |
| `--log-format` | `LOG_FORMAT` | `text` | `text` \| `json`. |
| `--metrics-provider` | `METRICS_PROVIDER` | `dockerstats` | Global default metric source: `dockerstats` \| `prometheus` \| `agents`. A service can override it per-label (see below). |
| `--prometheus-url` | `PROMETHEUS_URL` | _(empty)_ | Prometheus base URL. Required when `--metrics-provider=prometheus`, and needed for **any** service that selects `source=prometheus`. |
| `--prometheus-timeout` | `PROMETHEUS_TIMEOUT` | `10s` | Per-query timeout for PromQL requests. Must be `> 0`. |
| `--metrics-addr` | `METRICS_ADDR` | `:9095` | Listen address for the daemon's own `/metrics` endpoint (see [Observability](observability.md)). |

Durations use Go syntax (`15s`, `2m`, `1h30m`). The effective configuration is
logged at startup (`msg="effective configuration"`); any credentials in
`--prometheus-url` are redacted in logs.

## Manager-mode flags & environment

These configure the agent-report ingest endpoint and the rebalancer; they are
ignored in `agent` mode. `METRICS_PROVIDER=agents` (in the table above) selects
the cluster-wide [`agents`](metrics-providers.md#agents-provider-agents) source.

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--ingest-addr` | `INGEST_ADDR` | `:9096` | Listen address for the agent-report endpoint (`POST /v1/report`). Keep it on the internal overlay. |
| _(none)_ | `INGEST_TOKEN` | _(empty)_ | Shared bearer secret agents authenticate with. **Env-only** (no flag, so it never appears in a process listing). When unset the endpoint is unauthenticated and logs a warning. |
| `--agent-stale-timeout` | `AGENT_STALE_TIMEOUT` | `45s` | A report older than this is evicted, so a dead node stops influencing decisions. Must be `> 0`. |
| `--rebalance-threshold` | `REBALANCE_THRESHOLD` | `0.30` | Node-CPU spread **fraction** in `(0,1]` at/above which a rebalance is proposed. `0.30` = 30 percentage points between the busiest and idlest node. |
| `--rebalance-cooldown` | `REBALANCE_COOLDOWN` | `10m` | Minimum interval between **rebalance** actions on the same service. `0` disables. |

## Agent-mode flags & environment

Consulted only when `--mode=agent`; ignored by the manager. An agent also honors
`INGEST_TOKEN` (above) to authenticate its outgoing reports, plus the shared
`--log-*` and `--metrics-addr` (its `/metrics` + `/healthz`).

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--manager-url` | `MANAGER_URL` | _(empty)_ | Base URL of the manager's ingest endpoint. **Required** in agent mode, e.g. `http://swarm-hpa-manager:9096`. |
| `--report-interval` | `REPORT_INTERVAL` | `15s` | How often the agent collects and pushes a report. Must be `> 0`. |
| `--node-id` | `NODE_ID` | _(empty)_ | Override the reported node ID. Normally left empty — auto-detected from the local Docker daemon. |

## Service labels (`swarm.autoscaler.*`)

A service is managed **only** when it opts in via a label — either
`swarm.autoscaler.enabled=true` (autoscaling) or `swarm.autoscaler.heal=true`
(healing). This is the project's hard opt-in boundary — unlabeled services are
never mutated. A service that opts in but is misconfigured is logged and skipped,
never fatal.

The `min`/`max`/`metric`/`target` labels are **required for autoscaling**
(`enabled=true`); a heal-only service needs none of them.

| Label | Required | Description |
|-------|----------|-------------|
| `swarm.autoscaler.enabled` | for autoscaling | Must be exactly `true` to opt into autoscaling. |
| `swarm.autoscaler.min` | for autoscaling | Minimum replicas (unsigned integer). |
| `swarm.autoscaler.max` | for autoscaling | Maximum replicas (`>= 1`, and `>= min`). |
| `swarm.autoscaler.metric` | for autoscaling | `cpu` \| `memory` for Docker stats; a logical name (e.g. `rps`) for Prometheus. |
| `swarm.autoscaler.target` | for autoscaling | Target value for the metric (float, `> 0`). |
| `swarm.autoscaler.source` | no | `dockerstats` \| `prometheus` \| `agents`. Empty = use the daemon's `--metrics-provider`. |
| `swarm.autoscaler.query` | no | PromQL expression, used when the source is `prometheus`. Supports `$SERVICE` / `$SERVICE_ID` expansion. |
| `swarm.autoscaler.heal` | no | Opt into stuck-pending healing independently of autoscaling (see below). |
| `swarm.autoscaler.rebalance` | no | Opt into load-aware rebalancing (`true`). Independent of `enabled`/`heal`; defaults false. See [Agents & Rebalancing](agents-and-rebalancing.md). |

See [Metrics Providers](metrics-providers.md) for how `source` and `query` drive
the Prometheus path.

### Healing opt-in (`swarm.autoscaler.heal`)

Healing (recovering tasks stuck `pending` under a placement constraint after a
node recovers — [moby/moby#42215](https://github.com/moby/moby/issues/42215)) is
a separate concern from autoscaling. The `heal` label lets you enable it on its
own — useful for **placement-pinned stateful singletons** (a database, a
per-node RabbitMQ) that should be healed but must never be autoscaled.

| Labels on the service | Behaviour |
|-----------------------|-----------|
| `enabled=true` (+ a valid policy) | autoscale **and** heal — `heal` defaults to the enabled state |
| `heal=true` alone | **heal-only** — no `min`/`max`/`metric`/`target` required; the service is never scaled |
| `enabled=true` + `heal=false` | autoscale only — healing disabled for this service |

Backward compatible: an existing `enabled=true` service keeps both behaviours.

```bash
# Heal a placement-pinned RabbitMQ node without autoscaling it:
docker service update --label-add swarm.autoscaler.heal=true rabbitmq-01
```

## How decisions are made

- **Scaling.** `desired = clamp(current × value / target, min, max)`, then
  stabilized and step-limited (see *Preventing flapping*), then applied through
  the single guarded path subject to dry-run and the direction's cooldown; equal
  current/desired is a no-op.
- **Healing.** A service is force-updated only when the full stuck-pending
  signature holds: it has placement constraints, a task has been `pending` (while
  Swarm wants it running) for at least `--heal-threshold`, **and** a node that
  satisfies the constraints is now Active+Ready (the constrained node recovered).
- **Cooldown.** After any action (or any dry-run "would…" log), further actions on
  that service are suppressed for the matching window: `--scale-up-cooldown`,
  `--scale-down-cooldown`, or `--cooldown` (heal).

## Preventing flapping

Three independent, opt-in controls dampen rapid replica churn. All default to
behavior-preserving values, so you enable them deliberately:

- **Direction-aware cooldowns** — scale-ups and scale-downs have their own minimum
  intervals (`--scale-up-cooldown` / `--scale-down-cooldown`), so a service can
  react quickly to load yet shrink slowly.
- **Step limit** — `--max-scale-step` caps how many replicas one action may change
  (absolute count), smoothing large jumps.
- **Scale-down stabilization** — `--scale-down-stabilization` holds a scale-down
  until the recommendation has stayed low for the whole window: it acts on the
  **maximum** recommendation within the window, so a brief metric dip never shrinks
  the service. Scale-ups are unaffected and it never grows a service. This mirrors
  the Kubernetes HPA scale-down stabilization window.

A flapping-resistant starting point — react fast up, shrink slowly:

```bash
./bin/swarm-hpa \
  --scale-up-cooldown=1m \
  --scale-down-cooldown=5m \
  --max-scale-step=2 \
  --scale-down-stabilization=5m
```

## Example: env-based configuration

```bash
export DRY_RUN=false
export METRICS_PROVIDER=prometheus
export PROMETHEUS_URL=http://prometheus:9090
export PROMETHEUS_TIMEOUT=5s
export COOLDOWN=2m
export LOG_LEVEL=debug
./bin/swarm-hpa
```

## See Also

- [Getting Started](getting-started.md) — build and first run.
- [Metrics Providers](metrics-providers.md) — choosing and configuring the metric source.
- [Agents & Rebalancing](agents-and-rebalancing.md) — the manager/agent roles, ingest flags, and the `rebalance` label.
- [Examples](../examples/README.md) — runnable stacks that put these labels to work.
