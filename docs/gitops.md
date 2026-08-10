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

For a complete, self-contained demo (local git repo + carry-forward + drift at
`GET /stacks`), see [`examples/gitops/`](../examples/gitops/).

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
| `compose_file` | yes | One or more compose files inside the repo. Accepts a single path (string), a list of paths, or a list of `{file, overrides, pull_policy}` objects — see [Multiple compose files](#multiple-compose-files-per-stack) and [Compose overrides](#compose-overrides-merged-deploy). |
| `values_file` | no | Optional; the compose file is rendered as a Go `text/template` with `{{.Values.*}}` from this file. |
| `sops_files` | no | sops-encrypted files (repo-relative) to decrypt before deploy. Ignored when `sops_secrets_discovery` is true. |
| `sops_secrets_discovery` | no | When true, auto-discover sops files from the compose's file-backed `secrets:` (and ignore `sops_files`). |
| `pull_policy` | no | Overrides the global `--gitops-pull-policy` for this stack only: `always` or `changed`. Omit to use the global flag. Can also be set per compose file — see [Multiple compose files](#multiple-compose-files-per-stack). (swarm-hpa extension; no swarm-cd equivalent.) |

When set, a stack's `pull_policy` takes precedence over the global
`--gitops-pull-policy` / `GITOPS_PULL_POLICY` for that stack's deploys only.

Both files are read from the `--gitops-configs-path` directory (default `.`).

### Multiple compose files per stack

`compose_file` accepts three shapes — all backward compatible with the single-file form:

```yaml
# 1. Single file (swarm-cd parity / the default):
web:
  repo: my-app
  compose_file: compose.yaml

# 2. Several files — split for convenience (independent service groups):
web:
  repo: my-app
  compose_file:
    - services.yaml
    - monitoring.yaml

# 3. Several files with a per-file image pull policy:
web:
  repo: my-app
  compose_file:
    - file: app.yaml
      pull_policy: always      # refresh :dev on every sync
    - file: postgres.yaml
      pull_policy: changed     # don't re-pull a pinned postgres image
```

Mixed lists (some entries bare strings, some `{file, pull_policy}` objects) are
allowed; bare strings inherit the stack / global pull policy.

**How files are applied:** they are deployed **in list order, one
`docker stack deploy` each** — *not* merged. Each file is rendered and deployed
as-is, so **each file must be self-contained**: declare the `networks:` /
`volumes:` and any top-level `secrets:` / `configs:` it references. List order is
the deploy order (put shared infrastructure first).

**Per-file pull policy** — precedence is `file → stack → global`: a file's
`pull_policy` overrides the stack-level `pull_policy`, which overrides the global
`--gitops-pull-policy`. This is the only way to apply different `--resolve-image`
modes within one stack (e.g. the dev split above: app pulls `always`, postgres
pulls `changed`, via two sequential deploys).

**Two caveats** inherent to sequential `docker stack deploy`:

- **Deploys are additive, not pruning.** `docker stack deploy` does *not* remove
  services absent from a later file's deploy — the files accumulate into one
  stack. Likewise, *removing* a service from a compose file does not remove it
  from Swarm; remove it manually with `docker service rm`.
- **Sequential deploys are not transactional.** If file *k* fails, files
  *1…k−1* are already applied in Swarm (they are not rolled back). The stack is
  recorded as failed (see `GET /stacks`); fix the failing file and the next sync
  retries.

SOPS discovery and config/secret rotation run per file (each resolved against its
own file's directory), and autoscaler carry-forward applies to each deploy
individually — so a `swarm.autoscaler.enabled` service is never reset by either
deploy.

### Compose overrides (merged deploy)

Docker Compose's `include:` is a Compose-only feature — **Swarm does not support
it**. The Swarm-native way to layer compose files is to pass several `-c` flags to
one deploy, which is what `overrides` does:

```yaml
monitoring:
  repo: infra
  compose_file:
    - file: monitoring/base.yml
      overrides:
        - monitoring/prod.yml
        - monitoring/env.override.yml
      pull_policy: always
    - traefik.yml                      # separate, additive deploy
```

produces:

```bash
docker stack deploy -c monitoring/base.yml -c monitoring/prod.yml -c monitoring/env.override.yml monitoring
docker stack deploy -c traefik.yml monitoring
```

Use it to define a base stack once and re-parameterize it per environment —
change `environment:`, image tags, replicas — without duplicating the compose.

#### Overrides vs. multiple compose files

Both features live on `compose_file` and are easy to confuse. The difference is
whether the files are **merged** or **deployed separately**:

| | Several **entries** (previous section) | `overrides` **inside one entry** |
|---|---|---|
| Deploys | one `docker stack deploy` **per file** | **one** deploy for the whole group |
| Semantics | additive, **no merge** | docker/cli **compose merge**, later `-c` wins |
| Each file | must be **self-contained** | overrides need **not** be self-contained |
| Pull policy | per file | per **group** |

A base file plus its overrides is called a **merge group**: one group = one
`docker stack deploy`, and a stack may declare several groups.

#### Merge semantics

The daemon does not merge compose itself — it hands every file of the group to
`docker stack deploy` as its own `-c` flag and lets docker/cli apply its own merge
rules. Broadly: mappings deep-merge and most sequences are replaced wholesale, so
an override that sets `environment:` merges into the base's environment, while one
that sets `command:` replaces it. Consult Docker's own [merge
documentation](https://docs.docker.com/reference/compose-file/merge/) for the
exact per-key rules — they belong to Docker, not to this daemon, and delegating
keeps the behavior identical to running the `docker` CLI by hand.

Because the merge happens in docker/cli, an override only needs the keys it
changes:

```yaml
# monitoring/base.yml — the full stack
services:
  grafana:
    image: grafana/grafana:11.0.0
    environment:
      GF_SERVER_ROOT_URL: http://localhost:3000
    deploy:
      replicas: 1

# monitoring/prod.yml — only what differs
services:
  grafana:
    image: grafana/grafana:11.3.0
    environment:
      GF_SERVER_ROOT_URL: https://grafana.example.com
```

#### Pull policy

A merge group is one deploy, and `docker stack deploy` accepts exactly one
`--resolve-image`, so the pull policy is **per group** — set it on the group's
entry, not on individual overrides. Precedence is unchanged: `file → stack →
global`. To apply *different* pull policies within a stack, use separate
`compose_file` entries (see the previous section).

#### Autoscaled replicas survive the merge

Carry-forward is computed over the **merged view** of the group, and the live
replica count is written into every document that declares the service. So a
`swarm.autoscaler.enabled` service keeps its HPA count even when:

- the autoscaler labels are only in the base file and an override re-declares the
  service (or vice versa — labels only in an override are honored too);
- an override sets its own `deploy.replicas`.

An override may also change the policy: adding `swarm.autoscaler.enabled: "true"`
opts a service in (it then disappears from drift reporting, since the HPA owns its
replicas), and setting it to `"false"` opts it back out. `min`/`max` from the
merged labels are what clamp the carried-forward count.

#### Relative paths, secrets and rotation

Each file's relative `configs:` / `secrets:` `file:` paths resolve against **its
own directory**, exactly as they would if you ran `docker stack deploy` yourself.
An override may therefore live in a different directory than its base:

```yaml
compose_file:
  - file: base/compose.yml       # secrets resolve under base/
    overrides:
      - env/prod.yml             # secrets resolve under env/
```

SOPS discovery unions the file-backed secrets of every document in the group
(each against its own directory) and decrypts them once before the deploy;
config/secret rotation likewise runs per document.

#### Caveats

- Overrides are **rendered like any other compose file** — the stack's
  `values_file` templating applies to them too.
- The group is **all-or-nothing before deploy**: if any file of the group fails to
  read or render, the stack fails at the `render` stage and nothing is deployed.
- Between groups the [additive and non-transactional caveats](#multiple-compose-files-per-stack)
  still apply: a failing group leaves earlier groups already applied.
- An override that repeats the base file is rejected at config load.

The `GET /stacks` JSON and UI list each group's overrides under its base file, so
you can see exactly which files went into a deploy.

### Flags & environment

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--gitops` | `GITOPS_ENABLED` | `false` | Enable the GitOps stack-sync loop (manager mode only). |
| `--gitops-configs-path` | `GITOPS_CONFIGS_PATH` | `.` | Directory containing `repos.yaml` and `stacks.yaml`. |
| `--gitops-repos-path` | `GITOPS_REPOS_PATH` | `repos` | Root under which each repo is cloned (`<path>/<repo>`). |
| `--gitops-interval` | `GITOPS_INTERVAL` | `120s` | Stack-sync loop period. |
| `--gitops-pull-policy` | `GITOPS_PULL_POLICY` | `always` | `--resolve-image` mode for `docker stack deploy`: `always` or `changed`. Overridden per stack by `pull_policy` in `stacks.yaml`. |
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
could drop it. If a stack declares [multiple compose files](#multiple-compose-files-per-stack),
the render → decrypt → rotate → carry-forward → deploy sequence runs once per
file, in list order.

## Concurrency with the autoscaler (deploy retry)

The GitOps loop and the autoscaler / healer / rebalancer run as **concurrent
loops in the same manager process** against the same Swarm daemon. Carry-forward
(above) settles the *replica-value* fight. There is a second, narrower
interaction: while a `docker stack deploy` is updating the stack's services, the
autoscaler may `ServiceUpdate` one of them in the same window — and Swarm rejects
the loser with `update out of sequence` (an optimistic-concurrency guard on the
service's `Version.Index`).

This is transient and self-healing:

- **Fast retry.** The deploy is wrapped in a bounded retry (3 attempts, short
  backoff, context-aware). A re-deploy converges — `docker stack deploy` is
  idempotent and carry-forward keeps replicas clamped to `[min, max]` — so the
  collision resolves in seconds. The same guard also covers the autoscaler's own
  `ServiceUpdate` path, so either side recovers regardless of which "wins".
- **Outer safety net.** If a deploy still fails, the loop records it and
  **re-deploys on the next tick** (`--gitops-interval`, default 120s); the
  `last_sync` status and `sync_errors_total` metric reflect the transient
  failure until it clears.

Carry-forward prevents the autoscaler↔deploy *replica* conflict; the retry
prevents the *version-timing* conflict from surfacing as a failed sync. See
[Troubleshooting](#troubleshooting-update-out-of-sequence) if it does not clear.

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
      "name": "web", "repo": "my-app", "state": "syncing",
      "revision": "abc12345", "ok": true,
      "last_sync": "2026-07-03T15:00:00Z", "deploy_count": 5,
      "desired_replicas": {"worker": 2},
      "drift": [{"service": "worker", "desired": 2, "live": 3, "drifted": true}],
      "drifted": true
    }],
    "summary": {"stacks": 3, "repos": 2, "syncing": 1, "waiting": 1,
                "concurrency": 4, "max_parallel": 2}
  }
  ```
  `ok` is `false` with `error_stage`/`error_message` when the last sync failed
  (git, render, secrets, rotate, or deploy). `desired_replicas` is the
  non-autoscaled, non-global replica snapshot taken at the last render.
  `repo` is the key from `repos.yaml` that backs the stack. `state` is the
  **transient** sync state of the current pass — `syncing`, `waiting`, or empty
  (idle) — see [Live sync state](#live-sync-state). For a
  [multi-file stack](#multiple-compose-files-per-stack) or one using
  [compose overrides](#compose-overrides-merged-deploy), each entry also carries a
  `files` array — see [Per-file status](#per-file-status-multi-file-stacks). The
  top-level `summary` aggregates totals across all stacks (the `concurrency` /
  `max_parallel` pair is omitted when GitOps is off).
- **`GET /`** (or **`GET /ui`**) — a read-only HTML table of the same data
  (refresh to update; no client-side JavaScript).

### Live sync state

Each stack carries a transient `state` so you can see **which stacks sync in
parallel** right now:

- `syncing` — a sync pass is running for this stack (it holds the repo lock).
- `waiting` — the stack is blocked on its **shared-repo lock** because another
  stack on the same repo is syncing. One on-disk worktree per repo forces
  serialization (see [Concurrency](#concurrency));
  the `waiting` badge makes that contention visible.
- empty (idle) — between ticks; the `ok`/`error_stage` status reflects the last
  completed sync.

A `Snapshot` taken during a pass is one instant, so the badge is a best-effort
live view. The UI summary line (`N stacks · M repos · syncing: S · waiting: W ·
concurrency: C → ≤K parallel`) rolls these up, where `K` is
`min(--gitops-concurrency, distinct repos)`.

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

### Per-file status (multi-file stacks)

A [multi-file stack](#multiple-compose-files-per-stack) is deployed as one
`docker stack deploy` per `compose_file` entry, in order. The status surface
reflects that per entry, so a **partial failure is visible** (not collapsed into a
single stack-level error). Each stack's `files` array, in deploy order:

```json
"files": [
  {"file": "base.yaml", "overrides": ["prod.yaml"], "pull_policy": "always", "status": "ok"},
  {"file": "postgres.yaml", "pull_policy": "changed", "status": "failed", "error": "image pull denied"},
  {"file": "monitor.yaml", "pull_policy": "changed", "status": "skipped"}
]
```

- One array element = one **deploy**, i.e. one merge group. `overrides` lists the
  files merged into that deploy via extra `-c` flags, in order; the key is absent
  for a plain single-file deploy. The `status` state machine below is therefore
  per group, not per individual file.
- `status` is `ok` (deployed this sync), `failed` (with `error`), `skipped` (an
  earlier group failed, so this one was never reached), or pending (empty — the
  stack failed before the deploy stage).
- `pull_policy` is the effective policy used for that group's deploy
  (precedence file → stack → global) — this is where the per-file pull split
  (e.g. app `always`, postgres `changed`) shows up.

The HTML table has a **repo** column (the `repos.yaml` key backing each stack), a
**state** column with the live `● syncing` / `⏸ waiting` badge (idle otherwise),
and a **files** column with one line per group (path · pull policy · status), each
group's override files listed beneath it. On a partial failure the failing group
is red with its error, earlier groups are green (already applied — Swarm deploys
are additive and **not** transactional, so they are not rolled back), and later
groups are grey (`skipped`). The stack-level status still reads the failing stage.
Single-file stacks show one line. `files` is empty (the UI shows `—`) when the
stack failed before deploy or has never synced.

## Migrating from swarm-cd

swarm-hpa is a drop-in replacement for [swarm-cd](https://github.com/m-adawi/swarm-cd):
your existing `repos.yaml` / `stacks.yaml` are read as-is. See the dedicated
[swarm-cd migration guide](migrating-from-swarm-cd.md) for the feature-parity
matrix, the config field mapping, a step-by-step cut-over, and rollback.

> v0.4.0 covers git sync, compose rendering, the autoscaler-aware deploy, SOPS
> secret decryption, config/secret rotation, config loading, the loop, bounded
> worker-pool concurrency, the per-stack status API + drift UI, and the swarm-cd
> migration guide.

## Dry run

With `--dry-run` (the default), the loop logs each intended deploy and records a
`sync_suppressed_total{reason="dry_run"}` metric **without** calling
`docker stack deploy`. Flip `--dry-run=false` (or `DRY_RUN=false`) to apply.

## Troubleshooting: `update out of sequence`

A `docker stack deploy` log line like

```
deploy: stackdeploy: deploy "web": failed to update service web_core: Error
response from daemon: rpc error: code = Unknown desc = update out of sequence
```

means Swarm rejected a `ServiceUpdate` because the service changed between
docker/cli's read and its write. Two causes:

- **Transient / episodic** — the autoscaler/healer mutated the service
  mid-deploy. Expected and self-healing: the deploy retries in seconds, and the
  next sync tick re-applies if needed. Watch `sync_errors_total` /
  `deploys_total` on `/metrics`; occasional blips are normal, a steady stream is
  not. If one service flips constantly, check whether the autoscaler is flapping
  it (cooldown / stabilization windows).
- **Persistent** — a **second writer** outside this manager is also mutating the
  service. Look for:
  - another `swarm-hpa` manager instance (`docker service ls` — the manager is
    `replicas: 1`);
  - **swarm-cd still running** alongside swarm-hpa (see
    [Migrating from swarm-cd](migrating-from-swarm-cd.md) — run only one);
  - an external tool or human running `docker service update` /
    `docker stack deploy` (Portainer, CI, a shell).

## See Also

- [Configuration](configuration.md) — daemon flags/env and `swarm.autoscaler.*` labels.
- [Agents & Rebalancing](agents-and-rebalancing.md) — the manager process this loop folds into.
- [Observability](observability.md) — the GitOps sync metrics (`sync_total`, `deploys_total`, `last_sync_timestamp_seconds`, …).
