package tokens_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/internal/tokens"
)

func session(ws, user uuid.UUID, role string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: user, WorkspaceID: ws, Role: role, Label: "Ada", Scopes: policy.ScopesForRole(role),
	})
}

func TestProductionGuardKnob(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	// The instance admin is the earliest user; u below is not.
	_, err = q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "first@example.com", Name: "First", PasswordHash: "x"})
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "Ada", PasswordHash: "x"})
	require.NoError(t, err)
	_, err = q.InsertMembership(bg, db.InsertMembershipParams{UserID: u.ID, WorkspaceID: ws.ID, Role: auth.RoleOwner})
	if err != nil {
		// CreateUser may already have made the owner membership.
		_, err = pool.Exec(bg, "UPDATE memberships SET role = 'owner' WHERE user_id = $1 AND workspace_id = $2", u.ID, ws.ID)
		require.NoError(t, err)
	}
	svc := tokens.NewService(pool, nil)
	a := auth.NewService(pool, policy.ScopesForRole)
	scopes := []string{policy.ScopeProjectsRead, policy.ScopeDeploymentsWrite}

	// Every token needs production approvals by default, whatever its kind.
	for _, kind := range []string{"", auth.TokenKindREST, auth.TokenKindMCP} {
		def, raw, err := svc.Create(session(ws.ID, u.ID, auth.RoleOwner), tokens.CreateInput{Name: "default", Kind: kind, Scopes: scopes})
		require.NoError(t, err)
		require.False(t, def.AllowProductionWithoutApproval)
		p, err := a.AuthenticateBearer(bg, raw)
		require.NoError(t, err)
		require.True(t, prodguard.Guarded(p), "a %q token is guarded by default", kind)
		mcp := *p
		mcp.Type = auth.TypeMCP
		require.True(t, prodguard.Guarded(&mcp))
	}
	_, _, err = svc.Create(session(ws.ID, u.ID, auth.RoleOwner), tokens.CreateInput{Name: "bad", Kind: "cli", Scopes: scopes})
	require.Error(t, err)

	// The kind is stored and reaches the principal.
	mcpTok, mcpRaw, err := svc.Create(session(ws.ID, u.ID, auth.RoleOwner), tokens.CreateInput{Name: "agent", Kind: auth.TokenKindMCP, Scopes: scopes})
	require.NoError(t, err)
	require.Equal(t, auth.TokenKindMCP, mcpTok.Kind)
	p, err := a.AuthenticateBearer(bg, mcpRaw)
	require.NoError(t, err)
	require.Equal(t, auth.TokenKindMCP, p.TokenKind)

	// A developer cannot switch the guard off for a token.
	_, _, err = svc.Create(session(ws.ID, u.ID, auth.RoleDeveloper), tokens.CreateInput{Name: "dev", Scopes: scopes, AllowProductionWithoutApproval: true})
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, ae.Status)
	require.Contains(t, ae.Message, "instance admin")

	// A team admin can; it is audited twice (the create and the switch-off).
	tok, raw, err := svc.Create(session(ws.ID, u.ID, auth.RoleAdmin), tokens.CreateInput{Name: "trusted", Scopes: scopes, AllowProductionWithoutApproval: true})
	require.NoError(t, err)
	require.True(t, tok.AllowProductionWithoutApproval)

	var createMeta, offMeta string
	require.NoError(t, pool.QueryRow(bg, `SELECT metadata::text FROM audit_events WHERE workspace_id = $1 AND action = 'token.create' AND resource_id = $2`, ws.ID, tok.ID.String()).Scan(&createMeta))
	require.Contains(t, createMeta, `"allowProductionWithoutApproval": true`)
	require.NoError(t, pool.QueryRow(bg, `SELECT metadata::text FROM audit_events WHERE workspace_id = $1 AND action = 'token.production_guard.disabled' AND resource_id = $2`, ws.ID, tok.ID.String()).Scan(&offMeta))
	require.Contains(t, offMeta, "trusted")

	// A token can never mint a token without approvals, even an exempt
	// admin-scope token, and never one that outlives it.
	exemptAdmin := auth.WithPrincipal(bg, &auth.Principal{
		Type: auth.TypeToken, UserID: u.ID, TokenID: &tok.ID, WorkspaceID: ws.ID, Role: auth.RoleOwner, Label: "trusted",
		Scopes: policy.AllScopes, AllowProductionWithoutApproval: true,
	})
	_, _, err = svc.Create(exemptAdmin, tokens.CreateInput{Name: "child", Scopes: scopes, AllowProductionWithoutApproval: true})
	ae, ok = apierr.As(err)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, ae.Status)
	child, _, err := svc.Create(exemptAdmin, tokens.CreateInput{Name: "child", Scopes: scopes})
	require.NoError(t, err)
	require.False(t, child.AllowProductionWithoutApproval)

	soon := time.Now().Add(time.Hour)
	expiring := auth.WithPrincipal(bg, &auth.Principal{
		Type: auth.TypeToken, UserID: u.ID, TokenID: &tok.ID, WorkspaceID: ws.ID, Role: auth.RoleOwner, Label: "short",
		Scopes: policy.AllScopes, TokenExpiresAt: &soon, RequireProductionApproval: true,
	})
	_, _, err = svc.Create(expiring, tokens.CreateInput{Name: "forever", Scopes: scopes})
	require.Error(t, err, "a token without expiry would outlive its creator")
	later := soon.Add(time.Hour)
	_, _, err = svc.Create(expiring, tokens.CreateInput{Name: "later", Scopes: scopes, ExpiresAt: &later})
	require.Error(t, err)
	within := soon.Add(-time.Minute)
	_, _, err = svc.Create(expiring, tokens.CreateInput{Name: "within", Scopes: scopes, ExpiresAt: &within})
	require.NoError(t, err)

	// The exemption is honoured while its creator is Owner/Admin, and lapses on demotion.
	p, err = a.AuthenticateBearer(bg, raw)
	require.NoError(t, err)
	require.True(t, p.AllowProductionWithoutApproval)
	mcp := *p
	mcp.Type = auth.TypeMCP
	require.False(t, prodguard.Guarded(&mcp), "an exempted token needs no approval")
	require.False(t, prodguard.Guarded(p))

	_, err = pool.Exec(bg, "UPDATE memberships SET role = 'developer' WHERE user_id = $1 AND workspace_id = $2", u.ID, ws.ID)
	require.NoError(t, err)
	p, err = a.AuthenticateBearer(bg, raw)
	require.NoError(t, err)
	require.False(t, p.AllowProductionWithoutApproval, "a demoted creator's token is guarded again")
	require.True(t, prodguard.Guarded(p))
	mcp = *p
	mcp.Type = auth.TypeMCP
	require.True(t, prodguard.Guarded(&mcp))
}

// The instance admin may create an exempt token even when only a developer
// in the team they are signed in to.
func TestInstanceAdminMayExemptAToken(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	first, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "first@example.com", Name: "First", PasswordHash: "x"})
	require.NoError(t, err)
	svc := tokens.NewService(pool, nil)
	scopes := []string{policy.ScopeProjectsRead}
	tok, _, err := svc.Create(session(ws.ID, first.ID, auth.RoleDeveloper), tokens.CreateInput{Name: "t", Scopes: scopes, AllowProductionWithoutApproval: true})
	require.NoError(t, err)
	require.True(t, tok.AllowProductionWithoutApproval)

	second, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "second@example.com", Name: "Second", PasswordHash: "x"})
	require.NoError(t, err)
	_, _, err = svc.Create(session(ws.ID, second.ID, auth.RoleDeveloper), tokens.CreateInput{Name: "t", Scopes: scopes, AllowProductionWithoutApproval: true})
	require.Error(t, err)
}
