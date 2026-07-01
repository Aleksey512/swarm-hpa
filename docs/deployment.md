[← Development](development.md) · [Back to README](../README.md)

# Deployment

Package the daemon as a container image and run it on a Docker Swarm — opt-in,
dry-run by default, and least-privilege.

## Build the image

The image is a multi-stage, CGO-free static build on `alpine`, running as a
non-root user with the version embedded via `-ldflags`:

```bash
make docker-build                 # tags ghcr.io/aleksey512/swarm-hpa:<version> and :latest
# or directly:
docker build --build-arg VERSION="$(git describe --tags --always --dirty)" \
  -t ghcr.io/aleksey512/swarm-hpa:latest .

docker run --rm ghcr.io/aleksey512/swarm-hpa:latest --version
```

Override the image name with `IMAGE=...`:

```bash
make docker-build IMAGE=registry.example.com/swarm-hpa
```

## Publish

Pushing a `v*` git tag triggers `.github/workflows/release.yml`, which builds a
multi-arch image (`linux/amd64`, `linux/arm64`) and pushes it to **GHCR**:

```bash
git tag v1.0.0 && git push origin v1.0.0   # → ghcr.io/<owner>/swarm-hpa:1.0.0 + :latest
```

For a manual push: `make docker-push` (after `make docker-build`).

## Deploy to Swarm

The daemon **must run on a manager node** — the services/tasks/nodes APIs it
uses are manager-only. It is **dry-run by default**: it logs the actions it would
take without applying them.

```bash
IMAGE=ghcr.io/aleksey512/swarm-hpa TAG=latest \
  docker stack deploy -c deploy/stack.yml swarm-hpa
```

Enable real scaling/healing only when you are ready:

```yaml
# in deploy/stack.yml → services.swarm-hpa.environment
DRY_RUN: "false"
```

## Least-privilege

Two stack files ship in `deploy/`:

| File | Socket access | Daemon user | Use when |
|------|---------------|-------------|----------|
| `deploy/stack.yml` | direct bind of `/var/run/docker.sock` | root (socket ownership) | quick start, single manager |
| `deploy/stack.proxy.yml` | `tecnativa/docker-socket-proxy` over TCP | **non-root** (65532) | hardened / production |

> **The `:ro` on a socket bind does NOT restrict the Docker API.** A read-only
> mount only makes the socket *file* read-only; `ServiceUpdate` (scale/heal)
> still works through it. For a genuinely restricted API surface, use the proxy.

The proxy variant (`deploy/stack.proxy.yml`) exposes only what the daemon calls
and lets it run as a non-root user with no socket mount at all:

| Setting | Why |
|---------|-----|
| `SERVICES=1` | `ServiceList` / `ServiceInspect` / `ServiceUpdate` |
| `TASKS=1` | `TaskList` |
| `NODES=1` | `NodeList` |
| `POST=1` | **required** — `ServiceUpdate` (scale/heal) is an HTTP `POST` |

Only the proxy needs the manager socket; `swarm-hpa` reaches it over the overlay
network via `DOCKER_HOST=tcp://docker-socket-proxy:2375` and can run on any node.

## Hardening recap

Both stacks apply: `cap_drop: [ALL]`, `security_opt: [no-new-privileges:true]`,
`read_only: true` rootfs (the daemon writes nothing to disk), resource limits,
and `DRY_RUN=true` by default. The proxy variant additionally runs the daemon as
non-root (uid 65532).

## Configuration

All tuning is via environment variables (poll interval, cooldowns, metric
provider, `METRICS_ADDR` — default `:9095`, etc.). See
[Configuration](configuration.md) for the full flag/env reference; the stack
files show the common ones inline.

## Upgrade & rollback

The daemon is stateless (in-memory cooldowns reset on restart; it re-observes
before acting), so rolling a new image is safe:

```bash
# upgrade to a new tag
docker service update --image ghcr.io/aleksey512/swarm-hpa:1.1.0 swarm-hpa_swarm-hpa
# or redeploy the whole stack
TAG=1.1.0 docker stack deploy -c deploy/stack.yml swarm-hpa

# rollback to the previously deployed spec
docker service rollback swarm-hpa_swarm-hpa
```

## See Also

- [Development](development.md) — build, test, and the CI pipeline.
- [Configuration](configuration.md) — every flag and environment variable.
- [Observability](observability.md) — the daemon's own `/metrics` endpoint.
