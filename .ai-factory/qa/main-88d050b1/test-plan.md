## Test Plan: examples/ demo workloads (CPU / Prometheus autoscaling + healer)

**Date:** 2026-07-01
**Branch / Version:** main (commits `a7abc97`, `548e527`, `cea23cc`, `3f8e79e`)
**Environment:** local single-node Docker Swarm (`docker swarm init`); a 2+-node swarm for the healer scenario

---

### 1. Testing Goal

Verify that each `examples/` stack **deploys** on a real Swarm, is **detected** by the daemon via its `swarm.autoscaler.*` labels, and **drives the intended action** (scale-out on CPU, scale on PromQL rps, force-update heal) — matching the commands and expected log lines in `examples/README.md`. Also confirm the validation tooling (`make examples-validate`, CI job) actually catches broken stacks.

---

### 2. Test Scope

**In Scope:**

- `examples/cpu-autoscale/stack.yml` + `loadgen.sh` (Docker stats path)
- `examples/prometheus-autoscale/stack.yml` + `prometheus.yml` (PromQL path, `$SERVICE` expansion, `promdemo` coupling)
- `examples/healer/stack.yml` (placement-constraint + managed-service gating)
- `examples/README.md` walkthrough accuracy (commands, ports, flags, teardown)
- `make examples-validate` and the CI `examples-validate` job
- Docs wiring links (README table, docs pages, AGENTS.md) resolve

**Out of Scope:**

- Daemon decision logic (`internal/core/*`) — unchanged, covered by existing unit tests
- The daemon's own image build / deploy stacks beyond a smoke "still deploys" regression
- Prometheus/Docker internals; non-example docs content

---

### 3. Test Types

| Type | Priority | Area |
|------|----------|------|
| Functional (deploy + detect + act) | 🔴 High | All three stacks end-to-end against a live daemon |
| Configuration / integration | 🔴 High | `$SERVICE` escaping, `promdemo` stack-name coupling, service-label placement, image pull |
| Negative | 🟡 Medium | Wrong stack name, labels in container scope, unreachable target for `loadgen.sh`, broken stack caught by `examples-validate` |
| Regression | 🟡 Medium | `make examples-validate`, CI jobs, daemon deploy stacks, doc links |
| Edge cases | 🟡 Medium | Multi-node dockerstats "no data", single-node healer approximation, port collisions |
| Performance | 🟢 Low | Load level needed to cross `target`; scale stabilization/cooldown timing |

---

### 4. Test Data

| Category | Data | Purpose |
|----------|------|---------|
| Valid deploy | `docker stack deploy -c examples/cpu-autoscale/stack.yml demo` | Happy path (cpu) |
| Valid deploy | `docker stack deploy -c examples/prometheus-autoscale/stack.yml promdemo` | Happy path (prometheus; name matters) |
| Valid deploy | node label `nodeNum=1` + `docker stack deploy -c examples/healer/stack.yml healer` | Happy path (healer) |
| Load | `CONCURRENCY=20 DURATION=120 examples/cpu-autoscale/loadgen.sh` | Drive CPU above `target=50` |
| Boundary | `CONCURRENCY=1`, very short `DURATION=5` | loadgen with minimal load |
| Invalid | deploy prometheus stack as `wrongname` (not `promdemo`) | Negative: scrape/label mismatch |
| Invalid | `loadgen.sh http://localhost:9999/` (nothing listening) | Negative: preflight must fail cleanly |
| Broken stack | temporarily corrupt a `stack.yml` (bad YAML) | `examples-validate` must fail non-zero |

---

### 5. Preconditions

- [ ] Docker Engine + Swarm mode active (`docker swarm init`); manager node
- [ ] Outbound access to `registry.k8s.io`, `quay.io`, Docker Hub (or images pre-pulled)
- [ ] Daemon built (`make build`) or image available; dry-run understood as default
- [ ] For healer: at least one node labelled `nodeNum=1`; ideally a 2+-node swarm
- [ ] Ports `8080`, `8081`, `9090` free on the host
- [ ] `curl` or `wget` present for `loadgen.sh`

---

### 6. Acceptance Criteria

- [ ] All 🔴 high-priority cases pass: each stack deploys, is detected, and the daemon logs the intended decision
- [ ] `$SERVICE` in the deployed prometheus service label is a literal `$SERVICE` (not blank), and the daemon's PromQL returns a numeric value
- [ ] `make examples-validate` exits 0 on the shipped stacks and non-zero on a deliberately broken one
- [ ] CI `examples-validate` and `build-test` jobs are green
- [ ] Negative scenarios behave safely (no wrong scale; clear errors)
- [ ] All docs links to `examples/` resolve

---

### 7. Plan Risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| Images unavailable / registry unreachable in target env (not verifiable in sandbox) | High | Pre-pull images; TC-001 checks pull explicitly before deploy |
| Prometheus example deployed under a non-`promdemo` name → no metrics | High | TC-005 explicitly tests the documented name and the wrong-name negative (TC-009) |
| CPU load insufficient to cross `target` on a fast host | Medium | Increase `CONCURRENCY`; observe dry-run decision math even below threshold |
| Healer stall not reproducible on single node | Medium | Use 2+ nodes; otherwise verify the signature + expected log, not full recovery |
| Port collisions when running cpu + prometheus demos together | Low | Run demos one at a time; ports are distinct by design |

---

### 8. Checklist

| Check | Priority |
|-------|----------|
| All four example images pull in the target environment | 🔴 High |
| cpu stack deploys; daemon reports `observed managed services count=1` | 🔴 High |
| loadgen drives CPU; daemon logs `scaling decision` / `would scale` for `demo_web` | 🔴 High |
| prometheus stack (as `promdemo`) deploys; Prometheus scrapes `promdemo_app` replicas | 🔴 High |
| deployed `swarm.autoscaler.query` label shows literal `$SERVICE`; daemon logs `source=prometheus` value | 🔴 High |
| healer service is managed (min=max=1) and matches heal signature after node recovery | 🔴 High |
| `make examples-validate` passes; fails non-zero on a broken stack | 🟡 Medium |
| CI `examples-validate` + `build-test` jobs green | 🟡 Medium |
| loadgen preflight fails cleanly for an unreachable URL; `-h` prints usage | 🟡 Medium |
| labels under `deploy.labels` (service scope), not container labels | 🟡 Medium |
| docs/README/AGENTS links to `examples/README.md` resolve | 🟢 Low |
| daemon deploy stacks still parse/deploy (regression) | 🟢 Low |
