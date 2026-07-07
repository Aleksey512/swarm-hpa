## Test Plan: GitOps deploy retry + mutation hardening ("update out of sequence")

**Date:** 2026-07-06
**Branch / Version:** `feature/gitops-deploy-conflict-retry`
**Environment:** local multi-node Docker Swarm (dev/staging) with the `swarm-hpa` manager running `--gitops` + autoscaler

---

### 1. Testing Goal

Verify that the GitOps deploy now **recovers from an autoscaler↔deploy
`update out of sequence` collision** via the new bounded retry (within seconds,
not 120s), that **non-conflict deploy errors still fail fast** (retry does not
mask real failures), and that the **autoscaler/healer/rebalancer mutation paths
still work** and now also tolerate the real error string — all without
regressing dry-run, scaling, healing, or carry-forward.

---

### 2. Test Scope

**In Scope** — we test:

- GitOps deploy retry on version conflict (`stackdeploy.WithRetry` + `main.go` wiring)
- `dockererr.IsVersionConflict` classification (both the `errdefs.Conflict` path and the `"update out of sequence"` string path)
- Autoscaler/healer/rebalancer `ServiceUpdate` retry (`mutate.go` hardened)
- Dry-run gating (unchanged — retry is reached only post-dry-run)
- Carry-forward preserving autoscaled replicas across a retried deploy

**Out of Scope** — we don't test:

- Git sync, compose rendering, SOPS decryption, config/secret rotation (unchanged by this change)
- Agent mode / rebalancer load collection (unchanged)
- `/metrics` and `/stacks` status UI (unchanged)

---

### 3. Test Types

| Type | Priority | Area |
|------|----------|------|
| Functional | 🔴 High | Deploy retry on conflict; `IsVersionConflict` classification; `mutate.go` retry on the real string |
| Regression | 🟡 Medium | Autoscaler scale, healer force-update, dry-run suppression, carry-forward |
| Edge cases | 🟡 Medium | ctx cancellation mid-retry; exhaustion after 3 attempts; nil/unrelated errors |
| Negative | 🟡 Medium | Non-conflict deploy error surfaced (fail fast); conflict-exhaustion reported |
| Security | 🔴 High | Dry-run still gates deploys; no secrets in retry WARN logs (the conflict error carries no secrets) |
| Performance | 🟢 Low | Retry backoff adds ≤ ~300ms in the conflict case; no tight deploy loop under sustained conflict |

---

### 4. Test Data

| Category | Data | Purpose |
|----------|------|---------|
| Valid data | Stack with ≥1 autoscaled service (`swarm.autoscaler.enabled=true`, `min`/`max`) + ≥1 non-autoscaled service | Happy-path deploy + carry-forward |
| Boundary values | 3 consecutive conflicts (exhausts attempts); ctx timeout shorter than the backoff | Edge cases |
| Invalid data | A deploy that fails with a non-conflict error (e.g. bad compose, network) | Negative / fail-fast |
| Special cases | The exact error string `rpc error: code = Unknown desc = update out of sequence` | Real-production-match |

---

### 5. Preconditions

- [ ] Multi-node Docker Swarm (≥1 manager) is reachable; the `swarm-hpa` image is built and available
- [ ] A Git repo with `repos.yaml` / `stacks.yaml` containing at least one autoscaled service
- [ ] Autoscaler enabled and able to scale the target service (a metric source is configured)
- [ ] Logs are observable with `LOG_LEVEL=debug` (to see the per-retry WARN / success-after-retry INFO lines)
- [ ] Dry-run ON initially for safety; flipped to OFF only for the real-deploy test cases
- [ ] The target host's Docker/Swarm version is recorded (to confirm the exact error wording)

---

### 6. Acceptance Criteria

- [ ] All 🔴 high-priority test cases pass
- [ ] The original `update out of sequence` symptom no longer fails a sync — the deploy retries and succeeds within seconds
- [ ] Non-conflict deploy errors are **not** retried (fail fast, real cause surfaced)
- [ ] Autoscaler scaling and healer force-update still function; dry-run still suppresses deploys
- [ ] No deploy storm — at most 3 deploy attempts per sync tick, with the next tick as the outer safety net

---

### 7. Plan Risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| Hard to reproduce a real version conflict deterministically | Medium | Force overlap: shrink `GITOPS_INTERVAL` vs `POLL_INTERVAL`, or run a manual `docker service update` during a sync. Unit tests already inject the error; do at least one real-Swarm repro. |
| Target Docker version emits a differently-worded error | Medium | Capture the exact phrasing on the target version; if it differs, the string match in `dockererr` must be updated. |
| Retry could mask a real deploy failure | Low | Non-conflict errors fail fast by design — covered by the negative test case. |

---

### 8. Checklist

| Check | Priority |
|-------|----------|
| Reproduce + verify the retry fixes the original symptom | High |
| Non-conflict deploy error fails fast (no retry) | High |
| Autoscaler scale / healer force-update behavior unchanged | Medium |
| Dry-run still suppresses the deploy (retry never reached) | High |
| Retry honors ctx cancellation (graceful shutdown) | Medium |
| No deploy storm under sustained conflict (≤3 attempts/tick) | Medium |
| No secrets present in retry WARN log lines | Low |
