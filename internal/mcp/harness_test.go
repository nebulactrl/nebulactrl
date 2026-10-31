package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/app"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

const (
	testPassword = "correct horse battery"
	canary       = "sekret-canary-12345"
)

// harness is a full control plane (REST via session cookie, MCP via bearer).
type harness struct {
	t      *testing.T
	srv    *httptest.Server
	app    *app.App
	client *http.Client
}

func newHarness(t *testing.T, mcpEnabled bool) *harness {
	t.Helper()
	pool := testutil.NewPool(t)
	cfg := &config.Config{SecretKey: bytes.Repeat([]byte{7}, 32), Listen: ":0", MCPEnabled: mcpEnabled}
	a, err := app.Build(cfg, pool, nil)
	require.NoError(t, err)
	t.Cleanup(a.Close)
	srv := httptest.NewServer(a.Handler)
	t.Cleanup(srv.Close)
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	h := &harness{t: t, srv: srv, app: a, client: &http.Client{Jar: jar}}
	r := h.rest("POST", "/setup", map[string]any{"email": "admin@example.com", "password": testPassword, "name": "Admin"})
	require.Equal(t, http.StatusCreated, r.status, string(r.raw))
	return h
}

type restResp struct {
	status int
	raw    []byte
}

func (r restResp) json() map[string]any {
	var m map[string]any
	if err := json.Unmarshal(r.raw, &m); err != nil {
		panic(string(r.raw))
	}
	return m
}

// rest issues a session-authenticated request under /api/v1.
func (h *harness) rest(method, path string, body any) restResp {
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
	res, err := h.client.Do(req)
	require.NoError(h.t, err)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	require.NoError(h.t, err)
	return restResp{status: res.StatusCode, raw: raw}
}

func (h *harness) token(scopes []string, projectIDs []string) string {
	h.t.Helper()
	return h.tokenOfKind("mcp", scopes, projectIDs)
}

// restToken creates a REST API token: it works on /api/v1 and is refused on /mcp.
func (h *harness) restToken(scopes []string, projectIDs []string) string {
	h.t.Helper()
	return h.tokenOfKind("rest", scopes, projectIDs)
}

func (h *harness) tokenOfKind(kind string, scopes []string, projectIDs []string) string {
	h.t.Helper()
	body := map[string]any{"name": kind + "-test", "kind": kind, "scopes": scopes}
	if projectIDs != nil {
		body["projectIds"] = projectIDs
	}
	r := h.rest("POST", "/tokens", body)
	require.Equal(h.t, http.StatusCreated, r.status, string(r.raw))
	return r.json()["token"].(string)
}

// fixture creates a project, a pending server and an image service.
type fixture struct {
	projectID, envID, serviceID, serverID string
	// projectSlug and serviceSlug build the selector project/production/service.
	projectSlug, serviceSlug string
}

func (f fixture) selector() string { return f.projectSlug + "/production/" + f.serviceSlug }

// service creates a fixture in a non-production environment (the environment
// is still named "production", so the selector is project/production/service):
// MCP changes to a production environment need a person's approval, which most
// tests are not about.
func (h *harness) service(name string) fixture { return h.serviceIn(name, false) }

// prodService is service in an environment flagged production.
func (h *harness) prodService(name string) fixture { return h.serviceIn(name, true) }

func (h *harness) serviceIn(name string, production bool) fixture {
	h.t.Helper()
	r := h.rest("POST", "/projects", map[string]any{"name": "P " + name})
	require.Equal(h.t, http.StatusCreated, r.status, string(r.raw))
	pid, pslug := r.json()["id"].(string), r.json()["slug"].(string)
	r = h.rest("GET", "/projects/"+pid+"/environments", nil)
	require.Equal(h.t, http.StatusOK, r.status, string(r.raw))
	envID := r.json()["items"].([]any)[0].(map[string]any)["id"].(string)
	if !production {
		r = h.rest("PATCH", "/environments/"+envID, map[string]any{"isProduction": false})
		require.Equal(h.t, http.StatusOK, r.status, string(r.raw))
	}
	r = h.rest("POST", "/servers/enrollment-tokens", map[string]any{"name": "srv-" + name})
	require.Equal(h.t, http.StatusCreated, r.status, string(r.raw))
	serverID := r.json()["server"].(map[string]any)["id"].(string)
	r = h.rest("POST", "/environments/"+envID+"/services", map[string]any{
		"name": name, "sourceType": "image", "serverId": serverID,
		"config": map[string]any{"image": "traefik/whoami:latest", "port": 80},
	})
	require.Equal(h.t, http.StatusCreated, r.status, string(r.raw))
	return fixture{
		projectID: pid, envID: envID, serviceID: r.json()["id"].(string), serverID: serverID,
		projectSlug: pslug, serviceSlug: r.json()["slug"].(string),
	}
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// allToolsets selects every toolset, the way most tests want the full surface.
const allToolsets = "?toolsets=core,config,data,servers,observe"

// session connects an SDK client named "test" to url+"/mcp" with every toolset
// listed (the default lists only core and observe; see TestToolsListSizeBudgets).
func session(t *testing.T, url, token string) *sdk.ClientSession {
	t.Helper()
	return sessionAt(t, url+"/mcp"+allToolsets, token)
}

func sessionAt(t *testing.T, endpoint, token string) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Transport: bearer{token: token, next: http.DefaultTransport}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *sdk.ClientSession, tool string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	require.NoError(t, err)
	return res
}

func text(res *sdk.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func structured(t *testing.T, res *sdk.CallToolResult) map[string]any {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m), string(raw))
	return m
}

func errCode(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	require.True(t, res.IsError, "expected a tool error, got %s", text(res))
	var te struct{ Code, Message string }
	require.NoError(t, json.Unmarshal([]byte(text(res)), &te), text(res))
	require.NotEmpty(t, te.Message)
	return te.Code
}

// toolNames lists the tools the session sees.
func toolNames(t *testing.T, cs *sdk.ClientSession) map[string]*sdk.Tool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	out := map[string]*sdk.Tool{}
	for _, tl := range res.Tools {
		out[tl.Name] = tl
	}
	return out
}

// markDeployed gives the service a succeeded deployment and an online server,
// as if the agent had deployed it.
func (h *harness) markDeployed(f fixture) {
	h.t.Helper()
	ctx := context.Background()
	q := db.New(h.app.Pool)
	sid, srv := uuid.MustParse(f.serviceID), uuid.MustParse(f.serverID)
	_, err := h.app.Pool.Exec(ctx, "UPDATE servers SET status = 'online' WHERE id = $1", srv)
	require.NoError(h.t, err)
	row, err := q.GetServiceScopeInternal(ctx, sid)
	require.NoError(h.t, err)
	snap, _ := json.Marshal(services.Snapshot{Config: services.DecodeConfig(row.Service.Config), ServerID: &srv})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceImage, Image: "traefik/whoami:latest"})
	rel, err := q.InsertRelease(ctx, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sid, Source: src, Config: snap, ImageRef: "traefik/whoami:latest"})
	require.NoError(h.t, err)
	dep, err := q.InsertDeployment(ctx, db.InsertDeploymentParams{ServiceID: sid, ReleaseID: &rel.ID, ServerID: &srv, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
	require.NoError(h.t, err)
	_, err = h.app.Pool.Exec(ctx, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(h.t, err)
	require.NoError(h.t, q.MarkServiceDeployed(ctx, db.MarkServiceDeployedParams{ID: sid, CurrentDeploymentID: &dep.ID, AppliedGeneration: row.Service.Generation}))
}
