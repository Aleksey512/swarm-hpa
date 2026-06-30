package observability

import (
	"context"
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in     string
		want   slog.Level
		wantOK bool
	}{
		{"", slog.LevelInfo, true},
		{"info", slog.LevelInfo, true},
		{"INFO", slog.LevelInfo, true},
		{" debug ", slog.LevelDebug, true},
		{"warn", slog.LevelWarn, true},
		{"warning", slog.LevelWarn, true},
		{"error", slog.LevelError, true},
		{"bogus", slog.LevelInfo, false},
	}
	for _, c := range cases {
		got, ok := ParseLevel(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("ParseLevel(%q) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

func TestNewRespectsLevel(t *testing.T) {
	logger := New(Options{Level: "warn"})
	ctx := context.Background()
	if logger.Enabled(ctx, slog.LevelInfo) {
		t.Error("INFO should be disabled when level is warn")
	}
	if !logger.Enabled(ctx, slog.LevelWarn) {
		t.Error("WARN should be enabled when level is warn")
	}
}
