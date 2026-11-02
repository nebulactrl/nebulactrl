package maintenance_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/maintenance"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// restToken is a REST token as authentication builds it: guarded unless exempt.
func (f *fixture) restToken() context.Context {
	p, ok := auth.PrincipalFrom(f.ctx)
	require.True(f.t, ok)
	tok := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: p.UserID, TokenID: &tok, WorkspaceID: p.WorkspaceID, Role: auth.RoleOwner,
		Label: "ci", Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
}

func (f *fixture) installGuard() *prodguard.Guard {
	f.t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte{3}, 32))
	require.NoError(f.t, err)
	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: box})
	g.Install()
	f.t.Cleanup(prodguardtest.Uninstall)
	return g
}

// Taking a production service offline is the guarded direction: a REST token
// needs a person's approval to switch maintenance on, never to switch it off.
func TestEnablingMaintenanceInProductionNeedsApprovalForRESTTokens(t *testing.T) {
	f := newFixture(t) // the fixture environment is production
	g := f.installGuard()
	sv := f.deployedWeb("web", "app.example.com")
	rest := f.restToken()
	in := maintenance.SetInput{Enabled: true, Title: "Upgrading", Message: "Back soon"}

	_, err := f.svc.Set(rest, sv.ID, in)
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Summary, "Put web in production into maintenance mode")
	require.Len(t, ar.Plan.Effects, 2)
	require.Empty(t, f.syncs(), "nothing is sent before the approval")
	require.Empty(t, f.auditActions())

	person := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: mustUser(t, f), WorkspaceID: policy.WorkspaceID(f.ctx), Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})
	_, aerr := g.Approve(person, ar.ApprovalID)
	require.NoError(t, aerr)

	v, err := f.svc.Set(rest, sv.ID, in)
	require.NoError(t, err)
	require.True(t, v.Maintenance.Enabled)
	require.Len(t, f.syncs(), 1)
	require.Equal(t, []string{"service.maintenance.on"}, f.auditActions())

	// A different text is a different call: the approval was for the first.
	_, err = f.svc.Set(rest, sv.ID, maintenance.SetInput{Enabled: true, Title: "Other"})
	_, asked := prodguard.AsRequired(err)
	require.True(t, asked, "got %v", err)

	// Bringing the service back is not guarded, and neither is a person in the browser.
	_, err = f.svc.Set(rest, sv.ID, maintenance.SetInput{Enabled: false})
	require.NoError(t, err)
	eta := time.Now().Add(time.Hour)
	_, err = f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: true, ETA: &eta})
	require.NoError(t, err)
}

func mustUser(t *testing.T, f *fixture) uuid.UUID {
	t.Helper()
	p, ok := auth.PrincipalFrom(f.ctx)
	require.True(t, ok)
	return p.UserID
}

func TestEnablingMaintenanceOutsideProductionIsNotGuarded(t *testing.T) {
	f := newFixture(t)
	f.installGuard()
	_, err := f.pool.Exec(context.Background(), "UPDATE environments SET is_production = false WHERE id = $1", f.env.ID)
	require.NoError(t, err)
	sv := f.deployedWeb("web", "app.example.com")
	_, err = f.svc.Set(f.restToken(), sv.ID, maintenance.SetInput{Enabled: true})
	require.NoError(t, err)
}
