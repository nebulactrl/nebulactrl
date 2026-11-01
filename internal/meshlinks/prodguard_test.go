package meshlinks

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// restToken is a REST token as authentication builds it: guarded unless exempt.
// Approvals are bound to the token, so a test keeps one context per token.
func (f *fx) restToken() context.Context {
	p, _ := auth.PrincipalFrom(f.ctx)
	tok := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: p.UserID, TokenID: &tok, WorkspaceID: f.ws.ID, Role: auth.RoleOwner,
		Label: "ci", Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
}

func (f *fx) person() context.Context {
	p, _ := auth.PrincipalFrom(f.ctx)
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: p.UserID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})
}

func (f *fx) installGuard() *prodguard.Guard {
	f.t.Helper()
	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: f.box})
	g.Install()
	f.t.Cleanup(prodguard.Uninstall)
	return g
}

func (f *fx) approve(g *prodguard.Guard, err error) {
	f.t.Helper()
	ar, ok := prodguard.AsRequired(err)
	require.True(f.t, ok, "want approval_required, got %v", err)
	_, aerr := g.Approve(f.person(), ar.ApprovalID)
	require.NoError(f.t, aerr)
}

// A REST token cannot reconcile the mesh of a production environment without
// a person's approval; the browser session is not asked.
func TestReconcileNowInProductionNeedsApprovalForRESTTokens(t *testing.T) {
	f := newFx(t) // the fixture environment is production
	g := f.installGuard()
	f.service("postgres", services.KindApp, &f.b, 5432, nil)
	f.service("api", services.KindApp, &f.a, 8080, refVars)
	rest := f.restToken()

	_, err := f.s.ReconcileNow(rest, f.env.ID, ModeAdd, true)
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Summary, "Reconcile the mesh")
	require.Empty(t, f.grants(), "nothing is created before the approval")
	links, exposures := f.counts()
	require.Zero(t, links)
	require.Zero(t, exposures)

	// Another mode is another call: the approval below does not carry over to it.
	_, repairErr := f.s.ReconcileNow(rest, f.env.ID, ModeRepair, false)
	_, ok = prodguard.AsRequired(repairErr)
	require.True(t, ok, "got %v", repairErr)

	f.approve(g, err)
	v, err := f.s.ReconcileNow(rest, f.env.ID, ModeAdd, true)
	require.NoError(t, err)
	require.Equal(t, []GrantChange{{Consumer: "api", Provider: "postgres", Source: SourceReference}}, v.Added)
	require.Len(t, f.grants(), 1)

	// The approval was used up.
	_, err = f.s.ReconcileNow(rest, f.env.ID, ModeAdd, true)
	_, ok = prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)

	// A repair after its approval proceeds too.
	f.approve(g, repairErr)
	_, err = f.s.ReconcileNow(rest, f.env.ID, ModeRepair, false)
	require.NoError(t, err)

	// A person in the browser is never asked.
	_, err = f.s.ReconcileNow(f.ctx, f.env.ID, ModeAdd, true)
	require.NoError(t, err)
	_, err = f.s.ReconcileNow(f.ctx, f.env.ID, ModeRepair, false)
	require.NoError(t, err)
}

// An invalid mode is refused before the guard opens an approval.
func TestReconcileNowValidatesTheModeBeforeTheGuard(t *testing.T) {
	f := newFx(t)
	f.installGuard()

	_, err := f.s.ReconcileNow(f.restToken(), f.env.ID, Mode("nope"), false)
	_, asked := prodguard.AsRequired(err)
	require.False(t, asked)
	requireCode(t, err, "validation_failed")
}

// An environment that is not production is not guarded.
func TestReconcileNowOutsideProductionIsNotGuarded(t *testing.T) {
	f := newFx(t)
	f.installGuard()
	_, err := f.pool.Exec(context.Background(), "UPDATE environments SET is_production = false WHERE id = $1", f.env.ID)
	require.NoError(t, err)
	f.service("postgres", services.KindApp, &f.b, 5432, nil)
	f.service("api", services.KindApp, &f.a, 8080, refVars)

	v, err := f.s.ReconcileNow(f.restToken(), f.env.ID, ModeAdd, true)
	require.NoError(t, err)
	require.Len(t, v.Added, 1)
	require.Len(t, f.grants(), 1)
}
