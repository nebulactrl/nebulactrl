package registryadmin

import (
	"context"
	"testing"

	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

func (f *fixture) gcJobs() int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM river_job WHERE kind = 'instsvc_registry_gc'").Scan(&n))
	return n
}

func TestGCWindowIsScheduledOnceAndDrivenByItsWorker(t *testing.T) {
	f := newFixture(t, "http://registry.invalid")
	ctx := context.Background()
	require.NoError(t, f.b.Prepare(ctx))

	// Starting without registered jobs is an error, not a silent no-op.
	require.Error(t, f.b.StartGC(ctx))

	jm := jobs.New(f.pool, nil)
	RegisterJobs(jm, f.b)

	// The hourly check starts a window only when one is due (02:30 UTC, never collected).
	require.NoError(t, (&maintenanceWorker{b: f.b}).Work(ctx, nil))
	require.Equal(t, 1, f.gcJobs())
	// One live window: the same check again does not queue a second.
	require.NoError(t, (&maintenanceWorker{b: f.b}).Work(ctx, nil))
	require.Equal(t, 1, f.gcJobs())

	// Outside the quiet hours nothing starts.
	f.advance(10 * 60 * 60 * 1e9) // 12:30
	_, err := f.pool.Exec(ctx, "DELETE FROM river_job WHERE kind = 'instsvc_registry_gc'")
	require.NoError(t, err)
	require.NoError(t, (&maintenanceWorker{b: f.b}).Work(ctx, nil))
	require.Zero(t, f.gcJobs())

	// The window worker snoozes while the window has more to do, and the window
	// began by making the registry read-only.
	err = (&gcWorker{b: f.b}).Work(ctx, nil)
	var snooze *rivertype.JobSnoozeError
	require.ErrorAs(t, err, &snooze)
	require.Equal(t, drainGrace, snooze.Duration)
	require.True(t, f.state().ReadOnly)

	// Without a registry row the check has nothing to do.
	_, err = f.pool.Exec(ctx, "DELETE FROM instance_services WHERE kind = 'registry'")
	require.NoError(t, err)
	require.NoError(t, (&maintenanceWorker{b: f.b}).Work(ctx, nil))
}
