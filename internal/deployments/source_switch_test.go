package deployments

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// stageGit moves a deployed image service to a git repository.
func (f *buildFixture) stageGit(sv db.Service) services.ServiceView {
	f.t.Helper()
	cur, err := f.svcs.Get(f.ctx, sv.ID)
	require.NoError(f.t, err)
	require.False(f.t, cur.HasStagedChanges)
	v, err := f.svcs.StageSourceChange(f.ctx, sv.ID, cur.Generation, services.SourceChangeInput{
		SourceType: services.SourceGit, GitURL: "git://127.0.0.1:1/app.git", GitBranch: "main", BuildType: "railpack",
	})
	require.NoError(f.t, err)
	return v
}

// The plan of a deployed image service that moves to a git repository shows one
// staged source change and a build.
func TestPlanShowsAStagedSourceSwitch(t *testing.T) {
	f := newBuildFixture(t)
	web := f.web("web")
	staged := f.stageGit(web)
	require.Equal(t, services.SourceGit, staged.SourceType)
	require.True(t, staged.HasStagedChanges)
	require.Equal(t, 1, staged.PendingChangeCounts.Settings, "one staged setting: the source")

	plan, err := f.deps.Plan(f.ctx, web.ID)
	require.NoError(t, err)
	require.Equal(t, services.SourceGit, plan.Source.Type)
	require.Equal(t, "git://127.0.0.1:1/app.git", plan.Source.GitURL)
	require.Equal(t, "main", plan.Source.Branch)
	require.NotNil(t, plan.Placement, "a git source builds before it starts")
	require.Len(t, plan.Changes, 1, "%+v", plan.Changes)
	require.Equal(t, services.FieldSource, plan.Changes[0].Field)
	require.Equal(t, "image", plan.Changes[0].From)
	require.Equal(t, "Git (127.0.0.1:1/app, main, Railpack)", plan.Changes[0].To)

	pending, err := f.deps.PendingChanges(f.ctx, web.ID)
	require.NoError(t, err)
	require.Equal(t, plan.Changes, pending)
}

// The next deployment of the switched service builds the repository and records
// a git release; the old releases keep their image source.
func TestDeployAfterASourceSwitchBuildsTheRepository(t *testing.T) {
	f := newBuildFixture(t)
	web := f.web("web")
	f.stageGit(web)

	depID, err := f.deployAt(web, sha1)
	require.NoError(t, err)
	f.buildCmd(depID) // fails the test when no build was queued

	var src []byte
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT r.source FROM releases r JOIN deployments d ON d.release_id = r.id WHERE d.id = $1", depID).Scan(&src))
	rs := services.DecodeSource(src)
	require.Equal(t, services.SourceGit, rs.Type)
	require.Equal(t, "git://127.0.0.1:1/app.git", rs.GitURL)
	require.Equal(t, "main", rs.Branch)
	require.Equal(t, sha1, rs.CommitSHA)

	var oldType string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT r.source->>'type' FROM releases r JOIN deployments d ON d.release_id = r.id WHERE d.service_id = $1 AND d.id <> $2 ORDER BY d.created_at LIMIT 1", web.ID, depID).Scan(&oldType))
	require.Equal(t, services.SourceImage, oldType, "history keeps the old source")
}

func TestDiscardingASourceSwitchRestoresTheReleasesImage(t *testing.T) {
	f := newBuildFixture(t)
	web := f.web("web")
	f.stageGit(web)

	v, err := f.deps.DiscardPendingChange(f.ctx, web.ID, services.FieldSource)
	require.NoError(t, err)
	require.Equal(t, services.SourceImage, v.SourceType)
	require.Equal(t, "traefik/whoami:latest", v.Config.Image)
	require.Empty(t, v.Config.GitURL)
	require.Empty(t, v.Config.GitBranch)
	require.Equal(t, "auto", v.Config.BuildType)
	require.Equal(t, 80, v.Config.Port, "the rest of the config stays")
	pending, err := f.deps.PendingChanges(f.ctx, web.ID)
	require.NoError(t, err)
	require.Empty(t, pending)

	// Nothing is pending any more: the second discard is a 404.
	_, err = f.deps.DiscardPendingChange(f.ctx, web.ID, services.FieldSource)
	ae, ok := apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, 404, ae.Status)
}
