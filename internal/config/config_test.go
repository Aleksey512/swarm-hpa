package config

import (
	"strings"
	"testing"
	"time"
)

func fakeEnv(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestLoadArgsDefaults(t *testing.T) {
	c, err := LoadArgs(nil, fakeEnv(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.PollInterval != 15*time.Second {
		t.Errorf("PollInterval = %s, want 15s", c.PollInterval)
	}
	if !c.DryRun {
		t.Error("DryRun must default to true (safety)")
	}
	if c.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", c.LogLevel)
	}
	if c.MetricsProvider != ProviderDockerStats {
		t.Errorf("MetricsProvider = %q, want %q", c.MetricsProvider, ProviderDockerStats)
	}
	if c.MetricsAddr != ":9095" {
		t.Errorf("MetricsAddr = %q, want :9095", c.MetricsAddr)
	}
	if c.Cooldown != 3*time.Minute {
		t.Errorf("Cooldown = %s, want 3m", c.Cooldown)
	}
}

func TestLoadArgsCooldown(t *testing.T) {
	// env over default
	c, err := LoadArgs(nil, fakeEnv(map[string]string{"COOLDOWN": "45s"}))
	if err != nil || c.Cooldown != 45*time.Second {
		t.Fatalf("env cooldown: err=%v cooldown=%s", err, c.Cooldown)
	}
	// flag over env
	c, err = LoadArgs([]string{"--cooldown=10s"}, fakeEnv(map[string]string{"COOLDOWN": "45s"}))
	if err != nil || c.Cooldown != 10*time.Second {
		t.Fatalf("flag cooldown: err=%v cooldown=%s", err, c.Cooldown)
	}
	// zero is allowed (disables rate-limiting)
	if _, err := LoadArgs([]string{"--cooldown=0"}, fakeEnv(nil)); err != nil {
		t.Errorf("cooldown=0 should be valid, got %v", err)
	}
}

func TestLoadArgsHealThreshold(t *testing.T) {
	// default
	c, err := LoadArgs(nil, fakeEnv(nil))
	if err != nil || c.HealThreshold != 2*time.Minute {
		t.Fatalf("default heal_threshold: err=%v got=%s want=2m", err, c.HealThreshold)
	}
	// env over default
	c, err = LoadArgs(nil, fakeEnv(map[string]string{"HEAL_THRESHOLD": "90s"}))
	if err != nil || c.HealThreshold != 90*time.Second {
		t.Fatalf("env heal_threshold: err=%v got=%s", err, c.HealThreshold)
	}
	// flag over env
	c, err = LoadArgs([]string{"--heal-threshold=30s"}, fakeEnv(map[string]string{"HEAL_THRESHOLD": "90s"}))
	if err != nil || c.HealThreshold != 30*time.Second {
		t.Fatalf("flag heal_threshold: err=%v got=%s", err, c.HealThreshold)
	}
	// zero is allowed (disables the duration gate)
	if _, err := LoadArgs([]string{"--heal-threshold=0"}, fakeEnv(nil)); err != nil {
		t.Errorf("heal_threshold=0 should be valid, got %v", err)
	}
	// negative is rejected
	if _, err := LoadArgs([]string{"--heal-threshold=-1s"}, fakeEnv(nil)); err == nil {
		t.Error("negative heal_threshold must be rejected")
	}
	// malformed env duration is rejected
	if _, err := LoadArgs(nil, fakeEnv(map[string]string{"HEAL_THRESHOLD": "soon"})); err == nil {
		t.Error("malformed HEAL_THRESHOLD must be rejected")
	}
}

func TestLoadArgsStabilization(t *testing.T) {
	// defaults: cooldowns 3m, step/stabilization disabled (0)
	c, err := LoadArgs(nil, fakeEnv(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ScaleUpCooldown != 3*time.Minute || c.ScaleDownCooldown != 3*time.Minute {
		t.Errorf("default scale cooldowns = up:%s down:%s, want 3m/3m", c.ScaleUpCooldown, c.ScaleDownCooldown)
	}
	if c.MaxScaleStep != 0 || c.ScaleDownStabilizationWindow != 0 {
		t.Errorf("defaults must disable step/stabilization, got step=%d window=%s", c.MaxScaleStep, c.ScaleDownStabilizationWindow)
	}

	// env overrides
	c, err = LoadArgs(nil, fakeEnv(map[string]string{
		"SCALE_UP_COOLDOWN":        "30s",
		"SCALE_DOWN_COOLDOWN":      "10m",
		"MAX_SCALE_STEP":           "2",
		"SCALE_DOWN_STABILIZATION": "5m",
	}))
	if err != nil {
		t.Fatalf("env: %v", err)
	}
	if c.ScaleUpCooldown != 30*time.Second || c.ScaleDownCooldown != 10*time.Minute ||
		c.MaxScaleStep != 2 || c.ScaleDownStabilizationWindow != 5*time.Minute {
		t.Errorf("env override failed: %+v", c)
	}

	// flag over env
	c, err = LoadArgs([]string{"--max-scale-step=5", "--scale-down-cooldown=1m"},
		fakeEnv(map[string]string{"MAX_SCALE_STEP": "2", "SCALE_DOWN_COOLDOWN": "10m"}))
	if err != nil || c.MaxScaleStep != 5 || c.ScaleDownCooldown != time.Minute {
		t.Fatalf("flag over env: err=%v step=%d down=%s", err, c.MaxScaleStep, c.ScaleDownCooldown)
	}

	// invalid values
	if _, err := LoadArgs([]string{"--scale-down-stabilization=-1s"}, fakeEnv(nil)); err == nil {
		t.Error("negative scale_down_stabilization must be rejected")
	}
	if _, err := LoadArgs(nil, fakeEnv(map[string]string{"MAX_SCALE_STEP": "-1"})); err == nil {
		t.Error("non-uint MAX_SCALE_STEP must be rejected")
	}
	if _, err := LoadArgs(nil, fakeEnv(map[string]string{"SCALE_UP_COOLDOWN": "soon"})); err == nil {
		t.Error("malformed SCALE_UP_COOLDOWN must be rejected")
	}
}

func TestLoadArgsPrometheusTimeout(t *testing.T) {
	// default
	c, err := LoadArgs(nil, fakeEnv(nil))
	if err != nil || c.PrometheusTimeout != 10*time.Second {
		t.Fatalf("default prometheus_timeout: err=%v got=%s want=10s", err, c.PrometheusTimeout)
	}
	// env over default
	c, err = LoadArgs(nil, fakeEnv(map[string]string{"PROMETHEUS_TIMEOUT": "3s"}))
	if err != nil || c.PrometheusTimeout != 3*time.Second {
		t.Fatalf("env prometheus_timeout: err=%v got=%s", err, c.PrometheusTimeout)
	}
	// flag over env
	c, err = LoadArgs([]string{"--prometheus-timeout=1s"}, fakeEnv(map[string]string{"PROMETHEUS_TIMEOUT": "3s"}))
	if err != nil || c.PrometheusTimeout != time.Second {
		t.Fatalf("flag prometheus_timeout: err=%v got=%s", err, c.PrometheusTimeout)
	}
	// zero is rejected (a query timeout must be positive)
	if _, err := LoadArgs([]string{"--prometheus-timeout=0"}, fakeEnv(nil)); err == nil {
		t.Error("prometheus_timeout=0 must be rejected")
	}
	// malformed env duration is rejected
	if _, err := LoadArgs(nil, fakeEnv(map[string]string{"PROMETHEUS_TIMEOUT": "soon"})); err == nil {
		t.Error("malformed PROMETHEUS_TIMEOUT must be rejected")
	}
}

func TestLoadArgsPrecedence(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		env        map[string]string
		wantPoll   time.Duration
		wantDryRun bool
	}{
		{
			name:       "env overrides default",
			env:        map[string]string{"POLL_INTERVAL": "30s", "DRY_RUN": "false"},
			wantPoll:   30 * time.Second,
			wantDryRun: false,
		},
		{
			name:       "flag overrides env",
			args:       []string{"--poll-interval=5s", "--dry-run=true"},
			env:        map[string]string{"POLL_INTERVAL": "30s", "DRY_RUN": "false"},
			wantPoll:   5 * time.Second,
			wantDryRun: true,
		},
		{
			name:       "flag overrides default",
			args:       []string{"--poll-interval=7s"},
			wantPoll:   7 * time.Second,
			wantDryRun: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := LoadArgs(tc.args, fakeEnv(tc.env))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.PollInterval != tc.wantPoll {
				t.Errorf("PollInterval = %s, want %s", c.PollInterval, tc.wantPoll)
			}
			if c.DryRun != tc.wantDryRun {
				t.Errorf("DryRun = %v, want %v", c.DryRun, tc.wantDryRun)
			}
		})
	}
}

func TestLoadArgsPrometheusValid(t *testing.T) {
	c, err := LoadArgs(
		[]string{"--metrics-provider=prometheus", "--prometheus-url=http://prom:9090"},
		fakeEnv(nil),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.MetricsProvider != ProviderPrometheus {
		t.Errorf("MetricsProvider = %q, want %q", c.MetricsProvider, ProviderPrometheus)
	}
}

func TestLoadArgsValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"prometheus without url", nil, map[string]string{"METRICS_PROVIDER": "prometheus"}},
		{"zero poll interval", []string{"--poll-interval=0"}, nil},
		{"negative poll interval", []string{"--poll-interval=-1s"}, nil},
		{"invalid provider", []string{"--metrics-provider=bogus"}, nil},
		{"invalid log level", []string{"--log-level=loud"}, nil},
		{"invalid log format", []string{"--log-format=xml"}, nil},
		{"bad env duration", nil, map[string]string{"POLL_INTERVAL": "abc"}},
		{"negative cooldown", []string{"--cooldown=-1s"}, nil},
		{"bad env cooldown", nil, map[string]string{"COOLDOWN": "xyz"}},
		{"bad env bool", nil, map[string]string{"DRY_RUN": "maybe"}},
		{"unknown flag", []string{"--nope"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadArgs(tc.args, fakeEnv(tc.env)); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

func TestRedactURL(t *testing.T) {
	got := redactURL("http://user:secret@prom:9090/api")
	if strings.Contains(got, "secret") {
		t.Errorf("password leaked in redacted URL: %q", got)
	}
	if redactURL("") != "" {
		t.Error("empty URL should stay empty")
	}
	if redactURL("http://prom:9090") != "http://prom:9090" {
		t.Error("URL without credentials should be unchanged")
	}
}
