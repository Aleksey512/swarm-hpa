[← Getting Started](getting-started.md) · [Back to README](../README.md) · [Metrics Providers →](metrics-providers.md)

# Configuration

The daemon is configured at two levels: **daemon-level** flags/env (how the loop
runs) and **per-service** labels (which services are managed and how). Daemon
values resolve with the precedence **flag > environment > default**.

## Daemon flags & environment

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
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
| `--metrics-provider` | `METRICS_PROVIDER` | `dockerstats` | Global default metric source: `dockerstats` \| `prometheus`. A service can override it per-label (see below). |
| `--prometheus-url` | `PROMETHEUS_URL` | _(empty)_ | Prometheus base URL. Required when `--metrics-provider=prometheus`, and needed for **any** service that selects `source=prometheus`. |
| `--prometheus-timeout` | `PROMETHEUS_TIMEOUT` | `10s` | Per-query timeout for PromQL requests. Must be `> 0`. |
| `--metrics-addr` | `METRICS_ADDR` | `:9095` | Listen address for the daemon's own `/metrics` endpoint (see [Observability](observability.md)). |

Durations use Go syntax (`15s`, `2m`, `1h30m`). The effective configuration is
logged at startup (`msg="effective configuration"`); any credentials in
`--prometheus-url` are redacted in logs.

## Service labels (`swarm.autoscaler.*`)

A service is managed **only** when it carries `swarm.autoscaler.enabled=true`.
This is the project's hard opt-in boundary — unlabeled services are never
mutated. A service that opts in but is misconfigured is logged and skipped, never
fatal.

| Label | Required | Description |
|-------|----------|-------------|
| `swarm.autoscaler.enabled` | **yes** | Must be exactly `true` to opt in. |
| `swarm.autoscaler.min` | **yes** | Minimum replicas (unsigned integer). |
| `swarm.autoscaler.max` | **yes** | Maximum replicas (`>= 1`, and `>= min`). |
| `swarm.autoscaler.metric` | **yes** | `cpu` \| `memory` for Docker stats; a logical name (e.g. `rps`) for Prometheus. |
| `swarm.autoscaler.target` | **yes** | Target value for the metric (float, `> 0`). |
| `swarm.autoscaler.source` | no | `dockerstats` \| `prometheus`. Empty = use the daemon's `--metrics-provider`. |
| `swarm.autoscaler.query` | no | PromQL expression, used when the source is `prometheus`. Supports `$SERVICE` / `$SERVICE_ID` expansion. |

See [Metrics Providers](metrics-providers.md) for how `source` and `query` drive
the Prometheus path.

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
