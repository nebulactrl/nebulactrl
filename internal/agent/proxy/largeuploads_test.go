package proxy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func registryRoute() agentprotocol.Route {
	return agentprotocol.Route{
		ServiceID: svcApp, Hosts: []string{"registry.example.com"}, Upstreams: []string{"nc-registry-aaaaaaaa-0:5000"},
		Domains: []agentprotocol.DomainRule{{Host: "registry.example.com", Options: agentprotocol.RouteOptions{LargeUploads: true}}},
	}
}

func TestHasLargeUploads(t *testing.T) {
	require.False(t, HasLargeUploads(nil))
	require.False(t, HasLargeUploads(twoRoutes()))
	require.True(t, HasLargeUploads(append(twoRoutes(), registryRoute())))
	plain := registryRoute()
	plain.Domains[0].Options.LargeUploads = false
	require.False(t, HasLargeUploads([]agentprotocol.Route{plain}))
	// One domain with the option among several is enough.
	mixed := registryRoute()
	mixed.Domains = append([]agentprotocol.DomainRule{{Host: "other.example.com"}}, mixed.Domains...)
	require.True(t, HasLargeUploads([]agentprotocol.Route{mixed}))
}

// Traefik has no per-router read timeout: the incoming read timeout is the
// entrypoint's, a static flag. It is raised only when the table asks for it.
func TestStaticFlagsRaiseTheReadTimeoutOnlyForLargeUploads(t *testing.T) {
	plain := strings.Join(StaticFlags(StaticOptions{PublicHTTPSPort: 443}), "\n")
	require.NotContains(t, plain, "respondingtimeouts", "other servers keep Traefik's default read timeout")

	long := StaticFlags(StaticOptions{PublicHTTPSPort: 443, LargeUploads: true})
	require.Contains(t, long, "--entrypoints.websecure.transport.respondingtimeouts.readtimeout=30m")
	require.Contains(t, long, "--entrypoints.web.transport.respondingtimeouts.readtimeout=30m")
	require.Equal(t, "30m", LargeUploadReadTimeout)
	joined := strings.Join(long, "\n")
	require.NotContains(t, joined, "entrypoints.probe.transport", "the internal entrypoints are untouched")
	require.NotContains(t, joined, "entrypoints.ncadmin.transport")
	require.NotContains(t, joined, "writetimeout", "writes (large pulls) are already unbounded by default")

	// Everything else is the same set of flags.
	require.Len(t, long, len(StaticFlags(StaticOptions{PublicHTTPSPort: 443}))+2)
}

// The flag is part of the container's spec fingerprint, so the first registry
// route recreates the proxy once (like any static setting) and removing the last
// one recreates it again.
func TestSpecChangesWhenTheTableGainsALargeUploadsDomain(t *testing.T) {
	m, _, _ := newTestManager(t, &memRouteStore{})
	without := m.specFor(m.inputsFor(twoRoutes(), m.ProxyConfig()))
	with := m.specFor(m.inputsFor(append(twoRoutes(), registryRoute()), m.ProxyConfig()))
	require.NotEqual(t, without.Labels[LabelSpec], with.Labels[LabelSpec])
	require.Contains(t, with.Cmd, "--entrypoints.websecure.transport.respondingtimeouts.readtimeout=30m")
	require.NotContains(t, strings.Join(without.Cmd, " "), "respondingtimeouts")
	// The same table twice gives the same fingerprint: no needless recreate.
	again := m.specFor(m.inputsFor(append(twoRoutes(), registryRoute()), m.ProxyConfig()))
	require.Equal(t, with.Labels[LabelSpec], again.Labels[LabelSpec])
}

func TestLargeUploadsOptionRoundTrips(t *testing.T) {
	opts := agentprotocol.RouteOptions{LargeUploads: true}
	require.False(t, opts.IsZero())
	require.NoError(t, opts.Validate())
	require.True(t, agentprotocol.RouteOptions{}.IsZero())
}
