// Package gitopsync runs the periodic GitOps stack-sync control loop. It is a
// peer of app/reconciler and folds GitOps into the manager process: each tick it
// syncs stacks from Git, renders compose, and deploys — but only when Git
// actually changed (the autoscaler owns the replica count in between). A deploy
// is dry-run-gated and carries autoscaled replicas forward (see adapter/stackdeploy),
// so the GitOps reapply never clobbers a count the autoscaler set.
package gitopsync

import (
	"context"
	"log/slog"
	"sync"
	"time"

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
	recorder   port.Recorder
	stacks     []model.StackConfig
	pullPolicy string
	dryRun     bool
	logger     *slog.Logger

	tickSource TickSource
	customTick bool

	// lastDeployed tracks per-stack progress so we skip a deploy when Git is
	// unchanged AND the last deploy succeeded (retrying when it failed).
	mu              sync.Mutex
	lastDeployedRev map[string]string
	lastDeployedOK  map[string]bool
}

// New constructs the loop. A nil recorder falls back to no-op, a nil logger to
// slog.Default. opts (e.g. WithTickSource) are applied last.
func New(
	git port.GitSource,
	renderer port.StackRenderer,
	deployer port.StackDeployer,
	recorder port.Recorder,
	stacks []model.StackConfig,
	pullPolicy string,
	dryRun bool,
	logger *slog.Logger,
	opts ...Option,
) *Loop {
	if logger == nil {
		logger = slog.Default()
	}
	if recorder == nil {
		recorder = port.NopRecorder{}
	}
	l := &Loop{
		git:             git,
		renderer:        renderer,
		deployer:        deployer,
		recorder:        recorder,
		stacks:          stacks,
		pullPolicy:      pullPolicy,
		dryRun:          dryRun,
		logger:          logger,
		tickSource:      defaultTickSource,
		lastDeployedRev: make(map[string]string),
		lastDeployedOK:  make(map[string]bool),
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
		"interval", interval, "stacks", len(l.stacks), "dry_run", l.dryRun, "pull_policy", l.pullPolicy)
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

// syncAll runs one sync pass over every configured stack.
func (l *Loop) syncAll(ctx context.Context) {
	defer l.recorder.SyncRun()
	for _, st := range l.stacks {
		l.syncStack(ctx, st)
	}
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
	compose, err := l.renderer.Render(composeBytes, valuesBytes)
	if err != nil {
		log.Error("gitops: render failed", "err", err)
		l.recorder.SyncError("render")
		return
	}

	if l.dryRun {
		log.Info("gitops: dry-run; would deploy stack", "revision", rev)
		l.recorder.SyncSuppressed("dry_run")
		return
	}

	log.Info("gitops: deploying stack", "revision", rev)
	if err := l.deployer.Deploy(ctx, st.Name, compose, port.DeployOpts{PullPolicy: l.pullPolicy}); err != nil {
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

// defaultTickSource is the production tick source: a real time.Ticker.
func defaultTickSource(interval time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}
