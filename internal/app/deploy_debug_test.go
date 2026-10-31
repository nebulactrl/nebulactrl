package app_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const debugCanary = "sekret-canary-12345"

// logStreams reads a deployment's persisted log lines and returns their
// streams and text.
func (h *harness) logStreams(dep, query string) (streams, lines []string) {
	h.t.Helper()
	r := h.do("GET", "/deployments/"+dep+"/logs"+query, "", nil)
	require.Equal(h.t, http.StatusOK, r.Status, string(r.Raw))
	for _, it := range r.JSON()["items"].([]any) {
		m := it.(map[string]any)
		streams = append(streams, m["stream"].(string))
		lines = append(lines, m["line"].(string))
	}
	return streams, lines
}

// The deployment debug views (commands, release, debug log stream) are readable
// with the same access as the logs, and never carry a secret value: not in the
// command payload or result, not in the release snapshot, not in the debug
// stream. The debug stream is hidden from the default log.
func TestDeploymentDebugViewsNeverLeakSecrets(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.startApp()
	ctx := context.Background()
	e := h.newVarEnv("dbg")
	svc := e.service("api", "app", nil)

	r := h.put(svc,
		v("API_KEY", debugCanary, "isSecret", true),
		v("GENERATED", "${{secret(32)}}"),
		v("GREETING", "hello-visible"),
	)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	generated, _ := h.storedValue(svc, "GENERATED")
	require.Len(t, generated, 32)

	dep := h.deploy(svc, nil)["id"].(string)
	h.awaitPrepared(dep)
	var cmdID uuid.UUID
	require.NoError(t, h.app.Pool.QueryRow(ctx,
		"SELECT id FROM agent_commands WHERE deployment_id = $1 AND kind = 'deploy'", uuid.MustParse(dep)).Scan(&cmdID))

	// A failed result whose message and error echo the secret, as a careless
	// agent or an app's own output could.
	_, err := h.app.Pool.Exec(ctx, `UPDATE agent_commands SET status = 'failed', error = $2,
		result = jsonb_build_object('outcome', 'failed', 'data', jsonb_build_object('message', $3::text, 'env', jsonb_build_object('API_KEY', $4::text))),
		finished_at = now() WHERE id = $1`,
		cmdID, "boom "+debugCanary, "connecting with "+debugCanary+" and "+generated, debugCanary)
	require.NoError(t, err)

	// Commands.
	cr := h.do("GET", "/deployments/"+dep+"/commands", "", nil)
	require.Equal(t, http.StatusOK, cr.Status, string(cr.Raw))
	items := cr.JSON()["items"].([]any)
	require.Len(t, items, 1)
	cmd := items[0].(map[string]any)
	require.Equal(t, "deploy", cmd["kind"])
	require.Equal(t, "failed", cmd["status"])
	require.Equal(t, "failed", cmd["outcome"])
	require.Equal(t, "srv-dbg", cmd["serverName"])
	require.NotEmpty(t, cmd["createdAt"])
	payload := cmd["payload"].(map[string]any)
	require.Contains(t, payload["envKeys"], "API_KEY", "keys are shown")
	require.Contains(t, payload["secretEnvKeys"], "API_KEY")
	for _, secret := range []string{debugCanary, generated} {
		require.NotContains(t, string(cr.Raw), secret)
	}

	// Release.
	rr := h.do("GET", "/deployments/"+dep+"/release", "", nil)
	require.Equal(t, http.StatusOK, rr.Status, string(rr.Raw))
	rel := rr.JSON()
	require.Equal(t, dep, rel["deploymentId"])
	require.NotEmpty(t, rel["id"])
	require.Equal(t, "traefik/whoami:latest", rel["source"].(map[string]any)["image"])
	require.NotNil(t, rel["config"])
	vars := map[string]map[string]any{}
	for _, it := range rel["variables"].([]any) {
		m := it.(map[string]any)
		vars[m["key"].(string)] = m
	}
	require.Equal(t, true, vars["API_KEY"]["secret"])
	require.Equal(t, true, vars["GENERATED"]["secret"], "a generated secret is secret")
	require.Equal(t, false, vars["GREETING"]["secret"])
	require.Equal(t, false, vars["GREETING"]["platform"])
	require.Equal(t, true, vars["PORT"]["platform"])
	for _, secret := range []string{debugCanary, generated, "hello-visible"} {
		require.NotContains(t, string(rr.Raw), secret, "not even a plain value is listed: keys only")
	}

	// The debug stream: hidden by default, opt-in with ?streams=.
	for _, row := range [][2]string{{"deploy", "Pulling traefik/whoami:latest"}, {"debug", "health nc-api-1: probe GET http://172.20.0.5:80/: HTTP 200 in 3ms (pass)"}} {
		_, err := h.app.Pool.Exec(ctx, "INSERT INTO deployment_logs (deployment_id, stream, line) VALUES ($1, $2, $3)", uuid.MustParse(dep), row[0], row[1])
		require.NoError(t, err)
	}
	streams, _ := h.logStreams(dep, "")
	require.Equal(t, []string{"deploy"}, streams)
	streams, _ = h.logStreams(dep, "?streams=all")
	require.Equal(t, []string{"deploy", "debug"}, streams)
	streams, lines := h.logStreams(dep, "?streams=debug")
	require.Equal(t, []string{"debug"}, streams)
	require.Contains(t, lines[0], "probe GET")
	require.Equal(t, http.StatusUnprocessableEntity, h.do("GET", "/deployments/"+dep+"/logs?streams=secrets", "", nil).Status)

	// Access follows the logs: a read-only token (projects:read + logs:read)
	// sees all three, a token without them none, a token for another project 404s.
	reader := h.createToken([]string{"projects:read", "logs:read"}, nil)
	noRead := h.createToken([]string{"servers:read"}, nil)
	other := h.createProject("other")
	elsewhere := h.createToken([]string{"projects:read", "logs:read"}, []string{other})
	for _, path := range []string{"/commands", "/release", "/logs?streams=all"} {
		require.Equal(t, http.StatusOK, h.do("GET", "/deployments/"+dep+path, reader, nil).Status, path)
		require.Equal(t, http.StatusForbidden, h.do("GET", "/deployments/"+dep+path, noRead, nil).Status, path)
		require.Equal(t, http.StatusNotFound, h.do("GET", "/deployments/"+dep+path, elsewhere, nil).Status, path)
	}
	require.Equal(t, http.StatusNotFound, h.do("GET", "/deployments/"+uuid.NewString()+"/commands", "", nil).Status)
}
