package servers_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

// The server list and the single-server read carry the OS update summary: the
// pending counts, reboot-required and the run in flight.
func TestServersCarryThePatchSummary(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	fresh, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "never-checked"})
	require.NoError(t, err)
	checked, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "checked"})
	require.NoError(t, err)
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Scopes: policy.AllScopes})
	svc := servers.NewService(pool, events.NewBus(), nil, nil, nil)

	_, err = q.UpsertServerPatchState(bg, db.UpsertServerPatchStateParams{
		ServerID: checked.ID, WorkspaceID: ws.ID, Manager: "apt", Supported: true, Updates: 7, SecurityUpdates: 2,
		RebootRequired: true, RebootPackages: []string{"linux-base"}, BootID: "b1",
	})
	require.NoError(t, err)

	got, err := svc.Get(ctx, checked.ID)
	require.NoError(t, err)
	require.True(t, got.Patches.Known)
	require.True(t, got.Patches.Supported)
	require.Equal(t, 7, got.Patches.Updates)
	require.Equal(t, 2, got.Patches.Security)
	require.True(t, got.Patches.RebootRequired)
	require.NotNil(t, got.Patches.CheckedAt)
	require.Empty(t, got.Patches.Busy)

	// A run in flight shows even before the first check completed.
	_, err = pool.Exec(bg, `INSERT INTO server_patch_runs (server_id, workspace_id, action, trigger) VALUES ($1, $2, 'check', 'manual')`, fresh.ID, ws.ID)
	require.NoError(t, err)
	_, err = pool.Exec(bg, `INSERT INTO server_patch_runs (server_id, workspace_id, action, trigger, status) VALUES ($1, $2, 'reboot', 'manual', 'rebooting')`, checked.ID, ws.ID)
	require.NoError(t, err)

	list, _, err := svc.List(ctx, "", 50)
	require.NoError(t, err)
	byName := map[string]servers.ServerPatchSummary{}
	for _, s := range list {
		byName[s.Name] = s.Patches
	}
	require.False(t, byName["never-checked"].Known, "no check yet: the summary says so")
	require.Equal(t, "check", byName["never-checked"].Busy)
	require.Equal(t, 7, byName["checked"].Updates)
	require.Equal(t, "reboot", byName["checked"].Busy)
}
