package git

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"log/slog"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// seedRepo creates a local repository at a temp dir with HEAD on branch, a single
// initial commit adding compose.yaml. The returned path is used as the clone URL.
func seedRepo(t *testing.T, branch, composeContent string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	// Point HEAD at the desired branch before the first commit lands it there.
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(branch))); err != nil {
		t.Fatalf("set HEAD: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(composeContent), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add("compose.yaml"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("init", commitOpts()); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return dir
}

func commitOpts() *git.CommitOptions {
	return &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@example.com", When: time.Now()}}
}

func commitTo(t *testing.T, remote, content string) {
	t.Helper()
	repo, err := git.PlainOpen(remote)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remote, "compose.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add("compose.yaml"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("update", commitOpts()); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestSync_ClonesAndReads(t *testing.T) {
	remote := seedRepo(t, "main", "services:\n  web:\n    image: nginx\n")
	a := New(t.TempDir(), map[string]model.RepoConfig{"r": {URL: remote}}, testLogger())
	stack := model.StackConfig{Name: "s", Repo: "r", Branch: "main", ComposeFile: "compose.yaml"}

	rev, err := a.Sync(context.Background(), stack)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(rev) != 8 {
		t.Fatalf("revision length = %d, want 8 (%q)", len(rev), rev)
	}

	got, err := a.ReadFile(context.Background(), stack, "compose.yaml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "services:\n  web:\n    image: nginx\n" {
		t.Fatalf("ReadFile body = %q", string(got))
	}
}

func TestSync_AlreadyUpToDate(t *testing.T) {
	remote := seedRepo(t, "main", "v1\n")
	a := New(t.TempDir(), map[string]model.RepoConfig{"r": {URL: remote}}, testLogger())
	stack := model.StackConfig{Name: "s", Repo: "r", Branch: "main"}

	r1, err := a.Sync(context.Background(), stack)
	if err != nil {
		t.Fatalf("Sync #1: %v", err)
	}
	r2, err := a.Sync(context.Background(), stack)
	if err != nil {
		t.Fatalf("Sync #2: %v", err)
	}
	if r1 != r2 {
		t.Fatalf("expected same revision on no-op sync, got %q then %q", r1, r2)
	}
}

func TestSync_FetchesNewCommit(t *testing.T) {
	remote := seedRepo(t, "main", "v1\n")
	a := New(t.TempDir(), map[string]model.RepoConfig{"r": {URL: remote}}, testLogger())
	stack := model.StackConfig{Name: "s", Repo: "r", Branch: "main"}

	r1, err := a.Sync(context.Background(), stack)
	if err != nil {
		t.Fatalf("Sync #1: %v", err)
	}
	commitTo(t, remote, "v2\n")
	r2, err := a.Sync(context.Background(), stack)
	if err != nil {
		t.Fatalf("Sync #2: %v", err)
	}
	if r1 == r2 {
		t.Fatalf("expected new revision after remote commit, got %q twice", r1)
	}
	got, err := a.ReadFile(context.Background(), stack, "compose.yaml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "v2\n" {
		t.Fatalf("worktree not updated to new commit; got %q", string(got))
	}
}

func TestSync_UnknownRepo(t *testing.T) {
	a := New(t.TempDir(), map[string]model.RepoConfig{}, testLogger())
	_, err := a.Sync(context.Background(), model.StackConfig{Name: "s", Repo: "missing", Branch: "main"})
	if err == nil || !strings.Contains(err.Error(), "unknown repo") {
		t.Fatalf("expected unknown-repo error, got %v", err)
	}
}

func TestSync_PerRepoLockSerializes(t *testing.T) {
	remote := seedRepo(t, "main", "x\n")
	a := New(t.TempDir(), map[string]model.RepoConfig{"r": {URL: remote}}, testLogger())
	stack := model.StackConfig{Name: "s", Repo: "r", Branch: "main"}

	done := make(chan string, 4)
	for range 4 {
		go func() {
			rev, err := a.Sync(context.Background(), stack)
			if err != nil {
				t.Errorf("Sync: %v", err)
				done <- ""
				return
			}
			done <- rev
		}()
	}
	for range 4 {
		select {
		case rev := <-done:
			if len(rev) != 8 {
				t.Errorf("goroutine returned bad revision %q", rev)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timeout: per-repo lock deadlocked or hung")
		}
	}
}

func TestBasicAuth(t *testing.T) {
	pwFile := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pwFile, []byte("  filepass\n"), 0o600); err != nil {
		t.Fatalf("write pw file: %v", err)
	}
	cases := []struct {
		name string
		cfg  model.RepoConfig
		want *http.BasicAuth
		err  string
	}{
		{"public", model.RepoConfig{}, nil, ""},
		{"password", model.RepoConfig{Username: "u", Password: "p"}, &http.BasicAuth{Username: "u", Password: "p"}, ""},
		{"password_file", model.RepoConfig{Username: "u", PasswordFile: pwFile}, &http.BasicAuth{Username: "u", Password: "filepass"}, ""},
		{"missing_username", model.RepoConfig{Password: "p"}, nil, "username required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := basicAuth(tc.cfg)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want error containing %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.want == nil {
				if got != nil {
					t.Fatalf("public repo: want nil auth, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("want auth %+v, got nil", tc.want)
			}
			if got.Username != tc.want.Username || got.Password != tc.want.Password {
				t.Fatalf("auth = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestMapAuthErr(t *testing.T) {
	if err := mapAuthErr(errStr("authorization failed: authentication required")); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("want remapped 'authentication failed', got %v", err)
	}
	orig := errStr("some other error")
	if err := mapAuthErr(orig); err.Error() != "some other error" {
		t.Fatalf("unrelated error should pass through, got %v", err)
	}
	if err := mapAuthErr(nil); err != nil {
		t.Fatalf("nil should stay nil, got %v", err)
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }
