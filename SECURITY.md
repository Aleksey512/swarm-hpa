# Security Policy

## Reporting a Vulnerability

**Please do not open a public GitHub issue for security problems.**

Report suspected vulnerabilities privately, in order of preference:

1. **GitHub Private Vulnerability Reporting** — use the *"Report a vulnerability"* button on the
   [**Security → Advisories**](https://github.com/Aleksey512/swarm-hpa/security/advisories/new) tab.
   This is the preferred channel: it is private, keeps an audit trail, and lets us publish a fixed
   release + GitHub Security Advisory together.
2. **Email** — send to the maintainer at `mickemickes59@gmail.com` with `[swarm-hpa security]` in the
   subject. If the report contains sensitive details, request a PGP key first.

Please include:

- A clear description of the issue and its **impact** (what an attacker could gain).
- Affected **version** (`swarm-hpa --version` or the image tag) and how you deployed it
  (direct socket, `docker-socket-proxy`, GitOps on/off).
- A minimal **reproduction** — config, labels, steps, logs (redact secrets/tokens).
- Any mitigations you have already applied.

### Response expectations

| Stage | Target |
|-------|--------|
| Acknowledgement of the report | within **72 hours** |
| Initial assessment (valid / needs-info / declined) | within **7 days** |
| Fix or mitigation for a confirmed high/critical issue | next release, target **within 30 days** |

This is a small project; timelines are best-effort but we will keep you informed. We practice
**coordinated disclosure** — please do not publish details until a fix is released, and we will
credit you in the advisory unless you prefer to remain anonymous.

## Supported Versions

Only the **latest** release line receives security fixes. Upgrade before reporting.

| Version | Supported |
|---------|-----------|
| `v0.8.x` (latest) | :white_check_mark: |
| `< v0.8.0` | :x: |

## Scope

### In scope

- **The daemon's Go code** — the reconcile loop, autoscaler, stuck-task healer, manager/agent
  report path, and the GitOps sync loop (including SOPS decryption and config/secret rotation).
- **The HTTP surface** — the Prometheus `/metrics` endpoint, the read-only GitOps
  `GET /stacks` / `GET /` status UI, and the agent ingest `POST /v1/report` handler
  (authentication, authorization, input handling, SSRF/request-smuggling).
- **Secrets handling** — how `password`/`password_file`, sops age/gpg keys, and the agent
  `INGEST_TOKEN` are read, logged (must never be), or written to the ephemeral GitOps worktree.
- **The container image / Dockerfile** — runs as non-root, CGO-free static binary; a privilege
  escalation or unsafe default there is in scope.

### Hardening you are responsible for (deployment, not the project)

These are operational choices documented in [`docs/deployment.md`](docs/deployment.md)
("Hardening recap") — misusing them is **not** a project vulnerability, but we want to know if the
docs steer you wrong:

- **Docker API access.** The manager mutates Swarm and needs Docker API access. Prefer the
  least-privilege [`deploy/stack.proxy.yml`](deploy/stack.proxy.yml) (`docker-socket-proxy`) over
  mounting the raw `/var/run/docker.sock`. Mounting the raw socket grants effectively root on the
  node — do that only on a trusted, isolated manager.
- **Expose endpoints deliberately.** `/metrics` and `/stacks` are **read-only** and carry no
  secrets by default, but they reveal service names, replica counts, revisions, and drift. Do not
  expose them to untrusted networks without a scraping allowlist / reverse proxy in front. The
  **agent ingest** port should stay on the internal overlay, scoped to agents only.
- **GitOps credentials.** Prefer `password_file` / SOPS-managed secrets over inline `password`;
  keep `--gitops-repos-path` on ephemeral storage (sops decrypts plaintext there transiently).
- **Mutating actions are opt-in.** Nothing is mutated without `--dry-run=false` **and** explicit
  `swarm.autoscaler.*` labels on a service, and all mutations flow through one guarded path
  (dry-run + per-service cooldown, replicas clamped to `[min,max]`). If you observe the daemon
  acting on a service you did **not** label, that is worth a report.

### Out of scope

- Vulnerabilities in **dependencies** (Docker SDK, go-git, goccy/go-yaml, sops, prometheus client,
  docker/cli) — report those upstream; we will ship a dependency bump here once a fix is out.
- Self-inflicted exposure from running as root, mounting the raw socket onto untrusted code, or
  disabling dry-run without understanding the labels.
- Generic Docker Swarm / Linux kernel issues.

## Security-conscious design (for triage context)

These properties are intentional and load-bearing — a report that assumes the opposite may be a
misunderstanding rather than a bug:

- **Dry-run by default.** Out of the box the daemon only *logs* intended actions; every mutation is
  gated behind `--dry-run=false`.
- **Opt-in by label.** A service is managed only when it carries `swarm.autoscaler.*` (and/or
  healing) labels. Unlabeled services are never touched.
- **No external state store.** State is re-derived from Swarm each loop; in-memory cooldowns reset
  on restart (conservative — a fresh start re-observes before acting).
- **Read-only status surface.** `GET /stacks` performs no mutations and does not route through the
  guarded mutation path.
