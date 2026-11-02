package gitproviders_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
)

// githubApp walks the GitHub App manifest flow against the fake GitHub and
// returns the active connection's id.
func (f *fixture) githubApp() uuid.UUID {
	f.t.Helper()
	_, keyPEM := newAppKey(f.t)
	gh := f.provider
	gh.json("POST /api/v3/app-manifests/C/conversions", 201, map[string]any{
		"id": 1, "slug": "app", "client_id": "c", "client_secret": "s", "webhook_secret": "w", "pem": keyPEM, "owner": map[string]string{"login": "acme"},
	})
	gh.json("POST /api/v3/app/installations/9/access_tokens", 201, map[string]any{"token": "ghs_inst", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	start, err := f.svc.StartGitHubManifest(f.ctx, gitproviders.ManifestInput{BaseURL: gh.srv.URL})
	require.NoError(f.t, err)
	_, err = f.svc.CompleteManifest(f.ctx, "C", start.State)
	require.NoError(f.t, err)
	_, _, err = f.svc.CompleteInstallation(f.ctx, start.State, 9, "install")
	require.NoError(f.t, err)
	return start.ConnectionID
}

func TestGitHubAppBranchesPaginateAndRepoMissIsExplained(t *testing.T) {
	f := newFixture(t)
	id := f.githubApp()
	first := make([]map[string]string, 100)
	for i := range first {
		first[i] = map[string]string{"name": "b" + strconv.Itoa(i)}
	}
	f.provider.on("GET /api/v3/repos/acme/shop/branches", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			_ = json.NewEncoder(w).Encode(first)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]string{{"name": "last"}})
	})

	names, err := f.svc.ListBranches(f.ctx, id, "acme/shop")
	require.NoError(t, err)
	require.Len(t, names, 101, "a full page is followed by the next one")
	require.Equal(t, "b0", names[0])
	require.Equal(t, "last", names[100])
	require.Equal(t, "Bearer ghs_inst", f.provider.callsTo("GET", "/api/v3/repos/acme/shop/branches")[0].Auth, "the installation token authenticates API calls")

	_, err = f.svc.ListBranches(f.ctx, id, "acme/gone")
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "repo_not_found", ae.Code)
}

func TestGitHubPullRequestCommentIsCreatedThenUpdatedInPlace(t *testing.T) {
	f := newFixture(t)
	id := f.githubApp()
	marker := "<!-- nebulactrl-preview -->"
	bg := context.Background()

	f.provider.json("GET /api/v3/repos/acme/shop/issues/4/comments", 200, []map[string]any{{"id": 1, "body": "looks good"}})
	f.provider.json("POST /api/v3/repos/acme/shop/issues/4/comments", 201, map[string]any{})
	require.NoError(t, f.svc.UpsertPRComment(bg, id, "acme/shop", 4, marker, marker+"\nv1"))
	posts := f.provider.callsTo("POST", "/api/v3/repos/acme/shop/issues/4/comments")
	require.Len(t, posts, 1, "no comment carries the marker yet")
	require.JSONEq(t, `{"body":"`+marker+`\nv1"}`, posts[0].Body)

	f.provider.json("GET /api/v3/repos/acme/shop/issues/4/comments", 200, []map[string]any{{"id": 1, "body": "looks good"}, {"id": 8, "body": marker + "\nv1"}})
	f.provider.json("PATCH /api/v3/repos/acme/shop/issues/comments/8", 200, map[string]any{})
	require.NoError(t, f.svc.UpsertPRComment(bg, id, "acme/shop", 4, marker, marker+"\nv2"))
	require.Len(t, f.provider.callsTo("PATCH", "/api/v3/repos/acme/shop/issues/comments/8"), 1)
	require.Len(t, f.provider.callsTo("POST", "/api/v3/repos/acme/shop/issues/4/comments"), 1, "the sticky comment is edited, not repeated")

	f.provider.json("GET /api/v3/repos/acme/shop/issues/4/comments", 500, map[string]any{"message": "boom"})
	err := f.svc.UpsertPRComment(bg, id, "acme/shop", 4, marker, marker)
	require.ErrorContains(t, err, "comment on acme/shop#4", "a provider failure names the pull request")
}

func TestGitHubAppConnectionTestChecksRepositoryAccess(t *testing.T) {
	f := newFixture(t)
	id := f.githubApp()
	f.provider.json("GET /api/v3/installation/repositories", 200, map[string]any{"total_count": 0, "repositories": []any{}})

	v, err := f.svc.TestConnection(f.ctx, id)
	require.NoError(t, err)
	require.Equal(t, gitproviders.StatusActive, v.Status)
	require.Len(t, f.provider.callsTo("GET", "/api/v3/installation/repositories"), 1)

	f.provider.json("GET /api/v3/installation/repositories", 403, map[string]any{"message": "Resource not accessible by integration"})
	v, err = f.svc.TestConnection(f.ctx, id)
	require.NoError(t, err)
	require.Equal(t, gitproviders.StatusError, v.Status)
	require.Contains(t, v.StatusMessage, "403")
}

func TestLinkingToAGitHubAppNeedsNoRepositoryWebhook(t *testing.T) {
	f := newFixture(t)
	id := f.githubApp()
	f.provider.json("GET /api/v3/repos/acme/shop", 200, map[string]any{"full_name": "acme/shop", "clone_url": "https://github.com/acme/shop.git", "default_branch": "main"})
	svc := f.gitService("api")

	v, err := f.svc.Link(f.ctx, svc.ID, gitproviders.LinkInput{ConnectionID: id, Repo: "acme/shop"})
	require.NoError(t, err)
	require.Equal(t, "app", v.Webhook.Mode, "GitHub delivers through the App's own webhook")
	require.Equal(t, "active", v.Webhook.Status)
	require.Equal(t, "https://nebula.example.com/hooks/git/github/"+id.String(), v.Webhook.URL)
	require.Empty(t, f.provider.callsTo("POST", "/api/v3/repos/acme/shop/hooks"), "no per-repository webhook is created")
	require.NoError(t, f.svc.Unlink(f.ctx, svc.ID))
	got, err := f.svc.GetLink(f.ctx, svc.ID)
	require.NoError(t, err)
	require.False(t, got.Linked)
}

// ---- webhooks registered through provider APIs ----

func TestLinkReusesAnExistingGitLabWebhookAndUnlinkRemovesIt(t *testing.T) {
	f := newFixture(t)
	gl := f.provider
	res := connectWith(t, f, gitproviders.GitLab, "/api/v4/user", map[string]string{"username": "alice"})
	hook := "https://nebula.example.com/hooks/git/gitlab/" + res.Connection.ID.String()
	gl.json("GET /api/v4/projects/acme/shop", 200, map[string]any{"path_with_namespace": "acme/shop", "http_url_to_repo": gl.srv.URL + "/acme/shop.git", "default_branch": "main"})
	gl.json("GET /api/v4/projects/acme/shop/hooks", 200, []map[string]any{{"id": 3, "url": "https://other.example.com/x"}, {"id": 99, "url": hook}})
	gl.on("DELETE /api/v4/projects/acme/shop/hooks/99", func(w http.ResponseWriter, _ *http.Request, _ []byte) { w.WriteHeader(http.StatusNotFound) })
	svc := f.gitService("api")

	v, err := f.svc.Link(f.ctx, svc.ID, gitproviders.LinkInput{ConnectionID: res.Connection.ID, Repo: "acme/shop"})
	require.NoError(t, err)
	require.Equal(t, "registered", v.Webhook.Mode)
	require.Equal(t, "active", v.Webhook.Status)
	require.Empty(t, gl.callsTo("POST", "/api/v4/projects/acme/shop/hooks"), "the webhook that already points here is adopted, not duplicated")
	var ext string
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT external_id FROM git_webhooks WHERE connection_id = $1`, res.Connection.ID).Scan(&ext))
	require.Equal(t, "99", ext)

	// A hook that is already gone at GitLab does not block the unlink.
	require.NoError(t, f.svc.Unlink(f.ctx, svc.ID))
	require.Len(t, gl.callsTo("DELETE", "/api/v4/projects/acme/shop/hooks/99"), 1)
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM git_webhooks`).Scan(&n))
	require.Zero(t, n, "the webhook record goes with the last link")
}

func TestGitLabBranchesAndMissingRepo(t *testing.T) {
	f := newFixture(t)
	res := connectWith(t, f, gitproviders.GitLab, "/api/v4/user", map[string]string{"username": "alice"})
	f.provider.json("GET /api/v4/projects/acme/shop/repository/branches", 200, []map[string]string{{"name": "main"}, {"name": "dev"}})

	names, err := f.svc.ListBranches(f.ctx, res.Connection.ID, "acme/shop")
	require.NoError(t, err)
	require.Equal(t, []string{"main", "dev"}, names)

	_, err = f.svc.ListBranches(f.ctx, res.Connection.ID, "acme/gone")
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "repo_not_found", ae.Code)
}

func TestGiteaLinkAdoptsAnExistingHookAndRecordsRegistrationFailures(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	hook := "https://nebula.example.com/hooks/git/gitea/" + conn.Connection.ID.String()
	f.provider.json("GET /api/v1/repos/acme/shop/hooks", 200, []map[string]any{{"id": 41, "config": map[string]string{"url": hook}}})
	svc := f.gitService("api")

	v := f.link(svc.ID, conn.Connection.ID, gitproviders.LinkInput{})
	require.Equal(t, "active", v.Webhook.Status)
	require.Empty(t, f.provider.callsTo("POST", "/api/v1/repos/acme/shop/hooks"), "an existing hook is reused")

	// Linking a second service to the same repository does not touch the provider again.
	n := len(f.provider.callsTo("GET", "/api/v1/repos/acme/shop/hooks"))
	f.link(f.gitService("worker").ID, conn.Connection.ID, gitproviders.LinkInput{})
	require.Len(t, f.provider.callsTo("GET", "/api/v1/repos/acme/shop/hooks"), n)
}

func TestWebhookRegistrationFailureIsShownNotFatal(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	f.provider.json("GET /api/v1/repos/acme/shop/hooks", 200, []any{})
	f.provider.json("POST /api/v1/repos/acme/shop/hooks", 403, map[string]string{"message": "forbidden"})
	svc := f.gitService("api")

	v := f.link(svc.ID, conn.Connection.ID, gitproviders.LinkInput{})
	require.Equal(t, "registered", v.Webhook.Mode)
	require.Equal(t, "error", v.Webhook.Status)
	require.Contains(t, v.Webhook.Message, "Could not register the webhook")
	require.Contains(t, v.Webhook.Message, "permission to manage webhooks")
	require.True(t, v.Linked, "the link itself succeeded")

	// Fixing the permission and linking again registers it.
	f.provider.json("POST /api/v1/repos/acme/shop/hooks", 201, map[string]any{"id": 7})
	v = f.link(svc.ID, conn.Connection.ID, gitproviders.LinkInput{})
	require.Equal(t, "active", v.Webhook.Status)
	require.Empty(t, v.Webhook.Message)
}

func TestWebhookNeedsAPublicURLToBeRegistered(t *testing.T) {
	cfg := &config.Config{PublicURL: "https://nebula.example.com"}
	f := newFixture(t, func(d *gitproviders.Deps) { d.Config = cfg })
	conn := f.giteaConnection()
	f.giteaRepo("main")
	cfg.PublicURL = ""

	v := f.link(f.gitService("api").ID, conn.Connection.ID, gitproviders.LinkInput{})
	require.Equal(t, "error", v.Webhook.Status)
	require.Contains(t, v.Webhook.Message, "NEBULACTRL_PUBLIC_URL")
	require.Empty(t, f.provider.callsTo("POST", "/api/v1/repos/acme/shop/hooks"))
}

func TestRepoUnlinkedRemovesTheWebhookOnlyWhenNoServiceUsesTheRepository(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	a, b := f.gitService("api"), f.gitService("worker")
	f.link(a.ID, conn.Connection.ID, gitproviders.LinkInput{})
	f.link(b.ID, conn.Connection.ID, gitproviders.LinkInput{})
	bg := context.Background()

	// api moves to an image source: the link row goes, worker still needs the hook.
	_, err := f.pool.Exec(bg, `DELETE FROM service_git WHERE service_id = $1`, a.ID)
	require.NoError(t, err)
	f.svc.RepoUnlinked(bg, conn.Connection.ID, "acme/shop")
	require.Empty(t, f.provider.callsTo("DELETE", "/api/v1/repos/acme/shop/hooks/7"))

	_, err = f.pool.Exec(bg, `DELETE FROM service_git WHERE service_id = $1`, b.ID)
	require.NoError(t, err)
	f.svc.RepoUnlinked(bg, conn.Connection.ID, "acme/shop")
	require.Len(t, f.provider.callsTo("DELETE", "/api/v1/repos/acme/shop/hooks/7"), 1)

	f.svc.RepoUnlinked(bg, uuid.New(), "acme/shop") // an unknown connection is ignored
}

// ---- jobs ----

func TestMaintenanceJobRemovesOrphanWebhooksAndForgetsOldRows(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	f.link(f.gitService("api").ID, conn.Connection.ID, gitproviders.LinkInput{})
	bg := context.Background()
	body := readTestdata(t, "gitea_push.json")
	code, _ := f.post("gitea", conn.Connection.ID, giteaHeaders(conn.WebhookSecret, body, "m-1"), body)
	require.Equal(t, 200, code)

	// Nobody uses the repository any more (its services were deleted).
	_, err := f.pool.Exec(bg, `DELETE FROM service_git`)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, `UPDATE git_webhooks SET updated_at = now() - interval '2 hours'`)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, `UPDATE git_delivery_ids SET seen_at = now() - interval '8 days'`)
	require.NoError(t, err)

	require.NoError(t, f.svc.RunMaintenanceJobForTest(bg))
	require.Len(t, f.provider.callsTo("DELETE", "/api/v1/repos/acme/shop/hooks/7"), 1, "the orphaned webhook is removed at the provider")
	var hooks, ids int
	require.NoError(t, f.pool.QueryRow(bg, `SELECT count(*) FROM git_webhooks`).Scan(&hooks))
	require.NoError(t, f.pool.QueryRow(bg, `SELECT count(*) FROM git_delivery_ids`).Scan(&ids))
	require.Zero(t, hooks)
	require.Zero(t, ids, "delivery ids older than the replay window are forgotten")

	require.NoError(t, f.svc.RunMaintenanceJobForTest(bg), "a second run has nothing to do")
	require.Len(t, f.provider.callsTo("DELETE", "/api/v1/repos/acme/shop/hooks/7"), 1)
}

func TestMaintenanceDropsAbandonedPendingConnections(t *testing.T) {
	f := newFixture(t)
	start, err := f.svc.StartGitHubManifest(f.ctx, gitproviders.ManifestInput{BaseURL: f.provider.srv.URL})
	require.NoError(t, err)
	bg := context.Background()

	require.NoError(t, f.svc.RunMaintenanceJobForTest(bg))
	_, err = f.svc.GetConnection(f.ctx, start.ConnectionID)
	require.NoError(t, err, "a flow that is still within its window stays")

	_, err = f.pool.Exec(bg, `UPDATE git_connections SET pending_expires_at = now() - interval '2 days'`)
	require.NoError(t, err)
	require.NoError(t, f.svc.RunMaintenanceJobForTest(bg))
	_, err = f.svc.GetConnection(f.ctx, start.ConnectionID)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusNotFound, ae.Status, "an expired, never finished connection is dropped")
}

func TestSweepAndReportJobsPostCommitStatuses(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	svc := f.gitService("api")
	f.link(svc.ID, conn.Connection.ID, gitproviders.LinkInput{})
	sha := "bffeb74224043ba2feb48e12f2ac4a0a9de3e04c"
	f.provider.json("POST /api/v1/repos/acme/shop/statuses/"+sha, 201, map[string]string{})
	bg := context.Background()
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: svc.ID, ServerID: &f.server.ID, Trigger: "git_push", ActorType: "system", Strategy: "recreate"})
	require.NoError(t, err)
	require.NoError(t, f.q.SetDeploymentCommit(bg, db.SetDeploymentCommitParams{ID: dep.ID, CommitSha: sha, CommitBranch: "main"}))

	require.NoError(t, f.svc.RunReportJobForTest(bg, dep.ID))
	require.Len(t, f.provider.callsTo("POST", "/api/v1/repos/acme/shop/statuses/"+sha), 1, "the report job posts the pending status")
	require.NoError(t, f.svc.RunReportJobForTest(bg, dep.ID))
	require.Len(t, f.provider.callsTo("POST", "/api/v1/repos/acme/shop/statuses/"+sha), 1, "once per state")

	_, err = f.pool.Exec(bg, `UPDATE deployments SET status = 'succeeded' WHERE id = $1`, dep.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.RunSweepJobForTest(bg))
	require.Len(t, f.provider.callsTo("POST", "/api/v1/repos/acme/shop/statuses/"+sha), 2, "the sweep catches up on the final state")

	require.NoError(t, f.svc.RunReportJobForTest(bg, uuid.New()), "an unknown deployment is nothing to report")
}

func TestBitbucketLinkAndCloneCredentials(t *testing.T) {
	f := newFixture(t)
	bb, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{
		Provider: gitproviders.Bitbucket, Name: "Bitbucket DC", BaseURL: "https://git.corp.example.com", Token: "bbdc_repo_token_123",
	})
	require.NoError(t, err)
	require.Equal(t, gitproviders.AuthToken, bb.Connection.AuthType, "an optional repository token enables private clones")
	svc := f.gitService("api")

	v, err := f.svc.Link(f.ctx, svc.ID, gitproviders.LinkInput{ConnectionID: bb.Connection.ID, Repo: "TEAM/webapp", Branch: "release"})
	require.NoError(t, err)
	require.Equal(t, "manual", v.Webhook.Mode)
	require.Equal(t, "https://git.corp.example.com/projects/TEAM/repos/webapp", v.RepoURL)
	got, err := f.svcs.Get(f.ctx, svc.ID)
	require.NoError(t, err)
	require.Equal(t, "https://git.corp.example.com/scm/team/webapp.git", got.Config.GitURL, "Data Center clones live under /scm with a lower-case path")
	require.Equal(t, "release", got.Config.GitBranch)

	auth, err := f.svc.CloneAuth(context.Background(), svc.ID, got.Config.GitURL)
	require.NoError(t, err)
	require.Equal(t, "x-token-auth", auth.Username)
	require.Equal(t, "bbdc_repo_token_123", auth.Token)

	_, err = f.svc.CloneAuth(context.Background(), svc.ID, "https://git.corp.example.com/scm/other/repo.git")
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "git_link_mismatch", ae.Code, "the token is never sent to another repository")

	// Without a token the clone is anonymous.
	anon, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{Provider: gitproviders.Bitbucket, Name: "Public BB"})
	require.NoError(t, err)
	other := f.gitService("docs")
	_, err = f.svc.Link(f.ctx, other.ID, gitproviders.LinkInput{ConnectionID: anon.Connection.ID, Repo: "team/docs"})
	require.NoError(t, err)
	got, err = f.svcs.Get(f.ctx, other.ID)
	require.NoError(t, err)
	auth, err = f.svc.CloneAuth(context.Background(), other.ID, got.Config.GitURL)
	require.NoError(t, err)
	require.Empty(t, auth.Token)
	require.Empty(t, auth.Username)
}
