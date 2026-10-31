package app_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/app"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

const testPassword = "correct horse battery"

// harness is a running control plane against a fresh schema.
type harness struct {
	t      *testing.T
	srv    *httptest.Server
	app    *app.App
	client *http.Client // keeps cookies (the browser)
}

func newHarness(t *testing.T) *harness { return newHarnessCfg(t, nil) }

// newHarnessCfg starts a harness after letting tweak adjust the config.
func newHarnessCfg(t *testing.T, tweak func(*config.Config)) *harness {
	t.Helper()
	pool := testutil.NewPool(t)
	cfg := &config.Config{SecretKey: bytes.Repeat([]byte{7}, 32), Listen: ":0"}
	if tweak != nil {
		tweak(cfg)
	}
	a, err := app.Build(cfg, pool, nil)
	require.NoError(t, err)
	t.Cleanup(a.Close)
	srv := httptest.NewServer(a.Handler)
	t.Cleanup(srv.Close)
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	// Every sealed value the test created must open under the context its row
	// implies (secretbox registry): call sites and registry agree.
	t.Cleanup(func() { testutil.RequireSealedValuesOpen(t, pool, cfg.SecretKey) })
	return &harness{t: t, srv: srv, app: a, client: &http.Client{Jar: jar}}
}

type resp struct {
	Status int
	Header http.Header
	Raw    []byte
}

func (r resp) JSON() map[string]any {
	var m map[string]any
	if err := json.Unmarshal(r.Raw, &m); err != nil {
		panic(string(r.Raw))
	}
	return m
}

// do issues a request. token == "" uses the cookie jar (session).
func (h *harness) do(method, path, token string, body any, headers ...string) resp {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(h.t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, h.srv.URL+"/api/v1"+path, rd)
	require.NoError(h.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	c := h.client
	if token != "" {
		c = &http.Client{} // no cookies: pure bearer
	}
	res, err := c.Do(req)
	require.NoError(h.t, err)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	require.NoError(h.t, err)
	return resp{Status: res.StatusCode, Header: res.Header, Raw: raw}
}

func (h *harness) setup() {
	h.t.Helper()
	r := h.do("POST", "/setup", "", map[string]any{"email": "admin@example.com", "password": testPassword, "name": "Admin"})
	require.Equal(h.t, http.StatusCreated, r.Status, string(r.Raw))
}

func (h *harness) createProject(name string) string {
	h.t.Helper()
	r := h.do("POST", "/projects", "", map[string]any{"name": name})
	require.Equal(h.t, http.StatusCreated, r.Status, string(r.Raw))
	return r.JSON()["id"].(string)
}

func (h *harness) createToken(scopes []string, projectIDs []string) string {
	h.t.Helper()
	body := map[string]any{"name": "test", "scopes": scopes}
	if projectIDs != nil {
		body["projectIds"] = projectIDs
	}
	r := h.do("POST", "/tokens", "", body)
	require.Equal(h.t, http.StatusCreated, r.Status, string(r.Raw))
	tok := r.JSON()["token"].(string)
	require.True(h.t, strings.HasPrefix(tok, "nct_"))
	return tok
}

func TestSetupReturns409OnceUserExists(t *testing.T) {
	h := newHarness(t)

	r := h.do("GET", "/setup", "", nil)
	require.Equal(t, 200, r.Status)
	require.Equal(t, true, r.JSON()["needsSetup"])

	h.setup()

	r = h.do("GET", "/setup", "", nil)
	require.Equal(t, false, r.JSON()["needsSetup"])

	r = h.do("POST", "/setup", "", map[string]any{"email": "second@example.com", "password": testPassword, "name": "Second"})
	require.Equal(t, http.StatusConflict, r.Status)
	require.Equal(t, "already_setup", r.JSON()["code"])
	require.Contains(t, r.Header.Get("Content-Type"), "application/problem+json")

	// Setup logged the first user in.
	r = h.do("GET", "/me", "", nil)
	require.Equal(t, 200, r.Status, string(r.Raw))
	require.Equal(t, "admin@example.com", r.JSON()["email"])
}

func TestSessionLifecycle(t *testing.T) {
	h := newHarness(t)
	require.Equal(t, 401, h.do("GET", "/me", "", nil).Status)
	h.setup()

	cookie := ""
	for _, c := range h.client.Jar.Cookies(mustURL(h.srv.URL)) {
		if c.Name == "nebulactrl_session" {
			cookie = c.Value
		}
	}
	require.NotEmpty(t, cookie)

	require.Equal(t, 204, h.do("POST", "/auth/logout", "", nil).Status)
	require.Equal(t, 401, h.do("GET", "/me", "", nil).Status)

	bad := h.do("POST", "/auth/login", "", map[string]any{"email": "admin@example.com", "password": "wrong password!!"})
	require.Equal(t, 401, bad.Status)
	require.Equal(t, "invalid_credentials", bad.JSON()["code"])

	ok := h.do("POST", "/auth/login", "", map[string]any{"email": "ADMIN@example.com", "password": testPassword})
	require.Equal(t, 200, ok.Status, string(ok.Raw))
	require.Equal(t, 200, h.do("GET", "/me", "", nil).Status)
}

func TestLoginRateLimited(t *testing.T) {
	h := newHarness(t)
	h.setup()
	var last resp
	for i := 0; i < 12; i++ {
		last = h.do("POST", "/auth/login", "", map[string]any{"email": "admin@example.com", "password": "wrong password!!"})
	}
	require.Equal(t, http.StatusTooManyRequests, last.Status)
	require.Equal(t, "rate_limited", last.JSON()["code"])
}

func TestTokenWithoutScopeGets403(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.createProject("Alpha")

	tok := h.createToken([]string{"servers:read"}, nil)
	r := h.do("GET", "/projects", tok, nil)
	require.Equal(t, http.StatusForbidden, r.Status, string(r.Raw))
	require.Equal(t, "forbidden", r.JSON()["code"])

	// Read scope cannot write.
	readTok := h.createToken([]string{"projects:read"}, nil)
	require.Equal(t, 200, h.do("GET", "/projects", readTok, nil).Status)
	require.Equal(t, 403, h.do("POST", "/projects", readTok, map[string]any{"name": "Nope"}).Status)

	// Non-admin tokens cannot manage tokens or read audit events.
	require.Equal(t, 403, h.do("GET", "/tokens", readTok, nil).Status)
	require.Equal(t, 403, h.do("GET", "/audit-events", readTok, nil).Status)

	// admin implies every scope.
	adminTok := h.createToken([]string{"admin"}, nil)
	require.Equal(t, 200, h.do("GET", "/projects", adminTok, nil).Status)
}

func TestTokenRestrictedToProjectA(t *testing.T) {
	h := newHarness(t)
	h.setup()
	a := h.createProject("Project A")
	b := h.createProject("Project B")

	tok := h.createToken([]string{"projects:read", "projects:write"}, []string{a})

	require.Equal(t, 200, h.do("GET", "/projects/"+a+"/environments", tok, nil).Status)

	r := h.do("GET", "/projects/"+b+"/environments", tok, nil)
	require.Equal(t, http.StatusNotFound, r.Status, string(r.Raw))
	require.Equal(t, "not_found", r.JSON()["code"])
	// A truly missing project is indistinguishable from a forbidden one.
	missing := h.do("GET", "/projects/"+uuid.NewString()+"/environments", tok, nil)
	require.Equal(t, r.JSON()["detail"], missing.JSON()["detail"])

	require.Equal(t, 404, h.do("PATCH", "/projects/"+b, tok, map[string]any{"name": "x"}).Status)
	require.Equal(t, 404, h.do("DELETE", "/projects/"+b, tok, nil).Status)
	require.Equal(t, 404, h.do("POST", "/projects/"+b+"/environments", tok, map[string]any{"name": "dev"}).Status)

	list := h.do("GET", "/projects", tok, nil).JSON()["items"].([]any)
	require.Len(t, list, 1)
	require.Equal(t, a, list[0].(map[string]any)["id"])

	// A restricted token cannot mint projects it could not see.
	require.Equal(t, 403, h.do("POST", "/projects", tok, map[string]any{"name": "C"}).Status)
}

func TestRevokedAndExpiredTokensRejected(t *testing.T) {
	h := newHarness(t)
	h.setup()
	adminTok := h.createToken([]string{"admin"}, nil)

	created := h.do("POST", "/tokens", adminTok, map[string]any{"name": "short", "scopes": []string{"projects:read"}})
	require.Equal(t, 201, created.Status, string(created.Raw))
	tok := created.JSON()["token"].(string)
	id := created.JSON()["id"].(string)
	require.Len(t, created.JSON()["prefix"], 8)
	require.Equal(t, 200, h.do("GET", "/projects", tok, nil).Status)

	// The secret is never returned by list.
	list := h.do("GET", "/tokens", adminTok, nil)
	require.NotContains(t, string(list.Raw), tok)

	require.Equal(t, 204, h.do("DELETE", "/tokens/"+id, adminTok, nil).Status)
	r := h.do("GET", "/projects", tok, nil)
	require.Equal(t, 401, r.Status)
	require.Equal(t, "invalid_token", r.JSON()["code"])

	// Expiry in the past is rejected at creation; force expiry directly.
	bad := h.do("POST", "/tokens", adminTok, map[string]any{"name": "x", "scopes": []string{"projects:read"}, "expiresAt": "2001-01-01T00:00:00Z"})
	require.Equal(t, 422, bad.Status)
	tok2 := h.createToken([]string{"projects:read"}, nil)
	_, err := h.app.Pool.Exec(context.Background(), "UPDATE api_tokens SET expires_at = now() - interval '1 minute' WHERE prefix = $1", tok2[4:12])
	require.NoError(t, err)
	require.Equal(t, 401, h.do("GET", "/projects", tok2, nil).Status)

	require.Equal(t, 401, h.do("GET", "/projects", "nct_garbage", nil).Status)
	unknownScope := h.do("POST", "/tokens", adminTok, map[string]any{"name": "x", "scopes": []string{"everything"}})
	require.Equal(t, 422, unknownScope.Status)
	require.Equal(t, "invalid_scope", unknownScope.JSON()["code"])
}

func TestIdempotencyReplayReturnsIdenticalBody(t *testing.T) {
	h := newHarness(t)
	h.setup()

	first := h.do("POST", "/projects", "", map[string]any{"name": "Once"}, "Idempotency-Key", "key-1")
	require.Equal(t, 201, first.Status, string(first.Raw))
	require.Empty(t, first.Header.Get("Idempotent-Replayed"))

	second := h.do("POST", "/projects", "", map[string]any{"name": "Once"}, "Idempotency-Key", "key-1")
	require.Equal(t, 201, second.Status)
	require.Equal(t, string(first.Raw), string(second.Raw), "replay must be byte-identical")
	require.Equal(t, "true", second.Header.Get("Idempotent-Replayed"))

	items := h.do("GET", "/projects", "", nil).JSON()["items"].([]any)
	require.Len(t, items, 1)

	// Same key, different endpoint: refused.
	reuse := h.do("POST", "/projects/"+first.JSON()["id"].(string)+"/environments", "", map[string]any{"name": "dev"}, "Idempotency-Key", "key-1")
	require.Equal(t, 422, reuse.Status)
	require.Equal(t, "idempotency_key_reused", reuse.JSON()["code"])

	// A different key creates a different project.
	third := h.do("POST", "/projects", "", map[string]any{"name": "Once"}, "Idempotency-Key", "key-2")
	require.Equal(t, 201, third.Status)
	require.NotEqual(t, first.JSON()["id"], third.JSON()["id"])
	require.Equal(t, "once-2", third.JSON()["slug"])

	// Concurrent duplicates serialise to a single creation.
	const n = 6
	ids := make(chan string, n)
	for i := 0; i < n; i++ {
		go func() {
			r := h.do("POST", "/projects", "", map[string]any{"name": "Racy"}, "Idempotency-Key", "key-race")
			ids <- r.JSON()["id"].(string)
		}()
	}
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		seen[<-ids] = true
	}
	require.Len(t, seen, 1)
}

func TestProjectsAndEnvironments(t *testing.T) {
	h := newHarness(t)
	h.setup()
	id := h.createProject("My Cool App!")

	proj := h.do("GET", "/projects/"+id, "", nil).JSON()
	require.Equal(t, "my-cool-app", proj["slug"])

	envs := h.do("GET", "/projects/"+id+"/environments", "", nil).JSON()["items"].([]any)
	require.Len(t, envs, 1)
	prod := envs[0].(map[string]any)
	require.Equal(t, "production", prod["slug"])
	require.Equal(t, true, prod["isProduction"])

	st := h.do("POST", "/projects/"+id+"/environments", "", map[string]any{"name": "Staging"})
	require.Equal(t, 201, st.Status, string(st.Raw))
	require.Equal(t, "staging", st.JSON()["slug"])
	dup := h.do("POST", "/projects/"+id+"/environments", "", map[string]any{"name": "staging"})
	require.Equal(t, "staging-2", dup.JSON()["slug"])

	upd := h.do("PATCH", "/environments/"+st.JSON()["id"].(string), "", map[string]any{"name": "QA"})
	require.Equal(t, 200, upd.Status, string(upd.Raw))
	require.Equal(t, "QA", upd.JSON()["name"])
	require.Equal(t, "staging", upd.JSON()["slug"], "slug is stable across renames")

	p2 := h.do("PATCH", "/projects/"+id, "", map[string]any{"description": "hello"})
	require.Equal(t, "hello", p2.JSON()["description"])
	require.Equal(t, "My Cool App!", p2.JSON()["name"])

	require.Equal(t, 204, h.do("DELETE", "/environments/"+st.JSON()["id"].(string), "", nil).Status)
	require.Equal(t, 204, h.do("DELETE", "/projects/"+id, "", nil).Status)
	require.Equal(t, 404, h.do("GET", "/projects/"+id, "", nil).Status)

	// Validation errors use problem+json with field details.
	bad := h.do("POST", "/projects", "", map[string]any{"name": ""})
	require.Equal(t, 422, bad.Status)
	require.Equal(t, "validation_failed", bad.JSON()["code"])
	require.NotEmpty(t, bad.JSON()["errors"])
}

func TestPaginationAndAudit(t *testing.T) {
	h := newHarness(t)
	h.setup()
	for _, n := range []string{"p1", "p2", "p3"} {
		h.createProject(n)
	}
	page1 := h.do("GET", "/projects?limit=2", "", nil).JSON()
	require.Len(t, page1["items"], 2)
	next, _ := page1["nextCursor"].(string)
	require.NotEmpty(t, next)
	page2 := h.do("GET", "/projects?limit=2&cursor="+next, "", nil).JSON()
	require.Len(t, page2["items"], 1)
	require.Nil(t, page2["nextCursor"])
	require.Equal(t, 400, h.do("GET", "/projects?cursor=garbage", "", nil).Status)

	a1 := h.do("GET", "/audit-events?limit=3", "", nil).JSON()
	items := a1["items"].([]any)
	require.Len(t, items, 3)
	first := items[0].(map[string]any)
	require.Equal(t, "environment.create", first["action"])
	require.Equal(t, "user", first["actorType"])
	require.NotEmpty(t, a1["nextCursor"])
}

func TestAuditRecordsTokenActor(t *testing.T) {
	h := newHarness(t)
	h.setup()
	tok := h.createToken([]string{"projects:write"}, nil)
	require.Equal(t, 201, h.do("POST", "/projects", tok, map[string]any{"name": "ByToken"}).Status)
	var actor string
	err := h.app.Pool.QueryRow(context.Background(),
		"SELECT actor_type FROM audit_events WHERE action = 'project.create' ORDER BY id DESC LIMIT 1").Scan(&actor)
	require.NoError(t, err)
	require.Equal(t, "token", actor)
}

func TestCrossOriginBrowserRequestRejected(t *testing.T) {
	h := newHarness(t)
	h.setup()
	r := h.do("POST", "/projects", "", map[string]any{"name": "evil"}, "Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site")
	require.Equal(t, 403, r.Status)
	require.Equal(t, "cross_origin_forbidden", r.JSON()["code"])
	same := h.do("POST", "/projects", "", map[string]any{"name": "fine"}, "Sec-Fetch-Site", "same-origin")
	require.Equal(t, 201, same.Status, string(same.Raw))
}

func TestEventsSSE(t *testing.T) {
	h := newHarness(t)
	h.setup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", h.srv.URL+"/api/v1/events", nil)
	res, err := h.client.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, 200, res.StatusCode)
	require.Equal(t, "text/event-stream", res.Header.Get("Content-Type"))

	id := h.createProject("Streamed")
	buf := make([]byte, 0, 1024)
	chunk := make([]byte, 512)
	for !strings.Contains(string(buf), "project.created") {
		n, err := res.Body.Read(chunk)
		require.NoError(t, err)
		buf = append(buf, chunk[:n]...)
	}
	require.Contains(t, string(buf), "data: {")
	require.Contains(t, string(buf), id)
}

func TestHealthEndpoints(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/healthz", "/readyz"} {
		res, err := http.Get(h.srv.URL + p)
		require.NoError(t, err)
		res.Body.Close()
		require.Equal(t, 200, res.StatusCode, p)
	}
}

func TestOpenAPIBuildsWithoutDatabase(t *testing.T) {
	a, err := app.Build(&config.Config{}, nil, nil)
	require.NoError(t, err)
	defer a.Close()
	raw, err := json.Marshal(a.API.OpenAPI())
	require.NoError(t, err)
	var doc struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.Equal(t, "3.1.0", doc.OpenAPI)
	for _, p := range []string{"/setup", "/auth/login", "/projects", "/tokens", "/audit-events", "/events", "/me"} {
		require.Contains(t, doc.Paths, p)
	}
}

func TestSecretKeyRequired(t *testing.T) {
	_, err := config.Load(func(k string) string {
		if k == "NEBULACTRL_DATABASE_URL" {
			return "postgres://x"
		}
		return ""
	})
	require.ErrorContains(t, err, "NEBULACTRL_SECRET_KEY")
	cfg, err := config.Load(func(k string) string {
		switch k {
		case "NEBULACTRL_DATABASE_URL":
			return "postgres://x"
		case "NEBULACTRL_SECRET_KEY":
			return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
		}
		return ""
	})
	require.NoError(t, err)
	require.Equal(t, ":8080", cfg.Listen)
	require.False(t, cfg.MCPEnabled)
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
