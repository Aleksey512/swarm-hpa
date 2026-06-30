[Back to README](../README.md) · [Configuration →](configuration.md)

# Getting Started

## Prerequisites

| Requirement | Notes |
|-------------|-------|
| Go 1.25+ | To build from source (`go.mod` pins `go 1.25.0`). |
| Docker Swarm | Run the daemon on (or with API access to) a **manager** node. |
| Docker API access | The daemon reads services/tasks/nodes and issues `ServiceUpdate`; it needs the Docker socket or a `DOCKER_HOST` endpoint. |

The daemon talks to the Docker Engine through the standard environment
(`DOCKER_HOST`, `DOCKER_CERT_PATH`, `DOCKER_TLS_VERIFY`); on a manager node with
the default socket, no extra configuration is required.

## Build

```bash
make build                 # compiles ./cmd/swarm-hpa → bin/swarm-hpa
# or
go build -o bin/swarm-hpa ./cmd/swarm-hpa
```

Other useful targets: `make test`, `make test-race`, `make cover`, `make lint`,
`make vet`, `make run` (run `make help` for the full list).

## Run

Dry-run is **on by default** — the daemon logs intended actions without touching
any service. Start there:

```bash
./bin/swarm-hpa
# or, with flags through the Makefile:
make run ARGS="--log-level=debug"
```

Enable real mutations only when you are satisfied with the dry-run logs:

```bash
./bin/swarm-hpa --dry-run=false
```

The daemon is a single foreground process. It runs one reconcile loop on a fixed
interval and stops gracefully on `SIGINT` / `SIGTERM`.

## Mark a service for management

The daemon ignores every service that does not carry the opt-in label. Add the
`swarm.autoscaler.*` labels to bring a service under management:

```bash
docker service update \
  --label-add swarm.autoscaler.enabled=true \
  --label-add swarm.autoscaler.min=2 \
  --label-add swarm.autoscaler.max=10 \
  --label-add swarm.autoscaler.metric=cpu \
  --label-add swarm.autoscaler.target=70 \
  web
```

See [Configuration](configuration.md) for every label and its meaning.

## Verify it works

With `--log-level=debug`, a healthy startup logs the effective configuration and
the reconcile loop picking up your service:

```
level=INFO msg="effective configuration" config.dry_run=true config.metrics_provider=dockerstats ...
level=INFO msg="reconcile loop started" interval=15s
level=INFO msg="observed managed services" count=1
level=INFO msg="scaling decision" service=web current=2 desired=2 value=... target=70
```

While dry-run is enabled, any intended change is logged as `dry-run: would
scale` / `dry-run: would force-update (heal)` instead of being applied.

## Next steps

- [Configuration](configuration.md) — every flag, environment variable, and service label.
- [Metrics Providers](metrics-providers.md) — choose Docker stats or Prometheus per service.

## See Also

- [Configuration](configuration.md) — daemon and per-service settings.
- [Metrics Providers](metrics-providers.md) — how the scaling signal is measured.
