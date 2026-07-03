package stackdeploy

import (
	"errors"
	"path/filepath"
	"testing"
)

// contentResolver maps repo-relative paths to bytes (stands in for git.ReadFile
// over the decrypted worktree).
func contentResolver(files map[string][]byte) func(string) ([]byte, error) {
	return func(rel string) ([]byte, error) {
		if b, ok := files[rel]; ok {
			return b, nil
		}
		return nil, errors.New("not found: " + rel)
	}
}

func TestDiscoverSecretFiles(t *testing.T) {
	compose := map[string]any{"secrets": map[string]any{
		"tls": map[string]any{"file": "secrets/tls.crt"},
		"key": map[string]any{"file": "secrets/tls.key"},
		"ext": map[string]any{"external": true}, // no file → skipped
	}}
	// compose at "stack/compose.yaml" → dir "stack"
	got, err := DiscoverSecretFiles(compose, "stack")
	if err != nil {
		t.Fatalf("DiscoverSecretFiles: %v", err)
	}
	want := map[string]bool{
		filepath.Join("stack", "secrets/tls.crt"): true,
		filepath.Join("stack", "secrets/tls.key"): true,
	}
	if len(got) != 2 {
		t.Fatalf("got %d files %v, want 2", len(got), got)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected discovered path %q", p)
		}
	}
	// No secrets section → empty, no error.
	got, err = DiscoverSecretFiles(map[string]any{"services": map[string]any{}}, "stack")
	if err != nil || len(got) != 0 {
		t.Fatalf("no-secrets: got=%v err=%v", got, err)
	}
}

func TestApplyRotation_RenamesByContentHash(t *testing.T) {
	compose := map[string]any{
		"configs": map[string]any{
			"app": map[string]any{"file": "cfg/app.conf"},
		},
		"secrets": map[string]any{
			"tls": map[string]any{"file": "secrets/tls.crt"},
		},
	}
	files := contentResolver(map[string][]byte{
		filepath.Join("stack", "cfg/app.conf"):    []byte("config-content"),
		filepath.Join("stack", "secrets/tls.crt"): []byte("secret-content"),
	})

	changed, err := ApplyRotation(compose, "web", "stack", files)
	if err != nil {
		t.Fatalf("ApplyRotation: %v", err)
	}
	if changed != 2 {
		t.Fatalf("changed = %d, want 2", changed)
	}
	cfg := compose["configs"].(map[string]any)["app"].(map[string]any)
	if cfg["name"] != "web-app-a6b8b0b7" {
		t.Errorf("config name = %v, want web-app-a6b8b0b7", cfg["name"])
	}
	sec := compose["secrets"].(map[string]any)["tls"].(map[string]any)
	if sec["name"] != "web-tls-14a94c37" {
		t.Errorf("secret name = %v, want web-tls-14a94c37", sec["name"])
	}
}

func TestApplyRotation_StableAndChangedHash(t *testing.T) {
	mk := func(content string) map[string]any {
		return map[string]any{"configs": map[string]any{"app": map[string]any{"file": "app.conf"}}}
	}
	files := contentResolver(map[string][]byte{filepath.Join("", "app.conf"): []byte("X")})

	a := mk("X")
	if _, err := ApplyRotation(a, "s", "", files); err != nil {
		t.Fatal(err)
	}
	name1 := a["configs"].(map[string]any)["app"].(map[string]any)["name"]

	b := mk("X")
	if _, err := ApplyRotation(b, "s", "", files); err != nil {
		t.Fatal(err)
	}
	name2 := b["configs"].(map[string]any)["app"].(map[string]any)["name"]
	if name1 != name2 {
		t.Errorf("same content must hash equal: %q vs %q", name1, name2)
	}

	filesChanged := contentResolver(map[string][]byte{filepath.Join("", "app.conf"): []byte("Y")})
	c := mk("Y")
	if _, err := ApplyRotation(c, "s", "", filesChanged); err != nil {
		t.Fatal(err)
	}
	name3 := c["configs"].(map[string]any)["app"].(map[string]any)["name"]
	if name1 == name3 {
		t.Errorf("different content must hash differently: both %q", name1)
	}
}

func TestApplyRotation_NonFileObjectsUntouched(t *testing.T) {
	compose := map[string]any{"secrets": map[string]any{
		"ext": map[string]any{"external": true},
		"env": map[string]any{"environment": "FOO"},
	}}
	changed, err := ApplyRotation(compose, "s", "", contentResolver(nil))
	if err != nil {
		t.Fatalf("ApplyRotation: %v", err)
	}
	if changed != 0 {
		t.Errorf("changed = %d, want 0 (no file-backed objects)", changed)
	}
	ext := compose["secrets"].(map[string]any)["ext"].(map[string]any)
	if _, has := ext["name"]; has {
		t.Error("non-file object should not get a name")
	}
}

func TestApplyRotation_MissingFileErrors(t *testing.T) {
	compose := map[string]any{"configs": map[string]any{"app": map[string]any{"file": "missing.conf"}}}
	_, err := ApplyRotation(compose, "s", "", contentResolver(map[string][]byte{}))
	if err == nil {
		t.Fatal("want error for missing file")
	}
}
