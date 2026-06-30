[← Configuration](configuration.md) · [Back to README](../README.md) · [Observability →](observability.md)

# Metrics Providers

The autoscaler scales a service from the **current value of its scaling metric**.
That value comes from a `MetricsProvider`. Two are built in — `dockerstats` and
`prometheus` — and each service chooses which one to use.

## Per-service routing

A `Router` picks the provider for every service:

1. the service's `swarm.autoscaler.source` label (`dockerstats` or `prometheus`), or
2. the daemon's global `--metrics-provider` when the label is empty.

So one daemon can scale some services from Docker stats and others from
Prometheus at the same time. If a service selects `source=prometheus` but no
`--prometheus-url` is configured, that service gets a descriptive error and is
skipped for the tick — never a wrong scale.

## Docker stats provider (`dockerstats`)

- Reads per-task **CPU** or **memory** from the Docker Engine stats API and
  averages it across the service's tasks.
- Selected by `swarm.autoscaler.metric=cpu` or `=memory` (with `source` unset or
  `=dockerstats`).
- **Local-node only:** the stats API is served by the local daemon, so in a
  multi-node Swarm this provider sees only tasks on its node. When a service has
  no locally-readable stats, it reports *no data* and the service is skipped for
  that tick (not an error).
- No external dependencies — the baseline provider.

```bash
docker service update \
  --label-add swarm.autoscaler.enabled=true \
  --label-add swarm.autoscaler.min=2 --label-add swarm.autoscaler.max=10 \
  --label-add swarm.autoscaler.metric=cpu --label-add swarm.autoscaler.target=70 \
  web
```

## Prometheus provider (`prometheus`)

- Runs **one instant PromQL query per service** against `--prometheus-url`,
  bounded by `--prometheus-timeout`.
- The query comes from `swarm.autoscaler.query`; `swarm.autoscaler.metric` is
  just a logical name for logging.
- Closest to Kubernetes custom/external metrics: scale on RPS, p95 latency, queue
  depth, or any app metric Prometheus stores.

### Query placeholders

The query string is expanded before execution:

| Placeholder | Replaced with |
|-------------|---------------|
| `$SERVICE` | the Swarm service name (`svc.Ref.Name`) |
| `$SERVICE_ID` | the Swarm service ID |

This lets one templated query be reused across services.

### Result handling

The query must reduce to a single number:

| PromQL result | Outcome |
|---------------|---------|
| Scalar | Its value is used. |
| Vector with exactly **one** series | That sample's value is used. |
| Empty vector, or a `NaN`/`±Inf` value | *No data* → the service is skipped this tick (not an error). |
| Vector with **more than one** series | Misconfiguration → descriptive error, service skipped. |
| Empty query / transport / HTTP error | Descriptive error, service skipped. |

A single bad query never crashes the loop — the error is logged and the next
service is processed.

### Target semantics

Scaling uses the same proportional rule as Docker stats:
`desired = clamp(current × value / target, min, max)`. Write a query whose value
relative to `target` yields the ratio you want — typically a **per-replica**
figure compared against a per-replica target.

```bash
docker service update \
  --label-add swarm.autoscaler.enabled=true \
  --label-add swarm.autoscaler.min=3 --label-add swarm.autoscaler.max=20 \
  --label-add swarm.autoscaler.metric=rps --label-add swarm.autoscaler.target=50 \
  --label-add swarm.autoscaler.source=prometheus \
  --label-add 'swarm.autoscaler.query=sum(rate(http_requests_total{service="$SERVICE"}[1m]))/scalar(count(up{service="$SERVICE"}))' \
  api

./bin/swarm-hpa --dry-run=false \
  --metrics-provider=dockerstats \
  --prometheus-url=http://prometheus:9090 --prometheus-timeout=5s
```

> Here the global default stays `dockerstats`; only `api` opts into Prometheus
> via its `source` label, while `--prometheus-url` makes the Prometheus path
> available.

## Troubleshooting

| Symptom | Likely cause |
|---------|--------------|
| `requests source=prometheus but PROMETHEUS_URL is not configured` | A service set `source=prometheus` but the daemon has no `--prometheus-url`. |
| `query returned N series; reduce it to a scalar or a single series` | The PromQL result has multiple series — aggregate it (e.g. `sum(...)`). |
| Service never scales, logs show *no data* | Empty result / `NaN`, or (Docker stats) the task runs on another node. |
| `uses source=prometheus but has no swarm.autoscaler.query` | Add the `swarm.autoscaler.query` label. |

## See Also

- [Configuration](configuration.md) — the `source`/`query` labels and Prometheus flags.
- [Getting Started](getting-started.md) — build and run the daemon.
