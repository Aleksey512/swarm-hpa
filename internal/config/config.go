package config

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Metrics provider identifiers (the swarm.autoscaler.* metric source).
const (
	ProviderDockerStats = "dockerstats"
	ProviderPrometheus  = "prometheus"
)

// Config is the daemon's runtime configuration. Values are resolved with the
// precedence flag > environment > default.
type Config struct {
	// PollInterval is the reconcile loop period.
	PollInterval time.Duration
	// Cooldown is the minimum interval between mutations of the same service
	// (0 disables rate-limiting).
	Cooldown time.Duration
	// HealThreshold is the minimum duration a task must stay pending (while
	// desired-running) before the healer considers the service stuck. A higher
	// value is more conservative; 0 disables the duration gate.
	HealThreshold time.Duration
	// DryRun, when true (the default), logs intended mutations without applying
	// them. This is the project's safety default.
	DryRun bool
	// LogLevel is debug|info|warn|error.
	LogLevel string
	// LogFormat is text|json.
	LogFormat string
	// MetricsProvider selects the scaling-metric source: dockerstats|prometheus.
	MetricsProvider string
	// PrometheusURL is the Prometheus base URL; required when
	// MetricsProvider == ProviderPrometheus.
	PrometheusURL string
	// MetricsAddr is the listen address for the daemon's own /metrics endpoint.
	MetricsAddr string
}

// Default returns the configuration with all defaults applied.
func Default() Config {
	return Config{
		PollInterval:    15 * time.Second,
		Cooldown:        3 * time.Minute,
		HealThreshold:   2 * time.Minute,
		DryRun:          true,
		LogLevel:        "info",
		LogFormat:       "text",
		MetricsProvider: ProviderDockerStats,
		MetricsAddr:     ":9095",
	}
}

// Validate reports the first configuration problem, if any.
func (c Config) Validate() error {
	if c.PollInterval <= 0 {
		return fmt.Errorf("poll_interval must be > 0, got %s", c.PollInterval)
	}
	if c.Cooldown < 0 {
		return fmt.Errorf("cooldown must be >= 0, got %s", c.Cooldown)
	}
	if c.HealThreshold < 0 {
		return fmt.Errorf("heal_threshold must be >= 0, got %s", c.HealThreshold)
	}
	switch c.MetricsProvider {
	case ProviderDockerStats:
		// no extra requirements
	case ProviderPrometheus:
		if strings.TrimSpace(c.PrometheusURL) == "" {
			return fmt.Errorf("prometheus_url is required when metrics_provider=%s", ProviderPrometheus)
		}
	default:
		return fmt.Errorf("invalid metrics_provider %q (want %s|%s)",
			c.MetricsProvider, ProviderDockerStats, ProviderPrometheus)
	}
	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "debug", "info", "warn", "warning", "error":
		// ok
	default:
		return fmt.Errorf("invalid log_level %q (want debug|info|warn|error)", c.LogLevel)
	}
	switch c.LogFormat {
	case "text", "json":
		// ok
	default:
		return fmt.Errorf("invalid log_format %q (want text|json)", c.LogFormat)
	}
	return nil
}

// LogValue implements slog.LogValuer so a Config logs as a structured group with
// any URL credentials redacted.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Duration("poll_interval", c.PollInterval),
		slog.Duration("cooldown", c.Cooldown),
		slog.Duration("heal_threshold", c.HealThreshold),
		slog.Bool("dry_run", c.DryRun),
		slog.String("log_level", c.LogLevel),
		slog.String("log_format", c.LogFormat),
		slog.String("metrics_provider", c.MetricsProvider),
		slog.String("prometheus_url", redactURL(c.PrometheusURL)),
		slog.String("metrics_addr", c.MetricsAddr),
	)
}

// Load resolves the configuration from the process arguments and environment,
// validates it, and logs the effective configuration at INFO.
func Load() (Config, error) {
	c, err := LoadArgs(os.Args[1:], os.LookupEnv)
	if err != nil {
		return Config{}, err
	}
	slog.Info("effective configuration", "config", c)
	slog.Debug("configuration resolved",
		"poll_interval", c.PollInterval,
		"dry_run", c.DryRun,
		"metrics_provider", c.MetricsProvider,
	)
	return c, nil
}

// LoadArgs is the testable core of Load: it resolves configuration from the
// given arguments and environment-lookup function (flag > env > default) and
// validates the result. It performs no logging and reads no global state.
func LoadArgs(args []string, lookupEnv func(string) (string, bool)) (Config, error) {
	c := Default()

	// Apply environment overrides first; flags below default to these values, so
	// a provided flag wins, otherwise env wins, otherwise the built-in default.
	if v, ok := lookupEnv("POLL_INTERVAL"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("env POLL_INTERVAL=%q: %w", v, err)
		}
		c.PollInterval = d
	}
	if v, ok := lookupEnv("COOLDOWN"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("env COOLDOWN=%q: %w", v, err)
		}
		c.Cooldown = d
	}
	if v, ok := lookupEnv("HEAL_THRESHOLD"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("env HEAL_THRESHOLD=%q: %w", v, err)
		}
		c.HealThreshold = d
	}
	if v, ok := lookupEnv("DRY_RUN"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("env DRY_RUN=%q: %w", v, err)
		}
		c.DryRun = b
	}
	if v, ok := lookupEnv("LOG_LEVEL"); ok {
		c.LogLevel = v
	}
	if v, ok := lookupEnv("LOG_FORMAT"); ok {
		c.LogFormat = v
	}
	if v, ok := lookupEnv("METRICS_PROVIDER"); ok {
		c.MetricsProvider = v
	}
	if v, ok := lookupEnv("PROMETHEUS_URL"); ok {
		c.PrometheusURL = v
	}
	if v, ok := lookupEnv("METRICS_ADDR"); ok {
		c.MetricsAddr = v
	}

	fs := flag.NewFlagSet("swarm-hpa", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are returned, not printed
	pollInterval := fs.Duration("poll-interval", c.PollInterval, "reconcile loop interval")
	cooldown := fs.Duration("cooldown", c.Cooldown, "minimum interval between mutations of the same service")
	healThreshold := fs.Duration("heal-threshold", c.HealThreshold, "minimum time a task must stay pending before the healer force-updates the service")
	dryRun := fs.Bool("dry-run", c.DryRun, "log intended mutations without applying them")
	logLevel := fs.String("log-level", c.LogLevel, "log level: debug|info|warn|error")
	logFormat := fs.String("log-format", c.LogFormat, "log format: text|json")
	metricsProvider := fs.String("metrics-provider", c.MetricsProvider, "metrics source: dockerstats|prometheus")
	prometheusURL := fs.String("prometheus-url", c.PrometheusURL, "Prometheus base URL (required when metrics-provider=prometheus)")
	metricsAddr := fs.String("metrics-addr", c.MetricsAddr, "listen address for the /metrics endpoint")

	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("parse flags: %w", err)
	}

	c.PollInterval = *pollInterval
	c.Cooldown = *cooldown
	c.HealThreshold = *healThreshold
	c.DryRun = *dryRun
	c.LogLevel = *logLevel
	c.LogFormat = *logFormat
	c.MetricsProvider = *metricsProvider
	c.PrometheusURL = *prometheusURL
	c.MetricsAddr = *metricsAddr

	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return c, nil
}

// redactURL replaces any userinfo (user:password@) in a URL with a placeholder
// so credentials never reach the logs.
func redactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User("xxxxx")
	return u.String()
}
