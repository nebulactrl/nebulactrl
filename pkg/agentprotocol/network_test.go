package agentprotocol

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Docker picks a container's default gateway network by name order (before
// Engine 28 offers GwPriority). Traefik is on the edge network and joins
// environment networks: if one of them sorted first, the gateway endpoint
// would change and Docker would re-bind Traefik's published ports. So every
// managed network name must sort after the edge network.
func TestManagedNetworksSortAfterTheEdgeNetwork(t *testing.T) {
	const env, shared = "e0e0e0e0-0000-4000-8000-000000000001", "f1f1f1f1-0000-4000-8000-000000000002"
	names := []string{
		EnvNetwork(env), EnvInternalNetwork(env), SharedNetworkName(shared),
		EnvNetwork("00000000-0000-4000-8000-000000000000"), EnvInternalNetwork("00000000-0000-4000-8000-000000000000"),
		EnvNetwork("ffffffff-0000-4000-8000-000000000000"), SharedNetworkName("00000000-0000-4000-8000-000000000000"),
	}
	for _, n := range names {
		require.Greater(t, n, EdgeNetwork, "%s must sort after %s", n, EdgeNetwork)
	}
	for _, prefix := range []string{EnvNetworkPrefix, SharedNetworkPrefix} {
		require.Greater(t, prefix, EdgeNetwork)
	}
	all := append([]string{EdgeNetwork}, names...)
	sort.Strings(all)
	require.Equal(t, EdgeNetwork, all[0], "the edge network is first in name order")
}

func TestManagedNetworkNames(t *testing.T) {
	const env = "e0e0e0e0-0000-4000-8000-000000000001"
	require.Equal(t, "nebulactrl-env-e0e0e0e0", EnvNetwork(env))
	require.Equal(t, "nebulactrl-env-e0e0e0e0-int", EnvInternalNetwork(env))
	require.Equal(t, "nebulactrl-shared-f1f1f1f1", SharedNetworkName("f1f1f1f1-0000-4000-8000-000000000002"))
	for _, p := range []string{LegacyEnvNetworkPrefix, LegacySharedNetworkPrefix} {
		require.False(t, strings.HasPrefix(EnvNetwork(env), p) || strings.HasPrefix(SharedNetworkName(env), p))
	}
}
