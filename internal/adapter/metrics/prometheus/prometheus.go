package prometheus

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	coremodel "github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// defaultTimeout bounds a query when the caller passes a non-positive timeout.
const defaultTimeout = 10 * time.Second

// queryAPI is the subset of the Prometheus v1 API this provider uses. Narrowing
// it to an interface lets tests inject canned results without a live server —
// the same pattern as the swarm/dockerstats adapters.
type queryAPI interface {
	Query(ctx context.Context, query string, ts time.Time, opts ...promv1.Option) (model.Value, promv1.Warnings, error)
}

// Provider implements port.MetricsProvider by running one instant PromQL query
// per service. The query comes from svc.Policy.Query and must reduce to a scalar
// or a single-series vector. It depends inward on internal/core; the Prometheus
// client never leaks past this adapter.
type Provider struct {
	api     queryAPI
	timeout time.Duration
	logger  *slog.Logger
}

// compile-time proof the provider satisfies the core port.
var _ port.MetricsProvider = (*Provider)(nil)

// New constructs a Prometheus metrics provider against the given base URL. A
// non-positive timeout falls back to defaultTimeout; a nil logger to slog.Default.
func New(baseURL string, timeout time.Duration, logger *slog.Logger) (*Provider, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	cli, err := promapi.NewClient(promapi.Config{Address: baseURL})
	if err != nil {
		return nil, fmt.Errorf("prometheus: invalid address %q: %w", baseURL, err)
	}
	logger.Debug("prometheus: provider configured", "address", baseURL, "timeout", timeout)
	return &Provider{api: promv1.NewAPI(cli), timeout: timeout, logger: logger}, nil
}

// Value runs the service's PromQL query and returns the resulting scalar. An
// empty result (or a NaN/Inf value) returns coremodel.ErrNoMetricData so the
// reconciler skips the service this tick; an empty query, a multi-series result,
// or a transport error returns a descriptive wrapped error (logged and skipped).
func (p *Provider) Value(ctx context.Context, svc coremodel.ManagedService) (float64, error) {
	query := strings.TrimSpace(expandQuery(svc.Policy.Query, svc))
	if query == "" {
		return 0, fmt.Errorf("prometheus: service %s uses source=prometheus but has no swarm.autoscaler.query", svc.Ref.Name)
	}

	qctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	p.logger.Debug("prometheus: querying", "service", svc.Ref.Name, "query", query)
	value, warnings, err := p.api.Query(qctx, query, time.Time{})
	if err != nil {
		return 0, fmt.Errorf("prometheus: query for %s: %w", svc.Ref.Name, err)
	}
	for _, w := range warnings {
		p.logger.Warn("prometheus: query warning", "service", svc.Ref.Name, "warning", w)
	}

	f, err := scalarFromValue(value)
	if err != nil {
		return 0, fmt.Errorf("prometheus: %s: %w", svc.Ref.Name, err)
	}
	p.logger.Debug("prometheus: service metric",
		"service", svc.Ref.Name, "metric", svc.Policy.Metric, "value", f)
	return f, nil
}

// expandQuery substitutes $SERVICE and $SERVICE_ID placeholders so operators can
// write one reusable, service-templated PromQL expression. $SERVICE_ID is
// replaced before $SERVICE so the longer token wins.
func expandQuery(query string, svc coremodel.ManagedService) string {
	return strings.NewReplacer(
		"$SERVICE_ID", svc.Ref.ID,
		"$SERVICE", svc.Ref.Name,
	).Replace(query)
}

// scalarFromValue reduces a PromQL result to a single float. A scalar yields its
// value; a vector must hold exactly one sample. An empty vector or a non-finite
// value means "no data right now" (coremodel.ErrNoMetricData); more than one
// series is an operator misconfiguration.
func scalarFromValue(v model.Value) (float64, error) {
	switch t := v.(type) {
	case *model.Scalar:
		return finite(float64(t.Value))
	case model.Vector:
		switch len(t) {
		case 0:
			return 0, coremodel.ErrNoMetricData
		case 1:
			return finite(float64(t[0].Value))
		default:
			return 0, fmt.Errorf("query returned %d series; reduce it to a scalar or a single series", len(t))
		}
	default:
		return 0, fmt.Errorf("unsupported result type %s (want scalar or vector)", v.Type())
	}
}

// finite maps NaN/±Inf (no usable value) to coremodel.ErrNoMetricData.
func finite(f float64) (float64, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, coremodel.ErrNoMetricData
	}
	return f, nil
}
