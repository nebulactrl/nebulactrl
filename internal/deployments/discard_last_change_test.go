package deployments

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Discarding the last staged change leaves the service identical to its
// release. It must stop reporting staged changes (no ghost "Deploy 1" with an
// empty review).
func TestDiscardingTheLastChangeClearsTheStagedFlag(t *testing.T) {
	f := newBuildFixture(t)
	web := f.web("web")
	staged := f.stageGit(web)
	require.True(t, staged.HasStagedChanges)

	v, err := f.deps.DiscardPendingChange(f.ctx, web.ID, services.FieldSource)
	require.NoError(t, err)
	require.False(t, v.HasStagedChanges, "no change is left")
	require.Nil(t, v.StagedKind)
	require.Zero(t, v.PendingChangeCounts)

	got, err := f.svcs.Get(f.ctx, web.ID)
	require.NoError(t, err)
	require.False(t, got.HasStagedChanges)
}
