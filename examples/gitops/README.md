[← Back to Examples](../README.md) · [GitOps docs](../../docs/gitops.md) · [Configuration](../../docs/configuration.md)

# GitOps stack sync — carry-forward + drift

A self-contained demo of swarm-hpa's GitOps loop: it deploys a stack **from a
local git repo** into Swarm, and — because the **same process** runs the
autoscaler — a deploy **never clobbers a replica count the autoscaler set**
(carry-forward). The read-only status surface at `GET /stacks` shows drift.

It is offline: no GitHub, no external git host. The app lives in
[`app-repo/`](app-repo/) (a two-service compose file); `init-repo.sh` turns it
into a real git source and wires `repos.yaml` at it.

The two services exist on purpose:

| Service | Opt-in? | What the demo shows |
|---------|---------|---------------------|
| `web`   | `swarm.autoscaler.enabled=true` | **Carry-forward** — a sync does NOT reset its replica count. |
| `cache` | plain (no labels)              | **Drift** — tracked at `GET /stacks`; a sync reconciles it to compose. |

## Prerequisites

```bash
docker swarm init          # a single-node swarm is enough
make build                 # builds ./bin/swarm-hpa
```

Dry-run is **on by default** — the GitOps loop logs sync intent and touches
nothing until you pass `--dry-run=false`.

## 1. Make the app a git source

`init-repo.sh` commits [`app-repo/`](app-repo/) as a git repo and writes the
absolute `file://` URL into [`repos.yaml`](repos.yaml):

```bash
bash examples/gitops/init-repo.sh
```

It prints the resolved URL and the suggested `make run` line. (To use a real
remote instead, edit `url:` in `repos.yaml` — see the file's header comment.)

## 2. Dry-run — watch the sync intent

Run the daemon with GitOps enabled, dry-run ON, debug logs:

```bash
make run ARGS="--gitops --gitops-configs-path=examples/gitops --gitops-repos-path=/tmp/swarm-hpa-repos --log-level=debug"
```

Expected logs (the loop syncs **immediately on startup**, then every
`--gitops-interval`, default 120s):

```
level=INFO  msg="gitops loop started" interval=2m0s stacks=1 dry_run=true
level=DEBUG msg="gitops: syncing stacks" stacks=1 concurrency=4
level=DEBUG msg="gitops: acquired repo lock" repo=demoapp stack=demoapp
level=INFO  msg="gitops: dry-run; would decrypt/rotate/deploy stack" stack=demoapp revision=<rev>
```

Dry-run skips decrypt/rotate/deploy, so the `demoapp` stack is **not** created
yet — you are only watching intent. Stop the daemon (`Ctrl-C`) when the logs
look right.

## 3. Enable real sync — the stack deploys

Add `--dry-run=false` and restart:

```bash
make run ARGS="--gitops --gitops-configs-path=examples/gitops --gitops-repos-path=/tmp/swarm-hpa-repos --dry-run=false --log-level=debug"
```

Expected:

```
level=INFO msg="gitops: deploying stack" stack=demoapp revision=<rev>
level=INFO msg="gitops: stack synced"   stack=demoapp revision=<rev>
```

```bash
docker service ls                  # demoapp_web (autoscaled) + demoapp_cache (plain)
```

## 4. Carry-forward demo (the headline)

Goal: prove a `docker stack deploy` does **not** reset the autoscaled replica
count. `compose.yaml` sets `web` to `replicas: 2`.

1. Simulate the autoscaler having scaled `web` out:
   ```bash
   docker service scale demoapp_web=5
   docker service ls                # demoapp_web is now 5/5
   ```
2. The loop skips deploy when the git revision is unchanged
   (`gitops: no changes; skipping deploy`). So **advance the revision** to force
   a real deploy:
   ```bash
   git -C examples/gitops/app-repo commit --allow-empty -m "tickle sync"
   ```
3. Restart the daemon (restart = immediate sync; new revision → deploy fires),
   again with `--dry-run=false`.
4. Observe — `web` was **not** reset to 2:
   ```bash
   docker service inspect demoapp_web --format '{{.Spec.Mode.Replicated.Replicas}}'   # 5
   docker service ls                                                                  # demoapp_web 5/5
   ```

That is carry-forward: before `docker stack deploy`, the daemon rewrote
`web`'s `deploy.replicas` to the **live** count (clamped to `[min=2, max=8]`),
so the deploy is a no-op for `web`'s replicas. A plain `docker stack deploy`
(the old swarm-cd behavior) would have reset `web` to 2 and fought the
autoscaler. The plain `cache` service, by contrast, is reset to its compose
value on every sync.

> **Note:** with `--dry-run=false` the autoscaler is also live and may move
> `web` on CPU between syncs — that is separate from carry-forward. What matters
> here is that the **deploy** did not reset `web` to the compose value.

## 5. Drift at `GET /stacks`

The GitOps status surface rides on the metrics listener
(`http://localhost:9095/` by default):

- `GET /stacks` — JSON (per-stack revision, status, drift)
- `GET /` — static HTML UI (refresh to update)

```bash
curl -s http://localhost:9095/stacks | jq        # or open http://localhost:9095/ in a browser
```

Drift is computed **only for non-autoscaled services** — the autoscaled `web` is
intentionally absent (its replica divergence is expected, not drift). So only
`cache` appears. Demo it:

1. Push `cache` out of sync:
   ```bash
   docker service scale demoapp_cache=4
   ```
2. Refresh `/stacks` — `cache` shows **drifted** (desired 2, live 4).
3. Trigger a sync (empty commit + restart as in step 4, or wait up to
   `--gitops-interval`). The deploy resets `cache` to 2 → drift resolves.

## Teardown

```bash
docker stack rm demoapp
rm -rf /tmp/swarm-hpa-repos        # the cloned repos root from --gitops-repos-path
```

## How it works

swarm-hpa folds the GitOps loop into the manager process alongside the
autoscaler. Each tick: `git pull` → render compose → (decrypt/rotate) → for
every `swarm.autoscaler.enabled=true` service, set `deploy.replicas` to the live
count clamped to `[min,max]` → `docker stack deploy`. One process owns all Swarm
mutations, so sync and scale can't fight. Full reference (config fields,
templating, SOPS, swarm-cd migration):
[../../docs/gitops.md](../../docs/gitops.md).

## See also

- [GitOps stack sync](../../docs/gitops.md) — full config reference + how it works.
- [Migrating from swarm-cd](../../docs/migrating-from-swarm-cd.md) — cut-over guide.
- [Configuration](../../docs/configuration.md) — every flag, env var, and label.
- [Examples](../README.md) — the other demos (CPU/Prometheus autoscaling, healer, agents).
