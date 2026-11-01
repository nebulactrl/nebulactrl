package environments_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/environments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

// The dashboard serves /p/{project}/settings, so no environment may take that slug.
func TestEnvironmentSlugSettingsIsReserved(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "Ada", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	svc := environments.NewService(pool, events.NewBus())
	person := auth.WithPrincipal(bg, &auth.Principal{
		Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})

	for _, name := range []string{"settings", "Settings", "  SETTINGS! "} {
		_, err := svc.Create(person, prj.ID, environments.CreateInput{Name: name})
		ae, ok := apierr.As(err)
		require.True(t, ok, "%q: %v", name, err)
		require.Equal(t, "slug_reserved", ae.Code, name)
		require.Equal(t, 422, ae.Status, name)
	}
	envs, err := q.ListEnvironments(bg, prj.ID)
	require.NoError(t, err)
	require.Empty(t, envs)

	// Other names are fine, "Settings 2" included.
	e, err := svc.Create(person, prj.ID, environments.CreateInput{Name: "Settings 2"})
	require.NoError(t, err)
	require.Equal(t, "settings-2", e.Slug)

	// A rename never changes the slug, so it cannot take the reserved one.
	name := "Settings"
	got, err := svc.Update(person, e.ID, environments.UpdateInput{Name: &name})
	require.NoError(t, err)
	require.Equal(t, "settings-2", got.Slug)
}
