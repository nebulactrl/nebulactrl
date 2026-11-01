// Package testutil holds helpers shared by tests across packages.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// EnvDatabaseURL names the variable pointing at a Postgres server that tests
// may create schemas in.
const EnvDatabaseURL = "NEBULACTRL_TEST_DATABASE_URL"

// NewPool returns a pool bound to a fresh, fully migrated schema that is
// dropped when the test ends. The test is skipped when NEBULACTRL_TEST_DATABASE_URL
// is unset. Every connection of the pool has search_path set to the new schema,
// so unqualified table names (including scs's `sessions` and River's tables,
// whose migrations also run here)
// resolve inside it.
func NewPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	if os.Getenv(EnvDatabaseURL) == "" {
		t.Skipf("%s not set; skipping database test", EnvDatabaseURL)
	}
	pool, closeFn, err := OpenSchema(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	return pool
}

// OpenSchema is NewPool without a *testing.T, for harnesses that share one
// schema across a whole test binary (TestMain). The returned func closes the
// pool and drops the schema. NEBULACTRL_TEST_DATABASE_URL must be set.
func OpenSchema(ctx context.Context) (*pgxpool.Pool, func(), error) {
	base := os.Getenv(EnvDatabaseURL)
	if base == "" {
		return nil, nil, fmt.Errorf("%s not set", EnvDatabaseURL)
	}
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		return nil, nil, fmt.Errorf("connect admin pool: %w", err)
	}
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		admin.Close()
		return nil, nil, err
	}
	schema := "t_" + hex.EncodeToString(rnd[:])
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		return nil, nil, fmt.Errorf("create schema: %w", err)
	}

	dsn, err := withSearchPath(base, schema)
	if err != nil {
		admin.Close()
		return nil, nil, err
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		admin.Close()
		return nil, nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		admin.Close()
		return nil, nil, fmt.Errorf("connect test pool: %w", err)
	}
	closeFn := func() {
		pool.Close()
		dctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Exec(dctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	}
	if err := db.Migrate(ctx, pool, nil); err != nil {
		closeFn()
		return nil, nil, fmt.Errorf("migrate: %w", err)
	}
	if err := jobs.New(pool, nil).Migrate(ctx); err != nil {
		closeFn()
		return nil, nil, fmt.Errorf("river migrate: %w", err)
	}
	return pool, closeFn, nil
}

func withSearchPath(base, schema string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", EnvDatabaseURL, err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
