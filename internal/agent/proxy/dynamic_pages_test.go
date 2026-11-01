package proxy

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func maintRoute(m *agentprotocol.Maintenance, rules ...agentprotocol.DomainRule) agentprotocol.Route {
	r := ruleRoute(rules...)
	r.Maintenance = m
	return r
}

func TestPagesOffEmitsNothing(t *testing.T) {
	routes := []agentprotocol.Route{maintRoute(&agentprotocol.Maintenance{}, rule(domA, "app.example.com", nil))}
	p := parse(t, goldenConfig(t, "pages_off_maintenance_down", routes, Options{}))
	require.NotContains(t, p.HTTP.Services, pagesService)
	require.NotContains(t, p.HTTP.Middlewares, errorsMiddleware)
	require.NotContains(t, p.HTTP.Routers, notFoundRouter+"-https")
	require.Equal(t, maintenanceDownService, p.HTTP.Routers["nc-"+svcApp+"-daaaaaaaa-https"]["service"],
		"a service in maintenance never reaches its upstream, also without a pages server: Traefik answers 503 for a service without servers")
}

func TestPagesErrorsMiddlewareAndCatchAll(t *testing.T) {
	plain := agentprotocol.Route{ServiceID: svcApp, Hosts: []string{"app.example.com", "www.example.com"}, Upstreams: []string{"nc-app-aaaaaaaa-0:8080"}}
	ruled := ruleRoute(rule(domA, "api.example.com", func(d *agentprotocol.DomainRule) {
		d.Options.Compress = true
	}))
	ruled.ServiceID = svcWhoami
	p := parse(t, goldenConfig(t, "pages_errors_catchall", []agentprotocol.Route{plain, ruled}, Options{Pages: true, ACMEEmail: "ops@example.com"}))

	// The errors middleware wraps both kinds of service router, first.
	require.Equal(t, []string{errorsMiddleware}, strs(p.HTTP.Routers["nc-"+svcApp+"-https"]["middlewares"]))
	require.Equal(t, []string{errorsMiddleware, "nc-" + svcWhoami + "-daaaaaaaa-compress"}, strs(p.HTTP.Routers["nc-"+svcWhoami+"-daaaaaaaa-https"]["middlewares"]))
	require.Equal(t, map[string]any{"status": []any{"502-504"}, "service": pagesService, "query": "/err/{status}"}, p.HTTP.Middlewares[errorsMiddleware]["errors"])
	require.Equal(t, map[string]any{"servers": []any{map[string]any{"url": "http://nebulactrl-pages:8080"}}, "passHostHeader": true},
		p.HTTP.Services[pagesService]["loadBalancer"])

	// Probe routers stay on the real service without the errors middleware,
	// so verification still reflects the app.
	probe := p.HTTP.Routers["nc-"+svcApp+"-probe"]
	require.Equal(t, "nc-"+svcApp, probe["service"])
	require.NotContains(t, probe, "middlewares")
	require.NotContains(t, p.HTTP.Routers["nc-"+svcWhoami+"-daaaaaaaa-probe"], "middlewares")

	// The catch-all: lowest priority, web + websecure (TLS) only, 404 page.
	for _, ep := range []string{"http", "https"} {
		r := p.HTTP.Routers[notFoundRouter+"-"+ep]
		require.Equal(t, "PathPrefix(`/`)", r["rule"])
		require.Equal(t, pagesService, r["service"])
		require.EqualValues(t, 1, r["priority"])
		require.Equal(t, []string{notFoundMiddleware}, strs(r["middlewares"]))
	}
	require.Equal(t, []string{EntryWeb}, strs(p.HTTP.Routers[notFoundRouter+"-http"]["entryPoints"]))
	require.Equal(t, []string{EntryWebSecure}, strs(p.HTTP.Routers[notFoundRouter+"-https"]["entryPoints"]))
	require.Equal(t, map[string]any{}, p.HTTP.Routers[notFoundRouter+"-https"]["tls"])
	require.Equal(t, map[string]any{"path": "/err/404"}, p.HTTP.Middlewares[notFoundMiddleware]["replacePath"])
}

func TestPagesMaintenanceRepoint(t *testing.T) {
	eta := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	m := &agentprotocol.Maintenance{Title: "Upgrade", ETA: &eta}
	maint := maintRoute(m,
		rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) {
			d.Options.BasicAuth, d.AuthRef = true, "auth:"+domA
			d.AuthUsers = []string{"a:" + bcryptHash}
		}),
		rule(domB, "plain.example.com", func(d *agentprotocol.DomainRule) { d.TLSMode = agentprotocol.TLSNone }),
		rule(domC, "go.example.com", func(d *agentprotocol.DomainRule) {
			d.Kind, d.RedirectTo = agentprotocol.DomainRedirect, "app.example.com"
		}),
	)
	maint.Hosts = append(maint.Hosts, "bare.example.com") // a host served with defaults
	other := agentprotocol.Route{ServiceID: svcWhoami, Hosts: []string{"other.example.com"}, Upstreams: []string{"nc-db-bbbbbbbb-0:80"}}
	p := parse(t, goldenConfig(t, "pages_maintenance", []agentprotocol.Route{maint, other}, Options{Pages: true, ACMEEmail: "ops@example.com"}))

	svc := "nc-" + svcApp
	mw := svc + "-maint"
	require.Equal(t, map[string]any{"path": "/m/" + svcApp}, p.HTTP.Middlewares[mw]["replacePath"])

	// Serving routers go to the pages server behind the page rewrite only: no
	// access rules (a maintenance page must not need a login), no errors mw.
	for _, name := range []string{
		svc + "-daaaaaaaa-https", // auth domain, https
		svc + "-dbbbbbbbb-http",  // tlsMode none: served on web
		svc + "-https",           // default host
	} {
		r := p.HTTP.Routers[name]
		require.NotNil(t, r, name)
		require.Equal(t, pagesService, r["service"], name)
		require.Equal(t, []string{mw}, strs(r["middlewares"]), name)
	}
	// HTTP→HTTPS redirects and redirect domains are untouched.
	require.Equal(t, "noop@internal", p.HTTP.Routers[svc+"-daaaaaaaa-http"]["service"])
	require.Equal(t, []string{"nc-https-redirect"}, strs(p.HTTP.Routers[svc+"-daaaaaaaa-http"]["middlewares"]))
	require.Equal(t, "noop@internal", p.HTTP.Routers[svc+"-dcccccccc-redir"]["service"])

	// Probe routers keep the real service (and its prefix rewrites only).
	for _, name := range []string{svc + "-daaaaaaaa-probe", svc + "-dbbbbbbbb-probe", svc + "-probe"} {
		require.Equal(t, svc, p.HTTP.Routers[name]["service"], name)
		require.NotContains(t, p.HTTP.Routers[name], "middlewares", name)
	}
	require.Contains(t, p.HTTP.Services, svc, "the real service stays defined for the probe")

	// Another service is not affected.
	require.Equal(t, []string{errorsMiddleware}, strs(p.HTTP.Routers["nc-"+svcWhoami+"-https"]["middlewares"]))
	require.Equal(t, "nc-"+svcWhoami, p.HTTP.Routers["nc-"+svcWhoami+"-https"]["service"])
	require.NotContains(t, p.HTTP.Middlewares, "nc-"+svcWhoami+"-maint")
}

func TestPagesRouterNamesIncludeCatchAll(t *testing.T) {
	names := RouterNames(nil, Options{Pages: true, Generation: 1})
	require.ElementsMatch(t, []string{genRouter, notFoundRouter + "-http", notFoundRouter + "-https"}, names)
	require.Equal(t, []string{genRouter}, RouterNames(nil, Options{Generation: 1}))
}

func TestValidateMaintenanceRoutes(t *testing.T) {
	ok := maintRoute(&agentprotocol.Maintenance{Title: "t", Message: "m"}, rule(domA, "app.example.com", nil))
	require.NoError(t, ValidateRoutes([]agentprotocol.Route{ok}, agentprotocol.DefaultUpstreamPolicy()))

	long := maintRoute(&agentprotocol.Maintenance{Title: strings.Repeat("x", agentprotocol.MaxMaintenanceTitle+1)}, rule(domA, "app.example.com", nil))
	require.ErrorContains(t, ValidateRoutes([]agentprotocol.Route{long}, agentprotocol.DefaultUpstreamPolicy()), "title")

	l4 := agentprotocol.Route{ServiceID: svcPG, Upstreams: []string{"a:1"}, RouteExt: agentprotocol.RouteExt{Protocol: "tcp", ListenPort: 5432, Maintenance: &agentprotocol.Maintenance{}}}
	require.ErrorContains(t, ValidateRoutes([]agentprotocol.Route{l4}, agentprotocol.DefaultUpstreamPolicy()), "only applies to http")
}
