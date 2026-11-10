package gitproviders_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

const (
	pollRunning = "1111111111111111111111111111111111111111"
	pollHead    = "2222222222222222222222222222222222222222"
	pollNewer   = "3333333333333333333333333333333333333333"
)

// deployedAt gives the service a running release built from commit sha.
func (f *fixture) deployedAt(sv services.ServiceView, sha string) {
	f.t.Helper()
	bg := context.Background()
	row, err := f.q.GetServiceScopeInternal(bg, sv.ID)
	require.NoError(f.t, err)
	snap, _ := json.Marshal(services.Snapshot{Config: services.DecodeConfig(row.Service.Config), ServerID: &f.server.ID})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceGit, Branch: "main", CommitSHA: sha})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: src, Config: snap, ImageRef: "x"})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID, AppliedGeneration: row.Service.Generation}))
}

// deploymentOf records a deployment of commit sha with the given status, the way a real one is written.
func (f *fixture) deploymentOf(serviceID uuid.UUID, sha, status string) {
	f.t.Helper()
	bg := context.Background()
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: serviceID, ServerID: &f.server.ID, Trigger: "git_push", ActorType: "system", Strategy: "rolling"})
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.SetDeploymentCommit(bg, db.SetDeploymentCommitParams{ID: dep.ID, CommitSha: sha, CommitBranch: "main"}))
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = $2 WHERE id = $1", dep.ID, status)
	require.NoError(f.t, err)
}

// pollRig is a deployed git service (running pollRunning) whose remote head and clock the test controls.
type pollRig struct {
	*fixture
	id  uuid.UUID
	now time.Time
}

func newPollRig(t *testing.T) *pollRig {
	t.Helper()
	f := newFixture(t)
	sv := f.gitService("api")
	f.deployedAt(sv, pollRunning)
	f.dep.setHead(pollRunning, "main", nil)
	r := &pollRig{fixture: f, id: sv.ID, now: time.Now().UTC().Truncate(time.Second)}
	f.svc.SetNow(func() time.Time { return r.now })
	return r
}

func (r *pollRig) put(minutes int, deploy bool) services.GitPollView {
	r.t.Helper()
	v, err := r.svc.PutGitPoll(r.ctx, r.id, gitproviders.GitPollInput{Enabled: true, IntervalMinutes: &minutes, Deploy: &deploy})
	require.NoError(r.t, err)
	return v
}

// sweep moves the clock and runs one sweep.
func (r *pollRig) sweep(d time.Duration) {
	r.t.Helper()
	r.now = r.now.Add(d)
	require.NoError(r.t, r.svc.Sweep(context.Background()))
}

func (r *pollRig) view() services.GitPollView {
	r.t.Helper()
	v, err := r.svc.GetGitPoll(r.ctx, r.id)
	require.NoError(r.t, err)
	return v
}

func (r *pollRig) setConfig(cfg services.Config) {
	r.t.Helper()
	raw, _ := json.Marshal(cfg)
	_, err := r.pool.Exec(context.Background(), "UPDATE services SET config = $2 WHERE id = $1", r.id, raw)
	require.NoError(r.t, err)
}

func TestPollHeadUnchangedDoesNotDeploy(t *testing.T) {
	r := newPollRig(t)
	require.False(t, r.view().Enabled)
	v := r.put(5, true)
	require.True(t, v.Enabled)
	require.Nil(t, v.CheckedAt, "enabling checks on the next sweep, not at once")

	r.sweep(0)
	v = r.view()
	require.NotNil(t, v.CheckedAt)
	require.Equal(t, pollRunning, v.HeadSha)
	require.Equal(t, "main", v.HeadBranch)
	require.False(t, v.UpdateAvailable)
	require.Empty(t, v.Error)
	require.Empty(t, r.dep.snapshot())
}

func TestPollNewHeadDeploysExactlyOnce(t *testing.T) {
	r := newPollRig(t)
	r.put(5, true)
	r.dep.setHead(pollHead, "main", nil)

	r.sweep(0)
	calls := r.dep.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, r.id, calls[0].ServiceID)
	require.Equal(t, "git_push", calls[0].Trigger)
	require.Equal(t, pollHead, calls[0].Commit.SHA)
	require.Equal(t, "main", calls[0].Commit.Branch)
	require.Equal(t, "New commit found by checking main every 5 min", calls[0].Message)
	require.True(t, r.view().UpdateAvailable, "until the deployment finishes the running commit is the old one")

	// The deployment the real deployer would have created exists now: the next check leaves it alone.
	r.deploymentOf(r.id, pollHead, "building")
	before := r.dep.headCalls
	r.sweep(6 * time.Minute)
	require.Greater(t, r.dep.headCalls, before, "the next check ran")
	require.Len(t, r.dep.snapshot(), 1)

	// A newer commit deploys once that deployment has finished, one deployment per commit.
	_, err := r.pool.Exec(context.Background(), "UPDATE deployments SET status = 'succeeded' WHERE status = 'building'")
	require.NoError(t, err)
	r.dep.setHead(pollNewer, "main", nil)
	r.sweep(6 * time.Minute)
	calls = r.dep.snapshot()
	require.Len(t, calls, 2)
	require.Equal(t, pollNewer, calls[1].Commit.SHA)
}

func TestPollIntervalWaitsBetweenChecks(t *testing.T) {
	r := newPollRig(t)
	r.put(5, true)
	r.sweep(0)
	n := r.dep.headCalls
	r.sweep(3 * time.Minute)
	require.Equal(t, n, r.dep.headCalls, "not due after 3 of 5 minutes")
	r.sweep(3 * time.Minute) // 6 minutes: past 5 minutes plus 10 % jitter
	require.Equal(t, n+1, r.dep.headCalls)
}

func TestPollBusyLinkedServiceQueuesTheCommit(t *testing.T) {
	r := newPollRig(t)
	conn := r.giteaConnection()
	r.giteaRepo("main")
	r.link(r.id, conn.Connection.ID, gitproviders.LinkInput{})
	r.dep.fail = func(n int, _ depCall) error {
		if n == 0 {
			return apierr.Conflict("deployment_in_progress", "a deployment is already in progress for this service")
		}
		return nil
	}
	r.put(5, true)
	r.dep.setHead(pollHead, "main", nil)
	r.deploymentOf(r.id, "", "building") // a deployment is running when the commit is found

	r.sweep(0)
	require.Empty(t, r.dep.snapshot())
	l, err := r.svc.GetLink(r.ctx, r.id)
	require.NoError(t, err)
	require.Equal(t, pollHead, l.QueuedCommit, "the commit waits for its turn")

	_, err = r.pool.Exec(context.Background(), "UPDATE deployments SET status = 'succeeded' WHERE status = 'building'")
	require.NoError(t, err)
	r.sweep(10 * time.Second)
	calls := r.dep.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, pollHead, calls[0].Commit.SHA)
	require.Equal(t, "New commit found by checking main every 5 min", calls[0].Message, "the queued commit keeps how it was found")
}

func TestPollBusyUnlinkedServiceLooksAgainSoon(t *testing.T) {
	r := newPollRig(t)
	r.put(60, true)
	r.dep.setHead(pollHead, "main", nil)
	r.deploymentOf(r.id, "", "building") // a deployment is running

	r.sweep(0)
	require.Empty(t, r.dep.snapshot())
	_, err := r.pool.Exec(context.Background(), "UPDATE deployments SET status = 'succeeded' WHERE status = 'building'")
	require.NoError(t, err)
	r.sweep(2 * time.Minute) // far before the hour
	require.Len(t, r.dep.snapshot(), 1)
	require.Equal(t, pollHead, r.dep.snapshot()[0].Commit.SHA)
}

func TestPollNeverRetriesAFailedCommit(t *testing.T) {
	r := newPollRig(t)
	r.put(5, true)
	r.dep.setHead(pollHead, "main", nil)
	r.deploymentOf(r.id, pollHead, "failed")

	r.sweep(0)
	r.sweep(6 * time.Minute)
	require.Empty(t, r.dep.snapshot())
	require.True(t, r.view().UpdateAvailable)
}

func TestPollDoesNotUndoARollback(t *testing.T) {
	r := newPollRig(t)
	r.deploymentOf(r.id, pollHead, "succeeded") // deployed after the running release, then rolled back from
	r.put(5, true)
	r.dep.setHead(pollHead, "main", nil)
	r.sweep(0)
	require.Empty(t, r.dep.snapshot())
}

func TestPollDeployOffOnlyShowsTheCommit(t *testing.T) {
	r := newPollRig(t)
	r.put(5, false)
	r.dep.setHead(pollHead, "main", nil)
	r.sweep(0)
	require.Empty(t, r.dep.snapshot())
	v := r.view()
	require.True(t, v.UpdateAvailable)
	require.False(t, v.Deploy)
	require.Equal(t, pollHead, v.HeadSha)
}

func TestPollNewHeadRunsThePushHookOnce(t *testing.T) {
	// Without a webhook the poll is the only thing that sees a new commit, so it
	// must let the project file be read at it, deploy on or off.
	r := newPollRig(t)
	conn := r.giteaConnection()
	r.giteaRepo("main")
	r.link(r.id, conn.Connection.ID, gitproviders.LinkInput{})
	var seen []gitproviders.PushEvent
	r.svc.SetPushHook(hookFunc(func(_ context.Context, ev gitproviders.PushEvent) { seen = append(seen, ev) }))
	r.put(5, false)
	r.dep.setHead(pollHead, "main", nil)

	r.sweep(0)
	require.Len(t, seen, 1)
	require.Equal(t, conn.Connection.ID, seen[0].ConnectionID)
	require.Equal(t, "acme/shop", seen[0].RepoFullName)
	require.Equal(t, "main", seen[0].Branch)
	require.Equal(t, pollHead, seen[0].CommitSHA)
	require.False(t, seen[0].PathsComplete, "a poll knows no changed files, so the file is always read")

	r.sweep(6 * time.Minute)
	require.Len(t, seen, 1, "an unchanged head is not checked again")

	r.dep.setHead(pollNewer, "main", nil)
	r.sweep(6 * time.Minute)
	require.Len(t, seen, 2)
	require.Equal(t, pollNewer, seen[1].CommitSHA)
}

func TestPollResolverErrorKeepsTheErrorAndKeepsChecking(t *testing.T) {
	r := newPollRig(t)
	r.put(5, false)
	r.dep.setHead(pollHead, "main", nil)
	r.sweep(0)
	require.Equal(t, pollHead, r.view().HeadSha)

	r.dep.setHead("", "", errors.New("git ls-remote: connection refused"))
	r.sweep(6 * time.Minute)
	v := r.view()
	require.Contains(t, v.Error, "connection refused")
	require.Equal(t, pollHead, v.HeadSha, "the last head seen stays")
	require.Empty(t, r.dep.snapshot())
	p, err := r.q.GetGitPoll(context.Background(), r.id)
	require.NoError(t, err)
	require.True(t, p.NextCheckAt.After(r.now), "the next check is still scheduled")

	n := r.dep.headCalls
	r.sweep(6 * time.Minute)
	require.Equal(t, n+1, r.dep.headCalls, "and it runs again")

	r.dep.setHead(pollNewer, "main", nil)
	r.sweep(6 * time.Minute)
	v = r.view()
	require.Empty(t, v.Error)
	require.Equal(t, pollNewer, v.HeadSha)
}

func TestPollWaitsForCIOnALinkThatAsksForIt(t *testing.T) {
	r := newPollRig(t)
	conn := r.giteaConnection()
	r.giteaRepo("main")
	r.link(r.id, conn.Connection.ID, gitproviders.LinkInput{WaitForCI: ptr(true)})
	r.put(5, true)
	r.dep.setHead(pollHead, "main", nil)

	r.sweep(0)
	require.Empty(t, r.dep.snapshot(), "parked, not deployed")
	l, err := r.svc.GetLink(r.ctx, r.id)
	require.NoError(t, err)
	require.NotNil(t, l.WaitingForCI)
	require.Equal(t, pollHead, l.WaitingForCI.SHA)

	raw := readTestdata(t, "gitea_status_success.json")
	r.provider.on("GET /api/v1/repos/acme/shop/commits/"+pollHead+"/status", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	})
	r.sweep(40 * time.Second)
	calls := r.dep.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, pollHead, calls[0].Commit.SHA)
	require.Equal(t, "New commit found by checking main every 5 min", calls[0].Message)
}

func TestPollNeverDeploysAServiceThatMustNotDeploy(t *testing.T) {
	for name, prepare := range map[string]func(r *pollRig){
		"stopped": func(r *pollRig) {
			_, err := r.pool.Exec(context.Background(), "UPDATE services SET desired_state = 'stopped' WHERE id = $1", r.id)
			require.NoError(t, err)
		},
		"cron": func(r *pollRig) {
			cfg := services.DecodeConfig(mustConfig(t, r))
			cfg.OneShot, cfg.Cron = true, &services.CronSpec{Schedule: "* * * * *"}
			r.setConfig(cfg)
		},
		"preview": func(r *pollRig) {
			_, err := r.pool.Exec(context.Background(), "UPDATE environments SET kind = 'preview' WHERE id = $1", r.env.ID)
			require.NoError(t, err)
		},
		"never deployed": func(r *pollRig) {
			_, err := r.pool.Exec(context.Background(), "UPDATE services SET current_deployment_id = NULL WHERE id = $1", r.id)
			require.NoError(t, err)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newPollRig(t)
			prepare(r)
			r.put(5, true)
			r.dep.setHead(pollHead, "main", nil)
			r.sweep(0)
			require.Empty(t, r.dep.snapshot())
			require.Equal(t, pollHead, r.view().HeadSha, "it is still checked")
		})
	}
}

func mustConfig(t *testing.T, r *pollRig) []byte {
	t.Helper()
	row, err := r.q.GetServiceScopeInternal(context.Background(), r.id)
	require.NoError(t, err)
	return row.Service.Config
}

func TestPollClaimIsExclusive(t *testing.T) {
	r := newPollRig(t)
	r.put(5, true)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, r.svc.Sweep(context.Background()))
		}()
	}
	wg.Wait()
	require.Equal(t, 1, r.dep.headCalls, "two concurrent sweeps check a due service once")
}

func TestPollViewIsOnTheServiceView(t *testing.T) {
	r := newPollRig(t)
	v, err := r.svcs.Get(r.ctx, r.id)
	require.NoError(t, err)
	require.Nil(t, v.GitPoll, "null while off")

	r.put(15, true)
	r.dep.setHead(pollHead, "main", nil)
	r.sweep(0)
	v, err = r.svcs.Get(r.ctx, r.id)
	require.NoError(t, err)
	require.NotNil(t, v.GitPoll)
	require.Equal(t, 15, v.GitPoll.IntervalMinutes)
	require.Equal(t, pollHead, v.GitPoll.HeadSha)
	require.True(t, v.GitPoll.UpdateAvailable)
}

func TestPollSetCheckAndRemoveOverHTTP(t *testing.T) {
	r := newPollRig(t)
	path := "/services/" + r.id.String() + "/git-poll"

	rec := r.do(r.reader, http.MethodGet, path, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.False(t, decodeJSON[services.GitPollView](t, rec).Enabled)

	rec = r.do(r.reader, http.MethodPut, path, map[string]any{"enabled": true})
	require.Equal(t, http.StatusForbidden, rec.Code, "needs projects:write")

	rec = r.do(r.ctx, http.MethodPut, path, map[string]any{"enabled": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	v := decodeJSON[services.GitPollView](t, rec)
	require.True(t, v.Enabled)
	require.Equal(t, 5, v.IntervalMinutes, "default interval")
	require.True(t, v.Deploy, "deploys by default")

	rec = r.do(r.ctx, http.MethodPut, path, map[string]any{"enabled": true, "intervalMinutes": 60, "deploy": false})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	v = decodeJSON[services.GitPollView](t, rec)
	require.Equal(t, 60, v.IntervalMinutes)
	require.False(t, v.Deploy)

	r.dep.setHead(pollHead, "main", nil)
	rec = r.do(r.ctx, http.MethodPost, path+"/check", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	v = decodeJSON[services.GitPollView](t, rec)
	require.Equal(t, pollHead, v.HeadSha)
	require.True(t, v.UpdateAvailable)
	require.Empty(t, r.dep.snapshot(), "checking never deploys")
	require.True(t, r.view().UpdateAvailable, "and the result is stored")

	rec = r.do(r.ctx, http.MethodPut, path, map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, rec.Code)
	require.False(t, decodeJSON[services.GitPollView](t, rec).Enabled)
	_, err := r.q.GetGitPoll(context.Background(), r.id)
	require.Error(t, err, "the row is gone")
}

func TestPollRejectsBadIntervalsAndNonGitServices(t *testing.T) {
	r := newPollRig(t)
	path := "/services/" + r.id.String() + "/git-poll"
	for _, bad := range []int{0, 2, 7, 30, 120, -1} {
		rec := r.do(r.ctx, http.MethodPut, path, map[string]any{"enabled": true, "intervalMinutes": bad})
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "interval %d: %s", bad, rec.Body.String())
	}
	for _, ok := range []int{1, 5, 15, 60} {
		rec := r.do(r.ctx, http.MethodPut, path, map[string]any{"enabled": true, "intervalMinutes": ok})
		require.Equal(t, http.StatusOK, rec.Code, "interval %d: %s", ok, rec.Body.String())
	}

	img, err := r.svcs.Create(r.ctx, r.env.ID, services.CreateInput{
		Name: "web", SourceType: services.SourceImage, ServerID: &r.server.ID, Config: services.Config{Image: "nginx:1", Port: 80},
	})
	require.NoError(t, err)
	imgPath := "/services/" + img.ID.String() + "/git-poll"
	rec := r.do(r.ctx, http.MethodPut, imgPath, map[string]any{"enabled": true})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "git_poll_not_applicable", errCode(t, rec))
	rec = r.do(r.ctx, http.MethodPost, imgPath+"/check", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "git_poll_not_applicable", errCode(t, rec))
	rec = r.do(r.ctx, http.MethodPut, imgPath, map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, rec.Code, "turning it off is always fine")
	rec = r.do(r.ctx, http.MethodGet, imgPath, nil)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestPollCheckWithoutPollingReportsTheHeadAndStoresNothing(t *testing.T) {
	r := newPollRig(t)
	r.dep.setHead(pollHead, "main", nil)
	v, err := r.svc.CheckGitPoll(r.ctx, r.id)
	require.NoError(t, err)
	require.False(t, v.Enabled)
	require.Equal(t, pollHead, v.HeadSha)
	require.True(t, v.UpdateAvailable)
	_, err = r.q.GetGitPoll(context.Background(), r.id)
	require.Error(t, err)

	r.dep.setHead("", "", errors.New("authentication required"))
	v, err = r.svc.CheckGitPoll(r.ctx, r.id)
	require.NoError(t, err, "an unreachable remote is reported in the view")
	require.Contains(t, v.Error, "authentication required")
}

// A REST token cannot make a production service deploy commits by itself without a person's approval.
func TestPollEnablingInProductionNeedsApprovalForRESTTokens(t *testing.T) {
	r := newPollRig(t) // the fixture environment is production
	g := prodguard.New(prodguard.Deps{Pool: r.pool, Box: r.box})
	g.Install()
	t.Cleanup(prodguardtest.Uninstall)
	rest := r.restToken()
	in := gitproviders.GitPollInput{Enabled: true}

	_, err := r.svc.PutGitPoll(rest, r.id, in)
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Summary, "Check for new commits of api")
	require.False(t, r.view().Enabled, "nothing changes before the approval")

	r.approve(g, err)
	_, err = r.svc.PutGitPoll(rest, r.id, in)
	require.NoError(t, err)
	require.True(t, r.view().Enabled)

	// Turning it off needs nobody, and a person in the browser is never asked.
	_, err = r.svc.PutGitPoll(rest, r.id, gitproviders.GitPollInput{})
	require.NoError(t, err)
	_, err = r.svc.PutGitPoll(r.ctx, r.id, in)
	require.NoError(t, err)
}
