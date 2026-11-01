package proxy

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	domA = "aaaaaaaa-1111-4111-8111-111111111111"
	domB = "bbbbbbbb-2222-4222-8222-222222222222"
	domC = "cccccccc-3333-4333-8333-333333333333"

	bcryptHash = "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234"
)

func ruleRoute(rules ...agentprotocol.DomainRule) agentprotocol.Route {
	hosts := make([]string, 0, len(rules))
	seen := map[string]bool{}
	for _, r := range rules {
		if !seen[r.Host] {
			seen[r.Host] = true
			hosts = append(hosts, r.Host)
		}
	}
	return agentprotocol.Route{ServiceID: svcApp, Hosts: hosts, Upstreams: []string{"nc-app-aaaaaaaa-0:8080"}, Domains: rules}
}

func rule(id, host string, mut func(*agentprotocol.DomainRule)) agentprotocol.DomainRule {
	d := agentprotocol.DomainRule{ID: id, Host: host}
	if mut != nil {
		mut(&d)
	}
	return d
}

func goldenConfig(t *testing.T, name string, routes []agentprotocol.Route, opts Options) []byte {
	t.Helper()
	if opts.Generation == 0 {
		opts.Generation = 1
	}
	got, err := BuildConfig(routes, opts)
	require.NoError(t, err)
	golden(t, name+".golden.json", got)
	return got
}

type parsed struct {
	HTTP struct {
		Routers     map[string]map[string]any `json:"routers"`
		Services    map[string]map[string]any `json:"services"`
		Middlewares map[string]map[string]any `json:"middlewares"`
		Transports  map[string]map[string]any `json:"serversTransports"`
	} `json:"http"`
	TCP struct {
		Routers     map[string]map[string]any `json:"routers"`
		Services    map[string]map[string]any `json:"services"`
		Middlewares map[string]map[string]any `json:"middlewares"`
	} `json:"tcp"`
	UDP struct {
		Routers  map[string]map[string]any `json:"routers"`
		Services map[string]map[string]any `json:"services"`
	} `json:"udp"`
	TLS struct {
		Certificates []map[string]string `json:"certificates"`
	} `json:"tls"`
}

func parse(t *testing.T, raw []byte) parsed {
	t.Helper()
	var p parsed
	require.NoError(t, json.Unmarshal(raw, &p))
	return p
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestGoldenPathPrefixStripAndAdd(t *testing.T) {
	r := ruleRoute(rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) {
		d.PathPrefix, d.StripPrefix, d.AddPrefix = "/api", true, "/v2"
	}))
	p := parse(t, goldenConfig(t, "path_prefix", []agentprotocol.Route{r}, Options{ACMEEmail: "ops@example.com"}))
	n := "nc-" + svcApp + "-daaaaaaaa"
	require.Equal(t, "Host(`app.example.com`) && PathPrefix(`/api`)", p.HTTP.Routers[n+"-https"]["rule"])
	require.Equal(t, []string{n + "-strip", n + "-addprefix"}, strs(p.HTTP.Routers[n+"-https"]["middlewares"]))
	require.Equal(t, []string{n + "-strip", n + "-addprefix"}, strs(p.HTTP.Routers[n+"-probe"]["middlewares"]), "probe keeps the prefix rewrites")
	require.Equal(t, map[string]any{"prefixes": []any{"/api"}}, p.HTTP.Middlewares[n+"-strip"]["stripPrefix"])
	require.Equal(t, map[string]any{"prefix": "/v2"}, p.HTTP.Middlewares[n+"-addprefix"]["addPrefix"])
}

func TestGoldenEachMiddleware(t *testing.T) {
	cases := []struct {
		name string
		opts agentprotocol.RouteOptions
		kind string // middleware suffix expected
		key  string // dynamic middleware field
	}{
		{"mw_access", agentprotocol.RouteOptions{Access: &agentprotocol.Access{AllowCIDRs: []string{"10.0.0.0/8", "203.0.113.7"}, TrustedProxyDepth: 2, RejectStatus: 404}}, "ipallow", "ipAllowList"},
		{"mw_ratelimit", agentprotocol.RouteOptions{RateLimit: &agentprotocol.RateLimit{Average: 100, PeriodSeconds: 1, Burst: 50, TrustedProxyDepth: 1}}, "ratelimit", "rateLimit"},
		{"mw_redirects", agentprotocol.RouteOptions{Redirects: []agentprotocol.Redirect{{Regex: `^https://app.example.com/old/(.*)`, Replacement: "https://app.example.com/new/${1}", Permanent: true}, {Regex: `^/a$`, Replacement: "/b"}}}, "redirect1", "redirectRegex"},
		{"mw_headers_security_basic", agentprotocol.RouteOptions{SecurityHeaders: "basic"}, "headers", "headers"},
		{"mw_headers_security_strict", agentprotocol.RouteOptions{SecurityHeaders: "strict"}, "headers", "headers"},
		{"mw_cors", agentprotocol.RouteOptions{CORS: &agentprotocol.CORS{Origins: []string{"https://a.example.com", "http://localhost:3000"}, Methods: []string{"GET", "POST"}, Headers: []string{"Authorization"}, ExposeHeaders: []string{"X-Total"}, Credentials: true, MaxAgeSeconds: 600}}, "headers", "headers"},
		{"mw_custom_headers", agentprotocol.RouteOptions{RequestHeaders: []agentprotocol.Header{{Name: "X-Env", Value: "prod"}, {Name: "X-Strip-Me", Value: ""}}, ResponseHeaders: []agentprotocol.Header{{Name: "Cache-Control", Value: "no-store"}}}, "headers", "headers"},
		{"mw_noindex", agentprotocol.RouteOptions{Noindex: true}, "headers", "headers"},
		{"mw_compress", agentprotocol.RouteOptions{Compress: true}, "compress", "compress"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ruleRoute(rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) { d.Options = c.opts }))
			p := parse(t, goldenConfig(t, c.name, []agentprotocol.Route{r}, Options{}))
			n := "nc-" + svcApp + "-daaaaaaaa"
			mw := p.HTTP.Middlewares[n+"-"+c.kind]
			require.NotNil(t, mw, "middleware %s emitted", c.kind)
			require.Contains(t, mw, c.key)
			require.Contains(t, strs(p.HTTP.Routers[n+"-https"]["middlewares"]), n+"-"+c.kind)
			require.NotContains(t, p.HTTP.Routers[n+"-probe"], "middlewares", "access-rule middlewares are not on the probe router")
		})
	}
}

func TestGoldenBasicAuth(t *testing.T) {
	r := ruleRoute(rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) {
		d.Options.BasicAuth, d.AuthRef = true, agentprotocol.SecretAuthPrefix+domA
		d.AuthUsers = []string{"alice:" + bcryptHash, "bob:" + bcryptHash}
	}))
	p := parse(t, goldenConfig(t, "mw_basicauth", []agentprotocol.Route{r}, Options{}))
	n := "nc-" + svcApp + "-daaaaaaaa"
	require.Equal(t, map[string]any{"users": []any{"alice:" + bcryptHash, "bob:" + bcryptHash}}, p.HTTP.Middlewares[n+"-auth"]["basicAuth"])
	require.Contains(t, strs(p.HTTP.Routers[n+"-https"]["middlewares"]), n+"-auth")
	require.NotContains(t, p.HTTP.Routers[n+"-probe"], "middlewares", "the probe router is not protected, so verification checks routing only")
}

func TestMiddlewareChainOrder(t *testing.T) {
	r := ruleRoute(rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) {
		d.PathPrefix, d.StripPrefix, d.AddPrefix = "/api", true, "/v2"
		d.AuthRef, d.AuthUsers = agentprotocol.SecretAuthPrefix+domA, []string{"a:" + bcryptHash}
		d.Options = agentprotocol.RouteOptions{
			Compress: true, Noindex: true, SecurityHeaders: "strict",
			Redirects: []agentprotocol.Redirect{{Regex: "^/a$", Replacement: "/b"}, {Regex: "^/c$", Replacement: "/d"}},
			RateLimit: &agentprotocol.RateLimit{Average: 1, PeriodSeconds: 1, Burst: 1},
			Access:    &agentprotocol.Access{AllowCIDRs: []string{"10.0.0.0/8"}},
			BasicAuth: true,
		}
	}))
	p := parse(t, goldenConfig(t, "mw_chain_order", []agentprotocol.Route{r}, Options{}))
	n := "nc-" + svcApp + "-daaaaaaaa-"
	want := []string{n + "ipallow", n + "ratelimit", n + "auth", n + "redirect1", n + "redirect2", n + "strip", n + "addprefix", n + "headers", n + "compress"}
	require.Equal(t, want, strs(p.HTTP.Routers[n+"https"]["middlewares"]), "cheapest rejection first")
	require.Equal(t, []string{n + "strip", n + "addprefix"}, strs(p.HTTP.Routers[n+"probe"]["middlewares"]))
	for _, name := range want {
		require.Contains(t, p.HTTP.Middlewares, name)
	}
}

func TestHeadersMapping(t *testing.T) {
	h := headersFor(agentprotocol.RouteOptions{
		SecurityHeaders: "strict", Noindex: true,
		RequestHeaders:  []agentprotocol.Header{{Name: "X-A", Value: "1"}},
		ResponseHeaders: []agentprotocol.Header{{Name: "X-B", Value: "2"}},
		CORS:            &agentprotocol.CORS{Origins: []string{"*"}, Methods: []string{"GET"}, MaxAgeSeconds: 10},
	})
	require.NotNil(t, h)
	require.True(t, h.ContentTypeNosniff)
	require.True(t, h.FrameDeny)
	require.Equal(t, "strict-origin-when-cross-origin", h.ReferrerPolicy)
	require.Equal(t, int64(31536000), h.STSSeconds)
	require.True(t, h.STSIncludeSubdomains)
	require.Equal(t, map[string]string{"X-A": "1"}, h.CustomRequestHeaders)
	require.Equal(t, map[string]string{"X-B": "2", "X-Robots-Tag": "noindex, nofollow"}, h.CustomResponseHeaders)
	require.Equal(t, []string{"*"}, h.AccessControlAllowOriginList)
	require.Equal(t, int64(10), h.AccessControlMaxAge)

	basic := headersFor(agentprotocol.RouteOptions{SecurityHeaders: "basic"})
	require.Zero(t, basic.STSSeconds, "HSTS only for strict")
	require.Nil(t, headersFor(agentprotocol.RouteOptions{Compress: true}))
}

func TestGoldenRedirectDomain(t *testing.T) {
	routes := []agentprotocol.Route{ruleRoute(
		rule(domA, "example.com", nil),
		rule(domB, "www.example.com", func(d *agentprotocol.DomainRule) {
			d.Kind, d.RedirectTo, d.RedirectCode, d.PreservePath = agentprotocol.DomainRedirect, "example.com", 301, true
		}),
		rule(domC, "old.example.com", func(d *agentprotocol.DomainRule) {
			d.Kind, d.RedirectTo, d.RedirectCode = agentprotocol.DomainRedirect, "example.com", 302
		}),
	)}
	p := parse(t, goldenConfig(t, "redirect_domain", routes, Options{ACMEEmail: "ops@example.com", PublicHTTPSPort: 8443}))
	n := "nc-" + svcApp + "-dbbbbbbbb"

	redir := p.HTTP.Routers[n+"-redir"]
	require.Equal(t, "noop@internal", redir["service"], "a redirect domain never touches an upstream")
	require.Equal(t, map[string]any{"certResolver": "le"}, redir["tls"], "it needs a certificate like a serve domain")
	rx := p.HTTP.Middlewares[n+"-redir"]["redirectRegex"].(map[string]any)
	require.Equal(t, "https://example.com:8443${1}", rx["replacement"])
	require.Equal(t, true, rx["permanent"])
	require.Equal(t, "noop@internal", p.HTTP.Routers[n+"-http"]["service"])
	require.Equal(t, []string{n + "-redir"}, strs(p.HTTP.Routers[n+"-http"]["middlewares"]))
	require.NotContains(t, p.HTTP.Routers, n+"-probe", "nothing to verify on a redirect")

	nc := "nc-" + svcApp + "-dcccccccc"
	rc := p.HTTP.Middlewares[nc+"-redir"]["redirectRegex"].(map[string]any)
	require.Equal(t, "https://example.com:8443/", rc["replacement"], "without preservePath the path is dropped")
	require.Equal(t, false, rc["permanent"])
}

func TestTLSModes(t *testing.T) {
	route := ruleRoute(
		rule(domA, "plain.example.com", func(d *agentprotocol.DomainRule) { d.TLSMode = agentprotocol.TLSNone }),
		rule(domB, "custom.example.com", func(d *agentprotocol.DomainRule) { d.TLSMode = agentprotocol.TLSCustom }),
		rule(domC, "both.example.com", func(d *agentprotocol.DomainRule) { d.Options.ForceHTTPS = new(false) }),
	)
	p := parse(t, goldenConfig(t, "tls_modes", []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com"}))
	a, b, c := "nc-"+svcApp+"-daaaaaaaa", "nc-"+svcApp+"-dbbbbbbbb", "nc-"+svcApp+"-dcccccccc"

	require.NotContains(t, p.HTTP.Routers, a+"-https", "tlsMode none renders no TLS router")
	require.Equal(t, "nc-"+svcApp, p.HTTP.Routers[a+"-http"]["service"], "and the http router serves the service")
	require.NotContains(t, p.HTTP.Routers[a+"-http"], "middlewares", "without a redirect")

	require.Equal(t, map[string]any{}, p.HTTP.Routers[b+"-https"]["tls"], "custom never asks ACME")
	require.Equal(t, "noop@internal", p.HTTP.Routers[b+"-http"]["service"], "custom still redirects http to https")

	require.Equal(t, map[string]any{"certResolver": "le"}, p.HTTP.Routers[c+"-https"]["tls"])
	require.Equal(t, "nc-"+svcApp, p.HTTP.Routers[c+"-http"]["service"], "forceHttps=false serves over http too")
}

func TestUploadedCertificateTakesPrecedenceOverACME(t *testing.T) {
	crt, key := testCert(t)
	cert := agentprotocol.CertRef{ID: "c1", Name: "wild", Domains: []string{"*.example.com", "example.com"}, CertPEM: crt, KeyPEM: key}
	route := ruleRoute(rule(domA, "app.example.com", nil), rule(domB, "app.other.org", nil))
	p := parse(t, goldenConfig(t, "custom_cert", []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com", Certificates: []agentprotocol.CertRef{cert}}))

	require.Len(t, p.TLS.Certificates, 1)
	require.Equal(t, crt, p.TLS.Certificates[0]["certFile"], "inline PEM, no file paths")
	require.Equal(t, key, p.TLS.Certificates[0]["keyFile"])
	require.Equal(t, map[string]any{}, p.HTTP.Routers["nc-"+svcApp+"-daaaaaaaa-https"]["tls"], "a covered name is served from the store, not ACME")
	require.Equal(t, map[string]any{"certResolver": "le"}, p.HTTP.Routers["nc-"+svcApp+"-dbbbbbbbb-https"]["tls"], "an uncovered name still uses ACME")
}

func TestCertificateWithoutKeyOrBadPairIsNeverEmitted(t *testing.T) {
	crt, key := testCert(t)
	noKey := agentprotocol.CertRef{ID: "c1", Domains: []string{"a.example.com"}, CertPEM: crt}
	bad := agentprotocol.CertRef{ID: "c2", Domains: []string{"b.example.com"}, CertPEM: crt, KeyPEM: "-----BEGIN PRIVATE KEY-----\nnope\n-----END PRIVATE KEY-----"}
	ok := agentprotocol.CertRef{ID: "c3", Domains: []string{"c.example.com"}, CertPEM: crt, KeyPEM: key}
	raw, err := BuildConfig(twoRoutes(), Options{Generation: 1, Certificates: []agentprotocol.CertRef{noKey, bad, ok}})
	require.NoError(t, err)
	require.Len(t, parse(t, raw).TLS.Certificates, 1)

	usable, status := CertStatuses([]agentprotocol.CertRef{noKey, bad, ok})
	require.Len(t, usable, 1)
	require.Equal(t, "c3", usable[0].ID)
	require.Len(t, status, 3)
	require.False(t, status[0].Loaded)
	require.NotEmpty(t, status[0].Error)
	require.False(t, status[1].Loaded)
	require.True(t, status[2].Loaded)
	require.Empty(t, status[2].Error)

	raw, err = BuildConfig(twoRoutes(), Options{Generation: 1})
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"tls": {`+"\n"+`    "certificates"`, "no certificates, no tls block")
	require.Empty(t, parse(t, raw).TLS.Certificates)
}

func TestCertCovers(t *testing.T) {
	require.True(t, certCovers([]string{"*.example.com"}, "a.example.com"))
	require.True(t, certCovers([]string{"*.example.com"}, "A.Example.COM"))
	require.False(t, certCovers([]string{"*.example.com"}, "a.b.example.com"), "a wildcard matches one label")
	require.False(t, certCovers([]string{"*.example.com"}, "example.com"))
	require.True(t, certCovers([]string{"example.com"}, "example.com"))
	require.False(t, certCovers([]string{"example.com"}, "evilexample.com"))
	require.False(t, certCovers(nil, "example.com"))
}

func TestGoldenExternalHTTPSUpstreamWithTransport(t *testing.T) {
	pass := false
	r := agentprotocol.Route{
		ServiceID: svcApp, Hosts: []string{"nas.example.com"}, Upstreams: []string{"192.168.1.20:8443", "192.168.1.21:8443"},
		HealthPath: "/healthz",
		RouteExt: agentprotocol.RouteExt{
			Kind: agentprotocol.RouteKindExternal, Scheme: "https", PassHost: &pass,
			Transport: &agentprotocol.UpstreamTransport{InsecureSkipVerify: true, ServerName: "nas.lan"},
		},
	}
	p := parse(t, goldenConfig(t, "external_https", []agentprotocol.Route{r}, Options{Policy: agentprotocol.DefaultUpstreamPolicy()}))
	svc := p.HTTP.Services["nc-"+svcApp]["loadBalancer"].(map[string]any)
	require.Equal(t, false, svc["passHostHeader"])
	require.Equal(t, "nc-"+svcApp, svc["serversTransport"])
	require.Equal(t, []any{map[string]any{"url": "https://192.168.1.20:8443"}, map[string]any{"url": "https://192.168.1.21:8443"}}, svc["servers"])
	require.Equal(t, map[string]any{"insecureSkipVerify": true, "serverName": "nas.lan"}, p.HTTP.Transports["nc-"+svcApp])
}

func TestGoldenTCPRouteWithAllowlist(t *testing.T) {
	r := agentprotocol.Route{
		ServiceID: svcApp, Upstreams: []string{"nc-pg-aaaaaaaa-0:5432"},
		RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoTCP, ListenPort: 5432, AllowCIDRs: []string{"203.0.113.0/24", "10.0.0.7"}, ProxyProtocol: 2},
	}
	p := parse(t, goldenConfig(t, "tcp_route", []agentprotocol.Route{r}, Options{}))
	n := "nc-" + svcApp + "-tcp"
	rt := p.TCP.Routers[n]
	require.Equal(t, []string{"tcp-5432"}, strs(rt["entryPoints"]))
	require.Equal(t, "HostSNI(`*`)", rt["rule"])
	require.Equal(t, []string{n + "-allow"}, strs(rt["middlewares"]))
	require.Equal(t, map[string]any{"ipAllowList": map[string]any{"sourceRange": []any{"203.0.113.0/24", "10.0.0.7"}}}, p.TCP.Middlewares[n+"-allow"])
	lb := p.TCP.Services[n]["loadBalancer"].(map[string]any)
	require.Equal(t, map[string]any{"version": float64(2)}, lb["proxyProtocol"])
	require.Equal(t, []any{map[string]any{"address": "nc-pg-aaaaaaaa-0:5432"}}, lb["servers"])
	require.Empty(t, p.HTTP.Middlewares, "an L4-only table renders no http middlewares")
}

func TestGoldenUDPRoute(t *testing.T) {
	r := agentprotocol.Route{
		ServiceID: svcApp, Upstreams: []string{"192.168.1.9:51820"},
		RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoUDP, ListenPort: 51820, Kind: agentprotocol.RouteKindExternal},
	}
	p := parse(t, goldenConfig(t, "udp_route", []agentprotocol.Route{r}, Options{Policy: agentprotocol.DefaultUpstreamPolicy()}))
	n := "nc-" + svcApp + "-udp"
	require.Equal(t, map[string]any{"entryPoints": []any{"udp-51820"}, "service": n}, p.UDP.Routers[n])
	require.Equal(t, map[string]any{"loadBalancer": map[string]any{"servers": []any{map[string]any{"address": "192.168.1.9:51820"}}}}, p.UDP.Services[n])
	require.Empty(t, p.TCP.Routers)
}

func TestRouterNamesAndL4RouterNames(t *testing.T) {
	routes := append(twoRoutes(),
		agentprotocol.Route{ServiceID: svcPG, Upstreams: []string{"a:1"}, RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoTCP, ListenPort: 5432}},
		agentprotocol.Route{ServiceID: svcDNS, Upstreams: []string{"a:2"}, RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoUDP, ListenPort: 5353}},
	)
	tcp, udp := L4RouterNames(routes, Options{})
	require.Equal(t, []string{"nc-" + svcPG + "-tcp"}, tcp)
	require.Equal(t, []string{"nc-" + svcDNS + "-udp"}, udp)
	for _, n := range RouterNames(routes, Options{}) {
		require.NotContains(t, n, "-tcp")
		require.NotContains(t, n, "-udp")
	}
}

const (
	svcPG  = "33333333-3333-3333-3333-333333333333"
	svcDNS = "44444444-4444-4444-4444-444444444444"
)

func TestHostsWithoutRuleKeepDefaultRouterNames(t *testing.T) {
	r := ruleRoute(rule(domA, "ruled.example.com", nil))
	r.Hosts = append(r.Hosts, "plain.example.com")
	raw, err := BuildConfig([]agentprotocol.Route{r}, Options{ACMEEmail: "a@b.c", Generation: 1})
	require.NoError(t, err)
	p := parse(t, raw)
	require.Contains(t, p.HTTP.Routers, "nc-"+svcApp+"-https", "hosts without a rule are served with defaults under the service-level names")
	require.Equal(t, "Host(`plain.example.com`)", p.HTTP.Routers["nc-"+svcApp+"-https"]["rule"])
	require.Contains(t, p.HTTP.Routers, "nc-"+svcApp+"-daaaaaaaa-https")
}

func TestEmptyDomainsMatchesDefaultGolden(t *testing.T) {
	// The default output (no domain rules) is byte-for-byte the default golden.
	got, err := BuildConfig(twoRoutes(), Options{ACMEEmail: "ops@example.com", PublicHTTPSPort: 8443, Generation: 42})
	require.NoError(t, err)
	want, err := os.ReadFile("testdata/dynamic.golden.json")
	require.NoError(t, err)
	require.Equal(t, strings.TrimSpace(string(want)), strings.TrimSpace(string(got)))
	require.NotContains(t, string(got), `"tcp"`)
	require.NotContains(t, string(got), `"udp"`)
	require.NotContains(t, string(got), `"tls"`+`: {`+"\n"+`    "certificates"`)
}

func TestStaticFlagsL4Golden(t *testing.T) {
	flags := StaticFlags(StaticOptions{
		ACMEEmail: "ops@example.com", PublicHTTPSPort: 8443, TrafficAnalytics: true,
		L4Ports: []L4Port{{Proto: "tcp", Port: 5432}, {Proto: "udp", Port: 5432}, {Proto: "udp", Port: 51820}},
	})
	golden(t, "static_l4.golden.txt", []byte(strings.Join(flags, "\n")))
	joined := strings.Join(flags, "\n")
	require.Contains(t, joined, "--entrypoints.tcp-5432.address=:5432/tcp")
	require.Contains(t, joined, "--entrypoints.udp-5432.address=:5432/udp")
	require.Contains(t, joined, "--entrypoints.udp-51820.address=:51820/udp")
	require.NotContains(t, strings.Join(StaticFlags(StaticOptions{}), "\n"), "tcp-", "no L4 ports, no extra entrypoints")
}

func TestL4PortsDerivation(t *testing.T) {
	routes := []agentprotocol.Route{
		{ServiceID: "a", Upstreams: []string{"x:1"}, RouteExt: agentprotocol.RouteExt{Protocol: "udp", ListenPort: 9000 + 5}},
		{ServiceID: "b", Upstreams: []string{"x:1"}, RouteExt: agentprotocol.RouteExt{Protocol: "tcp", ListenPort: 9005}},
		{ServiceID: "c", Upstreams: []string{"x:1"}, RouteExt: agentprotocol.RouteExt{Protocol: "tcp", ListenPort: 2222}},
		{ServiceID: "d", Hosts: []string{"h.example.com"}, Upstreams: []string{"x:1"}},
	}
	require.Equal(t, []L4Port{{"tcp", 2222}, {"tcp", 9005}, {"udp", 9005}}, L4Ports(routes))
	require.Empty(t, L4Ports(twoRoutes()))
}

func TestBootstrapAPIRouterCoversL4Status(t *testing.T) {
	y := string(BootstrapYAML("x"))
	require.Contains(t, y, "PathPrefix(`/api`)")
	require.Contains(t, y, "Path(`/api/providers/rest`)")
	require.Equal(t, "/api/tcp/routers", APITCPRoutersPath)
	require.Equal(t, "/api/udp/routers", APIUDPRoutersPath)
}

func testCert(t *testing.T) (crt, key string) {
	t.Helper()
	c, err := os.ReadFile("testdata/test-wildcard.crt")
	require.NoError(t, err)
	k, err := os.ReadFile("testdata/test-wildcard.key")
	require.NoError(t, err)
	return string(c), string(k)
}
