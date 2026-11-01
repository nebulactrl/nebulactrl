package statusroute

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestConfigureReadsTheInstallerUpstream(t *testing.T) {
	t.Cleanup(func() { Set("") })
	Configure(nil)
	_, ok := Upstream()
	require.False(t, ok)

	Configure(&config.Config{})
	_, ok = Upstream()
	require.False(t, ok, "a configuration without an upstream disables the routes")

	Configure(&config.Config{Installer: config.Installer{StatusUpstream: " nebulactrl:8081 "}})
	u, ok := Upstream()
	require.True(t, ok)
	require.Equal(t, "nebulactrl:8081", u)
}

func TestIDsAreStableDistinctAndWellFormed(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	require.Equal(t, ServiceID(a), ServiceID(a))
	require.NotEqual(t, ServiceID(a), ServiceID(b))
	require.NotEqual(t, ServiceID(a), DomainID(a), "route and domain ids differ")
	for _, id := range []string{ServiceID(a), DomainID(a)} {
		_, err := uuid.Parse(id)
		require.NoError(t, err)
	}
}

func TestRouteIsPublicWithBasicHeadersToTheListener(t *testing.T) {
	id := uuid.New()
	r := Route("nebulactrl:8081", "status.example.com", id, "")
	require.Equal(t, ServiceID(id), r.ServiceID)
	require.Equal(t, []string{"status.example.com"}, r.Hosts)
	require.Equal(t, []string{"nebulactrl:8081"}, r.Upstreams)
	require.Equal(t, HealthPath, r.HealthPath)
	require.Equal(t, agentprotocol.RouteKindExternal, r.Kind)
	require.Len(t, r.Domains, 1)
	d := r.Domains[0]
	require.Equal(t, DomainID(id), d.ID)
	require.Equal(t, "status.example.com", d.Host)
	require.Equal(t, agentprotocol.TLSAuto, d.TLSMode)
	require.Empty(t, d.DNSResolver)
	require.Nil(t, d.Options.Access, "status pages are public: no allow list")
	require.Equal(t, "basic", d.Options.SecurityHeaders)

	require.Equal(t, "cloudflare", Route("nebulactrl:8081", "status.example.com", id, "cloudflare").Domains[0].DNSResolver)
}
