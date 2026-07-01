# Swarm HPA & Task Healer

> Horizontal autoscaling and stuck-task healing for Docker Swarm — opt-in, dry-run by default, fully logged.

A small Go daemon that adds two capabilities Swarm lacks out of the box: a
Horizontal Pod Autoscaler analogue that scales opt-in services by a metric, and
a healer that recovers tasks stuck in `pending` under a placement constraint
after a node recovers ([moby/moby#42215](https://github.com/moby/moby/issues/42215)).
It manages real production services, so every mutating action is opt-in (via
labels), gated by dry-run + cooldown, and logged.

## Quick Start

```bash
make build                      # → bin/swarm-hpa
./bin/swarm-hpa                 # dry-run is ON by default: nothing is mutated
```

Mark a service for management and watch the daemon decide (still dry-run):

```bash
docker service update \
  --label-add swarm.autoscaler.enabled=true \
  --label-add swarm.autoscaler.min=2 \
  --label-add swarm.autoscaler.max=10 \
  --label-add swarm.autoscaler.metric=cpu \
  --label-add swarm.autoscaler.target=70 \
  web

./bin/swarm-hpa --dry-run=false      # enable real scaling/healing
```

## Key Features

- **Horizontal autoscaling** of opt-in services between `min`/`max` by a target metric and threshold.
- **Pluggable metrics**, chosen per service: **Docker stats** (CPU/memory, no deps) or **Prometheus** (arbitrary PromQL — RPS, p95 latency, queue depth).
- **Stuck-task healer** — force-updates a service only when the precise stuck-pending signature holds and the constrained node has recovered.
- **Opt-in via labels** — a service is touched only when it carries `swarm.autoscaler.*` labels.
- **Dry-run by default** — out of the box the daemon only logs intended actions.
- **Safe mutations** — one guarded path enforces dry-run + per-service cooldown; replica changes are clamped to `min`/`max`.

## Example

```bash
# Scale "api" on a Prometheus signal (requests/sec per replica target = 50)
docker service update \
  --label-add swarm.autoscaler.enabled=true \
  --label-add swarm.autoscaler.min=3 --label-add swarm.autoscaler.max=20 \
  --label-add swarm.autoscaler.metric=rps --label-add swarm.autoscaler.target=50 \
  --label-add swarm.autoscaler.source=prometheus \
  --label-add 'swarm.autoscaler.query=sum(rate(http_requests_total{service="$SERVICE"}[1m]))/scalar(count(up{service="$SERVICE"}))' \
  api

./bin/swarm-hpa --dry-run=false \
  --metrics-provider=prometheus --prometheus-url=http://prometheus:9090
```

---

## Documentation

| Guide | Description |
|-------|-------------|
| [Getting Started](docs/getting-started.md) | Prerequisites, build, run, verify |
| [Configuration](docs/configuration.md) | Daemon flags/env and `swarm.autoscaler.*` service labels |
| [Metrics Providers](docs/metrics-providers.md) | Docker stats vs Prometheus, per-service routing, PromQL |
| [Observability](docs/observability.md) | The daemon's own `/metrics` endpoint and metric catalog |
| [Development](docs/development.md) | Build, test, the integration harness, and CI |
| [Deployment](docs/deployment.md) | Container image, Swarm stack, least-privilege, upgrades |

## License

MIT — see [LICENSE](LICENSE).
