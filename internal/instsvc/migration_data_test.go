package instsvc

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestMigration0011DropsTheRoutesOfTheOldPlatformServices runs migration 0011 over
// the state of an install that has the old per-team services: S3, registry, Loki
// and Grafana, each with a public hostname. Only Headscale keeps its IP allow
// list now, so a leftover domain of the others would be served to the internet:
// the migration removes the domains, keeps the services (their containers stay
// for the manual move) and leaves the Headscale domain alone.
func TestMigration0011DropsTheRoutesOfTheOldPlatformServices(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		_, err := f.pool.Exec(ctx, q, args...)
		require.NoError(t, err)
	}

	raw, err := os.ReadFile("../../db/migrations/0011_instance_services.sql")
	require.NoError(t, err)
	up, down, ok := strings.Cut(string(raw), "-- +goose Down")
	require.True(t, ok)
	up = strings.TrimPrefix(up, "-- +goose Up")

	// Back to the schema before 0011 (the tests' schema is migrated to the latest).
	exec(down)

	var proj, env uuid.UUID
	require.NoError(t, f.pool.QueryRow(ctx, `INSERT INTO projects (workspace_id, name, slug, system) VALUES ($1, 'NebulaCtrl platform', 'nebulactrl-platform', true) RETURNING id`, f.a.ID).Scan(&proj))
	require.NoError(t, f.pool.QueryRow(ctx, `INSERT INTO environments (project_id, name, slug) VALUES ($1, 'Platform', 'platform') RETURNING id`, proj).Scan(&env))

	services := map[string]uuid.UUID{}
	for _, kind := range []string{"s3", "registry", "loki", "grafana", "headscale"} {
		id := uuid.New()
		services[kind] = id
		exec(`INSERT INTO services (id, environment_id, name, slug, kind, source_type, config) VALUES ($1, $2, $3, $3, 'app', 'image', '{}')`, id, env, kind)
		exec(`INSERT INTO domains (service_id, hostname, target_port) VALUES ($1, $2, 80)`, id, kind+".example.com")
		exec(`INSERT INTO platform_services (workspace_id, kind, service_ids, environment_id) VALUES ($1, $2, $3, $4)`, f.a.ID, kind, []uuid.UUID{id}, env)
	}
	// An ordinary service of the team keeps its domain.
	web := uuid.New()
	exec(`INSERT INTO services (id, environment_id, name, slug, kind, source_type, config) VALUES ($1, $2, 'web', 'web', 'app', 'image', '{}')`, web, env)
	exec(`INSERT INTO domains (service_id, hostname, target_port) VALUES ($1, 'app.example.com', 80)`, web)

	exec(up)

	hosts := func() []string {
		rows, err := f.pool.Query(ctx, `SELECT hostname FROM domains ORDER BY hostname`)
		require.NoError(t, err)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var h string
			require.NoError(t, rows.Scan(&h))
			out = append(out, h)
		}
		require.NoError(t, rows.Err())
		return out
	}
	require.Equal(t, []string{"app.example.com", "headscale.example.com"}, hosts(), "only the Headscale and the ordinary domains are left")
	require.Equal(t, 5, f.count(`SELECT count(*) FROM services WHERE deleted_at IS NULL AND id = ANY($1)`, []uuid.UUID{
		services["s3"], services["registry"], services["loki"], services["grafana"], services["headscale"],
	}), "the old services themselves stay")
	require.Equal(t, 1, f.count(`SELECT count(*) FROM platform_services`))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM platform_services WHERE kind = 'headscale' AND service_ids = $1`, []uuid.UUID{services["headscale"]}))
}

// TestMigration0016StopsTheRetiredPlatformContainers: the old per-team services
// stay in the system project after 0011. They are stopped (volumes kept); the
// Headscale service and every service outside a system project are not touched.
func TestMigration0016StopsTheRetiredPlatformContainers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		_, err := f.pool.Exec(ctx, q, args...)
		require.NoError(t, err)
	}
	raw, err := os.ReadFile("../../db/migrations/0016_stop_retired_platform_services.sql")
	require.NoError(t, err)
	up, _, ok := strings.Cut(string(raw), "-- +goose Down")
	require.True(t, ok)
	up = strings.TrimPrefix(up, "-- +goose Up")

	var sysProj, sysEnv, userProj, userEnv uuid.UUID
	require.NoError(t, f.pool.QueryRow(ctx, `INSERT INTO projects (workspace_id, name, slug, system) VALUES ($1, 'NebulaCtrl platform', 'nebulactrl-platform', true) RETURNING id`, f.a.ID).Scan(&sysProj))
	require.NoError(t, f.pool.QueryRow(ctx, `INSERT INTO environments (project_id, name, slug) VALUES ($1, 'Platform', 'platform') RETURNING id`, sysProj).Scan(&sysEnv))
	require.NoError(t, f.pool.QueryRow(ctx, `INSERT INTO projects (workspace_id, name, slug) VALUES ($1, 'Shop', 'shop') RETURNING id`, f.a.ID).Scan(&userProj))
	require.NoError(t, f.pool.QueryRow(ctx, `INSERT INTO environments (project_id, name, slug) VALUES ($1, 'Production', 'production') RETURNING id`, userProj).Scan(&userEnv))

	service := func(env uuid.UUID, name string) uuid.UUID {
		id := uuid.New()
		exec(`INSERT INTO services (id, environment_id, name, slug, kind, source_type, config) VALUES ($1, $2, $3, $3, 'app', 'image', '{}')`, id, env, name)
		return id
	}
	retiredS3, retiredLoki, headscale, web := service(sysEnv, "s3"), service(sysEnv, "loki"), service(sysEnv, "headscale"), service(userEnv, "web")
	exec(`INSERT INTO platform_services (workspace_id, kind, service_ids, environment_id) VALUES ($1, 'headscale', $2, $3)`, f.a.ID, []uuid.UUID{headscale}, sysEnv)

	exec(up)

	state := func(id uuid.UUID) string {
		var s string
		require.NoError(t, f.pool.QueryRow(ctx, `SELECT desired_state FROM services WHERE id = $1`, id).Scan(&s))
		return s
	}
	require.Equal(t, "stopped", state(retiredS3))
	require.Equal(t, "stopped", state(retiredLoki))
	require.Equal(t, "running", state(headscale), "the Headscale service is still a platform service")
	require.Equal(t, "running", state(web), "ordinary services are never touched")
	require.Equal(t, 4, f.count(`SELECT count(*) FROM services WHERE deleted_at IS NULL AND id = ANY($1)`, []uuid.UUID{retiredS3, retiredLoki, headscale, web}), "nothing is deleted: the volumes stay")
}
