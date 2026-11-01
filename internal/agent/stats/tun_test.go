package stats

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func withTunPath(t *testing.T, p string) {
	t.Helper()
	old := tunPath
	tunPath = p
	t.Cleanup(func() { tunPath = old })
}

func TestTunDeviceNeedsACharacterDevice(t *testing.T) {
	t.Setenv("NEBULACTRL_TUN_DEVICE", "")

	withTunPath(t, filepath.Join(t.TempDir(), "absent"))
	require.False(t, TunDevice(), "no /dev/net/tun")

	regular := filepath.Join(t.TempDir(), "tun")
	require.NoError(t, os.WriteFile(regular, nil, 0o600))
	withTunPath(t, regular)
	require.False(t, TunDevice(), "a regular file is not a TUN device")

	withTunPath(t, "/dev/null") // a character device on every unix host
	require.True(t, TunDevice())
}

func TestTunDeviceEnvOverrideForAgentsInContainers(t *testing.T) {
	withTunPath(t, filepath.Join(t.TempDir(), "absent"))
	for _, v := range []string{"1", "true", "YES"} {
		t.Setenv("NEBULACTRL_TUN_DEVICE", v)
		require.True(t, TunDevice(), v)
	}
	withTunPath(t, "/dev/null")
	for _, v := range []string{"0", "false", "No"} {
		t.Setenv("NEBULACTRL_TUN_DEVICE", v)
		require.False(t, TunDevice(), v)
	}
}

func TestFactsAdvertiseMeshAndTunDevice(t *testing.T) {
	t.Setenv("NEBULACTRL_TUN_DEVICE", "1")
	f := New(Options{HTTPPort: 18080, HTTPSPort: 18443}).Facts(context.Background())
	require.True(t, f.TunDevice)
	require.True(t, slices.Contains(f.Capabilities, agentprotocol.CapMesh))

	t.Setenv("NEBULACTRL_TUN_DEVICE", "0")
	f = New(Options{HTTPPort: 18080, HTTPSPort: 18443}).Facts(context.Background())
	require.False(t, f.TunDevice, "a server node needs no TUN: only link nodes do")
	require.True(t, slices.Contains(f.Capabilities, agentprotocol.CapMesh), "mesh membership works on every host")
}

func TestHeartbeatCarriesMeshStatus(t *testing.T) {
	st := &agentprotocol.MeshStatus{TunDevice: true, Nodes: []agentprotocol.MeshNodeStatus{
		{NodeID: "n1", Kind: agentprotocol.MeshKindServer, State: agentprotocol.MeshStateOnline},
	}}
	c := New(Options{MeshStatus: func(context.Context) *agentprotocol.MeshStatus { return st }})
	require.Equal(t, st, c.Heartbeat(context.Background()).Mesh)

	require.Nil(t, New(Options{}).Heartbeat(context.Background()).Mesh, "no mesh manager, no field")
	none := New(Options{MeshStatus: func(context.Context) *agentprotocol.MeshStatus { return nil }})
	require.Nil(t, none.Heartbeat(context.Background()).Mesh, "a host with no nodes reports none")
}
