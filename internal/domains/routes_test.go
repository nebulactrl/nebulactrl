package domains

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// routeService inserts a route service on the fixture's server, deployed with cfg.
func (f *wwwFixture) routeService(name string, cfg services.RouteConfig) db.Service {
	f.t.Helper()
	full := services.Config{Route: &cfg}
	raw, err := json.Marshal(full)
	require.NoError(f.t, err)
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: name, Slug: name, Kind: services.KindRoute, SourceType: services.SourceExternal, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	f.deployService(sv.ID, full)
	return sv
}

func (f *wwwFixture) routeFor(serviceID uuid.UUID) (agentprotocol.Route, bool) {
	f.t.Helper()
	rs, err := ServerRoutes(context.Background(), f.q, f.server.ID, uuid.Nil)
	require.NoError(f.t, err)
	for _, r := range rs {
		if r.ServiceID == serviceID.String() {
			return r, true
		}
	}
	return agentprotocol.Route{}, false
}

func TestRuleFromRowCarriesTheRoutingFields(t *testing.T) {
	id, cert := uuid.New(), uuid.New()
	to := "target.example.com"
	rule := RuleFromRow(db.Domain{
		ID: id, Hostname: "app.example.com", PathPrefix: "/api", StripPrefix: true, AddPrefix: "/v2", Kind: agentprotocol.DomainServe,
		TlsMode: agentprotocol.TLSCustom, CertificateID: &cert, Options: []byte(`{"basicAuth":true}`), DnsResolver: "cloudflare", Generated: true,
		RedirectCode: 301, PreservePath: true, RedirectTo: &to,
	})
	require.Equal(t, id.String(), rule.ID)
	require.Equal(t, "app.example.com", rule.Host)
	require.Equal(t, "/api", rule.PathPrefix)
	require.True(t, rule.StripPrefix)
	require.Equal(t, "/v2", rule.AddPrefix)
	require.Equal(t, agentprotocol.TLSCustom, rule.TLSMode)
	require.Equal(t, cert.String(), rule.CertificateID)
	require.Equal(t, "cloudflare", rule.DNSResolver)
	require.True(t, rule.Generated)
	require.Equal(t, agentprotocol.SecretAuthPrefix+id.String(), rule.AuthRef, "basic auth is referenced, not embedded")
	require.Empty(t, rule.RedirectTo, "a serve rule has no redirect fields")
	require.Zero(t, rule.RedirectCode)

	red := RuleFromRow(db.Domain{ID: id, Hostname: "old.example.com", Kind: agentprotocol.DomainRedirect, RedirectTo: &to, RedirectCode: 302, PreservePath: true})
	require.Equal(t, "target.example.com", red.RedirectTo)
	require.Equal(t, 302, red.RedirectCode)
	require.True(t, red.PreservePath)
	require.Empty(t, red.CertificateID)
	require.Empty(t, red.AuthRef)
	red = RuleFromRow(db.Domain{ID: id, Kind: agentprotocol.DomainRedirect})
	require.Empty(t, red.RedirectTo, "a redirect without a target stays empty instead of dereferencing nil")
}

func TestRulesAndHostsKeepFirstSeenOrderWithoutDuplicates(t *testing.T) {
	rows := []db.Domain{{ID: uuid.New(), Hostname: "b.example.com"}, {ID: uuid.New(), Hostname: "a.example.com"}, {ID: uuid.New(), Hostname: "b.example.com", PathPrefix: "/x"}}
	rules, hosts := RulesAndHosts(rows)
	require.Len(t, rules, 3, "one rule per domain row")
	require.Equal(t, []string{"b.example.com", "a.example.com"}, hosts)
	rules, hosts = RulesAndHosts(nil)
	require.Empty(t, rules)
	require.Empty(t, hosts)
}

func TestUpstreamsNameEveryReplica(t *testing.T) {
	dep := uuid.NewString()
	require.Equal(t, []string{agentprotocol.ContainerName("web", dep, 0) + ":8080"}, Upstreams("web", dep, 0, 8080), "at least one")
	got := Upstreams("web", dep, 3, 80)
	require.Len(t, got, 3)
	require.Equal(t, agentprotocol.ContainerName("web", dep, 2)+":80", got[2])
}

func TestServerRoutesGroupDomainsPerServiceAndSkipWhatDoesNotRun(t *testing.T) {
	f := newWWWFixture(t)
	f.mustCreate("a.example.com", RuleInput{})
	f.mustCreate("b.example.com", RuleInput{})
	path := "/api"
	f.mustCreate("a.example.com", RuleInput{PathPrefix: &path})
	other := f.otherService("other")
	_, err := f.svc.Create(f.ctx, other.ID, "o.example.com", 0, RuleInput{})
	require.NoError(t, err)

	// Nothing is routed before a deployment runs.
	rs, err := ServerRoutes(context.Background(), f.q, f.server.ID, uuid.Nil)
	require.NoError(t, err)
	require.Empty(t, rs)

	dep := f.deployService(f.svcA.ID, services.Config{Image: "traefik/whoami:latest", Port: 80, Healthcheck: services.Healthcheck{Type: services.HealthHTTP, Path: "/up"}}.WithDefaults())
	f.deployService(other.ID, services.Config{Image: "traefik/whoami:latest", Port: 80}.WithDefaults())
	rt, ok := f.routeFor(f.svcA.ID)
	require.True(t, ok)
	require.Equal(t, []string{"a.example.com", "b.example.com"}, rt.Hosts, "hosts are deduplicated")
	require.Len(t, rt.Domains, 3, "one rule per domain row")
	require.Equal(t, []string{agentprotocol.ContainerName("site", dep.ID.String(), 0) + ":80"}, rt.Upstreams)
	require.Equal(t, "/up", rt.HealthPath)
	require.Nil(t, rt.Maintenance)

	// A service can be left out of the table (the one being redeployed).
	rs, err = ServerRoutes(context.Background(), f.q, f.server.ID, f.svcA.ID)
	require.NoError(t, err)
	for _, r := range rs {
		require.NotEqual(t, f.svcA.ID.String(), r.ServiceID)
	}
	_, ok = f.routeFor(other.ID)
	require.True(t, ok)

	// Maintenance mode rides on the route; tailnet-only services drop out.
	f.exec(`UPDATE services SET maintenance_enabled = true, maintenance_title = 'Back soon', maintenance_message = 'Upgrading' WHERE id = $1`, f.svcA.ID)
	rt, _ = f.routeFor(f.svcA.ID)
	require.NotNil(t, rt.Maintenance)
	require.Equal(t, "Back soon", rt.Maintenance.Title)
	require.Equal(t, "Upgrading", rt.Maintenance.Message)
	require.Equal(t, rt.Maintenance, MaintenanceOf(mustService(t, f, f.svcA.ID)))
	require.Nil(t, MaintenanceOf(mustService(t, f, other.ID)))

	// Another server's table does not mention these services.
	ip2 := "203.0.113.20"
	srv2, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.principal().WorkspaceID, Name: "agent2", PublicIp: &ip2})
	require.NoError(t, err)
	rs, err = ServerRoutes(context.Background(), f.q, srv2.ID, uuid.Nil)
	require.NoError(t, err)
	require.Empty(t, rs)
}

func mustService(t *testing.T, f *wwwFixture, id uuid.UUID) db.Service {
	t.Helper()
	row, err := f.q.GetServiceScopeInternal(context.Background(), id)
	require.NoError(t, err)
	return row.Service
}

func TestServerTableNumbersEveryTableHigherThanTheLast(t *testing.T) {
	f := newWWWFixture(t)
	f.mustCreate("a.example.com", RuleInput{})
	f.deployService(f.svcA.ID, services.Config{Image: "traefik/whoami:latest", Port: 80}.WithDefaults())
	ctx := context.Background()

	routes, seq1, err := ServerTable(ctx, f.q, f.server.ID, uuid.Nil)
	require.NoError(t, err)
	require.Len(t, routes, 1)
	_, seq2, err := ServerTable(ctx, f.q, f.server.ID, uuid.Nil)
	require.NoError(t, err)
	require.Equal(t, seq1+1, seq2, "a table built later carries a higher number")
	_, seqOther, err := ServerTable(ctx, f.q, mustOtherServer(t, f), uuid.Nil)
	require.NoError(t, err)
	require.Equal(t, uint64(1), seqOther, "each server counts its own tables")
}

func mustOtherServer(t *testing.T, f *wwwFixture) uuid.UUID {
	t.Helper()
	srv, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.principal().WorkspaceID, Name: "other-agent"})
	require.NoError(t, err)
	return srv.ID
}

func TestRouteServicesBuildExternalTCPAndManagedRoutes(t *testing.T) {
	f := newWWWFixture(t)
	f.mustCreate("managed.example.com", RuleInput{})
	dep := f.deployService(f.svcA.ID, services.Config{Image: "traefik/whoami:latest", Port: 80}.WithDefaults())

	// External http upstream with TLS options.
	host := f.routeService("ext", services.RouteConfig{
		Protocol: agentprotocol.ProtoHTTP, Scheme: "https", InsecureSkipVerify: true, ServerName: "internal.example", HealthPath: "/h", VerifySkip: true,
		Upstreams: []services.RouteUpstream{{Address: "10.0.0.5:8443"}, {Address: ""}},
	})
	_, err := f.svc.Create(f.ctx, host.ID, "ext.example.com", 0, RuleInput{})
	require.NoError(t, err)
	rt, ok := f.routeFor(host.ID)
	require.True(t, ok)
	require.Equal(t, agentprotocol.RouteKindExternal, rt.Kind)
	require.Equal(t, []string{"10.0.0.5:8443"}, rt.Upstreams, "an upstream without an address is ignored")
	require.Equal(t, []string{"ext.example.com"}, rt.Hosts)
	require.Equal(t, "https", rt.Scheme)
	require.Equal(t, &agentprotocol.UpstreamTransport{InsecureSkipVerify: true, ServerName: "internal.example"}, rt.Transport)
	require.Equal(t, "/h", rt.HealthPath)
	require.True(t, rt.VerifySkip)

	// The default scheme is not spelled out.
	plain := f.routeService("plain", services.RouteConfig{Protocol: agentprotocol.ProtoHTTP, Scheme: "http", Upstreams: []services.RouteUpstream{{Address: "10.0.0.6:80"}}})
	_, err = f.svc.Create(f.ctx, plain.ID, "plain.example.com", 0, RuleInput{})
	require.NoError(t, err)
	rt, _ = f.routeFor(plain.ID)
	require.Empty(t, rt.Scheme)
	require.Nil(t, rt.Transport)

	// An http route without a domain, or without a usable upstream, has nothing to route.
	f.routeService("nodomain", services.RouteConfig{Protocol: agentprotocol.ProtoHTTP, Upstreams: []services.RouteUpstream{{Address: "10.0.0.7:80"}}})
	f.routeService("noupstream", services.RouteConfig{Protocol: agentprotocol.ProtoHTTP, Upstreams: []services.RouteUpstream{{Address: ""}}})
	rs, err := ServerRoutes(context.Background(), f.q, f.server.ID, uuid.Nil)
	require.NoError(t, err)
	for _, r := range rs {
		require.NotEmpty(t, r.Upstreams)
		if r.Kind == agentprotocol.RouteKindExternal {
			require.NotEmpty(t, r.Hosts)
		}
	}

	// A managed upstream resolves to the current containers of the service on this server.
	mgd := f.routeService("mgd", services.RouteConfig{Protocol: agentprotocol.ProtoHTTP, Upstreams: []services.RouteUpstream{{ServiceID: &f.svcA.ID, Port: 9000}}})
	_, err = f.svc.Create(f.ctx, mgd.ID, "mgd.example.com", 0, RuleInput{})
	require.NoError(t, err)
	rt, ok = f.routeFor(mgd.ID)
	require.True(t, ok)
	require.Equal(t, []string{agentprotocol.ContainerName("site", dep.ID.String(), 0) + ":9000"}, rt.Upstreams)
	require.Empty(t, rt.Kind, "a managed upstream is not an external route")
	require.NotEmpty(t, rt.Networks)
	// A managed service on another server is not reachable from this table.
	srv2 := mustOtherServer(t, f)
	f.exec(`UPDATE deployments SET server_id = $2 WHERE id = $1`, dep.ID, srv2)
	_, ok = f.routeFor(mgd.ID)
	require.False(t, ok)
	f.exec(`UPDATE deployments SET server_id = $2 WHERE id = $1`, dep.ID, f.server.ID)

	// tcp routes carry their listener and access rules, and no hosts.
	tcp := f.routeService("db", services.RouteConfig{
		Protocol: agentprotocol.ProtoTCP, ListenPort: 5433, AllowCIDRs: []string{"203.0.113.0/24"}, DenyCIDRs: []string{"203.0.113.9/32"}, ProxyProtocol: 2,
		Upstreams: []services.RouteUpstream{{Address: "10.0.0.9:5432"}},
	})
	f.exec(`UPDATE services SET maintenance_enabled = true WHERE id = $1`, tcp.ID)
	rt, ok = f.routeFor(tcp.ID)
	require.True(t, ok)
	require.Equal(t, agentprotocol.ProtoTCP, rt.Protocol)
	require.Equal(t, 5433, rt.ListenPort)
	require.Equal(t, []string{"203.0.113.0/24"}, rt.AllowCIDRs)
	require.Equal(t, []string{"203.0.113.9/32"}, rt.DenyCIDRs)
	require.Equal(t, 2, rt.ProxyProtocol)
	require.NotNil(t, rt.Hosts)
	require.Empty(t, rt.Hosts)
	require.Nil(t, rt.Maintenance, "maintenance pages are for http")

	// The route builder used when a route service is deployed gets the same answer for the same config.
	cfg := services.RouteConfig{Protocol: agentprotocol.ProtoHTTP, Upstreams: []services.RouteUpstream{{ServiceID: &f.svcA.ID, Port: 7000}}}
	override := Overrides{f.svcA.ID: Target{Slug: "site", DeploymentID: "newdep", Replicas: 2, Networks: []string{"net-a"}}}
	built, ok, err := RouteServiceRoute(context.Background(), f.q, mgd.ID, f.server.ID, cfg, override)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, agentprotocol.ContainerNames("site", "newdep", 2)[0]+":7000", built.Upstreams[0], "routes point at the NEW containers before the agent applies the table")
	require.Len(t, built.Upstreams, 2)
	require.Equal(t, []string{"net-a"}, built.Networks)
	require.Equal(t, []string{"mgd.example.com"}, built.Hosts)
	_, ok, err = RouteServiceRoute(context.Background(), f.q, mgd.ID, f.server.ID, services.RouteConfig{Protocol: agentprotocol.ProtoHTTP, Upstreams: []services.RouteUpstream{{ServiceID: new(uuid.New()), Port: 1}}}, nil)
	require.NoError(t, err)
	require.False(t, ok, "an upstream that is not running leaves nothing to route to")
}

func TestStackRoutesGroupByComponentAndPort(t *testing.T) {
	svc, env := uuid.New(), uuid.New()
	doms := []db.Domain{
		{ID: uuid.New(), Hostname: "b.example.com", Component: "web", TargetPort: 8080},
		{ID: uuid.New(), Hostname: "a.example.com", Component: "web", TargetPort: 80},
		{ID: uuid.New(), Hostname: "c.example.com", Component: "web", TargetPort: 8080},
		{ID: uuid.New(), Hostname: "api.example.com", Component: "api", TargetPort: 80},
	}
	rs := StackRoutes(svc, env, false, doms)
	require.Len(t, rs, 3, "one route per component and port")
	require.Equal(t, agentprotocol.StackRouteID(svc.String(), "web", 80), rs[0].ServiceID, "lower ports first")
	require.Equal(t, svc.String(), rs[0].Stack)
	require.Equal(t, []string{agentprotocol.StackContainerName(svc.String(), "web") + ":80"}, rs[0].Upstreams)
	var web8080 agentprotocol.Route
	for _, r := range rs {
		if r.ServiceID == agentprotocol.StackRouteID(svc.String(), "web", 8080) {
			web8080 = r
		}
	}
	require.Equal(t, []string{"b.example.com", "c.example.com"}, web8080.Hosts)
	require.Len(t, web8080.Domains, 2)
	require.Equal(t, services.RouteNetworks(env, false), rs[0].Networks)
	require.NotEqual(t, services.RouteNetworks(env, false), StackRoutes(svc, env, true, doms)[0].Networks, "egress blocking changes the networks")
	require.Empty(t, StackRoutes(svc, env, false, nil))
}

func TestStackDomainsNeedAComponentAndAPort(t *testing.T) {
	cfg := services.Config{Stack: &services.StackConfig{Components: []agentprotocol.StackComponent{
		{Name: "web", Ports: []int{80}}, {Name: "db"}, {Name: "multi", Ports: []int{1, 2}},
	}}}
	_, err := stackTarget(cfg, "", 0)
	requireCode(t, err, "component_required")
	_, err = stackTarget(cfg, "nope", 0)
	ae := requireCodeErr(t, err, "unknown_component")
	require.NotEmpty(t, ae.Details["components"])
	_, err = stackTarget(cfg, "db", 0)
	requireCode(t, err, "target_port_required")
	_, err = stackTarget(cfg, "multi", 0)
	requireCode(t, err, "target_port_required")
	port, err := stackTarget(cfg, "web", 0)
	require.NoError(t, err)
	require.Equal(t, 80, port, "the one declared port")
	port, err = stackTarget(cfg, "db", 5432)
	require.NoError(t, err)
	require.Equal(t, 5432, port, "a component may serve a port it does not declare")
	_, err = stackTarget(cfg, "web", 70000)
	requireCode(t, err, "invalid_target_port")
	_, err = stackTarget(cfg, "web", -1)
	requireCode(t, err, "invalid_target_port")
}

func TestRouteServiceDomainRules(t *testing.T) {
	f := newWWWFixture(t)
	tcp := f.routeService("db", services.RouteConfig{Protocol: agentprotocol.ProtoTCP, ListenPort: 5433, OpenToInternet: true, Upstreams: []services.RouteUpstream{{Address: "10.0.0.9:5432"}}})
	web := f.routeService("web", services.RouteConfig{Protocol: agentprotocol.ProtoHTTP, Upstreams: []services.RouteUpstream{{Address: "10.0.0.9:80"}}})

	_, err := f.svc.Create(f.ctx, tcp.ID, "db.example.com", 0, RuleInput{})
	requireCode(t, err, "no_http_route")
	_, err = f.svc.Create(f.ctx, web.ID, "web.example.com", 8080, RuleInput{})
	requireCode(t, err, "invalid_target_port")
	_, err = f.svc.CreateForComponent(f.ctx, web.ID, "web.example.com", 0, "api", RuleInput{})
	requireCode(t, err, "invalid_component")
	v, err := f.svc.Create(f.ctx, web.ID, "web.example.com", 0, RuleInput{})
	require.NoError(t, err)
	require.Zero(t, v.TargetPort)
}

func TestMoveToServiceRehomesDomainsWithoutStagingAChange(t *testing.T) {
	f := newWWWFixture(t)
	a := f.mustCreate("a.example.com", RuleInput{})
	b := f.mustCreate("b.example.com", RuleInput{})
	target := f.otherService("target")
	f.deployService(target.ID, services.Config{Image: "traefik/whoami:latest", Port: 80}.WithDefaults())
	hook := &dnsRecorder{}
	f.svc.SetDNS(hook)
	genBefore := mustService(t, f, target.ID).Generation

	require.NoError(t, f.svc.MoveToService(f.ctx, nil, nil, target.ID, ""), "nothing to move is fine")
	tx, err := f.pool.Begin(context.Background())
	require.NoError(t, err)
	require.NoError(t, f.svc.MoveToService(f.ctx, tx, []uuid.UUID{a.ID, b.ID}, target.ID, ""))
	require.NoError(t, tx.Commit(context.Background()))

	rows, err := f.q.ListServiceDomains(context.Background(), target.ID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Empty(t, f.rows())
	require.Equal(t, genBefore, mustService(t, f, target.ID).Generation, "the moved domains are part of what runs: no staged change")
	require.ElementsMatch(t, []uuid.UUID{a.ID, b.ID}, hook.renamed, "the DNS automation re-matches them")

	// The release snapshot of the target lists them, so the dashboard shows nothing pending.
	var snapDomains int
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT jsonb_array_length(r.config->'domains') FROM releases r JOIN deployments d ON d.release_id = r.id JOIN services s ON s.current_deployment_id = d.id WHERE s.id = $1`, target.ID).Scan(&snapDomains))
	require.Equal(t, 2, snapDomains)

	// A process the target does not have, or one without a port, is refused and nothing moves.
	c := f.mustCreate("c.example.com", RuleInput{})
	tx, err = f.pool.Begin(context.Background())
	require.NoError(t, err)
	err = f.svc.MoveToService(f.ctx, tx, []uuid.UUID{c.ID}, target.ID, "ghost")
	requireCode(t, err, "invalid_process")
	require.NoError(t, tx.Rollback(context.Background()))
	require.Contains(t, f.rows(), "c.example.com")
}

// ---- HTTP ----

// hdo sends one request through the registered domain routes as p.
func (f *wwwFixture) hdo(p *auth.Principal, method, path string, body any, headers ...string) (int, map[string]any, http.Header) {
	f.t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if p != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), p))
			}
			next.ServeHTTP(w, req)
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	Register(humachi.New(r, cfg), f.svc)
	var rd *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(f.t, err)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 && rec.Body.Bytes()[0] == '{' {
		require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	}
	return rec.Code, out, rec.Header()
}

func TestDomainEndpoints(t *testing.T) {
	f := newWWWFixture(t)
	o := f.principal()
	base := "/services/" + f.svcA.ID.String()

	code, body, _ := f.hdo(o, http.MethodPost, base+"/domains", map[string]any{"hostname": "App.Example.com", "pathPrefix": "/api", "options": map[string]any{"compress": true}})
	require.Equal(t, http.StatusCreated, code, body)
	id := body["id"].(string)
	require.Equal(t, "app.example.com", body["hostname"])
	require.Equal(t, "/api", body["pathPrefix"])
	require.Equal(t, float64(80), body["targetPort"])
	code, body, _ = f.hdo(o, http.MethodPost, base+"/domains", map[string]any{"hostname": "app.example.com", "pathPrefix": "/api"})
	require.Equal(t, http.StatusConflict, code, body)
	require.Equal(t, "domain_conflict", body["code"])
	code, body, _ = f.hdo(o, http.MethodPost, base+"/domains", map[string]any{"hostname": "x.example.com", "component": "web", "process": "api"})
	require.Equal(t, http.StatusUnprocessableEntity, code, body)
	require.Equal(t, "validation_failed", body["code"])
	code, body, _ = f.hdo(o, http.MethodPost, base+"/domains", map[string]any{"hostname": "x.example.com", "process": "api"})
	require.Equal(t, http.StatusUnprocessableEntity, code, body)
	require.Equal(t, "invalid_process", body["code"])

	code, body, _ = f.hdo(o, http.MethodGet, base+"/domains", nil)
	require.Equal(t, http.StatusOK, code, body)
	require.Len(t, body["items"], 1)

	code, body, _ = f.hdo(o, http.MethodPost, base+"/domains/generate", nil)
	require.Equal(t, http.StatusCreated, code, body)
	require.Equal(t, true, body["generated"])
	require.Equal(t, "site-production-shop.203.0.113.9.sslip.io", body["hostname"])
	code, body, _ = f.hdo(o, http.MethodPost, base+"/domains/generate?component=a&process=b", nil)
	require.Equal(t, http.StatusUnprocessableEntity, code, body)

	// Updates need If-Match and answer with the new ETag.
	code, body, _ = f.hdo(o, http.MethodPatch, "/domains/"+id, map[string]any{"stripPrefix": true})
	require.Equal(t, http.StatusPreconditionRequired, code, body)
	require.Equal(t, "if_match_required", body["code"])
	code, body, _ = f.hdo(o, http.MethodPatch, "/domains/"+id, map[string]any{"stripPrefix": true}, "If-Match", "abc")
	require.Equal(t, http.StatusBadRequest, code, body)
	require.Equal(t, "invalid_if_match", body["code"])
	code, body, _ = f.hdo(o, http.MethodPatch, "/domains/"+id, map[string]any{"stripPrefix": true}, "If-Match", services.ETag(f.gen()-1))
	require.Equal(t, http.StatusPreconditionFailed, code, body)
	require.Equal(t, "generation_conflict", body["code"])
	gen := f.gen()
	code, body, hdr := f.hdo(o, http.MethodPatch, "/domains/"+id, map[string]any{"stripPrefix": true}, "If-Match", services.ETag(gen))
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, true, body["stripPrefix"])
	require.Equal(t, services.ETag(gen+1), hdr.Get("ETag"))

	// Basic-auth users: set, list through the domain, remove.
	req := []map[string]any{{"username": "zed", "password": "zed-password"}, {"username": "ann", "password": "ann-password"}}
	raw, _ := json.Marshal(req)
	code = f.putJSON(o, "/domains/"+id+"/auth-users", raw)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []string{"ann", "zed"}, f.view("app.example.com").AuthUsers)
	code, body, _ = f.hdo(o, http.MethodDelete, "/domains/"+id+"/auth-users/zed", nil)
	require.Equal(t, http.StatusNoContent, code, body)
	code, body, _ = f.hdo(o, http.MethodDelete, "/domains/"+id+"/auth-users/zed", nil)
	require.Equal(t, http.StatusNotFound, code, body)
	require.Equal(t, []string{"ann"}, f.view("app.example.com").AuthUsers)

	// Delete.
	code, body, _ = f.hdo(o, http.MethodDelete, "/domains/"+id, nil)
	require.Equal(t, http.StatusNoContent, code, body)
	code, body, _ = f.hdo(o, http.MethodDelete, "/domains/"+id, nil)
	require.Equal(t, http.StatusNotFound, code, body)
	require.Equal(t, "not_found", body["code"])

	// Signed out, and a token that can only read.
	code, _, _ = f.hdo(nil, http.MethodGet, base+"/domains", nil)
	require.Equal(t, http.StatusUnauthorized, code)
	ro := *o
	ro.Scopes = []string{policy.ScopeProjectsRead}
	code, _, _ = f.hdo(&ro, http.MethodGet, base+"/domains", nil)
	require.Equal(t, http.StatusOK, code)
	code, body, _ = f.hdo(&ro, http.MethodPost, base+"/domains", map[string]any{"hostname": "ro.example.com"})
	require.Equal(t, http.StatusForbidden, code, body)
	code, _, _ = f.hdo(o, http.MethodGet, "/services/"+uuid.NewString()+"/domains", nil)
	require.Equal(t, http.StatusNotFound, code)
}

// putJSON sends a raw JSON array body (the auth-users endpoint takes one).
func (f *wwwFixture) putJSON(p *auth.Principal, path string, raw []byte) int {
	f.t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	Register(humachi.New(r, cfg), f.svc)
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code
}
