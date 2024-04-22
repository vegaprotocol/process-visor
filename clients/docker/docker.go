package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

type ContainerState string

const (
	StateCreating   ContainerState = "created"
	StateRunning    ContainerState = "running"
	StateRestarting ContainerState = "restarting"
	StateExited     ContainerState = "exited"
	StatePaused     ContainerState = "paused"
	StateDead       ContainerState = "dead"
)

type Client struct {
	apiClient *client.Client
}

func New() (*Client, error) {

	apiClient, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create new default docker client from env: %w", err)
	}

	return &Client{
		apiClient: apiClient,
	}, nil
}

func (c *Client) Close() error {
	return c.apiClient.Close()
}

func (c *Client) ContainerStateByName(ctx context.Context, name string) (*types.ContainerState, error) {
	containers, err := c.apiClient.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list docker containers: %w", err)
	}

	containerId := ""
ContainerLoop:
	for _, container := range containers {
		for _, containerName := range container.Names {
			if strings.Contains(containerName, name) {
				containerId = container.ID
				break ContainerLoop
			}
		}
	}

	if len(containerId) < 1 {
		return nil, fmt.Errorf("container with given name(%s) does not exist", name)
	}

	containerDetails, err := c.apiClient.ContainerInspect(ctx, containerId)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container(%s:%s) for name: %w", name, containerId, err)
	}

	return containerDetails.State, nil
}

func IsContainerState(container *types.Container, state ContainerState) bool {
	if container == nil {
		return false
	}

	return strings.ToLower(container.State) == string(state)
}
