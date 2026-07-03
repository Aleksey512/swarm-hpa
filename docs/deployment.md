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

Pushing a `v*` git tag triggers `.github/workflows/release.yml`, which builds one
multi-arch image (`linux/amd64`, `linux/arm64`) and pushes it to **both**
registries — **GHCR** (primary) and **Docker Hub** (mirror):

```bash
git tag v0.1.0 && git push origin v0.1.0
# → ghcr.io/aleksey512/swarm-hpa:0.1.0 + :0.1 + :latest
# → docker.io/mrframe/swarm-hpa:0.1.0 + :0.1 + :latest
```

**Required secrets.** The Docker Hub push needs two GitHub Actions repository
secrets (Settings → Secrets and variables → Actions), or the release fails at its
Docker Hub login step:

| Secret | Value |
|--------|-------|
| `DOCKERHUB_USERNAME` | Docker Hub login (`mrframe`) |
| `DOCKERHUB_TOKEN` | a Docker Hub **Read & Write** access token |

GHCR needs no extra secret — it authenticates with the built-in `GITHUB_TOKEN`.

**Verify the release** once the workflow is green — pull and run `--version` from
each registry:

```bash
docker run --rm ghcr.io/aleksey512/swarm-hpa:0.1.0 --version
docker run --rm docker.io/mrframe/swarm-hpa:0.1.0 --version
```

For a manual push: `make docker-push` (after `make docker-build`), optionally with
`IMAGE=docker.io/mrframe/swarm-hpa` to target Docker Hub instead of GHCR.

## Deploy to Swarm

The stacks deploy **two services** from one image, split by `MODE` (see
[Agents & Rebalancing](agents-and-rebalancing.md)):

| Service | `MODE` | Placement | Role |
|---------|--------|-----------|------|
| `swarm-hpa-manager` | `manager` | one replica, **manager-pinned** (services/tasks/nodes APIs are manager-only) | reconcile loop (autoscale + heal + rebalance), ingest endpoint, `/metrics` |
| `swarm-hpa-agent` | `agent` | `mode: global` — exactly one per node | reports LOCAL per-task/-node load to the manager; never mutates Swarm |

Agents authenticate to the manager's ingest endpoint with a shared
**`INGEST_TOKEN`** — an env-only secret both services read. Generate one at deploy
time (the stacks `?`-require it, so the deploy fails fast if unset):

```bash
INGEST_TOKEN=$(openssl rand -hex 16) \
IMAGE=ghcr.io/aleksey512/swarm-hpa TAG=latest \
  docker stack deploy -c deploy/stack.yml swarm-hpa
```

The stacks are **dry-run by default** — the manager logs the actions it would
take without applying them, and set `METRICS_PROVIDER=agents` so autoscaling reads
the whole fleet. Enable real scaling/healing/rebalancing only when you are ready:

```yaml
# in deploy/stack.yml → services.swarm-hpa-manager.environment
DRY_RUN: "false"
```

## Least-privilege

Two stack files ship in `deploy/`, both now split into manager + agent:

| File | Manager socket access | Manager user | Use when |
|------|-----------------------|--------------|----------|
| `deploy/stack.yml` | direct bind of `/var/run/docker.sock` | root (socket ownership) | quick start, single manager |
| `deploy/stack.proxy.yml` | `tecnativa/docker-socket-proxy` over TCP | **non-root** (65532) | hardened / production |

> **The `:ro` on a socket bind does NOT restrict the Docker API.** A read-only
> mount only makes the socket *file* read-only; `ServiceUpdate` (scale/heal/
> rebalance) still works through it. For a genuinely restricted API surface on the
> **manager**, use the proxy.

The proxy variant (`deploy/stack.proxy.yml`) exposes only what the **manager**
calls and lets it run as a non-root user with no socket mount at all:

| Setting | Why |
|---------|-----|
| `SERVICES=1` | `ServiceList` / `ServiceInspect` / `ServiceUpdate` |
| `TASKS=1` | `TaskList` |
| `NODES=1` | `NodeList` |
| `POST=1` | **required** — `ServiceUpdate` (scale/heal/rebalance) is an HTTP `POST` |

Only the proxy needs the manager socket; `swarm-hpa-manager` reaches it over the
overlay via `DOCKER_HOST=tcp://docker-socket-proxy:2375` and can run on any node.

### The agent needs a DIRECT local socket

In **both** stack files the agent binds `/var/run/docker.sock` directly (read-only)
— it cannot use the socket proxy. The agent's whole job is to read
`ContainerStats` (and `Info`) from the **local** daemon, and the socket proxy
**does not expose `ContainerStats`**. This is a deliberate tradeoff: the agent is
**read-only** (it never scales/heals/updates anything), so its blast radius is
local stats collection. The manager cannot read remote-node stats — which is the
entire reason agents exist.

## Hardening recap

Both stacks apply to **each** service: `cap_drop: [ALL]`,
`security_opt: [no-new-privileges:true]`, `read_only: true` rootfs (nothing is
written to disk), resource limits, and `DRY_RUN=true` on the manager by default.
In `stack.proxy.yml` the manager runs non-root (uid 65532) via the proxy; the
agent runs as root only to open its local socket, otherwise identically confined.

## Configuration

All tuning is via environment variables. The **manager** takes the reconcile,
metric-provider, ingest (`INGEST_ADDR`, `INGEST_TOKEN`, `AGENT_STALE_TIMEOUT`),
and rebalance (`REBALANCE_THRESHOLD`, `REBALANCE_COOLDOWN`) settings; the
**agent** takes `MANAGER_URL`, `REPORT_INTERVAL`, and optional `NODE_ID`. See
[Configuration](configuration.md) for the full flag/env reference; the stack
files show the common ones inline.

## Migration from v0.2.0

Fully backward compatible. The default `mode=manager` with
`dockerstats`/`prometheus` behaves **exactly** as v0.2.0 — upgrading a
single-node / manager-only deployment needs **no changes**. To go cluster-wide,
adopt the two-service stack:

| You want | Do |
|----------|-----|
| Same behavior as before | Keep a manager-only deployment; just bump the image tag. |
| Cluster-wide stats autoscaling | Deploy `swarm-hpa-agent` (`mode: global`) and set `METRICS_PROVIDER=agents` on the manager. |
| Load-aware rebalancing | Deploy agents (above) and label services `swarm.autoscaler.rebalance=true`. |

The shipped `deploy/stack.yml` / `deploy/stack.proxy.yml` already include both
services and set `METRICS_PROVIDER=agents`; drop the `swarm-hpa-agent` service (and
revert `METRICS_PROVIDER`) if you want the v0.2.0 manager-only behavior.

## Upgrade & rollback

Both services are stateless (in-memory cooldowns reset on restart; the manager
re-observes before acting), so rolling a new image is safe. Stack service names
are `<stack>_<service>` — e.g. `swarm-hpa_swarm-hpa-manager`:

```bash
# upgrade to a new tag
docker service update --image ghcr.io/aleksey512/swarm-hpa:1.1.0 swarm-hpa_swarm-hpa-manager
docker service update --image ghcr.io/aleksey512/swarm-hpa:1.1.0 swarm-hpa_swarm-hpa-agent
# or redeploy the whole stack (INGEST_TOKEN must be set again)
INGEST_TOKEN=$(openssl rand -hex 16) TAG=1.1.0 \
  docker stack deploy -c deploy/stack.yml swarm-hpa

# rollback a service to its previously deployed spec
docker service rollback swarm-hpa_swarm-hpa-manager
```

## See Also

- [Agents & Rebalancing](agents-and-rebalancing.md) — the manager/agent architecture behind these stacks.
- [Development](development.md) — build, test, and the CI pipeline.
- [Configuration](configuration.md) — every flag and environment variable.
- [Observability](observability.md) — the daemon's own `/metrics` endpoint.
