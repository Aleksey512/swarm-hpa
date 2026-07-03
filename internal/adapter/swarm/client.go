package swarm

import (
	"fmt"

	"github.com/docker/docker/client"
)

// NewClient constructs a Docker API client from the environment (DOCKER_HOST,
// TLS, etc.) with API-version negotiation. The daemon must run against a Swarm
// manager — the services/tasks/nodes APIs are manager-only.
func NewClient() (*client.Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return cli, nil
}
