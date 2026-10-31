package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

func TestPruneIdempotencyJobDeletesOldKeys(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()

	for key, age := range map[string]string{"old": "25 hours", "fresh": "1 hour"} {
		_, err := pool.Exec(ctx,
			`INSERT INTO idempotency_keys (principal_id, key, method, path, status, created_at)
			 VALUES ('p', $1, 'POST', '/x', 201, now() - $2::interval)`, key, age)
		require.NoError(t, err)
	}

	m := jobs.New(pool, nil)
	require.NoError(t, m.Migrate(ctx))
	require.NoError(t, m.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, m.Stop(stopCtx))
	})
	require.NoError(t, m.Insert(ctx, jobs.PruneIdempotencyArgs{}, nil))

	require.Eventually(t, func() bool {
		var n int
		require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM idempotency_keys").Scan(&n))
		return n == 1
	}, 15*time.Second, 100*time.Millisecond)

	var left string
	require.NoError(t, pool.QueryRow(ctx, "SELECT key FROM idempotency_keys").Scan(&left))
	require.Equal(t, "fresh", left)
}
