// Package sops implements port.SecretDecrypter over the sops/v3 library. It
// decrypts sops-encrypted files IN PLACE (parity with swarm-cd's util.DecryptFile):
// age/gpg backends are selected by the sops library from env (SOPS_AGE_KEY_FILE,
// SOPS_GPG_PRIVATE_KEY_FILE, SOPS_GPG_PRIVATE_KEY) — this package never reads
// those envs itself.
//
// SECURITY: Decrypt overwrites <worktree>/<file> with PLAINTEXT on disk. The
// worktree is an ephemeral repo clone under repos_path; keep repos_path on
// ephemeral storage. Never log decrypted contents (DEBUG logs file name + size
// only).
package sops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	sopsdecrypt "github.com/getsops/sops/v3/decrypt"

	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// DecryptFunc is the seam over sops decrypt.File (the format is derived from the
// file extension by the caller). The production default is sops decrypt.File;
// tests inject a fake so the adapter is unit-testable without real sops crypto or
// keys.
type DecryptFunc func(path, format string) ([]byte, error)

// Adapter implements port.SecretDecrypter.
type Adapter struct {
	decrypt DecryptFunc
	logger  *slog.Logger
}

// compile-time proof the adapter satisfies the core port.
var _ port.SecretDecrypter = (*Adapter)(nil)

// New builds an Adapter backed by the real sops decrypt.File. A nil logger falls
// back to slog.Default.
func New(logger *slog.Logger) *Adapter {
	return NewWithDecrypter(sopsdecrypt.File, logger)
}

// NewWithDecrypter builds an Adapter with an injected DecryptFunc (for tests).
func NewWithDecrypter(fn DecryptFunc, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{decrypt: fn, logger: logger}
}

// Decrypt decrypts each file in place at <worktree>/<file>, overwriting it with
// plaintext. It returns on the first failure (the caller then skips the deploy).
// files are repo-worktree-relative paths.
func (a *Adapter) Decrypt(_ context.Context, worktree string, files []string) error {
	for _, rel := range files {
		path := filepath.Join(worktree, rel)
		cleartext, err := a.decrypt(path, formatFor(rel))
		if err != nil {
			a.logger.Warn("sops: decrypt failed", "file", rel, "err", err)
			return fmt.Errorf("sops: decrypt %q: %w", rel, err)
		}
		if err := os.WriteFile(path, cleartext, 0o600); err != nil {
			return fmt.Errorf("sops: write plaintext %q: %w", rel, err)
		}
		a.logger.Debug("sops: decrypted file", "file", rel, "bytes", len(cleartext))
	}
	return nil
}

// formatFor maps a file extension to the sops format string (mirrors swarm-cd's
// getFileFormat). Unknown extensions use "binary".
func formatFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".ini":
		return "ini"
	case ".env":
		return "dotenv"
	default:
		return "binary"
	}
}
