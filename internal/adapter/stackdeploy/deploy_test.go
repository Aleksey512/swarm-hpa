package stackdeploy

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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
//
// Note: co-location of the BASE temp file is load-bearing — its directory is
// docker/cli's anchor for resolving relative configs:/secrets: file: paths, and
// every document's file: is rebased relative to it
// (TestDeploy_RebasesConfigSecretFilePathsRelativeToBase, issues #20, #22).
// Co-location of override temp files is belt-and-suspenders for other relative-
// path types (build:, env_file:) that docker stack deploy resolves.
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

// TestRebaseFileObjects covers the pure helper that rewrites each document's
// relative configs:/secrets: file: path to be relative to the BASE (first)
// compose file's directory (baseDir) before the temp compose is handed to
// docker. The base document (docDir == baseDir) is the identity case; override
// documents are rebased so docker/cli's first-`-c` resolution rule finds them.
func TestRebaseFileObjects(t *testing.T) {
	const (
		baseDir = "/work/betapp/base"     // stands in for the base (first) compose dir
		ovrDir  = "/work/betapp/override" // an override living in a different dir
	)
	// wantRel is the expected rebased path: file f in docDir, expressed relative
	// to baseDir. Rel cannot fail for these in-worktree test paths.
	wantRel := func(f string) string {
		rel, err := filepath.Rel(baseDir, filepath.Join(ovrDir, f))
		if err != nil {
			t.Fatalf("wantRel: Rel: %v", err)
		}
		return rel
	}
	cases := []struct {
		name      string
		compose   map[string]any
		docDir    string
		baseDir   string
		wantCount int
		check     func(*testing.T, map[string]any)
	}{
		{
			name: "base document: relative file unchanged (Rel identity)",
			compose: map[string]any{"configs": map[string]any{
				"db-init": map[string]any{"file": "configs/db-init.sql"},
			}},
			docDir: baseDir, baseDir: baseDir, wantCount: 1,
			check: func(t *testing.T, c map[string]any) {
				got := c["configs"].(map[string]any)["db-init"].(map[string]any)["file"]
				if want := "configs/db-init.sql"; got != want {
					t.Errorf("base doc file = %v, want %q (Rel identity leaves it untouched)", got, want)
				}
			},
		},
		{
			name: "override document: config file rebased relative to base dir",
			compose: map[string]any{"configs": map[string]any{
				"postgres-init": map[string]any{"file": "configs/postgres-init.sql"},
			}},
			docDir: ovrDir, baseDir: baseDir, wantCount: 1,
			check: func(t *testing.T, c map[string]any) {
				got := c["configs"].(map[string]any)["postgres-init"].(map[string]any)["file"]
				if want := wantRel("configs/postgres-init.sql"); got != want {
					t.Errorf("override doc file = %v, want %q (relative to base dir)", got, want)
				}
			},
		},
		{
			name: "override document: secret file rebased too",
			compose: map[string]any{"secrets": map[string]any{
				"tls": map[string]any{"file": "secrets/tls.crt"},
			}},
			docDir: ovrDir, baseDir: baseDir, wantCount: 1,
			check: func(t *testing.T, c map[string]any) {
				got := c["secrets"].(map[string]any)["tls"].(map[string]any)["file"]
				if want := wantRel("secrets/tls.crt"); got != want {
					t.Errorf("override doc secret file = %v, want %q (relative to base dir)", got, want)
				}
			},
		},
		{
			name: "already-absolute file left unchanged",
			compose: map[string]any{"configs": map[string]any{
				"x": map[string]any{"file": "/etc/config/x.yml"},
			}},
			docDir: ovrDir, baseDir: baseDir, wantCount: 0,
			check: func(t *testing.T, c map[string]any) {
				got := c["configs"].(map[string]any)["x"].(map[string]any)["file"]
				if got != "/etc/config/x.yml" {
					t.Errorf("file = %v, want unchanged absolute path", got)
				}
			},
		},
		{
			name: "non-file (external) object untouched",
			compose: map[string]any{"secrets": map[string]any{
				"ext": map[string]any{"external": true, "name": "ext-secret"},
			}},
			docDir: ovrDir, baseDir: baseDir, wantCount: 0,
			check: func(t *testing.T, c map[string]any) {
				obj := c["secrets"].(map[string]any)["ext"].(map[string]any)
				if _, has := obj["file"]; has {
					t.Error("non-file object gained a file: key")
				}
			},
		},
		{
			name:    "missing configs/secrets sections is a no-op",
			compose: map[string]any{"services": map[string]any{"web": map[string]any{"image": "nginx"}}},
			docDir:  ovrDir, baseDir: baseDir, wantCount: 0,
			check: func(*testing.T, map[string]any) {},
		},
		{
			name: "empty baseDir (no anchor) leaves relative path as-is",
			compose: map[string]any{"configs": map[string]any{
				"x": map[string]any{"file": "c/x.yml"},
			}},
			docDir: ovrDir, baseDir: "", wantCount: 0,
			check: func(t *testing.T, c map[string]any) {
				got := c["configs"].(map[string]any)["x"].(map[string]any)["file"]
				if got != "c/x.yml" {
					t.Errorf("file = %v, want relative path preserved when baseDir is empty", got)
				}
			},
		},
		{
			name: "empty docDir (OS-temp fallback) leaves relative path as-is",
			compose: map[string]any{"configs": map[string]any{
				"x": map[string]any{"file": "c/x.yml"},
			}},
			docDir: "", baseDir: baseDir, wantCount: 0,
			check: func(t *testing.T, c map[string]any) {
				got := c["configs"].(map[string]any)["x"].(map[string]any)["file"]
				if got != "c/x.yml" {
					t.Errorf("file = %v, want relative path preserved when docDir is empty", got)
				}
			},
		},
		{
			name: "empty file value untouched",
			compose: map[string]any{"configs": map[string]any{
				"x": map[string]any{"file": ""},
			}},
			docDir: ovrDir, baseDir: baseDir, wantCount: 0,
			check: func(t *testing.T, c map[string]any) {
				got := c["configs"].(map[string]any)["x"].(map[string]any)["file"]
				if got != "" {
					t.Errorf("file = %v, want empty preserved", got)
				}
			},
		},
		{
			name: "non-map object value is skipped without panic",
			compose: map[string]any{"configs": map[string]any{
				"scalar": "not-a-map",
			}},
			docDir: ovrDir, baseDir: baseDir, wantCount: 0,
			check: func(*testing.T, map[string]any) {},
		},
		{
			name: "count covers both configs and secrets in one call",
			compose: map[string]any{
				"configs": map[string]any{"a": map[string]any{"file": "a.yml"}},
				"secrets": map[string]any{"b": map[string]any{"file": "b.key"}},
			},
			docDir: ovrDir, baseDir: baseDir, wantCount: 2,
			check: func(*testing.T, map[string]any) {},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rebaseFileObjects(tc.compose, tc.docDir, tc.baseDir, discardLog())
			if got != tc.wantCount {
				t.Errorf("rewritten = %d, want %d", got, tc.wantCount)
			}
			if tc.check != nil {
				tc.check(t, tc.compose)
			}
		})
	}
}

// TestDeploy_RebasesConfigSecretFilePathsRelativeToBase is the regression test
// for issue #20: `docker stack deploy -c base -c override` resolves ALL relative
// configs:/secrets: file: paths against the FIRST -c file's directory, so an
// override whose files live in a different directory than the base could not be
// referenced by a plain relative path (deploy and rotate disagreed). The deployer
// rebases each document's file: relative to the BASE (first) compose file's
// directory in the materialized temp compose, so docker/cli's first-`-c`
// resolution finds it regardless of -c ordering. Asserted via the recorder seam,
// which captures each marshalled temp compose. (Dirs here are absolute — the
// relative-GitOpsReposPath case is covered by
// TestDeploy_RebasesConfigSecretFilePathsWithRelativeReposPath.)
func TestDeploy_RebasesConfigSecretFilePathsRelativeToBase(t *testing.T) {
	baseDir, ovrDir := t.TempDir(), t.TempDir() // distinct source compose dirs
	rec := &recorder{}
	dep := New(fakeState{svcs: []model.StackService{}}, rec.fn(), discardLog())

	docs := []port.ComposeDoc{
		{
			Map: map[string]any{
				// carry-forward needs at least one services map in the group.
				"services": map[string]any{},
				"configs": map[string]any{
					"db-init": map[string]any{"file": "configs/db-init.sql"},
				},
			},
			Dir: baseDir,
		},
		{
			Map: map[string]any{"configs": map[string]any{
				"postgres-init": map[string]any{"file": "configs/postgres-init.sql"},
			}},
			Dir: ovrDir, // different directory than the base — the issue #20 case
		},
		{
			Map: map[string]any{"configs": map[string]any{
				"x": map[string]any{"file": "c/x.yml"},
			}},
			Dir: "", // OS-temp fallback: relative path must be left as-is
		},
	}
	if err := dep.Deploy(context.Background(), "mystack", docs, port.DeployOpts{}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(rec.deployed) != 3 {
		t.Fatalf("deployed %d documents, want 3", len(rec.deployed))
	}
	fileOf := func(doc map[string]any, name string) any {
		return doc["configs"].(map[string]any)[name].(map[string]any)["file"]
	}
	// Base document (docDir == baseDir): Rel identity leaves its relative file: as-is.
	if got, want := fileOf(rec.deployed[0], "db-init"), "configs/db-init.sql"; got != want {
		t.Errorf("base doc db-init.file = %v, want %q (unchanged relative path)", got, want)
	}
	// Override document — the issue #20 core: its relative file: is rebased
	// relative to the BASE dir (so docker/cli resolves it against the base temp
	// file's directory), NOT left relative to the override's own dir.
	wantOvr, err := filepath.Rel(baseDir, filepath.Join(ovrDir, "configs/postgres-init.sql"))
	if err != nil {
		t.Fatalf("expected override rel: %v", err)
	}
	if got := fileOf(rec.deployed[1], "postgres-init"); got != wantOvr {
		t.Errorf("override doc postgres-init.file = %v, want %q (relative to the base dir)", got, wantOvr)
	}
	// Empty-Dir fallback document: relative path preserved (no base to resolve against).
	if got := fileOf(rec.deployed[2], "x"); got != "c/x.yml" {
		t.Errorf("empty-dir doc x.file = %v, want %q (relative preserved when Dir unset)", got, "c/x.yml")
	}
}

// TestDeploy_RebasesConfigSecretFilePathsWithRelativeReposPath is the regression
// test for issue #22: the previous absolutize approach used filepath.Join without
// filepath.Abs, so when GitOpsReposPath is relative (the DEFAULT, "repos") the
// "absolute" path stayed relative and docker/cli resolved it against the first -c
// file's directory, doubling the worktree segment and breaking EVERY stack with
// file-backed configs/secrets. This case uses RELATIVE doc.Dirs (mirroring the
// production default) — the case the absolutize tests hid by using absolute
// t.TempDir() dirs. The rebase cancels the worktree prefix via filepath.Rel, so
// the override file: contains no worktree segment at all and is independent of
// the relative/absolute shape of GitOpsReposPath.
func TestDeploy_RebasesConfigSecretFilePathsWithRelativeReposPath(t *testing.T) {
	// Relative dirs, as the loop builds them under the default GitOpsReposPath
	// ("repos"): doc.Dir = filepath.Join(worktree, filepath.Dir(composeFile)).
	const (
		baseDir = "repos/st/shared"   // docs[0] — the base, docker/cli's anchor
		ovrDir  = "repos/st/override" // docs[1] — an override in a sibling dir
	)
	// Run inside a temp CWD and materialize the relative worktree dirs on disk:
	// writeTempCompose writes each temp compose into doc.Dir, so the relative
	// dirs must exist relative to CWD exactly as they do in production (where the
	// process CWD contains the relative repos/ worktree). No test in this package
	// uses t.Parallel(), so a scoped Chdir is safe.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tmpRoot := t.TempDir()
	if err := os.Chdir(tmpRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	for _, d := range []string{baseDir, ovrDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	rec := &recorder{}
	dep := New(fakeState{svcs: []model.StackService{}}, rec.fn(), discardLog())

	docs := []port.ComposeDoc{
		{
			Map: map[string]any{
				"services": map[string]any{},
				"configs": map[string]any{
					"db-init": map[string]any{"file": "configs/db-init.sql"},
				},
			},
			Dir: baseDir,
		},
		{
			Map: map[string]any{"configs": map[string]any{
				"postgres-init": map[string]any{"file": "configs/postgres-init.sql"},
			}},
			Dir: ovrDir,
		},
	}
	if err := dep.Deploy(context.Background(), "mystack", docs, port.DeployOpts{}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(rec.deployed) != 2 {
		t.Fatalf("deployed %d documents, want 2", len(rec.deployed))
	}
	fileOf := func(doc map[string]any, name string) any {
		return doc["configs"].(map[string]any)[name].(map[string]any)["file"]
	}
	// Base document: relative file: unchanged (Rel identity).
	if got, want := fileOf(rec.deployed[0], "db-init"), "configs/db-init.sql"; got != want {
		t.Errorf("base doc db-init.file = %v, want %q", got, want)
	}
	// Override document: rebased relative to the base dir.
	wantOvr, err := filepath.Rel(baseDir, filepath.Join(ovrDir, "configs/postgres-init.sql"))
	if err != nil {
		t.Fatalf("expected override rel: %v", err)
	}
	got := fileOf(rec.deployed[1], "postgres-init")
	if got != wantOvr {
		t.Errorf("override doc postgres-init.file = %v, want %q (relative to the base dir)", got, wantOvr)
	}
	// Issue #22 regression signature: under the old absolutize-without-Abs bug the
	// path stayed "repos/st/override/configs/postgres-init.sql" (still containing
	// the worktree segment "repos/st"), which docker then doubled. The rebase
	// cancels the worktree prefix, so the rebased path must contain no "repos/st"
	// segment at all.
	if s, ok := got.(string); !ok {
		t.Errorf("override doc postgres-init.file = %T, want string", got)
	} else if strings.Contains(s, "repos/st") {
		t.Errorf("override doc postgres-init.file = %q must not contain the worktree segment \"repos/st\" (the #22 doubling bug)", s)
	}
}
