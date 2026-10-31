package policy_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

func ctxWith(p *auth.Principal) context.Context {
	return auth.WithPrincipal(context.Background(), p)
}

func statusOf(t *testing.T, err error) int {
	t.Helper()
	require.Error(t, err)
	e, ok := apierr.As(err)
	require.True(t, ok)
	return e.Status
}

func TestRequire(t *testing.T) {
	a, b := uuid.New(), uuid.New()

	require.Equal(t, http.StatusUnauthorized, statusOf(t, policy.Require(context.Background(), policy.ScopeProjectsRead, nil)))

	reader := &auth.Principal{Type: auth.TypeToken, Scopes: []string{policy.ScopeProjectsRead}, ProjectIDs: []uuid.UUID{a}}
	require.NoError(t, policy.Require(ctxWith(reader), policy.ScopeProjectsRead, nil))
	require.NoError(t, policy.Require(ctxWith(reader), policy.ScopeProjectsRead, &a))
	require.Equal(t, http.StatusForbidden, statusOf(t, policy.Require(ctxWith(reader), policy.ScopeProjectsWrite, &a)))
	require.Equal(t, http.StatusNotFound, statusOf(t, policy.Require(ctxWith(reader), policy.ScopeProjectsRead, &b)))
	// Scope is checked before project visibility.
	require.Equal(t, http.StatusForbidden, statusOf(t, policy.Require(ctxWith(reader), policy.ScopeProjectsWrite, &b)))

	admin := &auth.Principal{Type: auth.TypeToken, Scopes: []string{policy.ScopeAdmin}}
	require.NoError(t, policy.Require(ctxWith(admin), policy.ScopeDeploymentsRollback, &b))

	// Admin does not lift a project restriction.
	restrictedAdmin := &auth.Principal{Type: auth.TypeToken, Scopes: []string{policy.ScopeAdmin}, ProjectIDs: []uuid.UUID{a}}
	require.Equal(t, http.StatusNotFound, statusOf(t, policy.Require(ctxWith(restrictedAdmin), policy.ScopeProjectsRead, &b)))

	// Empty (non-nil) restriction sees nothing.
	none := &auth.Principal{Type: auth.TypeToken, Scopes: []string{policy.ScopeProjectsRead}, ProjectIDs: []uuid.UUID{}}
	require.Equal(t, http.StatusNotFound, statusOf(t, policy.Require(ctxWith(none), policy.ScopeProjectsRead, &a)))
}

func TestWorkspaceID(t *testing.T) {
	ws := uuid.New()
	require.Equal(t, ws, policy.WorkspaceID(ctxWith(&auth.Principal{WorkspaceID: ws})))
	require.Equal(t, uuid.Nil, policy.WorkspaceID(context.Background()))
}

func TestValidScope(t *testing.T) {
	for _, s := range policy.AllScopes {
		require.True(t, policy.ValidScope(s))
	}
	require.False(t, policy.ValidScope("root"))
}

func TestScopesForRole(t *testing.T) {
	require.ElementsMatch(t, policy.AllScopes, policy.ScopesForRole(auth.RoleOwner))
	require.ElementsMatch(t, policy.AllScopes, policy.ScopesForRole(auth.RoleAdmin))

	dev := policy.ScopesForRole(auth.RoleDeveloper)
	require.ElementsMatch(t, []string{
		policy.ScopeProjectsRead, policy.ScopeLogsRead, policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite,
		policy.ScopeDeploymentsRollback, policy.ScopeServersRead,
	}, dev)
	for _, withheld := range []string{policy.ScopeAdmin, policy.ScopeServersWrite, policy.ScopeProxyWrite} {
		require.NotContains(t, dev, withheld)
	}

	require.ElementsMatch(t, []string{policy.ScopeProjectsRead, policy.ScopeLogsRead, policy.ScopeServersRead}, policy.ScopesForRole(auth.RoleViewer))
	require.Empty(t, policy.ScopesForRole(""))
	require.Empty(t, policy.ScopesForRole("superuser"))

	// The result is a copy: callers cannot corrupt the table.
	policy.ScopesForRole(auth.RoleOwner)[0] = "tampered"
	require.Equal(t, policy.ScopeProjectsRead, policy.AllScopes[0])
}

func TestRequireRole(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	require.Equal(t, http.StatusUnauthorized, statusOf(t, policy.RequireRole(context.Background(), nil, auth.RoleAdmin)))

	viewer := &auth.Principal{Type: auth.TypeUser, Role: auth.RoleViewer}
	dev := &auth.Principal{Type: auth.TypeUser, Role: auth.RoleDeveloper, ProjectIDs: []uuid.UUID{a}}
	system := &auth.Principal{Type: auth.TypeSystem}

	require.NoError(t, policy.RequireRole(ctxWith(dev), nil, auth.RoleAdmin, auth.RoleDeveloper))
	require.NoError(t, policy.RequireRole(ctxWith(dev), &a, auth.RoleDeveloper))
	require.Equal(t, http.StatusNotFound, statusOf(t, policy.RequireRole(ctxWith(dev), &b, auth.RoleDeveloper)))
	require.Equal(t, http.StatusForbidden, statusOf(t, policy.RequireRole(ctxWith(viewer), nil, auth.RoleAdmin, auth.RoleDeveloper)))
	// Role is checked before project visibility.
	require.Equal(t, http.StatusForbidden, statusOf(t, policy.RequireRole(ctxWith(viewer), &b, auth.RoleAdmin)))
	// The system principal has no role.
	require.Equal(t, http.StatusForbidden, statusOf(t, policy.RequireRole(ctxWith(system), nil, auth.RoleOwner, auth.RoleAdmin)))
}

func TestRoleAtLeast(t *testing.T) {
	owner := &auth.Principal{Role: auth.RoleOwner}
	dev := &auth.Principal{Role: auth.RoleDeveloper}
	none := &auth.Principal{}
	require.True(t, policy.RoleAtLeast(owner, auth.RoleAdmin))
	require.True(t, policy.RoleAtLeast(dev, auth.RoleDeveloper))
	require.False(t, policy.RoleAtLeast(dev, auth.RoleAdmin))
	require.False(t, policy.RoleAtLeast(none, auth.RoleViewer))
	require.False(t, policy.RoleAtLeast(owner, "bogus"))
}
