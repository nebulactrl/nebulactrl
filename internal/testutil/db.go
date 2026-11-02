// Package testutil holds helpers shared by tests across packages.
package testutil

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/nebulactrl/nebulactrl/db/migrations"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// EnvDatabaseURL names the variable pointing at a Postgres server that tests
// may create databases in. The role needs CREATEDB.
const EnvDatabaseURL = "NEBULACTRL_TEST_DATABASE_URL"

const (
	// templatePrefix starts the name of every template database. A template is
	// named after a hash of everything that shapes the schema, so a changed
	// migration builds a new one and the old ones are dropped.
	templatePrefix = "nebula_tmpl_"
	// templateLockKey is the advisory lock (taken on the database the URL names)
	// that makes one process at a time build a template.
	templateLockKey int64 = 0x6e62_7574_706c_0001
	// schemaFormat is bumped when this file changes what a template holds.
	schemaFormat = "1"
)

// NewPool returns a pool bound to a fresh, fully migrated database that is
// dropped when the test ends. The test is skipped when NEBULACTRL_TEST_DATABASE_URL
// is unset. The database is a copy of a template that holds the goose
// migrations and River's (migrated once per schema version, see OpenSchema), in
// the public schema, so unqualified table names (including scs's `sessions` and
// River's tables) resolve.
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
// database across a whole test binary (TestMain). The returned func closes the
// pool and drops the database. NEBULACTRL_TEST_DATABASE_URL must be set.
//
// The database is `CREATE DATABASE t_<hex> TEMPLATE <template>`. The template
// (nebula_tmpl_<hash>) is migrated once per server: the first caller builds it
// under an advisory lock, concurrent test binaries wait for it, and it only
// exists under its final name once it is complete. Nothing stays connected to it.
func OpenSchema(ctx context.Context) (*pgxpool.Pool, func(), error) {
	base := os.Getenv(EnvDatabaseURL)
	if base == "" {
		return nil, nil, fmt.Errorf("%s not set", EnvDatabaseURL)
	}
	srv, err := connectServer(ctx, base)
	if err != nil {
		return nil, nil, err
	}
	admin := srv.pool
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, nil, err
	}
	name := "t_" + hex.EncodeToString(rnd[:])

	// Another process may have replaced the template (a different schema
	// version drops the old one): build again and retry once.
	for attempt := 0; ; attempt++ {
		tmpl, err := template(ctx, admin, base)
		if err != nil {
			return nil, nil, err
		}
		_, err = admin.Exec(ctx, "CREATE DATABASE "+name+" TEMPLATE "+tmpl+srv.strategy)
		if err == nil {
			break
		}
		var pgErr *pgconn.PgError
		if attempt == 0 && errors.As(err, &pgErr) && pgErr.Code == "3D000" { // invalid_catalog_name
			forgetTemplate(base)
			continue
		}
		return nil, nil, fmt.Errorf("create database %s from %s: %w", name, tmpl, err)
	}
	drop := func() {
		dctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = admin.Exec(dctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	}

	dsn, err := withDatabase(base, name)
	if err != nil {
		drop()
		return nil, nil, err
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		drop()
		return nil, nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		drop()
		return nil, nil, fmt.Errorf("connect test pool: %w", err)
	}
	return pool, func() {
		pool.Close()
		drop()
	}, nil
}

// server is the process-wide admin connection to one Postgres server.
type server struct {
	pool *pgxpool.Pool
	// strategy is appended to CREATE DATABASE. FILE_COPY (Postgres 15+) copies
	// the small template's files directly: about half the time of the default
	// WAL_LOG, and it holds up under many parallel clones.
	strategy string
}

var (
	adminMu    sync.Mutex
	admins     = map[string]*server{}
	templateMu sync.Mutex
	templates  = map[string]string{}
)

// connectServer returns the process-wide admin pool on the database the URL
// names, used to create and drop databases. It lives until the test binary exits.
func connectServer(ctx context.Context, base string) (*server, error) {
	adminMu.Lock()
	defer adminMu.Unlock()
	if p, ok := admins[base]; ok {
		return p, nil
	}
	cfg, err := pgxpool.ParseConfig(base)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", EnvDatabaseURL, err)
	}
	cfg.MaxConns = 8
	cfg.MaxConnIdleTime = 15 * time.Second
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect admin pool: %w", err)
	}
	var version int
	if err := p.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		p.Close()
		return nil, fmt.Errorf("read server version: %w", err)
	}
	srv := &server{pool: p}
	if version >= 150000 {
		srv.strategy = " STRATEGY = FILE_COPY"
	}
	admins[base] = srv
	return srv, nil
}

func forgetTemplate(base string) {
	templateMu.Lock()
	defer templateMu.Unlock()
	delete(templates, base)
}

// template returns the name of the complete, migrated template database,
// building it first when it does not exist yet.
func template(ctx context.Context, admin *pgxpool.Pool, base string) (string, error) {
	templateMu.Lock()
	defer templateMu.Unlock()
	if name, ok := templates[base]; ok {
		return name, nil
	}
	hash, err := schemaHash()
	if err != nil {
		return "", err
	}
	name := templatePrefix + hash
	exists, err := databaseExists(ctx, admin, name)
	if err != nil {
		return "", err
	}
	if !exists {
		if err := buildTemplate(ctx, admin, base, name); err != nil {
			return "", err
		}
	}
	templates[base] = name
	return name, nil
}

func databaseExists(ctx context.Context, admin interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, name string) (bool, error) {
	var ok bool
	err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("look up template database: %w", err)
	}
	return ok, nil
}

// buildTemplate migrates a work database and renames it to name, so name only
// ever exists complete. The advisory lock serialises builders across processes.
func buildTemplate(ctx context.Context, admin *pgxpool.Pool, base, name string) error {
	conn, err := admin.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire admin connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, templateLockKey); err != nil {
		return fmt.Errorf("lock template build: %w", err)
	}
	defer func() {
		// Unlock; a connection that cannot is dropped, which releases the lock.
		if _, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, templateLockKey); err != nil {
			_ = conn.Conn().Close(context.WithoutCancel(ctx))
		}
	}()

	// Someone else may have built it while we waited.
	if exists, err := databaseExists(ctx, conn, name); err != nil || exists {
		return err
	}
	// Old templates, and the leftovers of a builder that died.
	if err := dropTemplates(ctx, conn); err != nil {
		return err
	}

	work := name + "_wip"
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+work); err != nil {
		return fmt.Errorf("create template work database: %w", err)
	}
	if err := migrateDatabase(ctx, base, work); err != nil {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "DROP DATABASE IF EXISTS "+work+" WITH (FORCE)")
		return err
	}
	if _, err := conn.Exec(ctx, "ALTER DATABASE "+work+" RENAME TO "+name); err != nil {
		return fmt.Errorf("publish template: %w", err)
	}
	// Nothing may connect to a template: a clone fails while it has sessions.
	if _, err := conn.Exec(ctx, "ALTER DATABASE "+name+" WITH ALLOW_CONNECTIONS false"); err != nil {
		return fmt.Errorf("close template to connections: %w", err)
	}
	return nil
}

// dropTemplates drops every template database and work database. The caller
// holds the build lock; a template a test still clones from only delays the drop.
func dropTemplates(ctx context.Context, conn *pgxpool.Conn) error {
	rows, err := conn.Query(ctx, `SELECT datname FROM pg_database WHERE datname LIKE $1`, strings.ReplaceAll(templatePrefix, "_", `\_`)+"%")
	if err != nil {
		return fmt.Errorf("list old templates: %w", err)
	}
	old, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("list old templates: %w", err)
	}
	for _, d := range old {
		_, _ = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{d}.Sanitize()+" WITH (FORCE)")
	}
	return nil
}

// migrateDatabase applies the goose and River migrations to database name and
// disconnects again, so the database can be renamed and cloned.
func migrateDatabase(ctx context.Context, base, name string) error {
	dsn, err := withDatabase(base, name)
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect template database: %w", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, nil); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := jobs.New(pool, nil).Migrate(ctx); err != nil {
		return fmt.Errorf("river migrate: %w", err)
	}
	return nil
}

// schemaHash identifies what a template holds: the embedded goose migrations,
// River's migration files for the linked driver, and this file's format.
func schemaHash() (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "format %s\n", schemaFormat)
	if err := hashFS(h, "goose", migrations.FS); err != nil {
		return "", err
	}
	driver := riverpgxv5.New(nil)
	for _, line := range driver.GetMigrationLines() {
		if err := hashFS(h, "river/"+line, driver.GetMigrationFS(line)); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:8]), nil
}

func hashFS(h io.Writer, label string, fsys fs.FS) error {
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s %s %d\n", label, p, len(b))
		_, err = h.Write(b)
		return err
	})
	if err != nil {
		return fmt.Errorf("hash %s migrations: %w", label, err)
	}
	return nil
}

// withDatabase returns base pointed at database name.
func withDatabase(base, name string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", EnvDatabaseURL, err)
	}
	u.Path = "/" + name
	return u.String(), nil
}
