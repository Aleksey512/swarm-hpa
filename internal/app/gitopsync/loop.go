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
		files    []model.StackFileStatus // per-file deploy outcome of this tick (v0.6.0 multi-compose), for the /stacks UI
	)
	defer func() {
		if r := recover(); r != nil {
			l.logger.Error("gitops: panic syncing stack; continuing", "stack", st.Name, "panic", r)
			l.recorder.SyncError("sync")
			stage, errMsg = "sync", fmt.Sprintf("panic: %v", r)
		}
		l.recordStatus(st.Name, rev, stage, errMsg, deployed, files)
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

	// Read the optional, shared values file once — the same Values render every
	// compose file in this stack.
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

	// Render every merge group in declaration order. A group is one
	// ComposeFileSpec: its base file plus the overrides merged into the SAME
	// deploy. Every file of every group is rendered against the same Values.
	//
	// Two orders are preserved here and both are load-bearing: groups deploy in
	// list order (separate, additive deploys), and the documents WITHIN a group
	// keep base-then-overrides order because that is what docker/cli's merge uses
	// to decide which value wins.
	groups := make([]renderedGroup, 0, len(st.ComposeFiles))
	totalFiles := 0
	for gi, spec := range st.ComposeFiles {
		paths := spec.AllFiles()
		docs := make([]renderedDoc, 0, len(paths))
		for di, path := range paths {
			composeBytes, rerr := l.git.ReadFile(ctx, st, path)
			if rerr != nil {
				log.Error("gitops: read compose failed", "file", path, "group", gi, "err", rerr)
				l.recorder.SyncError("render")
				stage, errMsg = "render", rerr.Error()
				return
			}
			composeMap, rerr := l.renderer.Render(composeBytes, valuesBytes)
			if rerr != nil {
				log.Error("gitops: render failed", "file", path, "group", gi, "err", rerr)
				l.recorder.SyncError("render")
				stage, errMsg = "render", rerr.Error()
				return
			}
			log.Debug("gitops: rendered compose file",
				"file", path, "group", gi, "index_in_group", di, "is_override", di > 0,
				"groups_total", len(st.ComposeFiles))
			docs = append(docs, renderedDoc{path: path, compose: composeMap})
		}
		effPolicy, source := effectivePullPolicy(spec, st.PullPolicy, l.pullPolicy)
		log.Debug("gitops: merge group resolved",
			"base", spec.File, "overrides", spec.Overrides, "files", len(docs),
			"pull_policy", effPolicy, "source", source)
		totalFiles += len(docs)
		groups = append(groups, renderedGroup{spec: spec, docs: docs})
	}

	// Snapshot the non-autoscaled, non-global desired replicas for the drift
	// check (computed only on a successful render; stable across unchanged skips).
	// Unioned across all groups — a service lives in exactly one group.
	desiredSnap := make(map[string]uint64)
	for gi, g := range groups {
		groupDesired, excluded := desiredReplicasGroup(g.maps())
		log.Debug("gitops: desired snapshot",
			"group", gi, "base", g.spec.File, "services", len(groupDesired),
			"excluded_autoscaled", excluded)
		for svc, n := range groupDesired {
			desiredSnap[svc] = n
		}
	}
	if len(desiredSnap) == 0 {
		desiredSnap = nil
	}
	l.setDesired(st.Name, desiredSnap)
	l.recordStackReplicas(ctx, st.Name, desiredSnap)

	// Dry-run short-circuits BEFORE decrypt: decrypt writes plaintext to disk (a
	// side effect), so dry-run must not prepare or deploy.
	if l.dryRun {
		for _, g := range groups {
			eff, _ := effectivePullPolicy(g.spec, st.PullPolicy, l.pullPolicy)
			log.Info("gitops: dry-run; would deploy compose group",
				"file", g.spec.File, "overrides", g.spec.Overrides, "files", len(g.docs),
				"pull_policy", eff)
		}
		log.Info("gitops: dry-run; would decrypt/rotate/deploy stack", "revision", rev)
		l.recorder.SyncSuppressed("dry_run")
		return
	}

	// Decrypt sops-encrypted secret files in place (no-op when none; skipped when
	// no decrypter is wired). Discovery UNIONS file-backed secrets across every
	// DOCUMENT of every group — each resolved against ITS OWN file's directory,
	// because an override may live elsewhere than its base — so every secret
	// referenced anywhere in the stack is decrypted once, before any deploy.
	sopsFiles := st.SopsFiles
	if st.SopsSecretsDiscovery {
		discovered := make([]string, 0)
		for gi, g := range groups {
			for _, d := range g.docs {
				secretFiles, derr := compose.DiscoverSecretFiles(d.compose, filepath.Dir(d.path))
				if derr != nil {
					log.Error("gitops: secret discovery failed", "file", d.path, "group", gi, "err", derr)
					l.recorder.SyncError("secrets")
					stage, errMsg = "secrets", derr.Error()
					return
				}
				discovered = append(discovered, secretFiles...)
			}
		}
		sopsFiles = discovered
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

	// Deploy each merge group in order, with that group's effective pull policy
	// (file → stack → global). A group is ONE `docker stack deploy` carrying all
	// of its documents as separate -c flags; docker/cli merges them. Deploys
	// BETWEEN groups are ADDITIVE: Swarm does not prune services absent from a
	// later group, so the groups compose one stack. They are NOT transactional —
	// a mid-stack failure leaves earlier groups already applied (logged below).
	//
	// Seed the per-group deploy outcomes for the /stacks UI: each entry carries
	// its base file, its overrides and the effective pull policy up front; the
	// deploy loop below flips Status to ok/failed/skipped as it goes. files stays
	// nil if the stack never reached deploy this tick (a git/render/secrets
	// failure) — the stack-level ErrorStage already says deploy wasn't reached.
	files = make([]model.StackFileStatus, len(groups))
	for i, g := range groups {
		p, _ := effectivePullPolicy(g.spec, st.PullPolicy, l.pullPolicy)
		files[i] = model.StackFileStatus{File: g.spec.File, Overrides: g.spec.Overrides, PullPolicy: p}
	}
	// markFailedFrom records that group i failed and every later group was not
	// reached (sequential deploys stop at the first failure), for the /stacks UI.
	markFailedFrom := func(i int, because string) {
		files[i].Status = "failed"
		files[i].Error = because
		for j := i + 1; j < len(files); j++ {
			files[j].Status = "skipped"
		}
	}
	worktree := l.git.WorktreePath(st)
	log.Info("gitops: deploying stack", "revision", rev, "groups", len(groups), "files", totalFiles)
	for i, g := range groups {
		// Rotate file-backed configs/secrets by content hash for EACH document of
		// the group so Swarm picks up changes (no-op when autoRotate is disabled).
		// Per-document rotation is correct under docker/cli's merge: rotation only
		// sets `name` on the top-level configs:/secrets: object, so the rotated
		// name travels with the very object that carries the winning `file:` key.
		// Each document resolves its file paths against its OWN directory.
		if l.autoRotate {
			resolver := func(rel string) ([]byte, error) { return l.git.ReadFile(ctx, st, rel) }
			for _, d := range g.docs {
				n, rerr := compose.ApplyRotation(d.compose, st.Name, filepath.Dir(d.path), resolver)
				if rerr != nil {
					markFailedFrom(i, rerr.Error())
					log.Error("gitops: rotation failed", "file", d.path, "group", i, "err", rerr)
					l.recorder.SyncError("rotate")
					stage, errMsg = "rotate", rerr.Error()
					return
				}
				if n > 0 {
					log.Debug("gitops: rotated objects", "file", d.path, "group", i, "count", n)
				}
			}
		}

		effPolicy, source := effectivePullPolicy(g.spec, st.PullPolicy, l.pullPolicy)
		// Co-locate each temp compose with ITS OWN source file so that document's
		// relative configs:/secrets: paths resolve against the repo worktree, not
		// /tmp (see patch 2026-07-06-14.11). An override may live in a different
		// directory than the base, hence a directory per document.
		docs := make([]port.ComposeDoc, len(g.docs))
		for j, d := range g.docs {
			docs[j] = port.ComposeDoc{
				Map: d.compose,
				Dir: filepath.Join(worktree, filepath.Dir(d.path)),
			}
		}
		log.Debug("gitops: deploying merge group",
			"base", g.spec.File, "overrides", g.spec.Overrides, "files", len(docs),
			"group", i, "pull_policy", effPolicy, "source", source)
		if err := l.deployer.Deploy(ctx, st.Name, docs, port.DeployOpts{PullPolicy: effPolicy}); err != nil {
			markFailedFrom(i, err.Error())
			log.Warn("gitops: deploy failed mid-stack; earlier groups already applied (additive deploy, not rolled back)",
				"file", g.spec.File, "overrides", g.spec.Overrides, "applied_before", i, "err", err)
			l.recorder.SyncError("deploy")
			l.markDeploy(st.Name, rev, false)
			stage, errMsg = "deploy", err.Error()
			return
		}
		files[i].Status = "ok"
	}
	l.recorder.DeployApplied(st.Name)
	l.markDeploy(st.Name, rev, true)
	l.incDeploy(st.Name)
	deployed = true
	log.Debug("gitops: all groups deployed", "groups", len(groups), "files", totalFiles)
	log.Info("gitops: stack synced", "revision", rev, "groups", len(groups), "files", totalFiles)
}

// renderedDoc is ONE rendered compose document, paired with the repo-relative
// path it came from. The path is kept because rotation, secret discovery and the
// deploy temp-file directory must each resolve against the document's OWN
// directory — an override may live elsewhere than the base it overrides.
type renderedDoc struct {
	path    string
	compose map[string]any
}

// renderedGroup is one ComposeFileSpec rendered: the base document first, then
// its overrides, in `-c` order. It is deployed by a single `docker stack deploy`
// whose documents docker/cli merges last-wins.
type renderedGroup struct {
	spec model.ComposeFileSpec
	docs []renderedDoc
}

// maps returns the group's compose maps in `-c` order, for the transforms that
// need the merged view of the whole group (carry-forward, drift snapshot).
func (g renderedGroup) maps() []map[string]any {
	out := make([]map[string]any, len(g.docs))
	for i, d := range g.docs {
		out[i] = d.compose
	}
	return out
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
func (l *Loop) recordStatus(name, rev, stage, errMsg string, deployed bool, files []model.StackFileStatus) {
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
		Files:           files,
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

// effectivePullPolicy resolves the image pull policy for one compose file's
// deploy, with precedence: the per-file spec.PullPolicy → the stack-level
// st.PullPolicy → the global default (l.pullPolicy). It returns the resolved
// policy and a short source label ("per-file"|"stack"|"global") for logging.
// This is what makes a per-file pull split possible — e.g. a dev app file pulls
// "always" while a postgres file pulls "changed", via two sequential deploys.
func effectivePullPolicy(spec model.ComposeFileSpec, stackPolicy, def string) (policy, source string) {
	switch {
	case spec.PullPolicy != "":
		return spec.PullPolicy, "per-file"
	case stackPolicy != "":
		return stackPolicy, "stack"
	default:
		return def, "global"
	}
}

// desiredReplicasGroup returns the non-autoscaled, non-global service→replica
// snapshot of one MERGE GROUP, for drift detection, plus how many services were
// excluded because the merged view marks them autoscaler-owned.
//
// The snapshot must be computed over the merged view, not per document, because
// docker/cli merges a group last-wins and an override can change the verdict:
//   - an override that sets deploy.replicas must WIN over the base value;
//   - an override that adds swarm.autoscaler.enabled=true must EXCLUDE the
//     service (autoscaler-owned divergence is intentional, not drift) — and one
//     that sets it to false must include a service the base had excluded;
//   - an override that flips the service to mode: global must exclude it.
//
// A naive per-document union gets all three wrong. Services without an explicit
// replicas count anywhere in the group are skipped (unknown desired).
func desiredReplicasGroup(docs []map[string]any) (desired map[string]uint64, excludedAutoscaled int) {
	merged := mergeGroupServices(docs)
	out := make(map[string]uint64, len(merged))
	for name, ms := range merged {
		if ms.global {
			continue
		}
		if ms.labels[config.LabelEnabled] == "true" {
			excludedAutoscaled++ // autoscaler-owned: the HPA controls its replicas
			continue
		}
		if !ms.hasReplicas {
			continue
		}
		out[name] = ms.replicas
	}
	if len(out) == 0 {
		return nil, excludedAutoscaled
	}
	return out, excludedAutoscaled
}

// mergedGroupService is the effective view of one service across a merge group,
// following docker/cli's last-wins merge. Mirrors the detection in
// adapter/stackdeploy/carryforward.go (kept private per package; not shared
// across the app→adapter boundary).
type mergedGroupService struct {
	labels      map[string]string
	global      bool
	replicas    uint64
	hasReplicas bool
}

// mergeGroupServices folds a group's documents into the per-service view above.
// Documents without a services map contribute nothing; a key a document omits
// leaves the previous document's value in place.
func mergeGroupServices(docs []map[string]any) map[string]*mergedGroupService {
	merged := make(map[string]*mergedGroupService)
	for _, doc := range docs {
		services, ok := doc["services"].(map[string]any)
		if !ok {
			continue
		}
		for name, raw := range services {
			svc, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			ms, seen := merged[name]
			if !seen {
				ms = &mergedGroupService{labels: map[string]string{}}
				merged[name] = ms
			}
			for k, v := range flatServiceLabels(svc) {
				ms.labels[k] = v
			}
			if mode, ok := explicitServiceMode(svc); ok {
				ms.global = mode == "global"
			}
			if r, ok := replicasOf(svc); ok {
				ms.replicas, ms.hasReplicas = r, true
			}
		}
	}
	return merged
}

// explicitServiceMode returns a compose service's declared mode (deploy.mode
// takes precedence over the top-level mode) and whether one was declared at all.
// Only an explicit mode overrides what an earlier document of the group set.
func explicitServiceMode(svc map[string]any) (string, bool) {
	if d, ok := svc["deploy"].(map[string]any); ok {
		if m, ok := d["mode"].(string); ok && m != "" {
			return m, true
		}
	}
	if m, ok := svc["mode"].(string); ok && m != "" {
		return m, true
	}
	return "", false
}

// flatServiceLabels flattens a compose service's deploy.labels and top-level
// labels into one map, handling both the map form (key: value) and the list form
// (- "key=value"). Deploy labels take precedence over service-level labels.
func flatServiceLabels(svc map[string]any) map[string]string {
	out := map[string]string{}
	collect := func(labels any) {
		switch t := labels.(type) {
		case map[string]any:
			for k, v := range t {
				out[k] = fmt.Sprint(v)
			}
		case []any:
			for _, item := range t {
				s := fmt.Sprint(item)
				if k, v, ok := strings.Cut(s, "="); ok {
					out[k] = v
				} else {
					out[s] = "true"
				}
			}
		}
	}
	collect(svc["labels"])
	if d, ok := svc["deploy"].(map[string]any); ok {
		collect(d["labels"]) // deploy.labels win over service-level labels
	}
	return out
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
