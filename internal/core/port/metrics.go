package port

import (
	"context"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// MetricsProvider yields the current aggregate value of a managed service's
// scaling metric. The provider reads svc.Policy.Metric to know which metric to
// report. Adapters under internal/adapter/metrics implement it; the core
// depends only on this interface.
//
// Implementations should return model.ErrNoMetricData (not a hard error) when a
// metric is simply unavailable for the service right now, so callers skip it
// rather than treating it as a failure.
type MetricsProvider interface {
	Value(ctx context.Context, svc model.ManagedService) (float64, error)
}
