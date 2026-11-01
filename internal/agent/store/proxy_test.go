package store

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestProxyConfigPersistsAcrossReopenWithResolvedKey(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)

	_, ok, err := s.LoadProxyConfig()
	require.NoError(t, err)
	require.False(t, ok, "nothing saved yet")

	cfg := agentprotocol.ProxyConfig{
		Certificates:   []agentprotocol.CertRef{{ID: "c1", Name: "wild", Domains: []string{"*.example.com"}, CertPEM: "CERT", KeyRef: "cert:c1", KeyPEM: "KEY"}},
		UpstreamPolicy: agentprotocol.UpstreamPolicy{AllowHostGateway: true, AllowLoopback: true},
		Tailnet:        &agentprotocol.TailnetSpec{Mode: agentprotocol.TailnetManaged, Hostname: "nebula-a", MagicDNS: true},
	}
	require.NoError(t, s.SaveProxyConfig(cfg))
	require.NoError(t, s.Close())

	s2, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	got, ok, err := s2.LoadProxyConfig()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, cfg, got)
}
