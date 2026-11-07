package gitproviders_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
)

// asPrincipal serves every request of the router with ctx's principal, the way
// the API's authentication middleware does.
func asPrincipal(ctx context.Context, next http.Handler) http.Handler {
	p, _ := auth.PrincipalFrom(ctx)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	})
}

// do runs one request against the git endpoints as ctx's principal.
func (f *fixture) do(ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler { return asPrincipal(ctx, next) })
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	gitproviders.Register(humachi.New(r, cfg), f.svc)

	var rd *bytes.Reader
	if body == nil {
		rd = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		require.NoError(f.t, err)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// browse runs one request against the browser redirect endpoints as ctx's
// principal and returns the redirect target's query.
func (f *fixture) browse(ctx context.Context, path string) (*httptest.ResponseRecorder, url.Values) {
	f.t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler { return asPrincipal(ctx, next) })
	f.svc.MountAPI(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(f.t, err)
	return rec, loc.Query()
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v), rec.Body.String())
	return v
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e), rec.Body.String())
	return e.Code
}

func TestConnectionCRUDOverHTTP(t *testing.T) {
	f := newFixture(t)
	f.provider.json("GET /api/v1/user", 200, map[string]string{"login": "alice"})

	rec := f.do(f.ctx, http.MethodPost, "/git/connections", map[string]any{
		"provider": "gitea", "name": "Team Gitea", "baseUrl": f.provider.srv.URL, "authType": "token", "token": "gta_secret_token_123456",
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	created := decodeJSON[gitproviders.CreateConnectionResult](t, rec)
	require.Equal(t, "Team Gitea", created.Connection.Name)
	require.Equal(t, gitproviders.StatusActive, created.Connection.Status)
	require.Equal(t, "alice", created.Connection.AccountLogin)
	require.NotEmpty(t, created.WebhookSecret, "the secret is shown once, in the create response")
	id := created.Connection.ID.String()
	require.NotContains(t, rec.Body.String(), "gta_secret_token_123456", "the token is write-only")

	rec = f.do(f.ctx, http.MethodGet, "/git/connections", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	list := decodeJSON[struct {
		Items []gitproviders.ConnectionView `json:"items"`
	}](t, rec)
	require.Len(t, list.Items, 1)
	require.Equal(t, created.Connection.ID, list.Items[0].ID)
	require.NotContains(t, rec.Body.String(), created.WebhookSecret, "listing never repeats the secret")

	rec = f.do(f.ctx, http.MethodGet, "/git/connections/"+id, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, created.Connection.ID, decodeJSON[gitproviders.ConnectionView](t, rec).ID)

	rec = f.do(f.ctx, http.MethodPatch, "/git/connections/"+id, map[string]any{"name": "Renamed"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "Renamed", decodeJSON[gitproviders.ConnectionView](t, rec).Name)

	rec = f.do(f.ctx, http.MethodPatch, "/git/connections/"+id, map[string]any{"name": "   "})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "validation_failed", errCode(t, rec))

	f.provider.json("GET /api/v1/user", 401, map[string]string{"message": "revoked"})
	rec = f.do(f.ctx, http.MethodPost, "/git/connections/"+id+"/test", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	tested := decodeJSON[gitproviders.ConnectionView](t, rec)
	require.Equal(t, gitproviders.StatusError, tested.Status, "a failed test is recorded, not an HTTP error")
	require.Contains(t, tested.StatusMessage, "401")

	rec = f.do(f.ctx, http.MethodDelete, "/git/connections/"+id, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	rec = f.do(f.ctx, http.MethodGet, "/git/connections/"+id, nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestConnectionEndpointsRejectBadInputAndMissingScopes(t *testing.T) {
	f := newFixture(t)
	body := map[string]any{"provider": "gitea", "baseUrl": f.provider.srv.URL, "authType": "token", "token": "tok_0123456789"}

	rec := f.do(f.reader, http.MethodPost, "/git/connections", body)
	require.Equal(t, http.StatusForbidden, rec.Code, "creating a connection needs the admin scope")

	rec = f.do(f.ctx, http.MethodPost, "/git/connections", map[string]any{"provider": "svn"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "the provider is an enum")

	rec = f.do(f.ctx, http.MethodPost, "/git/connections", map[string]any{"provider": "github"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "GitHub connects through the manifest flow, not this endpoint")

	f.provider.json("GET /api/v1/user", 401, map[string]string{"message": "bad token"})
	rec = f.do(f.ctx, http.MethodPost, "/git/connections", body)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "credentials_invalid", errCode(t, rec), "a token the provider rejects is not stored")
	rec = f.do(f.ctx, http.MethodGet, "/git/connections", nil)
	require.JSONEq(t, `{"items":[]}`, rec.Body.String())

	missing := uuid.NewString()
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/git/connections/" + missing},
		{http.MethodPatch, "/git/connections/" + missing},
		{http.MethodDelete, "/git/connections/" + missing},
		{http.MethodPost, "/git/connections/" + missing + "/test"},
		{http.MethodPost, "/git/connections/" + missing + "/oauth/start"},
		{http.MethodPost, "/git/connections/" + missing + "/refresh"},
		{http.MethodPost, "/git/connections/" + missing + "/webhook-secret/rotate"},
		{http.MethodGet, "/git/connections/" + missing + "/repos"},
		{http.MethodGet, "/git/connections/" + missing + "/branches?repo=acme/shop"},
		{http.MethodGet, "/git/connections/" + missing + "/deliveries"},
	} {
		var payload any
		if c.method == http.MethodPatch {
			payload = map[string]any{"name": "x"}
		}
		rec = f.do(f.ctx, c.method, c.path, payload)
		require.Equal(t, http.StatusNotFound, rec.Code, c.method+" "+c.path)
	}
}

func TestReaderCanListButNotAdministerConnections(t *testing.T) {
	f := newFixture(t)
	res := f.giteaConnection()
	id := res.Connection.ID.String()

	require.Equal(t, http.StatusOK, f.do(f.reader, http.MethodGet, "/git/connections", nil).Code)
	require.Equal(t, http.StatusOK, f.do(f.reader, http.MethodGet, "/git/connections/"+id, nil).Code)
	for _, c := range []struct{ method, path string }{
		{http.MethodPatch, "/git/connections/" + id},
		{http.MethodDelete, "/git/connections/" + id},
		{http.MethodPost, "/git/connections/" + id + "/test"},
		{http.MethodPost, "/git/connections/" + id + "/oauth/start"},
		{http.MethodPost, "/git/connections/" + id + "/refresh"},
		{http.MethodPost, "/git/connections/" + id + "/webhook-secret/rotate"},
		{http.MethodGet, "/git/connections/" + id + "/repos"},
		{http.MethodGet, "/git/connections/" + id + "/branches?repo=acme/shop"},
		{http.MethodGet, "/git/connections/" + id + "/deliveries"},
		{http.MethodPost, "/git/github/manifest"},
	} {
		var payload any
		if c.method == http.MethodPatch || c.path == "/git/github/manifest" {
			payload = map[string]any{"name": "x"}
		}
		rec := f.do(f.reader, c.method, c.path, payload)
		require.Equal(t, http.StatusForbidden, rec.Code, c.method+" "+c.path+": "+rec.Body.String())
	}
	conn, err := f.svc.GetConnection(f.ctx, res.Connection.ID)
	require.NoError(t, err)
	require.Equal(t, res.Connection.Name, conn.Name, "a refused PATCH changes nothing")
}

func TestDeleteConnectionInUseIs409UnlessForced(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	svc := f.gitService("api")
	f.link(svc.ID, conn.Connection.ID, gitproviders.LinkInput{})
	path := "/git/connections/" + conn.Connection.ID.String()

	rec := f.do(f.ctx, http.MethodDelete, path, nil)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "connection_in_use", errCode(t, rec))
	_, err := f.svc.GetConnection(f.ctx, conn.Connection.ID)
	require.NoError(t, err, "the connection survives a refused delete")

	rec = f.do(f.ctx, http.MethodDelete, path+"?force=true", nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Len(t, f.provider.callsTo("DELETE", "/api/v1/repos/acme/shop/hooks/7"), 1)
}

func TestUpdateConnectionTokenAndClientSecretRules(t *testing.T) {
	f := newFixture(t)
	res := f.giteaConnection()
	path := "/git/connections/" + res.Connection.ID.String()

	rec := f.do(f.ctx, http.MethodPatch, path, map[string]any{"clientSecret": "s3cret"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "not_applicable", errCode(t, rec), "only OAuth connections have a client secret")

	rec = f.do(f.ctx, http.MethodPatch, path, map[string]any{"token": "  "})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "validation_failed", errCode(t, rec))

	f.provider.json("GET /api/v1/user", 401, map[string]string{"message": "no"})
	rec = f.do(f.ctx, http.MethodPatch, path, map[string]any{"token": "gta_new_token_9876543"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "credentials_invalid", errCode(t, rec))

	f.provider.json("GET /api/v1/user", 200, map[string]string{"login": "bob"})
	rec = f.do(f.ctx, http.MethodPatch, path, map[string]any{"token": "gta_new_token_9876543"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "bob", decodeJSON[gitproviders.ConnectionView](t, rec).AccountLogin, "the account follows the new token")

	// A rename to a name another connection uses is a conflict.
	f.provider.json("GET /api/v1/user", 200, map[string]string{"login": "alice"})
	other, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{
		Provider: gitproviders.Gitea, Name: "Other", BaseURL: f.provider.srv.URL, AuthType: gitproviders.AuthToken, Token: "gta_other_token_1234567",
	})
	require.NoError(t, err)
	rec = f.do(f.ctx, http.MethodPatch, "/git/connections/"+other.Connection.ID.String(), map[string]any{"name": res.Connection.Name})
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "name_conflict", errCode(t, rec))
}

func TestOAuthConnectionEndpoints(t *testing.T) {
	f := newFixture(t)
	f.provider.json("GET /api/v4/user", 200, map[string]string{"username": "alice"})
	rec := f.do(f.ctx, http.MethodPost, "/git/connections", map[string]any{
		"provider": "gitlab", "name": "GL", "baseUrl": f.provider.srv.URL, "authType": "oauth", "clientId": "app-1", "clientSecret": "secret-1",
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	created := decodeJSON[gitproviders.CreateConnectionResult](t, rec)
	require.Equal(t, gitproviders.StatusPending, created.Connection.Status)
	require.NotContains(t, rec.Body.String(), "secret-1")
	path := "/git/connections/" + created.Connection.ID.String()

	rec = f.do(f.ctx, http.MethodPost, path+"/oauth/start", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	u, err := url.Parse(decodeJSON[struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}](t, rec).AuthorizeURL)
	require.NoError(t, err)
	require.Equal(t, "/oauth/authorize", u.Path)
	require.Equal(t, "app-1", u.Query().Get("client_id"))
	require.NotEmpty(t, u.Query().Get("state"))
	oldURL, err := url.Parse(created.AuthorizeURL)
	require.NoError(t, err)
	require.NotEqual(t, oldURL.Query().Get("state"), u.Query().Get("state"), "restarting issues a fresh state")

	// Replacing the client secret works for OAuth connections only.
	rec = f.do(f.ctx, http.MethodPatch, path, map[string]any{"clientSecret": "secret-2"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "secret-2")
	rec = f.do(f.ctx, http.MethodPatch, path, map[string]any{"clientSecret": "  "})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "validation_failed", errCode(t, rec))
	rec = f.do(f.ctx, http.MethodPatch, path, map[string]any{"token": "tok"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "not_applicable", errCode(t, rec), "an OAuth connection has no access token to replace")

	// A token connection cannot start an OAuth flow.
	gitea := f.giteaConnection()
	rec = f.do(f.ctx, http.MethodPost, "/git/connections/"+gitea.Connection.ID.String()+"/oauth/start", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "not_applicable", errCode(t, rec))
}

func TestOAuthNeedsAPublicURL(t *testing.T) {
	cfg := &config.Config{}
	f := newFixture(t, func(d *gitproviders.Deps) { d.Config = cfg })
	create := map[string]any{"provider": "gitlab", "name": "GL", "baseUrl": f.provider.srv.URL, "authType": "oauth", "clientId": "c", "clientSecret": "s"}

	rec := f.do(f.ctx, http.MethodPost, "/git/connections", create)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "public_url_required", errCode(t, rec), "the OAuth callback address is unknown")

	// The address disappears after the connection was made: restarting the flow says so.
	cfg.PublicURL = "https://nebula.example.com"
	rec = f.do(f.ctx, http.MethodPost, "/git/connections", create)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	id := decodeJSON[gitproviders.CreateConnectionResult](t, rec).Connection.ID
	cfg.PublicURL = ""
	rec = f.do(f.ctx, http.MethodPost, "/git/connections/"+id.String()+"/oauth/start", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "public_url_required", errCode(t, rec))
}

func TestRotateWebhookSecretOverHTTP(t *testing.T) {
	f := newFixture(t)
	bb, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{Provider: gitproviders.Bitbucket, Name: "bb"})
	require.NoError(t, err)

	rec := f.do(f.ctx, http.MethodPost, "/git/connections/"+bb.Connection.ID.String()+"/webhook-secret/rotate", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	next := decodeJSON[struct {
		WebhookSecret string `json:"webhookSecret"`
	}](t, rec).WebhookSecret
	require.NotEmpty(t, next)
	require.NotEqual(t, bb.WebhookSecret, next)

	gt := f.giteaConnection()
	rec = f.do(f.ctx, http.MethodPost, "/git/connections/"+gt.Connection.ID.String()+"/webhook-secret/rotate", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "hooks registered through the API manage their own secret")
}

func TestRepoPickerOverHTTP(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	path := "/git/connections/" + conn.Connection.ID.String()
	f.provider.json("GET /api/v1/user/repos", 200, []map[string]any{
		{"full_name": "acme/shop", "clone_url": "https://git.example.com/acme/shop.git", "default_branch": "main", "private": true},
		{"full_name": "acme/docs", "clone_url": "https://git.example.com/acme/docs.git", "default_branch": "main", "description": "Shop documentation"},
		{"full_name": "alice/dotfiles", "clone_url": "https://git.example.com/alice/dotfiles.git", "default_branch": "master"},
	})

	rec := f.do(f.ctx, http.MethodGet, path+"/repos?q=shop", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	repos := decodeJSON[struct {
		Items []gitproviders.Repo `json:"items"`
	}](t, rec)
	require.Len(t, repos.Items, 2, "the query matches names and descriptions")

	rec = f.do(f.ctx, http.MethodGet, path+"/repos?limit=1", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, decodeJSON[struct {
		Items []gitproviders.Repo `json:"items"`
	}](t, rec).Items, 1)

	require.Equal(t, http.StatusUnprocessableEntity, f.do(f.ctx, http.MethodGet, path+"/repos?limit=0", nil).Code, "the limit is validated")
	require.Equal(t, http.StatusUnprocessableEntity, f.do(f.ctx, http.MethodGet, path+"/repos?limit=500", nil).Code)

	f.provider.json("GET /api/v1/user/repos", 500, map[string]string{"message": "boom"})
	rec = f.do(f.ctx, http.MethodGet, path+"/repos", nil)
	require.Equal(t, http.StatusBadGateway, rec.Code, "a provider failure is a 502")
	require.Equal(t, "provider_error", errCode(t, rec))

	f.provider.json("GET /api/v1/repos/acme/shop/branches", 200, []map[string]string{{"name": "main"}, {"name": "develop"}})
	rec = f.do(f.ctx, http.MethodGet, path+"/branches?repo=acme/shop", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"main", "develop"}, decodeJSON[struct {
		Items []string `json:"items"`
	}](t, rec).Items)

	require.Equal(t, http.StatusUnprocessableEntity, f.do(f.ctx, http.MethodGet, path+"/branches", nil).Code, "repo is required")
	rec = f.do(f.ctx, http.MethodGet, path+"/branches?repo=not-a-repo", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "validation_failed", errCode(t, rec))
	rec = f.do(f.ctx, http.MethodGet, path+"/branches?repo=acme/gone", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "repo_not_found", errCode(t, rec), "an inaccessible repository is named, not a 502")
}

func TestRepoPickerIsUnavailableForBitbucket(t *testing.T) {
	f := newFixture(t)
	bb, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{Provider: gitproviders.Bitbucket, Name: "bb"})
	require.NoError(t, err)
	path := "/git/connections/" + bb.Connection.ID.String()

	rec := f.do(f.ctx, http.MethodGet, path+"/repos", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "no_repo_api", errCode(t, rec))
	rec = f.do(f.ctx, http.MethodGet, path+"/branches?repo=team/webapp", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "no_repo_api", errCode(t, rec))
}

func TestDeliveryLogPaginatesNewestFirst(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	base := readTestdata(t, "gitea_push.json")
	for _, id := range []string{"d-1", "d-2", "d-3"} {
		// Each payload differs, so none is a replay of another.
		body := bytes.Replace(base, []byte("Add services folder"), []byte("Commit "+id), 1)
		code, _ := f.post("gitea", conn.Connection.ID, giteaHeaders(conn.WebhookSecret, body, id), body)
		require.Equal(t, 200, code)
	}
	path := "/git/connections/" + conn.Connection.ID.String() + "/deliveries"

	type page struct {
		Items []gitproviders.WebhookDeliveryView `json:"items"`
		Next  string                             `json:"nextCursor"`
	}
	rec := f.do(f.ctx, http.MethodGet, path+"?limit=2", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	first := decodeJSON[page](t, rec)
	require.Len(t, first.Items, 2)
	require.Equal(t, "d-3", first.Items[0].DeliveryID, "newest first")
	require.Equal(t, "d-2", first.Items[1].DeliveryID)
	require.Equal(t, "processed", first.Items[0].Outcome)
	require.Equal(t, 200, first.Items[0].HTTPStatus)
	require.NotEmpty(t, first.Next)

	rec = f.do(f.ctx, http.MethodGet, path+"?limit=2&cursor="+first.Next, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	second := decodeJSON[page](t, rec)
	require.Len(t, second.Items, 1)
	require.Equal(t, "d-1", second.Items[0].DeliveryID)
	require.Empty(t, second.Next, "the last page has no cursor")

	for _, bad := range []string{"abc", "0", "-5"} {
		rec = f.do(f.ctx, http.MethodGet, path+"?cursor="+bad, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code, bad)
		require.Equal(t, "invalid_cursor", errCode(t, rec), bad)
	}
}

func TestStartGitHubManifestOverHTTP(t *testing.T) {
	f := newFixture(t)

	rec := f.do(f.ctx, http.MethodPost, "/git/github/manifest", map[string]any{"owner": "acme", "name": "Company GitHub"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	start := decodeJSON[gitproviders.ManifestStart](t, rec)
	require.Equal(t, "https://github.com/organizations/acme/settings/apps/new?state="+url.QueryEscape(start.State), start.PostURL)
	require.Equal(t, "https://nebula.example.com/api/v1/git/github/manifest/form?state="+url.QueryEscape(start.State), start.FormURL)
	require.NotEmpty(t, start.State)
	require.Contains(t, start.Manifest, "https://nebula.example.com/hooks/git/github/"+start.ConnectionID.String())
	conn, err := f.svc.GetConnection(f.ctx, start.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, gitproviders.StatusPending, conn.Status)
	require.Equal(t, "Company GitHub", conn.Name)

	rec = f.do(f.ctx, http.MethodPost, "/git/github/manifest", map[string]any{"owner": "bad owner!"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "validation_failed", errCode(t, rec))
}

func TestStartGitHubManifestNeedsAPublicURL(t *testing.T) {
	f := newFixture(t, func(d *gitproviders.Deps) { d.Config = &config.Config{} })
	rec := f.do(f.ctx, http.MethodPost, "/git/github/manifest", map[string]any{})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "public_url_required", errCode(t, rec))
	list, err := f.svc.ListConnections(f.ctx)
	require.NoError(t, err)
	require.Empty(t, list, "nothing is created when the flow cannot work")
}

func TestRefreshGitHubInstallationOverHTTP(t *testing.T) {
	f := newFixture(t)
	_, keyPEM := newAppKey(t)
	gh := f.provider
	gh.json("POST /api/v3/app-manifests/C/conversions", 201, map[string]any{
		"id": 1, "slug": "app", "client_id": "c", "client_secret": "s", "webhook_secret": "w", "pem": keyPEM, "owner": map[string]string{"login": "acme"},
	})
	start, err := f.svc.StartGitHubManifest(f.ctx, gitproviders.ManifestInput{BaseURL: gh.srv.URL})
	require.NoError(t, err)
	_, err = f.svc.CompleteManifest(f.ctx, "C", start.State)
	require.NoError(t, err)
	path := "/git/connections/" + start.ConnectionID.String() + "/refresh"

	// Nothing is installed yet.
	gh.json("GET /api/v3/app/installations", 200, []map[string]any{})
	rec := f.do(f.ctx, http.MethodPost, path, nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "not_installed", errCode(t, rec))

	// GitHub lists the installation: it is found and the connection activates.
	gh.json("GET /api/v3/app/installations", 200, []map[string]any{{"id": 9, "account": map[string]string{"login": "acme"}}})
	gh.json("POST /api/v3/app/installations/9/access_tokens", 201, map[string]any{"token": "ghs_x", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	rec = f.do(f.ctx, http.MethodPost, path, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	view := decodeJSON[gitproviders.ConnectionView](t, rec)
	require.Equal(t, gitproviders.StatusActive, view.Status)
	require.EqualValues(t, 9, view.GitHub.InstallationID)

	// Only GitHub Apps have an installation.
	gt := f.giteaConnection()
	rec = f.do(f.ctx, http.MethodPost, "/git/connections/"+gt.Connection.ID.String()+"/refresh", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "not_applicable", errCode(t, rec))
}

func TestServiceGitLinkOverHTTP(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	svc := f.gitService("api")
	path := "/services/" + svc.ID.String() + "/git"

	rec := f.do(f.ctx, http.MethodGet, path, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.False(t, decodeJSON[gitproviders.GitLinkView](t, rec).Linked, "a new service has no link")

	rec = f.do(f.ctx, http.MethodPut, path, map[string]any{
		"connectionId": conn.Connection.ID, "repo": "acme/shop", "branch": "main", "autoDeploy": true, "watchPaths": []string{"apps/api/**", "!**/*.md"},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	link := decodeJSON[gitproviders.GitLinkView](t, rec)
	require.True(t, link.Linked)
	require.Equal(t, "acme/shop", link.Repo)
	require.Equal(t, "main", link.Branch)
	require.True(t, link.AutoDeploy)
	require.Equal(t, []string{"apps/api/**", "!**/*.md"}, link.WatchPaths)
	require.Equal(t, "registered", link.Webhook.Mode)
	require.Len(t, f.provider.callsTo("POST", "/api/v1/repos/acme/shop/hooks"), 1)

	rec = f.do(f.ctx, http.MethodGet, path, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, link.Repo, decodeJSON[gitproviders.GitLinkView](t, rec).Repo)

	// A second PUT only changes what it names: auto-deploy off keeps the repo and watch paths.
	rec = f.do(f.ctx, http.MethodPut, path, map[string]any{"connectionId": conn.Connection.ID, "repo": "acme/shop", "autoDeploy": false})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	link = decodeJSON[gitproviders.GitLinkView](t, rec)
	require.False(t, link.AutoDeploy)
	require.Equal(t, []string{"apps/api/**", "!**/*.md"}, link.WatchPaths)

	rec = f.do(f.ctx, http.MethodDelete, path, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Len(t, f.provider.callsTo("DELETE", "/api/v1/repos/acme/shop/hooks/7"), 1, "unlinking removes the webhook NebulaCtrl registered")
	rec = f.do(f.ctx, http.MethodGet, path, nil)
	require.False(t, decodeJSON[gitproviders.GitLinkView](t, rec).Linked)
}

func TestServiceGitLinkRejectsBadRequests(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	svc := f.gitService("api")
	path := "/services/" + svc.ID.String() + "/git"
	body := map[string]any{"connectionId": conn.Connection.ID, "repo": "acme/shop"}

	rec := f.do(f.reader, http.MethodPut, path, body)
	require.Equal(t, http.StatusForbidden, rec.Code, "linking needs projects:write")
	rec = f.do(f.reader, http.MethodDelete, path, nil)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, http.StatusOK, f.do(f.reader, http.MethodGet, path, nil).Code, "reading the link needs projects:read only")

	rec = f.do(f.ctx, http.MethodPut, path, map[string]any{"connectionId": conn.Connection.ID, "repo": "ab"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "repo has a minimum length")

	rec = f.do(f.ctx, http.MethodPut, path, map[string]any{"connectionId": uuid.New(), "repo": "acme/shop"})
	require.Equal(t, http.StatusNotFound, rec.Code, "an unknown connection")

	f.provider.json("GET /api/v1/repos/acme/shop", 404, map[string]string{"message": "not found"})
	rec = f.do(f.ctx, http.MethodPut, path, body)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "repo_not_found", errCode(t, rec), "a repository the connection cannot access is refused")
	require.False(t, decodeJSON[gitproviders.GitLinkView](t, f.do(f.ctx, http.MethodGet, path, nil)).Linked)

	missing := "/services/" + uuid.NewString() + "/git"
	require.Equal(t, http.StatusNotFound, f.do(f.ctx, http.MethodGet, missing, nil).Code)
	require.Equal(t, http.StatusNotFound, f.do(f.ctx, http.MethodDelete, missing, nil).Code)
	require.Equal(t, http.StatusNotFound, f.do(f.ctx, http.MethodPut, missing, body).Code)
}

// ---- browser redirects ----

func TestManifestCallbackRedirectsToGitHubInstall(t *testing.T) {
	f := newFixture(t)
	_, keyPEM := newAppKey(t)
	f.provider.json("POST /api/v3/app-manifests/CODE/conversions", 201, map[string]any{
		"id": 7, "slug": "nebula-app", "client_id": "c", "client_secret": "s", "webhook_secret": "w", "pem": keyPEM,
	})
	start, err := f.svc.StartGitHubManifest(f.ctx, gitproviders.ManifestInput{BaseURL: f.provider.srv.URL})
	require.NoError(t, err)

	rec, _ := f.browse(f.ctx, "/git/github/manifest/callback?code=CODE&state="+url.QueryEscape(start.State))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, f.provider.srv.URL+"/apps/nebula-app/installations/new?state="+url.QueryEscape(start.State), rec.Header().Get("Location"),
		"the user goes on to install the App")
}

// manifestFormFields reads the form the manifest page submits.
func manifestFormFields(t *testing.T, body string) (action, manifest, script string) {
	t.Helper()
	field := func(re string) string {
		m := regexp.MustCompile(re).FindStringSubmatch(body)
		require.NotNil(t, m, "%s in %s", re, body)
		return html.UnescapeString(m[1])
	}
	return field(`<form id="manifest" method="post" action="([^"]*)"`), field(`name="manifest" value="([^"]*)"`), field(`(?s)<script>(.*?)</script>`)
}

func TestManifestFormPostsTheManifestToGitHubOnly(t *testing.T) {
	f := newFixture(t)
	start, err := f.svc.StartGitHubManifest(f.ctx, gitproviders.ManifestInput{Owner: "acme", BaseURL: f.provider.srv.URL})
	require.NoError(t, err)

	rec, _ := f.browse(f.ctx, "/git/github/manifest/form?state="+url.QueryEscape(start.State))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	action, manifest, script := manifestFormFields(t, rec.Body.String())
	require.Equal(t, f.provider.srv.URL+"/organizations/acme/settings/apps/new?state="+url.QueryEscape(start.State), action)
	var m struct {
		Hook map[string]any `json:"hook_attributes"`
	}
	require.NoError(t, json.Unmarshal([]byte(manifest), &m), manifest)
	require.Equal(t, "https://nebula.example.com/hooks/git/github/"+start.ConnectionID.String(), m.Hook["url"])

	csp := rec.Header().Get("Content-Security-Policy")
	require.Contains(t, csp, "form-action "+f.provider.srv.URL+";", "the form may go to the flow's GitHub host and nowhere else")
	sum := sha256.Sum256([]byte(script))
	require.Contains(t, csp, "script-src 'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'", "the auto-submit script is allowed by its hash")
	require.NotContains(t, csp, "unsafe-inline")

	// A non-admin, an unknown state and a missing state go back to Settings with the reason.
	_, q := f.browse(f.reader, "/git/github/manifest/form?state="+url.QueryEscape(start.State))
	require.NotEmpty(t, q.Get("git_error"))
	_, q = f.browse(f.ctx, "/git/github/manifest/form?state=nope")
	require.Contains(t, q.Get("git_error"), "expired or was already completed")
	_, q = f.browse(f.ctx, "/git/github/manifest/form")
	require.Contains(t, q.Get("git_error"), "state is required")

	// CSP cannot express an IPv6 literal as a form-action source, so the page
	// explains instead of rendering a form the browser would block.
	v6, err := f.svc.StartGitHubManifest(f.ctx, gitproviders.ManifestInput{BaseURL: "https://[2001:db8::1]"})
	require.NoError(t, err)
	rec, q = f.browse(f.ctx, "/git/github/manifest/form?state="+url.QueryEscape(v6.State))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Contains(t, q.Get("git_error"), "IPv6")
}

func TestRestartGitHubManifestReplacesTheStateUntilTheAppExists(t *testing.T) {
	f := newFixture(t)
	_, keyPEM := newAppKey(t)
	f.provider.json("POST /api/v3/app-manifests/CODE/conversions", 201, map[string]any{
		"id": 7, "slug": "nebula-app", "client_id": "c", "client_secret": "s", "webhook_secret": "w", "pem": keyPEM,
	})
	start, err := f.svc.StartGitHubManifest(f.ctx, gitproviders.ManifestInput{Owner: "acme", BaseURL: f.provider.srv.URL})
	require.NoError(t, err)
	path := "/git/connections/" + start.ConnectionID.String() + "/github/manifest"

	require.Equal(t, http.StatusForbidden, f.do(f.reader, http.MethodPost, path, nil).Code)
	rec := f.do(f.ctx, http.MethodPost, path, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	again := decodeJSON[gitproviders.ManifestStart](t, rec)
	require.Equal(t, start.ConnectionID, again.ConnectionID)
	require.NotEqual(t, start.State, again.State)
	require.Equal(t, f.provider.srv.URL+"/organizations/acme/settings/apps/new?state="+url.QueryEscape(again.State), again.PostURL, "the owner is kept")
	list, err := f.svc.ListConnections(f.ctx)
	require.NoError(t, err)
	require.Len(t, list, 1, "restarting creates no second connection")

	_, q := f.browse(f.ctx, "/git/github/manifest/form?state="+url.QueryEscape(start.State))
	require.Contains(t, q.Get("git_error"), "expired or was already completed", "the old state stops working")
	rec, _ = f.browse(f.ctx, "/git/github/manifest/form?state="+url.QueryEscape(again.State))
	require.Equal(t, http.StatusOK, rec.Code)

	// Once GitHub created the App, the form and the restart are refused: install it instead.
	_, err = f.svc.CompleteManifest(f.ctx, "CODE", again.State)
	require.NoError(t, err)
	_, q = f.browse(f.ctx, "/git/github/manifest/form?state="+url.QueryEscape(again.State))
	require.Contains(t, q.Get("git_error"), "already created")
	rec = f.do(f.ctx, http.MethodPost, path, nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "app_exists", errCode(t, rec))

	bb, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{Provider: gitproviders.Bitbucket, Name: "bb"})
	require.NoError(t, err)
	rec = f.do(f.ctx, http.MethodPost, "/git/connections/"+bb.Connection.ID.String()+"/github/manifest", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "not_applicable", errCode(t, rec))
}

func TestManifestCallbackFailureShowsTheErrorOnTheSettingsPage(t *testing.T) {
	f := newFixture(t)

	rec, q := f.browse(f.ctx, "/git/github/manifest/callback?code=CODE&state=unknown")
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Contains(t, rec.Header().Get("Location"), "https://nebula.example.com/settings/integrations?")
	require.Contains(t, q.Get("git_error"), "expired or was already completed", "the apierr message is shown")

	_, q = f.browse(f.ctx, "/git/github/manifest/callback")
	require.Contains(t, q.Get("git_error"), "code and state are required")

	_, q = f.browse(f.reader, "/git/github/manifest/callback?code=CODE&state=x")
	require.NotEmpty(t, q.Get("git_error"), "a non-admin gets the error, not a connection")
}

func TestInstalledCallbackActivatesOrExplains(t *testing.T) {
	f := newFixture(t)
	_, keyPEM := newAppKey(t)
	gh := f.provider
	gh.json("POST /api/v3/app-manifests/C/conversions", 201, map[string]any{
		"id": 1, "slug": "app", "client_id": "c", "client_secret": "s", "webhook_secret": "w", "pem": keyPEM,
	})
	start, err := f.svc.StartGitHubManifest(f.ctx, gitproviders.ManifestInput{BaseURL: gh.srv.URL})
	require.NoError(t, err)
	_, err = f.svc.CompleteManifest(f.ctx, "C", start.State)
	require.NoError(t, err)
	st := url.QueryEscape(start.State)

	// An organization that must approve the installation: a notice, still pending.
	rec, q := f.browse(f.ctx, "/git/github/installed?state="+st+"&setup_action=request")
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Contains(t, q.Get("git_notice"), "approve")
	require.Empty(t, q.Get("git_connected"))
	conn, err := f.svc.GetConnection(f.ctx, start.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, gitproviders.StatusPending, conn.Status)

	// A failing token mint is reported as an error on the page.
	gh.json("POST /api/v3/app/installations/555/access_tokens", 401, map[string]string{"message": "Bad credentials"})
	_, q = f.browse(f.ctx, "/git/github/installed?state="+st+"&installation_id=555&setup_action=install")
	require.Contains(t, q.Get("git_error"), "token could not be created")

	// The installation succeeds: the connection id is handed to the page.
	gh.json("POST /api/v3/app/installations/555/access_tokens", 201, map[string]any{"token": "ghs_x", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	start2, err := f.svc.StartGitHubManifest(f.ctx, gitproviders.ManifestInput{BaseURL: gh.srv.URL, Name: "Second"})
	require.NoError(t, err)
	_, err = f.svc.CompleteManifest(f.ctx, "C", start2.State)
	require.NoError(t, err)
	_, q = f.browse(f.ctx, "/git/github/installed?state="+url.QueryEscape(start2.State)+"&installation_id=555&setup_action=install")
	require.Equal(t, start2.ConnectionID.String(), q.Get("git_connected"))
	conn, err = f.svc.GetConnection(f.ctx, start2.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, gitproviders.StatusActive, conn.Status)

	// An unknown state is explained.
	_, q = f.browse(f.ctx, "/git/github/installed?state=nope&installation_id=1")
	require.Contains(t, q.Get("git_error"), "expired or was already completed")
}

func TestOAuthCallbackCompletesAndReportsFailures(t *testing.T) {
	f := newFixture(t)
	gl := f.provider
	gl.on("POST /oauth/token", func(w http.ResponseWriter, r *http.Request, body []byte) {
		values, _ := url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		if values.Get("code") == "bad-code" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"leaky detail"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "refresh_token": "rt", "expires_in": 7200, "token_type": "Bearer"})
	})
	gl.json("GET /api/v4/user", 200, map[string]string{"username": "alice"})
	res, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{
		Provider: gitproviders.GitLab, Name: "GL", BaseURL: gl.srv.URL, AuthType: "oauth", ClientID: "c", ClientSecret: "s",
	})
	require.NoError(t, err)
	au, err := url.Parse(res.AuthorizeURL)
	require.NoError(t, err)
	state := au.Query().Get("state")

	// The provider refused the grant: the page shows our message, never the provider's body.
	rec, q := f.browse(f.ctx, "/git/oauth/callback?code=bad-code&state="+url.QueryEscape(state))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Contains(t, q.Get("git_error"), "did not accept the authorization")
	require.NotContains(t, q.Get("git_error"), "leaky detail")

	// The state is still valid after a failed exchange: the right code completes the flow.
	au2, err := url.Parse(mustStartOAuth(t, f, res.Connection.ID))
	require.NoError(t, err)
	rec, q = f.browse(f.ctx, "/git/oauth/callback?code=good&state="+url.QueryEscape(au2.Query().Get("state")))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, res.Connection.ID.String(), q.Get("git_connected"))
	conn, err := f.svc.GetConnection(f.ctx, res.Connection.ID)
	require.NoError(t, err)
	require.Equal(t, gitproviders.StatusActive, conn.Status)
	require.Equal(t, "alice", conn.AccountLogin)

	_, q = f.browse(f.ctx, "/git/oauth/callback?code=good&state="+url.QueryEscape(state))
	require.Contains(t, q.Get("git_error"), "expired or was already completed")

	// The provider's own error is relayed, bounded.
	_, q = f.browse(f.ctx, "/git/oauth/callback?error=access_denied&error_description=user+said+no")
	require.Contains(t, q.Get("git_error"), "access_denied")
	require.Contains(t, q.Get("git_error"), "user said no")
	long := "/git/oauth/callback?error=" + url.QueryEscape(string(bytes.Repeat([]byte("x"), 1000)))
	_, q = f.browse(f.ctx, long)
	require.LessOrEqual(t, len([]rune(q.Get("git_error"))), 300)
}

func mustStartOAuth(t *testing.T, f *fixture, id uuid.UUID) string {
	t.Helper()
	u, err := f.svc.StartOAuth(f.ctx, id)
	require.NoError(t, err)
	return u
}
