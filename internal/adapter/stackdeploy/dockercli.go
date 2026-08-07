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
//
// Every compose file of the merge group is passed as its own -c flag, in order:
// `deploy ... -c base.yml -c override.yml -c another.override.yml <stack>`. This
// is the Swarm-native equivalent of compose's unsupported `include:` — docker/cli
// merges the documents itself, later -c winning per key, so the daemon never
// implements compose merge semantics. Preserving the slice order is therefore
// required for correctness, not cosmetics.
func DockerCLIDeploy(dockerCli *command.DockerCli) DeployFunc {
	return func(ctx context.Context, name string, composeFiles []string, pullPolicy string) error {
		args := make([]string, 0, 5+2*len(composeFiles))
		args = append(args,
			"deploy",
			"--detach",
			"--with-registry-auth",
			"--resolve-image", pullPolicy,
		)
		for _, f := range composeFiles {
			args = append(args, "-c", f)
		}
		args = append(args, name)

		cmd := stack.NewStackCommand(dockerCli) //nolint:staticcheck // SA1019: docker/cli deprecates direct cobra imports, but there is no programmatic deploy API — matches swarm-cd; a future native granular deploy removes this
		cmd.SetArgs(args)
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		return cmd.ExecuteContext(ctx)
	}
}
