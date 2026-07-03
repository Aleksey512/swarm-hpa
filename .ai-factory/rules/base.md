# Project Base Rules

> Auto-detected/seeded conventions for a Go daemon. No source code exists yet, so
> these are standard Go conventions tailored to this project. Edit as the
> codebase grows.

## Naming Conventions

- Files: lowercase, short, no underscores where avoidable (`autoscaler.go`,
  `healer.go`, `metrics_prometheus.go` only when a compound is unavoidable);
  tests as `<name>_test.go`.
- Packages: short, lowercase, no underscores or camelCase (`autoscaler`,
  `healer`, `metrics`, `swarm`).
- Variables / functions: `camelCase` (unexported), `PascalCase` (exported).
- Interfaces: name by behavior (`MetricsProvider`), small and focused.
- Constants: `PascalCase` or `camelCase`; group related constants in `const (...)`.
- Label keys: namespaced under `swarm.autoscaler.*` (and healing equivalents),
  defined once as constants — never hardcode label strings inline.

## Module Structure

- Single Go module (`go.mod`) at repository root; module path TBD.
- `cmd/<binary>/main.go` — entry point: flag/env parsing, wiring, run loop start.
- `internal/` for all application packages (not importable externally):
  - `internal/swarm/` — Docker SDK client wrapper (list services/tasks, update).
  - `internal/autoscaler/` — scaling decision logic and replica computation.
  - `internal/healer/` — stuck-task detection and force-update recovery.
  - `internal/metrics/` — `MetricsProvider` interface + `dockerstats` and
    `prometheus` implementations.
  - `internal/config/` — label parsing and daemon-level config.
- Keep decision logic provider-agnostic and Docker-SDK-agnostic where practical
  (depend on interfaces, not concrete clients) to keep units testable.

## Error Handling

- Return `error` as the last return value; wrap with context using
  `fmt.Errorf("...: %w", err)`.
- The reconciliation loop must never crash on a single transient API error:
  log at WARN/ERROR and continue to the next iteration.
- Use `context.Context` for cancellation/timeouts on all Docker/Prometheus calls;
  honor a top-level context tied to SIGINT/SIGTERM for graceful shutdown.
- Do not panic in normal operation; `panic` only for programmer errors at startup
  wiring.

## Logging

- Use `log/slog` (structured). Level configurable via `LOG_LEVEL`.
- Every observation and decision is logged with structured fields
  (`service`, `current_replicas`, `desired_replicas`, `metric`, `value`,
  `decision`, `reason`).
- Every mutating action — and every action suppressed by dry-run — is logged
  explicitly (`action`, `dry_run`, `applied`).
- No secrets in logs.

## Testing

- Standard `testing` package; table-driven tests for decision logic.
- Mock the Docker client and `MetricsProvider` via interfaces — no live Docker in
  unit tests.
- Prioritize coverage of scaling math, clamping to `min`/`max`, cooldown gating,
  and stuck-task detection signature.

## Safety (project-specific)

- Mutations default to OFF (dry-run). Code paths that mutate Swarm must check the
  dry-run flag in one central guarded function, not scattered call sites.
- Never act on a service that lacks the explicit `swarm.autoscaler.*` opt-in
  labels.
- Clamp replica changes to `[min, max]` and respect cooldown windows before any
  `ServiceUpdate`.
