package port

import (
	"context"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// GitSource is the core's window onto the Git repositories backing stacks. The
// git adapter (go-git) implements it; the core never imports a git library.
type GitSource interface {
	// Sync fast-forwards the stack's repo to its configured branch and returns
	// the new revision (short hash). Concurrent stacks that share a repo are
	// serialized inside the adapter by repo, never by stack.
	Sync(ctx context.Context, stack model.StackConfig) (revision string, err error)

	// ReadFile returns the bytes of a path relative to the repo worktree root
	// (a compose file or a values file). relPath is interpreted relative to the
	// repo root, matching the compose_file / values_file in the stack config.
	ReadFile(ctx context.Context, stack model.StackConfig, relPath string) ([]byte, error)

	// WorktreePath returns the on-disk root of the stack's repo worktree
	// (repos_path/<repo>), for callers that need real file paths (e.g. in-place
	// sops decrypt).
	WorktreePath(stack model.StackConfig) string
}

// StackRenderer turns raw compose bytes — optionally a Go text/template rendered
// against a values file — into a generic compose map ready for deploy. It is pure:
// no I/O, both inputs are passed in (values may be nil to skip templating).
type StackRenderer interface {
	Render(compose, values []byte) (map[string]any, error)
}

// StackStateReader returns the live Swarm services of a stack. The deploy adapter
// uses it for carry-forward (autoscaler-aware deploys). Implemented by the swarm
// adapter alongside SwarmController.
type StackStateReader interface {
	StackServices(ctx context.Context, stack string) ([]model.StackService, error)
}

// ComposeDoc is ONE rendered compose document of a merge group — one `-c` flag
// of the resulting `docker stack deploy`.
//
// Dir is the on-disk directory of the document's OWN source compose file. The
// temp compose the deployer writes is placed there so the relative
// configs:/secrets: file paths inside that document resolve against the same
// directory they resolve against for its source file (NOT against the OS temp
// dir, which lives outside the repo worktree — see patch 2026-07-06-14.11).
// Because an override may live in a different directory than the base, this is
// per DOCUMENT and not per deploy. Empty falls back to the OS temp dir — the
// historical behavior, used by tests and any caller that does not care about
// relative file paths.
type ComposeDoc struct {
	Map map[string]any
	Dir string
}

// DeployOpts carries per-deploy knobs. PullPolicy is the --resolve-image mode:
// "always" (re-resolve every deploy) or "changed" (only when the digest changed).
// It is per DEPLOY, i.e. per merge group: `docker stack deploy` accepts exactly
// one --resolve-image no matter how many -c flags it is given.
type DeployOpts struct {
	PullPolicy string
}

// StackDeployer applies one rendered merge group to Swarm as a SINGLE
// `docker stack deploy` with one -c per document: docs[0] is the base compose
// file and docs[1:] are its overrides, in declaration order. docker/cli performs
// the merge, later documents winning per key — the daemon never merges compose
// itself.
//
// Implementations MUST be autoscaler-aware: the replicas of any service opted in
// via swarm.autoscaler.enabled=true are carried forward from live state (clamped
// to [min,max]) and never overwritten from the compose file — that is what
// dissolves the swarm-cd↔HPA replicas conflict. For a multi-document group that
// detection MUST run against the MERGED view of the group, not document by
// document: the autoscaler labels may appear only in the base file while an
// override re-declares the service, or only in an override. Applying
// carry-forward per document would miss both cases and let a deploy clobber a
// replica count the autoscaler just set.
type StackDeployer interface {
	Deploy(ctx context.Context, name string, docs []ComposeDoc, opts DeployOpts) error
}

// SecretDecrypter decrypts sops-encrypted files IN PLACE (overwrites the file at
// <worktree>/<file> with plaintext). Implemented by adapter/sops over the sops
// library, which selects the age/gpg backend from env (SOPS_AGE_KEY_FILE,
// SOPS_GPG_PRIVATE_KEY_FILE, SOPS_GPG_PRIVATE_KEY). Decrypt is a disk side
// effect that writes plaintext to the worktree — callers MUST skip it in dry-run
// and MUST NOT log decrypted contents.
type SecretDecrypter interface {
	Decrypt(ctx context.Context, worktree string, files []string) error
}
