package tunnel

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// restToken is a REST token as authentication builds it: guarded unless exempt.
func (f *fixture) restToken() context.Context {
	p, _ := auth.PrincipalFrom(f.admin)
	tok := uuid.New()
	return auth.WithPrincipal(bg, &auth.Principal{
		Type: auth.TypeToken, UserID: p.UserID, TokenID: &tok, WorkspaceID: f.ws, Role: auth.RoleOwner,
		Label: "ci", Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
}

// person is a signed-in person (a browser session) who may approve.
func (f *fixture) person() context.Context {
	p, _ := auth.PrincipalFrom(f.admin)
	return auth.WithPrincipal(bg, &auth.Principal{
		Type: auth.TypeUser, UserID: p.UserID, WorkspaceID: f.ws, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})
}

func (f *fixture) installGuard() *prodguard.Guard {
	f.t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(f.t, err)
	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: box})
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

// requireUntouched checks that nothing of a refused Disable happened.
func (f *fixture) requireUntouched(state string, syncs int) {
	f.t.Helper()
	require.Equal(f.t, state, f.row().State, "the tunnel is unchanged")
	require.Empty(f.t, f.disp.of(agentprotocol.KindRemoveTunnel), "no connector removal was sent")
	require.Equal(f.t, syncs, f.cf.syncCount(), "DNS was not re-synced")
	require.Empty(f.t, f.deleteJobArgs(), "no delete is scheduled")
}

// A REST token cannot turn off the tunnel of a server that hosts a production
// service without a person's approval; the approval is single use and bound to
// the force flag.
func TestDisableOfProductionHostNeedsApprovalForRESTTokens(t *testing.T) {
	f := newFixture(t) // the fixture environment is production
	f.deployed("web", "app.example.com", true)
	f.enable()
	f.activate()
	g := f.installGuard()
	state, syncs := f.row().State, f.cf.syncCount()
	rest := f.restToken()

	_, err := f.svc.Disable(rest, f.server.ID, false)
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Summary, "Agent 1")
	f.requireUntouched(state, syncs)

	// Dropping the record without asking Cloudflare is a different call.
	f.approve(g, err)
	_, forceErr := f.svc.Disable(rest, f.server.ID, true)
	_, ok = prodguard.AsRequired(forceErr)
	require.True(t, ok, "the approval does not carry over to force: %v", forceErr)
	f.requireUntouched(state, syncs)

	v, err := f.svc.Disable(rest, f.server.ID, false)
	require.NoError(t, err)
	require.Equal(t, "removing", v.State)
	require.Equal(t, "removing", f.row().State)
	require.Len(t, f.disp.of(agentprotocol.KindRemoveTunnel), 1)

	// The approval was used up: the same call on a new tunnel asks again.
	require.NoError(t, f.svc.DeleteRemote(bg, f.server.ID))
	f.enable()
	_, err = f.svc.Disable(rest, f.server.ID, false)
	_, ok = prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
}

// A person in the browser is never asked.
func TestDisableOfProductionHostNeverAsksAPerson(t *testing.T) {
	f := newFixture(t)
	f.deployed("web", "app.example.com", true)
	f.enable()
	f.installGuard()

	v, err := f.svc.Disable(f.person(), f.server.ID, false)
	require.NoError(t, err)
	require.Equal(t, "removing", v.State)
}

// Only a server that hosts a live service of a production environment is
// guarded.
func TestDisableWithoutProductionServiceNeedsNoApproval(t *testing.T) {
	t.Run("no service", func(t *testing.T) {
		f := newFixture(t)
		f.enable()
		f.installGuard()
		v, err := f.svc.Disable(f.restToken(), f.server.ID, false)
		require.NoError(t, err)
		require.Equal(t, "removing", v.State)
	})
	t.Run("non-production environment", func(t *testing.T) {
		f := newFixture(t)
		f.deployed("web", "app.example.com", true)
		_, err := f.pool.Exec(bg, "UPDATE environments SET is_production = false WHERE id = $1", f.env.ID)
		require.NoError(t, err)
		f.enable()
		f.installGuard()
		v, err := f.svc.Disable(f.restToken(), f.server.ID, false)
		require.NoError(t, err)
		require.Equal(t, "removing", v.State)
	})
}

// The server NebulaCtrl runs on keeps its tunnel for everyone, and the refusal
// comes before any approval is asked.
func TestDisableOnControlPlaneIsRefusedBeforeApproval(t *testing.T) {
	f := newFixture(t)
	f.deployed("web", "app.example.com", true)
	f.enable()
	f.activate()
	f.installGuard()
	_, err := f.pool.Exec(bg, "UPDATE servers SET is_control_plane = true WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	state, syncs := f.row().State, f.cf.syncCount()

	for name, ctx := range map[string]context.Context{"token": f.restToken(), "admin": f.admin, "person": f.person()} {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Disable(ctx, f.server.ID, false)
			_, asked := prodguard.AsRequired(err)
			require.False(t, asked, "no approval is asked for a refused change: %v", err)
			ae, ok := apierr.As(err)
			require.True(t, ok, "not an API error: %v", err)
			require.Equal(t, http.StatusConflict, ae.Status)
			require.Equal(t, "server_is_control_plane", ae.Code)
			f.requireUntouched(state, syncs)
		})
	}
}
