package databases_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/proxyroutes"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// proxyServer makes the fixture server proxy-v2 capable (route services need it).
func (f *fixture) proxyServer(publicIP string) {
	f.t.Helper()
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapProxyV2}})
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET facts = $2, public_ip = NULLIF($3, '') WHERE id = $1", f.server.ID, facts, publicIP)
	require.NoError(f.t, err)
}

func code(t *testing.T, err error) (int, string) {
	t.Helper()
	var ae *apierr.Error
	require.True(t, errors.As(err, &ae), "want an API error, got %v", err)
	return ae.Status, ae.Code
}

func (f *fixture) routeOf(id uuid.UUID) services.RouteConfig {
	f.t.Helper()
	rc := services.DecodeConfig(f.reload(id).Config).Route
	require.NotNil(f.t, rc)
	return *rc
}

func TestPublicPortRequiresAllowListAndConfirmsOpenAccess(t *testing.T) {
	f := newFixture(t)
	f.proxyServer("198.51.100.7")
	pg := f.createDB("pg", "postgres", "17")

	v, err := f.dbs.PublicPort(f.ctx, pg.ID)
	require.NoError(t, err)
	require.False(t, v.Enabled)
	require.Equal(t, 5432, v.SuggestedPort)
	require.Equal(t, "198.51.100.7", v.Host)
	require.Equal(t, "passthrough", v.TLS)

	_, err = f.dbs.SetPublicPort(f.ctx, pg.ID, databases.PublicPortInput{ListenPort: 15432})
	st, c := code(t, err)
	require.Equal(t, 422, st)
	require.Equal(t, "allowlist_required", c)

	for _, open := range []string{"0.0.0.0/0", "::/0"} {
		_, err = f.dbs.SetPublicPort(f.ctx, pg.ID, databases.PublicPortInput{ListenPort: 15432, AllowCIDRs: []string{"10.0.0.0/8", open}})
		_, c = code(t, err)
		require.Equal(t, "open_confirmation_required", c, open)
		_, err = f.dbs.SetPublicPort(f.ctx, pg.ID, databases.PublicPortInput{ListenPort: 15432, AllowCIDRs: []string{open}, ConfirmOpen: "not-the-slug"})
		_, c = code(t, err)
		require.Equal(t, "open_confirmation_required", c, "the typed confirmation is the slug")
	}

	v, err = f.dbs.SetPublicPort(f.ctx, pg.ID, databases.PublicPortInput{ListenPort: 15432, AllowCIDRs: []string{"0.0.0.0/0"}, ConfirmOpen: pg.Slug})
	require.NoError(t, err)
	require.True(t, v.Enabled)
	require.True(t, v.OpenToAll)
	require.Contains(t, v.Warnings[len(v.Warnings)-1], "Every address on the internet")
}

func TestPublicPortStagesATCPRouteToTheDatabase(t *testing.T) {
	f := newFixture(t)
	f.proxyServer("198.51.100.7")
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)

	v, err := f.dbs.SetPublicPort(f.ctx, pg.ID, databases.PublicPortInput{
		ListenPort: 15432, AllowCIDRs: []string{"203.0.113.0/24", "198.51.100.9"}, DenyCIDRs: []string{"203.0.113.66"},
	})
	require.NoError(t, err)
	require.True(t, v.Enabled)
	require.NotNil(t, v.RouteID)
	require.Equal(t, 15432, v.ListenPort)
	require.Equal(t, []string{"203.0.113.0/24", "198.51.100.9"}, v.AllowCIDRs)
	require.Equal(t, []string{"203.0.113.66"}, v.DenyCIDRs)
	require.False(t, v.OpenToAll)
	require.True(t, v.Staged, "nothing is applied before the route is deployed")
	require.False(t, v.Deployed)
	require.True(t, v.NewEntrypoint, "a new port recreates the proxy container")
	require.Equal(t, "198.51.100.7", v.Host)

	rc := f.routeOf(*v.RouteID)
	require.Equal(t, agentprotocol.ProtoTCP, rc.Protocol)
	require.True(t, rc.PublicDatabase)
	require.False(t, rc.OpenToInternet)
	require.Equal(t, []string{"203.0.113.66"}, rc.DenyCIDRs)
	require.Len(t, rc.Upstreams, 1)
	require.Equal(t, pg.ID, *rc.Upstreams[0].ServiceID)
	require.Equal(t, 5432, rc.Upstreams[0].Port)

	// The same listen port cannot be taken twice on the server.
	redis := f.createDB("cache", "valkey", "9")
	_, err = f.dbs.SetPublicPort(f.ctx, redis.ID, databases.PublicPortInput{ListenPort: 15432, AllowCIDRs: []string{"10.0.0.0/8"}})
	var pe *proxyroutes.PortInUseError
	require.ErrorAs(t, err, &pe)

	// The suggestion moves off a taken port.
	other, err := f.dbs.PublicPort(f.ctx, redis.ID)
	require.NoError(t, err)
	require.NotEqual(t, 15432, other.SuggestedPort)
	require.Equal(t, 6379, other.SuggestedPort)

	// Reserved ports are refused.
	_, err = f.dbs.SetPublicPort(f.ctx, redis.ID, databases.PublicPortInput{ListenPort: 443, AllowCIDRs: []string{"10.0.0.0/8"}})
	_, c := code(t, err)
	require.Equal(t, "invalid_listen_port", c)

	// A deny list that covers the whole allow-list admits nobody.
	_, err = f.dbs.SetPublicPort(f.ctx, redis.ID, databases.PublicPortInput{ListenPort: 16379, AllowCIDRs: []string{"10.0.0.0/8"}, DenyCIDRs: []string{"10.0.0.0/8"}})
	_, c = code(t, err)
	require.Equal(t, "invalid_config", c)
}

func TestPublicPortUpdateAndDisable(t *testing.T) {
	f := newFixture(t)
	f.proxyServer("")
	pg := f.createDB("pg", "postgres", "17")
	v, err := f.dbs.SetPublicPort(f.ctx, pg.ID, databases.PublicPortInput{ListenPort: 15432, AllowCIDRs: []string{"10.0.0.0/8"}})
	require.NoError(t, err)
	routeID := *v.RouteID
	require.Contains(t, v.Warnings[len(v.Warnings)-1], "no public IP")

	// Changing the port and rules edits the same route in place.
	gen := f.reload(routeID).Generation
	v, err = f.dbs.SetPublicPort(f.ctx, pg.ID, databases.PublicPortInput{ListenPort: 25432, AllowCIDRs: []string{"192.0.2.0/24"}, DenyCIDRs: []string{"192.0.2.5"}})
	require.NoError(t, err)
	require.Equal(t, routeID, *v.RouteID)
	require.Equal(t, 25432, v.ListenPort)
	require.Greater(t, f.reload(routeID).Generation, gen)
	var held int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM route_ports WHERE service_id = $1 AND listen_port = 25432", routeID).Scan(&held))
	require.Equal(t, 1, held, "the port registry follows the route")

	// A generic route PATCH cannot strip the allow-list of a public database port.
	empty := []string{}
	_, err = f.routesUpdate(routeID, proxyroutes.RouteFields{AllowCIDRs: &empty})
	require.Error(t, err)

	v, err = f.dbs.DisablePublicPort(f.ctx, pg.ID)
	require.NoError(t, err)
	require.False(t, v.Enabled)
	_, err = f.dbs.DisablePublicPort(f.ctx, pg.ID)
	st, _ := code(t, err)
	require.Equal(t, 404, st)
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM route_ports WHERE service_id = $1", routeID).Scan(&held))
	require.Zero(t, held, "the port is free again")
}

func (f *fixture) routesUpdate(id uuid.UUID, fields proxyroutes.RouteFields) (services.ServiceView, error) {
	f.t.Helper()
	routes := proxyroutes.NewService(f.pool, nil, nil, f.disp, f.svcs, nil)
	return routes.Update(f.ctx, id, f.reload(id).Generation, proxyroutes.UpdateInput{RouteFields: fields})
}

func TestPublicPortScopes(t *testing.T) {
	f := newFixture(t)
	f.proxyServer("198.51.100.7")
	pg := f.createDB("pg", "postgres", "17")
	in := databases.PublicPortInput{ListenPort: 15432, AllowCIDRs: []string{"10.0.0.0/8"}}

	noProxy := f.principal([]string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite}, nil)
	_, err := f.dbs.SetPublicPort(noProxy, pg.ID, in)
	st, _ := code(t, err)
	require.Equal(t, 403, st, "publishing traffic needs proxy:write")

	readOnly := f.principal([]string{policy.ScopeProjectsRead}, nil)
	_, err = f.dbs.PublicPort(readOnly, pg.ID)
	require.NoError(t, err, "reading the state needs only projects:read")
	_, err = f.dbs.DisablePublicPort(readOnly, pg.ID)
	st, _ = code(t, err)
	require.Equal(t, 403, st)

	// Another team's token cannot see it at all.
	otherWS, err := f.q.CreateWorkspace(context.Background(), "other")
	require.NoError(t, err)
	other := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: otherWS.ID, Scopes: policy.AllScopes})
	for _, call := range []func() error{
		func() error { _, err := f.dbs.PublicPort(other, pg.ID); return err },
		func() error { _, err := f.dbs.SetPublicPort(other, pg.ID, in); return err },
		func() error { _, err := f.dbs.RevealPublicConnection(other, pg.ID, ""); return err },
	} {
		st, _ := code(t, call())
		require.Equal(t, 404, st)
	}
}

func TestPublicConnectionRevealIsAuditedAndNamesThePublicAddress(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")

	f.proxyServer("198.51.100.7")
	_, err := f.dbs.RevealPublicConnection(f.ctx, pg.ID, "")
	st, c := code(t, err)
	require.Equal(t, 409, st)
	require.Equal(t, "public_port_disabled", c)

	_, err = f.dbs.SetPublicPort(f.ctx, pg.ID, databases.PublicPortInput{ListenPort: 15432, AllowCIDRs: []string{"10.0.0.0/8"}})
	require.NoError(t, err)

	f.proxyServer("")
	_, err = f.dbs.RevealPublicConnection(f.ctx, pg.ID, "")
	_, c = code(t, err)
	require.Equal(t, "no_public_address", c)

	f.proxyServer("198.51.100.7")
	ro := f.principal([]string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite, policy.ScopeProxyWrite}, nil)
	_, err = f.dbs.RevealPublicConnection(ro, pg.ID, "")
	st, _ = code(t, err)
	require.Equal(t, 403, st, "reveal needs deployments:write")

	r, err := f.dbs.RevealPublicConnection(f.ctx, pg.ID, "")
	require.NoError(t, err)
	require.Equal(t, "DATABASE_URL", r.Key)
	require.Equal(t, "198.51.100.7", r.Host)
	require.Equal(t, 15432, r.Port)
	u, err := url.Parse(r.Value)
	require.NoError(t, err)
	require.Equal(t, net.JoinHostPort("198.51.100.7", strconv.Itoa(15432)), u.Host)
	pw, _ := u.User.Password()
	require.Len(t, pw, 32)

	_, err = f.dbs.RevealPublicConnection(f.ctx, pg.ID, "NOPE")
	st, _ = code(t, err)
	require.Equal(t, 404, st)

	var n int
	var meta string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT count(*), coalesce(max(metadata::text), '') FROM audit_events WHERE action = 'secret.reveal' AND resource_id = $1", pg.ID.String()).Scan(&n, &meta))
	require.Equal(t, 1, n)
	require.NotContains(t, meta, pw, "the secret never reaches the audit log")
	require.Contains(t, meta, `"public": true`)
}
