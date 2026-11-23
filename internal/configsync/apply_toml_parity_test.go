package configsync_test

// Settings that exist in the dashboard for every kind of service and so must be settable in the
// file: stop grace and egress for a stack, scheduled tasks for a database, and turning a cron
// schedule off.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/configsync"
)

const applyStackParityHead = "schema = 1\nserver = \"agent1\"\n\n[services.suite]\n"

const applyStackParityBody = `
[services.suite.stack]
compose = '''
services:
  web:
    image: nginx:1.27-alpine
'''
`

func TestApplyStackStopGraceAndEgress(t *testing.T) {
	f := newApplyFixture(t)

	v1 := applyStackParityHead + "stop_grace_seconds = 45\nblock_egress = true\n" + applyStackParityBody
	pl := f.plan("staging", v1)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	res := f.mustApply(v1)
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	suite := f.svcRow(f.staging, "suite")
	cfg := f.cfgOf(suite.ID)
	require.Equal(t, 45, cfg.StopGraceSeconds)
	require.True(t, cfg.BlockEgress)
	require.Contains(t, f.owned(suite.ID), "config.stopGraceSeconds")
	require.Contains(t, f.owned(suite.ID), "config.blockEgress")

	// A change of an existing stack is a staged config change like for an app.
	v2 := applyStackParityHead + "stop_grace_seconds = 90\nblock_egress = false\n" + applyStackParityBody
	res = f.mustApply(v2)
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	cfg = f.cfgOf(suite.ID)
	require.Equal(t, 90, cfg.StopGraceSeconds)
	require.False(t, cfg.BlockEgress)
	require.NotNil(t, cfg.Stack, "the compose file stays")
	require.Equal(t, configsync.StatusNoop, f.mustApply(v2).Status)
}

func TestApplyDatabaseTasks(t *testing.T) {
	f := newApplyFixture(t)
	v1 := `schema = 1
server = "agent1"

[services.pg.database]
engine = "postgres"
version = "17"

[services.pg.tasks.vacuum]
schedule = "0 4 * * *"
command = ["psql", "-c", "VACUUM"]
`
	pl := f.plan("staging", v1)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	res := f.mustApply(v1)
	require.NotEqual(t, configsync.StatusNoop, res.Status, applyJSON(res.Steps))
	pg := f.svcRow(f.staging, "pg")
	rows := f.taskRows(pg.ID)
	require.Contains(t, rows, "vacuum")
	require.Equal(t, "0 4 * * *", rows["vacuum"].Schedule)
	require.Contains(t, f.owned(pg.ID), "tasks.vacuum")

	require.Equal(t, configsync.StatusNoop, f.mustApply(v1).Status)

	// Prune removes it again.
	v2 := `schema = 1
server = "agent1"

[services.pg.database]
engine = "postgres"
version = "17"
`
	pl = f.plan("staging", v2, applyWithPrune())
	require.Equal(t, map[string][]string{"pg": {"tasks.vacuum"}}, applyRemovedFields(pl))
	f.mustApply(v2, applyWithPrune())
	require.Empty(t, f.taskRows(pg.ID))
}

const applyCronHead = "schema = 1\nserver = \"agent1\"\n\n[services.job]\nimage = \"nginx:1.27\"\n"

const applyCronTable = `
[services.job.cron]
schedule = "0 3 * * *"
timezone = "Europe/Berlin"
concurrency = "allow"
`

func TestApplyPruneTurnsACronScheduleOff(t *testing.T) {
	f := newApplyFixture(t)
	v1 := applyCronHead + applyCronTable
	f.mustApply(v1)
	job := f.svcRow(f.staging, "job")
	cfg := f.cfgOf(job.ID)
	require.NotNil(t, cfg.Cron)
	require.True(t, cfg.OneShot, "a cron service is a one-shot")

	v2 := applyCronHead

	// Without prune the schedule is released and stays.
	pl := f.plan("staging", v2)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.Empty(t, applyRemovedFields(pl))
	require.Contains(t, applyPlanService(t, pl, "job").Released, "services.job.cron.schedule")
	f.mustApply(v2)
	require.NotNil(t, f.cfgOf(job.ID).Cron, "released, not removed")
	require.NotContains(t, f.owned(job.ID), "config.cron.schedule")

	// Ownership is gone, so a prune no longer touches it.
	require.Empty(t, applyRemovedFields(f.plan("staging", v2, applyWithPrune())))
}

func TestApplyPruneOfCronKeepsAOneShotServiceUnlessTheFileSaysOtherwise(t *testing.T) {
	for name, c := range map[string]struct {
		file    string
		oneShot bool
	}{
		"one_shot not written": {applyCronHead, true},
		"one_shot = true":      {applyCronHead + "one_shot = true\n", true},
		"one_shot = false":     {applyCronHead + "one_shot = false\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newApplyFixture(t)
			f.mustApply(applyCronHead + applyCronTable)
			job := f.svcRow(f.staging, "job")
			require.NotNil(t, f.cfgOf(job.ID).Cron)

			pl := f.plan("staging", c.file, applyWithPrune())
			require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
			require.Equal(t, map[string][]string{"job": {"config.cron.schedule"}}, applyRemovedFields(pl))
			change := ""
			for _, ch := range applyPlanService(t, pl, "job").Changes {
				if ch.Field == "config.cron.schedule" {
					change = ch.Applies
				}
			}
			require.Equal(t, configsync.AppliesStaged, change, "turning a schedule off is a staged change")

			commands := f.disp.count()
			res := f.mustApply(c.file, applyWithPrune(), applyWithPlanID(pl.PlanID))
			require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
			cfg := f.cfgOf(job.ID)
			require.Nil(t, cfg.Cron, "the schedule is off")
			require.Equal(t, c.oneShot, cfg.OneShot)
			require.Equal(t, commands, f.disp.count(), "staged: nothing reaches an agent")
			owned := f.owned(job.ID)
			for _, id := range []string{"config.cron.schedule", "config.cron.timezone", "config.cron.concurrency"} {
				require.NotContains(t, owned, id)
			}
			require.Equal(t, configsync.StatusNoop, f.mustApply(c.file, applyWithPrune()).Status)
		})
	}
}

func TestApplyPruneOfOneCronKeyOnlyReleasesIt(t *testing.T) {
	f := newApplyFixture(t)
	f.mustApply(applyCronHead + applyCronTable)
	job := f.svcRow(f.staging, "job")

	v2 := applyCronHead + "\n[services.job.cron]\nschedule = \"0 3 * * *\"\n"
	pl := f.plan("staging", v2, applyWithPrune())
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.Empty(t, applyRemovedFields(pl), "the schedule stays, so nothing is turned off")
	f.mustApply(v2, applyWithPrune())
	cfg := f.cfgOf(job.ID)
	require.NotNil(t, cfg.Cron)
	require.Equal(t, "0 3 * * *", cfg.Cron.Schedule)
	require.Equal(t, "Europe/Berlin", cfg.Cron.Timezone, "a released key keeps its live value")
}
