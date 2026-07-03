//go:build integration

package gitopsync

import (
	"context"
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
	loop := New(src, renderer, deployer, &fakeRec{}, stacks("mystack", "compose.yaml"), "changed", false, testLogger(), WithTickSource(tickSrc))

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
	loop := New(src, renderer, deployer, rec, stacks("mystack", "compose.yaml"), "changed", true, testLogger(), WithTickSource(tickSrc))

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
