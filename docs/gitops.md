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
| `sops_files` | no | sops-encrypted files (repo-relative) to decrypt before deploy. Ignored when `sops_secrets_discovery` is true. |
| `sops_secrets_discovery` | no | When true, auto-discover sops files from the compose's file-backed `secrets:` (and ignore `sops_files`). |

Both files are read from the `--gitops-configs-path` directory (default `.`).

### Flags & environment

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--gitops` | `GITOPS_ENABLED` | `false` | Enable the GitOps stack-sync loop (manager mode only). |
| `--gitops-configs-path` | `GITOPS_CONFIGS_PATH` | `.` | Directory containing `repos.yaml` and `stacks.yaml`. |
| `--gitops-repos-path` | `GITOPS_REPOS_PATH` | `repos` | Root under which each repo is cloned (`<path>/<repo>`). |
| `--gitops-interval` | `GITOPS_INTERVAL` | `120s` | Stack-sync loop period. |
| `--gitops-pull-policy` | `GITOPS_PULL_POLICY` | `always` | `--resolve-image` mode for `docker stack deploy`: `always` or `changed`. |
| `--gitops-auto-rotate` | `GITOPS_AUTO_ROTATE` | `true` | Rename file-backed configs/secrets to `<stack>-<name>-<hash>` so Swarm picks up changed content (swarm-cd `auto_rotate`). |
| `--gitops-concurrency` | `GITOPS_CONCURRENCY` | `4` | Max number of stacks synced in parallel. Stacks sharing a repo serialize, so effective parallelism is bounded by the number of distinct repos (`>= 1`). |

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
   ├─ decrypt sops secrets (in place)
   ├─ rotate configs/secrets → <stack>-<name>-<hash>
   ├─ carry-forward: web.deploy.replicas = clamp(live 7, min, max) = 7
   └─ docker stack deploy  →  web replicas stays 7  (NOT reset to 3)
```

So the GitOps re-apply is a no-op for the autoscaler's replica field — no
two-controller fight. The carry-forward logic is isolated in
`internal/adapter/stackdeploy/carryforward.go` so a future native granular deploy
could drop it.

## Secrets (SOPS)

Secrets (and any other files) can be [sops](https://github.com/getsops/sops)-encrypted
in the repo and decrypted in place before deploy. List the encrypted files per
stack, or auto-discover them from the compose `secrets:`:

```yaml
# stacks.yaml
web:
  repo: my-app
  branch: main
  compose_file: compose.yaml
  sops_files:
    - secrets/tls.crt
    - secrets/tls.key
  # OR: sops_secrets_discovery: true   # decrypt every file-backed secret
```

The age / gpg backend is chosen by the **sops library** from env, exactly like
swarm-cd: `SOPS_AGE_KEY_FILE` (age), or `SOPS_GPG_PRIVATE_KEY_FILE` /
`SOPS_GPG_PRIVATE_KEY` (gpg). swarm-hpa does not parse these itself.

> **Security:** decrypt is in-place — it overwrites the encrypted file with
> plaintext in the repo worktree under `--gitops-repos-path` (an ephemeral clone).
> Keep `--gitops-repos-path` on ephemeral storage. Decrypted contents are never
> logged. Decrypt is **skipped in dry-run** (it is a disk side effect).

## Config/secret rotation

Swarm configs and secrets are immutable, so a changed file is not picked up unless
its object name changes. With `--gitops-auto-rotate` (on by default, swarm-cd
`auto_rotate` parity), every file-backed `configs:` / `secrets:` object is renamed
to `<stack>-<name>-<content-hash>` (md5 of the decrypted content, first 8 hex)
before deploy — so editing a config/secret in Git and syncing mints a new object
and Swarm rolls it out. Disable with `--gitops-auto-rotate=false`.

## Concurrency

Each sync pass fans the configured stacks out across a **bounded worker pool** of
`--gitops-concurrency` workers (default `4`, must be `>= 1`). Stacks on
**different repos** sync in parallel; stacks that **share a repo serialize
end-to-end**. The reason: every repo has a single on-disk worktree
(`<repos-path>/<repo>`) that sops-decrypt and rotation mutate in place, so two
stacks on the same repo would otherwise interleave and corrupt that shared
worktree. Effective parallelism is therefore
`min(--gitops-concurrency, number of distinct repos)`.

This mirrors swarm-cd's per-repo concurrency model. Set `--gitops-concurrency=1`
to reproduce fully-sequential sync (handy for debugging). A fault on one stack —
a panic, a deploy error — never cancels the others; each stack is isolated and
the loop continues.

## Status, drift & UI

The manager exposes a read-only view of stack health on the metrics listener
(`--metrics-addr`, default `:9095`) — there is no extra flag; it is only present
when `--gitops` is enabled.

- **`GET /stacks`** — JSON, one entry per stack:
  ```json
  {
    "stacks": [{
      "name": "web", "revision": "abc12345", "ok": true,
      "last_sync": "2026-07-03T15:00:00Z", "deploy_count": 5,
      "desired_replicas": {"worker": 2},
      "drift": [{"service": "worker", "desired": 2, "live": 3, "drifted": true}],
      "drifted": true
    }]
  }
  ```
  `ok` is `false` with `error_stage`/`error_message` when the last sync failed
  (git, render, secrets, rotate, or deploy). `desired_replicas` is the
  non-autoscaled, non-global replica snapshot taken at the last render.
- **`GET /`** (or **`GET /ui`**) — a read-only HTML table of the same data
  (refresh to update; no client-side JavaScript).

### Drift

`drift` compares each stack's live Swarm replicas against `desired_replicas`,
**computed on demand per request** so it is always fresh:

- An **autoscaled** service (`swarm.autoscaler.enabled=true`) is never reported as
  drift — the HPA intentionally changes its replicas and carry-forward preserves
  them.
- A **global** service has no replica count to compare.
- A desired service **missing from live** (not deployed yet) counts as drift.

A failed Swarm read for one stack degrades just that stack's `drift` to a
`drift_error` note — it never turns the whole response into a 5xx.

## Migrating from swarm-cd

1. Stop swarm-cd (its work is now done by swarm-hpa's manager).
2. Point swarm-hpa at your existing config:
   ```bash
   ./bin/swarm-hpa --gitops --gitops-configs-path=/path/to/your/swarm-cd-configs --dry-run=false
   ```
   `repos.yaml` and `stacks.yaml` are read as-is.
3. SOPS secret decryption and config/secret rotation are supported — set
   `SOPS_AGE_KEY_FILE` / `SOPS_GPG_*` and `sops_files` / `sops_secrets_discovery`
   exactly as you did for swarm-cd. The stack status API, drift detection, and
   read-only UI are available on the metrics endpoint (`GET /stacks`, `GET /`).
4. Start in dry-run, confirm the logged deploy intents and preserved replica
   counts, then disable dry-run.

> v0.4.0 so far covers git sync, compose rendering, the autoscaler-aware deploy,
> SOPS secret decryption, config/secret rotation, config loading, the loop,
> bounded worker-pool concurrency, and the per-stack status API + drift UI.

## Dry run

With `--dry-run` (the default), the loop logs each intended deploy and records a
`sync_suppressed_total{reason="dry_run"}` metric **without** calling
`docker stack deploy`. Flip `--dry-run=false` (or `DRY_RUN=false`) to apply.

## See Also

- [Configuration](configuration.md) — daemon flags/env and `swarm.autoscaler.*` labels.
- [Agents & Rebalancing](agents-and-rebalancing.md) — the manager process this loop folds into.
- [Observability](observability.md) — the GitOps sync metrics (`sync_total`, `deploys_total`, `last_sync_timestamp_seconds`, …).
