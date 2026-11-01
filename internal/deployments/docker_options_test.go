package deployments

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// agentValidates is the check the agent runs on a deploy payload (Docker
// options against the server policy it carries; nil policy = restricted).
func agentValidates(pl agentprotocol.DeployPayload) error {
	return pl.Docker.Validate(pl.DockerPolicy != nil && pl.DockerPolicy.Unrestricted)
}

func (f *routeFixture) setUnrestricted(on bool) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET unrestricted = $2 WHERE id = $1", f.server.ID, on)
	require.NoError(f.t, err)
}

func (f *routeFixture) newDocker(name string, o *agentprotocol.DockerOptions) (services.ServiceView, error) {
	return f.svcs.Create(f.ctx, f.env.ID, services.CreateInput{
		Name: name, SourceType: services.SourceImage, ServerID: &f.server.ID,
		Config: services.Config{Image: "traefik/whoami:latest", Port: 80, Docker: o},
	})
}

// The server's Unrestricted switch gates the host-level Docker options at
// create, at plan, at deploy and in the payload the agent re-validates.
func TestDockerOptionsPlacementPlanDeployAndPayload(t *testing.T) {
	f := newRouteFixture(t)
	host := &agentprotocol.DockerOptions{Privileged: true, Devices: []agentprotocol.DeviceMapping{{HostPath: "/dev/dri/renderD128"}}}

	// Restricted server: refused on create (nothing is created), the ungated tier is fine.
	_, err := f.newDocker("gpu", host)
	require.Equal(t, services.CodeRequiresUnrestricted, codeOf(t, err))
	hardened, err := f.newDocker("hardened", &agentprotocol.DockerOptions{CapDrop: []string{"ALL"}, ReadOnlyRootfs: true, PidsLimit: 100})
	require.NoError(t, err)
	v, err := f.deps.Deploy(f.ctx, hardened.ID, "", "")
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeploy(context.Background(), v.ID))
	var pl agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.lastCommand(agentprotocol.KindDeploy).Payload, &pl))
	require.Equal(t, &agentprotocol.DockerPolicy{Unrestricted: false}, pl.DockerPolicy, "the agent is told the server is restricted")
	require.Equal(t, []string{"ALL"}, pl.Docker.CapDrop)
	require.NoError(t, agentValidates(pl), "the agent accepts the ungated tier on a restricted server")

	// Unrestricted server: accepted, planned without a blocker, the payload carries the policy.
	f.setUnrestricted(true)
	gpu, err := f.newDocker("gpu", host)
	require.NoError(t, err)
	plan, err := f.deps.Plan(f.ctx, gpu.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Blocking)
	require.Contains(t, plan.Warnings, host.Warnings()[0], "the plan repeats the options' warnings")
	v, err = f.deps.Deploy(f.ctx, gpu.ID, "", "")
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeploy(context.Background(), v.ID))
	require.NoError(t, json.Unmarshal(f.lastCommand(agentprotocol.KindDeploy).Payload, &pl))
	require.Equal(t, &agentprotocol.DockerPolicy{Unrestricted: true}, pl.DockerPolicy)
	require.True(t, pl.Docker.Privileged)
	require.NoError(t, agentValidates(pl))

	// Switched off afterwards: the plan blocks, a deploy is refused before anything is sent, and the
	// payload would not pass the agent's re-validation without the policy either.
	f.setUnrestricted(false)
	plan, err = f.deps.Plan(f.ctx, gpu.ID)
	require.NoError(t, err)
	var codes []string
	for _, b := range plan.Blocking {
		codes = append(codes, b.Code)
	}
	require.Equal(t, []string{services.CodeRequiresUnrestricted}, codes)
	require.Contains(t, plan.Blocking[0].Message, "devices")
	_, err = f.deps.Deploy(f.ctx, gpu.ID, "", "")
	require.Equal(t, services.CodeRequiresUnrestricted, codeOf(t, err))
	pl.DockerPolicy = &agentprotocol.DockerPolicy{Unrestricted: false}
	require.Error(t, agentValidates(pl))

	// Moving a service that needs it to a restricted server is refused too.
	f.setUnrestricted(true)
	cur, err := f.svcs.Get(f.ctx, gpu.ID)
	require.NoError(t, err)
	other, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.server.WorkspaceID, Name: "plain"})
	require.NoError(t, err)
	_, err = f.svcs.Update(f.ctx, gpu.ID, cur.Generation, services.UpdateInput{ServerID: &other.ID})
	require.Equal(t, services.CodeRequiresUnrestricted, codeOf(t, err))
}

// A read-only root filesystem cannot take config files (docker cp is refused),
// so the plan blocks until each file sits on a volume.
func TestReadOnlyRootfsWithFilesBlocksThePlan(t *testing.T) {
	f := newRouteFixture(t)
	v, err := f.newDocker("ro", &agentprotocol.DockerOptions{ReadOnlyRootfs: true})
	require.NoError(t, err)
	cur, err := f.svcs.Get(f.ctx, v.ID)
	require.NoError(t, err)
	_, err = f.svcs.AddVolume(f.ctx, v.ID, "data", "/data")
	require.NoError(t, err)
	cfg := cur.Config
	cfg.Files = []services.FileMount{{Path: "/etc/app.conf", Content: "x"}}
	cur, err = f.svcs.Get(f.ctx, v.ID)
	require.NoError(t, err)
	_, err = f.svcs.Update(f.ctx, v.ID, cur.Generation, services.UpdateInput{Config: &cfg})
	require.Equal(t, "invalid_config", codeOf(t, err), "the file is not on the volume")

	cfg.Files = []services.FileMount{{Path: "/data/app.conf", Content: "x"}}
	cur, err = f.svcs.Get(f.ctx, v.ID)
	require.NoError(t, err)
	_, err = f.svcs.Update(f.ctx, v.ID, cur.Generation, services.UpdateInput{Config: &cfg})
	require.NoError(t, err)
	plan, err := f.deps.Plan(f.ctx, v.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Blocking)
}
