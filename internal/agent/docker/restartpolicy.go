package docker

import (
	"context"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// RestartPolicySetter is implemented by engines that can change the restart
// policy of an existing container without recreating it (docker update
// --restart). It is optional, so it is not part of Engine: the agent fences a
// database with it (policy "no") and falls back to an error when absent.
type RestartPolicySetter interface {
	// ContainerSetRestartPolicy sets the policy ("no", "unless-stopped", ...).
	ContainerSetRestartPolicy(ctx context.Context, id, policy string) error
}

var _ RestartPolicySetter = (*SDKEngine)(nil)

// ContainerSetRestartPolicy implements RestartPolicySetter.
func (e *SDKEngine) ContainerSetRestartPolicy(ctx context.Context, id, policy string) error {
	_, err := e.cli.ContainerUpdate(ctx, id, client.ContainerUpdateOptions{
		RestartPolicy: &container.RestartPolicy{Name: container.RestartPolicyMode(policy)},
	})
	return wrap(err, "update restart policy of container "+id)
}
