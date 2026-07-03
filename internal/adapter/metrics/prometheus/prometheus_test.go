package prometheus

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	coremodel "github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeAPI is a controllable queryAPI: it records the executed query and returns
// canned results so Value's parsing/error mapping is testable without a server.
type fakeAPI struct {
	val      model.Value
	warnings promv1.Warnings
	err      error
	gotQuery string
}

func (f *fakeAPI) Query(_ context.Context, query string, _ time.Time, _ ...promv1.Option) (model.Value, promv1.Warnings, error) {
	f.gotQuery = query
	return f.val, f.warnings, f.err
}

func promSvc(query string) coremodel.ManagedService {
	return coremodel.ManagedService{
		Ref: coremodel.ServiceRef{ID: "svc-id", Name: "web"},
		Policy: coremodel.ServicePolicy{
			Enabled: true, Source: "prometheus", Query: query,
			Metric: "rps", Target: 100, Min: 1, Max: 5,
		},
	}
}

func newProvider(api queryAPI) *Provider {
	return &Provider{api: api, timeout: time.Second, logger: discardLogger()}
}

func TestValueResultMapping(t *testing.T) {
	cases := []struct {
		name    string
		val     model.Value
		want    float64
		wantErr error // errors.Is target; nil means "no error"
		anyErr  bool  // a non-nil error that is not ErrNoMetricData
	}{
		{name: "scalar", val: &model.Scalar{Value: 42}, want: 42},
		{name: "single-series vector", val: model.Vector{{Value: 7}}, want: 7},
		{name: "empty vector -> no data", val: model.Vector{}, wantErr: coremodel.ErrNoMetricData},
		{name: "NaN scalar -> no data", val: &model.Scalar{Value: model.SampleValue(math.NaN())}, wantErr: coremodel.ErrNoMetricData},
		{name: "Inf vector -> no data", val: model.Vector{{Value: model.SampleValue(math.Inf(1))}}, wantErr: coremodel.ErrNoMetricData},
		{name: "multi-series -> error", val: model.Vector{{Value: 1}, {Value: 2}}, anyErr: true},
		{name: "matrix (range) -> error", val: model.Matrix{}, anyErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newProvider(&fakeAPI{val: tc.val})
			got, err := p.Value(context.Background(), promSvc("up"))
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want errors.Is %v", err, tc.wantErr)
				}
			case tc.anyErr:
				if err == nil || errors.Is(err, coremodel.ErrNoMetricData) {
					t.Fatalf("want a descriptive error (not ErrNoMetricData), got %v", err)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got != tc.want {
					t.Errorf("value = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestValueEmptyQuery(t *testing.T) {
	f := &fakeAPI{val: &model.Scalar{Value: 1}}
	p := newProvider(f)
	_, err := p.Value(context.Background(), promSvc("   "))
	if err == nil || errors.Is(err, coremodel.ErrNoMetricData) {
		t.Fatalf("empty query must be a descriptive error, got %v", err)
	}
	if f.gotQuery != "" {
		t.Errorf("API must not be queried for an empty query, got %q", f.gotQuery)
	}
}

func TestValueQueryError(t *testing.T) {
	p := newProvider(&fakeAPI{err: errors.New("boom")})
	if _, err := p.Value(context.Background(), promSvc("up")); err == nil {
		t.Error("transport error must propagate")
	}
}

func TestValueExpandsServicePlaceholders(t *testing.T) {
	f := &fakeAPI{val: &model.Scalar{Value: 1}}
	p := newProvider(f)
	svc := promSvc(`rate($SERVICE_total{id="$SERVICE_ID"}[1m])`)
	if _, err := p.Value(context.Background(), svc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `rate(web_total{id="svc-id"}[1m])`
	if f.gotQuery != want {
		t.Errorf("expanded query = %q, want %q", f.gotQuery, want)
	}
}

// TestValueThroughRealClient is an end-to-end smoke test: it drives the real
// client_golang API against an httptest server returning a scalar result.
func TestValueThroughRealClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"scalar","result":[1700000000,"42"]}}`)
	}))
	defer srv.Close()

	p, err := New(srv.URL, time.Second, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := p.Value(context.Background(), promSvc("up"))
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if got != 42 {
		t.Errorf("value = %v, want 42", got)
	}
}
