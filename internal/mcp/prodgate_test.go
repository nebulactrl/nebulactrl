package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// approvalOf returns the approval id of a confirmation_required result.
func approvalOf(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	require.Equal(t, "confirmation_required", errCode(t, res), text(res))
	var te struct {
		Data struct{ ApprovalID, ApprovalURL string }
	}
	require.NoError(t, json.Unmarshal([]byte(text(res)), &te))
	require.Contains(t, te.Data.ApprovalURL, "/approvals/")
	require.NotEmpty(t, te.Data.ApprovalID)
	return te.Data.ApprovalID
}

func callOperation(t *testing.T, cs *sdk.ClientSession, op string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	return call(t, cs, "call_operation", map[string]any{"operationId": op, "arguments": args})
}

// The operations whose domain service does not ask for an approval itself are
// guarded by the MCP dispatch itself: a cron task, a git link, a clone with
// secrets and cutting a production server off each need a person on
// production, and nothing happens before they approve.
func TestCentralGateGuardsProductionOperationsNoDomainServiceChecks(t *testing.T) {
	h := newHarness(t, true)
	prod := h.prodService("prod")
	h.markDeployed(prod)
	stage := h.service("stage")
	h.markDeployed(stage)
	cs := session(t, h.srv.URL, h.token([]string{"admin"}, nil))

	task := map[string]any{"name": "x", "schedule": "* * * * *", "command": []string{"sh", "-c", "env"}}
	taskOn := func(f fixture) map[string]any {
		args := map[string]any{"serviceId": f.serviceID}
		for k, v := range task {
			args[k] = v
		}
		return args
	}
	countTasks := func() int {
		var n int
		require.NoError(t, h.app.Pool.QueryRow(context.Background(), "SELECT count(*) FROM service_tasks").Scan(&n))
		return n
	}

	// Production: asked, nothing created.
	id := approvalOf(t, callOperation(t, cs, "createServiceTask", taskOn(prod)))
	require.Zero(t, countTasks(), "the task must not exist before a person approves")

	// The approver reads what will run.
	r := h.rest("GET", "/approvals/"+id, nil)
	require.Equal(t, http.StatusOK, r.status, string(r.raw))
	plan := r.json()["plan"].(map[string]any)
	require.Contains(t, plan["summary"], "in production")
	require.Contains(t, strings.Join(toStrings(plan["effects"]), "\n"), `"env"`)

	// A different command is a different request.
	other := taskOn(prod)
	other["command"] = []string{"id"}
	require.NotEqual(t, id, approvalOf(t, callOperation(t, cs, "createServiceTask", other)))

	// A non-production service needs no approval.
	res := callOperation(t, cs, "createServiceTask", taskOn(stage))
	if res.IsError {
		require.NotEqual(t, "confirmation_required", errCode(t, res), text(res))
	}

	// The person approves on the dashboard; the same call then runs, once.
	r = h.rest("POST", "/approvals/"+id+"/approve", nil)
	require.Equal(t, http.StatusOK, r.status, string(r.raw))
	before := countTasks()
	res = callOperation(t, cs, "createServiceTask", taskOn(prod))
	require.False(t, res.IsError, text(res))
	require.Equal(t, before+1, countTasks())
	again := taskOn(prod)
	again["name"] = "y"
	approvalOf(t, callOperation(t, cs, "createServiceTask", again))
	approvalOf(t, callOperation(t, cs, "updateServiceTask", map[string]any{"taskId": firstTask(t, h, prod.serviceID), "enabled": false}))

	// Git link: a push to the linked branch would deploy production.
	link := map[string]any{"serviceId": prod.serviceID, "connectionId": "00000000-0000-4000-8000-000000000001", "repo": "acme/api", "branch": "agent-tmp", "autoDeploy": true}
	approvalOf(t, callOperation(t, cs, "linkServiceGit", link))
	approvalOf(t, callOperation(t, cs, "unlinkServiceGit", map[string]any{"serviceId": prod.serviceID}))

	// Clone copies the secrets into an environment people may run commands in.
	approvalOf(t, callOperation(t, cs, "cloneEnvironment", map[string]any{"environmentId": prod.envID, "name": "copy"}))
	approvalOf(t, callOperation(t, cs, "cloneService", map[string]any{"serviceId": prod.serviceID, "environmentId": stage.envID}))
	res = callOperation(t, cs, "cloneEnvironment", map[string]any{"environmentId": stage.envID, "name": "copy"})
	if res.IsError {
		require.NotEqual(t, "confirmation_required", errCode(t, res), text(res))
	}

	// A server that hosts production cannot be cut off without approval.
	approvalOf(t, callOperation(t, cs, "revokeServer", map[string]any{"serverId": prod.serverID}))
	approvalOf(t, callOperation(t, cs, "disableServerTunnel", map[string]any{"serverId": prod.serverID}))
	var status string
	require.NoError(t, h.app.Pool.QueryRow(context.Background(), "SELECT status FROM servers WHERE id = $1", prod.serverID).Scan(&status))
	require.NotEqual(t, "revoked", status)
}

func firstTask(t *testing.T, h *harness, serviceID string) string {
	t.Helper()
	var id string
	require.NoError(t, h.app.Pool.QueryRow(context.Background(), "SELECT id::text FROM service_tasks WHERE service_id = $1 ORDER BY created_at LIMIT 1", serviceID).Scan(&id))
	return id
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// An approval is only given on the dashboard by a signed-in person. A client
// that declares elicitation is never asked, and nothing it sends can approve.
func TestClientIsNeverAskedToApprove(t *testing.T) {
	h := newHarness(t, true)
	prod := h.prodService("prod")
	h.markDeployed(prod)
	token := h.token([]string{"admin"}, nil)

	var asked atomic.Int32
	client := sdk.NewClient(&sdk.Implementation{Name: "eager", Version: "0"}, &sdk.ClientOptions{
		ElicitationHandler: func(context.Context, *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
			asked.Add(1)
			return &sdk.ElicitResult{Action: "accept", Content: map[string]any{"approve": true}}, nil
		},
	})
	cs, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint: h.srv.URL + "/mcp" + allToolsets, HTTPClient: &http.Client{Transport: bearer{token: token, next: http.DefaultTransport}},
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	for _, res := range []*sdk.CallToolResult{
		call(t, cs, "service_restart", map[string]any{"serviceId": prod.serviceID}),
		callOperation(t, cs, "createServiceTask", map[string]any{"serviceId": prod.serviceID, "name": "x", "schedule": "* * * * *", "command": []string{"id"}}),
	} {
		approvalOf(t, res)
		require.NotContains(t, text(res), "elicit")
	}
	require.Zero(t, asked.Load(), "the client was shown an approval form")
	var n int
	require.NoError(t, h.app.Pool.QueryRow(context.Background(), "SELECT count(*) FROM approvals WHERE status <> 'pending'").Scan(&n))
	require.Zero(t, n, "nothing was approved without a person")
}

// A token is MCP-only or REST-only: each is refused on the other transport, so
// the production guard and the deny list cannot be sidestepped by sending the
// agent's token to /api/v1.
func TestTokenKindsAreNotInterchangeable(t *testing.T) {
	h := newHarness(t, true)
	mcpToken := h.token([]string{"admin"}, nil)
	r := h.rest("POST", "/tokens", map[string]any{"name": "ci", "scopes": []string{"admin"}})
	require.Equal(t, http.StatusCreated, r.status, string(r.raw))
	require.Equal(t, "rest", r.json()["kind"])
	restToken := r.json()["token"].(string)

	do := func(method, path, token, body string) (int, string) {
		req, err := http.NewRequest(method, h.srv.URL+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := res.Body.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return res.StatusCode, sb.String()
	}

	// An MCP token on REST: refused, even for the operations MCP denies and the
	// ones it guards.
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/projects", ""},
		{"POST", "/api/v1/tokens", `{"name":"x","kind":"rest","scopes":["admin"],"allowProductionWithoutApproval":true}`},
	} {
		st, body := do(c.method, c.path, mcpToken, c.body)
		require.Equal(t, http.StatusForbidden, st, c.path+" "+body)
		require.Contains(t, body, "token_kind_mismatch")
		require.Contains(t, body, "MCP token")
	}

	// A REST token on /mcp: refused.
	st, body := do("POST", "/mcp", restToken, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`)
	require.Equal(t, http.StatusForbidden, st, body)
	require.Contains(t, body, "token_kind_mismatch")

	// Each works where it belongs.
	st, _ = do("GET", "/api/v1/projects", restToken, "")
	require.Equal(t, http.StatusOK, st)
	require.NotNil(t, session(t, h.srv.URL, mcpToken))
}

// A token never creates a token that skips approvals, and only an instance
// admin or team admin signed in on the dashboard can switch the guard off.
func TestTokensCannotMintUnguardedTokens(t *testing.T) {
	h := newHarness(t, true)
	r := h.rest("POST", "/tokens", map[string]any{"name": "ci", "scopes": []string{"admin"}})
	require.Equal(t, http.StatusCreated, r.status, string(r.raw))
	require.False(t, r.json()["allowProductionWithoutApproval"].(bool), "guarded by default")
	restToken := r.json()["token"].(string)

	req, err := http.NewRequest("POST", h.srv.URL+"/api/v1/tokens", strings.NewReader(`{"name":"free","scopes":["projects:read"],"allowProductionWithoutApproval":true}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+restToken)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusForbidden, res.StatusCode)

	// The signed-in instance admin can.
	r = h.rest("POST", "/tokens", map[string]any{"name": "free", "scopes": []string{"projects:read"}, "allowProductionWithoutApproval": true})
	require.Equal(t, http.StatusCreated, r.status, string(r.raw))
	require.True(t, r.json()["allowProductionWithoutApproval"].(bool))
}

// Every composite and native tool that changes state is named here with the
// reason it cannot reach production without approval; a new one fails the test
// until someone decides.
var composedMutations = map[string]string{
	"sync_config":      "configsync.ApplyGuarded: immediate kinds in production need approval; deploy and prune are refused",
	"stage_changes":    "staged only; nothing runs until a guarded deploy",
	"apply_changes":    "deployments.DeployStaged is guarded",
	"rollback_service": "deployments.Rollback is guarded",
	"add_database":     "creates a staged database; its deploy is guarded",
	"setup_domain":     "stages the domain; DNS replacement is the guarded fixDomainDns",
	"create_backup":    "creates a new backup; nothing existing changes",
	"restore_backup":   "backups.Restore is guarded",
	"install_template": "creates staged services; the deploy is guarded",
	"run_command":      "tasks guardRun is guarded",
	"call_operation":   "the central gate and the domain services guard what it reaches",
}

func TestComposedMutationsAreAccountedFor(t *testing.T) {
	h := newHarness(t, true)
	seen := map[string]bool{}
	for _, ti := range h.app.MCP.Tools() {
		if ti.Kind == "curated" || ti.Hints.ReadOnly {
			continue
		}
		seen[ti.Name] = true
		reason, ok := composedMutations[ti.Name]
		require.True(t, ok, "tool %s (%s) changes state but is not in composedMutations: say how production is guarded", ti.Name, ti.Kind)
		require.NotEmpty(t, reason)
	}
	for name := range composedMutations {
		require.True(t, seen[name], "composedMutations names %s, which is not a registered mutating tool", name)
	}
}
