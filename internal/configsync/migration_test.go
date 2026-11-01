package configsync_test

// Migration 0007 (project config): up/down/up on a populated schema, the constraints it adds and its
// cascades. The harness migrates every test schema with goose, so the test drives goose on the same
// schema to take the migration back and forward.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/db/migrations"
)

const migrationVersion = 7

// migrationPGCode is the SQLSTATE of a database error ("" for anything else).
func migrationPGCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func (f *applyFixture) migrationRegclass(name string) bool {
	f.t.Helper()
	var reg *string
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT to_regclass($1)::text", name).Scan(&reg))
	return reg != nil
}

func (f *applyFixture) migrationHasConfigKey() bool {
	f.t.Helper()
	return f.count(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'backup_schedules' AND column_name = 'config_key'`) == 1
}

func TestMigration0007GoesDownAndUpAgainOnAPopulatedDatabase(t *testing.T) {
	f := newApplyFixture(t)
	ctx := context.Background()

	// A populated database: a managed service with a keyed backup schedule, ownership rows and shared variables.
	f.mustApply(applyDeferFile + "\n[variables]\nAPP_NAME = \"x\"\n")
	pg := f.svcRow(f.staging, "pg")
	require.Equal(t, 1, f.count("SELECT count(*) FROM config_sources"))
	require.Equal(t, 1, f.count("SELECT count(*) FROM environment_config_settings"))
	require.Equal(t, 1, f.count("SELECT count(*) FROM backup_schedules WHERE config_key = 'nightly'"))

	sqlDB := stdlib.OpenDBFromPool(f.pool)
	defer func() { _ = sqlDB.Close() }()
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	require.NoError(t, err)
	version, err := p.GetDBVersion(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, version, int64(migrationVersion))

	// Down: the tables and the column are gone, the schedule they identified stays.
	res, err := p.ApplyVersion(ctx, migrationVersion, false)
	require.NoError(t, err)
	require.False(t, res.Empty)
	require.False(t, f.migrationRegclass("config_sources"))
	require.False(t, f.migrationRegclass("environment_config_settings"))
	require.False(t, f.migrationHasConfigKey())
	require.Equal(t, 1, f.count("SELECT count(*) FROM backup_schedules"), "the schedule itself survives")

	// Up again: empty tables, the column back (NULL for the old row) and the constraints.
	_, err = p.ApplyVersion(ctx, migrationVersion, true)
	require.NoError(t, err)
	require.True(t, f.migrationRegclass("config_sources"))
	require.True(t, f.migrationRegclass("environment_config_settings"))
	require.True(t, f.migrationHasConfigKey())
	require.Equal(t, 0, f.count("SELECT count(*) FROM config_sources"))
	require.Equal(t, 0, f.count("SELECT count(*) FROM environment_config_settings"))
	require.Equal(t, 1, f.count("SELECT count(*) FROM backup_schedules WHERE config_key IS NULL"))

	dup := `INSERT INTO backup_schedules (workspace_id, service_id, destination_id, cron, timezone, enabled, retention_count, retention_days, next_run_at, config_key)
		SELECT workspace_id, service_id, destination_id, cron, timezone, enabled, retention_count, retention_days, next_run_at, $1 FROM backup_schedules LIMIT 1`
	_, err = f.pool.Exec(ctx, dup, "k")
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, dup, "k")
	require.Equal(t, "23505", migrationPGCode(err), "a schedule key is unique per service: %v", err)
	_, err = f.pool.Exec(ctx, dup, nil)
	require.NoError(t, err, "schedules without a key may repeat")
	_, err = f.pool.Exec(ctx, dup, nil)
	require.NoError(t, err)

	// The ownership table checks what it records.
	insert := `INSERT INTO config_sources (service_id, environment_id, workspace_id, origin, file_sha256, applied_generation, applied_by_type)
		VALUES ($1, $2, $3, $4, 'abc', 1, $5)`
	_, err = f.pool.Exec(ctx, insert, pg.ID, f.staging.ID, f.ws.ID, "bogus", "user")
	require.Equal(t, "23514", migrationPGCode(err), "origin is one of git, cli, api, mcp, ui: %v", err)
	_, err = f.pool.Exec(ctx, insert, pg.ID, f.staging.ID, f.ws.ID, "cli", "robot")
	require.Equal(t, "23514", migrationPGCode(err), "the actor is a user, token, mcp or system: %v", err)
	_, err = f.pool.Exec(ctx, insert, pg.ID, f.staging.ID, f.ws.ID, "git", "system")
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, insert, pg.ID, f.staging.ID, f.ws.ID, "git", "system")
	require.Equal(t, "23505", migrationPGCode(err), "one row per service: %v", err)

	// The binding table: a repository needs a branch, a status is one of the known ones.
	_, err = f.pool.Exec(ctx, "INSERT INTO environment_config_settings (environment_id, workspace_id, repo_full_name) VALUES ($1, $2, 'acme/shop')", f.prod.ID, f.ws.ID)
	require.Equal(t, "23514", migrationPGCode(err), "repo and branch are both set or both empty: %v", err)
	_, err = f.pool.Exec(ctx, "INSERT INTO environment_config_settings (environment_id, workspace_id, status) VALUES ($1, $2, 'weird')", f.prod.ID, f.ws.ID)
	require.Equal(t, "23514", migrationPGCode(err), "unknown status: %v", err)
	_, err = f.pool.Exec(ctx, "INSERT INTO environment_config_settings (environment_id, workspace_id, repo_full_name, branch) VALUES ($1, $2, 'acme/shop', 'main')", f.prod.ID, f.ws.ID)
	require.NoError(t, err)

	// Cascades: the row follows its service, the settings follow their environment.
	_, err = f.pool.Exec(ctx, "DELETE FROM services WHERE id = $1", pg.ID)
	require.NoError(t, err)
	require.Equal(t, 0, f.count("SELECT count(*) FROM config_sources WHERE service_id = $1", pg.ID))
	_, err = f.pool.Exec(ctx, "DELETE FROM environments WHERE id = $1", f.prod.ID)
	require.NoError(t, err)
	require.Equal(t, 0, f.count("SELECT count(*) FROM environment_config_settings WHERE environment_id = $1", f.prod.ID))
}
