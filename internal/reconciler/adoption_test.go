package reconciler

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A server held for adoption tells its agent to leave every unknown workload
// alone, and the services left running unmanaged are listed in every frame.
func TestDesiredStateCarriesTheAdoptionHoldAndUnmanagedServices(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()

	plain, err := f.r.DesiredState(bg, f.server.ID)
	require.NoError(t, err)
	require.False(t, plain.KeepUnknown)
	require.Empty(t, plain.Unmanaged)

	f.exec(`UPDATE servers SET adoption_state = 'held' WHERE id = $1`, f.server.ID)
	left := uuid.New()
	f.exec(`INSERT INTO server_unmanaged_services (server_id, service_id) VALUES ($1, $2)`, f.server.ID, left)

	held, err := f.r.DesiredState(bg, f.server.ID)
	require.NoError(t, err)
	require.True(t, held.KeepUnknown)
	require.Equal(t, []string{left.String()}, held.Unmanaged)

	f.exec(`UPDATE servers SET adoption_state = '' WHERE id = $1`, f.server.ID)
	released, err := f.r.DesiredState(bg, f.server.ID)
	require.NoError(t, err)
	require.False(t, released.KeepUnknown)
	require.Equal(t, []string{left.String()}, released.Unmanaged, "unmanaged outlives the hold")
}
