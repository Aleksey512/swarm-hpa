package stackdeploy

import (
	"context"
	"io"
	"os"
	"testing"

	"log/slog"

	"github.com/goccy/go-yaml"

	"github.com/Aleksey512/swarm-hpa/internal/config"
	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// replicated builds a compose service with deploy.replicas and optional labels.
func replicated(image string, replicas int, labels map[string]any) map[string]any {
	deploy := map[string]any{"replicas": replicas}
	if labels != nil {
		deploy["labels"] = labels
	}
	return map[string]any{"image": image, "deploy": deploy}
}

func autoscaleLabels(min, max string) map[string]any {
	return map[string]any{
		config.LabelEnabled: "true",
		config.LabelMin:     min,
		config.LabelMax:     max,
	}
}

func replicasOf(services map[string]any, name string) int64 {
	d := services[name].(map[string]any)["deploy"].(map[string]any)
	switch v := d["replicas"].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case uint64:
		return int64(v)
	}
	return -1
}

func TestApplyCarryForward(t *testing.T) {
	cases := []struct {
		name     string
		services map[string]any
		live     []model.StackService
		wantRepl map[string]int64 // service name → expected replicas after carry-forward
		wantChg  int
	}{
		{
			name:     "autoscaled preserves live over compose",
			services: map[string]any{"web": replicated("nginx", 3, autoscaleLabels("2", "10"))},
			live:     []model.StackService{{Name: "web", Replicas: 7, Replicated: true}},
			wantRepl: map[string]int64{"web": 7},
			wantChg:  1,
		},
		{
			name:     "plain service untouched (compose-owned)",
			services: map[string]any{"db": replicated("pg", 1, nil)},
			live:     []model.StackService{{Name: "db", Replicas: 9, Replicated: true}},
			wantRepl: map[string]int64{"db": 1},
			wantChg:  0,
		},
		{
			name:     "clamp high to max",
			services: map[string]any{"web": replicated("nginx", 3, autoscaleLabels("2", "10"))},
			live:     []model.StackService{{Name: "web", Replicas: 20, Replicated: true}},
			wantRepl: map[string]int64{"web": 10},
			wantChg:  1,
		},
		{
			name:     "clamp low to min",
			services: map[string]any{"web": replicated("nginx", 3, autoscaleLabels("2", "10"))},
			live:     []model.StackService{{Name: "web", Replicas: 0, Replicated: true}},
			wantRepl: map[string]int64{"web": 2},
			wantChg:  1,
		},
		{
			name: "global service skipped",
			services: map[string]any{"agent": map[string]any{
				"image": "agent", "deploy": map[string]any{"mode": "global", "labels": autoscaleLabels("1", "1")},
			}},
			live:    []model.StackService{{Name: "agent", Replicas: 0, Replicated: false}},
			wantChg: 0,
		},
		{
			name:     "autoscaled not yet live keeps compose value",
			services: map[string]any{"web": replicated("nginx", 3, autoscaleLabels("2", "10"))},
			live:     nil,
			wantRepl: map[string]int64{"web": 3},
			wantChg:  0,
		},
		{
			name: "labels in list form are detected",
			services: map[string]any{"web": map[string]any{
				"image": "nginx",
				"deploy": map[string]any{
					"replicas": 3,
					"labels":   []any{"swarm.autoscaler.enabled=true", "swarm.autoscaler.min=2", "swarm.autoscaler.max=10"},
				},
			}},
			live:     []model.StackService{{Name: "web", Replicas: 7, Replicated: true}},
			wantRepl: map[string]int64{"web": 7},
			wantChg:  1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			compose := map[string]any{"services": tc.services}
			chg, err := ApplyCarryForward(compose, tc.live, discardLog())
			if err != nil {
				t.Fatalf("ApplyCarryForward: %v", err)
			}
			if chg != tc.wantChg {
				t.Errorf("changed = %d, want %d", chg, tc.wantChg)
			}
			for name, want := range tc.wantRepl {
				if got := replicasOf(tc.services, name); got != want {
					t.Errorf("replicas[%s] = %d, want %d", name, got, want)
				}
			}
		})
	}
}

func TestApplyCarryForward_NoServicesMap(t *testing.T) {
	_, err := ApplyCarryForward(map[string]any{"version": "3"}, nil, discardLog())
	if err != errNoServices {
		t.Fatalf("want errNoServices, got %v", err)
	}
}

// --- Deploy end-to-end (fake state + recording deploy seam) ---

type fakeState struct {
	svcs []model.StackService
	err  error
}

func (f fakeState) StackServices(_ context.Context, _ string) ([]model.StackService, error) {
	return f.svcs, f.err
}

type recorder struct {
	called   bool
	name     string
	policy   string
	deployed map[string]any // parsed compose the deploy would have applied
}

func (r *recorder) fn() DeployFunc {
	return func(_ context.Context, name, composeFile, pullPolicy string) error {
		r.called = true
		r.name = name
		r.policy = pullPolicy
		b, err := os.ReadFile(composeFile) //nolint:gosec // G304: composeFile is a temp path the test created
		if err != nil {
			return err
		}
		var m map[string]any
		if err := yaml.Unmarshal(b, &m); err != nil {
			return err
		}
		r.deployed = m
		return nil
	}
}

func TestDeploy_CarryForwardThenDeploy(t *testing.T) {
	compose := map[string]any{"services": map[string]any{
		"web": replicated("nginx", 3, autoscaleLabels("2", "10")),
		"db":  replicated("pg", 1, nil),
	}}
	state := fakeState{svcs: []model.StackService{{Name: "web", Replicas: 7, Replicated: true}}}
	rec := &recorder{}
	dep := New(state, rec.fn(), discardLog())

	if err := dep.Deploy(context.Background(), "mystack", compose, port.DeployOpts{PullPolicy: "always"}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if !rec.called {
		t.Fatal("deploy seam not invoked")
	}
	if rec.name != "mystack" {
		t.Errorf("deploy name = %q, want mystack", rec.name)
	}
	if rec.policy != "always" {
		t.Errorf("pull policy = %q, want always", rec.policy)
	}
	services := rec.deployed["services"].(map[string]any)
	if got := replicasOf(services, "web"); got != 7 {
		t.Errorf("deployed web replicas = %d, want 7 (HPA preserved)", got)
	}
	if got := replicasOf(services, "db"); got != 1 {
		t.Errorf("deployed db replicas = %d, want 1 (compose-owned)", got)
	}
}

func TestDeploy_DefaultPullPolicy(t *testing.T) {
	rec := &recorder{}
	dep := New(fakeState{svcs: []model.StackService{}}, rec.fn(), discardLog())
	if err := dep.Deploy(context.Background(), "s", map[string]any{"services": map[string]any{"db": replicated("pg", 1, nil)}}, port.DeployOpts{}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if rec.policy != "changed" {
		t.Errorf("default pull policy = %q, want changed", rec.policy)
	}
}

func TestDeploy_StateErrorPropagates(t *testing.T) {
	dep := New(fakeState{err: errNoServices}, func(context.Context, string, string, string) error { t.Fatal("deploy should not run"); return nil }, discardLog())
	err := dep.Deploy(context.Background(), "s", map[string]any{"services": map[string]any{}}, port.DeployOpts{})
	if err == nil {
		t.Fatal("want error from state reader")
	}
}
