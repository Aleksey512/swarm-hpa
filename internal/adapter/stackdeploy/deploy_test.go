package stackdeploy

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
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

// composeDoc wraps a services map into a compose document.
func composeDoc(services map[string]any) map[string]any {
	return map[string]any{"services": services}
}

// servicesOf extracts the services map of a compose document.
func servicesOf(doc map[string]any) map[string]any {
	return doc["services"].(map[string]any)
}

// TestApplyCarryForwardGroup covers the cases a per-document carry-forward would
// get wrong. docker/cli merges a group's -c documents last-wins, so detection has
// to read the MERGED labels/mode/bounds, and the replica rewrite has to land in
// every document declaring the service — otherwise the losing document's compose
// value could resurrect and clobber the count the autoscaler just set.
func TestApplyCarryForwardGroup(t *testing.T) {
	cases := []struct {
		name string
		docs []map[string]any
		live []model.StackService
		// wantRepl is document index → service → expected replicas after the pass.
		wantRepl map[int]map[string]int64
		wantChg  int
	}{
		{
			name: "labels on the base, service re-declared in the override: both rewritten",
			docs: []map[string]any{
				composeDoc(map[string]any{"web": replicated("nginx", 3, autoscaleLabels("2", "10"))}),
				composeDoc(map[string]any{"web": replicated("nginx:dev", 5, nil)}),
			},
			live:     []model.StackService{{Name: "web", Replicas: 7, Replicated: true}},
			wantRepl: map[int]map[string]int64{0: {"web": 7}, 1: {"web": 7}},
			wantChg:  1,
		},
		{
			name: "labels only on the override: still detected (per-document would miss it)",
			docs: []map[string]any{
				composeDoc(map[string]any{"web": replicated("nginx", 3, nil)}),
				composeDoc(map[string]any{"web": map[string]any{
					"deploy": map[string]any{"labels": autoscaleLabels("2", "10")},
				}}),
			},
			live:     []model.StackService{{Name: "web", Replicas: 7, Replicated: true}},
			wantRepl: map[int]map[string]int64{0: {"web": 7}, 1: {"web": 7}},
			wantChg:  1,
		},
		{
			name: "override flips the service to mode: global: skipped",
			docs: []map[string]any{
				composeDoc(map[string]any{"web": replicated("nginx", 3, autoscaleLabels("2", "10"))}),
				composeDoc(map[string]any{"web": map[string]any{
					"deploy": map[string]any{"mode": "global"},
				}}),
			},
			live:     []model.StackService{{Name: "web", Replicas: 7, Replicated: true}},
			wantRepl: map[int]map[string]int64{0: {"web": 3}},
			wantChg:  0,
		},
		{
			name: "override tightens max: the merged bound clamps",
			docs: []map[string]any{
				composeDoc(map[string]any{"web": replicated("nginx", 3, autoscaleLabels("2", "10"))}),
				composeDoc(map[string]any{"web": map[string]any{
					"deploy": map[string]any{"labels": map[string]any{config.LabelMax: "5"}},
				}}),
			},
			live:     []model.StackService{{Name: "web", Replicas: 20, Replicated: true}},
			wantRepl: map[int]map[string]int64{0: {"web": 5}, 1: {"web": 5}},
			wantChg:  1,
		},
		{
			name: "override raises min: the merged bound clamps",
			docs: []map[string]any{
				composeDoc(map[string]any{"web": replicated("nginx", 3, autoscaleLabels("2", "10"))}),
				composeDoc(map[string]any{"web": map[string]any{
					"deploy": map[string]any{"labels": map[string]any{config.LabelMin: "6"}},
				}}),
			},
			live:     []model.StackService{{Name: "web", Replicas: 1, Replicated: true}},
			wantRepl: map[int]map[string]int64{0: {"web": 6}, 1: {"web": 6}},
			wantChg:  1,
		},
		{
			name: "override opting the service OUT is respected (Git wins on the next sync)",
			docs: []map[string]any{
				composeDoc(map[string]any{"web": replicated("nginx", 3, autoscaleLabels("2", "10"))}),
				composeDoc(map[string]any{"web": map[string]any{
					"deploy": map[string]any{"labels": map[string]any{config.LabelEnabled: "false"}},
				}}),
			},
			live:     []model.StackService{{Name: "web", Replicas: 7, Replicated: true}},
			wantRepl: map[int]map[string]int64{0: {"web": 3}},
			wantChg:  0,
		},
		{
			name: "an override without a services map is tolerated",
			docs: []map[string]any{
				composeDoc(map[string]any{"web": replicated("nginx", 3, autoscaleLabels("2", "10"))}),
				{"secrets": map[string]any{"tls": map[string]any{"file": "tls.crt"}}},
			},
			live:     []model.StackService{{Name: "web", Replicas: 7, Replicated: true}},
			wantRepl: map[int]map[string]int64{0: {"web": 7}},
			wantChg:  1,
		},
		{
			name: "the count is distinct services, not rewrites",
			docs: []map[string]any{
				composeDoc(map[string]any{
					"web": replicated("nginx", 3, autoscaleLabels("2", "10")),
					"api": replicated("api", 1, autoscaleLabels("1", "4")),
				}),
				composeDoc(map[string]any{"web": replicated("nginx:dev", 5, nil)}),
			},
			live: []model.StackService{
				{Name: "web", Replicas: 7, Replicated: true},
				{Name: "api", Replicas: 3, Replicated: true},
			},
			wantRepl: map[int]map[string]int64{0: {"web": 7, "api": 3}, 1: {"web": 7}},
			wantChg:  2, // two services, three document rewrites
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chg, err := ApplyCarryForwardGroup(tc.docs, tc.live, discardLog())
			if err != nil {
				t.Fatalf("ApplyCarryForwardGroup: %v", err)
			}
			if chg != tc.wantChg {
				t.Errorf("changed = %d, want %d", chg, tc.wantChg)
			}
			for docIdx, want := range tc.wantRepl {
				services := servicesOf(tc.docs[docIdx])
				for name, wantRepl := range want {
					if got := replicasOf(services, name); got != wantRepl {
						t.Errorf("doc[%d] replicas[%s] = %d, want %d", docIdx, name, got, wantRepl)
					}
				}
			}
		})
	}
}

// When the same swarm.autoscaler.* key is declared in BOTH deploy.labels and the
// service's top-level labels, deploy.labels must win: those are the labels Swarm
// stores as SERVICE labels, i.e. the ones the daemon reads at runtime. A
// top-level labels: key is only a container label. gitopsync's drift snapshot
// resolves the conflict identically, so carry-forward and drift can never
// disagree about who owns a service's replicas.
func TestApplyCarryForward_DeployLabelsBeatTopLevelLabels(t *testing.T) {
	cases := []struct {
		name        string
		deployValue string
		topValue    string
		wantRepl    int64 // 7 = treated as autoscaled (live wins), 3 = compose-owned
	}{
		{name: "deploy enables, top-level disables", deployValue: "true", topValue: "false", wantRepl: 7},
		{name: "deploy disables, top-level enables", deployValue: "false", topValue: "true", wantRepl: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			services := map[string]any{"web": map[string]any{
				"image":  "nginx",
				"labels": map[string]any{config.LabelEnabled: tc.topValue},
				"deploy": map[string]any{
					"replicas": 3,
					"labels": map[string]any{
						config.LabelEnabled: tc.deployValue,
						config.LabelMin:     "2",
						config.LabelMax:     "10",
					},
				},
			}}
			live := []model.StackService{{Name: "web", Replicas: 7, Replicated: true}}
			if _, err := ApplyCarryForward(composeDoc(services), live, discardLog()); err != nil {
				t.Fatalf("ApplyCarryForward: %v", err)
			}
			if got := replicasOf(services, "web"); got != tc.wantRepl {
				t.Errorf("replicas = %d, want %d (deploy.labels must win over top-level labels)", got, tc.wantRepl)
			}
		})
	}
}

func TestApplyCarryForwardGroup_NoDocHasServices(t *testing.T) {
	docs := []map[string]any{
		{"version": "3"},
		{"secrets": map[string]any{"tls": map[string]any{"file": "tls.crt"}}},
	}
	if _, err := ApplyCarryForwardGroup(docs, nil, discardLog()); err != errNoServices {
		t.Fatalf("want errNoServices, got %v", err)
	}
}

// An empty `services: {}` is a valid (if pointless) compose document and must not
// be reported as "no services map" — that was the pre-group behavior.
func TestApplyCarryForwardGroup_EmptyServicesMapIsNotAnError(t *testing.T) {
	chg, err := ApplyCarryForwardGroup([]map[string]any{composeDoc(map[string]any{})}, nil, discardLog())
	if err != nil {
		t.Fatalf("ApplyCarryForwardGroup: %v", err)
	}
	if chg != 0 {
		t.Errorf("changed = %d, want 0", chg)
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
	called bool
	name   string
	policy string
	// composeFiles are the temp compose paths handed to the deploy seam, in -c
	// order; deployed holds their parsed contents in the same order.
	composeFiles []string
	deployed     []map[string]any
	err          error // returned by the seam, to exercise failure paths
}

func (r *recorder) fn() DeployFunc {
	return func(_ context.Context, name string, composeFiles []string, pullPolicy string) error {
		r.called = true
		r.name = name
		r.policy = pullPolicy
		r.composeFiles = append([]string(nil), composeFiles...)
		r.deployed = nil
		for _, f := range composeFiles {
			b, err := os.ReadFile(f) //nolint:gosec // G304: f is a temp path the deployer created
			if err != nil {
				return err
			}
			var m map[string]any
			if err := yaml.Unmarshal(b, &m); err != nil {
				return err
			}
			r.deployed = append(r.deployed, m)
		}
		return r.err
	}
}

// composeFile is the single temp path of a one-document deploy.
func (r *recorder) composeFile(t *testing.T) string {
	t.Helper()
	if len(r.composeFiles) != 1 {
		t.Fatalf("want exactly 1 compose file, got %d (%v)", len(r.composeFiles), r.composeFiles)
	}
	return r.composeFiles[0]
}

// baseDoc is the parsed first (base) document of the deployed group.
func (r *recorder) baseDoc(t *testing.T) map[string]any {
	t.Helper()
	if len(r.deployed) == 0 {
		t.Fatal("no compose documents were deployed")
	}
	return r.deployed[0]
}

// oneDoc wraps a single compose map as a one-document merge group with no
// directory preference (the OS temp dir fallback).
func oneDoc(compose map[string]any) []port.ComposeDoc {
	return []port.ComposeDoc{{Map: compose}}
}

func TestDeploy_CarryForwardThenDeploy(t *testing.T) {
	compose := map[string]any{"services": map[string]any{
		"web": replicated("nginx", 3, autoscaleLabels("2", "10")),
		"db":  replicated("pg", 1, nil),
	}}
	state := fakeState{svcs: []model.StackService{{Name: "web", Replicas: 7, Replicated: true}}}
	rec := &recorder{}
	dep := New(state, rec.fn(), discardLog())

	if err := dep.Deploy(context.Background(), "mystack", oneDoc(compose), port.DeployOpts{PullPolicy: "always"}); err != nil {
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
	services := servicesOf(rec.baseDoc(t))
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
	if err := dep.Deploy(context.Background(), "s", oneDoc(composeDoc(map[string]any{"db": replicated("pg", 1, nil)})), port.DeployOpts{}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if rec.policy != "changed" {
		t.Errorf("default pull policy = %q, want changed", rec.policy)
	}
}

func TestDeploy_StateErrorPropagates(t *testing.T) {
	dep := New(fakeState{err: errNoServices}, func(context.Context, string, []string, string) error {
		t.Fatal("deploy should not run")
		return nil
	}, discardLog())
	err := dep.Deploy(context.Background(), "s", oneDoc(composeDoc(map[string]any{})), port.DeployOpts{})
	if err == nil {
		t.Fatal("want error from state reader")
	}
}

func TestDeploy_NoDocumentsIsAnError(t *testing.T) {
	dep := New(fakeState{}, func(context.Context, string, []string, string) error {
		t.Fatal("deploy should not run")
		return nil
	}, discardLog())
	if err := dep.Deploy(context.Background(), "s", nil, port.DeployOpts{}); err == nil {
		t.Fatal("want error for an empty merge group")
	}
}

// TestDeploy_TempComposeCoLocatedWithSource is the regression test for the
// "open /tmp/configs/nginx.conf: no such file or directory" bug: when a document
// carries a Dir, its temp compose MUST be written in that directory so the
// relative configs:/secrets: file paths inside it resolve against the source
// compose's directory (the worktree), not the OS temp dir.
func TestDeploy_TempComposeCoLocatedWithSource(t *testing.T) {
	dir := t.TempDir() // stands in for the source compose file's directory
	rec := &recorder{}
	dep := New(fakeState{svcs: []model.StackService{}}, rec.fn(), discardLog())
	docs := []port.ComposeDoc{{Map: composeDoc(map[string]any{"db": replicated("pg", 1, nil)}), Dir: dir}}
	if err := dep.Deploy(context.Background(), "mystack", docs, port.DeployOpts{}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if !rec.called {
		t.Fatal("deploy seam not invoked")
	}
	tmp := rec.composeFile(t)
	if got := filepath.Dir(tmp); got != dir {
		t.Errorf("temp compose dir = %q, want %q (relative configs:/secrets: paths must resolve against the source dir)", got, dir)
	}
	// The temp file is removed after the deploy returns; the co-located dir is left intact.
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("temp compose %q not removed after deploy", tmp)
	}
}

// TestDeploy_TempComposeFallsBackToOSTemp verifies the historical behavior is
// preserved when a document carries no Dir (tests / callers that don't care about
// relative paths): the temp compose goes to the OS temp dir, not the worktree.
func TestDeploy_TempComposeFallsBackToOSTemp(t *testing.T) {
	rec := &recorder{}
	dep := New(fakeState{svcs: []model.StackService{}}, rec.fn(), discardLog())
	if err := dep.Deploy(context.Background(), "mystack",
		oneDoc(composeDoc(map[string]any{"db": replicated("pg", 1, nil)})), port.DeployOpts{}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	tmp := rec.composeFile(t)
	if got := filepath.Dir(tmp); got != filepath.Clean(os.TempDir()) {
		t.Errorf("temp compose dir = %q, want %q (OS temp dir fallback when Dir unset)", got, filepath.Clean(os.TempDir()))
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("temp compose %q not removed after deploy", tmp)
	}
}

// TestDeploy_MergeGroupWritesOneTempFilePerDocument is the core multi-`-c` test:
// a base plus two overrides must reach the deploy seam as three temp files, in
// declaration order (that order is what decides docker/cli's merge precedence),
// each written next to its OWN source directory — overrides may live elsewhere
// than the base.
func TestDeploy_MergeGroupWritesOneTempFilePerDocument(t *testing.T) {
	baseDir, ovrDir := t.TempDir(), t.TempDir()
	rec := &recorder{}
	dep := New(fakeState{svcs: []model.StackService{}}, rec.fn(), discardLog())

	docs := []port.ComposeDoc{
		{Map: composeDoc(map[string]any{"web": replicated("nginx", 1, nil)}), Dir: baseDir},
		{Map: composeDoc(map[string]any{"web": replicated("nginx:prod", 2, nil)}), Dir: ovrDir},
		{Map: composeDoc(map[string]any{"web": map[string]any{"image": "nginx:final"}}), Dir: ""},
	}
	if err := dep.Deploy(context.Background(), "mystack", docs, port.DeployOpts{PullPolicy: "always"}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(rec.composeFiles) != 3 {
		t.Fatalf("deploy got %d compose files, want 3 (-c per document)", len(rec.composeFiles))
	}
	wantDirs := []string{baseDir, ovrDir, filepath.Clean(os.TempDir())}
	for i, want := range wantDirs {
		if got := filepath.Dir(rec.composeFiles[i]); got != want {
			t.Errorf("compose file %d dir = %q, want %q", i, got, want)
		}
	}
	// Order is load-bearing: assert the CONTENT arrived in declaration order, not
	// just that three files showed up.
	wantImages := []string{"nginx", "nginx:prod", "nginx:final"}
	for i, want := range wantImages {
		got := servicesOf(rec.deployed[i])["web"].(map[string]any)["image"]
		if got != want {
			t.Errorf("document %d image = %v, want %q (-c order not preserved)", i, got, want)
		}
	}
	for _, f := range rec.composeFiles {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("temp compose %q not removed after deploy", f)
		}
	}
}

// A failing deploy must still clean up every temp file it wrote — otherwise a
// repeatedly failing stack litters the repo worktree with temp composes.
func TestDeploy_TempFilesRemovedWhenDeployFails(t *testing.T) {
	baseDir, ovrDir := t.TempDir(), t.TempDir()
	rec := &recorder{err: errors.New("boom")}
	dep := New(fakeState{svcs: []model.StackService{}}, rec.fn(), discardLog())

	docs := []port.ComposeDoc{
		{Map: composeDoc(map[string]any{"web": replicated("nginx", 1, nil)}), Dir: baseDir},
		{Map: composeDoc(map[string]any{"web": replicated("nginx:prod", 2, nil)}), Dir: ovrDir},
	}
	if err := dep.Deploy(context.Background(), "mystack", docs, port.DeployOpts{}); err == nil {
		t.Fatal("want the deploy error to propagate")
	}
	if len(rec.composeFiles) != 2 {
		t.Fatalf("deploy got %d compose files, want 2", len(rec.composeFiles))
	}
	for _, f := range rec.composeFiles {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("temp compose %q leaked after a failed deploy", f)
		}
	}
}

// Carry-forward must be applied across the whole group before the temp files are
// written, so the count that reaches docker/cli is the live one in every document.
func TestDeploy_MergeGroupCarryForwardReachesEveryDocument(t *testing.T) {
	rec := &recorder{}
	state := fakeState{svcs: []model.StackService{{Name: "web", Replicas: 7, Replicated: true}}}
	dep := New(state, rec.fn(), discardLog())

	docs := []port.ComposeDoc{
		{Map: composeDoc(map[string]any{"web": replicated("nginx", 3, autoscaleLabels("2", "10"))})},
		{Map: composeDoc(map[string]any{"web": replicated("nginx:prod", 5, nil)})},
	}
	if err := dep.Deploy(context.Background(), "mystack", docs, port.DeployOpts{}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	for i := range rec.deployed {
		if got := replicasOf(servicesOf(rec.deployed[i]), "web"); got != 7 {
			t.Errorf("deployed document %d web replicas = %d, want 7 (HPA count must survive the merge)", i, got)
		}
	}
}
