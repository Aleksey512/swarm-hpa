# Architecture: Explicit Architecture (Ports & Adapters)

## Overview

This daemon uses Explicit Architecture in its **ports-and-adapters (hexagonal)**
reading, kept deliberately lightweight for a single-binary Go service — no DDD
aggregates, no CQRS. The **core** holds the decision logic (autoscaling math,
stuck-task detection) and the **ports** (interfaces) it needs from the outside
world. Everything that talks to a real system — the Docker Swarm SDK, the metrics
sources, the `/metrics` endpoint, the clock — is an **adapter** that depends
inward on the core. The core never imports `github.com/docker/docker/client` or a
Prometheus client.

This was chosen because the project's two hardest requirements are **testability**
and **swappable metrics**: the scaling/healing decisions must be unit-testable
without a live Swarm, and `MetricsProvider` must switch between Docker stats and
Prometheus without touching decision logic. Ports give both for free.

## Decision Rationale

- **Project type:** Long-running control-loop daemon for Docker Swarm (HPA + task healer)
- **Tech stack:** Go (stdlib-oriented), Docker Go SDK, Prometheus, `log/slog`
- **Key factor:** Pure, testable decision core + swappable infrastructure (metrics
  providers, Swarm client) behind interfaces. Mutations are prod-critical, so the
  safety policy (dry-run + cooldown + opt-in) lives at one application-layer chokepoint.

## Folder Structure

Extends the standard Go `cmd/` + `internal/` layout (see the `golang-project-layout`
skill). Module path is illustrative.

```text
.
├── cmd/
│   └── swarm-hpa/
│       └── main.go                # COMPOSITION ROOT: flags/env, wiring, signal handling, run loop
│
├── internal/
│   ├── core/                      # ── DOMAIN (pure; stdlib-only, no Docker/Prometheus imports) ──
│   │   ├── model/                 # Domain types: ServicePolicy, ScaleDecision, TaskView, NodeView
│   │   ├── port/                  # Interfaces (ports): MetricsProvider, SwarmController, Clock
│   │   ├── autoscaler/            # Pure scaling decision logic (metric+policy → desired replicas)
│   │   └── healer/                # Pure stuck-pending-task detection logic (TaskView → verdict)
│   │
│   ├── app/                       # ── APPLICATION (use-case orchestration) ──
│   │   └── reconciler/            # Reconcile loop; the SINGLE guarded mutation path (dry-run + cooldown)
│   │
│   ├── adapter/                   # ── INFRASTRUCTURE (adapters implement core ports) ──
│   │   ├── swarm/                 # Docker SDK adapter → implements port.SwarmController
│   │   ├── metrics/
│   │   │   ├── dockerstats/       # → implements port.MetricsProvider (Docker Engine stats)
│   │   │   └── prometheus/        # → implements port.MetricsProvider (PromQL)
│   │   └── observability/         # slog setup + prometheus client_golang /metrics
│   │
│   └── config/                    # Flag/env parsing + swarm.autoscaler.* label parsing → model
│
└── (go.mod, Makefile, Dockerfile, etc.)
```

## Dependency Rules

Dependencies point **inward** toward `core`. The compiler enforces this in Go via
import graphs — keep it honest.

- ✅ `adapter/*` imports `core` (to implement `core/port` interfaces and use `core/model` types)
- ✅ `app/*` imports `core` (calls decision logic, depends on `core/port` interfaces)
- ✅ `cmd/swarm-hpa` imports `core`, `app`, `adapter`, `config` (composition root wires concrete adapters into ports)
- ❌ `core/*` MUST NOT import `adapter/*`, `app/*`, `config/*`, `github.com/docker/docker/...`, or any Prometheus client
- ❌ `app/*` MUST NOT import `adapter/*` concrete types — it depends only on `core/port` interfaces (adapters are injected from `cmd`)
- ❌ No upward imports (an inner package importing an outer one) and no import cycles

Litmus test: if you `grep -r "docker/docker" internal/core/` and get a hit, a
dependency rule is broken.

## Layer / Module Communication

- **cmd → app/adapter (wiring):** `main.go` builds the Docker client, constructs the
  chosen `MetricsProvider` adapter and the `SwarmController` adapter, and injects them
  into the reconciler via constructor parameters (interfaces).
- **app → core (decisions):** the reconciler calls pure functions/types in
  `core/autoscaler` and `core/healer`, passing in values read through ports; it never
  embeds business rules itself.
- **app → outside (effects) via ports:** the reconciler reads metrics through
  `port.MetricsProvider` and applies changes through `port.SwarmController` — both
  interfaces, so tests inject fakes.
- **adapter → core (implements):** each adapter satisfies a `core/port` interface and
  maps external shapes (Docker `swarm.Task`, PromQL results) into `core/model` types.

## Key Principles

1. **Pure core.** `internal/core` has zero infrastructure imports. Decisions are
   deterministic functions of their inputs (policy, metric value, task/node views) and
   are table-test friendly.
2. **Ports defined by the core, implemented by adapters.** `MetricsProvider`,
   `SwarmController`, and `Clock` live in `core/port`; `adapter/*` implement them.
   Swapping dockerstats ↔ prometheus is a wiring change in `cmd`, not a core change.
3. **One guarded mutation chokepoint.** Every `ServiceUpdate` (scale and heal) flows
   through the reconciler's guarded path that checks **dry-run**, **opt-in labels**, and
   **cooldown** before calling `SwarmController`. The safety policy is in one place.
4. **Inject the clock.** Cooldowns and "pending too long" use a `Clock` port, never
   `time.Now()` directly in decision code — so time-based logic is testable.
5. **Accept interfaces, return structs.** Constructors take port interfaces; adapters
   return concrete structs. (See `golang-design-patterns`.)
6. **Composition root only in `cmd`.** All concrete-type wiring happens in `main.go`;
   no package reaches out to construct its own dependencies.

## Code Examples

### Ports defined in the core (`internal/core/port`)

```go
package port

import (
    "context"
    "time"

    "swarm-hpa/internal/core/model"
)

// MetricsProvider yields the current value of a service's scaling metric.
// Implemented by adapter/metrics/dockerstats and adapter/metrics/prometheus.
type MetricsProvider interface {
    Value(ctx context.Context, svc model.ServiceRef, metric model.Metric) (float64, error)
}

// SwarmController is the only way the core mutates Swarm. Implemented by adapter/swarm.
type SwarmController interface {
    ManagedServices(ctx context.Context) ([]model.ManagedService, error)
    Scale(ctx context.Context, id string, replicas uint64) error
    ForceUpdate(ctx context.Context, id string) error // heal: SDK ForceUpdate++
}

// Clock is injected so cooldown / "pending too long" logic is testable.
type Clock interface{ Now() time.Time }
```

### Pure decision logic in the core (`internal/core/autoscaler`)

```go
package autoscaler

import "swarm-hpa/internal/core/model"

// Desired computes the target replica count from the current value and policy.
// Pure: no I/O, no clock, no Docker — trivially unit-testable.
func Desired(current uint64, value float64, p model.ServicePolicy) uint64 {
    if p.Target <= 0 {
        return current // misconfigured policy: never act
    }
    desired := uint64(float64(current) * (value / p.Target)) // proportional, like K8s HPA
    return clamp(desired, p.Min, p.Max)
}

func clamp(v, lo, hi uint64) uint64 {
    switch {
    case v < lo:
        return lo
    case v > hi:
        return hi
    default:
        return v
    }
}
```

### Adapter implements a port (`internal/adapter/metrics/prometheus`)

```go
package prometheus

import (
    "context"

    "swarm-hpa/internal/core/model"
    "swarm-hpa/internal/core/port" // adapter depends INWARD on the core
)

type Provider struct{ /* http client, base URL */ }

// compile-time proof the adapter satisfies the core port:
var _ port.MetricsProvider = (*Provider)(nil)

func (p *Provider) Value(ctx context.Context, svc model.ServiceRef, m model.Metric) (float64, error) {
    // run PromQL, map the result to a float64 …
    return 0, nil
}
```

### Composition root wires ports to adapters (`cmd/swarm-hpa/main.go`)

```go
cli, _ := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())

var metrics port.MetricsProvider
switch cfg.Provider {
case "prometheus":
    metrics = prometheus.New(cfg.PromURL)
default:
    metrics = dockerstats.New(cli)
}

swarmCtl := swarm.New(cli)                       // implements port.SwarmController
rec := reconciler.New(swarmCtl, metrics, clock.System{}, cfg.DryRun, cfg.Cooldown)
rec.Run(ctx)                                     // ctx tied to SIGINT/SIGTERM
```

## Anti-Patterns

- ❌ Importing `github.com/docker/docker/...` or a Prometheus client anywhere under
  `internal/core/` — breaks core purity and testability.
- ❌ Putting scaling/healing rules in the reconciler or in an adapter instead of in
  `core/autoscaler` / `core/healer` — logic leaks out of the testable center.
- ❌ Calling `ServiceUpdate` from more than one place — there must be exactly one
  guarded mutation path (dry-run + cooldown + opt-in).
- ❌ Using `time.Now()` inside decision logic instead of the injected `Clock`.
- ❌ A package constructing its own Docker client / adapters instead of receiving them
  from the composition root.
- ❌ Import cycles between `core`, `app`, and `adapter` (Go will refuse to compile;
  treat the first sign of one as a layering smell to fix, not to work around).
