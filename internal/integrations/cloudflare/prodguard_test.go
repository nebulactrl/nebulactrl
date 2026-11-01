package cloudflare

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
)

// guardEnv is an environment whose service lives in a production environment,
// with a connected Cloudflare account and the production guard installed.
type guardEnv struct {
	*env
	g      *prodguard.Guard
	rest   context.Context // REST token as authentication builds it: guarded
	mcp    context.Context // MCP principal: guarded
	person context.Context // signed-in Owner who can approve
	user   context.Context // the browser session of the person that owns the tokens
}

func newGuardEnv(t *testing.T) *guardEnv {
	t.Helper()
	e := newEnv(t) // the fixture environment is production
	e.svc.SetRoutePusher(&pushRecorder{})
	e.connect(t)
	bg := context.Background()

	u, err := e.q.CreateUser(bg, db.CreateUserParams{WorkspaceID: e.ws, Email: "a@example.com", Name: "Ada", PasswordHash: "x"})
	require.NoError(t, err)

	g := prodguard.New(prodguard.Deps{Pool: e.pool, Box: e.box})
	g.Install()
	t.Cleanup(prodguard.Uninstall)

	tok, mcpTok := uuid.New(), uuid.New()
	return &guardEnv{
		env: e, g: g,
		rest: auth.WithPrincipal(bg, &auth.Principal{
			Type: auth.TypeToken, UserID: u.ID, TokenID: &tok, WorkspaceID: e.ws, Role: auth.RoleOwner,
			Label: "ci", Scopes: policy.AllScopes, RequireProductionApproval: true,
		}),
		mcp: auth.WithPrincipal(bg, &auth.Principal{
			Type: auth.TypeMCP, UserID: u.ID, TokenID: &mcpTok, WorkspaceID: e.ws, Role: auth.RoleOwner,
			Label: "agent", Scopes: policy.AllScopes, RequireProductionApproval: true,
		}),
		person: auth.WithPrincipal(bg, &auth.Principal{
			Type: auth.TypeUser, UserID: u.ID, WorkspaceID: e.ws, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
		}),
		user: auth.WithPrincipal(bg, &auth.Principal{
			Type: auth.TypeUser, UserID: u.ID, WorkspaceID: e.ws, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
		}),
	}
}

func (f *guardEnv) approve(t *testing.T, err error) *prodguard.ErrApprovalRequired {
	t.Helper()
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "want approval_required, got %v", err)
	_, aerr := f.g.Approve(f.person, ar.ApprovalID)
	require.NoError(t, aerr)
	return ar
}

func requireApprovalRequired(t *testing.T, err error) *prodguard.ErrApprovalRequired {
	t.Helper()
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "want approval_required, got %v", err)
	return ar
}

func requireNotApprovalRequired(t *testing.T, err error) {
	t.Helper()
	_, ok := prodguard.AsRequired(err)
	require.False(t, ok, "must not ask for approval, got %v", err)
}

// A REST token or an MCP agent cannot turn the proxy or the origin lock of a
// production domain off (or flip the proxy) without a person's approval; the
// setting is untouched until then, and the approval is good for one call.
func TestDomainDNSSettingsInProductionNeedApprovalForRESTTokensAndMCP(t *testing.T) {
	f := newGuardEnv(t)
	d := f.domain(t, "app.example.com", "")
	require.True(t, f.dom(t, d.ID).CfProxied)
	require.True(t, f.dom(t, d.ID).CfOriginLock)

	off := false

	for _, c := range []struct {
		name string
		ctx  context.Context
	}{{"rest token", f.rest}, {"mcp", f.mcp}} {
		t.Run(c.name, func(t *testing.T) {
			before := f.count(t, `SELECT count(*) FROM audit_events WHERE action = 'domain.dns.update'`)
			// The origin lock opt-out.
			_, err := f.svc.SetSettings(c.ctx, d.ID, SettingsInput{OriginLock: &off})
			ar := requireApprovalRequired(t, err)
			require.Contains(t, ar.Plan.Summary, "app.example.com")
			require.Contains(t, ar.Plan.Summary, "production")
			require.Contains(t, ar.Plan.Effects, "Origin lock: false.")
			require.True(t, f.dom(t, d.ID).CfOriginLock, "the lock stays on before the approval")
			require.Equal(t, before, f.count(t, `SELECT count(*) FROM audit_events WHERE action = 'domain.dns.update'`), "nothing is recorded before the approval")

			f.approve(t, err)
			_, err = f.svc.SetSettings(c.ctx, d.ID, SettingsInput{OriginLock: &off})
			requireNotApprovalRequired(t, err)
			require.NoError(t, err)
			require.False(t, f.dom(t, d.ID).CfOriginLock)

			// The approval was used up.
			_, err = f.svc.SetSettings(c.ctx, d.ID, SettingsInput{OriginLock: &off})
			requireApprovalRequired(t, err)

			// A different change is a different call: the approval does not carry over.
			on := true
			_, err = f.svc.SetSettings(c.ctx, d.ID, SettingsInput{Proxied: &off, OriginLock: &on})
			ar = requireApprovalRequired(t, err)
			require.Contains(t, ar.Plan.Effects, "Proxied: false.")
			require.True(t, f.dom(t, d.ID).CfProxied, "the proxy stays on before the approval")

			f.approve(t, err)
			_, err = f.svc.SetSettings(c.ctx, d.ID, SettingsInput{Proxied: &off, OriginLock: &on})
			requireNotApprovalRequired(t, err)
			require.NoError(t, err)
			got := f.dom(t, d.ID)
			require.False(t, got.CfProxied)
			require.True(t, got.CfOriginLock)

			// Put the domain back for the next principal (the person is never asked).
			_, err = f.svc.SetSettings(f.user, d.ID, SettingsInput{Proxied: &on})
			require.NoError(t, err)
			require.True(t, f.dom(t, d.ID).CfProxied)
		})
	}
}

// Any proxy setting is guarded, not only turning it off.
func TestDomainProxyChangeInProductionNeedsApproval(t *testing.T) {
	f := newGuardEnv(t)
	d := f.domain(t, "app.example.com", "")
	on, off := true, false

	// The domain starts proxied; a DNS-only person-made change first.
	_, err := f.svc.SetSettings(f.user, d.ID, SettingsInput{Proxied: &off})
	require.NoError(t, err)
	require.False(t, f.dom(t, d.ID).CfProxied)

	_, err = f.svc.SetSettings(f.rest, d.ID, SettingsInput{Proxied: &on})
	requireApprovalRequired(t, err)
	require.False(t, f.dom(t, d.ID).CfProxied, "still DNS only before the approval")
	f.approve(t, err)
	_, err = f.svc.SetSettings(f.rest, d.ID, SettingsInput{Proxied: &on})
	require.NoError(t, err)
	require.True(t, f.dom(t, d.ID).CfProxied)
}

// Opting into the origin lock only tightens the origin: it is not guarded.
func TestOriginLockOptInIsNotGuarded(t *testing.T) {
	f := newGuardEnv(t)
	d := f.domain(t, "app.example.com", "")
	on, off := true, false
	_, err := f.svc.SetSettings(f.user, d.ID, SettingsInput{OriginLock: &off})
	require.NoError(t, err)
	require.False(t, f.dom(t, d.ID).CfOriginLock)

	_, err = f.svc.SetSettings(f.rest, d.ID, SettingsInput{OriginLock: &on})
	require.NoError(t, err)
	require.True(t, f.dom(t, d.ID).CfOriginLock)

	// A call that changes nothing about the proxy or the lock is not guarded either.
	_, err = f.svc.SetSettings(f.rest, d.ID, SettingsInput{})
	require.NoError(t, err)
}

// A person in the browser is never asked.
func TestDomainDNSSettingsAreNotGuardedForPeople(t *testing.T) {
	f := newGuardEnv(t)
	d := f.domain(t, "app.example.com", "")
	on, off := true, false

	_, err := f.svc.SetSettings(f.user, d.ID, SettingsInput{OriginLock: &off})
	require.NoError(t, err)
	require.False(t, f.dom(t, d.ID).CfOriginLock)
	_, err = f.svc.SetSettings(f.user, d.ID, SettingsInput{Proxied: &off})
	require.NoError(t, err)
	require.False(t, f.dom(t, d.ID).CfProxied)
	_, err = f.svc.SetSettings(f.user, d.ID, SettingsInput{Proxied: &on, OriginLock: &on})
	require.NoError(t, err)

	// So is the fixture's own session (every scope).
	_, err = f.svc.SetSettings(f.admin, d.ID, SettingsInput{Proxied: &off})
	require.NoError(t, err)
	_, err = f.svc.Fix(f.user, d.ID, true)
	requireNotApprovalRequired(t, err)
}

// A name outside production is not guarded.
func TestDomainDNSSettingsOutsideProductionAreNotGuarded(t *testing.T) {
	f := newGuardEnv(t)
	f.fake.put("z1", Record{Type: "A", Name: "app.example.com", Content: "198.51.100.99"})
	d := f.domain(t, "app.example.com", "")
	f.exec(t, `UPDATE environments SET is_production = false`)
	on, off := true, false

	_, err := f.svc.SetSettings(f.rest, d.ID, SettingsInput{OriginLock: &off})
	require.NoError(t, err)
	require.False(t, f.dom(t, d.ID).CfOriginLock)
	_, err = f.svc.SetSettings(f.rest, d.ID, SettingsInput{Proxied: &off})
	require.NoError(t, err)
	require.False(t, f.dom(t, d.ID).CfProxied)
	_, err = f.svc.SetSettings(f.mcp, d.ID, SettingsInput{Proxied: &on})
	require.NoError(t, err)
	require.True(t, f.dom(t, d.ID).CfProxied)

	_, err = f.svc.Fix(f.rest, d.ID, true)
	requireNotApprovalRequired(t, err)
	require.NoError(t, err)
}

// Replacing records somebody else created is a production change: a REST token
// needs a person's approval, and the foreign record stays until then.
func TestAdoptingForeignDNSRecordsInProductionNeedsApproval(t *testing.T) {
	f := newGuardEnv(t)
	foreign := f.fake.put("z1", Record{Type: "A", Name: "app.example.com", Content: "198.51.100.99", Comment: "my own note"})
	d := f.domain(t, "app.example.com", "")
	require.NoError(t, f.svc.SyncDomain(context.Background(), d.ID))
	f.fake.resetWrites()

	_, err := f.svc.Fix(f.rest, d.ID, true)
	ar := requireApprovalRequired(t, err)
	require.Contains(t, ar.Plan.Summary, "app.example.com")
	require.Contains(t, ar.Plan.Summary, "production")
	require.Empty(t, f.fake.writeLog(), "nothing is written before the approval")
	got := f.fake.records("z1", "app.example.com")
	require.Len(t, got, 1)
	require.Equal(t, "198.51.100.99", got[0].Content)
	require.Equal(t, "my own note", got[0].Comment)

	f.approve(t, err)
	st, err := f.svc.Fix(f.rest, d.ID, true)
	require.NoError(t, err)
	require.Equal(t, StatusManaged, st.Status)
	got = f.fake.records("z1", "app.example.com")
	require.Len(t, got, 1)
	require.Equal(t, foreign.ID, got[0].ID)
	require.Equal(t, ipA, got[0].Content)

	// The approval was used up.
	_, err = f.svc.Fix(f.rest, d.ID, true)
	requireApprovalRequired(t, err)
}

// The MCP principal is asked as well.
func TestAdoptingForeignDNSRecordsIsGuardedForMCP(t *testing.T) {
	f := newGuardEnv(t)
	f.fake.put("z1", Record{Type: "A", Name: "app.example.com", Content: "198.51.100.99"})
	d := f.domain(t, "app.example.com", "")
	f.fake.resetWrites()

	_, err := f.svc.Fix(f.mcp, d.ID, true)
	requireApprovalRequired(t, err)
	require.Empty(t, f.fake.writeLog())
	f.approve(t, err)
	_, err = f.svc.Fix(f.mcp, d.ID, true)
	require.NoError(t, err)
	require.Equal(t, ipA, f.fake.records("z1", "app.example.com")[0].Content)
}

// Fix without adopt never replaces a record and is not guarded; a person adopting is never asked.
func TestFixWithoutAdoptIsNotGuardedAndPeopleAreNeverAsked(t *testing.T) {
	f := newGuardEnv(t)
	f.fake.put("z1", Record{Type: "A", Name: "app.example.com", Content: "198.51.100.99"})
	d := f.domain(t, "app.example.com", "")
	f.fake.resetWrites()

	// Without adopt the foreign record blocks the fix (409), not the guard.
	_, err := f.svc.Fix(f.rest, d.ID, false)
	requireNotApprovalRequired(t, err)
	require.Equal(t, "adoption_required", code(t, err))
	require.Empty(t, f.fake.writeLog())

	// A person in the browser is never asked.
	st, err := f.svc.Fix(f.user, d.ID, true)
	require.NoError(t, err)
	require.Equal(t, StatusManaged, st.Status)
	require.Equal(t, ipA, f.fake.records("z1", "app.example.com")[0].Content)
}

// The adopt guard comes after the zone-admin check: a token without the admin
// scope is refused outright, not asked.
func TestAdoptNeedsZoneAdminBeforeTheGuard(t *testing.T) {
	f := newGuardEnv(t)
	f.fake.put("z1", Record{Type: "A", Name: "app.example.com", Content: "198.51.100.99"})
	d := f.domain(t, "app.example.com", "")
	tok := uuid.New()
	p, _ := auth.PrincipalFrom(f.rest)
	dev := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: p.UserID, TokenID: &tok, WorkspaceID: f.ws, Role: auth.RoleOwner,
		Label: "dev", Scopes: []string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite}, RequireProductionApproval: true,
	})
	_, err := f.svc.Fix(dev, d.ID, true)
	requireNotApprovalRequired(t, err)
	require.Equal(t, "forbidden", code(t, err))
}
