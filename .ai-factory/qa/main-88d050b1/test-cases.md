## Test Cases: examples/ demo workloads

> Run on a manager node of a `docker swarm init` cluster. Build the daemon first: `make build`.
> Dry-run is ON by default — every "would …" log line below appears without `--dry-run=false`.

---

### TC-001: All example images pull in the target environment

**Priority:** High
**Type:** Positive

**Precondition:** Docker host with outbound access to the registries (or a mirror).

**Steps:**

1. Pull each referenced image:
   - `docker pull registry.k8s.io/hpa-example`
   - `docker pull quay.io/brancz/prometheus-example-app:v0.5.0`
   - `docker pull prom/prometheus:v2.54.1`
   - `docker pull nginx:alpine`

**Expected result:**

All four pulls succeed. (This was NOT verifiable in the dev sandbox — Docker Hub egress was blocked — so it must be confirmed here; a missing tag or unreachable registry is the #1 cause of a "broken" example.)

**Test data:**

```
registry.k8s.io/hpa-example
quay.io/brancz/prometheus-example-app:v0.5.0
prom/prometheus:v2.54.1
nginx:alpine
```

---

### TC-002: cpu-autoscale deploys and is detected by the daemon

**Priority:** High
**Type:** Positive

**Precondition:** Swarm active; port 8080 free.

**Steps:**

1. `docker stack deploy -c examples/cpu-autoscale/stack.yml demo`
2. `docker stack services demo` — wait until `web` shows `2/2`.
3. In another terminal: `make run ARGS="--log-level=debug"`.

**Expected result:**

Service `demo_web` reaches 2/2 replicas. The daemon logs `observed managed services count=1` and a `scaling decision service=demo_web current=2 …` line. No mutation yet (dry-run), no errors.

---

### TC-003: loadgen drives a CPU scale-out decision

**Priority:** High
**Type:** Positive

**Precondition:** TC-002 deployed and the daemon running.

**Steps:**

1. `examples/cpu-autoscale/loadgen.sh` (defaults: `http://localhost:8080/`, 20 workers, 120s).
2. Watch the daemon logs during the run.
3. Optionally re-run with `--dry-run=false` to see a real scale.

**Expected result:**

`loadgen.sh` prints its config and periodic progress. As per-task CPU rises above `target=50`, the daemon logs `scaling decision … desired>current` and `dry-run: would scale service=demo_web`. With `--dry-run=false`, `docker stack services demo` shows the replica count increase (clamped ≤ `max=8`).

**Test data:**

```
default run
CONCURRENCY=50 DURATION=180 examples/cpu-autoscale/loadgen.sh    # if a fast host doesn't cross target
```

---

### TC-004: prometheus-autoscale deploys as `promdemo` and Prometheus scrapes all replicas

**Priority:** High
**Type:** Positive

**Precondition:** Swarm active; ports 9090 and 8081 free.

**Steps:**

1. `docker stack deploy -c examples/prometheus-autoscale/stack.yml promdemo`
2. Wait for `promdemo_prometheus` (1/1) and `promdemo_app` (3/3).
3. Open `http://localhost:9090/targets` (or query `up{service="promdemo_app"}`).

**Expected result:**

Prometheus shows **3** `demo-app` targets UP (one per replica, via `dns_sd` on `tasks.promdemo_app`), each series carrying `service="promdemo_app"`. `count(up{service="promdemo_app"})` returns `3`.

---

### TC-005: `$SERVICE` expansion + Prometheus scaling decision

**Priority:** High
**Type:** Positive

**Precondition:** TC-004 deployed.

**Steps:**

1. Confirm the deployed label is a literal `$SERVICE` (not blank):
   `docker service inspect promdemo_app --format '{{ index .Spec.Labels "swarm.autoscaler.query" }}'`
2. Generate rps: `examples/cpu-autoscale/loadgen.sh http://localhost:8081/`
3. Run the daemon: `make run ARGS="--dry-run=false --prometheus-url=http://localhost:9090 --log-level=debug"`

**Expected result:**

Step 1 prints `sum(rate(http_requests_total{service="$SERVICE"}[1m]))/scalar(count(up{service="$SERVICE"}))` — a single `$SERVICE` (proof the `$$` escaping survived deploy). The daemon logs `scaling decision service=promdemo_app source=prometheus value=<number> target=50 desired=<n>` and scales within `min=3..max=20`. No `query returned N series` / `no data` errors.

---

### TC-006: healer — service is managed and matches the heal signature

**Priority:** High
**Type:** Positive

**Precondition:** A node labelled for the constraint; ideally a 2+-node swarm.

**Steps:**

1. `docker node update --label-add nodeNum=1 <node-id>`
2. `docker stack deploy -c examples/healer/stack.yml healer`
3. `docker node update --availability drain <node-id>` → task goes `pending`.
4. `docker node update --availability active <node-id>` → node back Active+Ready.
5. `make run ARGS="--log-level=debug --heal-threshold=1m"` (then optionally `--dry-run=false`).

**Expected result:**

`healer_pinned` is listed among managed services (it carries the full `min=max=1` policy). After the task is `pending` ≥ 1m with the constrained node recovered, the daemon logs `dry-run: would force-update (heal) service=healer_pinned`; with `--dry-run=false` it force-updates and the task returns to `running`. On a single node this is approximate — at minimum the heal decision/log must appear once the signature holds.

---

### TC-007: `make examples-validate` passes on the shipped stacks

**Priority:** Medium
**Type:** Positive (regression)

**Steps:**

1. From repo root: `make examples-validate`

**Expected result:**

Prints `docker stack config -c` for each of the three stacks, then `examples-validate: PASS`, exit code 0. (promtool/shellcheck steps are skipped with a note if those tools are absent.)

---

### TC-008: `examples-validate` fails on a broken stack

**Priority:** Medium
**Type:** Negative

**Steps:**

1. Temporarily break a stack: e.g. add an invalid line to `examples/healer/stack.yml` (`  bad: [unclosed`).
2. Run `make examples-validate`; observe exit code with `echo $?`.
3. Revert the change (`git checkout -- examples/healer/stack.yml`).

**Expected result:**

The target prints `FAILED: examples/healer/stack.yml` and exits non-zero (the loop stops on the first bad file). Confirms the validation actually guards the stacks (and would catch a regression in CI).

---

### TC-009: prometheus example under a wrong stack name → no data (negative)

**Priority:** Medium
**Type:** Negative

**Steps:**

1. `docker stack deploy -c examples/prometheus-autoscale/stack.yml wrongname`
2. Open `http://localhost:9090/targets`.
3. Run the daemon with `--prometheus-url=http://localhost:9090`.

**Expected result:**

Prometheus has **0** targets for `tasks.promdemo_app` (the service is now `wrongname_app`), so `count(up{service="promdemo_app"})` is empty → the daemon logs *no data* and **skips** `wrongname_app` for the tick (never a wrong scale). This documents the `promdemo` coupling as intended behaviour. Teardown: `docker stack rm wrongname`.

---

### TC-010: `loadgen.sh` fails cleanly on an unreachable target; `-h` prints usage

**Priority:** Medium
**Type:** Negative

**Steps:**

1. `examples/cpu-autoscale/loadgen.sh http://localhost:9999/` (nothing listening).
2. `examples/cpu-autoscale/loadgen.sh -h`

**Expected result:**

Step 1: the preflight fails with `loadgen: error — http://localhost:9999/ is not reachable.` plus the deploy hint, and exits non-zero **without** spawning workers. Step 2: prints the usage block (TARGET_URL/CONCURRENCY/DURATION) and exits 0.

---

### TC-011: labels are read as service labels (deploy.labels), not container labels

**Priority:** Medium
**Type:** Positive / Negative

**Steps:**

1. Positive: `docker service inspect demo_web --format '{{ json .Spec.Labels }}'` — confirm the `swarm.autoscaler.*` keys are present at the **service** level.
2. Negative (optional): edit a copy of the stack to move the labels under a top-level `labels:` on the service (container labels), redeploy, run the daemon.

**Expected result:**

Step 1 shows all five `swarm.autoscaler.*` labels on the service spec → the daemon manages it. Step 2 (container labels) → the daemon reports `observed managed services count=0` and never touches the service, demonstrating why `deploy.labels` is required.

---

### TC-012: docs wiring + daemon deploy stacks (regression)

**Priority:** Low
**Type:** Regression

**Steps:**

1. Follow the `examples/` links from `README.md`, `docs/getting-started.md`, `docs/metrics-providers.md`, `docs/configuration.md`, `AGENTS.md`.
2. `docker stack config -c deploy/stack.yml` and `docker stack config -c deploy/stack.proxy.yml`.
3. Check CI: `gh run list --branch main --limit 1` (jobs `build-test` + `examples-validate` green).

**Expected result:**

All `examples/README.md` links resolve (no 404). Both daemon deploy stacks still parse. The latest CI run is green (golangci-lint v2 runs; `examples-validate` job passes).

---

## Test Data (based on test design techniques)

### Positive

* cpu: deploy stack `demo`, `loadgen.sh` defaults, target CPU=50
* prometheus: deploy stack `promdemo`, drive `:8081`, `--prometheus-url=http://localhost:9090`
* healer: node label `nodeNum=1`, `--heal-threshold=1m`, drain→active cycle

### Negative

* prometheus deployed as `wrongname` → empty scrape, `no data`, service skipped
* `loadgen.sh http://localhost:9999/` → clean preflight failure, non-zero exit
* corrupted `stack.yml` → `make examples-validate` non-zero
* labels under container `labels:` → `observed managed services count=0`
