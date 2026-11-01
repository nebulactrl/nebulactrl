package proxy

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/statusroute"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// A published status page's route, rendered through the real builder: a public
// https router for the page's host (no allow list), the basic security
// headers, and the status listener as the only upstream with a /healthz check.
// The builder has no special case for it: this pins what the control plane's
// status routes produce.
func TestStatusPageRoute(t *testing.T) {
	page := uuid.MustParse("0b0b0b0b-1111-4222-8333-444444444444")
	n := "nc-" + statusroute.ServiceID(page) + "-d" + statusroute.DomainID(page)[:8]
	opts := Options{ACMEEmail: "ops@example.com", DNSResolvers: []agentprotocol.DNSResolver{dnsResolver("cloudflare", map[string]string{"CF_DNS_API_TOKEN": "tok"}, nil)}}

	t.Run("HTTP-01", func(t *testing.T) {
		route := statusroute.Route("nebulactrl:8081", "status.example.com", page, "")
		p := parse(t, goldenConfigNoFile(t, []agentprotocol.Route{route}, opts))

		https := p.HTTP.Routers[n+"-https"]
		require.NotNil(t, https, "the page's host has an https router")
		require.Equal(t, "Host(`status.example.com`)", https["rule"])
		require.Equal(t, map[string]any{"certResolver": CertResolver}, https["tls"])
		require.Contains(t, strs(https["middlewares"]), n+"-headers")
		for name := range p.HTTP.Middlewares {
			require.NotContains(t, name, "ipallow", "status pages are public")
		}
		hdr := p.HTTP.Middlewares[n+"-headers"]["headers"].(map[string]any)
		require.Equal(t, true, hdr["contentTypeNosniff"])
		require.Equal(t, true, hdr["frameDeny"])

		lb := p.HTTP.Services[https["service"].(string)]["loadBalancer"].(map[string]any)
		require.Equal(t, []any{map[string]any{"url": "http://nebulactrl:8081"}}, lb["servers"], "only the status listener")
		require.Equal(t, true, lb["passHostHeader"], "the listener looks the page up by Host")
		require.Equal(t, "/healthz", lb["healthCheck"].(map[string]any)["path"])
	})

	t.Run("DNS-01", func(t *testing.T) {
		route := statusroute.Route("nebulactrl:8081", "status.example.com", page, "cloudflare")
		p := parse(t, goldenConfigNoFile(t, []agentprotocol.Route{route}, opts))
		require.Equal(t, map[string]any{"certResolver": "nc-dns-cloudflare"}, p.HTTP.Routers[n+"-https"]["tls"])
		require.NotContains(t, string(goldenConfigNoFile(t, []agentprotocol.Route{route}, opts)), "tok\"", "no credential in the dynamic config")
	})

	t.Run("DNS-01 resolver not applied yet falls back to HTTP-01", func(t *testing.T) {
		route := statusroute.Route("nebulactrl:8081", "status.example.com", page, "cloudflare")
		p := parse(t, goldenConfigNoFile(t, []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com"}))
		require.Equal(t, map[string]any{"certResolver": CertResolver}, p.HTTP.Routers[n+"-https"]["tls"])
	})

	t.Run("two pages keep separate objects", func(t *testing.T) {
		other := uuid.MustParse("0c0c0c0c-1111-4222-8333-444444444444")
		routes := []agentprotocol.Route{
			statusroute.Route("nebulactrl:8081", "status.example.com", page, ""),
			statusroute.Route("nebulactrl:8081", "status.other.org", other, ""),
		}
		p := parse(t, goldenConfigNoFile(t, routes, opts))
		m := "nc-" + statusroute.ServiceID(other) + "-d" + statusroute.DomainID(other)[:8]
		require.Equal(t, "Host(`status.other.org`)", p.HTTP.Routers[m+"-https"]["rule"])
		require.Equal(t, "Host(`status.example.com`)", p.HTTP.Routers[n+"-https"]["rule"])
	})
}
