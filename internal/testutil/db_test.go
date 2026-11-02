package testutil

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenSchemaGivesIsolatedMigratedDatabases(t *testing.T) {
	a := NewPool(t)
	b := NewPool(t)
	ctx := context.Background()

	// Goose's and River's tables resolve unqualified in both.
	var n int
	require.NoError(t, a.QueryRow(ctx, `SELECT count(*) FROM goose_db_version`).Scan(&n))
	require.Positive(t, n)
	require.NoError(t, b.QueryRow(ctx, `SELECT count(*) FROM river_job`).Scan(&n))

	// What one writes, the other does not see.
	_, err := a.Exec(ctx, `CREATE TABLE only_in_a (id int)`)
	require.NoError(t, err)
	var reg *string
	require.NoError(t, b.QueryRow(ctx, `SELECT to_regclass('only_in_a')::text`).Scan(&reg))
	require.Nil(t, reg)

	var da, db string
	require.NoError(t, a.QueryRow(ctx, `SELECT current_database()`).Scan(&da))
	require.NoError(t, b.QueryRow(ctx, `SELECT current_database()`).Scan(&db))
	require.NotEqual(t, da, db)
}

func TestCloseDropsTheDatabaseAndNothingStaysOnTheTemplate(t *testing.T) {
	base := os.Getenv(EnvDatabaseURL)
	if base == "" {
		t.Skipf("%s not set; skipping database test", EnvDatabaseURL)
	}
	ctx := context.Background()
	pool, closeFn, err := OpenSchema(ctx)
	require.NoError(t, err)
	var name string
	require.NoError(t, pool.QueryRow(ctx, `SELECT current_database()`).Scan(&name))

	srv, err := connectServer(ctx, base)
	require.NoError(t, err)
	var conns int
	require.NoError(t, srv.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname LIKE 'nebula\_tmpl\_%'`).Scan(&conns))
	require.Zero(t, conns, "nothing stays connected to a template")

	closeFn()
	var exists bool
	require.NoError(t, srv.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists))
	require.False(t, exists)
}
