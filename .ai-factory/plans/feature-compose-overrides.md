# Implementation Plan: Compose overrides per stack file (merged deploy)

Branch: feature/compose-overrides
Created: 2026-08-07

## Goal

Let one `compose_file` entry declare **override files** that are merged into a
**single** deploy:

```
docker stack deploy -c base.yml -c override.yml -c another.override.yml
```

Docker Compose's `include:` is a Compose-only feature — Swarm does not support
it. The Swarm-native equivalent is exactly the multi-`-c` form above, where
docker/cli merges the documents (last-wins per key) before deploying.

**User case:** define a shared base stack once (e.g. monitoring), then
re-parameterize it per environment from small override files — change
`environment`, image tags, replicas — without duplicating the whole compose.

## Key distinction from v0.6.0 (read this first)

The repo already has "multiple compose files per stack" (v0.6.0). It is a
**different mechanism** and both must coexist unambiguously:

| | v0.6.0 multi-file | v0.7.0 overrides (this plan) |
|---|---|---|
| Config | several **entries** in `compose_file` | `overrides` **inside one entry** |
| Deploys | one `docker stack deploy` **per file** | **one** deploy for the whole group |
| Semantics | additive, **no merge** (Swarm does not prune) | docker/cli **compose merge**, last-wins |
| Each file | must be **self-contained** | overrides need **not** be self-contained |
| Pull policy | per file | per **group** (one `--resolve-image` per deploy) |

Terminology used throughout the plan and the code: a **merge group** = one
`ComposeFileSpec` = base file + its overrides = one `docker stack deploy`.

## Config shape

Per-file `overrides`, layered onto the existing polymorphic `compose_file`
(chosen over a stack-level shorthand: one source of truth, and it composes
cleanly with multi-entry stacks):

```yaml
monitoring:
  repo: infra
  compose_file:
    - file: monitoring/base.yml
      overrides:
        - monitoring/prod.yml
        - monitoring/env.override.yml
      pull_policy: always
    - traefik.yml            # separate, additive deploy (v0.6.0 behavior)

# -> deploy 1: -c monitoring/base.yml -c monitoring/prod.yml -c monitoring/env.override.yml
# -> deploy 2: -c traefik.yml
```

Backward compatible: the scalar form, the `[]string` form and objects without
`overrides` behave exactly as today.

## Key design decision: delegate the merge to docker/cli

The daemon does **not** implement compose merge. It writes one temp file per
document and passes several `-c` flags, letting docker/cli apply its own merge
rules.

Rationale:

- **Fidelity.** Compose merge is non-trivial (maps deep-merge; some sequences
  replace, some merge by key). Reimplementing it is a permanent divergence risk,
  and `compose-go` is not in the module graph — docker/cli's merge is unexported.
- **Relative paths.** Each document's temp file is written next to its own source
  file, so relative `configs:`/`secrets:` paths resolve per file, exactly as
  Docker resolves them. An in-process merge would have to rewrite those paths.
- **Rotation stays local.** `compose.ApplyRotation` only sets `name` on the
  top-level `configs:`/`secrets:` object. Under docker's merge the rotated `name`
  travels with the same object that carries the winning `file:` key, so per-doc
  rotation is already correct — no cross-file reference rewriting needed.

**The one thing that must become group-aware: carry-forward.** Autoscaler labels
may live only in the base file while an override re-declares the service (or vice
versa). Detection must run on the **merged** view, and the replica rewrite must
be written into **every** document that declares the service so the merged result
is the live count regardless of which document wins. Getting this wrong silently
breaks the project's differentiator (a GitOps deploy clobbering an autoscaled
replica count) — hence task 4 and its dedicated tests.

The drift snapshot has the same class of bug and is fixed the same way (task 7).

## Port change (breaking, internal)

```go
type ComposeDoc struct {
    Map map[string]any
    Dir string // temp-file dir = the doc's own source dir
}

type StackDeployer interface {
    Deploy(ctx context.Context, name string, docs []ComposeDoc, opts DeployOpts) error
}
```

`DeployOpts.ComposeDir` is removed (it becomes per-doc `Dir`); `PullPolicy` stays
— a merge group is one deploy, so it has one `--resolve-image`. `DeployFunc`
becomes `func(ctx, name string, composeFiles []string, pullPolicy string) error`.

## Settings

- Testing: yes
- Logging: verbose
- Docs: yes  # mandatory docs checkpoint in /aif-implement

## Roadmap Linkage

Milestone: "v0.7.0 — Compose overrides per stack file (merged deploy)"
Rationale: every prior feature landed as a versioned milestone; v0.6.0 is
complete and there is no open section, so this plan proposes the v0.7.0 entry.
The roadmap itself is owned by `/aif-roadmap` (task 13), not by this plan.

## Commit Plan

- **Commit 1** (tasks 1-2): `feat(gitops): compose overrides model + stacks.yaml parsing`
- **Commit 2** (tasks 3-5): `feat(gitops): merged multi-file stack deploy with group-aware carry-forward`
- **Commit 3** (tasks 6-9): `feat(gitops): deploy compose merge groups from the sync loop`
- **Commit 4** (tasks 10-11): `test(gitops): cover compose overrides end to end`
- **Commit 5** (tasks 12-13): `docs(gitops): document compose overrides`

> NOTE: commits in this repo omit the `Co-Authored-By` trailer (project rule).

## Tasks

> Task IDs reference the `/tasks` list. Dependencies are set there
> (`blockedBy`). Implement in ID order.

### Phase 1: Foundation (model + config)

- [x] **Task 1** — Model: `ComposeFileSpec.Overrides` + `AllFiles()` helper
  - Doc comment must contrast merge-group vs additive multi-entry semantics.
  - Files: `internal/core/model/gitops.go`. (No deps.)
<!-- Commit checkpoint: tasks 1-2 -->
- [x] **Task 2** — Config: parse + validate `overrides` in `stacks.yaml`
  - Object form only; `[]any` of non-empty strings; reject non-list, non-string,
    empty and base-file-duplicating entries; all existing shapes unchanged.
  - Files: `internal/config/gitops.go`. (Depends on 1.)

### Phase 2: Deploy path (port + adapter)

- [x] **Task 3** — Port: `ComposeDoc` + `StackDeployer.Deploy(..., docs, opts)`
  - Drop `DeployOpts.ComposeDir`; document `docs[0]` = base, `docs[1:]` = overrides.
  - Files: `internal/core/port/gitops.go`. (Depends on 1.)
- [x] **Task 4** — Carry-forward: `ApplyCarryForwardGroup` over the merged view
  - **Correctness-critical.** Merged labels/mode/bounds for detection; rewrite
    replicas in every doc declaring the service; `errNoServices` only when no doc
    has `services`; single-doc wrapper keeps today's behavior.
  - Files: `internal/adapter/stackdeploy/carryforward.go`. (Depends on 3.)
<!-- Commit checkpoint: tasks 3-5 -->
- [x] **Task 5** — Deploy adapter: N temp files, multiple `-c`, updated retry
  - One temp file per doc in its own `Dir`; all removed on every exit path;
    `-c` order preserved (it decides merge precedence).
  - Files: `internal/adapter/stackdeploy/{deploy,dockercli,retry}.go`. (Depends on 3, 4.)

### Phase 3: Sync loop + status surface

- [x] **Task 6** — Loop: render/decrypt/rotate/deploy per merge group
  - Per-doc render with its own dir; sops discovery unioned per doc's own dir;
    per-doc rotation; one `Deploy` per group; failure/dry-run semantics unchanged.
  - Files: `internal/app/gitopsync/loop.go`. (Depends on 1, 3, 5.)
- [x] **Task 7** — Drift: `desiredReplicasGroup` over the merged view
  - Last-wins replicas; override-added/removed autoscaler label and `mode: global`
    respected; feeds both the `/stacks` drift table and the drift gauges.
  - Files: `internal/app/gitopsync/loop.go`. (Depends on 6.)
- [x] **Task 8** — Status: `Overrides` in `StackFileStatus`, `/stacks` JSON, UI
  - One `StackFileStatus` entry = one merge group = one deploy.
  - Files: `internal/core/model/stackstatus.go`, `internal/app/gitopsync/loop.go`,
    `internal/adapter/stackapi/{handler.go,ui.html}`. (Depends on 6.)
- [x] **Task 9** — Composition root + layering verification
  - `main.go` compiles against the new signatures; `build`/`vet`/`gofmt`/
    `golangci-lint` clean; core purity and app→adapter import checks pass.
  - Files: `cmd/swarm-hpa/main.go`. (Depends on 5, 6, 7, 8.)
<!-- Commit checkpoint: tasks 6-9 -->

### Phase 4: Tests

- [x] **Task 10** — Tests: config parsing, group carry-forward, deploy adapter
  - Includes the label-only-on-override case and temp-file cleanup on error.
  - Files: `internal/config/gitops_test.go`, `internal/adapter/stackdeploy/*_test.go`.
    (Depends on 2, 4, 5.)
- [x] **Task 11** — Tests: loop merge groups, drift, status API
  - One `Deploy` per group with ordered docs; v0.6.0 regression; dry-run;
    cross-directory sops discovery; partial failure; `-race` + goleak clean.
  - Files: `internal/app/gitopsync/loop_test.go`,
    `internal/adapter/stackapi/handler_test.go`. (Depends on 6, 7, 8, 9.)
<!-- Commit checkpoint: tasks 10-11 -->

### Phase 5: Docs & roadmap

- [x] **Task 12** — Docs + examples (mandatory docs checkpoint, via `/aif-docs`)
  - `docs/gitops.md` new section leading with the merge-vs-additive distinction;
    `docs/configuration.md`, `docs/migrating-from-swarm-cd.md`, `README.md`;
    `examples/gitops/` override example.
  - Files: as listed in the task. (Depends on 6, 8.)
- [x] **Task 13** — Roadmap: record the v0.7.0 milestone via `/aif-roadmap`
  - Files: `.ai-factory/ROADMAP.md`. (Depends on 12.)
<!-- Commit checkpoint: tasks 12-13 -->

## Notes for the implementer

- **Do not write a compose merge function.** The whole design rests on delegating
  the merge to docker/cli via multiple `-c`. If you find yourself deep-merging
  maps, you have taken the wrong branch of the design.
- **`-c` order is load-bearing.** Base first, overrides in declaration order.
  Losing the order silently inverts which value wins.
- **Carry-forward and the drift snapshot are the only places that need the merged
  view.** Everything else (rotation, secret discovery, temp files) stays per
  document — that is what makes relative paths keep working.
- Overrides may live in a different directory than the base. Every per-document
  path (`Dir`, rotation `composeDir`, secret discovery `composeDir`) must come
  from *that document's* path, never from the base file's.
- Deploys remain **additive** — `docker stack deploy` does not prune (verified
  empirically; see memory `docker-stack-deploy-no-prune`). Removing a service
  from an override does not remove it from Swarm.
- Groups are **not transactional**: a failing group leaves earlier groups applied.
  The existing WARN covers this; keep it and extend its fields.
