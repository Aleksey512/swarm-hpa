[← Agents & Rebalancing](agents-and-rebalancing.md) · [Back to README](../README.md) · [Observability →](observability.md)

# GitOps stack sync (autoscaler-aware)

swarm-hpa can deploy your stacks from Git itself — an in-cluster GitOps loop that
**replaces a third-party swarm-cd**. It is opt-in (`--gitops`, off by default),
dry-run-aware, and drop-in compatible with swarm-cd's `repos.yaml` / `stacks.yaml`.

Because the **same process** runs both this GitOps loop and the autoscaler, a
deploy **never clobbers a replica count the autoscaler just set** — the
swarm-cd↔HPA conflict is solved by construction (see [How it works](#how-it-works)).

## Quick example

```yaml
# repos.yaml
my-app:
  url: https://github.com/org/my-app.git
```

```yaml
# stacks.yaml
web:
  repo: my-app
  branch: main
  compose_file: compose.yaml
```

```bash
./bin/swarm-hpa --gitops --gitops-configs-path=/etc/swarm-hpa --dry-run=false
```

The manager now pulls `my-app` on `main`, renders `compose.yaml`, and deploys the
`web` stack every `--gitops-interval` (default 120s). Services marked
`swarm.autoscaler.enabled=true` keep their autoscaled replica count across syncs.

## Configuration

### Files (drop-in swarm-cd format)

`repos.yaml` — one entry per Git repository:

| Field | Required | Description |
|-------|----------|-------------|
| `url` | yes | Git URL (HTTPS). Public repos need no auth. |
| `username` | for auth | Username for HTTP basic auth. |
| `password` | for auth | Password / token. |
| `password_file` | for auth | Path to a file whose first line is the password / token (whitespace-trimmed). |

`stacks.yaml` — one entry per stack (the key is the Swarm stack namespace):

| Field | Required | Description |
|-------|----------|-------------|
| `repo` | yes | Key into `repos.yaml`. |
| `branch` | no | Branch to track (default `main`). |
| `compose_file` | yes | Path to the compose file inside the repo. |
| `values_file` | no | Optional; the compose file is rendered as a Go `text/template` with `{{.Values.*}}` from this file. |

Both files are read from the `--gitops-configs-path` directory (default `.`).

### Flags & environment

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--gitops` | `GITOPS_ENABLED` | `false` | Enable the GitOps stack-sync loop (manager mode only). |
| `--gitops-configs-path` | `GITOPS_CONFIGS_PATH` | `.` | Directory containing `repos.yaml` and `stacks.yaml`. |
| `--gitops-repos-path` | `GITOPS_REPOS_PATH` | `repos` | Root under which each repo is cloned (`<path>/<repo>`). |
| `--gitops-interval` | `GITOPS_INTERVAL` | `120s` | Stack-sync loop period. |
| `--gitops-pull-policy` | `GITOPS_PULL_POLICY` | `always` | `--resolve-image` mode for `docker stack deploy`: `always` or `changed`. |

The loop honors the global `--dry-run` / `DRY_RUN` flag (on by default).

## How it works

The classic GitOps↔autoscaler conflict: a tool like swarm-cd runs
`docker stack deploy` (a full re-apply from the compose file) every interval, and
`docker stack deploy` always sets `replicas` — so it resets whatever the
autoscaler just scaled to, causing oscillation and capacity loss. "Just omit
`replicas`" does not help (it defaults to 1).

swarm-hpa resolves it by **folding GitOps into the same process as the
autoscaler**. Before each `docker stack deploy`, the deploy step reads the live
services of the stack and **carries forward** the replica count of every
`swarm.autoscaler.enabled=true` service — clamped to that service's `[min, max]`
from the compose labels. Non-autoscaled services stay Git-owned; `mode: global`
services are skipped.

```
HPA scales web 3 → 7
   │
swarm-hpa GitOps tick
   ├─ git pull → render compose
   ├─ carry-forward: web.deploy.replicas = clamp(live 7, min, max) = 7
   └─ docker stack deploy  →  web replicas stays 7  (NOT reset to 3)
```

So the GitOps re-apply is a no-op for the autoscaler's replica field — no
two-controller fight. The carry-forward logic is isolated in
`internal/adapter/stackdeploy/carryforward.go` so a future native granular deploy
could drop it.

## Migrating from swarm-cd

1. Stop swarm-cd (its work is now done by swarm-hpa's manager).
2. Point swarm-hpa at your existing config:
   ```bash
   ./bin/swarm-hpa --gitops --gitops-configs-path=/path/to/your/swarm-cd-configs --dry-run=false
   ```
   `repos.yaml` and `stacks.yaml` are read as-is.
3. Re-create any swarm-cd-only features you relied on — **SOPS secret decryption,
   config/secret rotation, the web UI, and the status API are not in the v0.4.0
   foundation slice**; they land in later releases. Until then, decrypt secrets
   and rotate configs out of band (or keep them unencrypted in the repo).
4. Start in dry-run, confirm the logged deploy intents and preserved replica
   counts, then disable dry-run.

> The foundation slice (v0.4.0) covers git sync, compose rendering, the
> autoscaler-aware deploy, config loading, and the loop. SOPS/rotation,
> concurrency, the status/UI, drift detection, and the full migration guide are
> follow-ups.

## Dry run

With `--dry-run` (the default), the loop logs each intended deploy and records a
`sync_suppressed_total{reason="dry_run"}` metric **without** calling
`docker stack deploy`. Flip `--dry-run=false` (or `DRY_RUN=false`) to apply.

## See Also

- [Configuration](configuration.md) — daemon flags/env and `swarm.autoscaler.*` labels.
- [Agents & Rebalancing](agents-and-rebalancing.md) — the manager process this loop folds into.
- [Observability](observability.md) — the GitOps sync metrics (`sync_total`, `deploys_total`, `last_sync_timestamp_seconds`, …).
