package deployments

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestRestartPolicyRidesTheDeployPayload(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("api")

	_, plain := f.deployWeb(web.ID)
	require.Empty(t, plain.RestartPolicy)
	require.Zero(t, plain.RestartMaxRetries)

	cfg := services.Config{Image: "traefik/whoami:latest", Port: 80, RestartPolicy: services.RestartOnFailure, RestartMaxRetries: 3}.WithDefaults()
	setConfig(t, f, web.ID, configJSON(t, cfg))
	_, p := f.deployWeb(web.ID)
	require.Equal(t, agentprotocol.RestartOnFailure, p.RestartPolicy)
	require.Equal(t, 3, p.RestartMaxRetries)
}
