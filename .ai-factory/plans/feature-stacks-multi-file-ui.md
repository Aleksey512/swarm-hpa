# Implementation Plan: Per-file status in the /stacks UI (v0.6.0 multi-compose)

Branch: feature/stacks-multi-file-ui
Created: 2026-07-24

## Goal

Make the `/stacks` UI (JSON + HTML) correctly reflect the v0.6.0 multi-compose-file
feature. Today the status model and UI are purely **per-stack**: a stack with N
compose files shows a single row, and a mid-stack failure (file *k* fails after
files *1…k−1* deployed) is indistinguishable from a single-file failure. The
headline per-file pull policy (e.g. app `always` / postgres `changed`) is invisible.

This plan adds a **per-file status** vertical slice: model → loop recording →
status-store deep-copy → `/stacks` JSON + HTML rendering → tests → docs.

## Design

- New `model.StackFileStatus{File, PullPolicy, Status, Error}` and
  `StackStatus.Files []StackFileStatus`, in deploy order.
- `Status` state machine: `""` (pending — pre-deploy failure / not synced),
  `ok`, `failed` (+Error), `skipped` (an earlier file failed; sequential deploys
  stop at the first failure).
- The loop initializes `Files` with each file's **effective pull policy**
  (file → stack → global) up front, then flips entries to `ok` / `failed` /
  `skipped` as the deploy loop proceeds. Pre-deploy failures leave `Files` empty
  (the stack-level `ErrorStage` already says deploy wasn't reached).
- The status store deep-copies the `Files` slice (value-type elements → shallow
  slice copy is a correct deep copy), matching the existing `DesiredReplicas`
  treatment.
- `stackapi` adds a `files[]` array to the JSON response and a **"files" column**
  to the HTML table: per file — path + pull-policy badge + status badge (ok=green,
  failed=red+error, skipped=grey, pending=muted). Compact for the common
  single-file-ok case; a partial failure is visually obvious.
- **No deploy-logic change** — this is read-only status surfacing on top of the
  v0.6.0 pipeline. The deploy adapter / renderer / ports are untouched.

## Settings

- Testing: yes
- Logging: verbose
- Docs: yes  # mandatory docs checkpoint in /aif-implement

## Roadmap Linkage

Milestone: "none"
Rationale: Skipped — no open roadmap milestone (v0.6.0 just released; no v0.7.0
section). This is a UI/status-reporting refinement of v0.6.0, not a new milestone.
`/aif-verify --strict` may WARN on missing linkage; acceptable.

## Commit Plan

- **Commit 1** (tasks 7-9): `feat(stackstatus): per-file deploy status — model, loop recording, store deep-copy`
- **Commit 2** (task 10): `feat(stackapi): surface per-file status in /stacks JSON + HTML UI`
- **Commit 3** (task 11): `test(stackstatus): per-file status, store deep-copy, handler/template`
- **Commit 4** (task 12): `docs(gitops): document per-file /stacks UI breakdown`

> NOTE: commits in this repo omit the `Co-Authored-By` trailer (project rule).

## Tasks

> Task IDs reference the `/tasks` list (dependencies set via `blockedBy`).
> Note: tasks 7-12 continue the shared task list (1-6 were the v0.6.0 feature,
> now complete).

### Phase 1: Status model + recording + store

- [x] **Task 7** — Model: `StackFileStatus` + `StackStatus.Files`
  - New per-file type (Status state machine `""|ok|failed|skipped`) + `Files` field.
  - Files: `internal/core/model/stackstatus.go`. (No deps.)
<!-- Commit checkpoint: tasks 7-9 -->
- [x] **Task 8** — Loop: record per-file deploy status (ok/failed/skipped)
  - Initialize `Files` with effective pull policies up front; flip to ok/failed/skipped
    in the deploy loop; thread into `recordStatus`. Partial failure now visible.
  - Files: `internal/app/gitopsync/loop.go`. (Depends on 7.)
- [x] **Task 9** — Status store: deep-copy `Files` slice on set + snapshot
  - Match the existing `DesiredReplicas` deep-copy; value-type elements.
  - Files: `internal/adapter/statusstore/store.go`. (Depends on 7.)

### Phase 2: UI

- [x] **Task 10** — stackapi: per-file status in JSON + HTML UI
  - `files[]` in JSON; new "files" column in the HTML table with status badges.
  - Files: `internal/adapter/stackapi/handler.go`, `internal/adapter/stackapi/<ui template>`.
    (Depends on 7, 9.)
<!-- Commit checkpoint: task 10 -->

### Phase 3: Tests

- [x] **Task 11** — Tests: per-file status, store deep-copy, handler/template
  - Loop: success(all ok) / partial(ok/failed/skipped) / pre-deploy(empty) /
    single-file regression. Store: deep-copy both directions. Handler/template:
    JSON `files[]` + HTML renders the failed file.
  - Files: `internal/app/gitopsync/loop_test.go`, `internal/adapter/statusstore/*_test.go`,
    `internal/adapter/stackapi/*_test.go`. (Depends on 8, 9, 10.)
<!-- Commit checkpoint: task 11 -->

### Phase 4: Docs

- [x] **Task 12** — Docs: per-file breakdown in `/stacks` UI
  - `docs/gitops.md` Status/UI section: per-file path + policy + status; partial-failure
    appearance; JSON `files[]` shape; cross-link to v0.6.0 multi-compose section.
  - Files: `docs/gitops.md`. (Depends on 10.)
<!-- Commit checkpoint: task 12 -->

## Notes for the implementer

- This is **read-only status surfacing** — do NOT change the deploy pipeline,
  `DeployOpts`, renderer, or ports. The v0.6.0 per-file deploy loop already
  iterates `st.ComposeFiles`; this plan only records what it did.
- Go `html/template` auto-escapes `{{ }}` text — rely on it for file paths/errors.
- Verify the UI template's location (embedded string vs `ui.html`) before editing
  task 10; the Explore agent saw it rendered via `uiTmpl`.
- `StackFileStatus` is a value type (no pointers/maps) → `copy(slice, ...)`
  is a correct deep copy; no per-field cloning needed.
