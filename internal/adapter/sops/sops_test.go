package sops

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"log/slog"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type call struct{ path, format string }

// fakeDecrypt returns fixed plaintext and records each (path, format) it was
// asked for, so the test can assert format derivation + in-place overwrite
// without any real sops crypto.
func fakeDecrypt(plaintext []byte, seen *[]call) DecryptFunc {
	return func(path, format string) ([]byte, error) {
		*seen = append(*seen, call{path, format})
		return plaintext, nil
	}
}

func TestDecrypt_OverwritesInPlaceAndDerivesFormat(t *testing.T) {
	dir := t.TempDir()
	files := []string{"secrets/a.yaml", "b.json", "c.env", "d.bin"}
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), []byte("ENCRYPTED"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var seen []call
	a := NewWithDecrypter(fakeDecrypt([]byte("PLAINTEXT"), &seen), testLogger())
	if err := a.Decrypt(context.Background(), dir, files); err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	for _, f := range files {
		got, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if string(got) != "PLAINTEXT" {
			t.Errorf("%s = %q, want PLAINTEXT (overwritten in place)", f, got)
		}
	}

	wantFormat := map[string]string{"secrets/a.yaml": "yaml", "b.json": "json", "c.env": "dotenv", "d.bin": "binary"}
	if len(seen) != len(files) {
		t.Fatalf("decrypt calls = %d, want %d", len(seen), len(files))
	}
	for _, c := range seen {
		base := filepath.Base(c.path)
		// d.bin has no recognizable ext → binary; others keyed by base.
		key := base
		if base == "a.yaml" {
			key = "secrets/a.yaml"
		}
		if wantFormat[key] != c.format {
			t.Errorf("format for %s = %q, want %q", c.path, c.format, wantFormat[key])
		}
	}
}

func TestDecrypt_PropagatesErrorAndStops(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a.yaml", "b.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("enc"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	boom := errors.New("missing key")
	calls := 0
	fn := func(_ string, _ string) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, boom
		}
		return []byte("PLAINTEXT"), nil
	}
	a := NewWithDecrypter(fn, testLogger())
	err := a.Decrypt(context.Background(), dir, []string{"a.yaml", "b.yaml"})
	if err == nil {
		t.Fatal("want error from decrypt func")
	}
	if calls != 1 {
		t.Errorf("expected decrypt to stop on first error, calls = %d", calls)
	}
	// b.yaml must NOT have been touched (decrypt stopped at a.yaml).
	got, _ := os.ReadFile(filepath.Join(dir, "b.yaml"))
	if string(got) != "enc" {
		t.Errorf("b.yaml = %q, want untouched 'enc'", got)
	}
}

func TestFormatFor(t *testing.T) {
	cases := map[string]string{
		"tls.yaml": "yaml", "tls.yml": "yaml", "TLS.YAML": "yaml",
		"conf.json": "json", "app.ini": "ini", "vars.env": "dotenv", "cert": "binary",
	}
	for name, want := range cases {
		if got := formatFor(name); got != want {
			t.Errorf("formatFor(%q) = %q, want %q", name, got, want)
		}
	}
}
