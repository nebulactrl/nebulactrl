package proxyroutes_test

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
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/proxyroutes"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

func ptr[T any](v T) *T { return &v }

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

// insertService adds a service of any kind to env without a deployment.
func (f *fixture) insertService(env db.Environment, slug, kind, source string, server *uuid.UUID) db.Service {
	f.t.Helper()
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: env.ID, Name: slug, Slug: slug, Kind: kind, SourceType: source, Config: json.RawMessage(`{}`), ServerID: server,
	})
	require.NoError(f.t, err)
	return sv
}

func (f *fixture) otherServer(name string) db.Server {
	f.t.Helper()
	srv, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: name})
	require.NoError(f.t, err)
	f.exec(`UPDATE servers SET status = 'online', facts = (SELECT facts FROM servers WHERE id = $2) WHERE id = $1`, srv.ID, f.server.ID)
	srv, err = f.q.LoadServerRow(context.Background(), db.LoadServerRowParams{ID: srv.ID})
	require.NoError(f.t, err)
	return srv
}

func (f *fixture) updatePort(ctx context.Context, id uuid.UUID, in proxyroutes.PortRouteUpdate) (services.Scope, services.RouteConfig, error) {
	var sc services.Scope
	var before services.RouteConfig
	err := db.InTx(ctx, f.pool, func(tx pgx.Tx) error {
		var err error
		sc, before, err = proxyroutes.UpdatePortRouteTx(ctx, tx, id, in)
		return err
	})
	return sc, before, err
}

func (f *fixture) portInput(target uuid.UUID, port int) proxyroutes.PortRouteInput {
	return proxyroutes.PortRouteInput{
		EnvironmentID: f.env.ID, TargetServiceID: target, Name: "db public", Protocol: "tcp", ListenPort: port, TargetPort: 5432,
		AllowCIDRs: []string{"203.0.113.0/24"},
	}
}

func TestUpdatePortRouteTxMovesThePortAndStagesTheNewRules(t *testing.T) {
	f := newFixture(t)
	web := f.web("db")
	id, err := f.addPort(f.admin, f.portInput(web.ID, 25432))
	require.NoError(t, err)
	_, err = f.addPort(f.admin, func() proxyroutes.PortRouteInput {
		in := f.portInput(web.ID, 27000)
		in.Name = "second"
		return in
	}())
	require.NoError(t, err)

	sc, before, err := f.updatePort(f.admin, id, proxyroutes.PortRouteUpdate{ListenPort: 26000, AllowCIDRs: []string{"198.51.100.0/24"}, DenyCIDRs: []string{"198.51.100.7"}})
	require.NoError(t, err)
	require.Equal(t, 25432, before.ListenPort, "the config before the change is returned")
	require.Equal(t, []string{"203.0.113.0/24"}, before.AllowCIDRs)
	require.Equal(t, int64(2), sc.Service.Generation)

	rc := services.DecodeConfig(f.reloadService(id).Config).Route
	require.Equal(t, 26000, rc.ListenPort)
	require.Equal(t, []string{"198.51.100.0/24"}, rc.AllowCIDRs)
	require.Equal(t, []string{"198.51.100.7"}, rc.DenyCIDRs)
	require.Equal(t, web.ID, *rc.Upstreams[0].ServiceID, "the target is kept")

	used, err := proxyroutes.PortInUse(context.Background(), f.q, f.server.ID, "tcp", 25432)
	require.NoError(t, err)
	require.False(t, used, "the old port is free again")
	used, err = proxyroutes.PortInUse(context.Background(), f.q, f.server.ID, "tcp", 26000)
	require.NoError(t, err)
	require.True(t, used)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action = 'route.update' AND resource_id = $1 AND metadata @> '{"listenPort":26000,"allowCidrs":1,"denyCidrs":1}'`, id.String()))

	// Keeping the port is not a clash with itself; taking another route's port is.
	_, _, err = f.updatePort(f.admin, id, proxyroutes.PortRouteUpdate{ListenPort: 26000, OpenToInternet: true})
	require.NoError(t, err)
	_, _, err = f.updatePort(f.admin, id, proxyroutes.PortRouteUpdate{ListenPort: 27000, OpenToInternet: true})
	var pe *proxyroutes.PortInUseError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, "second", pe.HeldBy)
	require.Equal(t, 26000, services.DecodeConfig(f.reloadService(id).Config).Route.ListenPort, "a refused update changed nothing")
}

func TestUpdatePortRouteTxRefusals(t *testing.T) {
	f := newFixture(t)
	web := f.web("db")
	id, err := f.addPort(f.admin, f.portInput(web.ID, 25432))
	require.NoError(t, err)
	ok := proxyroutes.PortRouteUpdate{ListenPort: 26000, OpenToInternet: true}

	for name, tc := range map[string]struct {
		ctx    context.Context
		id     uuid.UUID
		in     proxyroutes.PortRouteUpdate
		status int
		code   string
	}{
		"not proxy:write":      {f.noProxy, id, ok, http.StatusForbidden, "forbidden"},
		"another workspace":    {f.other, id, ok, http.StatusNotFound, "not_found"},
		"unknown route":        {f.admin, uuid.New(), ok, http.StatusNotFound, "not_found"},
		"not a route":          {f.admin, web.ID, ok, http.StatusNotFound, "not_found"},
		"reserved port":        {f.admin, id, proxyroutes.PortRouteUpdate{ListenPort: 443, OpenToInternet: true}, http.StatusUnprocessableEntity, "invalid_listen_port"},
		"not a CIDR":           {f.admin, id, proxyroutes.PortRouteUpdate{ListenPort: 26000, AllowCIDRs: []string{"nope"}}, http.StatusUnprocessableEntity, "invalid_config"},
		"nobody allowed":       {f.admin, id, proxyroutes.PortRouteUpdate{ListenPort: 26000}, http.StatusUnprocessableEntity, "invalid_config"},
		"deny list not a CIDR": {f.admin, id, proxyroutes.PortRouteUpdate{ListenPort: 26000, OpenToInternet: true, DenyCIDRs: []string{"x"}}, http.StatusUnprocessableEntity, "invalid_config"},
	} {
		_, _, err := f.updatePort(tc.ctx, tc.id, tc.in)
		status, code := apiCode(t, err)
		require.Equal(t, tc.status, status, name)
		require.Equal(t, tc.code, code, name)
	}

	f.exec(`UPDATE servers SET facts = '{"os":"linux"}' WHERE id = $1`, f.server.ID)
	_, _, err = f.updatePort(f.admin, id, ok)
	status, _ := apiCode(t, err)
	require.Equal(t, http.StatusUnprocessableEntity, status, "an agent without the new proxy cannot take new rules")

	rc := services.DecodeConfig(f.reloadService(id).Config).Route
	require.Equal(t, 25432, rc.ListenPort)
	require.Equal(t, int64(1), f.reloadService(id).Generation, "no refused update staged anything")
}

func TestAddPortRouteTxRefusals(t *testing.T) {
	f := newFixture(t)
	web := f.web("db")
	first, err := f.addPort(f.admin, f.portInput(web.ID, 25432))
	require.NoError(t, err)
	staging, err := f.q.CreateEnvironment(context.Background(), db.CreateEnvironmentParams{ProjectID: f.project.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	elsewhere := f.insertService(staging, "cache", services.KindApp, services.SourceImage, &f.server.ID)
	unplaced := f.insertService(f.env, "unplaced", services.KindApp, services.SourceImage, nil)

	with := func(mut func(*proxyroutes.PortRouteInput)) error {
		in := f.portInput(web.ID, 25433)
		mut(&in)
		_, err := f.addPort(f.admin, in)
		return err
	}
	for name, tc := range map[string]struct {
		mut    func(*proxyroutes.PortRouteInput)
		status int
		code   string
	}{
		"protocol":                {func(in *proxyroutes.PortRouteInput) { in.Protocol = "icmp" }, 422, "invalid_config"},
		"target without a server": {func(in *proxyroutes.PortRouteInput) { in.TargetServiceID = unplaced.ID }, 422, "no_server"},
		"target of another env":   {func(in *proxyroutes.PortRouteInput) { in.TargetServiceID = elsewhere.ID }, 404, "not_found"},
		"target is a route":       {func(in *proxyroutes.PortRouteInput) { in.TargetServiceID = first }, 422, "invalid_upstream"},
		"unknown target":          {func(in *proxyroutes.PortRouteInput) { in.TargetServiceID = uuid.New() }, 404, "not_found"},
		"not a CIDR":              {func(in *proxyroutes.PortRouteInput) { in.AllowCIDRs = []string{"nope"} }, 422, "invalid_config"},
		"nobody allowed":          {func(in *proxyroutes.PortRouteInput) { in.AllowCIDRs, in.OpenToInternet = nil, false }, 422, "invalid_config"},
	} {
		status, code := apiCode(t, with(tc.mut))
		require.Equal(t, tc.status, status, name)
		require.Equal(t, tc.code, code, name)
	}
	require.Equal(t, 1, f.count(`SELECT count(*) FROM route_ports`), "only the first route holds a port")

	f.exec(`UPDATE servers SET facts = '{"os":"linux"}' WHERE id = $1`, f.server.ID)
	status, _ := apiCode(t, with(func(*proxyroutes.PortRouteInput) {}))
	require.Equal(t, 422, status, "an agent without the new proxy cannot expose ports")
}

func TestAddPortRouteTxNamesAnUnnamedRouteAfterItsTarget(t *testing.T) {
	f := newFixture(t)
	web := f.web("db")
	in := f.portInput(web.ID, 25432)
	in.Name = ""
	id, err := f.addPort(f.admin, in)
	require.NoError(t, err)
	require.Equal(t, "db tcp :25432", f.reloadService(id).Name)
}

func TestCreateRouteRulesAroundManagedTargets(t *testing.T) {
	f := newFixture(t)
	web := f.web("web")
	staging, err := f.q.CreateEnvironment(context.Background(), db.CreateEnvironmentParams{ProjectID: f.project.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	foreign := f.insertService(staging, "cache", services.KindApp, services.SourceImage, &f.server.ID)
	stack := f.insertService(f.env, "stack", services.KindStack, "compose", &f.server.ID)
	second := f.otherServer("agent2")
	remote := f.insertService(f.env, "remote", services.KindApp, services.SourceImage, &second.ID)
	asRoute, err := f.addPort(f.admin, f.portInput(web.ID, 25432))
	require.NoError(t, err)

	create := func(target uuid.UUID) error {
		_, err := f.routes.Create(f.admin, f.env.ID, proxyroutes.CreateInput{
			Name: "front", ServerID: f.server.ID, Protocol: "http", Domains: []proxyroutes.DomainInput{{Hostname: "front.example.com"}},
			RouteFields: proxyroutes.RouteFields{Upstreams: &[]proxyroutes.UpstreamInput{{ServiceID: &target, Port: 80}}},
		})
		return err
	}
	for name, tc := range map[string]struct {
		target uuid.UUID
		status int
		code   string
	}{
		"another environment": {foreign.ID, 404, "not_found"},
		"unknown service":     {uuid.New(), 404, "not_found"},
		"a route":             {asRoute, 422, "invalid_upstream"},
		"a compose stack":     {stack.ID, 422, "invalid_upstream"},
		"another server":      {remote.ID, 422, "invalid_upstream"},
	} {
		status, code := apiCode(t, create(tc.target))
		require.Equal(t, tc.status, status, name)
		require.Equal(t, tc.code, code, name)
	}
	require.NoError(t, create(web.ID))
}

func TestCreateRouteRefusals(t *testing.T) {
	f := newFixture(t)
	up := []proxyroutes.UpstreamInput{{Address: "192.168.1.20:8080"}}
	base := func() proxyroutes.CreateInput {
		return proxyroutes.CreateInput{Name: "nas", ServerID: f.server.ID, Protocol: "http", Domains: []proxyroutes.DomainInput{{Hostname: "nas.example.com"}}, RouteFields: proxyroutes.RouteFields{Upstreams: &up}}
	}
	revoked := f.otherServer("gone")
	f.exec(`UPDATE servers SET status = 'revoked' WHERE id = $1`, revoked.ID)

	for name, tc := range map[string]struct {
		mut    func(*proxyroutes.CreateInput)
		status int
		code   string
	}{
		"no name":        {func(in *proxyroutes.CreateInput) { in.Name = "  " }, 422, "validation_failed"},
		"unknown server": {func(in *proxyroutes.CreateInput) { in.ServerID = uuid.New() }, 404, "not_found"},
		"revoked server": {func(in *proxyroutes.CreateInput) { in.ServerID = revoked.ID }, 422, "server_revoked"},
		"domains on a tcp route": {func(in *proxyroutes.CreateInput) {
			in.Protocol = "tcp"
			in.ListenPort, in.OpenToInternet = ptr(25000), ptr(true)
		}, 422, "invalid_config"},
		"bad hostname": {func(in *proxyroutes.CreateInput) { in.Domains = []proxyroutes.DomainInput{{Hostname: "not a host"}} }, 422, ""},
		"duplicate hostnames": {func(in *proxyroutes.CreateInput) {
			in.Domains = []proxyroutes.DomainInput{{Hostname: "a.example.com"}, {Hostname: "A.example.com"}}
		}, 422, "invalid_hostname"},
		"reserved listen port": {func(in *proxyroutes.CreateInput) {
			in.Protocol, in.Domains = "tcp", nil
			in.ListenPort, in.OpenToInternet = ptr(443), ptr(true)
		}, 422, "invalid_listen_port"},
		"no upstream": {func(in *proxyroutes.CreateInput) { in.Upstreams = &[]proxyroutes.UpstreamInput{} }, 422, "invalid_config"},
		"loopback upstream": {func(in *proxyroutes.CreateInput) {
			in.Upstreams = &[]proxyroutes.UpstreamInput{{Address: "127.0.0.1:80"}}
		}, 422, "upstream_denied"},
		"cidr that is no cidr": {func(in *proxyroutes.CreateInput) {
			in.Protocol, in.Domains = "tcp", nil
			in.ListenPort, in.AllowCIDRs = ptr(25000), &[]string{"nope"}
		}, 422, "invalid_config"},
	} {
		in := base()
		tc.mut(&in)
		_, err := f.routes.Create(f.admin, f.env.ID, in)
		status, code := apiCode(t, err)
		require.Equal(t, tc.status, status, name)
		if tc.code != "" {
			require.Equal(t, tc.code, code, name)
		}
	}
	require.Zero(t, f.count(`SELECT count(*) FROM services WHERE kind = 'route'`), "no refused create left a route behind")
	require.Zero(t, f.count(`SELECT count(*) FROM domains`))
}

func TestCreateRouteStoresEveryHTTPSetting(t *testing.T) {
	f := newFixture(t)
	view, err := f.routes.Create(f.admin, f.env.ID, proxyroutes.CreateInput{
		Name: "!!!", ServerID: f.server.ID, Protocol: "http", Domains: []proxyroutes.DomainInput{{Hostname: "NAS.example.com"}},
		RouteFields: proxyroutes.RouteFields{
			Upstreams: &[]proxyroutes.UpstreamInput{{Address: "192.168.1.20:8443"}}, Scheme: ptr("https"), InsecureSkipVerify: ptr(true),
			ServerName: ptr("nas.internal"), PassHost: ptr(false), HealthPath: ptr("/up"), VerifySkip: ptr(true),
		},
	})
	require.NoError(t, err)
	require.Equal(t, "route", view.Slug, "a name without letters falls back to 'route'")
	rc := f.reloadService(view.ID).Config
	got := services.DecodeConfig(rc).Route
	require.Equal(t, "https", got.Scheme)
	require.True(t, got.InsecureSkipVerify)
	require.Equal(t, "nas.internal", got.ServerName)
	require.NotNil(t, got.PassHost)
	require.False(t, *got.PassHost)
	require.Equal(t, "/up", got.HealthPath)
	require.True(t, got.VerifySkip)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM domains WHERE service_id = $1 AND hostname = 'nas.example.com'`, view.ID))

	// A second route of the same name gets the next free slug; an exact slug is kept as given.
	again, err := f.routes.Create(f.admin, f.env.ID, proxyroutes.CreateInput{
		Name: "!!!", ServerID: f.server.ID, Protocol: "http", Domains: []proxyroutes.DomainInput{{Hostname: "other.example.com"}},
		RouteFields: proxyroutes.RouteFields{Upstreams: &[]proxyroutes.UpstreamInput{{Address: "192.168.1.20:8443"}}},
	})
	require.NoError(t, err)
	require.Equal(t, "route-2", again.Slug)
	exact, err := f.routes.Create(f.admin, f.env.ID, proxyroutes.CreateInput{
		Name: "Some name", Slug: "exact-slug", ServerID: f.server.ID, Protocol: "http", Domains: []proxyroutes.DomainInput{{Hostname: "third.example.com"}},
		RouteFields: proxyroutes.RouteFields{Upstreams: &[]proxyroutes.UpstreamInput{{Address: "192.168.1.20:8443"}}},
	})
	require.NoError(t, err)
	require.Equal(t, "exact-slug", exact.Slug)
}

func TestRoutesOnlyAnswerForRouteServices(t *testing.T) {
	f := newFixture(t)
	web := f.web("web")
	_, err := f.routes.Get(f.admin, web.ID)
	status, code := apiCode(t, err)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", code)
	_, err = f.routes.Update(f.admin, web.ID, 1, proxyroutes.UpdateInput{Name: ptr("x")})
	status, _ = apiCode(t, err)
	require.Equal(t, http.StatusNotFound, status)
	status, _ = apiCode(t, f.routes.Delete(f.admin, web.ID))
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM services WHERE id = $1`, web.ID), "a delete aimed at a non-route changed nothing")
}

func TestServerPortsListsStagedAndDeployedRoutes(t *testing.T) {
	f := newFixture(t)
	web := f.web("db")
	staged, err := f.addPort(f.admin, f.portInput(web.ID, 25432))
	require.NoError(t, err)
	deployed, err := f.addPort(f.admin, func() proxyroutes.PortRouteInput {
		in := f.portInput(web.ID, 25433)
		in.Protocol, in.Name = "udp", "udp one"
		in.AllowCIDRs, in.OpenToInternet = nil, true
		return in
	}())
	require.NoError(t, err)
	f.markDeployed(f.reloadService(deployed))

	ports, err := f.routes.ServerPorts(f.admin, f.server.ID)
	require.NoError(t, err)
	require.Len(t, ports, 2)
	by := map[uuid.UUID]proxyroutes.PortView{}
	for _, p := range ports {
		by[p.ServiceID] = p
	}
	require.Equal(t, proxyroutes.PortView{Protocol: "tcp", Port: 25432, ServiceID: staged, ServiceName: "db public", Deployed: false, ProjectID: &f.project.ID}, by[staged])
	require.True(t, by[deployed].Deployed)
	require.Equal(t, "udp", by[deployed].Protocol)

	// A caller who cannot see the project sees the port but not who holds it.
	blind := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: uuid.New(), WorkspaceID: f.ws.ID, Scopes: []string{"servers:read"}, ProjectIDs: []uuid.UUID{},
	})
	ports, err = f.routes.ServerPorts(blind, f.server.ID)
	require.NoError(t, err)
	require.Len(t, ports, 2)
	for _, p := range ports {
		require.Empty(t, p.ServiceName, "holder names of projects the caller cannot see are omitted")
		require.Nil(t, p.ProjectID)
		require.NotZero(t, p.Port)
	}

	_, err = f.routes.ServerPorts(f.other, f.server.ID)
	status, _ := apiCode(t, err)
	require.Equal(t, http.StatusNotFound, status)
	_, err = f.routes.ServerPorts(principalNoServers(f), f.server.ID)
	status, _ = apiCode(t, err)
	require.Equal(t, http.StatusForbidden, status)
}

func principalNoServers(f *fixture) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: f.ws.ID, Scopes: []string{"projects:read"}})
}

func TestCheckUpstreamsAppliesTheServersPolicy(t *testing.T) {
	f := newFixture(t)
	web := f.web("web")
	check := func(up services.RouteUpstream) error {
		return f.routes.CheckUpstreams(f.admin, f.q, services.RouteConfig{Protocol: "http", Upstreams: []services.RouteUpstream{up}}, f.env.ID, f.server)
	}
	require.NoError(t, check(services.RouteUpstream{Address: "192.168.1.20:8080"}))
	status, code := apiCode(t, check(services.RouteUpstream{Address: "127.0.0.1:80"}))
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "upstream_denied", code)
	require.NoError(t, check(services.RouteUpstream{ServiceID: &web.ID, Port: 80}))
	missing := uuid.New()
	status, _ = apiCode(t, check(services.RouteUpstream{ServiceID: &missing, Port: 80}))
	require.Equal(t, http.StatusNotFound, status)
}

// ---- HTTP ----

func TestHTTPRouteLifecycle(t *testing.T) {
	f := newFixture(t)
	handler := func(ctx context.Context) http.Handler {
		p, _ := auth.PrincipalFrom(ctx)
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
			})
		})
		cfg := huma.DefaultConfig("test", "1")
		cfg.CreateHooks = nil
		proxyroutes.Register(humachi.New(r, cfg), f.routes)
		return r
	}
	admin, noProxy := handler(f.admin), handler(f.noProxy)
	do := func(h http.Handler, method, path string, body any, headers ...string) (*httptest.ResponseRecorder, map[string]any) {
		var rd *bytes.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			require.NoError(t, err)
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
		h.ServeHTTP(rec, req)
		var m map[string]any
		if rec.Body.Len() > 0 {
			_ = json.Unmarshal(rec.Body.Bytes(), &m)
		}
		return rec, m
	}

	create := map[string]any{
		"name": "NAS", "serverId": f.server.ID.String(), "protocol": "http",
		"domains": []any{map[string]any{"hostname": "nas.example.com"}}, "upstreams": []any{map[string]any{"address": "192.168.1.20:8080"}},
	}
	rec, _ := do(noProxy, http.MethodPost, "/environments/"+f.env.ID.String()+"/routes", create)
	require.Equal(t, http.StatusForbidden, rec.Code, "creating a route needs proxy:write")

	rec, body := do(admin, http.MethodPost, "/environments/"+f.env.ID.String()+"/routes", create)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	id := body["id"].(string)
	require.Equal(t, "route", body["kind"])
	require.Equal(t, `"1"`, rec.Header().Get("ETag"))

	rec, body = do(admin, http.MethodGet, "/routes/"+id, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "NAS", body["name"])
	rec, _ = do(admin, http.MethodGet, "/routes/"+uuid.NewString(), nil)
	require.Equal(t, http.StatusNotFound, rec.Code)

	patch := map[string]any{"name": "NAS two", "healthPath": "/up"}
	rec, body = do(admin, http.MethodPatch, "/routes/"+id, patch)
	require.Equal(t, http.StatusPreconditionRequired, rec.Code)
	require.Equal(t, "if_match_required", body["code"])
	rec, body = do(admin, http.MethodPatch, "/routes/"+id, patch, "If-Match", "soon")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_if_match", body["code"])
	rec, body = do(admin, http.MethodPatch, "/routes/"+id, patch, "If-Match", `"9"`)
	require.Equal(t, http.StatusPreconditionFailed, rec.Code)
	require.Equal(t, "generation_conflict", body["code"])
	rec, _ = do(noProxy, http.MethodPatch, "/routes/"+id, patch, "If-Match", `"1"`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	rec, body = do(admin, http.MethodPatch, "/routes/"+id, patch, "If-Match", `"1"`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "NAS two", body["name"])
	require.Equal(t, `"2"`, rec.Header().Get("ETag"))

	rec, body = do(admin, http.MethodGet, "/servers/"+f.server.ID.String()+"/ports", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, body["items"], "an http route holds no tcp/udp port")

	rec, _ = do(admin, http.MethodDelete, "/routes/"+id, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	rec, _ = do(admin, http.MethodGet, "/routes/"+id, nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
}
