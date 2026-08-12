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
	"path/filepath"

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
// docker/cli's merge. Each temp file is written into its own document's Dir
// (co-location, see patch 2026-07-06-14.11): the BASE temp file's directory is
// docker/cli's anchor for resolving relative configs:/secrets: file: paths, and
// every document's file: is rebased relative to that base directory before
// marshal (see rebaseFileObjects) so an override living in a different directory
// than its base still resolves correctly — independent of -c ordering and of
// whether GitOpsReposPath is relative or absolute. See issues #20, #22.
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

// rebaseFileObjects rewrites each relative configs:/secrets: `file:` path in the
// compose document so it is relative to baseDir — the directory of the BASE
// (first) compose file of the merge group — and returns the number of paths
// rewritten (for DEBUG logging).
//
// docker stack deploy -c base -c override resolves ALL relative configs:/
// secrets: file: paths against the FIRST -c file's directory, regardless of which
// document declared them. Rather than make paths absolute (which re-ties
// resolution to the process CWD and to whether GitOpsReposPath is relative or
// absolute), we rebase every document's file: to be relative to the base
// directory: docker then resolves it against the base temp compose's directory
// (== baseDir, since the base temp compose is co-located with its source) and
// finds the file. Because filepath.Rel cancels the common worktree prefix shared
// by every document in a group (one stack -> one repo -> one worktree), the
// rebased path is identical whether GitOpsReposPath is relative ("repos", the
// default) or absolute — so this fixes both the cross-directory override case
// (#20) and the relative-GitOpsReposPath regression that the prior absolutize
// approach introduced (#22). The base document (docDir == baseDir) is the
// identity case — Rel(baseDir, Join(baseDir, f)) == f — so its paths are left
// untouched, matching how rotate (core/compose.ApplyRotation) and
// compose.DiscoverSecretFiles resolve each document against its own directory.
//
// Already-absolute paths (docker opens them directly), non-file objects (e.g.
// external: true), non-map objects, empty file: values, and a missing configs:/
// secrets: section are left untouched. When docDir or baseDir is empty (the
// OS-temp fallback used by tests and callers that don't carry relative paths),
// relative paths are left as-is: there is no anchor to rebase against, so the
// historical behavior is preserved.
func rebaseFileObjects(compose map[string]any, docDir, baseDir string, log *slog.Logger) int {
	if docDir == "" || baseDir == "" {
		return 0
	}
	rewritten := 0
	for _, section := range []string{"configs", "secrets"} {
		objects, ok := compose[section].(map[string]any)
		if !ok {
			continue
		}
		for _, raw := range objects {
			obj, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			f, ok := obj["file"].(string)
			if !ok || f == "" || filepath.IsAbs(f) {
				continue
			}
			actual := filepath.Join(docDir, f)
			rel, err := filepath.Rel(baseDir, actual)
			if err != nil {
				// Cannot happen within one worktree (all doc.Dirs share its
				// prefix); leave the path unchanged as a defensive fallback so a
				// future cross-volume case is diagnosable rather than fatal.
				log.Debug("stackdeploy: cannot rebase configs/secrets file path; leaving unchanged",
					"source_dir", docDir, "base_dir", baseDir, "file", f, "err", err)
				continue
			}
			obj["file"] = rel
			rewritten++
		}
	}
	return rewritten
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
	// The base (first) document's directory is docker/cli's resolution anchor:
	// `docker stack deploy -c base -c override` resolves ALL relative configs:/
	// secrets: file: paths against the FIRST -c file's directory. Rebase every
	// document's file: relative to it so docker finds each file regardless of -c
	// ordering and regardless of whether GitOpsReposPath is relative or absolute.
	// See rebaseFileObjects (issues #20, #22).
	baseDir := ""
	if len(docs) > 0 {
		baseDir = docs[0].Dir
	}
	for i, doc := range docs {
		if doc.Dir != "" {
			log.Debug("stackdeploy: writing temp compose next to source compose (the base temp file's dir is docker's anchor for relative configs:/secrets: paths)",
				"index", i, "source_dir", doc.Dir)
		}
		// Rebase each document's configs:/secrets: file: paths relative to the
		// base directory BEFORE marshal. docker stack deploy -c base -c override
		// resolves ALL relative file: paths against the FIRST -c file's
		// directory, so co-locating each temp next to its own source is not enough
		// when an override lives in a different directory than the base: a path
		// expressed relative to the base dir resolves correctly regardless of -c
		// ordering, and filepath.Rel cancels the worktree prefix so it is correct
		// whether GitOpsReposPath is relative or absolute. See issue #20, #22.
		if n := rebaseFileObjects(doc.Map, doc.Dir, baseDir, log); n > 0 {
			log.Debug("stackdeploy: rebased configs/secrets file paths relative to base compose dir",
				"index", i, "source_dir", doc.Dir, "base_dir", baseDir, "rewritten", n)
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
// original compose file. `docker stack deploy` resolves a compose's relative
// configs:/secrets: file: paths relative to the compose file it is given, so a
// temp file written to the OS temp dir (/tmp) breaks relative paths (they would
// be resolved under /tmp, where the referenced files do not exist). For a merge
// group only the FIRST -c file's directory is docker's anchor; the base temp
// file (written here into docs[0].Dir) therefore anchors resolution, and every
// document's file: is rebased relative to it before marshal (see
// rebaseFileObjects). Co-locating override temp files is retained as a safety
// net for other relative path types docker resolves (build:, env_file:). An
// empty dir falls back to the OS temp dir.
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
