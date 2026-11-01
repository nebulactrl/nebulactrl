package servers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// guardFixture is the servers fixture with the production guard installed. The
// fixture's environment is made a production one.
type guardFixture struct {
	*unrestrictedFixture
	g *prodguard.Guard
}

func newGuardFixture(t *testing.T, production bool) *guardFixture {
	t.Helper()
	u, _ := newUnrestrictedFixture(t)
	_, err := u.pool.Exec(context.Background(), "UPDATE environments SET is_production = $2 WHERE id = $1", u.env, production)
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	g := prodguard.New(prodguard.Deps{Pool: u.pool, Box: box})
	g.Install()
	t.Cleanup(prodguardtest.Uninstall)
	return &guardFixture{unrestrictedFixture: u, g: g}
}

func (f *guardFixture) workspace() uuid.UUID { return f.server.WorkspaceID }

// restToken is a REST token as authentication builds it: guarded unless exempt.
func (f *guardFixture) restToken() context.Context {
	p, _ := auth.PrincipalFrom(f.admin)
	tok := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: p.UserID, TokenID: &tok, WorkspaceID: f.workspace(), Role: auth.RoleOwner,
		Label: "ci", Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
}

// person is a signed-in person (a browser session) who may approve.
func (f *guardFixture) person() context.Context {
	p, _ := auth.PrincipalFrom(f.admin)
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: p.UserID, WorkspaceID: f.workspace(), Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})
}

func (f *guardFixture) approve(t *testing.T, err error) {
	t.Helper()
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "want approval_required, got %v", err)
	_, aerr := f.g.Approve(f.person(), ar.ApprovalID)
	require.NoError(t, aerr)
}

func (f *guardFixture) status(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var s string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT status FROM servers WHERE id = $1", id).Scan(&s))
	return s
}

// A REST token cannot revoke a server that hosts a production service without a
// person's approval; the approval is single use.
func TestRevokeOfProductionHostNeedsApprovalForRESTTokens(t *testing.T) {
	f := newGuardFixture(t, true)
	f.addService(t, "app", false)
	rest := f.restToken()

	_, err := f.svc.Revoke(rest, f.server.ID)
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Summary, "edge")
	require.True(t, ar.Plan.Irreversible)
	require.NotEqual(t, "revoked", f.status(t, f.server.ID), "nothing is changed before the approval")

	f.approve(t, err)
	sv, err := f.svc.Revoke(rest, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, "revoked", sv.Status)
	require.Equal(t, "revoked", f.status(t, f.server.ID))

	// Revoking an already revoked server is a no-op and asks nothing.
	_, err = f.svc.Revoke(rest, f.server.ID)
	require.NoError(t, err)
}

// The approval is bound to the server: another server needs its own.
func TestRevokeApprovalDoesNotCarryToAnotherServer(t *testing.T) {
	f := newGuardFixture(t, true)
	f.addService(t, "app", false)
	rest := f.restToken()

	_, err := f.svc.Revoke(rest, f.server.ID)
	f.approve(t, err)

	other, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.workspace(), Name: "other"})
	require.NoError(t, err)
	name := uuid.NewString()[:8]
	_, err = f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env, Name: name, Slug: name, Kind: "app", SourceType: "image", Config: json.RawMessage(`{}`), ServerID: &other.ID,
	})
	require.NoError(t, err)
	_, err = f.svc.Revoke(rest, other.ID)
	_, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.NotEqual(t, "revoked", f.status(t, other.ID))
	require.NotEqual(t, "revoked", f.status(t, f.server.ID), "the approved server is untouched by the other call")
}

// A person in the browser is never asked.
func TestRevokeOfProductionHostNeverAsksAPerson(t *testing.T) {
	f := newGuardFixture(t, true)
	f.addService(t, "app", false)

	sv, err := f.svc.Revoke(f.person(), f.server.ID)
	require.NoError(t, err)
	require.Equal(t, "revoked", sv.Status)
}

// Only a server that hosts a live service of a production environment is
// guarded.
func TestRevokeWithoutProductionServiceNeedsNoApproval(t *testing.T) {
	t.Run("no service", func(t *testing.T) {
		f := newGuardFixture(t, true)
		sv, err := f.svc.Revoke(f.restToken(), f.server.ID)
		require.NoError(t, err)
		require.Equal(t, "revoked", sv.Status)
	})
	t.Run("deleted production service", func(t *testing.T) {
		f := newGuardFixture(t, true)
		f.addService(t, "app", true)
		sv, err := f.svc.Revoke(f.restToken(), f.server.ID)
		require.NoError(t, err)
		require.Equal(t, "revoked", sv.Status)
	})
	t.Run("non-production environment", func(t *testing.T) {
		f := newGuardFixture(t, false)
		f.addService(t, "app", false)
		sv, err := f.svc.Revoke(f.restToken(), f.server.ID)
		require.NoError(t, err)
		require.Equal(t, "revoked", sv.Status)
	})
}

// The server NebulaCtrl runs on cannot be revoked by anyone, and the refusal
// comes before any approval is asked.
func TestRevokeOfControlPlaneIsRefusedBeforeApproval(t *testing.T) {
	f := newGuardFixture(t, true)
	f.addService(t, "app", false)
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET is_control_plane = true WHERE id = $1", f.server.ID)
	require.NoError(t, err)

	for name, ctx := range map[string]context.Context{"token": f.restToken(), "admin": f.admin, "person": f.person()} {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Revoke(ctx, f.server.ID)
			_, asked := prodguard.AsRequired(err)
			require.False(t, asked, "no approval is asked for a refused change: %v", err)
			requireAPIError(t, err, http.StatusConflict, "server_is_control_plane")
			require.NotEqual(t, "revoked", f.status(t, f.server.ID))
		})
	}
}
