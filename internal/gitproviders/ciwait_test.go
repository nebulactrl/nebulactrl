package gitproviders_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
)

// eventRecorder is a notifications.Emitter that keeps what it is given.
type eventRecorder struct {
	mu     sync.Mutex
	events []notifications.Event
}

func (r *eventRecorder) Emit(_ context.Context, ev notifications.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *eventRecorder) blocked() []notifications.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []notifications.Event
	for _, e := range r.events {
		if e.Type == notifications.TypeDeployBlockedByCI {
			out = append(out, e)
		}
	}
	return out
}

// ciRig is a Gitea-linked service that waits for CI, with a controllable clock.
type ciRig struct {
	*fixture
	conn  gitproviders.CreateConnectionResult
	svcID uuid.UUID
	rec   *eventRecorder
	now   time.Time
	body  []byte
}

const ciRigSHA = "bffeb74224043ba2feb48e12f2ac4a0a9de3e04c"

func ptr[T any](v T) *T { return &v }

func newCIRig(t *testing.T) *ciRig {
	t.Helper()
	rec := &eventRecorder{}
	f := newFixture(t, func(d *gitproviders.Deps) { d.Notifier = rec })
	conn := f.giteaConnection()
	f.giteaRepo("main")
	svc := f.gitService("api")
	f.link(svc.ID, conn.Connection.ID, gitproviders.LinkInput{WaitForCI: ptr(true)})
	r := &ciRig{fixture: f, conn: conn, svcID: svc.ID, rec: rec, now: time.Now().UTC().Truncate(time.Second), body: readTestdata(t, "gitea_push.json")}
	f.svc.SetNow(func() time.Time { return r.now })
	return r
}

// statuses makes the fake Gitea answer the commit status of sha with a fixture.
func (r *ciRig) statuses(sha, fixture string) {
	raw := readTestdata(r.t, fixture)
	r.provider.on("GET /api/v1/repos/acme/shop/commits/"+sha+"/status", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	})
}

func (r *ciRig) push(delivery, sha string) string {
	r.t.Helper()
	body := bytes.ReplaceAll(r.body, []byte(ciRigSHA), []byte(sha))
	code, reply := r.post("gitea", r.conn.Connection.ID, giteaHeaders(r.conn.WebhookSecret, body, delivery), body)
	require.Equal(r.t, 200, code)
	return reply["summary"]
}

// advance moves the clock and runs one sweep.
func (r *ciRig) advance(d time.Duration) {
	r.t.Helper()
	r.now = r.now.Add(d)
	require.NoError(r.t, r.svc.Sweep(context.Background()))
}

func (r *ciRig) waiting() *gitproviders.QueuedCommit {
	r.t.Helper()
	v, err := r.svc.GetLink(r.ctx, r.svcID)
	require.NoError(r.t, err)
	return v.WaitingForCI
}

func TestWaitForCIParksThePushAndDeploysWhenChecksPass(t *testing.T) {
	r := newCIRig(t)
	link, err := r.svc.GetLink(r.ctx, r.svcID)
	require.NoError(t, err)
	require.True(t, link.WaitForCI)

	r.statuses(ciRigSHA, "gitea_status_pending.json")
	require.Contains(t, r.push("ci-1", ciRigSHA), "waiting for CI")
	require.Empty(t, r.dep.snapshot(), "the push is parked, not deployed")
	w := r.waiting()
	require.NotNil(t, w)
	require.Equal(t, ciRigSHA, w.SHA)
	require.Equal(t, "main", w.Branch)
	require.WithinDuration(t, r.now, w.Since, time.Second)

	r.advance(10 * time.Second)
	require.Empty(t, r.dep.snapshot())
	r.advance(31 * time.Second)
	require.Empty(t, r.dep.snapshot(), "checks are still pending")
	require.NotNil(t, r.waiting())
	require.Empty(t, r.rec.blocked())

	r.statuses(ciRigSHA, "gitea_status_success.json")
	r.advance(10 * time.Second)
	require.Empty(t, r.dep.snapshot(), "checks are read at most every 30 seconds")
	r.advance(31 * time.Second)
	calls := r.dep.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, r.svcID, calls[0].ServiceID)
	require.Equal(t, "git_push", calls[0].Trigger)
	require.Equal(t, ciRigSHA, calls[0].Commit.SHA)
	require.Equal(t, "main", calls[0].Commit.Branch)
	require.Nil(t, r.waiting())

	r.advance(time.Minute)
	require.Len(t, r.dep.snapshot(), 1, "and only once")
	require.Empty(t, r.rec.blocked())
}

func TestWaitForCIReadsTheChecksOfTheNewestPush(t *testing.T) {
	r := newCIRig(t)
	const newer = "0123456789abcdef0123456789abcdef01234567"
	r.statuses(ciRigSHA, "gitea_status_success.json")
	r.statuses(newer, "gitea_status_pending.json")
	r.push("ci-1", ciRigSHA)
	r.push("ci-2", newer)
	require.Equal(t, newer, r.waiting().SHA, "the newest push replaces the parked one")

	r.advance(time.Minute)
	require.Empty(t, r.dep.snapshot(), "the older commit's passing checks do not release the newer push")
	r.statuses(newer, "gitea_status_success.json")
	r.advance(time.Minute)
	calls := r.dep.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, newer, calls[0].Commit.SHA)
}

func TestWaitForCICommitWithoutChecksDeploysAfterTwoMinutes(t *testing.T) {
	r := newCIRig(t)
	r.statuses(ciRigSHA, "gitea_status_empty.json")
	r.push("ci-1", ciRigSHA)

	r.advance(time.Minute)
	require.Empty(t, r.dep.snapshot())
	r.advance(59 * time.Second)
	require.Empty(t, r.dep.snapshot(), "one second short of two minutes")
	r.advance(31 * time.Second)
	require.Len(t, r.dep.snapshot(), 1)
	require.Nil(t, r.waiting())
}

func TestWaitForCIFailureDoesNotDeployAndNotifies(t *testing.T) {
	r := newCIRig(t)
	r.statuses(ciRigSHA, "gitea_status_failure.json")
	r.push("ci-1", ciRigSHA)
	r.advance(time.Minute)

	require.Empty(t, r.dep.snapshot())
	require.Nil(t, r.waiting(), "a failed push is dropped")
	got := r.rec.blocked()
	require.Len(t, got, 1)
	require.Equal(t, notifications.SeverityWarning, got[0].Severity)
	require.Equal(t, "CI failed: ci/test", got[0].Fields["Reason"])
	require.Equal(t, ciRigSHA[:7], got[0].Fields["Commit"])
	require.NotNil(t, got[0].ProjectID)
	require.Equal(t, r.svcID, *got[0].ServiceID)

	r.advance(time.Hour)
	require.Len(t, r.rec.blocked(), 1, "announced once")
	require.Empty(t, r.dep.snapshot())
}

func TestWaitForCIPendingForTwoHoursIsDropped(t *testing.T) {
	r := newCIRig(t)
	r.statuses(ciRigSHA, "gitea_status_pending.json")
	r.push("ci-1", ciRigSHA)

	for range 7 { // 1h45m in 15 minute steps
		r.advance(15 * time.Minute)
	}
	require.NotNil(t, r.waiting())
	require.Empty(t, r.rec.blocked())
	r.advance(15 * time.Minute)
	require.Nil(t, r.waiting())
	got := r.rec.blocked()
	require.Len(t, got, 1)
	require.Equal(t, "CI still pending after 2 hours", got[0].Fields["Reason"])
	require.Empty(t, r.dep.snapshot())
}

func TestWaitForCIKeepsWaitingWhenTheProviderErrors(t *testing.T) {
	r := newCIRig(t)
	r.provider.on("GET /api/v1/repos/acme/shop/commits/"+ciRigSHA+"/status", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	r.push("ci-1", ciRigSHA)
	r.advance(time.Minute)
	r.advance(time.Minute)
	require.NotNil(t, r.waiting(), "a provider error is not a verdict")
	require.Empty(t, r.dep.snapshot())
	require.Empty(t, r.rec.blocked())

	r.statuses(ciRigSHA, "gitea_status_success.json")
	r.advance(time.Minute)
	require.Len(t, r.dep.snapshot(), 1)
}

func TestWaitForCIRechecksTheServiceBeforeDeploying(t *testing.T) {
	t.Run("auto-deploy turned off", func(t *testing.T) {
		r := newCIRig(t)
		r.statuses(ciRigSHA, "gitea_status_pending.json")
		r.push("ci-1", ciRigSHA)
		r.link(r.svcID, r.conn.Connection.ID, gitproviders.LinkInput{AutoDeploy: ptr(false)})
		r.statuses(ciRigSHA, "gitea_status_success.json")
		r.advance(time.Minute)
		require.Empty(t, r.dep.snapshot())
		require.Nil(t, r.waiting(), "the parked push is dropped")
		require.Empty(t, r.rec.blocked(), "silently: nothing failed")
	})
	t.Run("wait for CI turned off", func(t *testing.T) {
		r := newCIRig(t)
		r.statuses(ciRigSHA, "gitea_status_success.json")
		r.push("ci-1", ciRigSHA)
		require.NotNil(t, r.waiting())
		v := r.link(r.svcID, r.conn.Connection.ID, gitproviders.LinkInput{WaitForCI: ptr(false)})
		require.False(t, v.WaitForCI)
		require.Nil(t, v.WaitingForCI, "turning the setting off forgets the parked push")
		r.advance(time.Minute)
		require.Empty(t, r.dep.snapshot())
	})
	t.Run("branch changed", func(t *testing.T) {
		r := newCIRig(t)
		r.statuses(ciRigSHA, "gitea_status_success.json")
		r.push("ci-1", ciRigSHA)
		_, err := r.pool.Exec(context.Background(), `UPDATE services SET config = jsonb_set(config, '{gitBranch}', '"dev"') WHERE id = $1`, r.svcID)
		require.NoError(t, err)
		r.advance(time.Minute)
		require.Empty(t, r.dep.snapshot())
		require.Nil(t, r.waiting())
	})
}

func TestWaitForCIPushWhileDeployingIsQueuedLikeAnyPush(t *testing.T) {
	r := newCIRig(t)
	r.dep.fail = func(n int, _ depCall) error {
		if n == 0 {
			return apierr.Conflict("deployment_in_progress", "a deployment is already in progress for this service")
		}
		return nil
	}
	r.statuses(ciRigSHA, "gitea_status_success.json")
	r.push("ci-1", ciRigSHA)
	// A deployment of the service is running when the green push comes due.
	dep, err := r.q.InsertDeployment(context.Background(), db.InsertDeploymentParams{ServiceID: r.svcID, ServerID: &r.server.ID, Trigger: "manual", ActorType: "user", Strategy: "recreate"})
	require.NoError(t, err)
	r.advance(time.Minute)
	require.Empty(t, r.dep.snapshot())
	v, err := r.svc.GetLink(r.ctx, r.svcID)
	require.NoError(t, err)
	require.Equal(t, ciRigSHA, v.QueuedCommit, "a green push of a busy service waits for its turn")
	require.Nil(t, v.WaitingForCI)

	_, err = r.pool.Exec(context.Background(), "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(t, err)
	r.advance(time.Minute)
	calls := r.dep.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, ciRigSHA, calls[0].Commit.SHA)
}

func TestWaitForCIOffDeploysImmediately(t *testing.T) {
	r := newCIRig(t)
	r.link(r.svcID, r.conn.Connection.ID, gitproviders.LinkInput{WaitForCI: ptr(false)})
	require.Contains(t, r.push("ci-1", ciRigSHA), "deploying")
	require.Len(t, r.dep.snapshot(), 1)
	require.Nil(t, r.waiting())
}

func TestLinkKeepsWaitForCIAndRejectsConnectionsThatCannotReadCI(t *testing.T) {
	r := newCIRig(t)
	v := r.link(r.svcID, r.conn.Connection.ID, gitproviders.LinkInput{AutoDeploy: ptr(false)})
	require.True(t, v.WaitForCI, "a link update that does not mention it keeps the setting")

	bb, err := r.svc.CreateConnection(r.ctx, gitproviders.CreateConnectionInput{Provider: gitproviders.Bitbucket, Name: "Bitbucket"})
	require.NoError(t, err)
	other := r.gitService("other")
	_, err = r.svc.Link(r.ctx, other.ID, gitproviders.LinkInput{ConnectionID: bb.Connection.ID, Repo: "team/webapp", Branch: "main", WaitForCI: ptr(true)})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "ci_not_supported", ae.Code)
	require.Equal(t, 422, ae.Status)

	ok, err := r.svc.Link(r.ctx, other.ID, gitproviders.LinkInput{ConnectionID: bb.Connection.ID, Repo: "team/webapp", Branch: "main"})
	require.NoError(t, err)
	require.False(t, ok.WaitForCI)

	// Moving a service that waits for CI to a connection that cannot read it turns the wait off.
	moved, err := r.svc.Link(r.ctx, r.svcID, gitproviders.LinkInput{ConnectionID: bb.Connection.ID, Repo: "team/webapp", Branch: "main"})
	require.NoError(t, err)
	require.False(t, moved.WaitForCI)
}

// githubRig connects a GitHub App (GitHub Enterprise Server style, on the fake provider).
func githubConnection(t *testing.T, f *fixture) gitproviders.ConnectionView {
	t.Helper()
	_, keyPEM := newAppKey(t)
	gh := f.provider
	gh.on("POST /api/v3/app-manifests/CODE123/conversions", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":4242,"slug":"nebulactrl-test","client_id":"Iv1.abc","client_secret":"cs_secret_value","webhook_secret":"whsec_from_github","pem":` + jsonString(keyPEM) + `,"html_url":"` + gh.srv.URL + `/apps/nebulactrl-test","owner":{"login":"acme"}}`))
	})
	gh.on("POST /api/v3/app/installations/555/access_tokens", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"token":"ghs_installation_token","expires_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`))
	})
	gh.json("GET /api/v3/repos/acme/shop", 200, map[string]any{"full_name": "acme/shop", "clone_url": "https://github.com/acme/shop.git", "default_branch": "main"})
	start, err := f.svc.StartGitHubManifest(f.ctx, gitproviders.ManifestInput{Owner: "acme", BaseURL: gh.srv.URL, Name: "GitHub"})
	require.NoError(t, err)
	_, err = f.svc.CompleteManifest(f.ctx, "CODE123", start.State)
	require.NoError(t, err)
	id, _, err := f.svc.CompleteInstallation(f.ctx, start.State, 555, "install")
	require.NoError(t, err)
	conn, err := f.svc.GetConnection(f.ctx, id)
	require.NoError(t, err)
	return conn
}

func TestWaitForCIGitHubWithoutChecksPermissionNotifiesHowToFixIt(t *testing.T) {
	rec := &eventRecorder{}
	f := newFixture(t, func(d *gitproviders.Deps) { d.Notifier = rec })
	conn := githubConnection(t, f)
	svc := f.gitService("api")
	v, err := f.svc.Link(f.ctx, svc.ID, gitproviders.LinkInput{ConnectionID: conn.ID, Repo: "acme/shop", WaitForCI: ptr(true)})
	require.NoError(t, err)
	require.True(t, v.WaitForCI)

	const base = "/api/v3/repos/acme/shop/commits/" + ciRigSHA
	f.provider.on("GET "+base+"/check-runs", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		http.Error(w, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden)
	})
	f.provider.json("GET "+base+"/status", 200, map[string]any{"state": "pending", "statuses": []any{}})

	body := bytes.ReplaceAll(readTestdata(t, "github_push.json"), []byte("octo-org/monorepo"), []byte("acme/shop"))
	body = bytes.ReplaceAll(body, []byte("a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"), []byte(ciRigSHA))
	code, reply := f.post("github", conn.ID, map[string]string{
		"X-GitHub-Event": "push", "X-GitHub-Delivery": "gh-ci-1", "X-Hub-Signature-256": "sha256=" + gitproviders.SignForTest("whsec_from_github", body),
	}, body)
	require.Equal(t, 200, code)
	require.Contains(t, reply["summary"], "waiting for CI")

	require.NoError(t, f.svc.Sweep(context.Background()))
	require.Empty(t, f.dep.snapshot())
	got := rec.blocked()
	require.Len(t, got, 1)
	require.Contains(t, got[0].Fields["Reason"], "'Checks: read'")
	after, err := f.svc.GetLink(f.ctx, svc.ID)
	require.NoError(t, err)
	require.Nil(t, after.WaitingForCI)
	require.Len(t, f.provider.callsTo("GET", base+"/check-runs"), 1)
	require.Equal(t, "Bearer ghs_installation_token", f.provider.callsTo("GET", base+"/check-runs")[0].Auth)
}

// ghTokenWith makes the fake GitHub mint installation tokens carrying perms.
func ghTokenWith(f *fixture, perms map[string]string) {
	f.provider.json("POST /api/v3/app/installations/555/access_tokens", 201, map[string]any{
		"token": "ghs_installation_token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "permissions": perms,
	})
}

func TestPollDoesNotReparkACommitWaitForCIRefused(t *testing.T) {
	rec := &eventRecorder{}
	f := newFixture(t, func(d *gitproviders.Deps) { d.Notifier = rec })
	conn := githubConnection(t, f)
	sv := f.gitService("api")
	_, err := f.svc.Link(f.ctx, sv.ID, gitproviders.LinkInput{ConnectionID: conn.ID, Repo: "acme/shop", Branch: "main", WaitForCI: ptr(true)})
	require.NoError(t, err)
	f.deployedAt(sv, pollRunning)
	const base = "/api/v3/repos/acme/shop/commits/" + pollHead
	f.provider.on("GET "+base+"/check-runs", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		http.Error(w, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden)
	})
	now := time.Now().UTC().Truncate(time.Second)
	f.svc.SetNow(func() time.Time { return now })
	minutes, deploy := 1, true
	_, err = f.svc.PutGitPoll(f.ctx, sv.ID, gitproviders.GitPollInput{Enabled: true, IntervalMinutes: &minutes, Deploy: &deploy})
	require.NoError(t, err)
	f.dep.setHead(pollHead, "main", nil)

	for range 10 {
		now = now.Add(time.Minute)
		require.NoError(t, f.svc.Sweep(context.Background()))
	}
	require.Empty(t, f.dep.snapshot())
	require.Len(t, rec.blocked(), 1, "the refused commit is not parked and refused again every check")
	require.Len(t, f.provider.callsTo("GET", base+"/check-runs"), 1)
	v, err := f.svc.GetLink(f.ctx, sv.ID)
	require.NoError(t, err)
	require.Nil(t, v.WaitingForCI)
	require.NotNil(t, v.CIBlocked, "the link says which commit was refused")
	require.Equal(t, pollHead, v.CIBlocked.SHA)
	require.Contains(t, v.CIBlocked.Reason, "'Checks: read'")

	// Turning wait for CI off forgets the refusal: the next check deploys the commit.
	v, err = f.svc.Link(f.ctx, sv.ID, gitproviders.LinkInput{ConnectionID: conn.ID, Repo: "acme/shop", WaitForCI: ptr(false)})
	require.NoError(t, err)
	require.Nil(t, v.CIBlocked)
	// Checks are spread by ±10% of the interval: two minutes is due for sure.
	now = now.Add(2 * time.Minute)
	require.NoError(t, f.svc.Sweep(context.Background()))
	calls := f.dep.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, pollHead, calls[0].Commit.SHA)
}

func TestLinkWarnsWhenTheGitHubAppCannotReadChecks(t *testing.T) {
	f := newFixture(t)
	conn := githubConnection(t, f)
	ghTokenWith(f, map[string]string{"contents": "read", "metadata": "read", "statuses": "write", "pull_requests": "write"})
	sv := f.gitService("api")

	v, err := f.svc.Link(f.ctx, sv.ID, gitproviders.LinkInput{ConnectionID: conn.ID, Repo: "acme/shop", Branch: "main"})
	require.NoError(t, err)
	require.Empty(t, v.CIWarning, "no warning while the service does not wait for CI")

	v, err = f.svc.Link(f.ctx, sv.ID, gitproviders.LinkInput{ConnectionID: conn.ID, Repo: "acme/shop", WaitForCI: ptr(true)})
	require.NoError(t, err)
	require.Contains(t, v.CIWarning, "'Checks: read'")
	require.NotContains(t, v.CIWarning, "Commit statuses")

	// Accepting the new permissions on GitHub drops the cached token, so the warning goes at once.
	ghTokenWith(f, map[string]string{"contents": "read", "metadata": "read", "statuses": "write", "checks": "read"})
	body := []byte(`{"action":"new_permissions_accepted","installation":{"id":555}}`)
	code, reply := f.post("github", conn.ID, map[string]string{
		"X-GitHub-Event": "installation", "X-GitHub-Delivery": "gh-perm-1", "X-Hub-Signature-256": "sha256=" + gitproviders.SignForTest("whsec_from_github", body),
	}, body)
	require.Equal(t, 200, code)
	require.Equal(t, "new GitHub App permissions accepted", reply["summary"])
	v, err = f.svc.GetLink(f.ctx, sv.ID)
	require.NoError(t, err)
	require.Empty(t, v.CIWarning)
}

func TestGitHubAppWebhookPointingElsewhereIsReportedAndCorrected(t *testing.T) {
	f := newFixture(t)
	conn := githubConnection(t, f)
	sv := f.gitService("api")
	want := "https://nebula.example.com/hooks/git/github/" + conn.ID.String()
	hook := "https://nebula.example.com:8443/hooks/git/github/" + conn.ID.String()
	var mu sync.Mutex
	f.provider.on("GET /api/v3/app/hook/config", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"url": hook, "content_type": "json"})
	})
	f.provider.on("PATCH /api/v3/app/hook/config", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var in struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(body, &in)
		mu.Lock()
		hook = in.URL
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})

	v, err := f.svc.Link(f.ctx, sv.ID, gitproviders.LinkInput{ConnectionID: conn.ID, Repo: "acme/shop", Branch: "main"})
	require.NoError(t, err)
	require.Equal(t, "error", v.Webhook.Status)
	require.Contains(t, v.Webhook.Message, "nebula.example.com:8443")

	require.NoError(t, f.svc.Maintain(context.Background()))
	patches := f.provider.callsTo("PATCH", "/api/v3/app/hook/config")
	require.Len(t, patches, 1)
	require.JSONEq(t, `{"url":"`+want+`"}`, patches[0].Body)
	v, err = f.svc.GetLink(f.ctx, sv.ID)
	require.NoError(t, err)
	require.Equal(t, "active", v.Webhook.Status)
	require.Empty(t, v.Webhook.Message)

	require.NoError(t, f.svc.Maintain(context.Background()))
	require.Len(t, f.provider.callsTo("PATCH", "/api/v3/app/hook/config"), 1, "an App already pointed here is left alone")
}

func jsonString(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}
