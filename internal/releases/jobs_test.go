package releases

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestPruneErrorsWhenTheDatabaseIsGone(t *testing.T) {
	pool := testutil.NewPool(t)
	pool.Close()

	n, err := NewService(pool, nil).Prune(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, "prune release environments")
	require.Zero(t, n)
}

func TestPruneJobClearsReleasesOutsideTheWindowAndLogsIt(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	cfg, _ := json.Marshal(services.Config{Image: "traefik/whoami:latest", Port: 80, ImageRetention: 1}.WithDefaults())
	sv, err := q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: env.ID, Name: "web", Slug: "web", Kind: services.KindApp, SourceType: services.SourceImage, Config: cfg, ServerID: &srv.ID,
	})
	require.NoError(t, err)

	base := time.Now().Add(-time.Hour)
	var rels []uuid.UUID
	for i := range 2 {
		rel, err := q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: []byte(`{}`), Config: []byte(`{}`), EnvEnc: []byte("sealed"), ImageRef: "img"})
		require.NoError(t, err)
		dep, err := q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &srv.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
		require.NoError(t, err)
		_, err = pool.Exec(bg, `UPDATE deployments SET status = 'succeeded', finished_at = $2 WHERE id = $1`, dep.ID, base.Add(time.Duration(i)*time.Minute))
		require.NoError(t, err)
		if i == 1 {
			require.NoError(t, q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID, AppliedGeneration: sv.Generation}))
		}
		rels = append(rels, rel.ID)
	}
	hasEnv := func(id uuid.UUID) bool {
		var n int
		require.NoError(t, pool.QueryRow(bg, `SELECT count(*) FROM releases WHERE id = $1 AND env_enc IS NOT NULL`, id).Scan(&n))
		return n == 1
	}

	logs := &syncBuffer{}
	svc := NewService(pool, slog.New(slog.NewTextHandler(logs, nil)))
	jm := jobs.New(pool, nil)
	RegisterJobs(jm, svc)
	require.NoError(t, jm.Migrate(bg))
	require.NoError(t, jm.Start(bg))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, jm.Stop(stopCtx))
	})

	require.Equal(t, "release_env_prune", PruneArgs{}.Kind())
	require.NoError(t, jm.Insert(bg, PruneArgs{}, nil))

	require.Eventually(t, func() bool {
		var n int
		err := pool.QueryRow(bg, `SELECT count(*) FROM releases WHERE id = $1 AND env_enc IS NULL`, rels[0]).Scan(&n)
		return err == nil && n == 1
	}, 15*time.Second, 50*time.Millisecond)
	require.True(t, hasEnv(rels[1]), "the running release keeps its environment")
	require.Contains(t, logs.String(), "pruned release environments outside the rollback window")
}
