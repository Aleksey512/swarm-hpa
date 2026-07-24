package gitopsync

import (
	"context"
	"errors"
	"fmt"
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
	return []model.StackConfig{{Name: name, Repo: "r", Branch: "main", ComposeFiles: []model.ComposeFileSpec{{File: compose}}}}
}

// --- fakes ---

type fakeGit struct {
	mu    sync.Mutex // guards n against concurrent Sync calls (parallel stacks)
	revs  []string   // returned in order; last is reused
	files map[string][]byte
	err   error // if set, Sync fails
	n     int
}

func (f *fakeGit) Sync(_ context.Context, _ model.StackConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	mu       sync.Mutex
	calls    int
	errs     []error // per-call error (index by call count)
	ch       chan string
	policies map[string]string // stack name → PullPolicy received in DeployOpts
	// pullPolicies records the PullPolicy of EVERY Deploy call, in order. Needed
	// to assert per-file policy order for multi-file stacks: the name-keyed
	// policies map above only keeps the LAST call for a given stack.
	pullPolicies []string
}

func newFakeDeployer(errs []error) *fakeDeployer {
	return &fakeDeployer{errs: errs, ch: make(chan string, 8), policies: map[string]string{}}
}

func (f *fakeDeployer) Deploy(_ context.Context, name string, _ map[string]any, opts port.DeployOpts) error {
	f.mu.Lock()
	idx := f.calls
	f.calls++
	errs := f.errs
	f.policies[name] = opts.PullPolicy
	f.pullPolicies = append(f.pullPolicies, opts.PullPolicy)
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

// policy returns the PullPolicy the loop passed to Deploy for the named stack.
func (f *fakeDeployer) policy(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.policies[name]
}

// pullPolicySeq returns the ordered PullPolicy values received across all Deploy
// calls (a copy), so callers can assert per-file policy order for multi-file
// stacks.
func (f *fakeDeployer) pullPolicySeq() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.pullPolicies))
	copy(out, f.pullPolicies)
	return out
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

func TestLoop_NewRevisionDeploysNotSkips(t *testing.T) {
	git := &fakeGit{revs: []string{"aaa", "bbb"}, files: map[string][]byte{"compose.yaml": []byte("services:\n")}}
	dep := newFakeDeployer(nil)
	src, tick := manualTicks()
	l := New(git, fakeRenderer{}, dep, nil, &fakeRec{}, nil, stacks("s", "compose.yaml"), "changed", false, false, 1, testLogger(), WithTickSource(src))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx, time.Hour); close(done) }()

	<-dep.ch // immediate syncAll: rev aaa → deploy #1
	tick <- time.Now()
	<-dep.ch // rev bbb → deploy #2
	tick <- time.Now()
	<-dep.ch // rev bbb → deploy #3

	if c := dep.callCount(); c != 3 {
		t.Fatalf("deploy call count = %d, want 3", c)
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
	st := []model.StackConfig{{Name: "s", Repo: "r", Branch: "main", ComposeFiles: []model.ComposeFileSpec{{File: "compose.yaml"}}, SopsFiles: []string{"secrets/tls.crt"}}}
	l := New(git, fakeRenderer{}, dep, sops, rec, nil, st, "changed", true, true, 1, testLogger(), WithTickSource(src))

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
	st := []model.StackConfig{{Name: "s", Repo: "r", Branch: "main", ComposeFiles: []model.ComposeFileSpec{{File: "compose.yaml"}}, SopsFiles: []string{"secrets/a.yaml", "secrets/b.yaml"}}}
	src, _ := manualTicks()
	l := New(git, fakeRenderer{}, dep, sops, &fakeRec{}, nil, st, "changed", false, false, 1, testLogger(), WithTickSource(src))

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
	l := New(git, fakeRenderer{}, dep, nil, rec, nil, stacks("s", "compose.yaml"), "changed", false, false, 1, testLogger(), WithTickSource(src))

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
	l := New(git, fakeRenderer{}, dep, nil, rec, nil, stacks("s", "compose.yaml"), "changed", false, false, 1, testLogger(), WithTickSource(src))

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
		fakeRenderer{}, newFakeDeployer(nil), nil, &fakeRec{}, nil, stacks("s", "compose.yaml"), "changed", false, false, 1, testLogger(), WithTickSource(src))

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

func TestLoop_PerStackPullPolicyOverridesGlobal(t *testing.T) {
	git := &fakeGit{revs: []string{"aaa"}, files: map[string][]byte{"compose.yaml": []byte("services:\n")}}
	dep := newFakeDeployer(nil)
	src, _ := manualTicks()
	// Two stacks on one repo: "override" pins pull_policy=changed; "default" leaves
	// it empty to fall back to the global. The global policy passed to New is "always".
	st := []model.StackConfig{
		{Name: "override", Repo: "r", Branch: "main", ComposeFiles: []model.ComposeFileSpec{{File: "compose.yaml"}}, PullPolicy: "changed"},
		{Name: "default", Repo: "r", Branch: "main", ComposeFiles: []model.ComposeFileSpec{{File: "compose.yaml"}}},
	}
	l := New(git, fakeRenderer{}, dep, nil, &fakeRec{}, nil, st, "always", false, false, 1, testLogger(), WithTickSource(src))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx, time.Hour); close(done) }()

	<-dep.ch // immediate syncAll deploys both stacks (concurrency=1, same repo → serial)
	<-dep.ch
	cancel()
	<-done

	if got, want := dep.policy("override"), "changed"; got != want {
		t.Errorf("override stack PullPolicy = %q, want %q (per-stack must win over global)", got, want)
	}
	if got, want := dep.policy("default"), "always"; got != want {
		t.Errorf("default stack PullPolicy = %q, want %q (global fallback)", got, want)
	}
}

// --- concurrency tests ---

// trackingDeployer instruments the worker pool: it records the peak number of
// simultaneously-active Deploy calls (the concurrency bound), whether two
// stacks on the same repo ever overlapped (they must not), the total attempted
// deploys, and can fail a specific stack by name. block holds each call so
// overlap is observable rather than a sub-microsecond blip.
type trackingDeployer struct {
	mu           sync.Mutex
	inFlight     int
	peak         int
	total        int
	repoOf       func(stack string) string
	repoInflight map[string]int
	overlap      map[string]bool
	block        time.Duration
	failStack    string
}

func (d *trackingDeployer) Deploy(_ context.Context, stack string, _ map[string]any, _ port.DeployOpts) error {
	repo := ""
	if d.repoOf != nil {
		repo = d.repoOf(stack)
	}
	d.mu.Lock()
	d.total++
	d.inFlight++
	if d.inFlight > d.peak {
		d.peak = d.inFlight
	}
	if repo != "" {
		d.repoInflight[repo]++
		if d.repoInflight[repo] > 1 {
			d.overlap[repo] = true
		}
	}
	d.mu.Unlock()

	if d.block > 0 {
		time.Sleep(d.block)
	}

	d.mu.Lock()
	d.inFlight--
	if repo != "" {
		d.repoInflight[repo]--
	}
	d.mu.Unlock()

	if d.failStack != "" && stack == d.failStack {
		return errors.New("synthetic deploy failure")
	}
	return nil
}

// waitForDeploys polls until at least want Deploy calls have been attempted, or
// the timeout elapses (returns false).
func waitForDeploys(d *trackingDeployer, want int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		got := d.total
		d.mu.Unlock()
		if got >= want {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// distinctRepoStacks builds n stacks, each on its own repo (repo = "r<i>"), so
// the only concurrency bound is the worker-pool size.
func distinctRepoStacks(n int) ([]model.StackConfig, func(string) string) {
	st := make([]model.StackConfig, n)
	repoOf := map[string]string{}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("s%d", i)
		repo := fmt.Sprintf("r%d", i)
		st[i] = model.StackConfig{Name: name, Repo: repo, Branch: "main", ComposeFiles: []model.ComposeFileSpec{{File: "compose.yaml"}}}
		repoOf[name] = repo
	}
	return st, func(s string) string { return repoOf[s] }
}

func TestSyncAll_ConcurrencyBound(t *testing.T) {
	const n = 6
	st, repoOf := distinctRepoStacks(n)
	files := map[string][]byte{"compose.yaml": []byte("services:\n")}

	t.Run("bounded by concurrency", func(t *testing.T) {
		dep := &trackingDeployer{repoOf: repoOf, repoInflight: map[string]int{}, overlap: map[string]bool{}, block: 15 * time.Millisecond}
		git := &fakeGit{revs: []string{"aaa"}, files: files}
		src, _ := manualTicks()
		l := New(git, fakeRenderer{}, dep, nil, &fakeRec{}, nil, st, "changed", false, false, 2, testLogger(), WithTickSource(src))

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = l.Run(ctx, time.Hour); close(done) }()
		if !waitForDeploys(dep, n, 2*time.Second) {
			t.Fatalf("only %d/%d stacks deployed", dep.total, n)
		}
		cancel()
		<-done

		dep.mu.Lock()
		peak := dep.peak
		dep.mu.Unlock()
		if peak > 2 {
			t.Errorf("peak in-flight = %d, want <= 2 (concurrency bound not respected)", peak)
		}
		if peak < 2 {
			t.Errorf("peak in-flight = %d, want exactly 2 (expected parallelism with 6 distinct-repo stacks)", peak)
		}
	})

	t.Run("serial at concurrency=1", func(t *testing.T) {
		dep := &trackingDeployer{repoOf: repoOf, repoInflight: map[string]int{}, overlap: map[string]bool{}, block: 15 * time.Millisecond}
		git := &fakeGit{revs: []string{"aaa"}, files: files}
		src, _ := manualTicks()
		l := New(git, fakeRenderer{}, dep, nil, &fakeRec{}, nil, st, "changed", false, false, 1, testLogger(), WithTickSource(src))

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = l.Run(ctx, time.Hour); close(done) }()
		if !waitForDeploys(dep, n, 2*time.Second) {
			t.Fatalf("only %d/%d stacks deployed", dep.total, n)
		}
		cancel()
		<-done

		dep.mu.Lock()
		peak := dep.peak
		dep.mu.Unlock()
		if peak != 1 {
			t.Errorf("peak in-flight = %d, want 1 (fully serial at concurrency=1)", peak)
		}
	})
}

func TestSyncAll_PerRepoSerialization(t *testing.T) {
	// Two stacks share repo "shared" (must serialize); a third is on repo "solo"
	// (must run alongside one of the shared-repo stacks → cross-repo parallelism).
	st := []model.StackConfig{
		{Name: "a", Repo: "shared", Branch: "main", ComposeFiles: []model.ComposeFileSpec{{File: "compose.yaml"}}},
		{Name: "b", Repo: "shared", Branch: "main", ComposeFiles: []model.ComposeFileSpec{{File: "compose.yaml"}}},
		{Name: "c", Repo: "solo", Branch: "main", ComposeFiles: []model.ComposeFileSpec{{File: "compose.yaml"}}},
	}
	repoMap := map[string]string{"a": "shared", "b": "shared", "c": "solo"}
	files := map[string][]byte{"compose.yaml": []byte("services:\n")}

	dep := &trackingDeployer{
		repoOf:       func(s string) string { return repoMap[s] },
		repoInflight: map[string]int{},
		overlap:      map[string]bool{},
		block:        25 * time.Millisecond, // long enough for overlap to be observable
	}
	git := &fakeGit{revs: []string{"aaa"}, files: files}
	src, _ := manualTicks()
	l := New(git, fakeRenderer{}, dep, nil, &fakeRec{}, nil, st, "changed", false, false, 3, testLogger(), WithTickSource(src))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx, time.Hour); close(done) }()
	if !waitForDeploys(dep, 3, 2*time.Second) {
		t.Fatalf("only %d/3 stacks deployed", dep.total)
	}
	cancel()
	<-done

	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.overlap["shared"] {
		t.Error("same-repo stacks (a,b) overlapped on repo \"shared\"; they must serialize end-to-end")
	}
	// With concurrency 3, the two shared-repo stacks serialize but the solo stack
	// runs alongside one of them, so at least two deploys overlap.
	if dep.peak < 2 {
		t.Errorf("peak in-flight = %d, want >= 2 (cross-repo parallelism expected)", dep.peak)
	}
}

func TestSyncAll_OneFailureDoesNotStopOthers(t *testing.T) {
	st := []model.StackConfig{
		{Name: "boom", Repo: "r1", Branch: "main", ComposeFiles: []model.ComposeFileSpec{{File: "compose.yaml"}}},
		{Name: "ok1", Repo: "r2", Branch: "main", ComposeFiles: []model.ComposeFileSpec{{File: "compose.yaml"}}},
		{Name: "ok2", Repo: "r3", Branch: "main", ComposeFiles: []model.ComposeFileSpec{{File: "compose.yaml"}}},
	}
	repoMap := map[string]string{"boom": "r1", "ok1": "r2", "ok2": "r3"}
	files := map[string][]byte{"compose.yaml": []byte("services:\n")}
	dep := &trackingDeployer{repoOf: func(s string) string { return repoMap[s] }, repoInflight: map[string]int{}, overlap: map[string]bool{}, failStack: "boom"}
	rec := &fakeRec{}
	git := &fakeGit{revs: []string{"aaa"}, files: files}
	src, _ := manualTicks()
	l := New(git, fakeRenderer{}, dep, nil, rec, nil, st, "changed", false, false, 3, testLogger(), WithTickSource(src))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx, time.Hour); close(done) }()
	if !waitForDeploys(dep, 3, 2*time.Second) {
		t.Fatalf("only %d/3 stacks attempted deploy", dep.total)
	}
	cancel()
	<-done

	if dep.total != 3 {
		t.Fatalf("all 3 stacks must attempt deploy even when one fails; got %d", dep.total)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	deployed := map[string]bool{}
	for _, s := range rec.deployed {
		deployed[s] = true
	}
	if !deployed["ok1"] || !deployed["ok2"] {
		t.Errorf("ok1/ok2 must still deploy when another stack fails; deployed=%v", rec.deployed)
	}
	if deployed["boom"] {
		t.Errorf("boom must not be recorded as deployed (its deploy failed); deployed=%v", rec.deployed)
	}
}

func TestNew_ConcurrencyClamped(t *testing.T) {
	// concurrency < 1 must not panic and must behave as 1 (serial). Drive one
	// quick pass and confirm it returns cleanly.
	cases := []int{0, -1, -5}
	for _, n := range cases {
		st, _ := distinctRepoStacks(3)
		git := &fakeGit{revs: []string{"aaa"}, files: map[string][]byte{"compose.yaml": []byte("services:\n")}}
		dep := &trackingDeployer{repoInflight: map[string]int{}, overlap: map[string]bool{}}
		src, _ := manualTicks()
		l := New(git, fakeRenderer{}, dep, nil, &fakeRec{}, nil, st, "changed", false, false, n, testLogger(), WithTickSource(src))
		if l.concurrency != 1 {
			t.Errorf("concurrency=%d should clamp to 1, got %d", n, l.concurrency)
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = l.Run(ctx, time.Hour); close(done) }()
		if !waitForDeploys(dep, 3, 2*time.Second) {
			t.Fatalf("concurrency=%d: only %d/3 stacks deployed", n, dep.total)
		}
		cancel()
		<-done
	}
}

// --- status store integration ---

// fakeStatusStore is a minimal port.StackStatusStore for asserting what the loop
// records.
type fakeStatusStore struct {
	mu       sync.Mutex
	statuses map[string]model.StackStatus
}

func newFakeStatusStore() *fakeStatusStore {
	return &fakeStatusStore{statuses: map[string]model.StackStatus{}}
}

func (f *fakeStatusStore) SetStatus(name string, s model.StackStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s.Name = name
	f.statuses[name] = s
}

func (f *fakeStatusStore) Snapshot() []model.StackStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.StackStatus, 0, len(f.statuses))
	for _, s := range f.statuses {
		out = append(out, s)
	}
	return out
}

func (f *fakeStatusStore) get(name string) (model.StackStatus, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.statuses[name]
	return s, ok
}

// statusRenderer returns a fixed compose map: web (autoscaled, replicas 3),
// worker (plain, replicas 2), agent (global) — to exercise desiredReplicas
// exclusion without parsing YAML in the fake.
type statusRenderer struct{}

func (statusRenderer) Render(_, _ []byte) (map[string]any, error) {
	return map[string]any{"services": map[string]any{
		"web":    map[string]any{"deploy": map[string]any{"replicas": 3, "labels": map[string]any{"swarm.autoscaler.enabled": "true"}}},
		"worker": map[string]any{"deploy": map[string]any{"replicas": 2}},
		"agent":  map[string]any{"deploy": map[string]any{"mode": "global"}},
	}}, nil
}

func waitForStatus(t *testing.T, store *fakeStatusStore, name string) model.StackStatus {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if s, ok := store.get(name); ok {
			return s
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("status for %q never written", name)
	return model.StackStatus{}
}

func TestLoop_WritesSuccessStatus(t *testing.T) {
	git := &fakeGit{revs: []string{"aaa"}, files: map[string][]byte{"compose.yaml": []byte("services:\n")}}
	dep := newFakeDeployer(nil)
	store := newFakeStatusStore()
	src, _ := manualTicks()
	st := []model.StackConfig{{Name: "s", Repo: "r", Branch: "main", ComposeFiles: []model.ComposeFileSpec{{File: "compose.yaml"}}}}
	l := New(git, statusRenderer{}, dep, nil, &fakeRec{}, store, st, "changed", false, false, 1, testLogger(), WithTickSource(src))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx, time.Hour); close(done) }()
	<-dep.ch // deploy fires
	got := waitForStatus(t, store, "s")
	cancel()
	<-done

	if !got.OK {
		t.Errorf("OK=false, want true; stage=%q msg=%q", got.ErrorStage, got.ErrorMessage)
	}
	if got.Revision != "aaa" {
		t.Errorf("Revision=%q want aaa", got.Revision)
	}
	if got.DeployCount != 1 {
		t.Errorf("DeployCount=%d want 1", got.DeployCount)
	}
	// DesiredReplicas: worker=2 only (web autoscaled excluded, agent global excluded).
	if got.DesiredReplicas["worker"] != 2 {
		t.Errorf("DesiredReplicas[worker]=%d want 2; full=%v", got.DesiredReplicas["worker"], got.DesiredReplicas)
	}
	if _, present := got.DesiredReplicas["web"]; present {
		t.Errorf("autoscaled web must be excluded from DesiredReplicas; got %v", got.DesiredReplicas)
	}
	if _, present := got.DesiredReplicas["agent"]; present {
		t.Errorf("global agent must be excluded from DesiredReplicas; got %v", got.DesiredReplicas)
	}
}

func TestLoop_WritesFailureStatus(t *testing.T) {
	git := &fakeGit{err: errors.New("boom"), files: map[string][]byte{}}
	dep := newFakeDeployer(nil)
	store := newFakeStatusStore()
	src, _ := manualTicks()
	l := New(git, fakeRenderer{}, dep, nil, &fakeRec{}, store, stacks("s", "compose.yaml"), "changed", false, false, 1, testLogger(), WithTickSource(src))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx, time.Hour); close(done) }()
	got := waitForStatus(t, store, "s")
	cancel()
	<-done

	if got.OK {
		t.Error("OK=true, want false on git failure")
	}
	if got.ErrorStage != "git" {
		t.Errorf("ErrorStage=%q want git", got.ErrorStage)
	}
}

// fakeStackState is a minimal port.StackStateReader for drift-gauge tests.
type fakeStackState struct{ services []model.StackService }

func (f fakeStackState) StackServices(_ context.Context, _ string) ([]model.StackService, error) {
	return f.services, nil
}

// driftRec captures StackReplicas calls (embeds NopRecorder for the rest).
type driftRec struct {
	port.NopRecorder
	mu       sync.Mutex
	replicas []stackReplicaCall
}

type stackReplicaCall struct {
	stack, service string
	desired, live  uint64
}

func (r *driftRec) StackReplicas(stack, service string, desired, live uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replicas = append(r.replicas, stackReplicaCall{stack: stack, service: service, desired: desired, live: live})
}

// TestLoop_RecordsStackReplicaDrift proves a per-stack desired-vs-live replica
// gauge is recorded from the rendered compose (desired) and the live-state reader.
func TestLoop_RecordsStackReplicaDrift(t *testing.T) {
	git := &fakeGit{revs: []string{"aaa"}, files: map[string][]byte{"compose.yaml": []byte("services:\n")}}
	dep := newFakeDeployer(nil)
	// statusRenderer yields web(autoscaled), worker(2), agent(global) → desired={worker:2}.
	state := fakeStackState{services: []model.StackService{{Name: "worker", Replicas: 2, Replicated: true}}}
	rec := &driftRec{}
	src, _ := manualTicks()
	st := []model.StackConfig{{Name: "mystack", Repo: "r", Branch: "main", ComposeFiles: []model.ComposeFileSpec{{File: "compose.yaml"}}}}
	l := New(git, statusRenderer{}, dep, nil, rec, nil, st, "changed", false, false, 1, testLogger(),
		WithTickSource(src), WithStackStateReader(state))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx, time.Hour); close(done) }()
	<-dep.ch // deploy fires (recordStackReplicas runs before it)
	cancel()
	<-done

	rec.mu.Lock()
	defer rec.mu.Unlock()
	var worker *stackReplicaCall
	for i := range rec.replicas {
		if rec.replicas[i].service == "worker" {
			worker = &rec.replicas[i]
		}
	}
	if worker == nil {
		t.Fatalf("no StackReplicas{worker} recorded; got %+v", rec.replicas)
	}
	if worker.stack != "mystack" || worker.desired != 2 || worker.live != 2 {
		t.Errorf("StackReplicas = stack %q desired %d live %d, want mystack/2/2", worker.stack, worker.desired, worker.live)
	}
}

// --- multi-file stack tests ---

// TestLoop_MultiFilePerFilePullPolicy is the headline case: one stack, two
// compose files with distinct per-file pull policies (the dev "app always /
// postgres changed" split). The loop must run TWO sequential deploys in file
// order, each with its own --resolve-image, and record DeployApplied exactly
// once for the stack (not once per file).
func TestLoop_MultiFilePerFilePullPolicy(t *testing.T) {
	files := map[string][]byte{
		"app.yaml": []byte("services:\n"),
		"pg.yaml":  []byte("services:\n"),
	}
	git := &fakeGit{revs: []string{"aaa"}, files: files}
	dep := newFakeDeployer(nil)
	rec := &fakeRec{}
	src, _ := manualTicks()
	st := []model.StackConfig{{Name: "s", Repo: "r", Branch: "main", ComposeFiles: []model.ComposeFileSpec{
		{File: "app.yaml", PullPolicy: "always"},
		{File: "pg.yaml", PullPolicy: "changed"},
	}}}
	// Global policy "changed" must be overridden by the per-file values.
	l := New(git, fakeRenderer{}, dep, nil, rec, nil, st, "changed", false, false, 1, testLogger(), WithTickSource(src))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx, time.Hour); close(done) }()
	<-dep.ch // app.yaml deploy
	<-dep.ch // pg.yaml deploy
	cancel()
	<-done

	if c := dep.callCount(); c != 2 {
		t.Fatalf("multi-file stack must deploy each file once; call count = %d, want 2", c)
	}
	if got := dep.pullPolicySeq(); len(got) != 2 || got[0] != "always" || got[1] != "changed" {
		t.Errorf("per-file pull policy seq = %v, want [always changed] (file order preserved)", got)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if n := len(rec.deployed); n != 1 || rec.deployed[0] != "s" {
		t.Errorf("DeployApplied = %v, want exactly one [s] (once per successful stack tick, not per file)", rec.deployed)
	}
}

// TestLoop_MultiFilePullPolicyPrecedence checks file → stack → global precedence
// for each file's deploy independently.
func TestLoop_MultiFilePullPolicyPrecedence(t *testing.T) {
	files := map[string][]byte{"a.yaml": []byte("services:\n"), "b.yaml": []byte("services:\n")}
	for _, tc := range []struct {
		name        string
		specs       []model.ComposeFileSpec
		stackPolicy string
		global      string
		want        []string
	}{
		{
			name:        "stack policy wins when no per-file policy",
			specs:       []model.ComposeFileSpec{{File: "a.yaml"}, {File: "b.yaml"}},
			stackPolicy: "changed",
			global:      "always",
			want:        []string{"changed", "changed"},
		},
		{
			name:   "global fallback when no policy anywhere",
			specs:  []model.ComposeFileSpec{{File: "a.yaml"}, {File: "b.yaml"}},
			global: "always",
			want:   []string{"always", "always"},
		},
		{
			name:        "per-file overrides stack and global, mixed",
			specs:       []model.ComposeFileSpec{{File: "a.yaml", PullPolicy: "always"}, {File: "b.yaml"}},
			stackPolicy: "changed",
			global:      "changed",
			want:        []string{"always", "changed"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			git := &fakeGit{revs: []string{"aaa"}, files: files}
			dep := newFakeDeployer(nil)
			src, _ := manualTicks()
			st := []model.StackConfig{{Name: "s", Repo: "r", Branch: "main", ComposeFiles: tc.specs, PullPolicy: tc.stackPolicy}}
			l := New(git, fakeRenderer{}, dep, nil, &fakeRec{}, nil, st, tc.global, false, false, 1, testLogger(), WithTickSource(src))

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { _ = l.Run(ctx, time.Hour); close(done) }()
			<-dep.ch
			<-dep.ch
			cancel()
			<-done

			if got := dep.pullPolicySeq(); len(got) != 2 || got[0] != tc.want[0] || got[1] != tc.want[1] {
				t.Errorf("pull policy seq = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLoop_MultiFilePartialFailure proves sequential deploys are NOT
// transactional: the first file deploys, the second fails, and the stack is
// recorded as failed (DeployApplied NOT fired; a 'deploy' SyncError is) — even
// though the first file's services are now live in Swarm.
func TestLoop_MultiFilePartialFailure(t *testing.T) {
	files := map[string][]byte{"a.yaml": []byte("services:\n"), "b.yaml": []byte("services:\n")}
	dep := newFakeDeployer([]error{nil, errors.New("pg deploy failure")}) // file 0 OK, file 1 fails
	rec := &fakeRec{}
	git := &fakeGit{revs: []string{"aaa"}, files: files}
	src, _ := manualTicks()
	st := []model.StackConfig{{Name: "s", Repo: "r", Branch: "main", ComposeFiles: []model.ComposeFileSpec{
		{File: "a.yaml"},
		{File: "b.yaml"},
	}}}
	l := New(git, fakeRenderer{}, dep, nil, rec, nil, st, "changed", false, false, 1, testLogger(), WithTickSource(src))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx, time.Hour); close(done) }()
	<-dep.ch // a.yaml deployed OK
	<-dep.ch // b.yaml failed
	cancel()
	<-done

	if c := dep.callCount(); c != 2 {
		t.Fatalf("both files must be attempted; call count = %d, want 2", c)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.deployed) != 0 {
		t.Errorf("DeployApplied must NOT fire when a file fails mid-stack; got %v", rec.deployed)
	}
	if len(rec.syncErrs) == 0 || rec.syncErrs[0] != "deploy" {
		t.Errorf("expected a 'deploy' SyncError on mid-stack failure; got %v", rec.syncErrs)
	}
}
