package servers_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/servers"
)

// A server that never connected gets a new install command: the old token
// stops working, the new one is live, and once an agent has enrolled the
// server is refused.
func TestReissueEnrollmentToken(t *testing.T) {
	f, _ := newUnrestrictedFixture(t)
	bg := context.Background()

	first, err := f.svc.CreateEnrollmentToken(f.admin, servers.CreateTokenInput{Name: "worker"})
	require.NoError(t, err)
	require.Equal(t, "pending", first.Server.Status)

	second, err := f.svc.ReissueEnrollmentToken(f.admin, first.Server.ID)
	require.NoError(t, err)
	require.Equal(t, first.Server.ID, second.Server.ID)
	require.NotEqual(t, first.Token, second.Token)
	require.Contains(t, second.InstallCommand, second.Token)

	live := func(token string) bool {
		var n int
		err := f.pool.QueryRow(bg,
			"SELECT count(*) FROM enrollment_tokens WHERE token_hash = $1 AND revoked_at IS NULL AND used_at IS NULL",
			agents.HashEnrollmentToken(token)).Scan(&n)
		require.NoError(t, err)
		return n == 1
	}
	require.False(t, live(first.Token), "the first command was revoked")
	require.True(t, live(second.Token), "the new command is live")

	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", first.Server.ID)
	require.NoError(t, err)
	_, err = f.svc.ReissueEnrollmentToken(f.admin, first.Server.ID)
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, "server_not_pending", ae.Code)
}
