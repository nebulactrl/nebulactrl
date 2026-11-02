package jobs_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

// lockedBuffer is a log sink the River workers can write while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func startManager(t *testing.T, m *jobs.Manager) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, m.Migrate(ctx))
	require.NoError(t, m.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, m.Stop(stopCtx))
	})
}

func TestManagerWithoutPoolRefusesToBuildAClient(t *testing.T) {
	m := jobs.New(nil, nil)
	ctx := context.Background()

	require.Nil(t, m.Pool())
	require.NotNil(t, m.Logger(), "a nil logger falls back to the default one")

	require.ErrorContains(t, m.Start(ctx), "no database pool")
	require.ErrorContains(t, m.Insert(ctx, jobs.PruneIdempotencyArgs{}, nil), "no database pool")
	_, err := m.Client()
	require.ErrorContains(t, err, "no database pool")
	// Nothing was built, so stopping has nothing to wait for.
	require.NoError(t, m.Stop(ctx))
	// Nothing froze the worker set either.
	require.NotPanics(t, func() { m.AddWorker(func(*river.Workers) {}) })
}

func TestManagerFreezesTheWorkerSetOnceTheClientExists(t *testing.T) {
	pool := testutil.NewPool(t)
	m := jobs.New(pool, nil)
	require.Same(t, pool, m.Pool())

	c, err := m.Client()
	require.NoError(t, err)
	again, err := m.Client()
	require.NoError(t, err)
	require.Same(t, c, again, "the client is built once")

	require.PanicsWithValue(t, "jobs: AddWorker called after the River client was built", func() {
		m.AddWorker(func(*river.Workers) {})
	})
	require.PanicsWithValue(t, "jobs: AddPeriodic called after the River client was built", func() {
		m.AddPeriodic(nil)
	})
}

func TestInsertTxIsOnlyVisibleWhenTheTransactionCommits(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()
	m := jobs.New(pool, nil)
	require.NoError(t, m.Migrate(ctx))

	count := func() int {
		var n int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'prune_idempotency'`).Scan(&n))
		return n
	}

	rolledBack, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, m.InsertTx(ctx, rolledBack, jobs.PruneIdempotencyArgs{}, nil))
	require.NoError(t, rolledBack.Rollback(ctx))
	require.Zero(t, count(), "a rolled back transaction enqueues nothing")

	committed, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, m.InsertTx(ctx, committed, jobs.PruneIdempotencyArgs{}, nil))
	require.Equal(t, 0, count(), "the job is invisible to other connections until commit")
	require.NoError(t, committed.Commit(ctx))
	require.Equal(t, 1, count())
}

func TestInsertTxWithoutPoolFails(t *testing.T) {
	pool := testutil.NewPool(t)
	tx, err := pool.Begin(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	err = jobs.New(nil, nil).InsertTx(context.Background(), tx, jobs.PruneIdempotencyArgs{}, nil)
	require.ErrorContains(t, err, "no database pool")
}

func TestPruneIdempotencyJobLogsHowManyKeysItDeleted(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx,
		`INSERT INTO idempotency_keys (principal_id, key, method, path, status, created_at)
		 VALUES ('p', 'old', 'POST', '/x', 201, now() - interval '30 hours')`)
	require.NoError(t, err)

	logs := &lockedBuffer{}
	m := jobs.New(pool, slog.New(slog.NewTextHandler(logs, nil)))
	startManager(t, m)
	require.NoError(t, m.Insert(ctx, jobs.PruneIdempotencyArgs{}, nil))

	require.Eventually(t, func() bool {
		return strings.Contains(logs.String(), "pruned idempotency keys") && strings.Contains(logs.String(), "count=1")
	}, 15*time.Second, 50*time.Millisecond, logs.String())
}

func TestPruneIdempotencyJobFailureIsRecordedOnTheJob(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()
	// A table that cannot be queried makes the worker fail.
	_, err := pool.Exec(ctx, `DROP TABLE idempotency_keys`)
	require.NoError(t, err)

	m := jobs.New(pool, slog.New(slog.NewTextHandler(&lockedBuffer{}, nil)))
	startManager(t, m)
	require.NoError(t, m.Insert(ctx, jobs.PruneIdempotencyArgs{}, &river.InsertOpts{MaxAttempts: 1}))

	require.Eventually(t, func() bool {
		var n int
		err := pool.QueryRow(ctx,
			`SELECT count(*) FROM river_job WHERE kind = 'prune_idempotency' AND state = 'discarded' AND errors::text LIKE '%prune idempotency keys%'`).Scan(&n)
		return err == nil && n > 0
	}, 15*time.Second, 50*time.Millisecond)
}
