package stackdeploy

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/command/stack"
	"github.com/docker/cli/cli/flags"
)

// NewDockerCli builds a docker/cli DockerCli wired to the daemon from the
// environment (DOCKER_HOST / docker-socket-proxy), with stdout/stderr discarded
// so the cobra stack command stays quiet. Mirrors swarm-cd's initialization.
func NewDockerCli(logger *slog.Logger) (*command.DockerCli, error) {
	cli, err := command.NewDockerCli(
		command.WithOutputStream(io.Discard),
		command.WithErrorStream(io.Discard),
	)
	if err != nil {
		return nil, fmt.Errorf("stackdeploy: new docker cli: %w", err)
	}
	if err := cli.Initialize(flags.NewClientOptions()); err != nil {
		return nil, fmt.Errorf("stackdeploy: init docker cli: %w", err)
	}
	logger.Debug("stackdeploy: docker cli initialized")
	return cli, nil
}

// DockerCLIDeploy returns a DeployFunc that runs `docker stack deploy` via the
// docker/cli cobra command — parity with swarm-cd
// (--detach --with-registry-auth --resolve-image <pullPolicy>).
func DockerCLIDeploy(dockerCli *command.DockerCli) DeployFunc {
	return func(ctx context.Context, name, composeFile, pullPolicy string) error {
		cmd := stack.NewStackCommand(dockerCli) //nolint:staticcheck // SA1019: docker/cli deprecates direct cobra imports, but there is no programmatic deploy API — matches swarm-cd; a future native granular deploy removes this
		cmd.SetArgs([]string{
			"deploy",
			"--detach",
			"--with-registry-auth",
			"--resolve-image", pullPolicy,
			"-c", composeFile,
			name,
		})
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		return cmd.ExecuteContext(ctx)
	}
}
