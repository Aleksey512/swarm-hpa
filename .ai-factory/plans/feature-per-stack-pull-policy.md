# Per-stack image pull policy (`pull_policy` in `stacks.yaml`)

- **Branch:** `feature/per-stack-pull-policy` (from `main`)
- **Created:** 2026-07-14
- **Mode:** Full

## Summary

Add a per-stack `pull_policy: always|changed` field to GitOps `stacks.yaml` that
overrides the global `--gitops-pull-policy` for a single stack, falling back to the
global default when unset. Today the pull policy is **global only** — one
`--gitops-pull-policy` value is applied to every stack (`loop.go:298`). The plumbing
is already in place (`port.DeployOpts.PullPolicy` carries it per deploy), so this is
a small, clean change: one config/model field + validation + a 2-line override at the
deploy site.

This completes the "global + per-stack" parity that `DESCRIPTION.md` advertises but
only the global half ships today.

## Settings

- **Testing:** yes — table-driven config tests, loop override test (fake deployer
  captures `DeployOpts`), integration seam assertion. `go test -race` must stay green.
- **Logging:** verbose — a DEBUG line per sync recording the resolved pull policy and
  its source (`per-stack` vs `global`).
- **Docs:** yes (mandatory checkpoint, routed via `/aif-docs`) — field reference in
  `docs/gitops.md`, commented example in `examples/gitops/stacks.yaml`, and a
  truthfulness fix in `DESCRIPTION.md`.

## Roadmap Linkage

- **Milestone:** "Per-stack image pull policy (v0.5.0)"
- **Rationale:** This plan delivers exactly that roadmap milestone — the per-stack
  `pull_policy` override. On completion `/aif-implement` may mark the milestone done
  once the implementation evidence is clear.

## Design (confirmed against current code)

- `port.DeployOpts` already has `PullPolicy string` (`internal/core/port/gitops.go`);
  the deploy adapter already maps it to `--resolve-image <policy>`
  (`internal/adapter/stackdeploy/deploy.go:66-71`, defaulting to `changed` when empty).
  **No port or adapter changes.**
- The sync loop builds `port.DeployOpts` at `internal/app/gitopsync/loop.go:297-300`
  using the global `l.pullPolicy`; the per-stack `model.StackConfig` (`st`) is already
  in scope there. The override is resolved inline before the `Deploy` call.
- Validation mirrors the global check at `config.go:259-262`
  (`gitops_pull_policy must be always|changed`).

## Tasks

### Phase 1 — Config & model
- [x] **1. Add `PullPolicy` field + mapping + validation**
  (`internal/core/model/gitops.go`, `internal/config/gitops.go`).
  - Add `PullPolicy string` to `model.StackConfig` (doc comment: override of global,
    empty = use global).
  - Add `PullPolicy string yaml:"pull_policy"` to `fileStack`; map it into
    `model.StackConfig` in `loadStacksFile`.
  - Validate in the per-stack loop: non-empty and not `always|changed` → error
    `gitops: stack %q pull_policy must be always|changed, got %q`.
  - **blockedBy:** none.

### Phase 2 — Loop
- [x] **2. Thread per-stack override through the sync loop + DEBUG log**
  (`internal/app/gitopsync/loop.go`).
  - Resolve `policy := l.pullPolicy; if st.PullPolicy != "" { policy = st.PullPolicy }`
    before the deploy call; pass into `port.DeployOpts{PullPolicy: policy}`.
  - `log.Debug("gitops: pull policy resolved", "stack", st.Name, "pull_policy", policy,
    "source", <per-stack|global>)`.
  - **blockedBy:** 1.

### Phase 3 — Tests
- [x] **3. Config + loop + integration tests**
  (`internal/config/config_test.go`, `internal/app/gitopsync/loop_test.go`,
  `internal/app/gitopsync/integration_test.go`).
  - Config: parse `always`/`changed`; reject `latest`; omitted → `""`.
  - Loop: extend `fakeDeployer` to capture `opts.PullPolicy`; two stacks (one override,
    one not) under global `always` → assert `changed` and `always` respectively.
  - Integration: `captureDeploy` records the policy; assert per-stack value reaches the
    seam.
  - **blockedBy:** 1, 2.

### Phase 4 — Docs
- [x] **4. Document the field + fix the over-promise**
  (`docs/gitops.md`, `examples/gitops/stacks.yaml`, `.ai-factory/DESCRIPTION.md`).
  - Add `pull_policy` row to the stacks.yaml field table + a precedence note.
  - Commented example in `examples/gitops/stacks.yaml`.
  - Make `DESCRIPTION.md`'s "global + per-stack" wording truthful (now that per-stack
    ships); align `docs/configuration.md` if it mirrors the flag.
  - **blockedBy:** 2.

## Commit Plan

4 tasks — below the 5-task threshold, so a **single conventional commit** at the end
is appropriate (no intermediate checkpoints). Suggested message:

```
feat(gitops): per-stack pull_policy override in stacks.yaml

Add a `pull_policy: always|changed` field to stacks.yaml that overrides the
global --gitops-pull-policy for a single stack (empty = global fallback).
Validated at load time; resolved per-deploy in the sync loop with a DEBUG
log of source (per-stack vs global). DeployOpts plumbing was already in place.
```

(The `ROADMAP.md` v0.5.0 edit already in the working tree should be committed
separately as `docs(roadmap): add v0.5.0 per-stack pull policy + metrics milestones`.)

## Verification

- `go build ./...`
- `go test ./internal/...`
- `go test -race ./internal/app/gitopsync/... ./internal/config/...`
- `golangci-lint run` (project CI gate)
- Manual: set two stacks, one `pull_policy: changed` under global `always`, run with
  `--dry-run` off, confirm `docker stack deploy ... --resolve-image changed` for the
  overridden stack and `--resolve-image always` for the other (DEBUG log shows source).

## Next Steps

```
/aif-implement     # execute the 4 tasks in dependency order
/aif-verify        # confirm nothing was missed, tests + lint green
```
