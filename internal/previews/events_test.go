package previews_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/previews"
)

func (f *fixture) jobs(kind string) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM river_job WHERE kind = $1", kind).Scan(&n))
	return n
}

func (f *fixture) enableLean() {
	f.t.Helper()
	f.enable(func(s *previews.PreviewSettings) { s.ExcludedServices = []string{"web", "worker", "postgres"} })
}

func TestHandlePullRequestEnqueuesAJobOnlyForPRActions(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	for _, action := range []string{"labeled", "edited", "assigned", ""} {
		f.pv.HandlePullRequest(bg, f.pr(5, action, "5555555555555555555555555555555555555555"))
	}
	require.Zero(t, f.jobs("preview_pr_event"), "events that change nothing are dropped before the queue")

	for _, action := range []string{previews.ActionOpened, previews.ActionSynchronize, previews.ActionReopened, previews.ActionClosed, previews.ActionMerged} {
		f.pv.HandlePullRequest(bg, f.pr(5, action, "5555555555555555555555555555555555555555"))
	}
	require.Equal(t, 5, f.jobs("preview_pr_event"))
	var attempts int
	var number float64
	require.NoError(t, f.pool.QueryRow(bg, "SELECT max_attempts, (args->'event'->>'number')::float FROM river_job WHERE kind = 'preview_pr_event' LIMIT 1").Scan(&attempts, &number))
	require.Equal(t, 8, attempts, "a webhook is retried for a while")
	require.EqualValues(t, 5, number)
	require.Empty(t, f.disp.kinds(), "enqueueing does no work itself")
}

func TestPullRequestIsIgnoredWithoutAMatchingEnabledProject(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	noPreview := func(n int) {
		t.Helper()
		_, err := f.q.GetPreviewByPR(bg, db.GetPreviewByPRParams{ProjectID: f.project.ID, ConnectionID: f.conn, Repo: "acme/shop", PrNumber: int32(n)})
		require.ErrorIs(t, err, pgx.ErrNoRows)
	}

	// Previews are off by default.
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(60, "opened", "6060606060606060606060606060606060606060")))
	noPreview(60)

	// No service of the project builds this repository.
	f.enableLean()
	f.git.services = nil
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(61, "opened", "6161616161616161616161616161616161616161")))
	noPreview(61)
	require.Zero(t, f.count("SELECT count(*) FROM environments WHERE kind = 'preview'"))
}

func (f *fixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func TestClosingAndMergingDeleteThePreviewAndAReopenStartsOver(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.enableLean()

	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(51, "opened", "5151515151515151515151515151515151515151")))
	pv := f.previewOf(51)
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(51, "merged", "5151515151515151515151515151515151515151")))
	got := f.previewOf(51)
	require.Equal(t, previews.StatusDeleting, got.Status)
	require.Equal(t, "Deleting: the pull request was merged.", got.StatusMessage)
	require.Equal(t, 1, f.count("SELECT count(*) FROM river_job WHERE kind = 'preview_delete' AND args->>'reason' = 'merged' AND args->>'previewId' = $1", pv.ID.String()))

	// A second event does not queue a second teardown; a push while it runs is retried later.
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(51, "closed", "5151515151515151515151515151515151515151")))
	require.Equal(t, 1, f.jobs("preview_delete"))
	err := f.pv.ProcessPullRequest(bg, f.pr(51, "synchronize", "5252525252525252525252525252525252525252"))
	require.ErrorContains(t, err, "still being deleted")

	// Closing an unknown PR is not an error.
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(999, "closed", "9999999999999999999999999999999999999999")))

	retry, err := f.pv.RunDeleteForTest(bg, pv.ID, "merged")
	require.NoError(t, err)
	require.False(t, retry)
	_, err = f.q.GetPreview(bg, pv.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.Contains(t, f.git.onlyComment(), "deleted")
	retry, err = f.pv.RunDeleteForTest(bg, pv.ID, "merged")
	require.NoError(t, err, "tearing down a preview that is gone is a no-op")
	require.False(t, retry)

	// Reopening the PR creates a fresh preview.
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(51, "reopened", "5353535353535353535353535353535353535353")))
	again := f.previewOf(51)
	require.NotEqual(t, pv.ID, again.ID)
	require.Equal(t, previews.StatusDeploying, again.Status)
	require.Equal(t, "5353535353535353535353535353535353535353", again.HeadSha)
}

func TestPushUpdatesTheTitleAndKeepsItWhenTheEventHasNone(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.enableLean()
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(52, "opened", "1111111111111111111111111111111111111152")))
	f.runToReady(52)

	renamed := f.pr(52, "synchronize", "2222222222222222222222222222222222222252")
	renamed.Title = "Rework checkout"
	renamed.HeadBranch = "rework"
	require.NoError(t, f.pv.ProcessPullRequest(bg, renamed))
	pv := f.previewOf(52)
	require.Equal(t, "Rework checkout", pv.PrTitle)
	require.Equal(t, "rework", pv.HeadBranch)
	env, err := f.q.GetEnvironmentScoped(bg, db.GetEnvironmentScopedParams{ID: pv.EnvironmentID, WorkspaceID: f.ws.ID})
	require.NoError(t, err)
	require.Equal(t, "PR #52 · Rework checkout", env.Name)

	blank := f.pr(52, "synchronize", "3333333333333333333333333333333333333352")
	blank.Title, blank.HeadBranch = "", ""
	require.NoError(t, f.pv.ProcessPullRequest(bg, blank))
	pv = f.previewOf(52)
	require.Equal(t, "Rework checkout", pv.PrTitle, "a missing title keeps the stored one")
	require.Equal(t, "rework", pv.HeadBranch)
	require.Equal(t, "3333333333333333333333333333333333333352", pv.HeadSha)
}

func TestTeardownWaitsForARunningDeployment(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.enableLean()
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(53, "opened", "5353535353535353535353535353535353535300")))
	pv := f.previewOf(53)
	require.False(t, f.step(pv.ID), "the wave started the api deployment")
	api := f.envServices(pv.EnvironmentID)["api"]

	// The deployment is still active: the project-delete path refuses, the job snoozes.
	err := f.pv.Teardown(f.ctx, pv.ID)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "deployment_in_progress", ae.Code)
	err = f.pv.WorkDeleteForTest(bg, pv.ID, "manual")
	var snooze *rivertype.JobSnoozeError
	require.ErrorAs(t, err, &snooze, "River retries a teardown that must wait")
	require.Equal(t, 5*time.Second, snooze.Duration)
	_, err = f.q.GetPreview(bg, pv.ID)
	require.NoError(t, err, "nothing is removed while a deployment runs")

	f.finish(api.ID, "failed")
	require.NoError(t, f.pv.Teardown(f.ctx, pv.ID))
	_, err = f.q.GetPreview(bg, pv.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
}

func TestWorkersReportWhatRiverNeedsToKnow(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.enableLean()

	// The event worker applies the PR.
	require.NoError(t, f.pv.WorkPRForTest(bg, f.pr(54, "opened", "5454545454545454545454545454545454545454")))
	pv := f.previewOf(54)

	// A wave that has more to do snoozes the job; a finished or unknown one completes.
	err := f.pv.WorkWaveForTest(bg, pv.ID)
	var snooze *rivertype.JobSnoozeError
	require.ErrorAs(t, err, &snooze)
	require.Positive(t, snooze.Duration)
	require.NoError(t, f.pv.WorkWaveForTest(bg, uuid.New()))
	pv = f.runToReady(54)
	require.Equal(t, previews.StatusReady, pv.Status)
	require.NoError(t, f.pv.WorkWaveForTest(bg, pv.ID), "a finished wave ends the job")

	// Publishing posts the sticky comment and the commit status.
	require.NoError(t, f.pv.WorkPublishForTest(bg, pv.ID))
	require.NotEmpty(t, f.git.onlyComment())
	require.NotEmpty(t, f.git.statuses)

	// The sweep worker deletes idle previews.
	_, err = f.pool.Exec(bg, "UPDATE previews SET last_push_at = now() - interval '30 days' WHERE id = $1", pv.ID)
	require.NoError(t, err)
	require.NoError(t, f.pv.WorkSweepForTest(bg))
	require.Equal(t, previews.StatusDeleting, f.previewOf(54).Status)
	require.NoError(t, f.pv.WorkDeleteForTest(bg, pv.ID, "idle"), "a finished teardown completes the job")
	_, err = f.q.GetPreview(bg, pv.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
}
