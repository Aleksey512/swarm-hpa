## Change Summary

**Commits:** 4 (`a7abc97`, `548e527`, `cea23cc` — examples feature; `3f8e79e` — CI lint fix that unblocks the run)
**Changed files:** 15 (11 behavioral: `examples/*`, docs, `Makefile`, `ci.yml`; 2 non-behavioral: plan + patch under `.ai-factory/`)
**Risk level:** 🟡 Medium

Scope of this QA: **"are the examples actually deployable and do they drive the daemon?"** — not the daemon's own logic (unchanged; `internal/core` untouched, `go build`/`vet`/lint green).

---

### What Changed

A new `examples/` tree with three runnable Docker Swarm demos of the daemon's capabilities — CPU autoscaling (Docker stats), Prometheus (PromQL) autoscaling, and the stuck-task healer — plus a walkthrough `README.md`. Supporting changes: docs cross-links (README table, getting-started, metrics-providers, configuration, AGENTS.md), a `make examples-validate` target and a CI job that parse-validate the stacks, and a fix to the CI golangci-lint step so the run is green. No daemon behavior changed; examples are YAML + shell + docs.

---

### Affected Areas

| Component | Change type | Description |
|-----------|-------------|-------------|
| `examples/cpu-autoscale/` | Added | `stack.yml` (hpa-example, CPU labels) + `loadgen.sh` (POSIX load generator) |
| `examples/prometheus-autoscale/` | Added | `prometheus.yml` (dns_sd scrape + `service` relabel) + `stack.yml` (prometheus + demo app, PromQL `query` label) |
| `examples/healer/` | Added | `stack.yml` — placement-constrained service + reproduction notes |
| `examples/README.md` | Added | End-to-end walkthrough: deploy commands, daemon flags, expected logs, teardown |
| `README.md`, `docs/*`, `AGENTS.md` | Changed | Links/rows pointing at `examples/` (5 files, 1 line each mostly) |
| `Makefile` | Changed | `examples-validate` target (`docker stack config` per stack; guarded promtool/shellcheck) |
| `.github/workflows/ci.yml` | Changed | New `examples-validate` job + `golangci-lint-action@v7` / pinned `v2.12.2` |

---

### Evidence

| Finding | Evidence |
|---------|----------|
| All three stacks parse | `docker stack config -c` OK for all `examples/*/stack.yml` (local, run during implement); `make examples-validate` → PASS |
| `$SERVICE` escaping is correct | `docker stack config` renders `swarm.autoscaler.query` label to a single literal `$SERVICE` (from `$$SERVICE`) — daemon expands it at runtime |
| Opt-in policy present & valid | each `stack.yml` carries 5 `swarm.autoscaler.*` label lines under `deploy.labels` (enabled/min/max/metric/target); healer adds a no-op `min=max=1` |
| CI examples-validate green | run `28511904913`, job `examples-validate` ✓ (5s) |
| Daemon logic unchanged | `git diff 405bd5e..HEAD` touches no `internal/` file; `go build ./...`, `go vet ./...`, `golangci-lint run` all clean |
| Image availability NOT verified | local `docker manifest inspect` / `docker pull` blocked in this sandbox (Docker Hub `EOF`; only quay reachable) — must be checked in the target environment |

---

### Risks

🔴 **Critical** (must verify):

- **Image availability in the target environment.** The demos pull `registry.k8s.io/hpa-example`, `quay.io/brancz/prometheus-example-app:v0.5.0`, `prom/prometheus:v2.54.1`, `nginx:alpine`. A missing/renamed tag or a registry the cluster can't reach makes the example fail at deploy. Not verifiable in this sandbox (Docker Hub egress blocked) — verify on the real host.
- **Prometheus example is coupled to the stack name `promdemo`.** `prometheus.yml` scrapes `tasks.promdemo_app` and relabels `service="promdemo_app"`, and the daemon's `$SERVICE` expands to the deployed service name. Deploying under any other stack name → empty scrape → `count(up)` = 0 → no data / no scaling. Documented, but a real footgun.
- **`$SERVICE` compose escaping.** If `$$SERVICE` is ever "corrected" to `$SERVICE`, `docker stack deploy` interpolates it to empty and the query breaks. Currently correct (see Evidence).

🟡 **Medium** (should verify):

- **Service vs container label placement.** The daemon reads *service* labels (`deploy.labels`). If moved to a top-level `labels:` key, the service is silently unmanaged. Currently correct.
- **Port coupling.** cpu demo publishes `8080`, prom demo app `8081`, prometheus `9090`. `loadgen.sh` defaults to `:8080`. Running cpu + prom stacks at once must not collide; the prom walkthrough drives `:8081`.
- **`loadgen.sh` portability & prerequisites.** POSIX `sh`, needs `curl` or `wget`, must be executable; behaviour differs slightly macOS vs Linux. Reachability preflight must fail cleanly when the stack isn't up.
- **Provider caveats.** Docker stats is node-local (multi-node → "no data" for remote tasks). The daemon must run with the right flags (`--dry-run=false`, `--prometheus-url=…`) to actually act.
- **Healer reproduction needs 2+ nodes** to reproduce the moby#42215 stall cleanly; single-node is approximate (documented).

🟢 **Low** (nice to verify):

- CI `Node.js 20 is deprecated` warnings (cosmetic; run still passes).
- `swarm.autoscaler.target` values (cpu=50, rps=50) may need per-host tuning to visibly cross the threshold.

---

### Testing Recommendations

**First priority:**

- [ ] On a single-node swarm (`docker swarm init`), deploy each stack and confirm all images pull and services reach `running`.
- [ ] Run the daemon and confirm it detects each managed service (`observed managed services count=1`).
- [ ] cpu-autoscale: run `loadgen.sh`, confirm a `scaling decision` / dry-run `would scale` for `demo_web`.
- [ ] prometheus-autoscale: deploy as `promdemo`, confirm Prometheus scrapes the app and the daemon logs `scaling decision … source=prometheus` with a numeric value.
- [ ] healer: confirm the `would force-update (heal)` path fires after `--heal-threshold` when the constrained node recovers.

**Regression:**

- [ ] `make examples-validate` passes locally and the CI `examples-validate` job stays green.
- [ ] The daemon deploy stacks (`deploy/stack.yml`, `deploy/stack.proxy.yml`) still parse and deploy.
- [ ] `build-test` CI job green (golangci-lint v2 runs, not the old exit-3).
