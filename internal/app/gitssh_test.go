package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/nebulactrl/nebulactrl/internal/gitssh/deploykeys"
	"github.com/nebulactrl/nebulactrl/internal/gitssh/gitsshtest"
	"github.com/nebulactrl/nebulactrl/internal/netguard"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// TestSSHDeployKeyThroughTheRealStack: a git service with an ssh:// URL gets a
// generated deploy key over REST, the plan and the build plan resolve the
// branch over SSH (pinning the host key on first use), the persisted command
// payload carries the pinned host key but never the private key, the key is
// attached as a send-time secret, and a changed host key fails loudly until it
// is accepted.
func TestSSHDeployKeyThroughTheRealStack(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv(netguard.AllowEnv, "127.0.0.1") // the control plane refuses loopback git remotes otherwise
	h := newHarness(t)
	h.setup()
	envID := h.newEnv("SSH")
	online := h.newOnlineServer("builder", 8*gib, gib)

	root, sha := sshBareRepo(t)
	host := gitsshtest.NewHostSigner(t)
	srv := gitsshtest.Start(t, root, host)
	url := "ssh://git@" + srv.Addr + "/app.git"

	r := h.do("POST", "/environments/"+envID+"/services", "", map[string]any{
		"name": "web", "sourceType": "git", "serverId": online, "config": map[string]any{"gitUrl": url, "gitBranch": "main", "port": 3000},
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	svc := r.JSON()["id"].(string)

	// scp-style and ssh:// URLs validate; a malformed one is rejected.
	r = h.do("POST", "/environments/"+envID+"/services", "", map[string]any{
		"name": "scp", "sourceType": "git", "config": map[string]any{"gitUrl": "git@github.com:acme/shop.git", "gitBranch": "main"},
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	r = h.do("POST", "/environments/"+envID+"/services", "", map[string]any{
		"name": "bad", "sourceType": "git", "config": map[string]any{"gitUrl": "ssh://git@github.com", "gitBranch": "main"},
	})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Contains(t, string(r.Raw), "no repository path")

	// No key yet: nothing can be fetched.
	r = h.do("GET", "/services/"+svc+"/ssh", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, true, r.JSON()["usesSshUrl"])
	require.Nil(t, r.JSON()["key"])
	r = h.do("POST", "/services/"+svc+"/build-plan", "", nil)
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Contains(t, string(r.Raw), "no deploy key")

	// "Use SSH": generate a key; only the public half comes back.
	r = h.do("POST", "/services/"+svc+"/ssh/key", "", map[string]any{})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	key := r.JSON()["key"].(map[string]any)
	pubLine := key["publicKey"].(string)
	require.NotContains(t, string(r.Raw), "PRIVATE KEY")
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pubLine))
	require.NoError(t, err)
	srv.Authorize(pub)
	r = h.do("POST", "/services/"+svc+"/ssh/key", "", map[string]any{})
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "ssh_key_exists", r.JSON()["code"])

	// The plan resolves the branch over SSH; the first fetch pinned the host key.
	r = h.do("POST", "/services/"+svc+"/deployments/plan", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, sha, r.JSON()["source"].(map[string]any)["commitSha"], string(r.Raw))
	r = h.do("GET", "/services/"+svc+"/ssh", "", nil)
	hostKeys := r.JSON()["hostKeys"].([]any)
	require.Len(t, hostKeys, 1)
	require.Equal(t, ssh.FingerprintSHA256(host.PublicKey()), hostKeys[0].(map[string]any)["fingerprint"])

	// The build plan command: payload has the pin, never the key; the key is a secret.
	deps := h.deps()
	deps.SetSSHKeys(deploykeys.NewService(h.app.Pool, nil, h.app.Box))
	seen := h.agentAnswers(online, &agentprotocol.BuildPlanResult{Outcome: agentprotocol.OutcomeSucceeded, Requested: "auto"}, "")
	r = h.do("POST", "/services/"+svc+"/build-plan", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	frames := <-seen
	var p agentprotocol.BuildPlanPayload
	require.NoError(t, json.Unmarshal([]byte(frames[0]), &p))
	require.True(t, p.HasSSHKey)
	require.False(t, p.HasToken)
	require.Equal(t, sha, p.CommitSHA)
	require.Len(t, p.KnownHosts, 1)
	require.Contains(t, p.KnownHosts[0], "ssh-ed25519 ")
	require.NotContains(t, frames[0], "PRIVATE KEY", "the persisted payload never carries the key")

	secrets, err := deps.SecretsForCommand(context.Background(), uuid.New(), agentprotocol.KindBuildPlan, []byte(frames[0]))
	require.NoError(t, err)
	signer, err := ssh.ParsePrivateKey([]byte(secrets[agentprotocol.SecretGitSSHKey]))
	require.NoError(t, err)
	require.Equal(t, pub.Marshal(), signer.PublicKey().Marshal(), "the secret is the private half of the shown public key")

	// The host presents another key: the plan fails loudly and the UI state says so.
	changed := gitsshtest.NewHostSigner(t)
	srv.SetHostKey(changed)
	r = h.do("POST", "/services/"+svc+"/build-plan", "", nil)
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Contains(t, string(r.Raw), "host key")
	require.Contains(t, string(r.Raw), ssh.FingerprintSHA256(changed.PublicKey()))
	r = h.do("GET", "/services/"+svc+"/ssh", "", nil)
	mm := r.JSON()["hostKeys"].([]any)[0].(map[string]any)["mismatch"].(map[string]any)
	require.Equal(t, ssh.FingerprintSHA256(changed.PublicKey()), mm["fingerprint"])

	// "Accept new host key" pins what was reviewed, then everything works again.
	r = h.do("POST", "/services/"+svc+"/ssh/host-keys/accept", "", map[string]any{"fingerprint": ssh.FingerprintSHA256(changed.PublicKey())})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	r = h.do("POST", "/services/"+svc+"/ssh/test", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, true, r.JSON()["ok"], string(r.Raw))
	require.Equal(t, sha, r.JSON()["commitSha"])

	// A read-only token can read the state but not change keys.
	ro := h.createToken([]string{"projects:read"}, nil)
	require.Equal(t, http.StatusOK, h.do("GET", "/services/"+svc+"/ssh", ro, nil).Status)
	require.Equal(t, http.StatusForbidden, h.do("POST", "/services/"+svc+"/ssh/key", ro, map[string]any{"rotate": true}).Status)
	require.Equal(t, http.StatusForbidden, h.do("DELETE", "/services/"+svc+"/ssh/key", ro, nil).Status)

	// Removing the key.
	require.Equal(t, http.StatusNoContent, h.do("DELETE", "/services/"+svc+"/ssh/key", "", nil).Status)
}

// sshBareRepo makes <root>/app.git with one commit on main.
func sshBareRepo(t *testing.T) (root, sha string) {
	t.Helper()
	root, work := t.TempDir(), t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return string(out)
	}
	run(work, "init", "-q")
	run(work, "commit", "-q", "--allow-empty", "-m", "init")
	run(root, "clone", "-q", "--bare", work, "app.git")
	return root, run(work, "rev-parse", "HEAD")[:40]
}
