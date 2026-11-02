package previews_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/previews"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

func TestPushDuringARunningDeploymentWaitsAndThenDeploysTheNewCommit(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.enableLean()
	const first, second = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1", "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"

	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(70, "opened", first)))
	pv := f.previewOf(70)
	require.False(t, f.step(pv.ID))
	api := f.envServices(pv.EnvironmentID)["api"]
	require.Len(t, f.deployer.callsFor(api.ID), 1)

	// A push arrives while the first deployment still runs: the new wave cannot
	// start a second deployment yet.
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(70, "synchronize", second)))
	require.False(t, f.step(pv.ID), "the wave waits for the running deployment")
	require.Len(t, f.deployer.callsFor(api.ID), 1)
	require.Equal(t, previews.StatusDeploying, f.previewOf(70).Status)

	f.finish(api.ID, "succeeded")
	require.False(t, f.step(pv.ID))
	calls := f.deployer.callsFor(api.ID)
	require.Len(t, calls, 2)
	require.Equal(t, second, calls[1].SHA, "the second deployment builds the pushed commit")
	f.finish(api.ID, "succeeded")
	require.True(t, f.step(pv.ID))
	require.Equal(t, previews.StatusReady, f.previewOf(70).Status)
}

func TestDeployErrorFailsThePreviewWithTheReason(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.enableLean()
	f.deployer.failWith = apierr.Unprocessable("invalid_config", "the image could not be resolved")
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(71, "opened", "7171717171717171717171717171717171717171")))
	pv := f.previewOf(71)
	require.True(t, f.step(pv.ID), "nothing is running, so the wave ends")
	got := f.previewOf(71)
	require.Equal(t, previews.StatusFailed, got.Status)
	require.Equal(t, "api could not be deployed: the image could not be resolved", got.StatusMessage)
	require.Empty(t, f.deployer.calls)
}

func TestSeedCommandOfAServiceRemovedFromThePreviewIsSkipped(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.enable(func(s *previews.PreviewSettings) {
		s.ExcludedServices = []string{"web", "postgres", "api"}
		s.SeedCommands = []previews.PreviewSeedCommand{{Service: "worker", Command: "run-seed"}}
	})
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(72, "opened", "7272727272727272727272727272727272727272")))
	pv := f.previewOf(72)
	require.Contains(t, f.envServices(pv.EnvironmentID), "worker")
	_, err := f.pool.Exec(bg, "UPDATE services SET deleted_at = now() WHERE environment_id = $1 AND slug = 'worker'", pv.EnvironmentID)
	require.NoError(t, err)
	pv = f.runToReady(72)
	require.Equal(t, previews.StatusReady, pv.Status)
	require.Empty(t, f.seeder.runs, "the service is gone from the preview, so there is nothing to run the seed in")
}

func TestCloneKeepsLinksAndEnvironmentSharedVariables(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	_, err := f.q.InsertLink(bg, db.InsertLinkParams{EnvironmentID: f.base.ID, FromServiceID: f.api.ID, ToServiceID: f.postgres.ID})
	require.NoError(t, err)
	// A link to a service the preview leaves out is not cloned.
	_, err = f.q.InsertLink(bg, db.InsertLinkParams{EnvironmentID: f.base.ID, FromServiceID: f.api.ID, ToServiceID: f.web.ID})
	require.NoError(t, err)
	for _, sv := range []struct {
		key    string
		secret bool
	}{{"STRIPE", true}, {"REGION", false}} {
		id := uuid.New()
		enc, err := f.box.SealString(secretbox.SharedVariablesValue, id.String(), "shared-"+sv.key)
		require.NoError(t, err)
		_, err = f.q.InsertSharedVariable(bg, db.InsertSharedVariableParams{
			ID: id, WorkspaceID: f.ws.ID, ProjectID: &f.project.ID, EnvironmentID: &f.base.ID, Key: sv.key, ValueEnc: enc, IsSecret: sv.secret,
		})
		require.NoError(t, err)
	}
	f.enable(func(s *previews.PreviewSettings) { s.ExcludedServices = []string{"web", "worker"} })

	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(73, "opened", "7373737373737373737373737373737373737373")))
	pv := f.previewOf(73)
	svcs := f.envServices(pv.EnvironmentID)
	require.Len(t, svcs, 2)

	var links int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM service_links WHERE environment_id = $1 AND from_service_id = $2 AND to_service_id = $3",
		pv.EnvironmentID, svcs["api"].ID, svcs["postgres"].ID).Scan(&links))
	require.Equal(t, 1, links, "the link follows the cloned services")
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM service_links WHERE environment_id = $1", pv.EnvironmentID).Scan(&links))
	require.Equal(t, 1, links, "the link to the excluded service is dropped")

	shared, err := f.q.ListEnvironmentSharedVariablesRaw(bg, &pv.EnvironmentID)
	require.NoError(t, err)
	require.Len(t, shared, 2)
	got := map[string]string{}
	secret := map[string]bool{}
	for _, v := range shared {
		plain, err := f.box.OpenString(secretbox.SharedVariablesValue, v.ID.String(), v.ValueEnc)
		require.NoError(t, err, "the copy is sealed for its own row")
		got[v.Key] = plain
		secret[v.Key] = v.IsSecret
	}
	require.Equal(t, map[string]string{"STRIPE": "shared-STRIPE", "REGION": "shared-REGION"}, got, "a same-repository PR inherits the secret too")
	require.True(t, secret["STRIPE"])
	require.False(t, secret["REGION"])

	// The base environment keeps its own rows.
	base, err := f.q.ListEnvironmentSharedVariablesRaw(bg, &f.base.ID)
	require.NoError(t, err)
	require.Len(t, base, 2)
}
