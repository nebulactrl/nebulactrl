package reconciler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Every frame carries a table number higher than the one before; the hello flag
// is the hub's to set, not the reconciler's.
func TestDesiredStateNumbersItsTable(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()

	first, err := f.r.DesiredState(bg, f.server.ID)
	require.NoError(t, err)
	second, err := f.r.DesiredState(bg, f.server.ID)
	require.NoError(t, err)
	require.Positive(t, first.TableSeq)
	require.Greater(t, second.TableSeq, first.TableSeq)
	require.False(t, first.Hello)
}

// The count of a service is stamped with the time the control plane stored it,
// so the agent can tell a frame built before one of its scale commands from one
// built after: the stamp of the frame and of the command are the same value.
func TestDesiredStateStampsTheReplicaCount(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	app := f.deployed("web", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1", Port: 80})

	out, err := f.r.DesiredState(bg, f.server.ID)
	require.NoError(t, err)
	require.Len(t, out.Services, 1)
	require.True(t, out.Services[0].ScaledAt.IsZero(), "never scaled")

	row, err := db.New(f.pool).ApplyServiceScale(bg, db.ApplyServiceScaleParams{
		ID: app.ID, Replicas: 3, ExpectedReplicas: app.Replicas, LastScaleEvent: services.NewScaleEvent(1, 3, "manual", "test"),
	})
	require.NoError(t, err)
	require.NotNil(t, row.LastScaleAt)

	out, err = f.r.DesiredState(bg, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, 3, out.Services[0].Replicas)
	require.True(t, row.LastScaleAt.Equal(out.Services[0].ScaledAt), "%v vs %v", row.LastScaleAt, out.Services[0].ScaledAt)
}
