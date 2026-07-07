## Change Summary

**Commits:** 3 (`main..feature/gitops-deploy-conflict-retry`)
**Changed files:** 10 (8 code/docs + 2 AI-factory artifacts)
**Risk level:** 🟡 Medium

---

### What Changed

When the GitOps deploy loop and the autoscaler/healer/rebalancer both tried to
update the same Swarm service at the same time, the deploy failed with
`update out of sequence` and stayed broken until the next sync tick (default
120s). The deploy path had no retry, and the autoscaler's retry only caught
SDK-classified conflicts — not the real `code = Unknown` error Swarm emits.

The fix adds a **bounded retry around `docker stack deploy`** (a few attempts
within seconds) and a **shared error classifier** (`dockererr.IsVersionConflict`)
so both the deploy path and the autoscaler/healer/rebalancer mutation path now
agree on which errors are transient and retry them. Goal: stop the intermittent
deploy failures **without changing what gets deployed** (deploy is idempotent;
carry-forward still clamps autoscaled replicas to `[min,max]`).

---

### Affected Areas

| Component | Change type | Description |
|-----------|-------------|-------------|
| `internal/adapter/dockererr` | Added | New shared helper `IsVersionConflict(err)` — `errdefs.IsConflict(err)` OR `strings.Contains(err, "update out of sequence")`. Used by both mutation paths. |
| `internal/adapter/stackdeploy` | Added | `WithRetry` decorator around the `DeployFunc` seam — re-invokes `docker stack deploy` up to 3× on a version conflict (short backoff, ctx-aware); non-conflict errors fail fast. |
| `cmd/swarm-hpa/main.go` | Changed | Wires `DockerCLIDeploy` through `WithRetry` at the composition root. |
| `internal/adapter/swarm/mutate.go` | Changed | Autoscaler/healer/rebalancer `ServiceUpdate` retry now uses `IsVersionConflict` — catches the real `code=Unknown` error string, not just `errdefs.Conflict`. |
| `docs/gitops.md` | Changed | New "Concurrency with the autoscaler (deploy retry)" + "Troubleshooting: update out of sequence" sections. |

---

### Risks

🔴 **Critical** (must verify):

- **Retry must catch the real production error.** The string-match (`"update out of sequence"`) is coupled to Docker's exact message wording. If the target Swarm/Docker version emits a differently-worded error, the retry won't fire and the original symptom persists. Unit tests use the known string; the production Docker version must be confirmed.
- **Re-running `docker stack deploy` on conflict must converge safely** — no deploy storm and no replica thrash. Bounded to 3 attempts/tick with the next-tick retry as the outer safety net, but verify there is no tight loop under sustained conflict.

🟡 **Medium** (should verify):

- **`mutate.go` broadens retry triggering** (strict superset of the old `errdefs.IsConflict` check). Verify the autoscaler/healer don't over-retry on a genuinely non-transient error. Low risk because the only added match is a Swarm-specific string.
- **ctx-cancellation during backoff** — verify graceful shutdown (SIGTERM) is not delayed by the retry backoff. The `select` honors `ctx.Done()` (unit-tested), but confirm on a real signal.

🟢 **Low** (nice to verify):

- String-match brittleness to a future Docker message rewording (mitigated: next-tick retry is the outer safety net).
- New `dockererr` package import graph — confirm no import cycle (it is a leaf: imports only `errdefs` + stdlib).

---

### Testing Recommendations

**First priority:**

- [ ] Reproduce the original `update out of sequence` on a real Swarm (autoscaler + GitOps both active on the same service) and confirm the deploy now succeeds within seconds, with one WARN log line per retry attempt.
- [ ] Confirm a deploy that fails for a **non-conflict** reason still fails fast (the retry must not mask real errors).

**Regression:**

- [ ] Autoscaler scaling still works and retries on a normal conflict.
- [ ] Healer force-update still works.
- [ ] Dry-run still suppresses deploys (the retry lives inside `DeployFunc`, reached only after the loop's dry-run gate).
- [ ] Carry-forward still preserves autoscaled replicas across a (now possibly retried) deploy.
