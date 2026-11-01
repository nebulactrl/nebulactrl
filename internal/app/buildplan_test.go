package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/netguard"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// deps returns a deployments.Service over the harness database; it computes
// the send-time secrets exactly as the agent hub does.
func (h *harness) deps() *deployments.Service { return h.fleetRig().deps }

// gitDaemon serves a one-commit repository over git:// from a local `git
// daemon` and returns its URL and commit SHA.
func gitDaemon(t *testing.T) (url, sha string) {
	t.Helper()
	t.Setenv(netguard.AllowEnv, "127.0.0.1") // the control plane refuses loopback git remotes otherwise
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	require.NoError(t, os.MkdirAll(work, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(work, "package.json"), []byte(`{"scripts":{"start":"node s.js"}}`), 0o644))
	run := func(dir string, args ...string) string {
		cmd := exec.Command(gitBin, append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	run(work, "init", "-q")
	run(work, "add", "-A")
	run(work, "commit", "-q", "-m", "x")
	sha = run(work, "rev-parse", "HEAD")
	base := filepath.Join(root, "srv")
	require.NoError(t, os.MkdirAll(base, 0o755))
	run(root, "clone", "-q", "--bare", work, filepath.Join(base, "app.git"))

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	d := exec.Command(gitBin, "daemon", "--reuseaddr", "--export-all", "--base-path="+base, "--listen=127.0.0.1", fmt.Sprintf("--port=%d", port), base)
	require.NoError(t, d.Start())
	t.Cleanup(func() { _ = d.Process.Kill(); _, _ = d.Process.Wait() })
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	}, 10*time.Second, 50*time.Millisecond)
	return "git://" + addr + "/app.git", sha
}

// agentAnswers plays the agent: it waits for the build_plan command of the
// server and finishes it with result (or error), returning the command row's
// persisted payload and the secrets the control plane would attach at send time.
func (h *harness) agentAnswers(serverID string, result *agentprotocol.BuildPlanResult, cmdErr string) <-chan [2]string {
	h.t.Helper()
	out := make(chan [2]string, 1)
	go func() {
		ctx := context.Background()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			var id uuid.UUID
			var payload []byte
			err := h.app.Pool.QueryRow(ctx, `SELECT id, payload FROM agent_commands WHERE server_id = $1 AND kind = 'build_plan' AND status IN ('pending','sent') ORDER BY created_at LIMIT 1`,
				uuid.MustParse(serverID)).Scan(&id, &payload)
			if err != nil {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			secrets, _ := h.deps().SecretsForCommand(ctx, id, agentprotocol.KindBuildPlan, payload)
			sj, _ := json.Marshal(secrets)
			out <- [2]string{string(payload), string(sj)}
			if cmdErr != "" {
				frame, _ := json.Marshal(agentprotocol.ResultFrame{CommandID: id.String(), Outcome: agentprotocol.OutcomeFailed, Error: cmdErr})
				_, _ = h.app.Pool.Exec(ctx, `UPDATE agent_commands SET status='failed', error=$2, result=$3, finished_at=now() WHERE id=$1`, id, cmdErr, frame)
				return
			}
			data, _ := json.Marshal(result)
			frame, _ := json.Marshal(agentprotocol.ResultFrame{CommandID: id.String(), Outcome: agentprotocol.OutcomeSucceeded, Data: data})
			_, _ = h.app.Pool.Exec(ctx, `UPDATE agent_commands SET status='succeeded', result=$2, finished_at=now() WHERE id=$1`, id, frame)
			return
		}
		close(out)
	}()
	return out
}

func TestBuildPlanPreview(t *testing.T) {
	h := newHarness(t)
	h.setup()
	url, sha := gitDaemon(t)
	envID := h.newEnv("Plan")
	online := h.newOnlineServer("builder", 8*gib, gib)

	create := func(name, serverID string, cfg map[string]any) string {
		body := map[string]any{"name": name, "sourceType": "git", "config": cfg}
		if serverID != "" {
			body["serverId"] = serverID
		}
		r := h.do("POST", "/environments/"+envID+"/services", "", body)
		require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
		return r.JSON()["id"].(string)
	}
	cfg := func() map[string]any {
		return map[string]any{"gitUrl": url, "gitBranch": "main", "port": 3000}
	}

	svc := create("web", online, cfg())
	r := h.do("GET", "/services/"+svc, "", nil)
	require.Equal(t, "auto", r.JSON()["config"].(map[string]any)["buildType"], "git services default to the automatic build method")

	// Validation of the config accepts the build types and rejects others.
	for _, bad := range []string{"nixpacks", "starter:cobol"} {
		c := cfg()
		c["buildType"] = bad
		r := h.do("POST", "/environments/"+envID+"/services", "", map[string]any{"name": "bad", "sourceType": "git", "serverId": online, "config": c})
		require.Equal(t, http.StatusUnprocessableEntity, r.Status, "%s: %s", bad, r.Raw)
	}
	c := cfg()
	c["buildType"] = "starter:node"
	r = h.do("POST", "/environments/"+envID+"/services", "", map[string]any{"name": "starter", "sourceType": "git", "serverId": online, "config": c})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))

	// Build variables: the payload names them, the secrets frame carries the values.
	h.putVars(svc,
		map[string]any{"key": "RAILPACK_START_CMD", "value": "node s.js", "scope": "build"},
		map[string]any{"key": "API_KEY", "value": canary, "isSecret": true, "scope": "build"},
		map[string]any{"key": "RUNTIME_ONLY", "value": "x"},
	)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	gitTok, err := box.SealString(secretbox.ServicesGitToken, svc, canary)
	require.NoError(t, err)
	_, err = h.app.Pool.Exec(context.Background(), `UPDATE services SET git_token_enc = $2 WHERE id = $1`, uuid.MustParse(svc), gitTok)
	require.NoError(t, err)

	want := &agentprotocol.BuildPlanResult{
		Outcome: agentprotocol.OutcomeSucceeded, CommitSHA: sha, Requested: "auto", DockerfilePath: "Dockerfile",
		Info: &agentprotocol.BuildInfo{
			Method: "railpack", Reason: "No Dockerfile in the repository, so Railpack detects how to build it.",
			RailpackVersion: "0.43.0", Providers: []string{"node"}, StartCommand: "npm run start",
		},
		SuggestedStarter: "node",
	}
	seen := h.agentAnswers(online, want, "")
	r = h.do("POST", "/services/"+svc+"/build-plan", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	got := r.JSON()
	require.Equal(t, "succeeded", got["outcome"])
	info := got["info"].(map[string]any)
	require.Equal(t, "railpack", info["method"])
	require.Equal(t, "npm run start", info["startCommand"])
	require.Equal(t, "node", got["suggestedStarter"])

	frames := <-seen
	var p agentprotocol.BuildPlanPayload
	require.NoError(t, json.Unmarshal([]byte(frames[0]), &p))
	require.Equal(t, sha, p.CommitSHA, "the branch head is resolved on the control plane")
	require.Equal(t, url, p.GitURL)
	require.Equal(t, "auto", p.BuildType)
	require.Equal(t, "Dockerfile", p.DockerfilePath)
	require.True(t, p.HasToken)
	require.Equal(t, []string{"RAILPACK_START_CMD"}, p.BuildArgKeys)
	require.Equal(t, []string{"API_KEY"}, p.BuildSecretKeys)
	require.NotContains(t, frames[0], canary, "the persisted payload carries keys, never values")
	require.NotContains(t, frames[0], "RUNTIME_ONLY")
	var secrets map[string]string
	require.NoError(t, json.Unmarshal([]byte(frames[1]), &secrets))
	require.Equal(t, canary, secrets[agentprotocol.SecretBuildPrefix+"API_KEY"])
	require.Equal(t, "node s.js", secrets[agentprotocol.SecretBuildPrefix+"RAILPACK_START_CMD"])
	require.Equal(t, canary, secrets[agentprotocol.SecretGitToken], "the stored git token is attached for the clone")
	require.NotContains(t, secrets, agentprotocol.SecretBuildPrefix+"RUNTIME_ONLY")
	var stored string
	require.NoError(t, h.app.Pool.QueryRow(context.Background(), `SELECT string_agg(payload::text || coalesce(result::text,''), ' ') FROM agent_commands WHERE kind = 'build_plan'`).Scan(&stored))
	require.NotContains(t, stored, canary)

	// A failing plan is a 422 with the agent's reason.
	seen = h.agentAnswers(online, nil, "git clone: repository not found")
	r = h.do("POST", "/services/"+svc+"/build-plan", "", nil)
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "build_plan_failed", r.JSON()["code"])
	require.Contains(t, string(r.Raw), "repository not found")
	<-seen

	// A source problem is part of a successful result.
	seen = h.agentAnswers(online, &agentprotocol.BuildPlanResult{
		Outcome: agentprotocol.OutcomeFailed, Requested: "dockerfile", DockerfilePath: "Dockerfile",
		Message: `dockerfilePath: "Dockerfile" not found in repository`,
	}, "")
	r = h.do("POST", "/services/"+svc+"/build-plan", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "failed", r.JSON()["outcome"])
	<-seen

	// Not plannable: an offline server, no server, an image service.
	offline := h.newOnlineServer("offline", 8*gib, gib)
	_, err = h.app.Pool.Exec(context.Background(), `UPDATE servers SET status='offline' WHERE id = $1`, uuid.MustParse(offline))
	require.NoError(t, err)
	r = h.do("POST", "/services/"+create("on-offline", offline, cfg())+"/build-plan", "", nil)
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "server_offline", r.JSON()["code"])

	r = h.do("POST", "/services/"+create("no-server", "", cfg())+"/build-plan", "", nil)
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "no_server", r.JSON()["code"])

	img := h.newServiceOn(envID, "img", online, nil)
	r = h.do("POST", "/services/"+img+"/build-plan", "", nil)
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "not_git_source", r.JSON()["code"])

	// It makes a server clone a repository: deployments:write is required.
	ro := h.createToken([]string{"projects:read"}, nil)
	require.Equal(t, http.StatusForbidden, h.do("POST", "/services/"+svc+"/build-plan", ro, nil).Status)
}

func TestDeploymentCarriesBuildInfo(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.startJobs()
	envID := h.newEnv("Info")
	rig := h.fleetRig()
	srv := h.newOnlineServer("builder", 8*gib, gib)
	svc := h.newServiceOn(envID, "app", srv, nil)
	dep := h.deploy(svc, nil)["id"].(string)
	h.awaitPrepared(dep)

	info := &agentprotocol.BuildInfo{Method: "railpack", RailpackVersion: "0.43.0", Providers: []string{"node"}, StartCommand: "npm run start", Builder: "docker"}
	data, _ := json.Marshal(agentprotocol.DeployResult{Outcome: agentprotocol.OutcomeSucceeded, Build: info})
	d := uuid.MustParse(dep)
	require.NoError(t, rig.deps.HandleResult(context.Background(), uuid.MustParse(srv), uuid.New(), &d,
		agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: data}))

	r := h.do("GET", "/deployments/"+dep, "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	build := r.JSON()["build"].(map[string]any)
	require.Equal(t, "railpack", build["method"])
	require.Equal(t, "0.43.0", build["railpackVersion"])
	require.Equal(t, "npm run start", build["startCommand"])

	// An image deployment has no build info.
	svc2 := h.newServiceOn(envID, "plain", h.newOnlineServer("b2", 8*gib, gib), nil)
	dep2 := h.deploy(svc2, nil)["id"].(string)
	r = h.do("GET", "/deployments/"+dep2, "", nil)
	require.Nil(t, r.JSON()["build"])
}
