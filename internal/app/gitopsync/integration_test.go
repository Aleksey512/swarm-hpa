//go:build integration

package gitopsync

import (
	"context"
	"crypto/md5" //nolint:gosec // G501: content hash for test assertion, not a security primitive
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/goccy/go-yaml"

	gitadapter "github.com/Aleksey512/swarm-hpa/internal/adapter/git"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/stackdeploy"
	"github.com/Aleksey512/swarm-hpa/internal/adapter/stackrender"
	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// This file is compiled only under `-tags integration`. It wires the REAL git,
// renderer, and deployer (only the Swarm read and the `docker stack deploy` call
// are faked) to prove an end-to-end sync carries autoscaled replicas forward
// instead of clobbering them, and that dry-run never deploys. The package's
// TestMain (main_test.go) runs it under goleak.

// seedGitRepo creates a local repository at a temp dir (used as the clone URL)
// on branch, with one commit adding compose.yaml.
func seedGitRepo(t *testing.T, branch, composeContent string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(branch))); err != nil {
		t.Fatalf("set HEAD: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(composeContent), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add("compose.yaml"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("init", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Now()}}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return dir
}

// liveState simulates Swarm's view of the stack: the autoscaled "web" service was
// scaled to 7 by the HPA (compose says 3); "db" is plain. carry-forward reads this
// to preserve HPA's decision.
type liveState struct{}

func (liveState) StackServices(_ context.Context, _ string) ([]model.StackService, error) {
	return []model.StackService{
		{Name: "web", Replicas: 7, Replicated: true, Labels: map[string]string{}},
		{Name: "db", Replicas: 1, Replicated: true, Labels: map[string]string{}},
	}, nil
}

// captureDeploy records the (carry-forward-adjusted) compose map the deploy would
// apply, instead of hitting a real Swarm.
type captureDeploy struct{ ch chan map[string]any }

func (c *captureDeploy) fn() stackdeploy.DeployFunc {
	return func(_ context.Context, _ string, composeFile, _ string) error {
		b, err := os.ReadFile(composeFile)
		if err != nil {
			return err
		}
		var m map[string]any
		if err := yaml.Unmarshal(b, &m); err != nil {
			return err
		}
		c.ch <- m
		return nil
	}
}

const integrationCompose = `services:
  web:
    image: nginx
    deploy:
      replicas: 3
      labels:
        swarm.autoscaler.enabled: "true"
        swarm.autoscaler.min: "2"
        swarm.autoscaler.max: "10"
  db:
    image: postgres
    deploy:
      replicas: 1
  agent:
    image: agent
    deploy:
      mode: global
`

func replicasInt64(services map[string]any, name string) int64 {
	d := services[name].(map[string]any)["deploy"].(map[string]any)
	switch v := d["replicas"].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case uint64:
		return int64(v)
	case float64:
		return int64(v)
	}
	return -1
}

// TestGitOpsLoop_CarryForwardEndToEnd proves a sync preserves the HPA's replica
// count (web 7) instead of the compose value (3), leaves compose-owned services
// (db 1) to Git, and skips global services.
func TestGitOpsLoop_CarryForwardEndToEnd(t *testing.T) {
	remote := seedGitRepo(t, "main", integrationCompose)
	src := gitadapter.New(t.TempDir(), map[string]model.RepoConfig{"r": {URL: remote}}, testLogger())
	renderer := stackrender.New(testLogger())
	cap := &captureDeploy{ch: make(chan map[string]any, 1)}
	deployer := stackdeploy.New(liveState{}, cap.fn(), testLogger())

	tickSrc, _ := manualTicks()
	loop := New(src, renderer, deployer, nil, &fakeRec{}, stacks("mystack", "compose.yaml"), "changed", false, false, 1, testLogger(), WithTickSource(tickSrc))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = loop.Run(ctx, time.Hour); close(done) }()

	select {
	case got := <-cap.ch:
		services := got["services"].(map[string]any)
		if r := replicasInt64(services, "web"); r != 7 {
			t.Fatalf("web replicas = %d, want 7 (HPA preserved, not compose 3)", r)
		}
		if r := replicasInt64(services, "db"); r != 1 {
			t.Fatalf("db replicas = %d, want 1 (compose-owned)", r)
		}
		agent := services["agent"].(map[string]any)["deploy"].(map[string]any)
		if _, has := agent["replicas"]; has {
			t.Fatalf("global agent must keep no replicas, got %v", agent["replicas"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: deploy never fired")
	}

	cancel()
	<-done
}

// TestGitOpsLoop_DryRunDoesNotDeploy proves the dry-run gate stops short of the
// deploy seam while still recording the suppressed intent.
func TestGitOpsLoop_DryRunDoesNotDeploy(t *testing.T) {
	remote := seedGitRepo(t, "main", integrationCompose)
	src := gitadapter.New(t.TempDir(), map[string]model.RepoConfig{"r": {URL: remote}}, testLogger())
	renderer := stackrender.New(testLogger())
	cap := &captureDeploy{ch: make(chan map[string]any, 1)}
	deployer := stackdeploy.New(liveState{}, cap.fn(), testLogger())
	rec := &fakeRec{}

	tickSrc, tick := manualTicks()
	loop := New(src, renderer, deployer, nil, rec, stacks("mystack", "compose.yaml"), "changed", true, false, 1, testLogger(), WithTickSource(tickSrc))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = loop.Run(ctx, time.Hour); close(done) }()
	tick <- time.Now() // drive at least one pass

	select {
	case <-cap.ch:
		t.Fatal("dry-run must not invoke the deploy seam")
	case <-time.After(150 * time.Millisecond):
	}
	rec.mu.Lock()
	suppressed := rec.suppressed
	rec.mu.Unlock()
	if suppressed == 0 {
		t.Fatal("expected SyncSuppressed recorded in dry-run")
	}

	cancel()
	<-done
}

// --- M3: sops decrypt + rotation end-to-end ---

// seedGitRepoFiles seeds a local repo with multiple files (compose + content files).
func seedGitRepoFiles(t *testing.T, branch string, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(branch))); err != nil {
		t.Fatalf("set HEAD: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	for name, content := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if _, err := wt.Add(name); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}
	if _, err := wt.Commit("init", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Now()}}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return dir
}

// worktreeSops is a fake SecretDecrypter that overwrites each file with fixed
// plaintext (stands in for real sops decrypt in the integration test).
type worktreeSops struct{ plaintext map[string][]byte }

func (w *worktreeSops) Decrypt(_ context.Context, worktree string, files []string) error {
	for _, f := range files {
		content, ok := w.plaintext[f]
		if !ok {
			return fmt.Errorf("worktreeSops: no plaintext for %q", f)
		}
		if err := os.WriteFile(filepath.Join(worktree, f), content, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func md5hash8(b []byte) string { return fmt.Sprintf("%x", md5.Sum(b))[:8] } //nolint:gosec // G401: test assertion hash

// TestGitOpsLoop_SopsDecryptRotateCarryForward proves the full M3 pipeline: git
// sync -> render -> sops decrypt (in place) -> rotate configs/secrets by content
// hash -> carry-forward preserves the autoscaler's replicas -> deploy.
func TestGitOpsLoop_SopsDecryptRotateCarryForward(t *testing.T) {
	compose := `services:
  web:
    image: nginx
    deploy:
      replicas: 3
      labels:
        swarm.autoscaler.enabled: "true"
        swarm.autoscaler.min: "2"
        swarm.autoscaler.max: "10"
configs:
  app:
    file: cfg/app.conf
secrets:
  tls:
    file: secrets/tls.crt
`
	remote := seedGitRepoFiles(t, "main", map[string][]byte{
		"compose.yaml":    []byte(compose),
		"cfg/app.conf":    []byte("config-v1"),
		"secrets/tls.crt": []byte("ENCRYPTED-BLOB"),
	})
	src := gitadapter.New(t.TempDir(), map[string]model.RepoConfig{"r": {URL: remote}}, testLogger())
	cap := &captureDeploy{ch: make(chan map[string]any, 1)}
	deployer := stackdeploy.New(liveState{}, cap.fn(), testLogger())
	sops := &worktreeSops{plaintext: map[string][]byte{"secrets/tls.crt": []byte("tls-plaintext")}}
	st := []model.StackConfig{{Name: "mystack", Repo: "r", Branch: "main", ComposeFile: "compose.yaml", SopsSecretsDiscovery: true}}

	tickSrc, _ := manualTicks()
	loop := New(src, stackrender.New(testLogger()), deployer, sops, &fakeRec{}, st, "changed", false, true, 1, testLogger(), WithTickSource(tickSrc))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = loop.Run(ctx, time.Hour); close(done) }()

	var got map[string]any
	select {
	case got = <-cap.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: deploy never fired")
	}
	cancel()
	<-done

	services := got["services"].(map[string]any)
	if r := replicasInt64(services, "web"); r != 7 {
		t.Errorf("web replicas = %d, want 7 (carry-forward)", r)
	}
	cfgObj := got["configs"].(map[string]any)["app"].(map[string]any)
	if cfgObj["name"] != "mystack-app-"+md5hash8([]byte("config-v1")) {
		t.Errorf("config name = %v, want mystack-app-%s", cfgObj["name"], md5hash8([]byte("config-v1")))
	}
	secObj := got["secrets"].(map[string]any)["tls"].(map[string]any)
	if secObj["name"] != "mystack-tls-"+md5hash8([]byte("tls-plaintext")) {
		t.Errorf("secret name = %v, want mystack-tls-%s (decrypted content hashed)", secObj["name"], md5hash8([]byte("tls-plaintext")))
	}
}

// TestGitOpsLoop_AutoRotateOff proves rotation is skipped when autoRotate=false.
func TestGitOpsLoop_AutoRotateOff(t *testing.T) {
	compose := `services:
  web:
    image: nginx
    deploy:
      replicas: 3
configs:
  app:
    file: cfg/app.conf
`
	remote := seedGitRepoFiles(t, "main", map[string][]byte{
		"compose.yaml": []byte(compose),
		"cfg/app.conf": []byte("config-v1"),
	})
	src := gitadapter.New(t.TempDir(), map[string]model.RepoConfig{"r": {URL: remote}}, testLogger())
	cap := &captureDeploy{ch: make(chan map[string]any, 1)}
	deployer := stackdeploy.New(liveState{}, cap.fn(), testLogger())
	st := []model.StackConfig{{Name: "mystack", Repo: "r", Branch: "main", ComposeFile: "compose.yaml"}}

	tickSrc, _ := manualTicks()
	loop := New(src, stackrender.New(testLogger()), deployer, nil, &fakeRec{}, st, "changed", false, false, 1, testLogger(), WithTickSource(tickSrc))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = loop.Run(ctx, time.Hour); close(done) }()

	var got map[string]any
	select {
	case got = <-cap.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: deploy never fired")
	}
	cancel()
	<-done

	cfgObj := got["configs"].(map[string]any)["app"].(map[string]any)
	if _, has := cfgObj["name"]; has {
		t.Errorf("autoRotate=false must not rename; got name=%v", cfgObj["name"])
	}
}
