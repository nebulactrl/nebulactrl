package databases_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
)

// restToken is a REST token as authentication builds it: guarded unless exempt.
// Approvals are bound to the token, so a test keeps one context per token.
func (f *fixture) restToken() context.Context {
	tok := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: f.userID, TokenID: &tok, WorkspaceID: f.ws.ID, Role: auth.RoleOwner,
		Label: "ci", Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
}

func (f *fixture) person() context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: f.userID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})
}

func (f *fixture) installGuard() *prodguard.Guard {
	f.t.Helper()
	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: f.box})
	g.Install()
	f.t.Cleanup(prodguardtest.Uninstall)
	return g
}

func (f *fixture) approve(g *prodguard.Guard, err error) {
	f.t.Helper()
	ar, ok := prodguard.AsRequired(err)
	require.True(f.t, ok, "want approval_required, got %v", err)
	_, aerr := g.Approve(f.person(), ar.ApprovalID)
	require.NoError(f.t, aerr)
}

func (f *fixture) routePorts() int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM route_ports").Scan(&n))
	return n
}

// A REST token cannot open or close the public port of a production database
// without a person's approval; closing needs one approval only (the removal of
// the port's route service is covered by it); a browser session is not asked.
func TestPublicPortInProductionNeedsApprovalForRESTTokens(t *testing.T) {
	f := newFixture(t) // the fixture environment is production
	g := f.installGuard()
	f.proxyServer("198.51.100.7")
	pg := f.createDB("pg", "postgres", "17")
	rest := f.restToken()

	in := databases.PublicPortInput{ListenPort: 15432, AllowCIDRs: []string{"203.0.113.0/24"}}
	_, setErr := f.dbs.SetPublicPort(rest, pg.ID, in)
	ar, ok := prodguard.AsRequired(setErr)
	require.True(t, ok, "got %v", setErr)
	require.Contains(t, ar.Plan.Summary, "Open the public database port")
	v, err := f.dbs.PublicPort(f.ctx, pg.ID)
	require.NoError(t, err)
	require.False(t, v.Enabled, "nothing is created before the approval")
	require.Zero(t, f.routePorts())

	// A different request is a different call: the approval below does not carry over to it.
	other := in
	other.ListenPort = 25432
	_, otherErr := f.dbs.SetPublicPort(rest, pg.ID, other)
	_, ok = prodguard.AsRequired(otherErr)
	require.True(t, ok, "got %v", otherErr)

	f.approve(g, setErr)
	v, err = f.dbs.SetPublicPort(rest, pg.ID, in)
	require.NoError(t, err)
	require.True(t, v.Enabled)
	require.NotNil(t, v.RouteID)
	require.Equal(t, 1, f.routePorts())

	// The approval was used up.
	_, again := f.dbs.SetPublicPort(rest, pg.ID, in)
	_, ok = prodguard.AsRequired(again)
	require.True(t, ok, "got %v", again)

	// Closing the port needs its own approval, and nothing changes before it.
	_, disErr := f.dbs.DisablePublicPort(rest, pg.ID)
	ar, ok = prodguard.AsRequired(disErr)
	require.True(t, ok, "got %v", disErr)
	require.Contains(t, ar.Plan.Summary, "Close the public database port")
	v, err = f.dbs.PublicPort(f.ctx, pg.ID)
	require.NoError(t, err)
	require.True(t, v.Enabled, "the port is still open")
	require.Equal(t, 1, f.routePorts())

	// One approval is enough: deleting the route service does not ask again.
	f.approve(g, disErr)
	v, err = f.dbs.DisablePublicPort(rest, pg.ID)
	require.NoError(t, err)
	require.False(t, v.Enabled)
	require.Zero(t, f.routePorts(), "the port is free again")

	// A person in the browser is never asked.
	v, err = f.dbs.SetPublicPort(f.ctx, pg.ID, in)
	require.NoError(t, err)
	require.True(t, v.Enabled)
	_, err = f.dbs.DisablePublicPort(f.ctx, pg.ID)
	require.NoError(t, err)
}

// An environment that is not production is not guarded.
func TestPublicPortOutsideProductionIsNotGuarded(t *testing.T) {
	f := newFixture(t)
	f.installGuard()
	f.proxyServer("198.51.100.7")
	_, err := f.pool.Exec(context.Background(), "UPDATE environments SET is_production = false WHERE id = $1", f.env.ID)
	require.NoError(t, err)
	pg := f.createDB("pg", "postgres", "17")
	rest := f.restToken()

	v, err := f.dbs.SetPublicPort(rest, pg.ID, databases.PublicPortInput{ListenPort: 15432, AllowCIDRs: []string{"203.0.113.0/24"}})
	require.NoError(t, err)
	require.True(t, v.Enabled)
	v, err = f.dbs.DisablePublicPort(rest, pg.ID)
	require.NoError(t, err)
	require.False(t, v.Enabled)
}
