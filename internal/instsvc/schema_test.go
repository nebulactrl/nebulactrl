package instsvc

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// pgCode is the SQLSTATE of err ("" when it is not a database error).
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if err == nil {
		return ""
	}
	if ok := asPgErr(err, &pgErr); !ok {
		return ""
	}
	return pgErr.Code
}

func TestMigrationConstraints(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	exec := func(q string, args ...any) error {
		_, err := f.pool.Exec(ctx, q, args...)
		return err
	}
	const (
		unique = "23505"
		check  = "23514"
	)

	// Headscale is the only per-team platform service; S3, registry, Loki and Grafana are refused.
	for _, kind := range []string{"s3", "loki", "grafana", "registry"} {
		require.Equal(t, check, pgCode(exec(`INSERT INTO platform_services (workspace_id, kind) VALUES ($1, $2)`, f.a.ID, kind)), kind)
	}
	require.NoError(t, exec(`INSERT INTO platform_services (workspace_id, kind) VALUES ($1, 'headscale')`, f.a.ID))

	// A registry is external or the team's one built-in slice.
	require.Equal(t, check, pgCode(exec(`INSERT INTO registries (workspace_id, name, kind, host) VALUES ($1, 'm', 'managed', 'h')`, f.a.ID)))
	require.NoError(t, exec(`INSERT INTO registries (workspace_id, name, kind, host) VALUES ($1, 'e1', 'external', 'h')`, f.a.ID))
	require.NoError(t, exec(`INSERT INTO registries (workspace_id, name, kind, host) VALUES ($1, 'e2', 'external', 'h')`, f.a.ID))
	require.NoError(t, exec(`INSERT INTO registries (workspace_id, name, kind, host) VALUES ($1, 'b1', 'builtin', 'h')`, f.a.ID))
	require.Equal(t, unique, pgCode(exec(`INSERT INTO registries (workspace_id, name, kind, host) VALUES ($1, 'b2', 'builtin', 'h')`, f.a.ID)))
	require.NoError(t, exec(`INSERT INTO registries (workspace_id, name, kind, host) VALUES ($1, 'b1', 'builtin', 'h')`, f.b.ID), "another team has its own")
	require.Equal(t, 0, f.count(`SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'registries' AND column_name = 'platform_service_id'`))

	// Backup destinations, instance backup and log drains.
	require.NoError(t, exec(`INSERT INTO backup_destinations (workspace_id, name, kind) VALUES ($1, 'd1', 'builtin')`, f.a.ID))
	require.Equal(t, unique, pgCode(exec(`INSERT INTO backup_destinations (workspace_id, name, kind) VALUES ($1, 'd2', 'builtin')`, f.a.ID)))
	require.Equal(t, check, pgCode(exec(`INSERT INTO backup_destinations (workspace_id, name, kind) VALUES ($1, 'd3', 'nfs')`, f.a.ID)))
	require.NoError(t, exec(`UPDATE instance_backup_settings SET dest_kind = 'builtin'`))
	require.Equal(t, check, pgCode(exec(`UPDATE instance_backup_settings SET dest_kind = 'nfs'`)))
	require.NoError(t, exec(`INSERT INTO log_drains (workspace_id, name, kind, builtin) VALUES ($1, 'l1', 'loki', true)`, f.a.ID))
	require.Equal(t, unique, pgCode(exec(`INSERT INTO log_drains (workspace_id, name, kind, builtin) VALUES ($1, 'l2', 'loki', true)`, f.a.ID)))
	require.NoError(t, exec(`INSERT INTO log_drains (workspace_id, name, kind) VALUES ($1, 'l3', 'loki')`, f.a.ID), "ordinary drains are not limited")

	// Slices: one per team and kind, a key belongs to one slice, quotas are positive.
	ins := `INSERT INTO team_service_slices (team_id, team_name, kind, ref, access_key) VALUES ($1, 'x', $2, 'r', $3)`
	require.NoError(t, exec(ins, f.a.ID, "s3", "KEY1"))
	require.Equal(t, unique, pgCode(exec(ins, f.a.ID, "s3", "KEY2")))
	require.Equal(t, unique, pgCode(exec(ins, f.b.ID, "s3", "KEY1")), "an access key identifies one team")
	require.NoError(t, exec(ins, f.a.ID, "registry", ""))
	require.NoError(t, exec(ins, f.b.ID, "registry", ""), "empty keys (registry, loki) do not collide")
	require.Equal(t, check, pgCode(exec(ins, f.a.ID, "grafana", "")))
	require.Equal(t, check, pgCode(exec(`UPDATE team_service_slices SET quota_bytes = 0 WHERE kind = 's3'`)))
	require.Equal(t, check, pgCode(exec(`UPDATE team_service_slices SET alert_level = 50 WHERE kind = 's3'`)))
	require.Equal(t, check, pgCode(exec(`UPDATE team_service_slices SET state = 'gone' WHERE kind = 's3'`)))
	require.Equal(t, check, pgCode(exec(`INSERT INTO instance_services (kind) VALUES ('grafana')`)))

	// A slice outlives its team: no foreign key.
	require.NoError(t, exec(`DELETE FROM workspaces WHERE id = $1`, f.b.ID))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM team_service_slices WHERE team_id = $1`, f.b.ID))
}
