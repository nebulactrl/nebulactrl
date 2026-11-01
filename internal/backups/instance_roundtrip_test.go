package backups

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/backup"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

// The round trip uses the real pg_dump and pg_restore. They come from PATH, or,
// when the machine has none, from the postgres:<major> image of the test server
// (only if that image is already present: the test never pulls).

// dockerPGRunner runs a Postgres client tool in a postgres:<major> container
// that reaches the test database through the host. Used where the developer
// machine has no PostgreSQL client tools.
func dockerPGRunner(image string) Runner {
	return func(ctx context.Context, c Cmd) error {
		args := []string{"run", "--rm", "--add-host=host.docker.internal:host-gateway"}
		if c.Stdin != nil {
			args = append(args, "-i")
		}
		var env []string
		for _, e := range c.Env {
			k, v, _ := strings.Cut(e, "=")
			if k == "PGHOST" && (v == "localhost" || v == "127.0.0.1" || v == "::1") {
				v = "host.docker.internal"
			}
			env = append(env, k+"="+v)
			args = append(args, "-e", k) // by name: the value stays out of the process list
		}
		args = append(args, image, c.Name)
		args = append(args, c.Args...)
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Env = append(os.Environ(), env...)
		cmd.Stdin, cmd.Stdout = c.Stdin, c.Stdout
		var tail stderrTail
		cmd.Stderr = &tail
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("docker %s: %w: %s", c.Name, err, tail.String())
		}
		return nil
	}
}

func realPGTools(t *testing.T, admin *pgxpool.Pool) PGTools {
	t.Helper()
	var num int
	require.NoError(t, admin.QueryRow(context.Background(), `SELECT current_setting('server_version_num')::int`).Scan(&num))
	server := num / 10000
	// pg_dump refuses a server newer than itself, so host tools only count
	// when their major version is at least the server's.
	if hostPGMajor("pg_dump") >= server && hostPGMajor("pg_restore") >= server {
		return DefaultPGTools()
	}
	image := "postgres:" + strconv.Itoa(server)
	if out, err := exec.Command("docker", "image", "inspect", image, "--format", "{{.Id}}").CombinedOutput(); err != nil {
		t.Skipf("no pg_dump/pg_restore >= %d on PATH and %s is not available locally (%s)", server, image, strings.TrimSpace(string(out)))
	}
	return PGTools{Run: dockerPGRunner(image), Dump: "pg_dump", Restore: "pg_restore", Check: func() error { return nil }}
}

// hostPGMajor returns the major version of a PostgreSQL client binary on PATH,
// or 0 when it is missing or its version can not be read.
func hostPGMajor(bin string) int {
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return 0
	}
	// "pg_dump (PostgreSQL) 16.15 (Ubuntu 16.15-1.pgdg24.04+2)"
	fields := strings.Fields(string(out))
	if len(fields) < 3 {
		return 0
	}
	major, err := strconv.Atoi(strings.SplitN(fields[2], ".", 2)[0])
	if err != nil {
		return 0
	}
	return major
}

// freshDatabase creates an empty database next to the test database and drops it
// when the test ends. It returns the connection URL.
func freshDatabase(t *testing.T, admin *pgxpool.Pool, baseURL, label string) string {
	t.Helper()
	var rnd [5]byte
	_, err := rand.Read(rnd[:])
	require.NoError(t, err)
	name := "nc_instbk_" + label + "_" + hex.EncodeToString(rnd[:])
	_, err = admin.Exec(context.Background(), `CREATE DATABASE `+name)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	})
	u, err := url.Parse(baseURL)
	require.NoError(t, err)
	u.Path = "/" + name
	q := u.Query()
	q.Del("search_path")
	u.RawQuery = q.Encode()
	return u.String()
}

// tableCounts returns the row count of every table of the public schema.
func tableCounts(t *testing.T, pool *pgxpool.Pool) map[string]int {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE' ORDER BY 1`)
	require.NoError(t, err)
	names, err := pgxCollectStrings(rows)
	require.NoError(t, err)
	out := map[string]int{}
	for _, n := range names {
		var c int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM "`+n+`"`).Scan(&c), n)
		out[n] = c
	}
	return out
}

func pgxCollectStrings(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}) ([]string, error) {
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// seedControlPlane fills a migrated database with enough rows to be recognizable.
func seedControlPlane(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO workspaces (name) SELECT 'team ' || g FROM generate_series(1, 3) g`,
		`INSERT INTO users (email, name, password_hash) SELECT 'user' || g || '@example.com', 'User ' || g, 'argon2id-hash-' || g FROM generate_series(1, 5) g`,
		`INSERT INTO audit_events (workspace_id, actor_type, actor_id, actor_label, action, resource_type, resource_id, metadata)
		   SELECT (SELECT id FROM workspaces ORDER BY created_at LIMIT 1), 'user', 'u', 'alice', 'thing.change.' || g, 'thing', g::text, jsonb_build_object('n', g)
		   FROM generate_series(1, 250) g`,
		// Sessions are deliberately not part of a backup.
		`INSERT INTO sessions (token, data, expiry) SELECT 'tok' || g, '\x00', now() + interval '1 day' FROM generate_series(1, 3) g`,
	} {
		_, err := pool.Exec(ctx, stmt)
		require.NoError(t, err, stmt)
	}
}

func digest(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var d string
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT md5(COALESCE((SELECT string_agg(email || name || password_hash, ',' ORDER BY email) FROM users), '') ||
		           COALESCE((SELECT string_agg(action || resource_id || metadata::text, ',' ORDER BY id) FROM audit_events), ''))`).Scan(&d))
	return d
}

// TestInstanceBackupRoundTrip is the end-to-end proof: back up a migrated,
// populated database to a local directory with the real pg_dump, restore it
// into a fresh empty database with the real pg_restore and check that every
// table has the same number of rows and the data is identical.
func TestInstanceBackupRoundTrip(t *testing.T) {
	baseURL := os.Getenv(testutil.EnvDatabaseURL)
	if baseURL == "" {
		t.Skipf("%s not set; skipping database test", testutil.EnvDatabaseURL)
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, baseURL)
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	tools := realPGTools(t, admin)

	// A control plane database as the server leaves it: goose and River migrated.
	srcURL := freshDatabase(t, admin, baseURL, "src")
	src, err := pgxpool.New(ctx, srcURL)
	require.NoError(t, err)
	t.Cleanup(src.Close)
	require.NoError(t, db.Migrate(ctx, src, nil))
	require.NoError(t, jobs.New(src, nil).Migrate(ctx))
	seedControlPlane(t, src)

	e := newInstEnvOn(t, src, srcURL, tools)
	e.enableLocal(t, 14)

	// Back up. RunNow commits the run row and its job; that is the state the dump sees.
	v, err := e.svc.RunNow(e.ctx)
	require.NoError(t, err)
	want := tableCounts(t, src)
	wantDigest := digest(t, src)
	e.svc.Run(e.ctx, v.ID)
	run, err := e.svc.q.GetInstanceBackup(e.ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Status, run.Error)
	require.Greater(t, run.SizeBytes, int64(0))
	require.NotEmpty(t, run.PgVersion)

	require.Greater(t, len(want), 25, "the migrated schema has dozens of tables")
	require.Equal(t, 250+2, want["audit_events"], "250 seeded rows plus the settings change and the run")
	require.Equal(t, 3, want["sessions"])
	require.Equal(t, 1, want["instance_backups"])

	// Restore into a fresh database.
	dstURL := freshDatabase(t, admin, baseURL, "dst")
	dst, err := pgxpool.New(ctx, dstURL)
	require.NoError(t, err)
	t.Cleanup(dst.Close)
	require.NoError(t, CheckEmptyDatabase(ctx, dstURL))
	var log bytes.Buffer
	res, err := RestoreInstance(ctx, RestoreOptions{
		Source: RestoreSource{Dir: filepath.Join(e.dir, "nightly")}, DatabaseURL: dstURL, TmpDir: t.TempDir(), Tools: tools, Out: &log,
	})
	require.NoError(t, err, log.String())
	require.Equal(t, run.ID.String(), res.Backup.Manifest.BackupID)
	require.Equal(t, len(want), res.Tables)
	require.Contains(t, log.String(), "SAME NEBULACTRL_SECRET_KEY")
	require.Contains(t, log.String(), KeyFingerprint(bytes.Repeat([]byte{3}, 32)), "the output names the key to start with")

	// Every table has the same row count, except sessions: a restore signs everyone out.
	got := tableCounts(t, dst)
	require.Equal(t, slices.Sorted(mapKeys(want)), slices.Sorted(mapKeys(got)), "same tables")
	for name, n := range want {
		if name == "sessions" {
			require.Zero(t, got[name], "session rows are not backed up")
			continue
		}
		require.Equal(t, n, got[name], "row count of %s", name)
	}
	require.Equal(t, wantDigest, digest(t, dst), "the data is identical, not just the counts")

	// The restored database is the control plane's: migrations are recorded as applied,
	// so the server starting on it has nothing left to run.
	var applied, available int
	require.NoError(t, dst.QueryRow(ctx, `SELECT count(*) FROM goose_db_version WHERE is_applied AND version_id > 0`).Scan(&applied))
	entries, err := os.ReadDir("../../db/migrations")
	require.NoError(t, err)
	for _, en := range entries {
		if strings.HasSuffix(en.Name(), ".sql") {
			available++
		}
	}
	require.Equal(t, available, applied)

	// Restoring is only ever into an empty database.
	afterRun := tableCounts(t, src)
	_, err = RestoreInstance(ctx, RestoreOptions{Source: RestoreSource{Dir: filepath.Join(e.dir, "nightly")}, DatabaseURL: dstURL, TmpDir: t.TempDir(), Tools: tools})
	require.ErrorIs(t, err, ErrDatabaseNotEmpty)
	_, err = RestoreInstance(ctx, RestoreOptions{Source: RestoreSource{Dir: filepath.Join(e.dir, "nightly")}, DatabaseURL: srcURL, TmpDir: t.TempDir(), Tools: tools})
	require.ErrorIs(t, err, ErrDatabaseNotEmpty)
	require.Equal(t, afterRun, tableCounts(t, src), "the refused restore left the source untouched")
}

// A damaged backup is refused before pg_restore sees it, and a restore that
// fails halfway leaves the target empty (it runs in one transaction).
func TestInstanceRestoreFailuresLeaveTargetEmpty(t *testing.T) {
	baseURL := os.Getenv(testutil.EnvDatabaseURL)
	if baseURL == "" {
		t.Skipf("%s not set; skipping database test", testutil.EnvDatabaseURL)
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, baseURL)
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	tools := realPGTools(t, admin)

	srcURL := freshDatabase(t, admin, baseURL, "src")
	src, err := pgxpool.New(ctx, srcURL)
	require.NoError(t, err)
	t.Cleanup(src.Close)
	require.NoError(t, db.Migrate(ctx, src, nil))
	require.NoError(t, jobs.New(src, nil).Migrate(ctx))
	seedControlPlane(t, src)
	e := newInstEnvOn(t, src, srcURL, tools)
	e.enableLocal(t, 14)
	run := e.runOnce(t)
	require.Equal(t, "succeeded", run.Status, run.Error)
	srcStore, err := backup.NewLocalStore(e.dir, "nightly")
	require.NoError(t, err)
	found, err := FindBackups(ctx, srcStore, "")
	require.NoError(t, err)
	require.Len(t, found, 1)
	good, err := os.ReadFile(filepath.Join(e.dir, "nightly", filepath.FromSlash(run.ObjectKey)))
	require.NoError(t, err)

	restoreFrom := func(dir string) error {
		dstURL := freshDatabase(t, admin, baseURL, "dst")
		_, err := RestoreInstance(ctx, RestoreOptions{Source: RestoreSource{Dir: dir}, DatabaseURL: dstURL, TmpDir: t.TempDir(), Tools: tools})
		require.NoError(t, CheckEmptyDatabase(ctx, dstURL), "the target must still be empty")
		return err
	}

	// 1. A flipped byte: the checksum in the manifest does not match.
	flipped := t.TempDir()
	st, err := backup.NewLocalStore(flipped, "")
	require.NoError(t, err)
	bad := bytes.Clone(good)
	bad[len(bad)/2] ^= 0xff
	m := putBackup(t, st, "nebulactrl-instance/2026/10/bad.dump", good, time.Now().UTC(), nil)
	require.NoError(t, os.WriteFile(filepath.Join(flipped, "nebulactrl-instance", "2026", "10", "bad.dump"), bad, 0o600))
	require.ErrorContains(t, restoreFrom(flipped), "SHA-256")
	_ = m

	// 2. A dump cut off halfway whose manifest agrees with the cut file: the
	// checksum passes, pg_restore fails inside its transaction, nothing is left.
	cut := t.TempDir()
	st, err = backup.NewLocalStore(cut, "")
	require.NoError(t, err)
	putBackup(t, st, "nebulactrl-instance/2026/10/cut.dump", good[:len(good)*6/10], time.Now().UTC(), nil)
	err = restoreFrom(cut)
	require.ErrorContains(t, err, "the database was left empty")
	require.ErrorContains(t, err, "end of file", "pg_restore itself failed on the truncated dump")

	// 3. No backups at all.
	require.ErrorContains(t, restoreFrom(t.TempDir()), "no control-plane backups")
}

func mapKeys[K comparable, V any](m map[K]V) func(yield func(K) bool) {
	return func(yield func(K) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}
