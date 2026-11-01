package auth_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
)

func TestEffectiveScopes(t *testing.T) {
	viewer := []string{"projects:read", "servers:read"}
	all := []string{"projects:read", "projects:write", "servers:read", "servers:write", "admin"}

	// A token never exceeds the role of its creator.
	require.Equal(t, []string{"projects:read"}, auth.EffectiveScopes([]string{"projects:read", "projects:write"}, viewer))
	// admin stands for every scope the role holds.
	require.ElementsMatch(t, viewer, auth.EffectiveScopes([]string{"admin"}, viewer))
	require.ElementsMatch(t, all, auth.EffectiveScopes([]string{"admin"}, all))
	// An admin role keeps the token's own scopes.
	require.Equal(t, []string{"projects:write"}, auth.EffectiveScopes([]string{"projects:write"}, all))
	// A creator with no role (removed from the team) leaves nothing.
	require.Empty(t, auth.EffectiveScopes([]string{"admin", "projects:read"}, nil))
	// The input is never aliased.
	role := []string{"projects:read"}
	got := auth.EffectiveScopes([]string{"admin"}, role)
	got[0] = "changed"
	require.Equal(t, "projects:read", role[0])
}

func TestEffectiveProjects(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	restricted := &auth.Membership{Role: auth.RoleDeveloper, ProjectIDs: []uuid.UUID{a, b}}
	open := &auth.Membership{Role: auth.RoleDeveloper}
	admin := &auth.Membership{Role: auth.RoleAdmin, ProjectIDs: []uuid.UUID{a}}

	require.Nil(t, auth.EffectiveProjects(nil, open))
	require.Equal(t, []uuid.UUID{a, b}, auth.EffectiveProjects(nil, restricted), "an unrestricted token inherits the member's restriction")
	require.Equal(t, []uuid.UUID{c}, auth.EffectiveProjects([]uuid.UUID{c}, open))
	require.Equal(t, []uuid.UUID{b}, auth.EffectiveProjects([]uuid.UUID{b, c}, restricted), "intersection")
	got := auth.EffectiveProjects([]uuid.UUID{c}, restricted)
	require.NotNil(t, got)
	require.Empty(t, got, "no overlap sees nothing, not everything")
	require.Nil(t, auth.EffectiveProjects(nil, admin), "owners and admins are never restricted")
}

func TestRoleRank(t *testing.T) {
	require.Greater(t, auth.RoleRank(auth.RoleOwner), auth.RoleRank(auth.RoleAdmin))
	require.Greater(t, auth.RoleRank(auth.RoleAdmin), auth.RoleRank(auth.RoleDeveloper))
	require.Greater(t, auth.RoleRank(auth.RoleDeveloper), auth.RoleRank(auth.RoleViewer))
	require.Zero(t, auth.RoleRank(""))
	require.True(t, auth.ValidRole("viewer"))
	require.False(t, auth.ValidRole("root"))
	require.Equal(t, "my-team", auth.TeamSlug("My  Team!"))
	require.Equal(t, "team", auth.TeamSlug("###"))
}
