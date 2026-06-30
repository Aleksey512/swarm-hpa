[← Metrics Providers](metrics-providers.md) · [Back to README](../README.md)

# Observability (`/metrics`)

The daemon exposes its **own** behavior as Prometheus metrics — what it observed,
decided, and did. These are distinct from the [metric providers](metrics-providers.md),
which are the **input** signals used to make scaling decisions; the `/metrics`
endpoint is the **output** telemetry about the daemon itself.

## The endpoint

| | |
|---|---|
| Path | `/metrics` |
| Address | `--metrics-addr` / `METRICS_ADDR` (default `:9095`) |
| Format | Prometheus text exposition (`prometheus/client_golang`) |
| Registry | private — only the `swarm_hpa_*` metrics below are exposed (no Go/process collectors) |

The metrics server is **best-effort**: it runs alongside the reconcile loop, and
a bind/serve failure is logged but never stops scaling or healing. It shuts down
gracefully on `SIGINT`/`SIGTERM`.

```bash
curl -s localhost:9095/metrics | grep '^swarm_hpa_'
```

## Metric catalog

| Metric | Type | Labels | Meaning |
|--------|------|--------|---------|
| `swarm_hpa_build_info` | gauge | `version` | Constant `1`; carries the build version. |
| `swarm_hpa_reconcile_total` | counter | — | Completed reconcile passes. |
| `swarm_hpa_managed_services` | gauge | — | Opted-in services observed in the last pass. |
| `swarm_hpa_scales_total` | counter | `service` | Scale actions applied. |
| `swarm_hpa_heals_total` | counter | `service` | Heal (force-update) actions applied. |
| `swarm_hpa_actions_suppressed_total` | counter | `action`, `reason` | Intended actions not applied. `action`: `scale`\|`heal`; `reason`: `dry_run`\|`cooldown`. |
| `swarm_hpa_errors_total` | counter | `stage` | Recoverable errors. `stage`: `services`\|`tasks`\|`nodes`\|`metric`\|`scale`\|`heal`. |

The `service` label is bounded to the opt-in set, so cardinality stays low.

## Sample exposition

```text
swarm_hpa_build_info{version="1.4.0"} 1
swarm_hpa_reconcile_total 128
swarm_hpa_managed_services 3
swarm_hpa_scales_total{service="web"} 5
swarm_hpa_actions_suppressed_total{action="scale",reason="dry_run"} 42
swarm_hpa_errors_total{stage="metric"} 1
```

## Scraping with Prometheus

```yaml
scrape_configs:
  - job_name: swarm-hpa
    static_configs:
      - targets: ['swarm-hpa:9095']
```

## Useful queries

| Goal | PromQL |
|------|--------|
| Scaling actions per service (5m rate) | `rate(swarm_hpa_scales_total[5m])` |
| Heals applied recently | `increase(swarm_hpa_heals_total[1h])` |
| Errors by stage | `sum by (stage) (rate(swarm_hpa_errors_total[5m]))` |
| Actions suppressed by dry-run | `swarm_hpa_actions_suppressed_total{reason="dry_run"}` |

> Tip: while `--dry-run` is enabled (the default), real actions are zero and the
> `actions_suppressed_total{reason="dry_run"}` series shows what *would* have
> happened — a safe way to validate policy before enabling mutations.

## See Also

- [Metrics Providers](metrics-providers.md) — the input signals (Docker stats / Prometheus).
- [Configuration](configuration.md) — `--metrics-addr` and the other daemon flags.
