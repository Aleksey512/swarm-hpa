[← Observability](observability.md) · [Back to README](../README.md)

# Development

How to build, run, and test the daemon locally, and what the CI pipeline enforces.

## Prerequisites

| Tool | Version | Needed for |
|------|---------|-----------|
| Go | 1.25+ | build, test |
| `golangci-lint` | v2.x | `make lint` (CI installs it automatically) |
| Docker | any recent | running against a real Swarm (optional for tests) |

The test suite is **pure stdlib** (`testing`) plus one test-only dependency,
`go.uber.org/goleak`, for goroutine-leak detection. No live Docker or Prometheus
is required — the core is exercised through port fakes.

## Make targets

```bash
make build             # → bin/swarm-hpa (version embedded via -ldflags)
make run ARGS="--dry-run=false"   # go run the daemon with flags
make fmt               # gofmt -s -w .
make fmt-check         # verify gofmt cleanliness (non-mutating; CI gate)
make vet               # go vet ./...
make lint              # golangci-lint run
make test              # go test ./...
make test-race         # go test -race ./...
make cover             # coverage profile + per-func summary
make test-integration  # go test -tags integration -race ./...
make tidy              # go mod tidy
make help              # list all targets
```

## Testing

The decision logic lives in a pure core (`internal/core/*`) with no
infrastructure imports, so it is tested directly and deterministically:

- **Table-driven unit tests** for the autoscaler math, healer stuck-pending
  signature, label parsing, cooldown/stabilization, and the Swarm/Prometheus/
  Docker-stats adapters.
- **Port fakes** (`SwarmController`, `MetricsProvider`, `Clock`, `Recorder`) let
  the reconcile loop be driven without a live daemon.
- **Race detector** — run `make test-race` before pushing; the mutex-guarded
  cooldown and stabilizer have dedicated concurrency tests.

```bash
make test-race     # unit tests under the race detector
make cover         # prints total coverage at the end
```

### Goroutine-leak checks

Packages that spawn goroutines (`internal/app/reconciler` and `cmd/swarm-hpa`)
run their tests under [`go.uber.org/goleak`](https://github.com/uber-go/goleak)
via a `TestMain` guard. A loop or ticker that ignores context cancellation, or a
metrics server that fails to shut down, fails the test run.

### Integration harness

The end-to-end harness is compiled only under the `integration` build tag. It
wires the whole daemon (`buildApp` → `app.run`: metrics server + reconcile loop
→ graceful shutdown) with fakes and an **injected tick source**, so the loop can
be stepped deterministically and the full start → tick → SIGTERM lifecycle
asserted — no Docker socket, no Prometheus. goleak confirms a clean shutdown
leaves no goroutines behind.

```bash
make test-integration            # runs unit + integration-tagged tests, with -race
go test -tags integration ./...  # equivalent without the race detector
```

The injectable seams that make this possible:

- `reconciler.WithTickSource(...)` — override the loop's tick channel (default
  wraps `time.NewTicker`; production behavior is unchanged).
- `buildApp(cfg, appDeps)` / `app.run(ctx)` in `cmd/swarm-hpa` — construct the
  daemon with injected ports, then drive its lifecycle.

## Continuous integration

`.github/workflows/ci.yml` runs on every push to `main` and every pull request:

| Step | Command |
|------|---------|
| Formatting | `make fmt-check` |
| Vet | `make vet` |
| Lint | `golangci-lint` (v2, via `golangci-lint-action`) |
| Build | `make build` |
| Unit tests (race) | `make test-race` |
| Integration tests | `make test-integration` |
| Coverage summary | `make cover` |

Any non-zero step fails the build. Run `make fmt-check vet lint test-race
test-integration` locally to reproduce the gate before pushing.

## See Also

- [Getting Started](getting-started.md) — prerequisites, build, run, verify.
- [Observability](observability.md) — the daemon's own `/metrics` endpoint.
