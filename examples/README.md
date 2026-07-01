[← Back to README](../README.md) · [Configuration](../docs/configuration.md) · [Metrics Providers](../docs/metrics-providers.md)

# Examples

Runnable, self-contained demos of the three things swarm-hpa does: CPU
autoscaling, Prometheus-driven autoscaling, and stuck-task healing. Each example
deploys a **target workload**; you run the daemon alongside it and watch it
decide.

| Example | Demonstrates | Provider |
|---------|--------------|----------|
| [`cpu-autoscale/`](cpu-autoscale/) | Scale out under CPU load | Docker stats |
| [`prometheus-autoscale/`](prometheus-autoscale/) | Scale on requests/sec per replica | Prometheus (PromQL) |
| [`healer/`](healer/) | Recover a task stuck `pending` after a node recovers | — |

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
level=INFO msg="scaling decision" service=demo_web current=2 desired=4 value=... target=50
level=INFO msg="dry-run: would scale" service=demo_web ...
```

Enable real scaling once the logs look right: `make run ARGS="--dry-run=false"`.
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

---

## Safety & teardown

- Everything is **dry-run until `--dry-run=false`** — the daemon logs `would …`
  and touches nothing.
- Only services carrying `swarm.autoscaler.enabled=true` are ever considered.
- Remove a demo with `docker stack rm <stack>` (`demo`, `promdemo`, `healer`).

## See also

- [Configuration](../docs/configuration.md) — every flag, env var, and service label.
- [Metrics Providers](../docs/metrics-providers.md) — Docker stats vs Prometheus, `$SERVICE`/`$SERVICE_ID`, PromQL result rules.
- [Getting Started](../docs/getting-started.md) — build and first run.
