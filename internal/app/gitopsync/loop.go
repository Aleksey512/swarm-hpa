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
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/config"
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

// WithStackStateReader wires a live-state reader so the loop can emit per-stack
// desired-vs-live replica drift gauges. Without it the drift gauges are skipped.
func WithStackStateReader(r port.StackStateReader) Option {
	return func(l *Loop) { l.state = r }
}

// Loop is the GitOps stack-sync control loop.
type Loop struct {
	git         port.GitSource
	renderer    port.StackRenderer
	deployer    port.StackDeployer
	sops        port.SecretDecrypter
	recorder    port.Recorder
	statusStore port.StackStatusStore // nil → status tracking disabled (tests)
	state       port.StackStateReader // live Swarm state for drift gauges (nil → skip)
	stacks      []model.StackConfig
	pullPolicy  string
	dryRun      bool
	autoRotate  bool
	logger      *slog.Logger

	tickSource TickSource
	customTick bool

	// lastDeployed tracks per-stack progress so we skip a deploy when Git is
	// unchanged AND the last deploy succeeded (retrying when it failed).
	// lastDesired is the per-stack non-autoscaled replica snapshot from the last
	// render (fed to the drift check); deployCount counts successful deploys.
	mu              sync.Mutex
	lastDeployedRev map[string]string
	lastDeployedOK  map[string]bool
	lastDesired     map[string]map[string]uint64
	deployCount     map[string]uint64

	// concurrency caps how many syncStack goroutines run at once. repoLocks
	// serializes stacks that share a repo end-to-end (one on-disk worktree per
	// repo); repoMu guards the lazy allocation of repoLocks.
	concurrency int
	repoMu      sync.Mutex
	repoLocks   map[string]*sync.Mutex
}

// New constructs the loop. sops decrypts secret files in place (nil disables
// decrypt). A nil recorder falls back to no-op, a nil logger to slog.Default.
// statusStore receives the per-stack status the GET /stacks API reads (nil
// disables status tracking). opts (e.g. WithTickSource) are applied last.
func New(
	git port.GitSource,
	renderer port.StackRenderer,
	deployer port.StackDeployer,
	sops port.SecretDecrypter,
	recorder port.Recorder,
	statusStore port.StackStatusStore,
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
		statusStore:     statusStore,
		stacks:          stacks,
		pullPolicy:      pullPolicy,
		dryRun:          dryRun,
		autoRotate:      autoRotate,
		concurrency:     concurrency,
		logger:          logger,
		tickSource:      defaultTickSource,
		lastDeployedRev: make(map[string]string),
		lastDeployedOK:  make(map[string]bool),
		lastDesired:     make(map[string]map[string]uint64),
		deployCount:     make(map[string]uint64),
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
// stack never kills the loop, and records the outcome (revision, failing stage,
// desired-replica snapshot) into the status store for the GET /stacks API.
func (l *Loop) syncStack(ctx context.Context, st model.StackConfig) {
	var (
		rev      string
		stage    string // "" = no error this pass; otherwise the failing stage
		errMsg   string
		deployed bool
	)
	defer func() {
		if r := recover(); r != nil {
			l.logger.Error("gitops: panic syncing stack; continuing", "stack", st.Name, "panic", r)
			l.recorder.SyncError("sync")
			stage, errMsg = "sync", fmt.Sprintf("panic: %v", r)
		}
		l.recordStatus(st.Name, rev, stage, errMsg, deployed)
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

	r, err := l.git.Sync(ctx, st)
	if err != nil {
		log.Error("gitops: git sync failed", "err", err)
		l.recorder.SyncError("git")
		stage, errMsg = "git", err.Error()
		return
	}
	rev = r
	l.recorder.LastRevision(st.Name, rev)

	composeBytes, err := l.git.ReadFile(ctx, st, st.ComposeFile)
	if err != nil {
		log.Error("gitops: read compose failed", "err", err)
		l.recorder.SyncError("render")
		stage, errMsg = "render", err.Error()
		return
	}
	var valuesBytes []byte
	if st.ValuesFile != "" {
		valuesBytes, err = l.git.ReadFile(ctx, st, st.ValuesFile)
		if err != nil {
			log.Error("gitops: read values failed", "err", err)
			l.recorder.SyncError("render")
			stage, errMsg = "render", err.Error()
			return
		}
	}
	composeMap, err := l.renderer.Render(composeBytes, valuesBytes)
	if err != nil {
		log.Error("gitops: render failed", "err", err)
		l.recorder.SyncError("render")
		stage, errMsg = "render", err.Error()
		return
	}
	// Snapshot the non-autoscaled, non-global desired replicas for the drift
	// check (computed only on a successful render; stable across unchanged skips).
	desiredSnap := desiredReplicas(composeMap)
	l.setDesired(st.Name, desiredSnap)
	l.recordStackReplicas(ctx, st.Name, desiredSnap)

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
			stage, errMsg = "secrets", err.Error()
			return
		}
	}
	if l.sops != nil && len(sopsFiles) > 0 {
		log.Debug("gitops: decrypting sops files", "count", len(sopsFiles))
		if err := l.sops.Decrypt(ctx, l.git.WorktreePath(st), sopsFiles); err != nil {
			log.Error("gitops: sops decrypt failed", "err", err)
			l.recorder.SyncError("secrets")
			stage, errMsg = "secrets", err.Error()
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
			stage, errMsg = "rotate", rerr.Error()
			return
		} else if n > 0 {
			log.Debug("gitops: rotated objects", "count", n)
		}
	}

	log.Info("gitops: deploying stack", "revision", rev)
	// Co-locate the temp compose with the source compose file so the relative
	// configs:/secrets: paths inside it resolve against the same directory they
	// resolve against for the original file (the worktree), not /tmp.
	composeDir := filepath.Join(l.git.WorktreePath(st), filepath.Dir(st.ComposeFile))
	if err := l.deployer.Deploy(ctx, st.Name, composeMap, port.DeployOpts{
		PullPolicy: l.pullPolicy,
		ComposeDir: composeDir,
	}); err != nil {
		log.Error("gitops: deploy failed", "err", err)
		l.recorder.SyncError("deploy")
		l.markDeploy(st.Name, rev, false)
		stage, errMsg = "deploy", err.Error()
		return
	}
	l.recorder.DeployApplied(st.Name)
	l.markDeploy(st.Name, rev, true)
	l.incDeploy(st.Name)
	deployed = true
	log.Info("gitops: stack synced", "revision", rev)
}

func (l *Loop) markDeploy(name, rev string, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lastDeployedRev[name] = rev
	l.lastDeployedOK[name] = ok
}

// setDesired caches the per-stack desired-replica snapshot from the last render.
func (l *Loop) setDesired(name string, desired map[string]uint64) {
	l.mu.Lock()
	l.lastDesired[name] = desired
	l.mu.Unlock()
}

// incDeploy bumps the per-stack successful-deploy counter.
func (l *Loop) incDeploy(name string) {
	l.mu.Lock()
	l.deployCount[name]++
	l.mu.Unlock()
}

// recordStatus writes the per-stack outcome to the status store (no-op when no
// store is wired). OK is true when no stage failed this pass. DesiredReplicas and
// DeployCount come from the cached counters so they stay stable on unchanged
// skips (which skip render/deploy).
func (l *Loop) recordStatus(name, rev, stage, errMsg string, deployed bool) {
	if l.statusStore == nil {
		return
	}
	l.mu.Lock()
	desired := l.lastDesired[name]
	count := l.deployCount[name]
	l.mu.Unlock()

	l.statusStore.SetStatus(name, model.StackStatus{
		Name:            name,
		Revision:        rev,
		OK:              stage == "",
		ErrorStage:      stage,
		ErrorMessage:    errMsg,
		LastSync:        time.Now(),
		DeployCount:     count,
		DesiredReplicas: desired,
	})
	l.logger.Debug("gitops: status updated", "stack", name, "ok", stage == "", "stage", stage)
}

// recordStackReplicas emits per-service desired (compose) vs live (Swarm) replica
// gauges for a stack — GitOps drift as a metric, in addition to the /stacks UI.
// desired is the non-autoscaled, non-global snapshot from desiredReplicas; live is
// read from Swarm when a StackStateReader is wired (skipped otherwise). A live-read
// failure is logged at DEBUG and skipped, never failing the sync.
func (l *Loop) recordStackReplicas(ctx context.Context, stack string, desired map[string]uint64) {
	if l.state == nil || len(desired) == 0 {
		return
	}
	live, err := l.state.StackServices(ctx, stack)
	if err != nil {
		l.logger.Debug("gitops: live-state read failed; drift metric skipped", "stack", stack, "err", err)
		return
	}
	liveByName := make(map[string]uint64, len(live))
	for _, s := range live {
		liveByName[s.Name] = s.Replicas
	}
	for svc, d := range desired {
		l.recorder.StackReplicas(stack, svc, d, liveByName[svc])
	}
}

// desiredReplicas returns the non-autoscaled, non-global service→replica snapshot
// from a rendered compose map, for drift detection. Autoscaled services
// (swarm.autoscaler.enabled=true) and global-mode services are excluded — their
// replica divergence is intentional, not drift. Services without an explicit
// replicas count are skipped (unknown desired). Mirrors the detection in
// adapter/stackdeploy/carryforward.go (kept private per package; not shared across
// the app→adapter boundary).
func desiredReplicas(compose map[string]any) map[string]uint64 {
	services, ok := compose["services"].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]uint64)
	for name, raw := range services {
		svc, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if isGlobalService(svc) {
			continue
		}
		if labelIsTrue(svc, config.LabelEnabled) {
			continue // autoscaler-owned: the HPA controls its replicas
		}
		replicas, ok := replicasOf(svc)
		if !ok {
			continue
		}
		out[name] = replicas
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isGlobalService reports whether a compose service is mode: global (top-level or
// under deploy:). Mirrors adapter/stackdeploy.isGlobalMode.
func isGlobalService(svc map[string]any) bool {
	if d, ok := svc["deploy"].(map[string]any); ok {
		if m, _ := d["mode"].(string); m == "global" {
			return true
		}
	}
	if m, _ := svc["mode"].(string); m == "global" {
		return true
	}
	return false
}

// labelIsTrue reports whether the given compose label is "true", looking under
// deploy.labels then the service's top-level labels, in either the map or the list
// (- "key=value") form. Mirrors adapter/stackdeploy.serviceLabels for one key.
func labelIsTrue(svc map[string]any, key string) bool {
	if d, ok := svc["deploy"].(map[string]any); ok {
		if readLabel(d["labels"], key) == "true" {
			return true
		}
	}
	return readLabel(svc["labels"], key) == "true"
}

// readLabel reads one key from a compose labels value (map or list form); "" if
// absent.
func readLabel(labels any, key string) string {
	switch t := labels.(type) {
	case map[string]any:
		if v, ok := t[key]; ok {
			return fmt.Sprint(v)
		}
	case []any:
		for _, item := range t {
			s := fmt.Sprint(item)
			if k, v, ok := strings.Cut(s, "="); ok && k == key {
				return v
			}
		}
	}
	return ""
}

// replicasOf reads a compose service's replica count from deploy.replicas, falling
// back to a top-level replicas field. ok is false when unset (unknown desired).
func replicasOf(svc map[string]any) (uint64, bool) {
	if d, ok := svc["deploy"].(map[string]any); ok {
		if r, ok := asUint64(d["replicas"]); ok {
			return r, true
		}
	}
	return asUint64(svc["replicas"])
}

// asUint64 coerces a YAML-decoded number (int/int64/uint64/float64) to uint64.
// Negative values are rejected (replicas can't be negative) so the signed→unsigned
// conversions below are safe.
func asUint64(v any) (uint64, bool) {
	switch n := v.(type) {
	case int:
		if n < 0 {
			return 0, false
		}
		return uint64(n), true //nolint:gosec // G115: guarded against negative
	case int64:
		if n < 0 {
			return 0, false
		}
		return uint64(n), true //nolint:gosec // G115: guarded against negative
	case uint64:
		return n, true
	case float64:
		if n < 0 {
			return 0, false
		}
		return uint64(n), true //nolint:gosec // G115: guarded against negative
	}
	return 0, false
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
