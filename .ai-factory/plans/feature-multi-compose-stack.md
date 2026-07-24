# Implementation Plan: Multiple compose files per stack + per-file pull policy

Branch: feature/multi-compose-stack
Created: 2026-07-23

## Goal

Let one GitOps stack declare **multiple compose files**. Files are deployed **in
list order, one `docker stack deploy` each** (additive — Swarm does not prune),
and each file may carry its own **image pull policy** (`always`|`changed`) that
overrides the stack-level and global policy.

This serves two concrete user cases:

1. **Convenience split (primary):** one product, many microservices, one stack,
   many files for manageability. Each file is an independent service group; they
   accumulate into the single stack namespace.
2. **Dev pull-policy split:** on dev, app images use `:dev` and must pull
   `always`, while `postgres` should pull `changed` only. This requires two
   `docker stack deploy` calls for the same stack with different
   `--resolve-image` values — impossible with a single merged deploy.

## Key design decision: sequential per-file deploy (NOT merge)

`docker stack deploy` accepts exactly one `--resolve-image` value, so a single
merged deploy **cannot** apply different pull policies to different files —
_case 2 forces multiple deploys_. And because deploys are **additive** (verified
empirically: a re-deploy with a different compose file leaves prior services in
place; see memory `docker-stack-deploy-no-prune`), the same sequential mechanism
satisfies case 1 (service groups accumulate).

So the design is **pure sequential per-file deploy**, not in-process merge:

- Each file is rendered + (rotated) + deployed **as-is**.
- **No merge function** is added — each file must be self-contained (declare its
  own `networks`/`volumes`/top-level `secrets`/`configs`).
- List order = deploy order (user orders infrastructure-first).
- This keeps the deploy adapter, `DeployFunc`, the renderer port, and
  `core/compose` **unchanged**: the existing `port.DeployOpts.PullPolicy` already
  carries a per-deploy policy.

Tradeoff accepted: N files → N `docker stack deploy` invocations per sync tick.
Cost is proportional to services anyway (additive, no prune), negligible at
minute-scale sync intervals for a handful of files.

## Config shape (polymorphic `compose_file`)

```yaml
# 1. Scalar (backward compatible, swarm-cd parity):
demoapp:
  repo: demoapp
  compose_file: compose.yaml

# 2. List of strings (case 1 — convenience split):
demoapp:
  repo: demoapp
  compose_file: [services.yaml, monitoring.yaml]

# 3. List of objects with per-file pull_policy (case 2 — dev):
demoapp:
  repo: demoapp
  compose_file:
    - file: app.yaml
      pull_policy: always      # :dev refreshes every deploy
    - file: postgres.yaml
      pull_policy: changed     # postgres not re-pulled needlessly
```

Mixed lists (some strings, some objects) are also accepted; strings inherit the
stack/global policy.

**Pull-policy precedence:** file-level `pull_policy` → stack-level `pull_policy`
→ global `--gitops-pull-policy`.

## Settings

- Testing: yes
- Logging: verbose
- Docs: yes  # mandatory docs checkpoint in /aif-implement

## Roadmap Linkage

Milestone: "none"
Rationale: Skipped — no open roadmap milestone (v0.5.0 is complete; no v0.6.0
section yet). `/aif-verify --strict` may WARN on this; acceptable per aif-plan.

## Commit Plan

- **Commit 1** (after tasks 1-2): `feat(gitops): multi-compose model + per-file pull policy config`
- **Commit 2** (after tasks 3-4): `feat(gitops): sequential per-file stack deploy with per-file pull policy`
- **Commit 3** (after task 5): `test(gitops): cover multi-file stacks and per-file pull policy`
- **Commit 4** (after task 6): `docs(gitops): document multi-file stacks and per-file pull policy`

> NOTE: commits in this repo omit the `Co-Authored-By` trailer (project rule).

## Tasks

> Task IDs reference the `/tasks` list. Dependencies are set there
> (`blockedBy`). Implement in ID order.

### Phase 1: Foundation (model + config)

- [x] **Task 1** — Model: `ComposeFileSpec` + `StackConfig.ComposeFiles []ComposeFileSpec`
  - Add `ComposeFileSpec{File, PullPolicy}`; replace `ComposeFile string`.
  - Files: `internal/core/model/gitops.go`. (No deps.)
<!-- Commit checkpoint: tasks 1-2 -->
- [x] **Task 2** — Config: polymorphic `compose_file` parser + per-file `pull_policy` validation
  - Accept scalar | []string | []object{file,pull_policy} | mixed; validate
    non-empty file and `pull_policy ∈ {"","always","changed"}`; backward compatible.
  - Files: `internal/config/gitops.go`. (Depends on 1.)

### Phase 2: Loop pipeline

- [x] **Task 3** — Loop: sequential per-file render→decrypt→rotate→deploy with per-file pull policy
  - Render each file (shared values); aggregate desired snapshot; decrypt once
    (discover across all maps); per-file rotate + deploy with effective pull
    policy (file>stack>global); per-file composeDir. Keep the whole pipeline
    under the per-repo lock.
  - Files: `internal/app/gitopsync/loop.go`. (Depends on 1.)
<!-- Commit checkpoint: tasks 3-4 -->
- [x] **Task 4** — Loop: aggregate status/drift/deploy-count + partial-failure semantics
  - `DeployApplied`/`markDeploy`/`incDeploy` once per successful stack tick (not
    per file); mid-stack failure → `SyncError("deploy")`, status OK=false, WARN
    log that earlier files are already applied (non-transactional).
  - Files: `internal/app/gitopsync/loop.go`. (Depends on 3.)

### Phase 3: Tests

- [x] **Task 5** — Tests: multi-file parsing + per-file pull policy + partial-failure
  - Config: all 4 shapes + backward compat + error cases. Loop: 2-file distinct
    policies (2 ordered deploys), policy precedence, 1-file regression, dry-run,
    partial failure, carry-forward per deploy. `go test -race` clean.
  - Files: `internal/config/gitops_test.go`, `internal/app/gitopsync/loop_test.go`.
    (Depends on 2, 3, 4.)
<!-- Commit checkpoint: task 5 -->

### Phase 4: Docs & examples

- [x] **Task 6** — Docs + examples: multi-file stacks and per-file pull policy
  - `docs/gitops.md` section (shapes, precedence, self-contained-file constraint,
    additive/no-prune + non-transactional caveats); example in
    `examples/gitops/stacks.yaml`; optional second compose file.
  - Files: `docs/gitops.md`, `examples/gitops/stacks.yaml`. (Depends on 3.)
<!-- Commit checkpoint: task 6 -->

## Notes for the implementer

- **Do not** add a merge function or touch the renderer/deploy ports — the
  design is deliberately sequential and reuses `DeployOpts.PullPolicy`.
- Verify the exact `goccy/go-yaml` `UnmarshalYAML` signature when writing the
  custom parser (task 2); fall back to decoding into `any` + post-process if the
  interface differs.
- Each compose file is deployed as-is → must be self-contained. Document this
  prominently (task 6) and in an inline comment in the loop (task 3).
- Reminder: `docker stack deploy` is additive (no prune). Removing a service
  from compose does not remove it from Swarm. Document this caveat (task 6).
