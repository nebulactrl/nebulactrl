//go:build integration

package integration

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// 8. git deploy: clone, build on the target, release pins commit + image;
// build logs are persisted.
func TestGitDeploy(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	g := gitServer(t)
	s := SharedStack(t)
	svc := s.NewService(SvcSpec{
		Name: "gitapp", GitURL: g.URL, GitBranch: "main", Domain: s.Host(),
		Vars: map[string]string{"VERSION": "from-git"},
	})

	d := s.DeployOK(svc.ID)
	src := d.Obj("release").Obj("source")
	require.Equal(t, g.CommitSHA, src.Str("commitSha"), "release source: %v", src)
	require.Equal(t, g.URL, src.Str("gitUrl"))
	require.NotEmpty(t, src.Str("imageId"), "release source has no image digest: %v", src)
	require.True(t, strings.HasPrefix(src.Str("imageId"), "sha256:"), "%v", src)

	logs := s.DeploymentLogs(d.Str("id"))
	r := s.Do("GET", "/deployments/"+d.Str("id")+"/logs", nil)
	var build int
	for _, it := range r.JSON().Items() {
		if it.Str("stream") == agentprotocol.StreamBuild {
			build++
		}
	}
	require.Positive(t, build, "no persisted build log lines:\n%s", logs)
	NewProxyClient(s.Host()).WaitBody(t, "/", "from-git", 30*time.Second)

	cs := s.RunningContainers(svc.ID)
	require.Len(t, cs, 1)
	img, err := dockerEng.ImageInspect(context.Background(), cs[0].ImageID)
	require.NoError(t, err)
	require.Contains(t, strings.Join(img.RepoTags, ","), "nc-build/"+svc.Slug+":", "image tags: %v", img.RepoTags)
	require.Equal(t, src.Str("imageId"), img.ID)
}

// 9. secrets: the canary never reaches logs, persisted commands, REST, MCP or any table.
func TestSecretsNeverLeak(t *testing.T) {
	requireTier(t, tierSmoke)
	t.Parallel()
	app := appFixture(t)
	s := SharedStack(t)
	svc := s.NewService(SvcSpec{
		Name: "api", Image: app.AppImage, Domain: s.Host(),
		Vars:    map[string]string{"VERSION": "v1", "SECRET_ECHO": "API_KEY"},
		Secrets: map[string]string{"API_KEY": canary},
	})
	d := s.DeployOK(svc.ID)
	require.True(t, eventually(30*time.Second, time.Second, func() bool {
		r := s.Do("GET", "/services/"+svc.ID+"/logs?tail=200", nil)
		return r.Status == http.StatusOK && strings.Contains(string(r.Raw), "startup: API_KEY=")
	}), "app never printed its startup line (the secret must reach the container)")

	// The container really received the secret...
	cs := s.RunningContainers(svc.ID)
	require.Len(t, cs, 1)
	require.Contains(t, cs[0].Env, "API_KEY="+canary)

	// ...and nothing the platform stores or serves contains it.
	for _, path := range []string{
		"/services/" + svc.ID,
		"/services/" + svc.ID + "/variables",
		"/services/" + svc.ID + "/pending-changes",
		"/services/" + svc.ID + "/deployments",
		"/services/" + svc.ID + "/logs?tail=200",
		"/environments/" + s.EnvID + "/services",
		"/deployments/" + d.Str("id"),
		"/deployments/" + d.Str("id") + "/events",
		"/deployments/" + d.Str("id") + "/logs",
		"/audit-events",
	} {
		r := s.Do("GET", path, nil)
		require.Equal(t, http.StatusOK, r.Status, "%s: %s", path, r.Raw)
		requireNoSecret(t, "GET "+path, string(r.Raw))
	}
	r := s.Do("POST", "/services/"+svc.ID+"/deployments/plan", map[string]any{})
	requireNoSecret(t, "plan", string(r.Raw))
	// The variables list still tells the UI the secret exists.
	r = s.Do("GET", "/services/"+svc.ID+"/variables", nil)
	var found bool
	for _, v := range r.JSON().Items() {
		if v.Str("key") == "API_KEY" {
			found = true
			require.True(t, v.Bool("isSecret"))
			require.True(t, v.Bool("set"))
			require.NotContains(t, v, "value")
		}
	}
	require.True(t, found)

	// Deployment and runtime logs contain the redaction mask where the app echoed it.
	require.Contains(t, s.DeploymentLogs(d.Str("id")), "[verify]")
	lr := s.Do("GET", "/services/"+svc.ID+"/logs?tail=200", nil)
	require.Contains(t, string(lr.Raw), "startup: API_KEY=••••••")

	// Every table, including agent_commands.payload/result, deployment_logs, idempotency bodies.
	requireNoSecret(t, "database dump", s.dumpAllTables())
	var payload string
	require.NoError(t, s.Pool.QueryRow(context.Background(),
		`SELECT string_agg(payload::text || coalesce(result::text,''), E'\n') FROM agent_commands`).Scan(&payload))
	require.Contains(t, payload, "API_KEY", "the command lists variable KEYS")
	requireNoSecret(t, "agent_commands", payload)

	// MCP reads of the service (and neighbors). The session also lists the
	// config toolset, where service_get and service_list live.
	tok := s.MCPToken([]string{"projects:read", "servers:read", "logs:read"}, nil)
	cs2 := mcpSession(t, s.URL(), tok)
	for tool, args := range map[string]map[string]any{
		"service_get":     {"serviceId": svc.ID},
		"service_list":    {"environmentId": s.EnvID},
		"deployment_list": {"serviceId": svc.ID},
		"deployment_get":  {"deploymentId": d.Str("id")},
		"tail_logs":       {"service": svc.ID},
		"review_changes":  {"service": svc.ID},
	} {
		res, err := cs2.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
		require.NoError(t, err, tool)
		raw, err := json.Marshal(res)
		require.NoError(t, err)
		requireNoSecret(t, "mcp "+tool, string(raw))
		if tool == "service_get" {
			require.False(t, res.IsError, string(raw))
		}
	}
}

func requireNoSecret(t *testing.T, what, s string) {
	t.Helper()
	require.NotContains(t, s, canary, "%s leaks the secret", what)
	require.NotContains(t, s, hex.EncodeToString([]byte(canary)), "%s leaks the hex-encoded secret", what)
}

func mcpSession(t *testing.T, url, token string) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "itest", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint:             url + "/mcp?toolsets=core,config,observe",
		HTTPClient:           &http.Client{Transport: bearerTransport{token: token}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// 11. reconcile: removing the service container behind the platform's back
// brings a `reconcile` deployment that restores it.
func TestReconcileRestoresMissingContainer(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	s := SharedStack(t)
	svc, px := liveV1(t, s, "web", nil)
	cs := s.RunningContainers(svc.ID)
	require.Len(t, cs, 1)
	original := s.Service(svc.ID).Str("currentDeploymentId")

	require.NoError(t, dockerEng.ContainerRemove(context.Background(), cs[0].ID, true, false))
	removedAt := time.Now()

	var rec J
	ok := eventually(60*time.Second, time.Second, func() bool {
		r := s.Do("GET", "/services/"+svc.ID+"/deployments", nil)
		for _, d := range r.JSON().Items() {
			if d.Str("trigger") == "reconcile" {
				rec = d
				return true
			}
		}
		return false
	})
	require.True(t, ok, "no reconcile deployment within 60s of removing the container")
	t.Logf("reconcile deployment created %s after removal", time.Since(removedAt).Round(time.Second))

	d := s.WaitDeployment(rec.Str("id"), 3*time.Minute)
	require.Equal(t, "succeeded", d.Str("status"), "%v\n%s", d, s.DeploymentLogs(d.Str("id")))
	require.NotEqual(t, original, d.Str("id"))
	require.Equal(t, d.Str("id"), s.Service(svc.ID).Str("currentDeploymentId"))
	require.Len(t, s.RunningContainers(svc.ID), 1)
	px.WaitBody(t, "/", "v1", 30*time.Second)
}

// 13. IDOR: a token restricted to project A cannot see or touch project B.
func TestIDOR(t *testing.T) {
	requireTier(t, tierSmoke)
	t.Parallel()
	app := appFixture(t)
	s := SharedStackN(t, 2) // two environments

	// Project B (the stack's own project is A): a service with a real deployment.
	r := s.Do("POST", "/projects", map[string]any{"name": "B " + s.hostBase})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	projB := r.JSON().Str("id")
	r = s.Do("GET", "/projects/"+projB+"/environments", nil)
	envB := r.JSON().Items()[0].Str("id")
	s.TrackEnv(envB)
	svcB := s.NewService(SvcSpec{Name: "secretsvc", Image: app.AppImage, Env: envB, Vars: map[string]string{"VERSION": "b"}})
	depB := s.DeployOK(svcB.ID).Str("id")
	svcA := s.NewService(SvcSpec{Name: "mine", Image: app.AppImage, Vars: map[string]string{"VERSION": "a"}})

	tok := s.Token([]string{"projects:read", "projects:write", "deployments:write", "deployments:rollback", "servers:read", "logs:read"}, []string{s.ProjectID})

	// Sanity: A's own resources work with this token.
	require.Equal(t, http.StatusOK, s.DoToken(tok, "GET", "/services/"+svcA.ID, nil).Status)
	require.Equal(t, http.StatusOK, s.DoToken(tok, "GET", "/projects/"+s.ProjectID, nil).Status)
	lst := s.DoToken(tok, "GET", "/projects", nil)
	require.Equal(t, http.StatusOK, lst.Status)
	require.NotContains(t, string(lst.Raw), projB, "project list leaks B")

	ifMatch := `"1"` // PATCH needs a generation before the lookup; the 404 must still win
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/projects/" + projB, nil},
		{"GET", "/projects/" + projB + "/environments", nil},
		{"GET", "/environments/" + envB + "/services", nil},
		{"GET", "/services/" + svcB.ID, nil},
		{"PATCH", "/services/" + svcB.ID, map[string]any{"name": "hacked"}},
		{"GET", "/services/" + svcB.ID + "/variables", nil},
		{"PUT", "/services/" + svcB.ID + "/variables", map[string]any{"variables": []map[string]any{{"key": "X", "value": "1", "isSecret": false}}}},
		{"GET", "/services/" + svcB.ID + "/logs?tail=10", nil},
		{"GET", "/services/" + svcB.ID + "/deployments", nil},
		{"POST", "/services/" + svcB.ID + "/deployments", map[string]any{}},
		{"POST", "/services/" + svcB.ID + "/restart", nil},
		{"GET", "/services/" + svcB.ID + "/analytics", nil},
		{"GET", "/deployments/" + depB, nil},
		{"GET", "/deployments/" + depB + "/events", nil},
		{"GET", "/deployments/" + depB + "/logs", nil},
		{"POST", "/deployments/" + depB + "/rollback", nil},
		{"DELETE", "/services/" + svcB.ID + "?confirm=secretsvc", nil},
	} {
		got := s.DoToken(tok, c.method, c.path, c.body, "If-Match", ifMatch)
		require.Equal(t, http.StatusNotFound, got.Status, "%s %s: %s", c.method, c.path, got.Raw)
	}

	// MCP enforces the same boundary (with an MCP token: REST tokens are refused there).
	cs := mcpSession(t, s.URL(), s.MCPToken([]string{"projects:read", "projects:write", "deployments:write", "deployments:rollback", "servers:read", "logs:read"}, []string{s.ProjectID}))
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "service_get", Arguments: map[string]any{"serviceId": svcB.ID}})
	require.NoError(t, err)
	require.True(t, res.IsError, "MCP service_get on another project's service must fail")
	require.Contains(t, mcpText(res), "not_found")

	// B is intact.
	require.Equal(t, "secretsvc", s.Service(svcB.ID).Str("name"))
	require.Len(t, s.RunningContainers(svcB.ID), 1)
}

func mcpText(res *sdk.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}
