package app_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const canary = "sekret-canary-12345"

// newService creates a project, an enrolled-pending server and an image
// service on it; it returns (serviceID, serverID).
func (h *harness) newService(name string) (string, string) {
	h.t.Helper()
	pid := h.createProject("P " + name)
	r := h.do("GET", "/projects/"+pid+"/environments", "", nil)
	require.Equal(h.t, http.StatusOK, r.Status, string(r.Raw))
	envID := r.JSON()["items"].([]any)[0].(map[string]any)["id"].(string)

	r = h.do("POST", "/servers/enrollment-tokens", "", map[string]any{"name": "srv-" + name})
	require.Equal(h.t, http.StatusCreated, r.Status, string(r.Raw))
	serverID := r.JSON()["server"].(map[string]any)["id"].(string)

	r = h.do("POST", "/environments/"+envID+"/services", "", map[string]any{
		"name": name, "sourceType": "image", "serverId": serverID,
		"config": map[string]any{"image": "traefik/whoami:latest", "port": 80},
	})
	require.Equal(h.t, http.StatusCreated, r.Status, string(r.Raw))
	return r.JSON()["id"].(string), serverID
}

func TestSecondDeployWhileActiveGets409(t *testing.T) {
	h := newHarness(t)
	h.setup()
	svc, _ := h.newService("web")

	r := h.do("POST", "/services/"+svc+"/deployments", "", map[string]any{})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))

	r = h.do("POST", "/services/"+svc+"/deployments", "", map[string]any{})
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "deployment_in_progress", r.JSON()["code"])
}

func TestPatchWithStaleIfMatchGets412(t *testing.T) {
	h := newHarness(t)
	h.setup()
	svc, _ := h.newService("web")

	r := h.do("GET", "/services/"+svc, "", nil)
	require.Equal(t, http.StatusOK, r.Status)
	etag := r.Header.Get("ETag")
	require.NotEmpty(t, etag)

	body := map[string]any{"name": "web-renamed"}
	r = h.do("PATCH", "/services/"+svc, "", body, "If-Match", etag)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.NotEqual(t, etag, r.Header.Get("ETag"))

	// The old generation is stale now.
	r = h.do("PATCH", "/services/"+svc, "", map[string]any{"name": "again"}, "If-Match", etag)
	require.Equal(t, http.StatusPreconditionFailed, r.Status, string(r.Raw))
	require.Equal(t, "generation_conflict", r.JSON()["code"])
}

func TestSecretNeverInVariablesReadOrPersistedCommandPayload(t *testing.T) {
	h := newHarness(t)
	h.setup()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, h.app.Start(ctx))

	svc, serverID := h.newService("api")
	r := h.do("PUT", "/services/"+svc+"/variables", "", map[string]any{"variables": []map[string]any{
		{"key": "API_KEY", "value": canary, "isSecret": true},
		{"key": "MODE", "value": "production", "isSecret": false},
	}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))

	r = h.do("GET", "/services/"+svc+"/variables", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.NotContains(t, string(r.Raw), canary)
	require.Contains(t, string(r.Raw), "production", "non-secret values are readable")

	r = h.do("GET", "/services/"+svc, "", nil)
	require.NotContains(t, string(r.Raw), canary)

	r = h.do("POST", "/services/"+svc+"/deployments", "", map[string]any{})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))

	// DeployJob writes the release and the (undelivered: server offline) command.
	var payload string
	require.Eventually(t, func() bool {
		err := h.app.Pool.QueryRow(ctx,
			"SELECT payload::text FROM agent_commands WHERE server_id = $1 AND kind = 'deploy'", serverID).Scan(&payload)
		return err == nil
	}, 15*time.Second, 100*time.Millisecond)
	require.NotContains(t, payload, canary)
	require.True(t, strings.Contains(payload, `"API_KEY"`), fmt.Sprintf("env key listed: %s", payload))

	var releaseEnv []byte
	require.NoError(t, h.app.Pool.QueryRow(ctx, "SELECT coalesce(env_enc, '') FROM releases ORDER BY created_at DESC LIMIT 1").Scan(&releaseEnv))
	require.NotContains(t, string(releaseEnv), canary, "release env is encrypted at rest")
}
