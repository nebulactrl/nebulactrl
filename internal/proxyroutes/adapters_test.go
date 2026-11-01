package proxyroutes_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/proxyroutes"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

func (f *fixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

// createTx runs CreateTx in its own transaction, committing or rolling it back.
func (f *fixture) createTx(ctx context.Context, commit bool, in proxyroutes.CreateInput) (db.Service, func(context.Context), error) {
	f.t.Helper()
	tx, err := f.pool.Begin(ctx)
	require.NoError(f.t, err)
	env, err := services.LoadEnvironment(ctx, db.New(tx), policy.ScopeProjectsWrite, f.env.ID)
	if err != nil {
		require.NoError(f.t, tx.Rollback(context.Background()))
		return db.Service{}, nil, err
	}
	row, after, err := f.routes.CreateTx(ctx, tx, env, in)
	if err != nil || !commit {
		require.NoError(f.t, tx.Rollback(context.Background()))
		return row, after, err
	}
	require.NoError(f.t, tx.Commit(ctx))
	return row, after, nil
}

// updateTx runs UpdateTx in its own transaction, committing or rolling it back.
func (f *fixture) updateTx(ctx context.Context, commit bool, id uuid.UUID, in proxyroutes.UpdateInput) (db.Service, func(context.Context), error) {
	f.t.Helper()
	tx, err := f.pool.Begin(ctx)
	require.NoError(f.t, err)
	sc, err := services.LoadScope(ctx, db.New(tx), policy.ScopeProjectsWrite, id)
	if err != nil {
		require.NoError(f.t, tx.Rollback(context.Background()))
		return db.Service{}, nil, err
	}
	row, after, err := f.routes.UpdateTx(ctx, tx, sc, in)
	if err != nil || !commit {
		require.NoError(f.t, tx.Rollback(context.Background()))
		return row, after, err
	}
	require.NoError(f.t, tx.Commit(ctx))
	return row, after, nil
}

func httpRouteInput(name, host string) proxyroutes.CreateInput {
	up := []proxyroutes.UpstreamInput{{Address: "192.168.1.20:8080"}}
	return proxyroutes.CreateInput{
		Name: name, Protocol: "http", Domains: []proxyroutes.DomainInput{{Hostname: host}},
		RouteFields: proxyroutes.RouteFields{Upstreams: &up},
	}
}

func (f *fixture) tcpRouteInput(name string, port int) proxyroutes.CreateInput {
	up := []proxyroutes.UpstreamInput{{Address: "192.168.1.20:5432"}}
	open := true
	return proxyroutes.CreateInput{
		Name: name, ServerID: f.server.ID, Protocol: "tcp",
		RouteFields: proxyroutes.RouteFields{Upstreams: &up, ListenPort: &port, OpenToInternet: &open},
	}
}

func TestCreateTxRolledBackLeavesNothing(t *testing.T) {
	f := newFixture(t)
	in := httpRouteInput("nas", "nas.example.com")
	in.ServerID = f.server.ID
	row, after, err := f.createTx(f.admin, false, in)
	require.NoError(t, err)
	require.NotNil(t, after)
	require.Equal(t, services.KindRoute, row.Kind)

	tcp := f.tcpRouteInput("pg", 25432)
	_, after, err = f.createTx(f.admin, false, tcp)
	require.NoError(t, err)
	require.NotNil(t, after)

	require.Equal(t, 0, f.count(`SELECT count(*) FROM services WHERE kind = 'route'`))
	require.Equal(t, 0, f.count(`SELECT count(*) FROM domains`))
	require.Equal(t, 0, f.count(`SELECT count(*) FROM route_ports`))
	require.Equal(t, 0, f.count(`SELECT count(*) FROM audit_events WHERE action = 'route.create'`))
	used, err := proxyroutes.PortInUse(context.Background(), f.q, f.server.ID, "tcp", 25432)
	require.NoError(t, err)
	require.False(t, used)
}

func TestCreateTxCommittedMatchesCreate(t *testing.T) {
	f := newFixture(t)
	in := httpRouteInput("nas", "nas.example.com")
	in.ServerID = f.server.ID
	want, err := f.routes.Create(f.admin, f.env.ID, in)
	require.NoError(t, err)

	in2 := httpRouteInput("nas two", "nas2.example.com")
	in2.ServerID = f.server.ID
	row, after, err := f.createTx(f.admin, true, in2)
	require.NoError(t, err)
	require.NotNil(t, after)
	after(context.Background())

	got := f.reloadService(row.ID)
	require.Equal(t, services.KindRoute, got.Kind)
	require.Equal(t, want.Generation, got.Generation)
	require.Equal(t, services.SourceExternal, got.SourceType)
	require.Equal(t, f.server.ID, *got.ServerID)
	require.Equal(t, services.DecodeConfig(f.reloadService(want.ID).Config), services.DecodeConfig(got.Config))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM domains WHERE service_id = $1 AND hostname = 'nas2.example.com'`, row.ID))
	require.Equal(t, 2, f.count(`SELECT count(*) FROM audit_events WHERE action = 'route.create'`))

	// tcp: the listen port is registered by the adapter exactly as by Create.
	_, _, err = f.createTx(f.admin, true, f.tcpRouteInput("pg", 25432))
	require.NoError(t, err)
	used, err := proxyroutes.PortInUse(context.Background(), f.q, f.server.ID, "tcp", 25432)
	require.NoError(t, err)
	require.True(t, used)
	// The view of the adapter-created route is served like any other.
	v, err := f.routes.Get(f.admin, row.ID)
	require.NoError(t, err)
	require.Equal(t, "nas two", v.Name)
}

func TestCreateTxErrorParity(t *testing.T) {
	f := newFixture(t)
	_, err := f.routes.Create(f.admin, f.env.ID, f.tcpRouteInput("pg", 25432))
	require.NoError(t, err)
	first := httpRouteInput("nas", "nas.example.com")
	first.ServerID = f.server.ID
	_, err = f.routes.Create(f.admin, f.env.ID, first)
	require.NoError(t, err)

	denied := httpRouteInput("bad", "bad.example.com")
	denied.ServerID = f.server.ID
	badUp := []proxyroutes.UpstreamInput{{Address: "127.0.0.1:8080"}}
	denied.Upstreams = &badUp

	dupHost := httpRouteInput("dup", "nas.example.com")
	dupHost.ServerID = f.server.ID

	noName := httpRouteInput("  ", "x.example.com")
	noName.ServerID = f.server.ID

	reserved := f.tcpRouteInput("reserved", 443)

	for _, tc := range []struct {
		name   string
		ctx    context.Context
		in     proxyroutes.CreateInput
		status int
		code   string
	}{
		{"duplicate listen port", f.admin, f.tcpRouteInput("pg2", 25432), http.StatusConflict, "port_in_use"},
		{"upstream policy", f.admin, denied, http.StatusUnprocessableEntity, "upstream_denied"},
		{"duplicate hostname", f.admin, dupHost, http.StatusConflict, "domain_conflict"},
		{"empty name", f.admin, noName, http.StatusUnprocessableEntity, "validation_failed"},
		{"reserved port", f.admin, reserved, http.StatusUnprocessableEntity, "invalid_listen_port"},
		{"no proxy:write", f.noProxy, f.tcpRouteInput("pg3", 25433), http.StatusForbidden, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, txErr := f.createTx(tc.ctx, true, tc.in)
			_, svcErr := f.routes.Create(tc.ctx, f.env.ID, tc.in)
			txStatus, txCode := apiCode(t, txErr)
			svcStatus, svcCode := apiCode(t, svcErr)
			require.Equal(t, tc.status, txStatus)
			require.Equal(t, tc.status, svcStatus)
			require.Equal(t, svcCode, txCode)
			if tc.code != "" {
				require.Equal(t, tc.code, txCode)
			}
			require.Equal(t, svcErr.Error(), txErr.Error())
		})
	}

	// The port-conflict error names the holder, like the original.
	_, _, err = f.createTx(f.admin, true, f.tcpRouteInput("pg2", 25432))
	var pe *proxyroutes.PortInUseError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, "pg", pe.HeldBy)

	// Nothing leaked from the failed attempts.
	require.Equal(t, 2, f.count(`SELECT count(*) FROM services WHERE kind = 'route'`))
}

func TestUpdateTx(t *testing.T) {
	f := newFixture(t)
	v, err := f.routes.Create(f.admin, f.env.ID, f.tcpRouteInput("pg", 25432))
	require.NoError(t, err)
	other, err := f.routes.Create(f.admin, f.env.ID, f.tcpRouteInput("other", 25440))
	require.NoError(t, err)
	gen := v.Generation
	updates := func() int {
		return f.count(`SELECT count(*) FROM audit_events WHERE action = 'route.update' AND resource_id = $1`, v.ID.String())
	}

	// No-op: nothing written, generation unchanged, no audit entry.
	row, after, err := f.updateTx(f.admin, true, v.ID, proxyroutes.UpdateInput{RouteFields: proxyroutes.RouteFields{ListenPort: new(25432)}})
	require.NoError(t, err)
	require.NotNil(t, after)
	after(context.Background())
	require.Equal(t, gen, row.Generation)
	require.Equal(t, gen, f.reloadService(v.ID).Generation)
	require.Equal(t, 0, updates())

	// Rolled back: the port, config, generation and audit trail are untouched.
	row, after, err = f.updateTx(f.admin, false, v.ID, proxyroutes.UpdateInput{Name: new("renamed"), RouteFields: proxyroutes.RouteFields{ListenPort: new(25433)}})
	require.NoError(t, err)
	require.NotNil(t, after)
	require.Equal(t, gen+1, row.Generation)
	cur := f.reloadService(v.ID)
	require.Equal(t, gen, cur.Generation)
	require.Equal(t, "pg", cur.Name)
	require.Equal(t, 0, updates())
	used, _ := proxyroutes.PortInUse(context.Background(), f.q, f.server.ID, "tcp", 25432)
	require.True(t, used)
	used, _ = proxyroutes.PortInUse(context.Background(), f.q, f.server.ID, "tcp", 25433)
	require.False(t, used)

	// Committed: same result as Update, one generation bump, one audit entry.
	row, after, err = f.updateTx(f.admin, true, v.ID, proxyroutes.UpdateInput{Name: new("renamed"), RouteFields: proxyroutes.RouteFields{ListenPort: new(25433)}})
	require.NoError(t, err)
	require.NotNil(t, after)
	after(context.Background())
	require.Equal(t, gen+1, row.Generation)
	cur = f.reloadService(v.ID)
	require.Equal(t, gen+1, cur.Generation)
	require.Equal(t, "renamed", cur.Name)
	require.Equal(t, 25433, services.DecodeConfig(cur.Config).Route.ListenPort)
	require.Equal(t, 1, updates())
	used, _ = proxyroutes.PortInUse(context.Background(), f.q, f.server.ID, "tcp", 25432)
	require.False(t, used, "the old port is released")
	used, _ = proxyroutes.PortInUse(context.Background(), f.q, f.server.ID, "tcp", 25433)
	require.True(t, used)

	viaSvc, err := f.routes.Update(f.admin, other.ID, other.Generation, proxyroutes.UpdateInput{Name: new("renamed"), RouteFields: proxyroutes.RouteFields{ListenPort: new(25441)}})
	require.NoError(t, err)
	require.Equal(t, other.Generation+1, viaSvc.Generation)
	require.Equal(t, cur.Generation-gen, viaSvc.Generation-other.Generation)

	// Error parity with Update.
	bad := []proxyroutes.UpstreamInput{{Address: "127.0.0.1:5432"}}
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		in     proxyroutes.UpdateInput
		status int
		code   string
	}{
		{"duplicate listen port", f.admin, proxyroutes.UpdateInput{RouteFields: proxyroutes.RouteFields{ListenPort: new(25441)}}, http.StatusConflict, "port_in_use"},
		{"upstream policy", f.admin, proxyroutes.UpdateInput{RouteFields: proxyroutes.RouteFields{Upstreams: &bad}}, http.StatusUnprocessableEntity, "upstream_denied"},
		{"empty name", f.admin, proxyroutes.UpdateInput{Name: new(" ")}, http.StatusUnprocessableEntity, "validation_failed"},
		{"reserved port", f.admin, proxyroutes.UpdateInput{RouteFields: proxyroutes.RouteFields{ListenPort: new(443)}}, http.StatusUnprocessableEntity, "invalid_listen_port"},
		{"no proxy:write", f.noProxy, proxyroutes.UpdateInput{Name: new("x")}, http.StatusForbidden, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, txErr := f.updateTx(tc.ctx, true, v.ID, tc.in)
			cur := f.reloadService(v.ID)
			_, svcErr := f.routes.Update(tc.ctx, v.ID, cur.Generation, tc.in)
			txStatus, txCode := apiCode(t, txErr)
			svcStatus, svcCode := apiCode(t, svcErr)
			require.Equal(t, tc.status, txStatus)
			require.Equal(t, tc.status, svcStatus)
			require.Equal(t, svcCode, txCode)
			if tc.code != "" {
				require.Equal(t, tc.code, txCode)
			}
			require.Equal(t, svcErr.Error(), txErr.Error())
			require.Equal(t, cur.Generation, f.reloadService(v.ID).Generation, "failed updates never bump")
		})
	}
}

func TestUpdateTxHTTPRouteKeepsProtocol(t *testing.T) {
	f := newFixture(t)
	in := httpRouteInput("nas", "nas.example.com")
	in.ServerID = f.server.ID
	v, err := f.routes.Create(f.admin, f.env.ID, in)
	require.NoError(t, err)

	// An http route cannot grow tcp-only settings; the protocol itself has no input.
	_, _, err = f.updateTx(f.admin, true, v.ID, proxyroutes.UpdateInput{RouteFields: proxyroutes.RouteFields{ListenPort: new(25432)}})
	status, code := apiCode(t, err)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "invalid_config", code)

	// A non-route service is a 404 for UpdateTx.
	web := f.web("site")
	_, _, err = f.updateTx(f.admin, true, web.ID, proxyroutes.UpdateInput{Name: new("x")})
	status, _ = apiCode(t, err)
	require.Equal(t, http.StatusNotFound, status)
}
