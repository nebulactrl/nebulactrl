package tasks

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// deployedCron turns the fixture's deployed service into a cron service: its
// release is a one-shot with a cron schedule, volume, file and docker options,
// and the hidden schedule task is due at 12:05.
func (f *fixture) deployedCron() (db.Service, uuid.UUID) {
	f.t.Helper()
	bg := context.Background()
	sv := f.deployed()
	cfg := services.Config{
		Image: "ghcr.io/acme/api:1", OneShot: true, StartCommand: []string{"sh", "-c", "echo tick"}, MemoryMB: 128,
		Files:  []services.FileMount{{Path: "/etc/job.conf", Content: "a = 1\n"}},
		Docker: &agentprotocol.DockerOptions{Hostname: "cronjob"},
		Cron:   &services.CronSpec{Schedule: "*/5 * * * *", TimeoutSeconds: 90},
	}.WithDefaults()
	snap, _ := json.Marshal(services.Snapshot{
		Config: cfg, ServerID: &f.server.ID,
		Volumes: []services.SnapshotVolume{{Name: "data", MountPath: "/data"}},
	})
	_, err := f.pool.Exec(bg, "UPDATE releases SET config = $2::jsonb WHERE service_id = $1", sv.ID, snap)
	require.NoError(f.t, err)
	next := time.Date(2026, 10, 26, 12, 5, 0, 0, time.UTC)
	require.NoError(f.t, f.q.UpsertServiceCronTask(bg, db.UpsertServiceCronTaskParams{
		ServiceID: sv.ID, Schedule: "*/5 * * * *", Timezone: "UTC", TimeoutSeconds: 90, Concurrency: "forbid", NextRunAt: &next,
	}))
	var taskID uuid.UUID
	require.NoError(f.t, f.pool.QueryRow(bg, "SELECT id FROM service_tasks WHERE service_id = $1 AND action = 'service'", sv.ID).Scan(&taskID))
	return sv, taskID
}

func TestCronServiceRunIsDispatchedAsAServiceRun(t *testing.T) {
	f := newFixture(t)
	sv, taskID := f.deployedCron()

	f.clock = time.Date(2026, 10, 26, 12, 5, 1, 0, time.UTC)
	require.NoError(t, f.svc.Tick(f.ctx))
	runs := f.runs(sv.ID)
	require.Len(t, runs, 1)
	require.Equal(t, SourceCron, runs[0].Source)
	require.Equal(t, StatusQueued, runs[0].Status)
	require.Equal(t, sv.Name, runs[0].Name, "a cron run is named after its service")
	require.Empty(t, runs[0].Command)

	cmd, cmdID := f.disp.last()
	require.Equal(t, agentprotocol.KindRunTask, cmd.Kind)
	var p agentprotocol.RunTaskPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.True(t, p.ServiceRun)
	require.Empty(t, p.Command)
	require.Equal(t, []string{"sh", "-c", "echo tick"}, p.StartCommand)
	require.Equal(t, 90, p.TimeoutSeconds)
	require.Equal(t, 128, p.MemoryMB)
	require.Equal(t, []agentprotocol.VolumeMount{{Name: agentprotocol.VolumeName(sv.ID.String(), "data"), MountPath: "/data"}}, p.Volumes)
	require.Equal(t, []agentprotocol.FileSpec{{Path: "/etc/job.conf", Content: "a = 1\n"}}, p.Files)
	require.Equal(t, "cronjob", p.Docker.Hostname)
	require.NotNil(t, p.DockerPolicy)
	require.False(t, p.DockerPolicy.Unrestricted)
	require.Equal(t, "ghcr.io/acme/api@sha256:abc", p.ImageRef)
	require.Contains(t, p.EnvKeys, "API_KEY")

	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeSucceeded, "", agentprotocol.RunTaskResult{
		Outcome: agentprotocol.OutcomeSucceeded, StartedAt: f.clock, FinishedAt: f.clock.Add(time.Second),
	})))
	got, err := f.svc.GetRun(f.ctx, runs[0].ID)
	require.NoError(t, err)
	require.Equal(t, StatusSucceeded, got.Status)

	// The schedule is the service's: it shows up in the cron view, not in the task list.
	views, err := services.BuildViews(f.ctx, f.q, f.box, []db.Service{sv}, f.proj.ID)
	require.NoError(t, err)
	require.NotNil(t, views[0].Cron)
	require.Equal(t, taskID, views[0].Cron.TaskID)
	require.NotNil(t, views[0].Cron.LastRun)
	require.Equal(t, StatusSucceeded, views[0].Cron.LastRun.Status)
	require.True(t, time.Date(2026, 10, 26, 12, 10, 0, 0, time.UTC).Equal(*views[0].Cron.NextRunAt))
}

func TestCronTaskIsHiddenButRunnable(t *testing.T) {
	f := newFixture(t)
	sv, taskID := f.deployedCron()

	list, err := f.svc.ListTasks(f.ctx, sv.ID)
	require.NoError(t, err)
	require.Empty(t, list, "the hidden schedule is not a task the user manages")

	enabled := false
	_, err = f.svc.UpdateTask(f.ctx, taskID, TaskPatch{Enabled: &enabled})
	requireAPIError(t, err, "task_not_found")
	requireAPIError(t, f.svc.DeleteTask(f.ctx, taskID), "task_not_found")

	run, err := f.svc.RunTaskNow(f.ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, SourceManual, run.Source)
	require.Equal(t, sv.Name, run.Name)
	cmd, _ := f.disp.last()
	var p agentprotocol.RunTaskPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.True(t, p.ServiceRun)
}

func TestCronTickIsSkippedWhilePaused(t *testing.T) {
	f := newFixture(t)
	sv, _ := f.deployedCron()

	_, err := f.pool.Exec(context.Background(), "UPDATE services SET desired_state = 'stopped' WHERE id = $1", sv.ID)
	require.NoError(t, err)
	f.clock = time.Date(2026, 10, 26, 12, 5, 1, 0, time.UTC)
	require.NoError(t, f.svc.Tick(f.ctx))
	runs := f.runs(sv.ID)
	require.Len(t, runs, 1)
	require.Equal(t, StatusSkipped, runs[0].Status)
	require.Equal(t, "the service is stopped", runs[0].Error)
	require.Empty(t, f.disp.cmds)

	// Resumed: the schedule runs again.
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET desired_state = 'running' WHERE id = $1", sv.ID)
	require.NoError(t, err)
	f.clock = time.Date(2026, 10, 26, 12, 15, 1, 0, time.UTC)
	require.NoError(t, f.svc.Tick(f.ctx))
	require.Len(t, f.disp.cmds, 1)
}
