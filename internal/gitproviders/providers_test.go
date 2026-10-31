package gitproviders_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
)

const sha40 = "bffeb74224043ba2feb48e12f2ac4a0a9de3e04c"

// ---- GitHub App ----

func newAppKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return key, string(pemBytes)
}

// verifyJWT checks an RS256 App JWT and returns its issuer.
func verifyJWT(t *testing.T, key *rsa.PublicKey, authz string) float64 {
	t.Helper()
	tok, ok := strings.CutPrefix(authz, "Bearer ")
	require.True(t, ok, "the App authenticates with a bearer JWT")
	parts := strings.Split(tok, ".")
	require.Len(t, parts, 3)
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	require.NoError(t, rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig), "the JWT is signed with the App's private key")
	var hdr map[string]string
	raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, json.Unmarshal(raw, &hdr))
	require.Equal(t, "RS256", hdr["alg"])
	var claims map[string]float64
	raw, _ = base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, json.Unmarshal(raw, &claims))
	require.Greater(t, claims["exp"], claims["iat"])
	require.LessOrEqual(t, claims["exp"]-claims["iat"], float64(11*60), "GitHub allows at most 10 minutes")
	return claims["iss"]
}

func TestGitHubAppManifestInstallCloneAndStatus(t *testing.T) {
	f := newFixture(t)
	key, keyPEM := newAppKey(t)
	gh := f.provider

	gh.on("POST /api/v3/app-manifests/CODE123/conversions", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 4242, "slug": "nebulactrl-test", "client_id": "Iv1.abc", "client_secret": "cs_secret_value", "webhook_secret": "whsec_from_github",
			"pem": keyPEM, "html_url": gh.srv.URL + "/apps/nebulactrl-test", "owner": map[string]string{"login": "acme"},
		})
	})
	gh.on("POST /api/v3/app/installations/555/access_tokens", func(w http.ResponseWriter, r *http.Request, body []byte) {
		require.EqualValues(t, 4242, verifyJWT(t, &key.PublicKey, r.Header.Get("Authorization")))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_installation_token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	})
	gh.json("GET /api/v3/installation/repositories", 200, map[string]any{"total_count": 1, "repositories": []map[string]any{
		{"full_name": "acme/shop", "clone_url": "https://github.com/acme/shop.git", "default_branch": "main", "private": true},
	}})
	gh.json("GET /api/v3/repos/acme/shop", 200, map[string]any{"full_name": "acme/shop", "clone_url": "https://github.com/acme/shop.git", "default_branch": "main"})
	gh.json("POST /api/v3/repos/acme/shop/statuses/"+sha40, 201, map[string]any{})

	// 1. The browser gets a manifest to post to GitHub.
	start, err := f.svc.StartGitHubManifest(f.ctx, gitproviders.ManifestInput{Owner: "acme", BaseURL: gh.srv.URL, Name: "Company GitHub"})
	require.NoError(t, err)
	u, err := url.Parse(start.PostURL)
	require.NoError(t, err)
	require.Equal(t, "/organizations/acme/settings/apps/new", u.Path, "an organization-owned App posts to the organization's settings")
	require.Equal(t, start.State, u.Query().Get("state"))
	var manifest struct {
		Name        string            `json:"name"`
		URL         string            `json:"url"`
		RedirectURL string            `json:"redirect_url"`
		SetupURL    string            `json:"setup_url"`
		Public      bool              `json:"public"`
		Hook        map[string]any    `json:"hook_attributes"`
		Permissions map[string]string `json:"default_permissions"`
		Events      []string          `json:"default_events"`
	}
	require.NoError(t, json.Unmarshal([]byte(start.Manifest), &manifest))
	require.Equal(t, "https://nebula.example.com/hooks/git/github/"+start.ConnectionID.String(), manifest.Hook["url"])
	require.Equal(t, true, manifest.Hook["active"])
	require.Equal(t, map[string]string{"contents": "read", "metadata": "read", "pull_requests": "write", "statuses": "write"}, manifest.Permissions)
	require.ElementsMatch(t, []string{"push", "pull_request"}, manifest.Events)
	require.Equal(t, "https://nebula.example.com/api/v1/git/github/manifest/callback", manifest.RedirectURL)
	require.Equal(t, "https://nebula.example.com/api/v1/git/github/installed", manifest.SetupURL)
	require.False(t, manifest.Public)
	require.LessOrEqual(t, len(manifest.Name), 34, "GitHub App names are limited to 34 characters")
	pending, err := f.svc.GetConnection(f.ctx, start.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, gitproviders.StatusPending, pending.Status)
	require.Equal(t, "Company GitHub", pending.Name)

	// 2. GitHub redirects back with a code: the App's credentials are stored.
	next, err := f.svc.CompleteManifest(f.ctx, "CODE123", start.State)
	require.NoError(t, err)
	require.Equal(t, gh.srv.URL+"/apps/nebulactrl-test/installations/new?state="+start.State, next, "then the user installs the App")
	var credsText, secretText string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT credentials_enc::text, webhook_secret_enc::text FROM git_connections").Scan(&credsText, &secretText))
	require.NotContains(t, credsText, "BEGIN RSA PRIVATE KEY")
	require.NotContains(t, secretText, "whsec_from_github")

	// 3. The installation activates the connection.
	id, note, err := f.svc.CompleteInstallation(f.ctx, start.State, 555, "install")
	require.NoError(t, err)
	require.Equal(t, start.ConnectionID, id)
	require.Empty(t, note)
	conn, err := f.svc.GetConnection(f.ctx, id)
	require.NoError(t, err)
	require.Equal(t, gitproviders.StatusActive, conn.Status, conn.StatusMessage)
	require.Equal(t, "nebulactrl-test", conn.GitHub.AppSlug)
	require.EqualValues(t, 555, conn.GitHub.InstallationID)
	require.Equal(t, "acme", conn.AccountLogin)
	require.True(t, conn.CanListRepos)
	raw, _ := json.Marshal(conn)
	for _, secret := range []string{"cs_secret_value", "whsec_from_github", "PRIVATE KEY", "ghs_installation_token"} {
		require.NotContains(t, string(raw), secret)
	}
	_, _, err = f.svc.CompleteInstallation(f.ctx, start.State, 555, "install")
	require.Error(t, err, "the state is single-use")

	// Repository listing uses a (cached) installation token.
	repos, err := f.svc.ListRepos(f.ctx, id, "", 10)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	_, err = f.svc.ListRepos(f.ctx, id, "shop", 10)
	require.NoError(t, err)
	require.Equal(t, "Bearer ghs_installation_token", gh.callsTo("GET", "/api/v3/installation/repositories")[0].Auth)

	// Linking a service gives deployments a short-lived, repository-scoped clone token.
	svc := f.gitService("api")
	link, err := f.svc.Link(f.ctx, svc.ID, gitproviders.LinkInput{ConnectionID: id, Repo: "acme/shop"})
	require.NoError(t, err)
	require.Equal(t, "app", link.Webhook.Mode, "GitHub delivers through the App's own webhook: no per-repo hook")
	got, _ := f.svcs.Get(f.ctx, svc.ID)
	require.Equal(t, "https://github.com/acme/shop.git", got.Config.GitURL)

	authz, err := f.svc.CloneAuth(context.Background(), svc.ID, got.Config.GitURL)
	require.NoError(t, err)
	require.Equal(t, "x-access-token", authz.Username)
	require.Equal(t, "ghs_installation_token", authz.Token)
	require.NotNil(t, authz.ExpiresAt)
	require.WithinDuration(t, time.Now().Add(time.Hour), *authz.ExpiresAt, 2*time.Minute)
	calls := gh.callsTo("POST", "/api/v3/app/installations/555/access_tokens")
	require.GreaterOrEqual(t, len(calls), 2)
	last := calls[len(calls)-1]
	var req struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}
	require.NoError(t, json.Unmarshal([]byte(last.Body), &req))
	require.Equal(t, []string{"shop"}, req.Repositories, "the clone token only reaches the service's repository")
	require.Equal(t, map[string]string{"contents": "read"}, req.Permissions, "and can only read its contents")

	// Services without a link get no credentials.
	other := f.gitService("other")
	empty, err := f.svc.CloneAuth(context.Background(), other.ID, "https://anywhere.example/acme/shop.git")
	require.NoError(t, err)
	require.Empty(t, empty.Token)

	// Commit statuses.
	require.NoError(t, f.svc.SetCommitStatus(context.Background(), id, "acme/shop", sha40, gitproviders.CommitStatus{
		State: gitproviders.StateSuccess, TargetURL: "https://nebula.example.com/d/1", Description: "Deployed", Context: "nebulactrl/api",
	}))
	st := gh.callsTo("POST", "/api/v3/repos/acme/shop/statuses/"+sha40)
	require.Len(t, st, 1)
	require.Equal(t, "Bearer ghs_installation_token", st[0].Auth)
	var body map[string]string
	require.NoError(t, json.Unmarshal([]byte(st[0].Body), &body))
	require.Equal(t, map[string]string{"state": "success", "target_url": "https://nebula.example.com/d/1", "description": "Deployed", "context": "nebulactrl/api"}, body)
	require.Error(t, f.svc.SetCommitStatus(context.Background(), id, "acme/shop", sha40, gitproviders.CommitStatus{State: "bogus"}))

	// Webhooks of the App verify with the secret GitHub generated.
	pushBody := readTestdata(t, "github_push.json")
	code, _ := f.post("github", id, map[string]string{
		"X-GitHub-Event": "push", "X-GitHub-Delivery": "gh-1", "X-Hub-Signature-256": "sha256=" + gitproviders.SignForTest("whsec_from_github", pushBody),
	}, pushBody)
	require.Equal(t, 200, code)
	code, _ = f.post("github", id, map[string]string{
		"X-GitHub-Event": "push", "X-GitHub-Delivery": "gh-2", "X-Hub-Signature-256": "sha256=" + gitproviders.SignForTest("whsec_placeholder", pushBody),
	}, pushBody)
	require.Equal(t, 401, code)
}

func TestGitHubAppInstallationRequestKeepsPending(t *testing.T) {
	f := newFixture(t)
	_, keyPEM := newAppKey(t)
	f.provider.json("POST /api/v3/app-manifests/C/conversions", 201, map[string]any{
		"id": 1, "slug": "app", "client_id": "c", "client_secret": "s", "webhook_secret": "w", "pem": keyPEM, "owner": map[string]string{"login": "acme"},
	})
	start, err := f.svc.StartGitHubManifest(f.ctx, gitproviders.ManifestInput{BaseURL: f.provider.srv.URL})
	require.NoError(t, err)
	u, _ := url.Parse(start.PostURL)
	require.Equal(t, "/settings/apps/new", u.Path, "without an owner the App belongs to the signed-in user")
	_, err = f.svc.CompleteManifest(f.ctx, "C", start.State)
	require.NoError(t, err)
	_, note, err := f.svc.CompleteInstallation(f.ctx, start.State, 0, "request")
	require.NoError(t, err)
	require.Contains(t, note, "approve")
	conn, _ := f.svc.GetConnection(f.ctx, start.ConnectionID)
	require.Equal(t, gitproviders.StatusPending, conn.Status)

	// Without a state (GitHub sometimes drops it) the waiting App is found, and
	// the installation is discovered through the API.
	f.provider.json("GET /api/v3/app/installations", 200, []map[string]any{{"id": 9, "account": map[string]string{"login": "acme"}}})
	f.provider.json("POST /api/v3/app/installations/9/access_tokens", 201, map[string]any{"token": "ghs_x", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	id, _, err := f.svc.CompleteInstallation(f.ctx, "", 0, "install")
	require.NoError(t, err)
	conn, _ = f.svc.GetConnection(f.ctx, id)
	require.Equal(t, gitproviders.StatusActive, conn.Status)
	require.EqualValues(t, 9, conn.GitHub.InstallationID)
}

func TestManifestNeedsPublicURLAndAdmin(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.StartGitHubManifest(f.reader, gitproviders.ManifestInput{})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusForbidden, ae.Status)
	_, err = f.svc.CompleteManifest(f.ctx, "x", "no-such-state")
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "invalid_state", ae.Code)
}

// ---- OAuth ----

func TestGitLabOAuthFlowAndRefresh(t *testing.T) {
	f := newFixture(t)
	gl := f.provider
	var grants []url.Values
	gl.on("POST /oauth/token", func(w http.ResponseWriter, r *http.Request, body []byte) {
		form, _ := url.ParseQuery(string(body))
		grants = append(grants, form)
		w.Header().Set("Content-Type", "application/json")
		switch form.Get("grant_type") {
		case "authorization_code":
			require.Equal(t, "the-code", form.Get("code"))
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at1_access", "refresh_token": "rt1_refresh", "expires_in": 7200, "token_type": "Bearer"})
		case "refresh_token":
			require.Equal(t, "rt1_refresh", form.Get("refresh_token"))
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at2_access", "refresh_token": "rt2_refresh", "expires_in": 7200, "token_type": "Bearer"})
		default:
			w.WriteHeader(400)
		}
	})
	gl.on("GET /api/v4/user", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		require.NotEmpty(t, r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]string{"username": "alice"})
	})
	gl.json("GET /api/v4/projects", 200, []map[string]any{{"path_with_namespace": "acme/shop", "http_url_to_repo": gl.srv.URL + "/acme/shop.git", "default_branch": "main", "visibility": "private"}})

	res, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{
		Provider: gitproviders.GitLab, Name: "GitLab", BaseURL: gl.srv.URL, AuthType: "oauth", ClientID: "app-id-1", ClientSecret: "app-secret-1",
	})
	require.NoError(t, err)
	require.Equal(t, gitproviders.StatusPending, res.Connection.Status)
	require.False(t, res.Connection.OAuth.Authorized)
	require.Equal(t, "https://nebula.example.com/api/v1/git/oauth/callback", res.Connection.OAuth.RedirectURI)
	au, err := url.Parse(res.AuthorizeURL)
	require.NoError(t, err)
	require.Equal(t, "/oauth/authorize", au.Path)
	q := au.Query()
	require.Equal(t, "app-id-1", q.Get("client_id"))
	require.Equal(t, "https://nebula.example.com/api/v1/git/oauth/callback", q.Get("redirect_uri"))
	require.Equal(t, "code", q.Get("response_type"))
	require.Equal(t, "api read_repository", q.Get("scope"))
	state := q.Get("state")
	require.NotEmpty(t, state)
	raw, _ := json.Marshal(res)
	require.NotContains(t, string(raw), "app-secret-1")

	_, err = f.svc.CompleteOAuth(f.ctx, "the-code", "wrong-state")
	require.Error(t, err)
	id, err := f.svc.CompleteOAuth(f.ctx, "the-code", state)
	require.NoError(t, err)
	require.Equal(t, res.Connection.ID, id)
	conn, _ := f.svc.GetConnection(f.ctx, id)
	require.Equal(t, gitproviders.StatusActive, conn.Status)
	require.Equal(t, "alice", conn.AccountLogin)
	require.True(t, conn.OAuth.Authorized)
	require.Equal(t, "app-id-1", grants[0].Get("client_id"))
	require.Equal(t, "app-secret-1", grants[0].Get("client_secret"))
	_, err = f.svc.CompleteOAuth(f.ctx, "the-code", state)
	require.Error(t, err, "the state is single-use")

	repos, err := f.svc.ListRepos(f.ctx, id, "", 10)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	require.Equal(t, "Bearer at1_access", gl.callsTo("GET", "/api/v4/projects")[0].Auth)

	// Two hours later the access token has expired: it is refreshed once and
	// the rotated refresh token is stored.
	f.svc.SetNow(func() time.Time { return time.Now().Add(121 * time.Minute) })
	_, err = f.svc.ListRepos(f.ctx, id, "", 10)
	require.NoError(t, err)
	require.Equal(t, "Bearer at2_access", gl.callsTo("GET", "/api/v4/projects")[1].Auth)
	require.Equal(t, "refresh_token", grants[len(grants)-1].Get("grant_type"))

	f.svc.SetNow(time.Now)
	n := len(grants)
	svc := f.gitService("api")
	gl.json("GET /api/v4/projects/acme/shop", 200, map[string]any{"path_with_namespace": "acme/shop", "http_url_to_repo": gl.srv.URL + "/acme/shop.git", "default_branch": "main"})
	gl.json("GET /api/v4/projects/acme/shop/hooks", 200, []any{})
	gl.json("POST /api/v4/projects/acme/shop/hooks", 201, map[string]any{"id": 31})
	_, err = f.svc.Link(f.ctx, svc.ID, gitproviders.LinkInput{ConnectionID: id, Repo: "acme/shop"})
	require.NoError(t, err)
	authz, err := f.svc.CloneAuth(context.Background(), svc.ID, gl.srv.URL+"/acme/shop.git")
	require.NoError(t, err)
	require.Equal(t, "oauth2", authz.Username)
	require.Equal(t, "at2_access", authz.Token)
	require.Len(t, grants, n, "a valid token is not refreshed")

	// A service's clone URL is editable by developers who cannot read the
	// connection's token: it only goes to the linked repository.
	for _, u := range []string{
		"http://attacker.example/acme/shop.git",
		gl.srv.URL + "/other/repo.git",
		strings.Replace(gl.srv.URL, "http://", "http://oauth2:x@", 1) + "/acme/shop.git",
		"file:///srv/git/acme/shop.git",
	} {
		_, err := f.svc.CloneAuth(context.Background(), svc.ID, u)
		var ae *apierr.Error
		require.ErrorAs(t, err, &ae, u)
		require.Equal(t, "git_link_mismatch", ae.Code, u)
	}
	// git:// has no authentication: the clone is anonymous and never carries the token.
	anon, err := f.svc.CloneAuth(context.Background(), svc.ID, strings.Replace(gl.srv.URL, "http://", "git://", 1)+"/attacker/repo.git")
	require.NoError(t, err)
	require.Empty(t, anon.Token)
	require.Empty(t, anon.Username)
	hook := gl.callsTo("POST", "/api/v4/projects/acme/shop/hooks")[0]
	var hb map[string]any
	require.NoError(t, json.Unmarshal([]byte(hook.Body), &hb))
	require.Equal(t, true, hb["push_events"])
	require.Equal(t, true, hb["merge_requests_events"])
	require.Equal(t, "https://nebula.example.com/hooks/git/gitlab/"+id.String(), hb["url"])
	require.NotEmpty(t, hb["token"], "GitLab echoes the secret token in X-Gitlab-Token")
}

func TestOAuthRefreshFailureMarksConnectionError(t *testing.T) {
	f := newFixture(t)
	gl := f.provider
	gl.on("POST /oauth/token", func(w http.ResponseWriter, r *http.Request, body []byte) {
		form, _ := url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		if form.Get("grant_type") == "refresh_token" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "a", "refresh_token": "r", "expires_in": 60, "token_type": "Bearer"})
	})
	gl.json("GET /api/v4/user", 200, map[string]string{"username": "alice"})
	res, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{Provider: gitproviders.GitLab, BaseURL: gl.srv.URL, AuthType: "oauth", ClientID: "c", ClientSecret: "s"})
	require.NoError(t, err)
	au, _ := url.Parse(res.AuthorizeURL)
	_, err = f.svc.CompleteOAuth(f.ctx, "code", au.Query().Get("state"))
	require.NoError(t, err)
	_, err = f.svc.ListRepos(f.ctx, res.Connection.ID, "", 10)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "credentials_unusable", ae.Code)
	conn, _ := f.svc.GetConnection(f.ctx, res.Connection.ID)
	require.Equal(t, gitproviders.StatusError, conn.Status)
	require.Contains(t, conn.StatusMessage, "reconnect")
}

// ---- commit statuses and PR comments per provider ----

func connectWith(t *testing.T, f *fixture, p gitproviders.Provider, userRoute string, userBody any) gitproviders.CreateConnectionResult {
	t.Helper()
	f.provider.json("GET "+userRoute, 200, userBody)
	res, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{Provider: p, BaseURL: f.provider.srv.URL, AuthType: "token", Token: "tok_0123456789"})
	require.NoError(t, err)
	return res
}

func TestGitLabCommitStatusAndMergeRequestComment(t *testing.T) {
	f := newFixture(t)
	gl := f.provider
	res := connectWith(t, f, gitproviders.GitLab, "/api/v4/user", map[string]string{"username": "alice"})
	gl.json("POST /api/v4/projects/acme/shop/statuses/"+sha40, 201, map[string]any{})
	for state, want := range map[string]string{"pending": "running", "success": "success", "failure": "failed", "error": "canceled"} {
		require.NoError(t, f.svc.SetCommitStatus(context.Background(), res.Connection.ID, "acme/shop", sha40, gitproviders.CommitStatus{
			State: state, Description: "d", Context: "nebulactrl/api", TargetURL: "https://nebula.example.com/d",
		}))
		calls := gl.callsTo("POST", "/api/v4/projects/acme/shop/statuses/"+sha40)
		var body map[string]string
		require.NoError(t, json.Unmarshal([]byte(calls[len(calls)-1].Body), &body))
		require.Equal(t, want, body["state"], state)
		require.Equal(t, "nebulactrl/api", body["name"])
		require.Equal(t, "https://nebula.example.com/d", body["target_url"])
	}
	require.Equal(t, "Bearer tok_0123456789", gl.callsTo("POST", "/api/v4/projects/acme/shop/statuses/"+sha40)[0].Auth)

	marker := "<!-- nebulactrl-preview -->"
	gl.json("GET /api/v4/projects/acme/shop/merge_requests/7/notes", 200, []map[string]any{{"id": 1, "body": "unrelated"}})
	gl.json("POST /api/v4/projects/acme/shop/merge_requests/7/notes", 201, map[string]any{})
	require.NoError(t, f.svc.UpsertPRComment(context.Background(), res.Connection.ID, "acme/shop", 7, marker, marker+"\npreview v1"))
	require.Len(t, gl.callsTo("POST", "/api/v4/projects/acme/shop/merge_requests/7/notes"), 1, "no comment with the marker yet: create one")
	gl.json("GET /api/v4/projects/acme/shop/merge_requests/7/notes", 200, []map[string]any{{"id": 1, "body": "unrelated"}, {"id": 5, "body": marker + "\nold"}})
	gl.json("PUT /api/v4/projects/acme/shop/merge_requests/7/notes/5", 200, map[string]any{})
	require.NoError(t, f.svc.UpsertPRComment(context.Background(), res.Connection.ID, "acme/shop", 7, marker, marker+"\npreview v2"))
	require.Len(t, gl.callsTo("PUT", "/api/v4/projects/acme/shop/merge_requests/7/notes/5"), 1, "the sticky comment is updated in place")
	require.Len(t, gl.callsTo("POST", "/api/v4/projects/acme/shop/merge_requests/7/notes"), 1)
	require.Error(t, f.svc.UpsertPRComment(context.Background(), res.Connection.ID, "acme/shop", 7, marker, "no marker in here"))
}

func TestGiteaCommitStatusAndPullRequestComment(t *testing.T) {
	f := newFixture(t)
	gt := f.provider
	res := connectWith(t, f, gitproviders.Gitea, "/api/v1/user", map[string]string{"login": "alice"})
	gt.json("POST /api/v1/repos/acme/shop/statuses/"+sha40, 201, map[string]any{})
	require.NoError(t, f.svc.SetCommitStatus(context.Background(), res.Connection.ID, "acme/shop", sha40, gitproviders.CommitStatus{
		State: gitproviders.StateFailure, Description: strings.Repeat("x", 400), Context: "nebulactrl/api",
	}))
	c := gt.callsTo("POST", "/api/v1/repos/acme/shop/statuses/"+sha40)[0]
	require.Equal(t, "token tok_0123456789", c.Auth)
	var body map[string]string
	require.NoError(t, json.Unmarshal([]byte(c.Body), &body))
	require.Equal(t, "failure", body["state"])
	require.LessOrEqual(t, len([]rune(body["description"])), 255, "long descriptions are cut to the provider's limit")

	marker := "<!-- nebulactrl-preview -->"
	gt.json("GET /api/v1/repos/acme/shop/issues/3/comments", 200, []map[string]any{{"id": 12, "body": marker + " old"}})
	gt.json("PATCH /api/v1/repos/acme/shop/issues/comments/12", 200, map[string]any{})
	require.NoError(t, f.svc.UpsertPRComment(context.Background(), res.Connection.ID, "acme/shop", 3, marker, marker+" new"))
	patch := gt.callsTo("PATCH", "/api/v1/repos/acme/shop/issues/comments/12")
	require.Len(t, patch, 1)
	require.JSONEq(t, `{"body":"`+marker+` new"}`, patch[0].Body)
}

func TestGitHubStatusAndCommentOnTokenlessConnectionFail(t *testing.T) {
	f := newFixture(t)
	bb, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{Provider: gitproviders.Bitbucket, Name: "bb"})
	require.NoError(t, err)
	err = f.svc.SetCommitStatus(context.Background(), bb.Connection.ID, "team/webapp", sha40, gitproviders.CommitStatus{State: gitproviders.StatePending})
	require.Error(t, err, "Bitbucket has no API integration")
}

func TestTestConnectionRecordsOutcome(t *testing.T) {
	f := newFixture(t)
	res := f.giteaConnection()
	conn, err := f.svc.TestConnection(f.ctx, res.Connection.ID)
	require.NoError(t, err)
	require.Equal(t, gitproviders.StatusActive, conn.Status)

	f.provider.json("GET /api/v1/user", 401, map[string]string{"message": "token revoked"})
	conn, err = f.svc.TestConnection(f.ctx, res.Connection.ID)
	require.NoError(t, err)
	require.Equal(t, gitproviders.StatusError, conn.Status)
	require.Contains(t, conn.StatusMessage, "401")
	require.NotContains(t, conn.StatusMessage, "gta_secret_token_123456")

	// Linking is refused while the connection is broken.
	_, err = f.svc.Link(f.ctx, f.gitService("api").ID, gitproviders.LinkInput{ConnectionID: res.Connection.ID, Repo: "acme/shop"})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "connection_not_active", ae.Code)

	// A new token repairs it.
	f.provider.json("GET /api/v1/user", 200, map[string]string{"login": "alice"})
	tok := "gta_new_token_9876543"
	conn, err = f.svc.UpdateConnection(f.ctx, res.Connection.ID, gitproviders.UpdateConnectionInput{Token: &tok})
	require.NoError(t, err)
	require.Equal(t, gitproviders.StatusActive, conn.Status)
	require.Equal(t, "token "+tok, f.provider.callsTo("GET", "/api/v1/user")[len(f.provider.callsTo("GET", "/api/v1/user"))-1].Auth)
}

func TestRotateWebhookSecretBitbucketOnly(t *testing.T) {
	f := newFixture(t)
	bb, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{Provider: gitproviders.Bitbucket, Name: "bb"})
	require.NoError(t, err)
	body := readTestdata(t, "bitbucket_push.json")
	hdrs := func(secret, id string) map[string]string {
		return map[string]string{"X-Event-Key": "repo:push", "X-Request-UUID": id, "X-Hub-Signature": "sha256=" + gitproviders.SignForTest(secret, body)}
	}
	code, _ := f.post("bitbucket", bb.Connection.ID, hdrs(bb.WebhookSecret, "1"), body)
	require.Equal(t, 200, code)
	next, err := f.svc.RotateWebhookSecret(f.ctx, bb.Connection.ID)
	require.NoError(t, err)
	require.NotEqual(t, bb.WebhookSecret, next)
	code, _ = f.post("bitbucket", bb.Connection.ID, hdrs(bb.WebhookSecret, "2"), body)
	require.Equal(t, 401, code, "the old secret stops working")
	body = bytes.ReplaceAll(body, []byte("709d658dc5b6d6afcd46049c2f332ee3f515a67d"), []byte("2222222222222222222222222222222222222222")) // a re-sent payload would be a replay
	code, _ = f.post("bitbucket", bb.Connection.ID, hdrs(next, "3"), body)
	require.Equal(t, 200, code)

	gt := f.giteaConnection()
	_, err = f.svc.RotateWebhookSecret(f.ctx, gt.Connection.ID)
	require.Error(t, err, "secrets of API-registered hooks are managed automatically")
}

// The OAuth callback sends the browser back to Team settings → Integrations,
// with the outcome in the query and no fragment.
func TestOAuthCallbackRedirectsToIntegrationsPage(t *testing.T) {
	f := newFixture(t)
	r := chi.NewRouter()
	f.svc.MountAPI(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/git/oauth/callback?error=access_denied", nil))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "https://nebula.example.com/settings/integrations", loc.Scheme+"://"+loc.Host+loc.Path)
	require.Empty(t, loc.Fragment)
	require.Contains(t, loc.Query().Get("git_error"), "access_denied")
}
