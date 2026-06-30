package observability

import (
	"log/slog"
	"os"
	"strings"
)

// Format selects the slog handler output format.
type Format string

const (
	// FormatText emits human-readable key=value lines (good for local dev).
	FormatText Format = "text"
	// FormatJSON emits one JSON object per line (good for log aggregation).
	FormatJSON Format = "json"
)

// Options configures the daemon logger. Level and Format are resolved by the
// config package (which owns flag/env parsing, including LOG_LEVEL); this
// package never reads the environment itself.
type Options struct {
	// Level is one of debug|info|warn|error (case-insensitive). Empty => info.
	Level string
	// Format is text|json. Empty => text.
	Format Format
}

// ParseLevel maps a level string to an slog.Level. Empty input resolves to
// Info. The bool is false when the input was non-empty but unrecognized (the
// caller may warn); the returned level still defaults to Info in that case.
func ParseLevel(s string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, true
	case "debug":
		return slog.LevelDebug, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return slog.LevelInfo, false
	}
}

// New builds a *slog.Logger writing to stderr with the given options. It does
// not change the process-wide default logger; use Setup for that.
func New(opts Options) *slog.Logger {
	level, _ := ParseLevel(opts.Level)
	handlerOpts := &slog.HandlerOptions{Level: level}

	var h slog.Handler
	switch opts.Format {
	case FormatJSON:
		h = slog.NewJSONHandler(os.Stderr, handlerOpts)
	default:
		h = slog.NewTextHandler(os.Stderr, handlerOpts)
	}
	return slog.New(h)
}

// Setup builds the logger, installs it as the slog default, and returns it so
// callers can also inject it explicitly. This is the single place that
// configures slog for the daemon.
func Setup(opts Options) *slog.Logger {
	logger := New(opts)
	slog.SetDefault(logger)
	return logger
}
