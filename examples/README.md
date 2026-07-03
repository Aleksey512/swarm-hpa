[← Back to README](../README.md) · [Configuration](../docs/configuration.md) · [Metrics Providers](../docs/metrics-providers.md)

# Examples

Runnable, self-contained demos of what swarm-hpa does: single-node CPU
autoscaling, Prometheus-driven autoscaling, stuck-task healing, and — via the
v0.3.0 manager/agent fleet — cluster-wide autoscaling and load-aware
rebalancing. Each example deploys a **target workload**; you run the daemon
alongside it (or, for the agents demo, inside the cluster) and watch it decide.

| Example | Demonstrates | Provider |
|---------|--------------|----------|
| [`cpu-autoscale/`](cpu-autoscale/) | Scale out under CPU load | Docker stats |
| [`prometheus-autoscale/`](prometheus-autoscale/) | Scale on requests/sec per replica | Prometheus (PromQL) |
| [`healer/`](healer/) | Recover a task stuck `pending` after a node recovers | — |
| [`agents/`](agents/) | Cluster-wide autoscaling + load-aware rebalancing | Agents (fleet) |

## The one rule that trips everyone up

The daemon reads the `swarm.autoscaler.*` labels from the **service** spec. In a
stack file that means they go under **`deploy.labels`** — *not* the top-level
`labels:` key (those are container labels, which the daemon never sees). A
service is managed only when it carries `enabled=true` **and** a valid policy
(`min`, `max`, `metric`, `target`). Misconfigured opt-ins are logged and skipped,
never acted on wrongly.

## Prerequisites

```bash
docker swarm init          # a single-node swarm is enough for cpu/prometheus demos
make build                 # builds ./bin/swarm-hpa
```

Dry-run is **on by default** — the daemon only logs intended actions until you
pass `--dry-run=false`. Start every example in dry-run, read the logs, then
enable real mutations.

## Running the daemon alongside an example

For local experimentation, run the daemon on the host pointing at your swarm:

```bash
# CPU / healer demos — Docker stats is the default provider:
make run ARGS="--log-level=debug"

# Prometheus demo — make the Prometheus URL available (published on :9090):
make run ARGS="--log-level=debug --prometheus-url=http://localhost:9090"
```

To run it *inside* the cluster instead, deploy [`deploy/stack.yml`](../deploy/stack.yml)
(or the least-privilege [`deploy/stack.proxy.yml`](../deploy/stack.proxy.yml)) and
set the same flags via env (`DRY_RUN`, `PROMETHEUS_URL`, `LOG_LEVEL`). See
[Deployment](../docs/deployment.md).

The **agents demo (§4) is different**: it runs the daemon *in-cluster* as a
`manager` service plus a per-node `agent` service (the agents must run on each
node to sample local stats), so there is no `make run` step — you deploy its
stack and read the manager's logs. It also needs a shared `INGEST_TOKEN` at
deploy time.

---

## 1. CPU autoscaling ([`cpu-autoscale/`](cpu-autoscale/))

Target: `registry.k8s.io/hpa-example` — every request runs a CPU-bound busy loop,
so load pushes per-task CPU past the `target` and the service scales out.

```bash
docker stack deploy -c examples/cpu-autoscale/stack.yml demo
make run ARGS="--log-level=debug"                 # terminal 2 — watch decisions (dry-run)
examples/cpu-autoscale/loadgen.sh                 # terminal 3 — generate load (http://localhost:8080/)
```

Expected daemon logs:

```
level=INFO msg="observed managed services" count=1
level=INFO msg="scaling decision" service=demo_web metric=cpu value=~50 target=20 current=2 desired=5
level=INFO msg="dry-run: would scale" service=demo_web from=2 to=5 direction=up
```

The demo `target=20` is intentionally below the per-task CPU ceiling (each task is
capped at `cpus: "0.50"`, so the metric plateaus near 50% under load) — that gap is
what makes the service visibly scale out. Enable real scaling once the logs look
right: `make run ARGS="--dry-run=false"`.
Docker stats is **local-node only** — on a multi-node swarm run the daemon where
the tasks are, or use the Prometheus example. Tear down: `docker stack rm demo`.

## 2. Prometheus autoscaling ([`prometheus-autoscale/`](prometheus-autoscale/))

Scales `promdemo_app` on **per-replica requests/sec** read from Prometheus.
Deploy it as **`promdemo`** — [`prometheus.yml`](prometheus-autoscale/prometheus.yml)
is coupled to that name (it scrapes `tasks.promdemo_app` and stamps
`service="promdemo_app"`, which the daemon's `$SERVICE` placeholder expands to).

```bash
docker stack deploy -c examples/prometheus-autoscale/stack.yml promdemo
make run ARGS="--dry-run=false --prometheus-url=http://localhost:9090 --log-level=debug"
examples/cpu-autoscale/loadgen.sh http://localhost:8081/    # drive rps (app is on :8081)
```

Expected: `msg="scaling decision" service=promdemo_app source=prometheus value=... target=50 desired=...`.
The PromQL query is per-replica rps (`desired = clamp(current × value/target, min, max)`);
its result must reduce to a single number or the tick is skipped. Only `app` opts
into Prometheus (via `source=prometheus`); the global provider can stay
`dockerstats`. Tear down: `docker stack rm promdemo`.

## 3. Stuck-task healer ([`healer/`](healer/))

Recovers a task left `pending` under a placement constraint after its node
recovers ([moby/moby#42215](https://github.com/moby/moby/issues/42215)).

```bash
docker node update --label-add nodeNum=1 <node-id>              # satisfy the constraint
docker stack deploy -c examples/healer/stack.yml healer
docker node update --availability drain  <node-id>             # task -> pending
docker node update --availability active <node-id>             # node back; task may stay pending (the bug)
make run ARGS="--dry-run=false --heal-threshold=1m --log-level=debug"
```

Expected: `msg="dry-run: would force-update (heal)"` (dry-run) or a real
force-update that unsticks the task. The heal signature is: placement constraints
present **and** a task `pending` ≥ `--heal-threshold` **and** a constraint-satisfying
node now Active+Ready. The service also carries a (no-op, `min=max=1`) autoscaler
policy because healing only acts on managed services. A 2+-node swarm reproduces
the stall most cleanly (see the notes in the stack file). Tear down:
`docker stack rm healer` and `docker node update --label-rm nodeNum <node-id>`.

## 4. Cluster-wide autoscaling + rebalancing ([`agents/`](agents/))

The v0.3.0 fleet in one stack: a `manager`, a per-node `agent` (global), and a
CPU-bound `web` workload opted into **both** cluster-wide autoscaling
(`source=agents`) and load-aware rebalancing (`swarm.autoscaler.rebalance=true`).
Agents push each node's local per-task CPU/memory to the manager, which
aggregates it across **all** nodes — the multi-node coverage the plain Docker
stats provider lacks (it only sees the manager's own node).

```bash
INGEST_TOKEN=$(openssl rand -hex 16) \
  docker stack deploy -c examples/agents/stack.yml agents
examples/cpu-autoscale/loadgen.sh http://localhost:8080/   # drive CPU at the target
docker service logs -f agents_manager                      # watch decisions (dry-run is ON)
```

Expected manager logs (`LOG_LEVEL=debug`, so the source-routing DEBUG line shows):

```
msg="agent connected" node=<id> name=<host>
msg="metrics: routing service" service=agents_web source=agents
msg="scaling decision" service=agents_web metric=cpu value=~50 target=20 current=2 desired=5
msg="dry-run: would scale" service=agents_web from=2 to=5 direction=up
msg="rebalance recommendation" service=agents_web from_node=<hot> to_node=<cold> ...   # multi-node skew only
```

Enable real actions with `docker service update --env-add DRY_RUN=false agents_manager`.
**Multi-node only:** on a single-node swarm `source=agents` equals `dockerstats`
(one agent = one node) and rebalancing has nothing to move. Rebalancing needs 2+
active nodes with a real CPU skew; it force-updates the **whole** service
(re-cycles all replicas — Swarm has no targeted task-move), so it is opt-in,
dry-run by default, and behind a long cooldown. Tear down: `docker stack rm agents`.

---

## Safety & teardown

- Everything is **dry-run until `--dry-run=false`** — the daemon logs `would …`
  and touches nothing.
- Only services carrying `swarm.autoscaler.enabled=true` are ever considered.
- Remove a demo with `docker stack rm <stack>` (`demo`, `promdemo`, `healer`, `agents`).

## See also

- [Configuration](../docs/configuration.md) — every flag, env var, and service label.
- [Metrics Providers](../docs/metrics-providers.md) — Docker stats vs Prometheus, `$SERVICE`/`$SERVICE_ID`, PromQL result rules.
- [Agents & Rebalancing](../docs/agents-and-rebalancing.md) — the manager/agent fleet, `source=agents`, and load-aware rebalancing (§4).
- [Getting Started](../docs/getting-started.md) — build and first run.
