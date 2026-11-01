package releases

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

func TestPruneKeepsTheRollbackWindow(t *testing.T) {
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

	base := time.Now().Add(-24 * time.Hour)
	seed := func(slug string, retention, n int) (db.Service, []uuid.UUID) {
		cfg, _ := json.Marshal(services.Config{Image: "traefik/whoami:latest", Port: 80, ImageRetention: retention}.WithDefaults())
		sv, err := q.InsertService(bg, db.InsertServiceParams{
			ID: uuid.New(), EnvironmentID: env.ID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceImage, Config: cfg, ServerID: &srv.ID,
		})
		require.NoError(t, err)
		var rels []uuid.UUID
		for i := range n {
			rel, err := q.InsertRelease(bg, db.InsertReleaseParams{
				ID: uuid.New(), ServiceID: sv.ID, Source: []byte(`{}`), Config: []byte(`{}`), EnvEnc: []byte(fmt.Sprintf("sealed-%s-%d", slug, i)), ImageRef: "img",
			})
			require.NoError(t, err)
			dep, err := q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &srv.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
			require.NoError(t, err)
			_, err = pool.Exec(bg, `UPDATE deployments SET status = 'succeeded', finished_at = $2 WHERE id = $1`, dep.ID, base.Add(time.Duration(i)*time.Minute))
			require.NoError(t, err)
			if i == n-1 {
				require.NoError(t, q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID, AppliedGeneration: sv.Generation}))
			}
			rels = append(rels, rel.ID)
		}
		return sv, rels
	}
	hasEnv := func(id uuid.UUID) bool {
		var n int
		require.NoError(t, pool.QueryRow(bg, `SELECT count(*) FROM releases WHERE id = $1 AND env_enc IS NOT NULL`, id).Scan(&n))
		return n == 1
	}

	_, small := seed("small", 2, 5)     // window of 2
	_, deflt := seed("default", 0, 4)   // default retention (5): nothing to prune
	one, single := seed("single", 1, 3) // window of 1: only the running release

	// A failed deployment's release and an in-flight deployment's release.
	failedRel, err := q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: one.ID, Source: []byte(`{}`), Config: []byte(`{}`), EnvEnc: []byte("sealed-failed")})
	require.NoError(t, err)
	failedDep, err := q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: one.ID, ReleaseID: &failedRel.ID, ServerID: &srv.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, `UPDATE deployments SET status = 'failed', finished_at = now() WHERE id = $1`, failedDep.ID)
	require.NoError(t, err)
	inflightRel, err := q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: one.ID, Source: []byte(`{}`), Config: []byte(`{}`), EnvEnc: []byte("sealed-inflight")})
	require.NoError(t, err)
	_, err = q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: one.ID, ReleaseID: &inflightRel.ID, ServerID: &srv.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
	require.NoError(t, err)

	svc := NewService(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	n, err := svc.Prune(bg)
	require.NoError(t, err)
	// small: 3 oldest; single: 2 oldest + the failed one.
	require.EqualValues(t, 3+2+1, n)

	for i, id := range small {
		require.Equal(t, i >= 3, hasEnv(id), "small release %d", i)
	}
	for i, id := range deflt {
		require.True(t, hasEnv(id), "default release %d is inside the window", i)
	}
	for i, id := range single {
		require.Equal(t, i == 2, hasEnv(id), "single release %d", i)
	}
	require.False(t, hasEnv(failedRel.ID), "a failed deployment's release is not rollback-eligible")
	require.True(t, hasEnv(inflightRel.ID), "a release in flight is kept")

	// Idempotent.
	n, err = svc.Prune(bg)
	require.NoError(t, err)
	require.Zero(t, n)
}
