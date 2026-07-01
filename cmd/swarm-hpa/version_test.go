package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestVersionString(t *testing.T) {
	got := versionString()
	for _, want := range []string{"swarm-hpa", version, runtime.Version(), runtime.GOOS, runtime.GOARCH} {
		if !strings.Contains(got, want) {
			t.Errorf("versionString() = %q, want it to contain %q", got, want)
		}
	}
}

func TestWantsVersion(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"--version", []string{"--version"}, true},
		{"-v", []string{"-v"}, true},
		{"among other flags", []string{"--dry-run=false", "-v"}, true},
		{"no version flag", []string{"--dry-run=false", "--log-level=debug"}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wantsVersion(tc.args); got != tc.want {
				t.Errorf("wantsVersion(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
