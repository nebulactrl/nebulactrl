package deployments

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// setCapabilities sets the facts of the fixture's server.
func (f *routeFixture) setCapabilities(caps ...string) {
	f.t.Helper()
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: caps})
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, facts)
	require.NoError(f.t, err)
}

func TestRestartPolicyRidesTheDeployPayload(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("api")
	f.setCapabilities(agentprotocol.CapProxyV2, agentprotocol.CapRestartPolicy)

	_, plain := f.deployWeb(web.ID)
	require.Empty(t, plain.RestartPolicy)
	require.Zero(t, plain.RestartMaxRetries)

	cfg := services.Config{Image: "traefik/whoami:latest", Port: 80, RestartPolicy: services.RestartOnFailure, RestartMaxRetries: 3}.WithDefaults()
	setConfig(t, f, web.ID, configJSON(t, cfg))
	_, p := f.deployWeb(web.ID)
	require.Equal(t, agentprotocol.RestartOnFailure, p.RestartPolicy)
	require.Equal(t, 3, p.RestartMaxRetries)
}

func TestRestartPolicyNeedsACapableAgent(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("api")
	f.setCapabilities(agentprotocol.CapProxyV2)
	before := f.deploymentCount(web.ID)

	cfg := services.Config{Image: "traefik/whoami:latest", Port: 80, RestartPolicy: services.RestartNever}.WithDefaults()
	setConfig(t, f, web.ID, configJSON(t, cfg))

	_, err := f.deps.Deploy(f.ctx, web.ID, "", "")
	requireStatusCode(t, err, 422, "agent_too_old")
	require.Contains(t, err.Error(), "Update the agent on")
	require.Equal(t, before, f.deploymentCount(web.ID), "nothing was persisted")

	plan, err := f.deps.Plan(f.ctx, web.ID)
	require.NoError(t, err)
	var codes []string
	for _, b := range plan.Blocking {
		codes = append(codes, b.Code)
	}
	require.Contains(t, codes, "agent_too_old")

	// The default policy is every agent's behaviour: nothing to refuse.
	cfg.RestartPolicy = services.RestartAlways
	setConfig(t, f, web.ID, configJSON(t, cfg))
	plan, err = f.deps.Plan(f.ctx, web.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Blocking)

	// And prepare itself refuses (a release staged and applied before the agent was downgraded).
	require.Error(t, checkRestartPolicyDeploy(services.Config{RestartPolicy: services.RestartOnFailure}, f.server))
	require.NoError(t, checkRestartPolicyDeploy(services.Config{}, f.server))
}
