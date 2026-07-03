// Package gitopsync runs the periodic GitOps stack-sync control loop. It is a
// peer of app/reconciler and folds GitOps into the manager process: each tick it
// syncs stacks from Git, renders compose, decrypts sops secrets, rotates
// configs/secrets by content hash, and deploys — but only when Git actually
// changed (the autoscaler owns the replica count in between). A deploy is
// dry-run-gated and carries autoscaled replicas forward (see adapter/stackdeploy),
// so the GitOps reapply never clobbers a count the autoscaler set.
package gitopsync

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/compose"
	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// TickSource produces the channel Run selects on, mirroring the reconciler seam
// so the loop is testable without real time.
type TickSource func(interval time.Duration) (ticks <-chan time.Time, stop func())

// Option configures the Loop (e.g. WithTickSource for tests).
type Option func(*Loop)

// WithTickSource overrides how Run obtains its ticks. Intended for tests.
func WithTickSource(src TickSource) Option {
	return func(l *Loop) {
		if src != nil {
			l.tickSource = src
			l.customTick = true
		}
	}
}

// Loop is the GitOps stack-sync control loop.
type Loop struct {
	git        port.GitSource
	renderer   port.StackRenderer
	deployer   port.StackDeployer
	sops       port.SecretDecrypter
	recorder   port.Recorder
	stacks     []model.StackConfig
	pullPolicy string
	dryRun     bool
	autoRotate bool
	logger     *slog.Logger

	tickSource TickSource
	customTick bool

	// lastDeployed tracks per-stack progress so we skip a deploy when Git is
	// unchanged AND the last deploy succeeded (retrying when it failed).
	mu              sync.Mutex
	lastDeployedRev map[string]string
	lastDeployedOK  map[string]bool

	// concurrency caps how many syncStack goroutines run at once. repoLocks
	// serializes stacks that share a repo end-to-end (one on-disk worktree per
	// repo); repoMu guards the lazy allocation of repoLocks.
	concurrency int
	repoMu      sync.Mutex
	repoLocks   map[string]*sync.Mutex
}

// New constructs the loop. sops decrypts secret files in place (nil disables
// decrypt). A nil recorder falls back to no-op, a nil logger to slog.Default.
// opts (e.g. WithTickSource) are applied last.
func New(
	git port.GitSource,
	renderer port.StackRenderer,
	deployer port.StackDeployer,
	sops port.SecretDecrypter,
	recorder port.Recorder,
	stacks []model.StackConfig,
	pullPolicy string,
	dryRun bool,
	autoRotate bool,
	concurrency int,
	logger *slog.Logger,
	opts ...Option,
) *Loop {
	if logger == nil {
		logger = slog.Default()
	}
	if recorder == nil {
		recorder = port.NopRecorder{}
	}
	if concurrency < 1 {
		concurrency = 1 // defensive: a misconfigured knob never panics or runs unbounded
	}
	l := &Loop{
		git:             git,
		renderer:        renderer,
		deployer:        deployer,
		sops:            sops,
		recorder:        recorder,
		stacks:          stacks,
		pullPolicy:      pullPolicy,
		dryRun:          dryRun,
		autoRotate:      autoRotate,
		concurrency:     concurrency,
		logger:          logger,
		tickSource:      defaultTickSource,
		lastDeployedRev: make(map[string]string),
		lastDeployedOK:  make(map[string]bool),
		repoLocks:       make(map[string]*sync.Mutex),
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Run syncs all stacks immediately and then on every interval tick, until ctx is
// cancelled (graceful stop, returning nil). One stack's failure never stops the
// loop or the other stacks.
func (l *Loop) Run(ctx context.Context, interval time.Duration) error {
	l.logger.Info("gitops loop started",
		"interval", interval, "stacks", len(l.stacks), "dry_run", l.dryRun,
		"pull_policy", l.pullPolicy, "auto_rotate", l.autoRotate,
		"concurrency", l.concurrency)
	if l.customTick {
		l.logger.Debug("custom tick source injected (non-default)")
	}

	ticks, stop := l.tickSource(interval)
	defer stop()

	l.syncAll(ctx)
	for {
		select {
		case <-ctx.Done():
			l.logger.Info("gitops loop stopping", "reason", ctx.Err())
			return nil
		case <-ticks:
			l.syncAll(ctx)
		}
	}
}

// syncAll runs one sync pass over every configured stack. Stacks are synced on a
// bounded worker pool (up to l.concurrency in flight); one stack's failure or
// panic never stops the others. Stacks that share a repo are serialized inside
// syncStack (one on-disk worktree per repo).
func (l *Loop) syncAll(ctx context.Context) {
	defer l.recorder.SyncRun()
	l.logger.Debug("gitops: syncing stacks", "stacks", len(l.stacks), "concurrency", l.concurrency)

	// Counting semaphore + WaitGroup: bounds parallelism without cancelling
	// siblings on the first error (each syncStack recovers and records on its own).
	sem := make(chan struct{}, l.concurrency)
	var wg sync.WaitGroup
	for _, st := range l.stacks {
		wg.Add(1)
		sem <- struct{}{} // block until a worker slot is free
		go func(st model.StackConfig) {
			defer wg.Done()
			defer func() { <-sem }()
			l.syncStack(ctx, st)
		}(st)
	}
	wg.Wait()
}

// syncStack syncs a single stack end-to-end. It recovers from panics so one bad
// stack never kills the loop.
func (l *Loop) syncStack(ctx context.Context, st model.StackConfig) {
	defer func() {
		if r := recover(); r != nil {
			l.logger.Error("gitops: panic syncing stack; continuing", "stack", st.Name, "panic", r)
			l.recorder.SyncError("sync")
		}
	}()
	log := l.logger.With(slog.String("stack", st.Name))

	// Serialize stacks that share a repo for the WHOLE pipeline. There is one
	// on-disk worktree per repo (reposPath/<repo>), shared by every stack on it,
	// and this pipeline mutates it in place (sops writes plaintext) and reads it
	// (rotation). The git adapter's per-repo lock only guards Sync, so the entire
	// Sync→ReadFile→Render→Decrypt→Rotate→Deploy sequence is locked here.
	unlock := l.acquireRepoLock(st.Repo)
	defer unlock()
	log.Debug("gitops: acquired repo lock", "repo", st.Repo)

	rev, err := l.git.Sync(ctx, st)
	if err != nil {
		log.Error("gitops: git sync failed", "err", err)
		l.recorder.SyncError("git")
		return
	}
	l.recorder.LastRevision(st.Name, rev)

	if l.unchangedSinceLastSuccess(st.Name, rev) {
		log.Debug("gitops: no changes; skipping deploy", "revision", rev)
		return
	}

	composeBytes, err := l.git.ReadFile(ctx, st, st.ComposeFile)
	if err != nil {
		log.Error("gitops: read compose failed", "err", err)
		l.recorder.SyncError("render")
		return
	}
	var valuesBytes []byte
	if st.ValuesFile != "" {
		valuesBytes, err = l.git.ReadFile(ctx, st, st.ValuesFile)
		if err != nil {
			log.Error("gitops: read values failed", "err", err)
			l.recorder.SyncError("render")
			return
		}
	}
	composeMap, err := l.renderer.Render(composeBytes, valuesBytes)
	if err != nil {
		log.Error("gitops: render failed", "err", err)
		l.recorder.SyncError("render")
		return
	}

	// Dry-run short-circuits BEFORE decrypt: decrypt writes plaintext to disk (a
	// side effect), so dry-run must not prepare or deploy.
	if l.dryRun {
		log.Info("gitops: dry-run; would decrypt/rotate/deploy stack", "revision", rev)
		l.recorder.SyncSuppressed("dry_run")
		return
	}

	// Decrypt sops-encrypted secret files in place (no-op when none; skipped when
	// no decrypter is wired).
	sopsFiles := st.SopsFiles
	if st.SopsSecretsDiscovery {
		if sopsFiles, err = compose.DiscoverSecretFiles(composeMap, filepath.Dir(st.ComposeFile)); err != nil {
			log.Error("gitops: secret discovery failed", "err", err)
			l.recorder.SyncError("secrets")
			return
		}
	}
	if l.sops != nil && len(sopsFiles) > 0 {
		log.Debug("gitops: decrypting sops files", "count", len(sopsFiles))
		if err := l.sops.Decrypt(ctx, l.git.WorktreePath(st), sopsFiles); err != nil {
			log.Error("gitops: sops decrypt failed", "err", err)
			l.recorder.SyncError("secrets")
			return
		}
	}

	// Rotate file-backed configs/secrets by content hash so Swarm picks up changes
	// (no-op when autoRotate is disabled).
	if l.autoRotate {
		resolver := func(rel string) ([]byte, error) { return l.git.ReadFile(ctx, st, rel) }
		if n, rerr := compose.ApplyRotation(composeMap, st.Name, filepath.Dir(st.ComposeFile), resolver); rerr != nil {
			log.Error("gitops: rotation failed", "err", rerr)
			l.recorder.SyncError("rotate")
			return
		} else if n > 0 {
			log.Debug("gitops: rotated objects", "count", n)
		}
	}

	log.Info("gitops: deploying stack", "revision", rev)
	if err := l.deployer.Deploy(ctx, st.Name, composeMap, port.DeployOpts{PullPolicy: l.pullPolicy}); err != nil {
		log.Error("gitops: deploy failed", "err", err)
		l.recorder.SyncError("deploy")
		l.markDeploy(st.Name, rev, false)
		return
	}
	l.recorder.DeployApplied(st.Name)
	l.markDeploy(st.Name, rev, true)
	log.Info("gitops: stack synced", "revision", rev)
}

// unchangedSinceLastSuccess reports whether the stack is already deployed at this
// revision with no prior failure (so a deploy would be a redundant no-op). A
// failed last deploy is retried even at the same revision.
func (l *Loop) unchangedSinceLastSuccess(name, rev string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastDeployedRev[name] == rev && l.lastDeployedOK[name]
}

func (l *Loop) markDeploy(name, rev string, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lastDeployedRev[name] = rev
	l.lastDeployedOK[name] = ok
}

// acquireRepoLock locks the per-repo mutex (allocating it lazily on first use)
// and returns an unlock function. Stacks that share a repo serialize end-to-end;
// stacks on different repos run concurrently. One on-disk worktree per repo
// makes this mandatory: decrypt and rotation mutate that shared worktree.
func (l *Loop) acquireRepoLock(repo string) func() {
	l.repoMu.Lock()
	lock, ok := l.repoLocks[repo]
	if !ok {
		lock = &sync.Mutex{}
		l.repoLocks[repo] = lock
	}
	l.repoMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// defaultTickSource is the production tick source: a real time.Ticker.
func defaultTickSource(interval time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}
