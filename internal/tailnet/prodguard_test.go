package tailnet

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/tailnet/tailnetcfg"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// user is the fixture's user (the admin principal's).
func (f *fixture) user() uuid.UUID {
	p, _ := auth.PrincipalFrom(f.admin)
	return p.UserID
}

// restToken is a REST token as authentication builds it: guarded unless exempt.
// Approvals are bound to the token, so a test keeps one context per token.
func (f *fixture) restToken() context.Context {
	tok := uuid.New()
	return auth.WithPrincipal(bg, &auth.Principal{
		Type: auth.TypeToken, UserID: f.user(), TokenID: &tok, WorkspaceID: f.ws, Role: auth.RoleOwner,
		Label: "ci", Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
}

func (f *fixture) person() context.Context {
	return auth.WithPrincipal(bg, &auth.Principal{
		Type: auth.TypeUser, UserID: f.user(), WorkspaceID: f.ws, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})
}

func (f *fixture) installGuard() *prodguard.Guard {
	f.t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte{5}, 32))
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

// appliedService is a production service whose tailnet node was applied once,
// so a re-provision has something to work on.
func (f *fixture) appliedService() uuid.UUID {
	f.t.Helper()
	f.connect()
	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	sc := f.scope(sv.ID)
	spec, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(f.t, err)
	f.apply(*spec, online, f.server.ID)
	return sv.ID
}

// A REST token cannot re-create the tailnet device of a production service
// without a person's approval; the browser session is not asked.
func TestReprovisionInProductionNeedsApprovalForRESTTokens(t *testing.T) {
	f := newFixture(t) // the fixture environment is production
	g := f.installGuard()
	id := f.appliedService()
	rest := f.restToken()

	// The guard comes before the checks of the node itself: even a service
	// without a node is asked about first.
	bare := f.service("bare", nil)
	_, err := f.svc.Reprovision(rest, bare.ID)
	_, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)

	_, err = f.svc.Reprovision(rest, id)
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Summary, "Re-create the tailnet device")
	require.Empty(t, f.disp.kinds(), "nothing is sent before the approval")
	require.False(t, f.row(id).NeedsReprovision)

	f.approve(g, err)
	res, err := f.svc.Reprovision(rest, id)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, res.CommandID)
	require.Equal(t, []string{agentprotocol.KindSyncTailnet}, f.disp.kinds())
	require.True(t, f.row(id).NeedsReprovision)
	require.Equal(t, services.TailnetProvisioning, f.row(id).State)

	// The approval was used up.
	_, err = f.svc.Reprovision(rest, id)
	_, ok = prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)

	// A person in the browser is never asked.
	_, err = f.svc.Reprovision(f.admin, id)
	require.NoError(t, err)
}

// With the approval the call goes past the guard to the checks of the node.
func TestReprovisionApprovalLeadsToTheNodeChecks(t *testing.T) {
	f := newFixture(t)
	g := f.installGuard()
	f.connect()
	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true}) // never deployed: no node
	rest := f.restToken()

	_, err := f.svc.Reprovision(rest, sv.ID)
	f.approve(g, err)
	_, err = f.svc.Reprovision(rest, sv.ID)
	_, asked := prodguard.AsRequired(err)
	require.False(t, asked, "the approval was given")
	require.Equal(t, "not_applicable", apiCode(t, err))
}

// An environment that is not production is not guarded.
func TestReprovisionOutsideProductionIsNotGuarded(t *testing.T) {
	f := newFixture(t)
	f.installGuard()
	id := f.appliedService()
	_, err := f.pool.Exec(bg, "UPDATE environments SET is_production = false WHERE id = $1", f.env.ID)
	require.NoError(t, err)

	_, err = f.svc.Reprovision(f.restToken(), id)
	require.NoError(t, err)
	require.Equal(t, []string{agentprotocol.KindSyncTailnet}, f.disp.kinds())
}
