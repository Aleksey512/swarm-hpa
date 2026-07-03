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

// DeployOpts carries per-deploy knobs. PullPolicy is the --resolve-image mode:
// "always" (re-resolve every deploy) or "changed" (only when the digest changed).
type DeployOpts struct {
	PullPolicy string
}

// StackDeployer applies one rendered stack to Swarm. Implementations MUST be
// autoscaler-aware: the replicas of any service opted in via
// swarm.autoscaler.enabled=true are carried forward from live state (clamped to
// [min,max]) and never overwritten from the compose file — that is what dissolves
// the swarm-cd↔HPA replicas conflict.
type StackDeployer interface {
	Deploy(ctx context.Context, name string, compose map[string]any, opts DeployOpts) error
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
