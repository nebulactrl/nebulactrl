package reconciler

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func factsWith(t *testing.T, caps ...string) []byte {
	t.Helper()
	raw, err := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: caps})
	require.NoError(t, err)
	return raw
}

func TestInstanceEndpointsOnlyForCapableAgents(t *testing.T) {
	capable := factsWith(t, agentprotocol.CapProxyV2, agentprotocol.CapInstanceTunnel)

	eps := instanceEndpoints(true, capable)
	require.NotNil(t, eps)
	got := map[string]int{}
	for _, e := range eps.Listen {
		got[e.Service] = e.Port
	}
	require.Equal(t, map[string]int{
		agentprotocol.TunnelS3: agentprotocol.LocalPortS3, agentprotocol.TunnelRegistry: agentprotocol.LocalPortRegistry, agentprotocol.TunnelLoki: agentprotocol.LocalPortLoki,
	}, got)

	require.Nil(t, instanceEndpoints(false, capable), "instance services off: nothing offered")
	require.Nil(t, instanceEndpoints(true, factsWith(t, agentprotocol.CapProxyV2)), "an old agent is offered nothing")
	require.Nil(t, instanceEndpoints(true, nil), "no facts yet")
	require.Nil(t, instanceEndpoints(true, []byte("not json")))
}
