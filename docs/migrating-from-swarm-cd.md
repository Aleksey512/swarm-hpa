[← GitOps stack sync](gitops.md) · [Back to README](../README.md)

# Migrating from swarm-cd

`swarm-hpa` can replace [`m-adawi/swarm-cd`](https://github.com/m-adawi/swarm-cd) for
in-cluster GitOps deploys — and because the same process also autoscales, it removes
the swarm-cd↔autoscaler replicas fight entirely. This guide takes you from swarm-cd
to swarm-hpa without touching your stack configs, and is reversible.

## Why migrate

- **No two-controller fight.** swarm-cd runs `docker stack deploy` on an interval,
  which always sets `replicas` and resets whatever the autoscaler scaled to.
  swarm-hpa folds GitOps into the autoscaler process: before each deploy it
  **carries forward** the live replica count of every `swarm.autoscaler.enabled`
  service (clamped to `[min,max]`). The GitOps re-apply becomes a no-op for the
  replica field. ([GitOps stack sync](gitops.md#how-it-works).)
- **One binary, more jobs.** Stuck-task healing (force-updates tasks left pending
  after a node recovers) and load-aware rebalancing come for free.
- **Observability built in.** `/metrics` for sync/autoscale/heal actions plus a
  read-only `GET /stacks` JSON + drift UI.

## Feature parity

| swarm-cd feature | swarm-hpa status | Notes |
|------------------|------------------|-------|
| Git sync (clone/pull, branch) | ✅ | go-git; per-repo locking. HTTP basic auth only (see below). |
| Compose templating (`values_file`) | ✅ | Go `text/template` over `{{.Values.*}}`. |
| SOPS decrypt (age/gpg) | ✅ | Env-selected backend (`SOPS_AGE_KEY_FILE`, `SOPS_GPG_*`); in-place decrypt. |
| Config/secret rotation (`auto_rotate`) | ✅ | Content-hash rename `<stack>-<name>-<hash>`. Global `--gitops-auto-rotate`. |
| Per-repo concurrency | ✅ | `--gitops-concurrency` (default 4); stacks sharing a repo serialize. |
| Autoscaler-aware deploy | ✅✅ | **The differentiator** — carry-forward; swarm-cd has nothing like it. |
| Status API / UI | ✅ | `GET /stacks` JSON + HTML UI on the metrics endpoint. |
| Drift detection | ✅ | On-demand, non-autoscaled replicas only. |
| Stuck-task healing | ✅ | Bonus — not a swarm-cd feature. |

**Not yet supported (be aware before migrating):**

- **Auth:** HTTP basic auth (`username` + `password` / `password_file`) only.
  SSH-auth and token/OAuth auth are **not** supported in this release. Public repos
  work with no auth.
- **Status persistence across restarts:** per-stack status is in-memory (resets on
  restart; re-derived on the next sync).

## Config mapping (drop-in)

swarm-hpa reads swarm-cd's `repos.yaml` and `stacks.yaml` **verbatim** — keep your
existing files, no renaming, no reshaping.

`repos.yaml`:

| swarm-cd field | swarm-hpa field | Maps to |
|----------------|-----------------|---------|
| `url` | `url` | `RepoConfig.URL` |
| `username` | `username` | `RepoConfig.Username` |
| `password` | `password` | `RepoConfig.Password` |
| `password_file` | `password_file` | `RepoConfig.PasswordFile` (first line, whitespace-trimmed) |

`stacks.yaml` (the key is the Swarm stack namespace):

| swarm-cd field | swarm-hpa field | Maps to |
|----------------|-----------------|---------|
| `repo` | `repo` | `StackConfig.Repo` (key into `repos.yaml`) |
| `branch` | `branch` | `StackConfig.Branch` (default `main`) |
| `compose_file` | `compose_file` | `StackConfig.ComposeFiles` — the scalar string form is swarm-cd-identical; swarm-hpa additionally accepts a list, and per-entry `pull_policy` / `overrides` (see below) |
| `values_file` | `values_file` | `StackConfig.ValuesFile` (optional template values) |
| `sops_files` | `sops_files` | `StackConfig.SopsFiles` (repo-relative) |
| `sops_secrets_discovery` | `sops_secrets_discovery` | `StackConfig.SopsSecretsDiscovery` (auto-discover from compose `secrets:`) |

**swarm-hpa-only `stacks.yaml` fields** (swarm-cd has no equivalent; all optional,
so an unmodified swarm-cd `stacks.yaml` keeps working):

| Field | What |
|-------|------|
| `pull_policy` | Per-stack `always` / `changed` override of the global image pull policy. Can also be set per `compose_file` entry. |
| `compose_file` as a list | Several compose files per stack, deployed in order as separate additive `docker stack deploy` calls. |
| `overrides` (inside a `compose_file` entry) | Compose files merged into that entry's deploy via extra `-c` flags (`-c base.yml -c prod.yml`). Swarm's answer to compose's unsupported `include:`. See [Compose overrides](gitops.md#compose-overrides-merged-deploy). |

**swarm-hpa-only knobs** (swarm-cd has no equivalent; these are extra):

| Flag | Env | Default | What |
|------|-----|---------|------|
| `--gitops-interval` | `GITOPS_INTERVAL` | `120s` | Sync loop period. |
| `--gitops-concurrency` | `GITOPS_CONCURRENCY` | `4` | Max stacks synced in parallel. |
| `--gitops-pull-policy` | `GITOPS_PULL_POLICY` | `always` | `always` / `changed` image resolution. |
| `--gitops-auto-rotate` | `GITOPS_AUTO_ROTATE` | `true` | Config/secret content-hash rotation. |

See [Configuration](configuration.md) for the full flag/env reference.

## Step-by-step cut-over

The configs are not modified at any point, so this is safe to run alongside swarm-cd
in dry-run first.

1. **Stop swarm-cd** — its job is now done by swarm-hpa's manager. (Leave it stopped
   for the rest of the steps; you can restart it to roll back — see below.)
2. **Deploy swarm-hpa** where swarm-cd ran (a manager node). The bundled example
   deploys both roles from one image:
   ```bash
   INGEST_TOKEN=$(openssl rand -hex 16) \
   IMAGE=ghcr.io/aleksey512/swarm-hpa TAG=latest \
     docker stack deploy -c deploy/stack.yml swarm-hpa
   ```
   (`deploy/stack.yml` — direct socket; `deploy/stack.proxy.yml` for a least-privilege
   `docker-socket-proxy`. Dry-run is **on by default**.)
3. **Point swarm-hpa at your existing swarm-cd config** and keep dry-run on:
   ```bash
   swarm-hpa --gitops \
     --gitops-configs-path=/etc/swarm-cd \
     --metrics-addr=:9095 \
     --dry-run
   ```
   `repos.yaml` and `stacks.yaml` under `/etc/swarm-cd` are read as-is.
4. **Set the SOPS env** exactly as you did for swarm-cd — `SOPS_AGE_KEY_FILE` (age)
   or `SOPS_GPG_PRIVATE_KEY_FILE` / `SOPS_GPG_PRIVATE_KEY` (gpg). swarm-hpa does not
   parse these itself; the sops library picks the backend from env.
5. **Confirm in dry-run.** Watch the logs for the intended deploy per stack and the
   "preserved replicas" lines, and check the live view:
   ```bash
   curl http://<manager>:9095/stacks   # JSON status + drift
   # or open http://<manager>:9095/    # read-only HTML UI
   ```
   Verify each stack shows `ok: true` and that autoscaled services keep their live
   replica count (drift excludes them by design).
6. **Cut over** — disable dry-run:
   ```bash
   swarm-hpa --gitops --gitops-configs-path=/etc/swarm-cd --dry-run=false
   ```
   (or `DRY_RUN=false`). The first real deploy carries autoscaled replicas forward
   instead of resetting them.
7. **Observe.** `/metrics` exposes `sync_total`, `deploys_total`, `sync_errors_total`,
   `last_sync_timestamp_seconds`; `/stacks` shows per-stack health + drift.

## Rollback

The migration touches no config and no Swarm stacks owned by swarm-cd, so rollback is
just reversing the process:

1. Stop swarm-hpa (`docker stack rm swarm-hpa`, or stop the process).
2. Restart swarm-cd against the **same** `repos.yaml` / `stacks.yaml`.
3. swarm-cd resumes syncing from the same Git state.

> During the brief overlap neither tool mutates the other's state: swarm-hpa carries
> autoscaled replicas forward, and swarm-cd simply re-applies the compose. If you run
> both at once intentionally, expect swarm-cd to reset autoscaled replicas on its
> interval — that is the conflict swarm-hpa exists to remove.

## See Also

- [GitOps stack sync](gitops.md) — the sync loop, carry-forward, drift, and the status API.
- [Configuration](configuration.md) — full daemon flags/env and `swarm.autoscaler.*` labels.
- [Observability](observability.md) — the `/metrics` catalog.
- [Deployment](deployment.md) — running the manager/agent pair.
