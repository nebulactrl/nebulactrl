package app_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func bitbucketPush(t *testing.T, h *harness, connID, secret, delivery string, body []byte) (int, map[string]any) {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/hooks/git/bitbucket/"+connID, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Event-Key", "repo:push")
	req.Header.Set("X-Request-UUID", delivery)
	req.Header.Set("X-Hub-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// TestGitPushCreatesCommitDeploymentThroughTheRealStack covers the whole
// path with the real domain services: connect a provider, link a service, a
// signed push creates a git_push deployment with the commit, a push during a
// deployment is queued, and the clone credentials of the connection reach the
// agent command as send-time secrets (never in the persisted payload).
func TestGitPushCreatesCommitDeploymentThroughTheRealStack(t *testing.T) {
	h := newHarness(t)
	h.setup()
	repoURL, _ := gitDaemon(t)
	envID := h.newEnv("Git")
	online := h.newOnlineServer("builder", 8*gib, gib)

	r := h.do("POST", "/environments/"+envID+"/services", "", map[string]any{
		"name": "web", "sourceType": "git", "serverId": online, "config": map[string]any{"gitUrl": repoURL, "gitBranch": "main", "port": 3000},
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	svc := r.JSON()["id"].(string)

	const token = "bb_clone_token_canary_123456"
	r = h.do("POST", "/git/connections", "", map[string]any{"provider": "bitbucket", "name": "Bitbucket", "token": token})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	conn := r.JSON()["connection"].(map[string]any)
	connID := conn["id"].(string)
	secret := r.JSON()["webhookSecret"].(string)
	require.NotEmpty(t, secret)
	require.NotContains(t, string(r.Raw), token, "the token is write-only")

	r = h.do("PUT", "/services/"+svc+"/git", "", map[string]any{
		"connectionId": connID, "repo": "team/webapp", "branch": "main", "watchPaths": []string{"apps/never/**"},
	})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "manual", r.JSON()["webhook"].(map[string]any)["mode"])
	// The link pointed the clone URL at Bitbucket; build from the local repo instead.
	_, err := h.app.Pool.Exec(context.Background(), `UPDATE services SET config = jsonb_set(config, '{gitUrl}', to_jsonb($2::text)) WHERE id = $1`, uuid.MustParse(svc), repoURL)
	require.NoError(t, err)

	r = h.do("GET", "/services/"+svc, "", nil)
	git := r.JSON()["git"].(map[string]any)
	require.Equal(t, "team/webapp", git["repo"])
	require.Equal(t, "bitbucket", git["provider"])
	require.Equal(t, true, git["autoDeploy"])
	require.Equal(t, []any{"apps/never/**"}, git["watchPaths"])

	// A signed push deploys (Bitbucket lists no files, so watch paths cannot filter it).
	body, err := os.ReadFile("../gitproviders/testdata/bitbucket_push.json")
	require.NoError(t, err)
	code, reply := bitbucketPush(t, h, connID, secret, "e2e-1", body)
	require.Equal(t, http.StatusOK, code, reply)
	require.Contains(t, reply["summary"], "deploying web")

	r = h.do("GET", "/services/"+svc+"/deployments", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	items := r.JSON()["items"].([]any)
	require.Len(t, items, 1)
	dep := items[0].(map[string]any)
	require.Equal(t, "git_push", dep["trigger"])
	require.Equal(t, "system", dep["actorType"])
	commit := dep["commit"].(map[string]any)
	require.Equal(t, "709d658dc5b6d6afcd46049c2f332ee3f515a67d", commit["sha"])
	require.Equal(t, "New commit 1", commit["message"])
	require.Equal(t, "Emma Tester", commit["author"])
	require.Equal(t, "main", commit["branch"])

	// A second push while that deployment runs is queued, not lost or duplicated.
	body2 := bytes.ReplaceAll(body, []byte("709d658dc5b6d6afcd46049c2f332ee3f515a67d"), []byte("1111111111111111111111111111111111111111"))
	code, reply = bitbucketPush(t, h, connID, secret, "e2e-2", body2)
	require.Equal(t, http.StatusOK, code, reply)
	require.Contains(t, reply["summary"], "queued to deploy next")
	r = h.do("GET", "/services/"+svc+"/git", "", nil)
	require.Equal(t, "1111111111111111111111111111111111111111", r.JSON()["queuedCommit"])

	// The delivery log shows both, with the signature redacted.
	r = h.do("GET", "/git/connections/"+connID+"/deliveries", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	log := r.JSON()["items"].([]any)
	require.Len(t, log, 2)
	require.Equal(t, "processed", log[0].(map[string]any)["outcome"])
	require.Equal(t, "[redacted]", log[0].(map[string]any)["headers"].(map[string]any)["X-Hub-Signature"])
	require.NotContains(t, string(r.Raw), secret)

	// The connection's token is attached to an agent command at send time only,
	// and only when the service's URL is the repository the link points at.
	// (The harness builds its deployments service apart from the app's wiring,
	// so give it the git credentials source the wiring gives the real one.)
	deps := h.deps()
	deps.SetGitAuth(gitproviders.NewService(gitproviders.Deps{Pool: h.app.Pool, Box: h.app.Box}))

	// The service's URL points away from the linked repository (a developer can
	// do that without the connection): nothing resolves with its credentials.
	const awayURL = "https://attacker.example/team/webapp.git"
	_, err = h.app.Pool.Exec(context.Background(), `UPDATE services SET config = jsonb_set(config, '{gitUrl}', to_jsonb($2::text)) WHERE id = $1`, uuid.MustParse(svc), awayURL)
	require.NoError(t, err)
	r = h.do("POST", "/services/"+svc+"/build-plan", "", nil)
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Contains(t, string(r.Raw), "git connection")
	other, err := json.Marshal(agentprotocol.BuildPlanPayload{ServiceID: svc, HasToken: true, GitURL: awayURL})
	require.NoError(t, err)
	_, err = deps.SecretsForCommand(context.Background(), uuid.New(), agentprotocol.KindBuildPlan, other)
	require.ErrorContains(t, err, "git connection")

	// The git protocol carries no credentials: that clone is anonymous, never refused.
	anon, err := json.Marshal(agentprotocol.BuildPlanPayload{ServiceID: svc, HasToken: true, GitURL: repoURL})
	require.NoError(t, err)
	secrets, err := deps.SecretsForCommand(context.Background(), uuid.New(), agentprotocol.KindBuildPlan, anon)
	require.NoError(t, err)
	require.Empty(t, secrets[agentprotocol.SecretGitToken])

	linked, err := json.Marshal(agentprotocol.BuildPlanPayload{ServiceID: svc, HasToken: true, GitURL: "https://bitbucket.org/team/webapp.git"})
	require.NoError(t, err)
	secrets, err = deps.SecretsForCommand(context.Background(), uuid.New(), agentprotocol.KindBuildPlan, linked)
	require.NoError(t, err)
	require.Equal(t, token, secrets[agentprotocol.SecretGitToken])
	require.Equal(t, "x-token-auth", secrets[agentprotocol.SecretGitUsername])

	// A read-only token sees connections but cannot change links or read deliveries.
	ro := h.createToken([]string{"projects:read"}, nil)
	r = h.do("GET", "/git/connections", ro, nil)
	require.Equal(t, http.StatusOK, r.Status)
	require.NotContains(t, string(r.Raw), token)
	require.Equal(t, http.StatusForbidden, h.do("PUT", "/services/"+svc+"/git", ro, map[string]any{"connectionId": connID, "repo": "team/webapp"}).Status)
	require.Equal(t, http.StatusForbidden, h.do("GET", "/git/connections/"+connID+"/deliveries", ro, nil).Status)
	require.Equal(t, http.StatusForbidden, h.do("POST", "/git/connections", ro, map[string]any{"provider": "bitbucket"}).Status)

	// Unlinking stops auto-deploy.
	require.Equal(t, http.StatusNoContent, h.do("DELETE", "/services/"+svc+"/git", "", nil).Status)
	r = h.do("GET", "/services/"+svc, "", nil)
	require.Nil(t, r.JSON()["git"])
}
