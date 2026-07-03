package gitopsync

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func stacks(name, compose string) []model.StackConfig {
	return []model.StackConfig{{Name: name, Repo: "r", Branch: "main", ComposeFile: compose}}
}

// --- fakes ---

type fakeGit struct {
	revs  []string // returned in order; last is reused
	files map[string][]byte
	err   error // if set, Sync fails
	n     int
}

func (f *fakeGit) Sync(_ context.Context, _ model.StackConfig) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	rev := f.revs[len(f.revs)-1]
	if f.n < len(f.revs) {
		rev = f.revs[f.n]
	}
	f.n++
	return rev, nil
}

func (f *fakeGit) ReadFile(_ context.Context, _ model.StackConfig, p string) ([]byte, error) {
	return f.files[p], nil
}

func (f *fakeGit) WorktreePath(_ model.StackConfig) string { return "/tmp/fake-worktree" }

type fakeRenderer struct{}

func (fakeRenderer) Render(_, _ []byte) (map[string]any, error) {
	return map[string]any{"services": map[string]any{}}, nil
}

type fakeDeployer struct {
	mu    sync.Mutex
	calls int
	errs  []error // per-call error (index by call count)
	ch    chan string
}

func newFakeDeployer(errs []error) *fakeDeployer {
	return &fakeDeployer{errs: errs, ch: make(chan string, 8)}
}

func (f *fakeDeployer) Deploy(_ context.Context, name string, _ map[string]any, _ port.DeployOpts) error {
	f.mu.Lock()
	idx := f.calls
	f.calls++
	errs := f.errs
	f.mu.Unlock()
	var err error
	if idx < len(errs) {
		err = errs[idx]
	}
	f.ch <- name
	return err
}

func (f *fakeDeployer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeSops records the file lists it was asked to decrypt.
type fakeSops struct {
	mu    sync.Mutex
	calls [][]string
}

func (f *fakeSops) Decrypt(_ context.Context, _ string, files []string) error {
	f.mu.Lock()
	f.calls = append(f.calls, files)
	f.mu.Unlock()
	return nil
}

func (f *fakeSops) totalFiles() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += len(c)
	}
	return n
}

type fakeRec struct {
	port.NopRecorder
	mu         sync.Mutex
	suppressed int
	deployed   []string
	syncErrs   []string
}

func (f *fakeRec) SyncSuppressed(string) {
	f.mu.Lock()
	f.suppressed++
	f.mu.Unlock()
}

func (f *fakeRec) DeployApplied(s string) {
	f.mu.Lock()
	f.deployed = append(f.deployed, s)
	f.mu.Unlock()
}

func (f *fakeRec) SyncError(s string) {
	f.mu.Lock()
	f.syncErrs = append(f.syncErrs, s)
	f.mu.Unlock()
}

// manualTicks returns a TickSource and a channel a test fires to advance the loop.
func manualTicks() (TickSource, chan<- time.Time) {
	ch := make(chan time.Time, 8)
	src := func(time.Duration) (<-chan time.Time, func()) { return ch, func() {} }
	return src, ch
}

// --- tests ---

func TestLoop_NewRevisionDeploysThenSkips(t *testing.T) {
	git := &fakeGit{revs: []string{"aaa", "bbb"}, files: map[string][]byte{"compose.yaml": []byte("services:\n")}}
	dep := newFakeDeployer(nil)
	src, tick := manualTicks()
	l := New(git, fakeRenderer{}, dep, nil, &fakeRec{}, stacks("s", "compose.yaml"), "changed", false, false, testLogger(), WithTickSource(src))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx, time.Hour); close(done) }()

	<-dep.ch // immediate syncAll: rev aaa → deploy #1
	tick <- time.Now()
	<-dep.ch           // rev bbb → deploy #2
	tick <- time.Now() // rev bbb again → unchanged → skip

	select {
	case <-dep.ch:
		t.Fatal("expected no third deploy on unchanged revision")
	case <-time.After(80 * time.Millisecond):
	}
	if c := dep.callCount(); c != 2 {
		t.Fatalf("deploy call count = %d, want 2", c)
	}

	cancel()
	<-done
}

func TestLoop_DryRunSkipsPrepareAndDeploy(t *testing.T) {
	git := &fakeGit{revs: []string{"aaa"}, files: map[string][]byte{"compose.yaml": []byte("services:\n")}}
	dep := newFakeDeployer(nil)
	sops := &fakeSops{}
	rec := &fakeRec{}
	src, tick := manualTicks()
	// Stack with sops files + autoRotate on — dry-run must skip decrypt/rotate/deploy.
	st := []model.StackConfig{{Name: "s", Repo: "r", Branch: "main", ComposeFile: "compose.yaml", SopsFiles: []string{"secrets/tls.crt"}}}
	l := New(git, fakeRenderer{}, dep, sops, rec, st, "changed", true, true, testLogger(), WithTickSource(src))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx, time.Hour); close(done) }()
	tick <- time.Now() // let a pass run

	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done

	if c := dep.callCount(); c != 0 {
		t.Fatalf("dry-run must not deploy; call count = %d", c)
	}
	if sops.totalFiles() != 0 {
		t.Fatalf("dry-run must not decrypt; decrypted %d files", sops.totalFiles())
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.suppressed == 0 {
		t.Fatal("expected SyncSuppressed recorded in dry-run")
	}
}

func TestLoop_SopsDecryptTriggered(t *testing.T) {
	git := &fakeGit{revs: []string{"aaa"}, files: map[string][]byte{"compose.yaml": []byte("services:\n")}}
	dep := newFakeDeployer(nil)
	sops := &fakeSops{}
	st := []model.StackConfig{{Name: "s", Repo: "r", Branch: "main", ComposeFile: "compose.yaml", SopsFiles: []string{"secrets/a.yaml", "secrets/b.yaml"}}}
	src, _ := manualTicks()
	l := New(git, fakeRenderer{}, dep, sops, &fakeRec{}, st, "changed", false, false, testLogger(), WithTickSource(src))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx, time.Hour); close(done) }()

	<-dep.ch // deploy runs after decrypt
	cancel()
	<-done

	if got, want := sops.totalFiles(), 2; got != want {
		t.Fatalf("sops decrypted %d files, want %d", got, want)
	}
}

func TestLoop_DeployErrorRetriesAtSameRevision(t *testing.T) {
	git := &fakeGit{revs: []string{"aaa"}, files: map[string][]byte{"compose.yaml": []byte("services:\n")}}
	dep := newFakeDeployer([]error{errors.New("transient deploy failure"), nil}) // fail then succeed
	rec := &fakeRec{}
	src, tick := manualTicks()
	l := New(git, fakeRenderer{}, dep, nil, rec, stacks("s", "compose.yaml"), "changed", false, false, testLogger(), WithTickSource(src))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx, time.Hour); close(done) }()

	<-dep.ch // rev aaa → deploy fails (ok=false)
	tick <- time.Now()
	<-dep.ch // same rev, but last failed → retry → succeed

	if c := dep.callCount(); c != 2 {
		t.Fatalf("expected failed deploy retried, call count = %d, want 2", c)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.syncErrs) == 0 || rec.syncErrs[0] != "deploy" {
		t.Fatalf("expected a 'deploy' SyncError, got %v", rec.syncErrs)
	}
	cancel()
	<-done
}

func TestLoop_GitSyncErrorStopsBeforeRender(t *testing.T) {
	git := &fakeGit{err: errors.New("boom"), files: map[string][]byte{}}
	dep := newFakeDeployer(nil)
	rec := &fakeRec{}
	src, _ := manualTicks()
	l := New(git, fakeRenderer{}, dep, nil, rec, stacks("s", "compose.yaml"), "changed", false, false, testLogger(), WithTickSource(src))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx, time.Hour); close(done) }()
	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done

	if c := dep.callCount(); c != 0 {
		t.Fatalf("git error must not reach deploy; call count = %d", c)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	found := false
	for _, s := range rec.syncErrs {
		if s == "git" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a 'git' SyncError, got %v", rec.syncErrs)
	}
}

func TestLoop_CancelStops(t *testing.T) {
	src, _ := manualTicks()
	l := New(&fakeGit{revs: []string{"aaa"}, files: map[string][]byte{"compose.yaml": []byte("services:\n")}},
		fakeRenderer{}, newFakeDeployer(nil), nil, &fakeRec{}, stacks("s", "compose.yaml"), "changed", false, false, testLogger(), WithTickSource(src))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx, time.Hour); close(done) }()

	cancel()
	select {
	case <-done:
		// good
	case <-time.After(time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}
