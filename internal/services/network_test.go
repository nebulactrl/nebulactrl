package services

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestRouteNetworks(t *testing.T) {
	env := uuid.New()
	require.Equal(t, []string{agentprotocol.EnvNetwork(env.String())}, RouteNetworks(env, false))
	require.Equal(t, []string{agentprotocol.EnvInternalNetwork(env.String())}, RouteNetworks(env, true))
}

func TestReleaseBlocksEgress(t *testing.T) {
	snap := func(block bool) json.RawMessage {
		raw, err := json.Marshal(Snapshot{Config: Config{Image: "x:1", BlockEgress: block}})
		require.NoError(t, err)
		return raw
	}
	require.True(t, ReleaseBlocksEgress(snap(true)))
	require.False(t, ReleaseBlocksEgress(snap(false)))
	require.False(t, ReleaseBlocksEgress(nil), "no release: not blocked")
}

func TestBlockEgressIsStagedConfig(t *testing.T) {
	a := Config{Image: "x:1", Port: 80}.WithDefaults()
	b := a
	b.BlockEgress = true
	changes := DiffConfig("config", a, b)
	require.Len(t, changes, 1)
	require.Equal(t, "config.blockEgress", changes[0].Field)
	require.Equal(t, "true", changes[0].To)

	// A route service has no container to isolate.
	route := Config{Route: &RouteConfig{}, BlockEgress: true}.WithDefaults()
	require.ErrorIs(t, route.Validate(SourceExternal), ErrInvalidConfig)
}

func TestSnapshotNetworksCarryAliasesToTheAgent(t *testing.T) {
	id := uuid.New()
	got := NetworkAttachments(SnapshotNetworks([]NetworkRef{{ID: id, Name: "Backend", Alias: "api.production.shop"}}))
	require.Equal(t, []agentprotocol.SharedNetworkAttachment{{Name: agentprotocol.SharedNetworkName(id.String()), Alias: "api.production.shop"}}, got)
}
