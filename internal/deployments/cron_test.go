package deployments

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type cronTaskRow struct {
	Action, Schedule, Timezone, Concurrency string
	Timeout                                 int
	Enabled                                 bool
	Next                                    *time.Time
}

func (f *routeFixture) cronRows(serviceID uuid.UUID) []cronTaskRow {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(),
		"SELECT action, schedule, timezone, concurrency, timeout_seconds, enabled, next_run_at FROM service_tasks WHERE service_id = $1 AND name = '__cron'", serviceID)
	require.NoError(f.t, err)
	defer rows.Close()
	var out []cronTaskRow
	for rows.Next() {
		var r cronTaskRow
		require.NoError(f.t, rows.Scan(&r.Action, &r.Schedule, &r.Timezone, &r.Concurrency, &r.Timeout, &r.Enabled, &r.Next))
		out = append(out, r)
	}
	return out
}

func cronConfig(schedule string) services.Config {
	return services.Config{
		Image: "busybox:latest", OneShot: true, StartCommand: []string{"sh", "-c", "echo tick"},
		Cron: &services.CronSpec{Schedule: schedule, Timezone: "Europe/Berlin", TimeoutSeconds: 120},
	}.WithDefaults()
}

func TestCronDeployStartsNothingAndSyncsTheHiddenTask(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("job")
	require.Empty(t, f.cronRows(web.ID))

	setConfig(t, f, web.ID, configJSON(t, cronConfig("*/5 * * * *")))
	_, p := f.deployWeb(web.ID)
	require.True(t, p.NoStart)
	require.True(t, p.OneShot)
	require.Equal(t, []string{"sh", "-c", "echo tick"}, p.StartCommand)

	rows := f.cronRows(web.ID)
	require.Len(t, rows, 1)
	require.Equal(t, "service", rows[0].Action)
	require.Equal(t, "*/5 * * * *", rows[0].Schedule)
	require.Equal(t, "Europe/Berlin", rows[0].Timezone)
	require.Equal(t, "forbid", rows[0].Concurrency)
	require.Equal(t, 120, rows[0].Timeout)
	require.True(t, rows[0].Enabled)
	require.NotNil(t, rows[0].Next)
	require.True(t, rows[0].Next.After(time.Now().Add(-time.Second)))

	// A new release with another schedule updates the same row.
	setConfig(t, f, web.ID, configJSON(t, cronConfig("0 3 * * *")))
	f.deployWeb(web.ID)
	rows = f.cronRows(web.ID)
	require.Len(t, rows, 1)
	require.Equal(t, "0 3 * * *", rows[0].Schedule)

	// Turning cron off removes the hidden task, and the deploy starts the service again.
	plain := services.Config{Image: "busybox:latest", OneShot: true}.WithDefaults()
	setConfig(t, f, web.ID, configJSON(t, plain))
	_, p = f.deployWeb(web.ID)
	require.False(t, p.NoStart)
	require.Empty(t, f.cronRows(web.ID))
}

func TestRollbackToANonCronReleaseDeletesTheHiddenTask(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("job")
	first := f.currentDeployment(web.ID) // the plain release f.web recorded

	setConfig(t, f, web.ID, configJSON(t, cronConfig("*/5 * * * *")))
	f.deployWeb(web.ID)
	require.Len(t, f.cronRows(web.ID), 1)

	rv, err := f.deps.Rollback(f.ctx, first)
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeploy(context.Background(), rv.ID))
	var p agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.lastCommand(agentprotocol.KindDeploy).Payload, &p))
	require.False(t, p.NoStart, "the old release is not a cron release")
	f.finish(rv.ID)
	require.Empty(t, f.cronRows(web.ID), "the schedule follows the deployed release")
}
