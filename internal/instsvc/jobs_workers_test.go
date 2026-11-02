package instsvc

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"
)

func TestWorkersProvisionReadUsageAndPurge(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Provisioning every team's slices is one job; its outcome is stored per row.
	require.NoError(t, (&ensureWorker{svc: f.svc}).Work(ctx, &river.Job[EnsureArgs]{}))
	for _, tm := range []team{f.a, f.b} {
		require.NotEqual(t, uuid.Nil, f.slice(tm, KindS3).ID)
	}

	// One team on its own.
	var n int
	require.NoError(t, f.pool.QueryRow(ctx, "DELETE FROM team_service_slices WHERE team_id = $1 RETURNING 1", f.b.ID).Scan(&n))
	require.NoError(t, (&ensureTeamWorker{svc: f.svc}).Work(ctx, &river.Job[EnsureTeamArgs]{Args: EnsureTeamArgs{TeamID: f.b.ID}}))
	require.NotEqual(t, uuid.Nil, f.slice(f.b, KindS3).ID, "the team's slices are back")

	// Usage and purge never fail the job, even when they cannot read the
	// database: problems are logged and retried on the next run.
	f.pool.Close()
	require.NoError(t, (&usageWorker{svc: f.svc}).Work(ctx, &river.Job[UsageArgs]{}))
	require.NoError(t, (&purgeWorker{svc: f.svc}).Work(ctx, &river.Job[PurgeArgs]{}))
}
