// Package stackdeploy implements port.StackDeployer. It is autoscaler-aware: a
// deploy carries forward the live replica count of any swarm.autoscaler.enabled
// service (clamped to [min,max]) before running `docker stack deploy`, so the
// GitOps reapply never clobbers a count the autoscaler just set. The carry-forward
// lives in carryforward.go, isolated so a future native granular deploy can drop it.
package stackdeploy

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/goccy/go-yaml"

	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// DeployFunc is the seam over `docker stack deploy`. The production implementation
// (DockerCLIDeploy) runs the docker/cli cobra command; tests inject a recorder so
// a deploy never touches a real daemon.
type DeployFunc func(ctx context.Context, name, composeFile, pullPolicy string) error

// Deployer implements port.StackDeployer: carry-forward, then deploy.
type Deployer struct {
	state  port.StackStateReader
	deploy DeployFunc
	logger *slog.Logger
}

// compile-time proof the deployer satisfies the core port.
var _ port.StackDeployer = (*Deployer)(nil)

// New builds a Deployer. deploy is the `docker stack deploy` seam.
func New(state port.StackStateReader, deploy DeployFunc, logger *slog.Logger) *Deployer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Deployer{state: state, deploy: deploy, logger: logger}
}

// Deploy carries autoscaled replicas forward from live Swarm state, writes the
// adjusted compose to a temp file, and runs `docker stack deploy` via DeployFunc.
func (d *Deployer) Deploy(ctx context.Context, name string, compose map[string]any, opts port.DeployOpts) error {
	log := d.logger.With(slog.String("stack", name))

	live, err := d.state.StackServices(ctx, name)
	if err != nil {
		return fmt.Errorf("stackdeploy: read live services for %q: %w", name, err)
	}
	changed, err := ApplyCarryForward(compose, live, log)
	if err != nil {
		return fmt.Errorf("stackdeploy: carry-forward for %q: %w", name, err)
	}
	log.Info("stackdeploy: carry-forward applied", "autoscaled_services", changed, "live_services", len(live))

	if opts.ComposeDir != "" {
		log.Debug("stackdeploy: writing temp compose next to source compose (relative configs:/secrets: paths resolve against the same dir, not /tmp)", "dir", opts.ComposeDir)
	}
	tmp, err := writeTempCompose(name, opts.ComposeDir, compose)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()

	pullPolicy := opts.PullPolicy
	if pullPolicy == "" {
		pullPolicy = "changed"
	}
	log.Info("stackdeploy: deploying", "pull_policy", pullPolicy, "compose_file", tmp)
	if err := d.deploy(ctx, name, tmp, pullPolicy); err != nil {
		return fmt.Errorf("stackdeploy: deploy %q: %w", name, err)
	}
	log.Info("stackdeploy: deployed")
	return nil
}

// writeTempCompose marshals the (carry-forward-adjusted) compose map to a temp
// file and returns its path. The caller removes it.
//
// When dir is non-empty, the temp file is written there — i.e. next to the
// original compose file — so that relative configs:/secrets: file paths inside
// the compose resolve against the original compose's directory. `docker stack
// deploy` resolves such paths relative to the compose file it is handed, so a
// temp file written to the OS temp dir (/tmp) breaks relative paths (they would
// be resolved under /tmp, where the referenced files do not exist). An empty dir
// falls back to the OS temp dir.
func writeTempCompose(name, dir string, compose map[string]any) (string, error) {
	b, err := yaml.Marshal(compose)
	if err != nil {
		return "", fmt.Errorf("stackdeploy: marshal compose for %q: %w", name, err)
	}
	f, err := os.CreateTemp(dir, "swarm-hpa-stack-"+sanitize(name)+"-*.yaml")
	if err != nil {
		return "", fmt.Errorf("stackdeploy: temp compose file: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("stackdeploy: write temp compose: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("stackdeploy: close temp compose: %w", err)
	}
	return f.Name(), nil
}

// sanitize replaces any character illegal in a CreateTemp pattern stem with '-'.
func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_' || c == '-' || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}
