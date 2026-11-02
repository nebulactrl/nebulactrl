package platform

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

func TestProvisionWorkerAdvancesOneStepAndSnoozes(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	row, err := q.InsertPlatformService(bg, db.InsertPlatformServiceParams{
		WorkspaceID: ws.ID, Kind: KindHeadscale, ServiceIds: []uuid.UUID{}, Hostname: "hs.example.test", PublicUrl: "https://hs.example.test",
		Options: []byte(`{}`), ID: uuid.New(),
	})
	require.NoError(t, err)
	require.Equal(t, StatusProvisioning, row.Status)
	svc := NewService(Deps{Pool: pool, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	w := &provisionWorker{svc: svc}
	job := func(id uuid.UUID) *river.Job[ProvisionArgs] {
		return &river.Job[ProvisionArgs]{Args: ProvisionArgs{ID: id}}
	}

	// A service that was removed ends its job.
	require.NoError(t, w.Work(bg, job(uuid.New())))

	// A step that is not finished asks River to come back; the next one starts from there.
	var snooze *river.JobSnoozeError
	require.ErrorAs(t, w.Work(bg, job(row.ID)), &snooze)
	cur, err := q.GetPlatformServiceInternal(bg, row.ID)
	require.NoError(t, err)
	require.Equal(t, StepDeploy, cur.Step)

	// Without anything to deploy to, the deploy step fails the service with a message for the user
	// (the job ends: retrying would not help).
	svc.Deploy = noDeploy{}
	_, err = pool.Exec(bg, `UPDATE platform_services SET step = 'deploy', status = 'provisioning' WHERE id = $1`, row.ID)
	require.NoError(t, err)
	require.NoError(t, w.Work(bg, job(row.ID)))
	cur, err = q.GetPlatformServiceInternal(bg, row.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, cur.Status)
	require.Equal(t, "The platform environment no longer exists.", cur.StatusMessage)

	// A failed service is not touched again.
	require.NoError(t, w.Work(bg, job(row.ID)))
}

type noDeploy struct{ Deployer }

func TestJobErrorsAndUserMessages(t *testing.T) {
	require.Equal(t, "try later", retryLater("try %s", "later").Error())
	require.Equal(t, "no server", fatalf("no %s", "server").Error())
	var re *retryError
	require.ErrorAs(t, retryLater("x"), &re)

	require.Equal(t, apierr.Unprocessable("bad", "nope").Error(), userMessage(apierr.Unprocessable("bad", "nope")))
	require.NotEmpty(t, userMessage(apierr.Conflict("busy", "a deployment is running")))
	require.Empty(t, userMessage(apierr.New(500, "internal", "boom")), "server errors are not shown to the user")
	require.Empty(t, userMessage(errors.New("connection reset")))
	require.Empty(t, userMessage(nil))
}

func TestMigrationWorkerHasATimeoutAndNothingToMigrate(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := NewService(Deps{Pool: pool, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	w := &migrateWorker{svc: svc}
	require.Equal(t, 2*time.Minute, w.Timeout(&river.Job[HeadscalePolicyMigrateArgs]{}))
	require.NoError(t, w.Work(context.Background(), &river.Job[HeadscalePolicyMigrateArgs]{}), "no ready Headscale: nothing to do")
	require.Equal(t, "mesh_headscale_policy_migrate", HeadscalePolicyMigrateArgs{}.Kind())
}

func TestCleanOutputAndLastLine(t *testing.T) {
	require.Equal(t, "ok\nnext", cleanOutput("\x1b[32mok\x1b[0m\r\nnext\r"))
	require.Equal(t, "last", lastLine("first\n\nlast\n"))
	require.Equal(t, "only", lastLine("  only  "))
	require.Empty(t, lastLine(""))
	require.Equal(t, "short", shorten(errors.New("short")))
	long := shorten(errors.New(string(make([]byte, 300))))
	require.Len(t, []rune(long), 201)
}
