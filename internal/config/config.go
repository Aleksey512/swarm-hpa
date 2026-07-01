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

// Runtime roles. A single binary runs as either a manager (the reconcile loop
// plus the agent-report ingest endpoint, rebalancer, and distributed metrics)
// or a per-node agent (collects local task/node load and reports it to the
// manager). Manager is the default so existing single-daemon deployments keep
// working unchanged.
const (
	ModeManager = "manager"
	ModeAgent   = "agent"
)

// Config is the daemon's runtime configuration. Values are resolved with the
// precedence flag > environment > default.
type Config struct {
	// PollInterval is the reconcile loop period.
	PollInterval time.Duration
	// Cooldown is the minimum interval between heal (force-update) actions on
	// the same service (0 disables rate-limiting).
	Cooldown time.Duration
	// ScaleUpCooldown / ScaleDownCooldown are the minimum intervals between
	// scale-up / scale-down actions on the same service (0 disables). Heal uses
	// Cooldown above.
	ScaleUpCooldown   time.Duration
	ScaleDownCooldown time.Duration
	// MaxScaleStep caps how many replicas a single scaling action may change
	// (0 = unlimited).
	MaxScaleStep uint64
	// ScaleDownStabilizationWindow holds a scale-down until the recommendation
	// has stayed low for this long (0 disables); scale-ups are unaffected. This
	// mirrors the Kubernetes HPA scale-down stabilization window.
	ScaleDownStabilizationWindow time.Duration
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
	// PrometheusTimeout bounds each PromQL query against PrometheusURL.
	PrometheusTimeout time.Duration
	// MetricsAddr is the listen address for the daemon's own /metrics endpoint.
	MetricsAddr string

	// Mode selects the runtime role: manager (default) or agent.
	Mode string

	// --- Manager-only settings (ignored in agent mode) ---

	// IngestAddr is the listen address for the agent-report ingest endpoint
	// (POST /v1/report). Kept separate from MetricsAddr so it can be scoped to
	// the internal overlay network rather than exposed for scraping.
	IngestAddr string
	// IngestToken is a shared secret; when set, agent reports must present it as
	// a bearer token and the manager rejects unauthenticated reports. Sourced
	// from the INGEST_TOKEN environment variable only (never a flag, so it does
	// not leak into process listings). The agent role reads the same variable to
	// authenticate its outgoing reports.
	IngestToken string
	// AgentStaleTimeout is how long an agent's last report stays usable; reports
	// older than this are evicted so a dead node stops influencing decisions.
	AgentStaleTimeout time.Duration
	// RebalanceThreshold is the node-load spread (as a fraction in (0,1], e.g.
	// 0.30 = 30 percentage points between the busiest and idlest node) at or
	// above which the rebalancer proposes moving load.
	RebalanceThreshold float64
	// RebalanceCooldown is the minimum interval between rebalance (force-update)
	// actions on the same service (0 disables rate-limiting).
	RebalanceCooldown time.Duration

	// --- Agent-only settings (ignored in manager mode) ---

	// ManagerURL is the base URL of the manager's ingest endpoint (required in
	// agent mode), e.g. http://swarm-hpa-manager:9096.
	ManagerURL string
	// ReportInterval is how often the agent collects and pushes a report.
	ReportInterval time.Duration
	// NodeID optionally overrides the node identity the agent reports; normally
	// left empty so the agent auto-detects it from the local Docker daemon.
	NodeID string
}

// Default returns the configuration with all defaults applied.
func Default() Config {
	return Config{
		PollInterval:      15 * time.Second,
		Cooldown:          3 * time.Minute,
		ScaleUpCooldown:   3 * time.Minute,
		ScaleDownCooldown: 3 * time.Minute,
		HealThreshold:     2 * time.Minute,
		DryRun:            true,
		LogLevel:          "info",
		LogFormat:         "text",
		MetricsProvider:   ProviderDockerStats,
		PrometheusTimeout: 10 * time.Second,
		MetricsAddr:       ":9095",

		Mode:               ModeManager,
		IngestAddr:         ":9096",
		AgentStaleTimeout:  45 * time.Second,
		RebalanceThreshold: 0.30,
		RebalanceCooldown:  10 * time.Minute,
		ReportInterval:     15 * time.Second,
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
	if c.ScaleUpCooldown < 0 {
		return fmt.Errorf("scale_up_cooldown must be >= 0, got %s", c.ScaleUpCooldown)
	}
	if c.ScaleDownCooldown < 0 {
		return fmt.Errorf("scale_down_cooldown must be >= 0, got %s", c.ScaleDownCooldown)
	}
	if c.ScaleDownStabilizationWindow < 0 {
		return fmt.Errorf("scale_down_stabilization must be >= 0, got %s", c.ScaleDownStabilizationWindow)
	}
	if c.HealThreshold < 0 {
		return fmt.Errorf("heal_threshold must be >= 0, got %s", c.HealThreshold)
	}
	switch c.Mode {
	case ModeManager:
		if err := c.validateManager(); err != nil {
			return err
		}
	case ModeAgent:
		if err := c.validateAgent(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid mode %q (want %s|%s)", c.Mode, ModeManager, ModeAgent)
	}
	if c.PrometheusTimeout <= 0 {
		return fmt.Errorf("prometheus_timeout must be > 0, got %s", c.PrometheusTimeout)
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

// validateManager checks the manager-only settings (metrics provider, ingest
// endpoint, rebalance thresholds). Agent-only fields are intentionally ignored.
func (c Config) validateManager() error {
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
	if strings.TrimSpace(c.IngestAddr) == "" {
		return fmt.Errorf("ingest_addr must not be empty in manager mode")
	}
	if c.AgentStaleTimeout <= 0 {
		return fmt.Errorf("agent_stale_timeout must be > 0, got %s", c.AgentStaleTimeout)
	}
	if c.RebalanceThreshold <= 0 || c.RebalanceThreshold > 1 {
		return fmt.Errorf("rebalance_threshold must be in (0,1], got %g", c.RebalanceThreshold)
	}
	if c.RebalanceCooldown < 0 {
		return fmt.Errorf("rebalance_cooldown must be >= 0, got %s", c.RebalanceCooldown)
	}
	return nil
}

// validateAgent checks the agent-only settings (manager URL, report interval).
// Manager-only fields are intentionally ignored.
func (c Config) validateAgent() error {
	if strings.TrimSpace(c.ManagerURL) == "" {
		return fmt.Errorf("manager_url is required in agent mode")
	}
	if _, err := url.Parse(c.ManagerURL); err != nil {
		return fmt.Errorf("invalid manager_url %q: %w", c.ManagerURL, err)
	}
	if c.ReportInterval <= 0 {
		return fmt.Errorf("report_interval must be > 0, got %s", c.ReportInterval)
	}
	return nil
}

// LogValue implements slog.LogValuer so a Config logs as a structured group with
// any URL credentials redacted.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Duration("poll_interval", c.PollInterval),
		slog.Duration("cooldown", c.Cooldown),
		slog.Duration("scale_up_cooldown", c.ScaleUpCooldown),
		slog.Duration("scale_down_cooldown", c.ScaleDownCooldown),
		slog.Uint64("max_scale_step", c.MaxScaleStep),
		slog.Duration("scale_down_stabilization", c.ScaleDownStabilizationWindow),
		slog.Duration("heal_threshold", c.HealThreshold),
		slog.Bool("dry_run", c.DryRun),
		slog.String("log_level", c.LogLevel),
		slog.String("log_format", c.LogFormat),
		slog.String("metrics_provider", c.MetricsProvider),
		slog.String("prometheus_url", redactURL(c.PrometheusURL)),
		slog.Duration("prometheus_timeout", c.PrometheusTimeout),
		slog.String("metrics_addr", c.MetricsAddr),
		slog.String("mode", c.Mode),
		slog.String("ingest_addr", c.IngestAddr),
		slog.Bool("ingest_token_set", c.IngestToken != ""),
		slog.Duration("agent_stale_timeout", c.AgentStaleTimeout),
		slog.Float64("rebalance_threshold", c.RebalanceThreshold),
		slog.Duration("rebalance_cooldown", c.RebalanceCooldown),
		slog.String("manager_url", redactURL(c.ManagerURL)),
		slog.Duration("report_interval", c.ReportInterval),
		slog.String("node_id", c.NodeID),
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
		"mode", c.Mode,
		"poll_interval", c.PollInterval,
		"dry_run", c.DryRun,
		"metrics_provider", c.MetricsProvider,
		"ingest_addr", c.IngestAddr,
		"manager_url", redactURL(c.ManagerURL),
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
	if v, ok := lookupEnv("SCALE_UP_COOLDOWN"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("env SCALE_UP_COOLDOWN=%q: %w", v, err)
		}
		c.ScaleUpCooldown = d
	}
	if v, ok := lookupEnv("SCALE_DOWN_COOLDOWN"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("env SCALE_DOWN_COOLDOWN=%q: %w", v, err)
		}
		c.ScaleDownCooldown = d
	}
	if v, ok := lookupEnv("MAX_SCALE_STEP"); ok {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("env MAX_SCALE_STEP=%q: %w", v, err)
		}
		c.MaxScaleStep = n
	}
	if v, ok := lookupEnv("SCALE_DOWN_STABILIZATION"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("env SCALE_DOWN_STABILIZATION=%q: %w", v, err)
		}
		c.ScaleDownStabilizationWindow = d
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
	if v, ok := lookupEnv("PROMETHEUS_TIMEOUT"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("env PROMETHEUS_TIMEOUT=%q: %w", v, err)
		}
		c.PrometheusTimeout = d
	}
	if v, ok := lookupEnv("METRICS_ADDR"); ok {
		c.MetricsAddr = v
	}
	if v, ok := lookupEnv("MODE"); ok {
		c.Mode = v
	}
	if v, ok := lookupEnv("INGEST_ADDR"); ok {
		c.IngestAddr = v
	}
	// INGEST_TOKEN is env-only (no flag) so the shared secret never appears in a
	// process listing. Used by the manager to authenticate reports and by the
	// agent to sign them.
	if v, ok := lookupEnv("INGEST_TOKEN"); ok {
		c.IngestToken = v
	}
	if v, ok := lookupEnv("AGENT_STALE_TIMEOUT"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("env AGENT_STALE_TIMEOUT=%q: %w", v, err)
		}
		c.AgentStaleTimeout = d
	}
	if v, ok := lookupEnv("REBALANCE_THRESHOLD"); ok {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return Config{}, fmt.Errorf("env REBALANCE_THRESHOLD=%q: %w", v, err)
		}
		c.RebalanceThreshold = f
	}
	if v, ok := lookupEnv("REBALANCE_COOLDOWN"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("env REBALANCE_COOLDOWN=%q: %w", v, err)
		}
		c.RebalanceCooldown = d
	}
	if v, ok := lookupEnv("MANAGER_URL"); ok {
		c.ManagerURL = v
	}
	if v, ok := lookupEnv("REPORT_INTERVAL"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("env REPORT_INTERVAL=%q: %w", v, err)
		}
		c.ReportInterval = d
	}
	if v, ok := lookupEnv("NODE_ID"); ok {
		c.NodeID = v
	}

	fs := flag.NewFlagSet("swarm-hpa", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are returned, not printed
	pollInterval := fs.Duration("poll-interval", c.PollInterval, "reconcile loop interval")
	cooldown := fs.Duration("cooldown", c.Cooldown, "minimum interval between heal actions on the same service")
	scaleUpCooldown := fs.Duration("scale-up-cooldown", c.ScaleUpCooldown, "minimum interval between scale-up actions on the same service")
	scaleDownCooldown := fs.Duration("scale-down-cooldown", c.ScaleDownCooldown, "minimum interval between scale-down actions on the same service")
	maxScaleStep := fs.Uint64("max-scale-step", c.MaxScaleStep, "maximum replicas changed per scaling action (0 = unlimited)")
	scaleDownStabilization := fs.Duration("scale-down-stabilization", c.ScaleDownStabilizationWindow, "hold a scale-down until the recommendation has stayed low for this long (0 = disabled)")
	healThreshold := fs.Duration("heal-threshold", c.HealThreshold, "minimum time a task must stay pending before the healer force-updates the service")
	dryRun := fs.Bool("dry-run", c.DryRun, "log intended mutations without applying them")
	logLevel := fs.String("log-level", c.LogLevel, "log level: debug|info|warn|error")
	logFormat := fs.String("log-format", c.LogFormat, "log format: text|json")
	metricsProvider := fs.String("metrics-provider", c.MetricsProvider, "metrics source: dockerstats|prometheus")
	prometheusURL := fs.String("prometheus-url", c.PrometheusURL, "Prometheus base URL (required when metrics-provider=prometheus)")
	prometheusTimeout := fs.Duration("prometheus-timeout", c.PrometheusTimeout, "per-query timeout for PromQL requests")
	metricsAddr := fs.String("metrics-addr", c.MetricsAddr, "listen address for the /metrics endpoint")
	mode := fs.String("mode", c.Mode, "runtime role: manager|agent")
	ingestAddr := fs.String("ingest-addr", c.IngestAddr, "manager: listen address for the agent-report ingest endpoint")
	agentStaleTimeout := fs.Duration("agent-stale-timeout", c.AgentStaleTimeout, "manager: how long an agent report stays usable before eviction")
	rebalanceThreshold := fs.Float64("rebalance-threshold", c.RebalanceThreshold, "manager: node-load spread fraction (0,1] at/above which rebalancing is proposed")
	rebalanceCooldown := fs.Duration("rebalance-cooldown", c.RebalanceCooldown, "manager: minimum interval between rebalance actions on the same service (0 = unlimited)")
	managerURL := fs.String("manager-url", c.ManagerURL, "agent: base URL of the manager ingest endpoint (required in agent mode)")
	reportInterval := fs.Duration("report-interval", c.ReportInterval, "agent: how often to collect and push a report")
	nodeID := fs.String("node-id", c.NodeID, "agent: override the reported node ID (default: auto-detect from the local daemon)")

	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("parse flags: %w", err)
	}

	c.PollInterval = *pollInterval
	c.Cooldown = *cooldown
	c.ScaleUpCooldown = *scaleUpCooldown
	c.ScaleDownCooldown = *scaleDownCooldown
	c.MaxScaleStep = *maxScaleStep
	c.ScaleDownStabilizationWindow = *scaleDownStabilization
	c.HealThreshold = *healThreshold
	c.DryRun = *dryRun
	c.LogLevel = *logLevel
	c.LogFormat = *logFormat
	c.MetricsProvider = *metricsProvider
	c.PrometheusURL = *prometheusURL
	c.PrometheusTimeout = *prometheusTimeout
	c.MetricsAddr = *metricsAddr
	c.Mode = *mode
	c.IngestAddr = *ingestAddr
	c.AgentStaleTimeout = *agentStaleTimeout
	c.RebalanceThreshold = *rebalanceThreshold
	c.RebalanceCooldown = *rebalanceCooldown
	c.ManagerURL = *managerURL
	c.ReportInterval = *reportInterval
	c.NodeID = *nodeID

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
