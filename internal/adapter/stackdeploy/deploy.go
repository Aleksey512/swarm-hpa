// Package stackdeploy implements port.StackDeployer. It is autoscaler-aware: a
// deploy carries forward the live replica count of any swarm.autoscaler.enabled
// service (clamped to [min,max]) before running `docker stack deploy`, so the
// GitOps reapply never clobbers a count the autoscaler just set. The carry-forward
// lives in carryforward.go, isolated so a future native granular deploy can drop it.
package stackdeploy

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/goccy/go-yaml"

	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// DeployFunc is the seam over `docker stack deploy`. The production implementation
// (DockerCLIDeploy) runs the docker/cli cobra command; tests inject a recorder so
// a deploy never touches a real daemon.
//
// composeFiles is one merge group in `-c` order: the base compose file first,
// then its overrides. The order is load-bearing — docker/cli merges later files
// over earlier ones.
type DeployFunc func(ctx context.Context, name string, composeFiles []string, pullPolicy string) error

// Deployer implements port.StackDeployer: carry-forward, then deploy.
type Deployer struct {
	state  port.StackStateReader
	deploy DeployFunc
	logger *slog.Logger
}

// compile-time proof the deployer satisfies the core port.
var _ port.StackDeployer = (*Deployer)(nil)

// New builds a Deployer. deploy is the `docker stack deploy` seam.
func New(state port.StackStateReader, deploy DeployFunc, logger *slog.Logger) *Deployer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Deployer{state: state, deploy: deploy, logger: logger}
}

// Deploy carries autoscaled replicas forward from live Swarm state, writes one
// temp compose per document of the merge group, and runs a SINGLE
// `docker stack deploy` with one -c per document via DeployFunc.
//
// docs[0] is the base compose file and docs[1:] are its overrides; the order is
// preserved all the way to the -c flags because it decides which value wins
// docker/cli's merge. Each temp file is written into its own document's Dir so
// that document's relative configs:/secrets: paths resolve exactly as they do for
// its source file (see patch 2026-07-06-14.11) — an override may live in a
// different directory than the base.
func (d *Deployer) Deploy(ctx context.Context, name string, docs []port.ComposeDoc, opts port.DeployOpts) error {
	log := d.logger.With(slog.String("stack", name))
	if len(docs) == 0 {
		return fmt.Errorf("stackdeploy: deploy %q: no compose documents", name)
	}

	live, err := d.state.StackServices(ctx, name)
	if err != nil {
		return fmt.Errorf("stackdeploy: read live services for %q: %w", name, err)
	}
	maps := make([]map[string]any, len(docs))
	for i, doc := range docs {
		maps[i] = doc.Map
	}
	// Carry-forward runs over the whole group: the merged view decides which
	// services are autoscaler-owned, and every document declaring one is rewritten
	// so the merge cannot resurrect a compose replica count.
	changed, err := ApplyCarryForwardGroup(maps, live, log)
	if err != nil {
		return fmt.Errorf("stackdeploy: carry-forward for %q: %w", name, err)
	}
	log.Info("stackdeploy: carry-forward applied",
		"autoscaled_services", changed, "live_services", len(live), "docs", len(docs))

	files, cleanup, err := writeTempComposeGroup(name, docs, log)
	defer cleanup()
	if err != nil {
		return err
	}

	pullPolicy := opts.PullPolicy
	if pullPolicy == "" {
		pullPolicy = "changed"
	}
	log.Info("stackdeploy: deploying", "pull_policy", pullPolicy, "files", len(files), "compose_files", files)
	if err := d.deploy(ctx, name, files, pullPolicy); err != nil {
		return fmt.Errorf("stackdeploy: deploy %q: %w", name, err)
	}
	log.Info("stackdeploy: deployed", "files", len(files))
	return nil
}

// writeTempComposeGroup materializes every document of a merge group as a temp
// compose file, in `-c` order. The returned cleanup removes every file that was
// created — including on a partial failure, so a mid-group write error never
// leaks temp files into the repo worktree. cleanup is always safe to call.
func writeTempComposeGroup(name string, docs []port.ComposeDoc, log *slog.Logger) (files []string, cleanup func(), err error) {
	files = make([]string, 0, len(docs))
	cleanup = func() {
		for _, f := range files {
			if rmErr := os.Remove(f); rmErr != nil && !os.IsNotExist(rmErr) {
				log.Debug("stackdeploy: temp compose cleanup failed", "path", f, "err", rmErr)
			}
		}
	}
	for i, doc := range docs {
		if doc.Dir != "" {
			log.Debug("stackdeploy: writing temp compose next to source compose (relative configs:/secrets: paths resolve against the same dir, not /tmp)",
				"index", i, "source_dir", doc.Dir)
		}
		tmp, werr := writeTempCompose(name, doc.Dir, doc.Map)
		if werr != nil {
			return files, cleanup, werr
		}
		files = append(files, tmp)
		log.Debug("stackdeploy: temp compose written", "index", i, "source_dir", doc.Dir, "path", tmp)
	}
	return files, cleanup, nil
}

// writeTempCompose marshals the (carry-forward-adjusted) compose map to a temp
// file and returns its path. The caller removes it.
//
// When dir is non-empty, the temp file is written there — i.e. next to the
// original compose file — so that relative configs:/secrets: file paths inside
// the compose resolve against the original compose's directory. `docker stack
// deploy` resolves such paths relative to the compose file it is handed, so a
// temp file written to the OS temp dir (/tmp) breaks relative paths (they would
// be resolved under /tmp, where the referenced files do not exist). An empty dir
// falls back to the OS temp dir.
func writeTempCompose(name, dir string, compose map[string]any) (string, error) {
	b, err := yaml.Marshal(compose)
	if err != nil {
		return "", fmt.Errorf("stackdeploy: marshal compose for %q: %w", name, err)
	}
	f, err := os.CreateTemp(dir, "swarm-hpa-stack-"+sanitize(name)+"-*.yaml")
	if err != nil {
		return "", fmt.Errorf("stackdeploy: temp compose file: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("stackdeploy: write temp compose: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("stackdeploy: close temp compose: %w", err)
	}
	return f.Name(), nil
}

// sanitize replaces any character illegal in a CreateTemp pattern stem with '-'.
func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_' || c == '-' || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}
