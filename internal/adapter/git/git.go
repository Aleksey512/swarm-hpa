// Package git implements port.GitSource with go-git. It clones/opens the
// repositories that back stacks, fast-forwards a stack's configured branch, and
// exposes worktree files (compose + values). It mirrors swarm-cd's mechanics for
// drop-in parity: HTTP basic auth (password or password_file, nil for public
// repos) and per-repo serialization so stacks sharing a repo never pull it
// concurrently. No SSH in the foundation slice.
package git

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// Adapter implements port.GitSource over on-disk go-git repositories.
type Adapter struct {
	reposPath string
	repos     map[string]model.RepoConfig
	logger    *slog.Logger

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per-repo locks, lazily allocated
}

// compile-time proof the adapter satisfies the core port.
var _ port.GitSource = (*Adapter)(nil)

// New builds a git adapter. reposPath is the root under which each repo is
// cloned at <reposPath>/<repoName>. A nil logger falls back to slog.Default.
func New(reposPath string, repos map[string]model.RepoConfig, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{
		reposPath: reposPath,
		repos:     repos,
		logger:    logger,
		locks:     make(map[string]*sync.Mutex),
	}
}

// Sync fast-forwards the stack's repo to its configured branch and returns the
// short revision hash. Concurrent Sync calls for stacks sharing a repo are
// serialized per repo.
func (a *Adapter) Sync(ctx context.Context, stack model.StackConfig) (string, error) {
	log := a.logger.With(slog.String("stack", stack.Name), slog.String("repo", stack.Repo), slog.String("branch", stack.Branch))

	repoCfg, ok := a.repos[stack.Repo]
	if !ok {
		return "", fmt.Errorf("git: stack %q references unknown repo %q", stack.Name, stack.Repo)
	}

	// Serialize per repo: one repo can back several stacks; never pull it twice
	// at once. Stacks on different repos proceed concurrently.
	lock := a.repoLock(stack.Repo)
	lock.Lock()
	defer lock.Unlock()

	auth, err := basicAuth(repoCfg)
	if err != nil {
		log.Warn("git auth misconfigured", "err", err)
		return "", fmt.Errorf("git: repo %q auth: %w", stack.Repo, err)
	}

	repo, err := a.openOrClone(ctx, stack.Repo, repoCfg, stack.Branch, auth, log)
	if err != nil {
		return "", err
	}

	// Fetch the configured branch into refs/remotes/origin/<branch>.
	refSpec := fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", stack.Branch, stack.Branch)
	if err := repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec(refSpec)},
		Auth:       auth,
	}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return "", mapAuthErr(fmt.Errorf("git: fetch origin/%s for %q: %w", stack.Branch, stack.Name, err))
	}

	// Move the worktree onto the fetched commit. Force recovers from any local
	// state (e.g. another stack on a different branch of the same repo).
	wt, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("git: worktree for %q: %w", stack.Name, err)
	}
	remoteRef := plumbing.NewRemoteReferenceName("origin", stack.Branch)
	if err := wt.Checkout(&git.CheckoutOptions{Branch: remoteRef, Force: true}); err != nil {
		return "", fmt.Errorf("git: checkout origin/%s for %q: %w", stack.Branch, stack.Name, err)
	}

	ref, err := repo.Reference(remoteRef, true)
	if err != nil {
		return "", fmt.Errorf("git: resolve origin/%s for %q: %w", stack.Branch, stack.Name, err)
	}
	revision := ref.Hash().String()[:8]
	log.Info("git synced", "revision", revision)
	log.Debug("git sync complete", "worktree", filepath.Join(a.reposPath, stack.Repo))
	return revision, nil
}

// ReadFile returns the bytes of relPath from the repo's worktree. Sync must have
// been called first so the repo is cloned and on the right branch.
func (a *Adapter) ReadFile(ctx context.Context, stack model.StackConfig, relPath string) ([]byte, error) {
	full := filepath.Join(a.reposPath, stack.Repo, relPath)
	b, err := os.ReadFile(full) //nolint:gosec // G304: full is an admin-controlled repo worktree path
	if err != nil {
		return nil, fmt.Errorf("git: read %q for stack %q: %w", relPath, stack.Name, err)
	}
	a.logger.Debug("git read file", "stack", stack.Name, "path", relPath, "bytes", len(b))
	return b, nil
}

// openOrClone returns a go-git repository for the repo, cloning it on first use.
// The clone is scoped to the configured branch (single-branch, with origin set)
// so later Fetch calls resolve refs/remotes/origin/<branch>.
func (a *Adapter) openOrClone(ctx context.Context, repoName string, cfg model.RepoConfig, branch string, auth *http.BasicAuth, log *slog.Logger) (*git.Repository, error) {
	path := filepath.Join(a.reposPath, repoName)
	if _, err := os.Stat(path); err == nil {
		log.Debug("git open existing repo", "path", path)
		return git.PlainOpen(path)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("git: stat repo %q: %w", repoName, err)
	}

	log.Info("git cloning repo", "url", cfg.URL, "path", path)
	if err := os.MkdirAll(a.reposPath, 0o750); err != nil {
		return nil, fmt.Errorf("git: mkdir repos path: %w", err)
	}
	repo, err := git.PlainCloneContext(ctx, path, false, &git.CloneOptions{
		URL:           cfg.URL,
		Auth:          auth,
		ReferenceName: plumbing.NewBranchReferenceName(branch),
		SingleBranch:  true,
	})
	if err != nil {
		return nil, mapAuthErr(fmt.Errorf("git: clone %q (branch %s) for %q: %w", cfg.URL, branch, repoName, err))
	}
	return repo, nil
}

// repoLock returns the per-repo mutex, allocating one on first use.
func (a *Adapter) repoLock(name string) *sync.Mutex {
	a.mu.Lock()
	defer a.mu.Unlock()
	lk, ok := a.locks[name]
	if !ok {
		lk = &sync.Mutex{}
		a.locks[name] = lk
	}
	return lk
}

// basicAuth builds HTTP basic auth from a repo config. It returns nil auth for
// public repos (no username and no password); otherwise a username is required.
func basicAuth(cfg model.RepoConfig) (*http.BasicAuth, error) {
	user := cfg.Username
	pass := cfg.Password
	if pass == "" && cfg.PasswordFile != "" {
		b, err := os.ReadFile(cfg.PasswordFile) //nolint:gosec // G304: PasswordFile is an admin-controlled path from repos.yaml
		if err != nil {
			return nil, fmt.Errorf("read password file %q: %w", cfg.PasswordFile, err)
		}
		pass = strings.TrimSpace(string(b))
	}
	if user == "" && pass == "" {
		return nil, nil // public repo
	}
	if user == "" {
		return nil, errors.New("username required when a password is set")
	}
	return &http.BasicAuth{Username: user, Password: pass}, nil
}

// mapAuthErr rewrites go-git's generic "authentication required" into a clearer
// "authentication failed" so misconfigured credentials are obvious. Mirrors
// swarm-cd's behavior.
func mapAuthErr(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "authentication required") {
		return fmt.Errorf("authentication failed: %w", err)
	}
	return err
}
