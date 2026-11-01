package deployments

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

func TestOutcomeWindowStart(t *testing.T) {
	// 23:59 in UTC-5 is already the next UTC day; the window is UTC days.
	now := time.Date(2026, 10, 26, 23, 59, 0, 0, time.FixedZone("x", -5*3600))
	require.Equal(t, time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -29), outcomeWindowStart(now, 30))
	require.Equal(t, time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC), outcomeWindowStart(now, 1))
}

func TestFillOutcomeDays(t *testing.T) {
	start := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	date := func(d int) pgtype.Date {
		return pgtype.Date{Time: time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC), Valid: true}
	}
	got := FillOutcomeDays(start, 4, []db.DeploymentOutcomesPerDayRow{
		{Day: date(25), Succeeded: 3, Failed: 1, Other: 2},
		{Day: date(27), Succeeded: 1},
		{Day: date(1)}, // outside the window: ignored
	})
	require.Equal(t, []DailyOutcome{
		{Date: "2026-10-24"},
		{Date: "2026-10-25", Succeeded: 3, Failed: 1, Other: 2},
		{Date: "2026-10-26"},
		{Date: "2026-10-27", Succeeded: 1},
	}, got.Days)
}

// TestDeploymentOutcomesPerDay runs the aggregate against Postgres: per UTC
// day, failed includes rollback_failed, other is everything else, other
// environments and deleted services are excluded, and the window start is
// respected.
func TestDeploymentOutcomesPerDay(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()
	q := db.New(pool)

	var ws, project, env, otherEnv uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO workspaces (name) VALUES ('w') RETURNING id`).Scan(&ws))
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO projects (workspace_id, name, slug) VALUES ($1, 'p', 'p') RETURNING id`, ws).Scan(&project))
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO environments (project_id, name, slug) VALUES ($1, 'production', 'production') RETURNING id`, project).Scan(&env))
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO environments (project_id, name, slug) VALUES ($1, 'staging', 'staging') RETURNING id`, project).Scan(&otherEnv))

	newService := func(env uuid.UUID, slug string, deleted bool) uuid.UUID {
		var id uuid.UUID
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO services (environment_id, name, slug, source_type, deleted_at)
			VALUES ($1, $2, $2, 'image', CASE WHEN $3 THEN now() END) RETURNING id`, env, slug, deleted).Scan(&id))
		return id
	}
	a, b := newService(env, "a", false), newService(env, "b", false)
	gone := newService(env, "gone", true)
	elsewhere := newService(otherEnv, "elsewhere", false)

	day := func(daysAgo, hour int) time.Time {
		n := time.Now().UTC()
		return time.Date(n.Year(), n.Month(), n.Day(), hour, 30, 0, 0, time.UTC).AddDate(0, 0, -daysAgo)
	}
	add := func(svc uuid.UUID, status string, at time.Time) {
		_, err := pool.Exec(ctx, `INSERT INTO deployments (service_id, trigger, actor_type, status, created_at)
			VALUES ($1, 'manual', 'user', $2, $3)`, svc, status, at)
		require.NoError(t, err)
	}
	// Today (two services, so one queued row does not trip deployments_one_active).
	add(a, "succeeded", day(0, 0))
	add(a, "succeeded", day(0, 0).Add(time.Minute))
	add(a, "failed", day(0, 0).Add(2*time.Minute))
	add(b, "queued", day(0, 0).Add(3*time.Minute))
	// Two days ago.
	add(a, "rollback_failed", day(2, 23))
	add(a, "rolled_back", day(2, 1))
	add(b, "canceled", day(2, 5))
	// Outside the 3-day window, other environment and deleted service.
	add(a, "succeeded", day(3, 12))
	add(elsewhere, "succeeded", day(0, 0))
	add(gone, "succeeded", day(0, 0))

	rows, err := q.DeploymentOutcomesPerDay(ctx, db.DeploymentOutcomesPerDayParams{
		EnvironmentID: env, Since: outcomeWindowStart(time.Now(), 3),
	})
	require.NoError(t, err)
	got := FillOutcomeDays(outcomeWindowStart(time.Now(), 3), 3, rows)

	today := day(0, 0).Format(time.DateOnly)
	twoAgo := day(2, 0).Format(time.DateOnly)
	require.Equal(t, []DailyOutcome{
		{Date: twoAgo, Failed: 1, Other: 2},
		{Date: day(1, 0).Format(time.DateOnly)},
		{Date: today, Succeeded: 2, Failed: 1, Other: 1},
	}, got.Days)
}
