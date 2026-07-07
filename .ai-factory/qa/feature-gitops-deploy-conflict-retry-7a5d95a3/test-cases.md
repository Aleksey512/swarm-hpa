## Test Cases: GitOps deploy retry + mutation hardening

> These are **manual / integration** test scenarios for a human tester against a real
> Swarm. (Automated unit coverage for the helper, the decorator, and the hardened
> `mutate.go` already exists under `internal/adapter/{dockererr,stackdeploy,swarm}`.)

---

### TC-001: Deploy succeeds on the first attempt (no conflict)

**Priority:** High
**Type:** Positive

**Precondition:** Manager running `--gitops` with `DRY_RUN=false`; one stack synced successfully before; Git revision unchanged then advanced by one trivial commit.

**Steps:**

1. Push a trivial change to the tracked compose (e.g. bump a non-autoscaled service's image tag) so a new revision syncs.
2. Wait for the next `GITOPS_INTERVAL` sync.
3. Watch the manager logs (`LOG_LEVEL=debug`).

**Expected result:**

- The deploy runs exactly once and succeeds.
- There is **no** `"deploy version conflict, retrying"` WARN line and **no** `"deploy succeeded after retry"` INFO line.
- `/stacks` shows the stack `ok: true` at the new revision; `deploys_total{stack}` increments by 1.

**Test data:**

```
stack: web
compose change: services.api.image: registry.example.com/api:1.2.4  (was :1.2.3)
```

---

### TC-002: Deploy retries and succeeds on a transient version conflict (the core fix)

**Priority:** High
**Type:** Positive

**Precondition:** Manager running `--gitops` + autoscaler (`DRY_RUN=false`); the target stack has an autoscaled service (`swarm.autoscaler.enabled=true`) that the autoscaler is actively scaling.

**Steps:**

1. Force the autoscaler and a GitOps sync to overlap on the same service: shrink `POLL_INTERVAL` (e.g. `5s`) and `GITOPS_INTERVAL` (e.g. `10s`) so a scale and a deploy collide, OR run `docker service update <stack>_<svc> --label-add x=1` the instant a sync begins.
2. Keep the logs open during the collision.
3. Observe the sync outcome on `/stacks`.

**Expected result:**

- A log line appears: `level=WARN msg="stackdeploy: deploy version conflict, retrying" stack=<name> attempt=1 err="...update out of sequence..."`.
- Within ~3 attempts the deploy **succeeds**: `level=INFO msg="stackdeploy: deploy succeeded after retry" stack=<name> attempts=<n>`.
- The sync ends `ok: true`; `sync_errors_total` does **not** increase for this tick; `deploys_total` increments.
- The autoscaled service's live replica count stays within `[min, max]` (carry-forward held).

**Test data:**

```
stack: web, autoscaled service: worker (min=2, max=10)
collision trigger: docker service update web_worker --label-add collision=1   (during sync)
```

---

### TC-003: Non-conflict deploy error fails fast (no retry)

**Priority:** High
**Type:** Negative

**Precondition:** Manager running `--gitops` (`DRY_RUN=false`); the tracked compose references an image on a registry the daemon cannot reach, or contains a malformed service definition that makes `docker stack deploy` fail with a non-conflict error.

**Steps:**

1. Push a compose that fails the deploy with a non-conflict error (e.g. `services.broken.image: registry.invalid/img:tag` with `--gitops-pull-policy=always`, or a syntactically broken service).
2. Wait for the next sync.
3. Watch the logs.

**Expected result:**

- The deploy is attempted **exactly once** — there is NO `"deploy version conflict, retrying"` line (the error is not a version conflict).
- The original error is surfaced to the caller (logged as the deploy failure, recorded via `SyncError("deploy")`).
- `/stacks` shows the stack `ok: false` with `error_stage: "deploy"` and the real error message; `sync_errors_total{stage="deploy"}` increments.

**Test data:**

```
stack: web
bad service: services.broken.image: registry.invalid.example/broken:latest
```

---

### TC-004: Conflict persists — retry exhausts attempts, then reports failure

**Priority:** Medium
**Type:** Negative

**Precondition:** Manager running `--gitops` (`DRY_RUN=false`); an external second writer (e.g. a loop running `docker service update <stack>_<svc>` every second) keeps the service's `Version.Index` moving so every deploy attempt conflicts.

**Steps:**

1. Start the external writer loop on the target service.
2. Trigger a GitOps sync for its stack (push a change).
3. Watch the logs.

**Expected result:**

- Exactly 3 `"deploy version conflict, retrying"` lines (attempt 1, 2, 3).
- After the 3rd, a single failure: `... exhausted 3 attempts: ...update out of sequence...`.
- The tick is recorded as a deploy failure; on the **next** tick the loop retries again (outer safety net). When the external writer stops, a subsequent sync succeeds.
- Critically: no unbounded retry storm — never more than 3 attempts in one tick.

**Test data:**

```
stack: web, service: web_worker
external writer: while true; do docker service update --label-add t=$(date +%s) web_worker; sleep 1; done
```

---

### TC-005: Retry honors context cancellation (graceful shutdown)

**Priority:** Medium
**Type:** Edge

**Precondition:** Manager running with a deploy currently in a conflict-retry backoff.

**Steps:**

1. Trigger a persistent-conflict situation (as in TC-004) so a retry is in its backoff `select`.
2. While a backoff is in progress, send `SIGTERM` to the manager (`docker service update`-style rolling stop, or `kill -TERM <pid>` if running directly).
3. Observe how fast the process exits.

**Expected result:**

- The process exits promptly (the backoff `select` returns on `ctx.Done()`); it does **not** wait for the full backoff timer.
- The deploy is reported as not-applied for that tick; on restart the next sync re-applies cleanly.

**Test data:**

```
signal: SIGTERM during retry backoff
```

---

### TC-006: Autoscaler ServiceUpdate retries on the real "update out of sequence"

**Priority:** High
**Type:** Regression

**Precondition:** Autoscaler enabled (`DRY_RUN=false`); a managed service with `swarm.autoscaler.enabled=true`. A concurrent writer will bump the service version during the autoscaler's update.

**Steps:**

1. Drive the autoscaler to scale the service (raise the metric / load so a scale-up fires).
2. At the same instant, run `docker service update <stack>_<svc> --label-add bump=1` to bump the version under the autoscaler.
3. Watch the autoscaler logs.

**Expected result:**

- A `"service version conflict, retrying"` WARN appears, then `"service mutated" action=scale`.
- The scale **succeeds** after re-inspect + retry (the `mutate.go` path now catches the real `code=Unknown` error, not just `errdefs.Conflict`).
- The service's replica count reaches the autoscaler's desired value within `[min, max]`.

**Test data:**

```
service: web_worker (enabled=true, min=2, max=10)
concurrent bump: docker service update --label-add bump=$(date +%s) web_worker
```

---

### TC-007: Dry-run still suppresses the deploy (retry never reached)

**Priority:** High
**Type:** Regression

**Precondition:** Manager running `--gitops` with `DRY_RUN=true`.

**Steps:**

1. Push a compose change that would normally deploy.
2. Wait for the next sync.
3. Watch logs and `/stacks`.

**Expected result:**

- The loop logs `"dry-run; would decrypt/rotate/deploy stack"` and records `sync_suppressed_total{reason="dry_run"}`.
- `docker stack deploy` is **never** invoked — therefore `WithRetry` is never reached and there are zero deploy/retry log lines.
- No Swarm service is actually mutated (`docker service inspect` shows unchanged spec).

**Test data:**

```
DRY_RUN=true; stack: web; compose change: any
```

---

### TC-008: Carry-forward preserves autoscaled replicas across a retried deploy

**Priority:** Medium
**Type:** Regression

**Precondition:** Autoscaler has scaled a service to a live count within `[min, max]` (e.g. live=7, min=2, max=10); `DRY_RUN=false`.

**Steps:**

1. Note the live replica count (`docker service ls` / `docker service inspect --format '{{.Spec.Mode.Replicated.Replicas}}'`).
2. Trigger a GitOps sync (push any change) that also hits at least one deploy conflict (as in TC-002).
3. After the retried deploy converges, re-check the live replica count.

**Expected result:**

- The autoscaled service's replica count is **not reset** toward the compose value by the (retried) deploy — it stays within `[min, max]` and converges to what the autoscaler wants on its next poll.
- A non-autoscaled service in the same stack **is** reconciled to the compose value (proving carry-forward is scoped to autoscaled services only).

**Test data:**

```
autoscaled: web_worker (live=7, min=2, max=10)
non-autoscaled: web_api (compose replicas=3)
```

---

### TC-009: No secrets in retry log lines

**Priority:** Low
**Type:** Security / Regression

**Precondition:** GitOps configured with SOPS secrets and a private registry auth; `DRY_RUN=false`; a conflict occurs during a sync (as in TC-002).

**Steps:**

1. Trigger a deploy conflict during a sync that also involved SOPS decryption / private-registry image resolution.
2. Grep the manager logs for the retry lines and surrounding context.

**Expected result:**

- The `"deploy version conflict, retrying"` line's `err` field contains only the Swarm RPC error text (`rpc error: code = Unknown desc = update out of sequence`) — **no** registry token, SOPS plaintext, or credential material.

**Test data:**

```
grep: level=WARN.*deploy version conflict
```

---

### TC-010: End-to-end — original symptom no longer breaks the sync

**Priority:** High
**Type:** Positive (end-to-end)

**Precondition:** A cluster that previously exhibited the bug: a GitOps sync failing with `deploy: stackdeploy: deploy "...": failed to update service ...: ...update out of sequence...` while the autoscaler ran.

**Steps:**

1. Run the new manager build on that cluster (`--gitops`, autoscaler on, `DRY_RUN=false`).
2. Let it run across several sync intervals with the autoscaler active on the same services.
3. Watch `/stacks`, `/metrics` (`sync_errors_total`, `deploys_total`), and the logs over ~10 minutes.

**Expected result:**

- No sync stays failed due to `update out of sequence`; any conflict is absorbed by the retry and the sync ends `ok: true`.
- `sync_errors_total` does not trend upward from version conflicts; occasional retry WARN lines are expected and benign.
- Replicas of autoscaled services remain stable within bounds; non-autoscaled services track Git.

**Test data:**

```
observation window: ~10 min; autoscaler + gitops both active on overlapping services
```

---

## Test Data (based on test design techniques)

### Positive

* A healthy stack: 1 autoscaled service (`min=2, max=10`) + 1 non-autoscaled service (`replicas=3`), on a reachable registry.
* A trivial compose change (image tag bump) that should deploy cleanly.
* A forced autoscaler↔deploy overlap (shrunk intervals, or a concurrent `docker service update`) to trigger exactly 1–2 conflicts then success.

### Negative

* A compose referencing an unreachable registry / broken service → non-conflict deploy error (fail-fast).
* A persistent external writer loop bumping the service version every second → conflict exhaustion (3 attempts, then failure; next tick retries).
* Concurrent `docker service update` under the autoscaler's `ServiceUpdate` → mutate.go conflict-retry path.

### Edge

* `SIGTERM` delivered during a retry backoff → prompt exit (ctx honored).
* Exactly 3 consecutive conflicts → exhaustion boundary.
* Error string variants: full `rpc error: code = Unknown desc = update out of sequence` vs. bare `update out of sequence` vs. an `errdefs.Conflict`-classified error — all must be treated as retryable.
