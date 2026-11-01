package environments_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/environments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

// The guard is enforced in the domain service, so every entry point (REST, MCP,
// composites) is covered: an MCP principal needs an approval to delete a
// production environment or to remove its production flag, a person and a
// non-production environment do not.
func TestProductionGuardInEnvironmentService(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "Ada", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{1}, 32))
	require.NoError(t, err)
	g := prodguard.New(prodguard.Deps{Pool: pool, Box: box})
	g.Install()
	t.Cleanup(prodguardtest.Uninstall)
	svc := environments.NewService(pool, events.NewBus())

	tok := uuid.New()
	mcp := auth.WithPrincipal(bg, &auth.Principal{
		Type: auth.TypeMCP, UserID: u.ID, TokenID: &tok, WorkspaceID: ws.ID, Role: auth.RoleOwner, Label: "agent", Scopes: policy.AllScopes,
	})
	person := auth.WithPrincipal(bg, &auth.Principal{
		Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})
	mk := func(name string, prod bool) environments.Environment {
		e, err := svc.Create(person, prj.ID, environments.CreateInput{Name: name, IsProduction: prod})
		require.NoError(t, err)
		return e
	}

	// Non-production: no approval.
	stage := mk("staging", false)
	require.NoError(t, svc.Delete(mcp, stage.ID))

	// Production delete: approval required, and nothing is deleted.
	prod := mk("live", true)
	err = svc.Delete(mcp, prod.ID)
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Summary, "Delete the production environment live")
	require.True(t, ar.Plan.Irreversible)
	_, err = svc.Get(person, prod.ID)
	require.NoError(t, err)

	// Un-flagging production is guarded too (it would remove the guard itself)...
	off := false
	_, err = svc.Update(mcp, prod.ID, environments.UpdateInput{IsProduction: &off})
	_, ok = prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	// ...while renaming is not.
	name := "live2"
	_, err = svc.Update(mcp, prod.ID, environments.UpdateInput{Name: &name})
	require.NoError(t, err)
	// Flagging an environment as production adds protection and needs nothing.
	on := true
	stage2 := mk("qa", false)
	_, err = svc.Update(mcp, stage2.ID, environments.UpdateInput{IsProduction: &on})
	require.NoError(t, err)

	// A person approves the delete; the retry deletes, once.
	_, err = g.Approve(person, ar.ApprovalID)
	require.NoError(t, err)
	require.NoError(t, svc.Delete(mcp, prod.ID))
	_, err = svc.Get(person, prod.ID)
	require.Error(t, err)
}
