package proxy

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var update = flag.Bool("update", false, "rewrite golden files")

const (
	svcApp    = "11111111-1111-1111-1111-111111111111"
	svcWhoami = "22222222-2222-2222-2222-222222222222"
)

func twoRoutes() []agentprotocol.Route {
	return []agentprotocol.Route{
		{ServiceID: svcApp, Hosts: []string{"app.example.com", "www.example.com"}, Upstreams: []string{"nc-app-aaaaaaaa-0:8080", "nc-app-aaaaaaaa-1:8080", "nc-app-aaaaaaaa-2:8080"}, HealthPath: "/healthz"},
		{ServiceID: svcWhoami, Hosts: []string{"whoami.localhost"}, Upstreams: []string{"nc-whoami-bbbbbbbb-0:80"}},
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, append(got, '\n'), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, strings.TrimSpace(string(want)), strings.TrimSpace(string(got)))
}

func TestBuildConfigGolden(t *testing.T) {
	got, err := BuildConfig(twoRoutes(), Options{ACMEEmail: "ops@example.com", PublicHTTPSPort: 8443, Generation: 42})
	require.NoError(t, err)
	golden(t, "dynamic.golden.json", got)
}

func TestStaticFlagsGolden(t *testing.T) {
	got := StaticFlags(StaticOptions{ACMEEmail: "ops@example.com", PublicHTTPSPort: 8443, TrafficAnalytics: true})
	golden(t, "static.golden.txt", []byte(strings.Join(got, "\n")))
}

func TestStaticFlagsOptionalParts(t *testing.T) {
	plain := strings.Join(StaticFlags(StaticOptions{PublicHTTPSPort: 443}), "\n")
	require.NotContains(t, plain, "certificatesresolvers", "no ACME resolver without an email")
	require.NotContains(t, plain, "accesslog", "access log off when traffic analytics is off")
	require.NotContains(t, plain, "advertisedport", "443 needs no advertised port")

	full := strings.Join(StaticFlags(StaticOptions{ACMEEmail: "a@b.c", PublicHTTPSPort: 8443, TrafficAnalytics: true}), "\n")
	require.Contains(t, full, "--certificatesresolvers.le.acme.email=a@b.c")
	require.Contains(t, full, "--certificatesresolvers.le.acme.storage=/data/acme.json")
	require.Contains(t, full, "--certificatesresolvers.le.acme.httpchallenge.entrypoint=web")
	require.Contains(t, full, "--entrypoints.websecure.http3.advertisedport=8443")
	require.Contains(t, full, "--accesslog.format=json")
}

func TestStaticFlagsRequiredAndForbidden(t *testing.T) {
	flags := StaticFlags(StaticOptions{TrafficAnalytics: true})
	joined := strings.Join(flags, "\n")
	for _, want := range []string{
		"--providers.rest=true",
		"--providers.file.filename=/etc/traefik/bootstrap.yml",
		"--entrypoints.web.address=:80",
		"--entrypoints.websecure.address=:443",
		"--entrypoints.websecure.http3=true",
		"--entrypoints.probe.address=:8099",
		"--entrypoints.ncadmin.address=:9000",
		"--entrypoints.websecure.transport.lifecycle.gracetimeout=10s",
		"--ping=true",
		"--ping.manualrouting=true",
		"--api.dashboard=false",
		"--accesslog=true",
		"--accesslog.fields.defaultmode=keep",
		"--accesslog.fields.headers.defaultmode=drop",
		"--accesslog.fields.headers.names.User-Agent=keep",
		"--accesslog.fields.headers.names.Referer=keep",
		"--log.filepath=/dev/stderr",
	} {
		require.Contains(t, flags, want)
	}
	// The REST provider's insecure mode would publish an unauthenticated write
	// endpoint to the whole edge network.
	require.NotContains(t, joined, "insecure")
	require.NotContains(t, joined, "--api.insecure")
}

func TestBuildConfigRequiredObjects(t *testing.T) {
	raw, err := BuildConfig(twoRoutes(), Options{ACMEEmail: "ops@example.com", PublicHTTPSPort: 8443, Generation: 7})
	require.NoError(t, err)

	var cfg struct {
		HTTP struct {
			Routers map[string]struct {
				EntryPoints []string `json:"entryPoints"`
				Rule        string   `json:"rule"`
				Service     string   `json:"service"`
				Middlewares []string `json:"middlewares"`
				TLS         *struct {
					CertResolver string `json:"certResolver"`
				} `json:"tls"`
				Observability *struct {
					AccessLogs bool `json:"accessLogs"`
				} `json:"observability"`
			} `json:"routers"`
			Services map[string]struct {
				LoadBalancer struct {
					PassHostHeader bool `json:"passHostHeader"`
					Servers        []struct {
						URL string `json:"url"`
					} `json:"servers"`
					HealthCheck *struct {
						Path            string `json:"path"`
						Interval        string `json:"interval"`
						Timeout         string `json:"timeout"`
						FollowRedirects bool   `json:"followRedirects"`
					} `json:"healthCheck"`
				} `json:"loadBalancer"`
			} `json:"services"`
			Middlewares map[string]struct {
				RedirectScheme struct {
					Scheme string `json:"scheme"`
					Port   string `json:"port"`
				} `json:"redirectScheme"`
			} `json:"middlewares"`
		} `json:"http"`
	}
	require.NoError(t, json.Unmarshal(raw, &cfg))
	r := cfg.HTTP.Routers

	// Public names: ACME resolver; the https router ORs all hosts.
	https := r["nc-"+svcApp+"-https"]
	require.Equal(t, []string{"websecure"}, https.EntryPoints)
	require.Equal(t, "Host(`app.example.com`) || Host(`www.example.com`)", https.Rule)
	require.Equal(t, "nc-"+svcApp, https.Service)
	require.NotNil(t, https.TLS)
	require.Equal(t, "le", https.TLS.CertResolver)

	// Local names keep the default certificate.
	local := r["nc-"+svcWhoami+"-https"]
	require.NotNil(t, local.TLS)
	require.Empty(t, local.TLS.CertResolver)

	// HTTP router redirects, with the public https port.
	httpR := r["nc-"+svcApp+"-http"]
	require.Equal(t, []string{"web"}, httpR.EntryPoints)
	require.Equal(t, []string{"nc-https-redirect"}, httpR.Middlewares)
	require.Equal(t, "noop@internal", httpR.Service)
	require.Nil(t, httpR.TLS)
	require.Equal(t, "https", cfg.HTTP.Middlewares["nc-https-redirect"].RedirectScheme.Scheme)
	require.Equal(t, "8443", cfg.HTTP.Middlewares["nc-https-redirect"].RedirectScheme.Port)

	// Probe router: same rule, no TLS, no access log.
	probe := r["nc-"+svcApp+"-probe"]
	require.Equal(t, []string{"probe"}, probe.EntryPoints)
	require.Equal(t, https.Rule, probe.Rule)
	require.Nil(t, probe.TLS)
	require.NotNil(t, probe.Observability)
	require.False(t, probe.Observability.AccessLogs)

	// Upstreams and host header.
	svc := cfg.HTTP.Services["nc-"+svcApp]
	require.True(t, svc.LoadBalancer.PassHostHeader)
	require.Len(t, svc.LoadBalancer.Servers, 3, "one server per replica")
	require.Equal(t, "http://nc-app-aaaaaaaa-0:8080", svc.LoadBalancer.Servers[0].URL)
	require.Equal(t, "http://nc-app-aaaaaaaa-2:8080", svc.LoadBalancer.Servers[2].URL)
	require.NotNil(t, svc.LoadBalancer.HealthCheck)
	require.Equal(t, "/healthz", svc.LoadBalancer.HealthCheck.Path)
	require.Equal(t, "5s", svc.LoadBalancer.HealthCheck.Interval)
	require.Equal(t, "3s", svc.LoadBalancer.HealthCheck.Timeout)
	require.False(t, svc.LoadBalancer.HealthCheck.FollowRedirects)
	require.Nil(t, cfg.HTTP.Services["nc-"+svcWhoami].LoadBalancer.HealthCheck, "no health path, no Traefik health check")

	// Generation marker.
	gen := r["nc-gen"]
	require.Equal(t, []string{"probe"}, gen.EntryPoints)
	require.Equal(t, "Host(`gen-7.nc.invalid`)", gen.Rule)
	require.Equal(t, "ping@internal", gen.Service)
	require.False(t, gen.Observability.AccessLogs)
	require.Len(t, r, 2*3+1, "https+http+probe per service, plus the marker")
}

func TestBuildConfigDefaultPortRedirectHasNoPort(t *testing.T) {
	raw, err := BuildConfig(twoRoutes(), Options{PublicHTTPSPort: 443, Generation: 1})
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"port"`)
	require.NotContains(t, string(raw), "certResolver", "no resolver without an ACME email")
}

func TestBuildConfigMixedHostsSplitsTLSRouters(t *testing.T) {
	routes := []agentprotocol.Route{{ServiceID: svcApp, Hosts: []string{"shop.example.com", "shop.localhost", "10.0.0.5"}, Upstreams: []string{"a:80"}}}
	raw, err := BuildConfig(routes, Options{ACMEEmail: "ops@example.com", Generation: 1})
	require.NoError(t, err)
	var cfg struct {
		HTTP struct {
			Routers map[string]struct {
				Rule string `json:"rule"`
				TLS  struct {
					CertResolver string `json:"certResolver"`
				} `json:"tls"`
			} `json:"routers"`
		} `json:"http"`
	}
	require.NoError(t, json.Unmarshal(raw, &cfg))
	require.Equal(t, "Host(`shop.example.com`)", cfg.HTTP.Routers["nc-"+svcApp+"-https"].Rule)
	require.Equal(t, "le", cfg.HTTP.Routers["nc-"+svcApp+"-https"].TLS.CertResolver)
	require.Equal(t, "Host(`shop.localhost`) || Host(`10.0.0.5`)", cfg.HTTP.Routers["nc-"+svcApp+"-https-local"].Rule)
	require.Empty(t, cfg.HTTP.Routers["nc-"+svcApp+"-https-local"].TLS.CertResolver)
	// Public hosts are still redirected and probed as one set.
	require.Contains(t, cfg.HTTP.Routers["nc-"+svcApp+"-http"].Rule, "shop.example.com")
	require.Contains(t, cfg.HTTP.Routers["nc-"+svcApp+"-http"].Rule, "shop.localhost")
}

func TestBuildConfigEmptyTableHasOnlyMarker(t *testing.T) {
	raw, err := BuildConfig(nil, Options{Generation: 3})
	require.NoError(t, err)
	var cfg struct {
		HTTP struct {
			Routers     map[string]json.RawMessage `json:"routers"`
			Middlewares map[string]json.RawMessage `json:"middlewares"`
		} `json:"http"`
	}
	require.NoError(t, json.Unmarshal(raw, &cfg))
	require.Len(t, cfg.HTTP.Routers, 1)
	require.Contains(t, cfg.HTTP.Routers, "nc-gen")
	require.Empty(t, cfg.HTTP.Middlewares)
}

func TestRouterNamesMatchBuiltRouters(t *testing.T) {
	opts := Options{ACMEEmail: "ops@example.com", Generation: 5}
	names := RouterNames(twoRoutes(), opts)
	raw, err := BuildConfig(twoRoutes(), opts)
	require.NoError(t, err)
	var cfg struct {
		HTTP struct {
			Routers map[string]json.RawMessage `json:"routers"`
		} `json:"http"`
	}
	require.NoError(t, json.Unmarshal(raw, &cfg))
	require.Len(t, names, len(cfg.HTTP.Routers))
	for _, n := range names {
		require.Contains(t, cfg.HTTP.Routers, n)
	}
}

func TestBootstrapYAMLRoutesAdminThroughBasicAuthOnly(t *testing.T) {
	y := string(BootstrapYAML("$2a$04$hash"))
	require.Contains(t, y, `"nebulactrl-agent:$2a$04$hash"`)
	require.Contains(t, y, "basicAuth")
	// Both admin routers sit on the admin entrypoint behind the auth middleware.
	require.Equal(t, 2, strings.Count(y, "middlewares: [nc-admin-auth]"))
	require.Contains(t, y, "service: rest@internal")
	require.Contains(t, y, "service: api@internal")
	require.Contains(t, y, "entryPoints: [ncadmin]")
	require.Contains(t, y, "service: ping@internal")
	require.Contains(t, y, "Host(`ping.nc.invalid`)")
	require.NotContains(t, y, "entryPoints: [web")
}

func TestValidateRoutes(t *testing.T) {
	ok := func(r agentprotocol.Route) []agentprotocol.Route { return []agentprotocol.Route{r} }
	good := agentprotocol.Route{ServiceID: svcApp, Hosts: []string{"a.example.com"}, Upstreams: []string{"nc-a-1:80"}}
	require.NoError(t, ValidateRoutes(twoRoutes(), agentprotocol.DefaultUpstreamPolicy()))
	require.NoError(t, ValidateRoutes(nil, agentprotocol.DefaultUpstreamPolicy()))

	bad := func(mut func(*agentprotocol.Route)) error {
		r := good
		r.Hosts = append([]string(nil), good.Hosts...)
		mut(&r)
		return ValidateRoutes(ok(r), agentprotocol.DefaultUpstreamPolicy())
	}
	require.Error(t, bad(func(r *agentprotocol.Route) { r.Hosts = nil }))
	require.Error(t, bad(func(r *agentprotocol.Route) { r.ServiceID = "" }))
	require.Error(t, bad(func(r *agentprotocol.Route) { r.Upstreams = nil }))
	require.Error(t, bad(func(r *agentprotocol.Route) { r.Upstreams = []string{"nohostport"} }))
	require.Error(t, bad(func(r *agentprotocol.Route) { r.Upstreams = []string{"a:80", "a:notaport"} }))
	require.Error(t, bad(func(r *agentprotocol.Route) { r.Upstreams = []string{"a:70000"} }))
	require.Error(t, bad(func(r *agentprotocol.Route) { r.Upstreams = []string{"bad host:80"} }))
	require.Error(t, bad(func(r *agentprotocol.Route) { r.HealthPath = "healthz" }))
	require.Error(t, bad(func(r *agentprotocol.Route) { r.HealthPath = "/a b" }))
	require.NoError(t, bad(func(r *agentprotocol.Route) { r.HealthPath = "/health?x=1" }))
	for _, h := range []string{"bad host", "a`b", "a)||Host(`b", "a/b", "-a.com", "a.com-", "a\"b", ""} {
		require.Error(t, bad(func(r *agentprotocol.Route) { r.Hosts = []string{h} }), h)
	}
	require.NoError(t, bad(func(r *agentprotocol.Route) {
		r.Hosts = []string{"xn--bcher-kva.example", "10.0.0.5", "a_b.example.com"}
	}))
}

func TestIsLocalName(t *testing.T) {
	for _, h := range []string{"whoami.localhost", "localhost", "nas", "10.1.2.3", "box.local", "svc.internal", "x.test"} {
		require.True(t, isLocalName(h), h)
	}
	for _, h := range []string{"app.example.com", "a.b.io", "sslip.io", "1-2-3-4.sslip.io"} {
		require.False(t, isLocalName(h), h)
	}
}
