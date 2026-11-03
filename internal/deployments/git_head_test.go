package deployments

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// gitService inserts a deployed git app that runs branch main.
func (f *routeFixture) gitService(slug string) db.Service {
	f.t.Helper()
	bg := context.Background()
	cfg := services.Config{GitURL: "https://git.example.com/acme/shop.git", GitBranch: "main", Port: 8080}.WithDefaults()
	raw, _ := json.Marshal(cfg)
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceGit, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	snap, _ := json.Marshal(services.Snapshot{Config: cfg, ServerID: &f.server.ID})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceGit, GitURL: cfg.GitURL, Branch: "main", CommitSHA: strings.Repeat("a", 40)})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: src, Config: snap, ImageRef: "x"})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID, AppliedGeneration: sv.Generation}))
	return sv
}

func TestResolveHeadReadsTheConfiguredBranch(t *testing.T) {
	f := newRouteFixture(t)
	sv := f.gitService("api")
	var gotURL, gotBranch string
	f.deps.SetGitResolver(func(_ context.Context, url, branch, _, _ string) (string, error) {
		gotURL, gotBranch = url, branch
		return strings.ToUpper(strings.Repeat("b", 40)), nil
	})

	sha, branch, err := f.deps.ResolveHead(context.Background(), sv.ID)
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("b", 40), sha, "the commit is lower case")
	require.Equal(t, "main", branch)
	require.Equal(t, "https://git.example.com/acme/shop.git", gotURL)
	require.Equal(t, "main", gotBranch)

	// A production service with a staged branch switch is read as a push deploys it: with the branch it runs.
	_, err = f.pool.Exec(context.Background(),
		`UPDATE services SET config = jsonb_set(config, '{gitBranch}', '"next"'), generation = generation + 1 WHERE id = $1`, sv.ID)
	require.NoError(t, err)
	_, branch, err = f.deps.ResolveHead(context.Background(), sv.ID)
	require.NoError(t, err)
	require.Equal(t, "main", branch)
	require.Equal(t, "main", gotBranch)
}

func TestResolveHeadReportsAnUnreachableRemote(t *testing.T) {
	f := newRouteFixture(t)
	sv := f.gitService("api")
	f.deps.SetGitResolver(func(context.Context, string, string, string, string) (string, error) {
		return "", context.DeadlineExceeded
	})
	_, _, err := f.deps.ResolveHead(context.Background(), sv.ID)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestResolveHeadRefusesServicesWithoutAGitSource(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("img")
	f.deps.SetGitResolver(func(context.Context, string, string, string, string) (string, error) {
		t.Fatal("an image service has no remote to read")
		return "", nil
	})
	_, _, err := f.deps.ResolveHead(context.Background(), web.ID)
	ae, ok := apierr.As(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, "git_poll_not_applicable", ae.Code)
}
