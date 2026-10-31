package proxy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func ext(r agentprotocol.Route, mut func(*agentprotocol.RouteExt)) agentprotocol.Route {
	mut(&r.RouteExt)
	return r
}

func extRoute(upstream string) agentprotocol.Route {
	return agentprotocol.Route{
		ServiceID: svcApp, Hosts: []string{"nas.example.com"}, Upstreams: []string{upstream},
		RouteExt: agentprotocol.RouteExt{Kind: agentprotocol.RouteKindExternal},
	}
}

func l4Route(id, proto string, port int, upstream string) agentprotocol.Route {
	return agentprotocol.Route{ServiceID: id, Upstreams: []string{upstream}, RouteExt: agentprotocol.RouteExt{Protocol: proto, ListenPort: port}}
}

func TestValidateRoutesUpstreamPolicyForExternalRoutes(t *testing.T) {
	def := agentprotocol.DefaultUpstreamPolicy()
	for _, up := range []string{"127.0.0.1:8080", "169.254.169.254:80", "nebulactrl-traefik:8080", "metadata.google.internal:80", "localhost:80", "0.0.0.0:80", "[::1]:80"} {
		require.Error(t, ValidateRoutes([]agentprotocol.Route{extRoute(up)}, def), up)
	}
	for _, up := range []string{"192.168.1.20:8123", "100.64.1.2:443", "host.docker.internal:8123", "example.com:443"} {
		require.NoError(t, ValidateRoutes([]agentprotocol.Route{extRoute(up)}, def), up)
	}
	// The admin toggles unlock loopback / link-local, never the proxy's own containers.
	allow := agentprotocol.UpstreamPolicy{AllowLoopback: true, AllowLinkLocal: true, AllowHostGateway: true}
	require.NoError(t, ValidateRoutes([]agentprotocol.Route{extRoute("127.0.0.1:8080")}, allow))
	require.NoError(t, ValidateRoutes([]agentprotocol.Route{extRoute("169.254.169.254:80")}, allow))
	require.Error(t, ValidateRoutes([]agentprotocol.Route{extRoute("nebulactrl-traefik:8080")}, allow))
	// Host gateway disabled per server.
	require.Error(t, ValidateRoutes([]agentprotocol.Route{extRoute("host.docker.internal:80")}, agentprotocol.UpstreamPolicy{}))
}

func TestValidateRoutesPolicyDoesNotApplyToManagedServiceRoutes(t *testing.T) {
	// Replica upstreams are container names the agent itself generated.
	require.NoError(t, ValidateRoutes(twoRoutes(), agentprotocol.UpstreamPolicy{}))
}

func TestValidateRoutesL4(t *testing.T) {
	def := agentprotocol.DefaultUpstreamPolicy()
	ok := []agentprotocol.Route{
		l4Route(svcPG, "tcp", 5432, "nc-pg-aaaaaaaa-0:5432"),
		l4Route(svcDNS, "udp", 5432, "192.168.1.5:53"), // same number, other protocol
	}
	require.NoError(t, ValidateRoutes(ok, def))

	bad := map[string]agentprotocol.Route{
		"port too low":      l4Route(svcPG, "tcp", 80, "a:1"),
		"reserved 443":      l4Route(svcPG, "tcp", 443, "a:1"),
		"reserved probe":    l4Route(svcPG, "tcp", agentprotocol.ProxyProbePort, "a:1"),
		"reserved admin":    l4Route(svcPG, "udp", agentprotocol.ProxyAdminPort, "a:1"),
		"zero port":         l4Route(svcPG, "tcp", 0, "a:1"),
		"loopback upstream": l4Route(svcPG, "tcp", 5432, "127.0.0.1:5432"),
		"metadata upstream": l4Route(svcPG, "udp", 5432, "169.254.169.254:53"),
		"infra upstream":    l4Route(svcPG, "tcp", 5432, "nebulactrl-traefik:80"),
		"hosts on l4": ext(l4Route(svcPG, "tcp", 5432, "a:1"), func(e *agentprotocol.RouteExt) {
			e.Domains = []agentprotocol.DomainRule{{ID: domA, Host: "x.example.com"}}
		}),
		"bad cidr":           ext(l4Route(svcPG, "tcp", 5432, "a:1"), func(e *agentprotocol.RouteExt) { e.AllowCIDRs = []string{"nope"} }),
		"bad proxy protocol": ext(l4Route(svcPG, "tcp", 5432, "a:1"), func(e *agentprotocol.RouteExt) { e.ProxyProtocol = 3 }),
		"udp allowlist":      ext(l4Route(svcPG, "udp", 5432, "a:1"), func(e *agentprotocol.RouteExt) { e.AllowCIDRs = []string{"10.0.0.0/8"} }),
		"udp proxy protocol": ext(l4Route(svcPG, "udp", 5432, "a:1"), func(e *agentprotocol.RouteExt) { e.ProxyProtocol = 1 }),
		"unknown protocol":   l4Route(svcPG, "sctp", 5432, "a:1"),
		"no upstream":        {ServiceID: svcPG, RouteExt: agentprotocol.RouteExt{Protocol: "tcp", ListenPort: 5432}},
		"upstream no port":   l4Route(svcPG, "tcp", 5432, "a"),
	}
	for name, r := range bad {
		require.Error(t, ValidateRoutes([]agentprotocol.Route{r}, def), name)
	}
	hosts := l4Route(svcPG, "tcp", 5432, "a:1")
	hosts.Hosts = []string{"x.example.com"}
	require.Error(t, ValidateRoutes([]agentprotocol.Route{hosts}, def), "an L4 route has no hosts")

	// Duplicates.
	require.Error(t, ValidateRoutes([]agentprotocol.Route{l4Route(svcPG, "tcp", 5432, "a:1"), l4Route(svcDNS, "tcp", 5432, "a:2")}, def), "duplicate listen port")
	require.Error(t, ValidateRoutes([]agentprotocol.Route{l4Route(svcPG, "tcp", 5432, "a:1"), l4Route(svcPG, "tcp", 5433, "a:1")}, def), "same service twice")
	require.NoError(t, ValidateRoutes([]agentprotocol.Route{l4Route(svcPG, "tcp", 5432, "a:1"), l4Route(svcPG, "udp", 5432, "a:1")}, def), "tcp and udp of one service")

	// Allowlist with proxy protocol on tcp is fine.
	tcp := ext(l4Route(svcPG, "tcp", 5432, "a:1"), func(e *agentprotocol.RouteExt) {
		e.AllowCIDRs, e.ProxyProtocol = []string{"10.0.0.0/8", "203.0.113.9"}, 1
	})
	require.NoError(t, ValidateRoutes([]agentprotocol.Route{tcp}, def))
}

func TestValidateRoutesHTTPExtensions(t *testing.T) {
	def := agentprotocol.DefaultUpstreamPolicy()
	good := ruleRoute(rule(domA, "app.example.com", nil))
	require.NoError(t, ValidateRoutes([]agentprotocol.Route{good}, def))

	mut := func(f func(r *agentprotocol.Route)) error {
		r := good
		r.Hosts = append([]string(nil), good.Hosts...)
		r.Domains = append([]agentprotocol.DomainRule(nil), good.Domains...)
		f(&r)
		return ValidateRoutes([]agentprotocol.Route{r}, def)
	}
	require.Error(t, mut(func(r *agentprotocol.Route) { r.Scheme = "ftp" }))
	require.Error(t, mut(func(r *agentprotocol.Route) { r.Kind = "magic" }))
	require.Error(t, mut(func(r *agentprotocol.Route) { r.Protocol = "gopher" }))
	require.Error(t, mut(func(r *agentprotocol.Route) { r.ListenPort = 8080 }), "listen port is tcp/udp only")
	require.Error(t, mut(func(r *agentprotocol.Route) { r.AllowCIDRs = []string{"10.0.0.0/8"} }))
	require.Error(t, mut(func(r *agentprotocol.Route) { r.Transport = &agentprotocol.UpstreamTransport{ServerName: "bad name"} }))
	require.NoError(t, mut(func(r *agentprotocol.Route) {
		r.Scheme, r.Transport = "https", &agentprotocol.UpstreamTransport{ServerName: "nas.lan", InsecureSkipVerify: true}
	}))

	// Domain rules.
	require.Error(t, mut(func(r *agentprotocol.Route) { r.Domains[0].PathPrefix = "api" }), "rule validation runs")
	require.Error(t, mut(func(r *agentprotocol.Route) { r.Domains[0].Host = "other.example.com" }), "a rule host must be listed in hosts")
	require.Error(t, mut(func(r *agentprotocol.Route) { r.Domains[0].ID = "short" }), "domain ids must be identifiers of 8+ chars")
	require.Error(t, mut(func(r *agentprotocol.Route) { r.Domains[0].ID = "aa`bbbbbbbb" }))
	require.Error(t, mut(func(r *agentprotocol.Route) {
		r.Domains = append(r.Domains, agentprotocol.DomainRule{ID: domA[:8] + "-zzzz", Host: "app.example.com", PathPrefix: "/x"})
	}), "the first 8 characters name the routers, so they must be unique")
	require.NoError(t, mut(func(r *agentprotocol.Route) {
		r.Domains = append(r.Domains, agentprotocol.DomainRule{ID: domB, Host: "app.example.com", PathPrefix: "/x"})
	}), "same host, different path")
	require.Error(t, ValidateRoutes([]agentprotocol.Route{good, good}, def), "one http route per service")
}

func TestBasicAuthWithoutUsersIsAHardError(t *testing.T) {
	def := agentprotocol.DefaultUpstreamPolicy()
	r := ruleRoute(rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) {
		d.Options.BasicAuth, d.AuthRef = true, agentprotocol.SecretAuthPrefix+domA
	}))
	err := ValidateRoutes([]agentprotocol.Route{r}, def)
	require.Error(t, err)
	require.Contains(t, err.Error(), "basic auth")
	_, err = BuildConfig([]agentprotocol.Route{r}, Options{Policy: def})
	require.Error(t, err, "an unprotected route is never rendered")

	r.Domains[0].AuthUsers = []string{"alice:plaintext"}
	require.Error(t, ValidateRoutes([]agentprotocol.Route{r}, def), "users must be bcrypt lines")
	r.Domains[0].AuthUsers = []string{"alice:" + bcryptHash}
	require.NoError(t, ValidateRoutes([]agentprotocol.Route{r}, def))
}

func TestResolveSecrets(t *testing.T) {
	r := ruleRoute(
		rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) {
			d.Options.BasicAuth, d.AuthRef = true, agentprotocol.SecretAuthPrefix+domA
		}),
		rule(domB, "open.example.com", nil),
	)
	in := []agentprotocol.Route{r}
	cfg := &agentprotocol.ProxyConfig{Certificates: []agentprotocol.CertRef{{ID: "c1", Name: "wild", KeyRef: agentprotocol.SecretCertPrefix + "c1"}}}
	secrets := map[string]string{
		agentprotocol.SecretAuthPrefix + domA: "alice:" + bcryptHash + "\n\nbob:" + bcryptHash + "\n",
		agentprotocol.SecretCertPrefix + "c1": "KEY-PEM",
		"env:UNRELATED":                       "x",
		agentprotocol.SecretTailnetAuthKey:    "tskey-auth-xyz",
	}

	out, rc, err := ResolveSecrets(in, cfg, secrets)
	require.NoError(t, err)
	require.Equal(t, []string{"alice:" + bcryptHash, "bob:" + bcryptHash}, out[0].Domains[0].AuthUsers)
	require.Empty(t, out[0].Domains[1].AuthUsers, "a domain without basic auth gets no users")
	require.Equal(t, "KEY-PEM", rc.Certificates[0].KeyPEM)
	require.Empty(t, in[0].Domains[0].AuthUsers, "the input is not modified")
	require.Empty(t, cfg.Certificates[0].KeyPEM)

	// A nil config resolves routes only.
	out, rc, err = ResolveSecrets(in, nil, secrets)
	require.NoError(t, err)
	require.Nil(t, rc)
	require.Len(t, out[0].Domains[0].AuthUsers, 2)

	// Already-resolved input survives a command without the secret (scale of a live table).
	resolved := out
	again, _, err := ResolveSecrets(resolved, nil, nil)
	require.NoError(t, err)
	require.Equal(t, resolved[0].Domains[0].AuthUsers, again[0].Domains[0].AuthUsers)
}

func TestResolveSecretsMissingOrInvalidIsAnError(t *testing.T) {
	r := ruleRoute(rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) {
		d.Options.BasicAuth, d.AuthRef = true, agentprotocol.SecretAuthPrefix+domA
	}))
	_, _, err := ResolveSecrets([]agentprotocol.Route{r}, nil, map[string]string{})
	require.Error(t, err, "missing auth secret")
	require.Contains(t, err.Error(), domA)

	const secretHash = "alice:$apr1$THIS-IS-THE-HASH-AND-MUST-NOT-LEAK"
	for _, raw := range []string{"", "\n \n", "alice:plain", "alice:$apr1$x$y", "no-colon"} {
		_, _, err = ResolveSecrets([]agentprotocol.Route{r}, nil, map[string]string{agentprotocol.SecretAuthPrefix + domA: raw})
		require.Error(t, err, raw)
		require.NotContains(t, err.Error(), "THIS-IS-THE-HASH", "hashes never appear in errors")
	}
	_, _, err = ResolveSecrets([]agentprotocol.Route{r}, nil, map[string]string{agentprotocol.SecretAuthPrefix + domA: secretHash + "\n"})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "THIS-IS-THE-HASH")

	cfg := &agentprotocol.ProxyConfig{Certificates: []agentprotocol.CertRef{{ID: "c1", Name: "wild", KeyRef: agentprotocol.SecretCertPrefix + "c1"}}}
	_, _, err = ResolveSecrets(nil, cfg, map[string]string{})
	require.Error(t, err, "missing certificate key")
	require.Contains(t, err.Error(), "c1")
}

func stubLookup(t *testing.T, answers map[string][]string) *[]string {
	t.Helper()
	orig := lookupHost
	t.Cleanup(func() { lookupHost = orig })
	asked := &[]string{}
	lookupHost = func(_ context.Context, host string) ([]string, error) {
		*asked = append(*asked, host)
		if a, ok := answers[host]; ok {
			return a, nil
		}
		return nil, errors.New("no such host")
	}
	return asked
}

func TestPinUpstreamsRejectsNamesPointingAtDeniedRanges(t *testing.T) {
	stubLookup(t, map[string][]string{
		"sneaky.example.com": {"93.184.216.34", "127.0.0.1"},
		"meta.example.com":   {"169.254.169.254"},
		"alibaba.example":    {"100.100.100.200"},
		"fine.example.com":   {"192.168.1.5"},
	})
	def := agentprotocol.DefaultUpstreamPolicy()
	ctx := context.Background()

	_, err := PinUpstreams(ctx, []agentprotocol.Route{extRoute("sneaky.example.com:80")}, def, false)
	require.Error(t, err)
	_, err = PinUpstreams(ctx, []agentprotocol.Route{l4Route(svcPG, "tcp", 5432, "meta.example.com:5432")}, def, false)
	require.Error(t, err)
	_, err = PinUpstreams(ctx, []agentprotocol.Route{extRoute("alibaba.example:80")}, def, false)
	require.Error(t, err, "metadata addresses outside 169.254.0.0/16 are denied too")
	_, err = PinUpstreams(ctx, []agentprotocol.Route{extRoute("fine.example.com:80")}, def, false)
	require.NoError(t, err)
	_, err = PinUpstreams(ctx, []agentprotocol.Route{extRoute("sneaky.example.com:80")}, agentprotocol.UpstreamPolicy{AllowLoopback: true, AllowHostGateway: true}, false)
	require.NoError(t, err)
}

// A name that does not resolve on the agent is refused whatever its spelling:
// the "nc-" prefix is no exemption for a name an operator typed.
func TestPinUpstreamsFailsClosed(t *testing.T) {
	stubLookup(t, nil)
	def := agentprotocol.DefaultUpstreamPolicy()
	ctx := context.Background()
	for _, up := range []string{"unresolvable.example.com:80", "nc-x.attacker.example:80", "nc-web:80", "nas:80"} {
		_, err := PinUpstreams(ctx, []agentprotocol.Route{extRoute(up)}, def, false)
		require.Error(t, err, up)
	}
	_, err := PinUpstreams(ctx, []agentprotocol.Route{l4Route(svcPG, "tcp", 5432, "nc-x.attacker.example:5432")}, def, false)
	require.Error(t, err, "tcp routes too")

	// Tailnet-only names are left to MagicDNS when the server has it.
	_, err = PinUpstreams(ctx, []agentprotocol.Route{extRoute("nas:80")}, def, true)
	require.NoError(t, err)
	_, err = PinUpstreams(ctx, []agentprotocol.Route{extRoute("nas.tail1234.ts.net:80")}, def, true)
	require.NoError(t, err)
	_, err = PinUpstreams(ctx, []agentprotocol.Route{extRoute("nc-x.attacker.example:80")}, def, true)
	require.Error(t, err, "MagicDNS does not exempt a public-looking name")
}

func TestPinUpstreamsLeavesTrustedNamesAlone(t *testing.T) {
	asked := stubLookup(t, nil)
	def := agentprotocol.DefaultUpstreamPolicy()
	system := extRoute("nebulactrl:8080")
	system.System = true
	in := []agentprotocol.Route{
		extRoute("192.168.1.5:80"),
		l4Route(svcPG, "tcp", 5432, "nc-pg-aaaaaaaa-0:5432"),
		extRoute("host.docker.internal:80"),
		twoRoutes()[0], // managed service route
		system,
	}
	out, err := PinUpstreams(context.Background(), in, def, false)
	require.NoError(t, err)
	require.Equal(t, in, out)
	require.Empty(t, *asked, "literal IPs, managed container names, the host gateway, system and managed routes are not resolved")
}

// The table Traefik receives has the resolved addresses, not the name: a name
// that answers differently later cannot reach another address.
func TestPinUpstreamsRendersResolvedAddresses(t *testing.T) {
	stubLookup(t, map[string][]string{"nas.example.com": {"192.168.1.9", "192.168.1.5", "fd00::1"}})
	def := agentprotocol.DefaultUpstreamPolicy()
	in := []agentprotocol.Route{extRoute("nas.example.com:8080")}
	out, err := PinUpstreams(context.Background(), in, def, false)
	require.NoError(t, err)
	require.Equal(t, []string{"nas.example.com:8080"}, in[0].Upstreams, "the input table is not modified")
	require.Equal(t, []string{"192.168.1.5:8080", "192.168.1.9:8080", "[fd00::1]:8080"}, out[0].Upstreams)
	require.Empty(t, out[0].HostOverride)

	// passHost=false and https keep the name for the Host header and TLS.
	noPass := false
	r := extRoute("nas.example.com:8443")
	r.PassHost, r.Scheme = &noPass, "https"
	out, err = PinUpstreams(context.Background(), []agentprotocol.Route{r}, def, false)
	require.NoError(t, err)
	require.Equal(t, "nas.example.com:8443", out[0].HostOverride)
	require.NotNil(t, out[0].Transport)
	require.Equal(t, "nas.example.com", out[0].Transport.ServerName)

	cfg, err := BuildConfig(out, Options{Policy: def})
	require.NoError(t, err)
	require.Contains(t, string(cfg), `"url": "https://192.168.1.5:8443"`)
	require.Contains(t, string(cfg), `"Host": "nas.example.com:8443"`)
	require.Contains(t, string(cfg), `"serverName": "nas.example.com"`)
	require.NotContains(t, string(cfg), `https://nas.example.com`)

	// Two hostnames cannot share one Host override.
	r2 := extRoute("nas.example.com:8443")
	r2.Upstreams = append(r2.Upstreams, "other.example.com:8443")
	r2.PassHost = &noPass
	stubLookup(t, map[string][]string{"nas.example.com": {"192.168.1.5"}, "other.example.com": {"192.168.1.6"}})
	_, err = PinUpstreams(context.Background(), []agentprotocol.Route{r2}, def, false)
	require.Error(t, err)
}

// The uptime probe dials from the agent's own namespace: it is held to the same
// policy and its error reveals nothing about what a name resolves to.
func TestVetProbeHost(t *testing.T) {
	stubLookup(t, map[string][]string{"meta.attacker.example": {"169.254.169.254"}, "nas.example.com": {"192.168.1.9", "192.168.1.5"}})
	def := agentprotocol.DefaultUpstreamPolicy()
	ctx := context.Background()
	r := extRoute("x:80")

	_, err := VetProbeHost(ctx, r, "meta.attacker.example", def)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "169.254")
	_, err = VetProbeHost(ctx, r, "unresolvable.example", def)
	require.Error(t, err)

	addrs, err := VetProbeHost(ctx, r, "nas.example.com", def)
	require.NoError(t, err)
	require.Equal(t, []string{"192.168.1.5", "192.168.1.9"}, addrs)

	for _, host := range []string{"127.0.0.1", "nc-pg-aaaaaaaa-0", agentprotocol.HostGatewayName} {
		addrs, err = VetProbeHost(ctx, r, host, def)
		require.NoError(t, err, host)
		require.Equal(t, []string{host}, addrs, host)
	}
}

func TestErrorsNeverContainBcryptHashes(t *testing.T) {
	r := ruleRoute(rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) {
		d.Options.BasicAuth, d.AuthRef = true, agentprotocol.SecretAuthPrefix+domA
		d.AuthUsers = []string{"alice:" + bcryptHash}
		d.Options.SecurityHeaders = "bogus" // invalid, makes the table fail
	}))
	err := ValidateRoutes([]agentprotocol.Route{r}, agentprotocol.DefaultUpstreamPolicy())
	require.Error(t, err)
	require.False(t, strings.Contains(err.Error(), bcryptHash))
}
