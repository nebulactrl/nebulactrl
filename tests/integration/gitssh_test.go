//go:build integration

package integration

import (
	"context"
	"encoding/hex"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/nebulactrl/nebulactrl/internal/gitssh"
	"github.com/nebulactrl/nebulactrl/internal/gitssh/gitsshtest"
)

// TestGitDeployOverSSH: a git service with an ssh:// URL deploys with a deploy
// key (generated, then replaced by an uploaded one), pins the host key on the
// first fetch, never leaks the private key, and refuses a changed host key
// (the old release keeps serving) until "Accept new host key".
//
// The SSH git server is a few lines of Go (internal/gitssh/gitsshtest: x/crypto
// ssh serving `git upload-pack` from a bare repo), not an sshd container: no
// extra image to build or pull, the host key can be swapped in-process to
// simulate a changed host, and the repository is cloned from the existing git://
// fixture, so the build input is identical to TestGitDeploy.
func TestGitDeployOverSSH(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	g := gitServer(t)
	s := SharedStack(t)

	root := t.TempDir()
	out, err := exec.Command("git", "clone", "-q", "--bare", g.URL, filepath.Join(root, "app.git")).CombinedOutput()
	require.NoError(t, err, string(out))
	host := gitsshtest.NewHostSigner(t)
	srv := gitsshtest.Start(t, root, host)
	url := "ssh://git@" + srv.Addr + "/app.git"

	svc := s.NewService(SvcSpec{
		Name: "sshapp", GitURL: url, Domain: s.Host(),
		Vars: map[string]string{"VERSION": "from-ssh"},
	})

	// 1. An SSH URL without a deploy key cannot be deployed, and says why.
	d := s.WaitDeployment(s.Deploy(svc.ID), time.Minute)
	require.Equal(t, "failed", d.Str("status"), "%v", d)
	require.Contains(t, d.Str("statusMessage"), "deploy key", "%v", d)

	// 2. "Use SSH": a generated key (public half only), authorized on the git server.
	r := s.Do("POST", "/services/"+svc.ID+"/ssh/key", map[string]any{})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.NotContains(t, string(r.Raw), "PRIVATE KEY")
	generated := r.JSON().Obj("key")
	require.Equal(t, "generated", generated.Str("source"))
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(generated.Str("publicKey")))
	require.NoError(t, err)
	srv.Authorize(pub)
	r = s.Do("POST", "/services/"+svc.ID+"/ssh/test", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.True(t, r.JSON().Bool("ok"), string(r.Raw))
	require.Equal(t, g.CommitSHA, r.JSON().Str("commitSha"))
	hostKeys := func(j J) []J {
		arr, _ := j["hostKeys"].([]any)
		var out []J
		for _, v := range arr {
			m, _ := v.(map[string]any)
			out = append(out, J(m))
		}
		return out
	}
	// The test connection was the first fetch: it pinned the host key.
	pins := hostKeys(r.JSON().Obj("status"))
	require.Len(t, pins, 1)
	require.Equal(t, ssh.FingerprintSHA256(host.PublicKey()), pins[0].Str("fingerprint"))

	// 3. Replace it with an uploaded key (rotation), then deploy for real.
	own, err := gitssh.Generate("itest")
	require.NoError(t, err)
	ownPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(own.PublicKey))
	require.NoError(t, err)
	srv.Authorize(ownPub)
	r = s.Do("POST", "/services/"+svc.ID+"/ssh/key", map[string]any{"privateKey": string(own.PrivatePEM), "rotate": true})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "uploaded", r.JSON().Obj("key").Str("source"))
	require.Equal(t, own.Fingerprint, r.JSON().Obj("key").Str("fingerprint"))

	d = s.DeployOK(svc.ID)
	src := d.Obj("release").Obj("source")
	require.Equal(t, g.CommitSHA, src.Str("commitSha"), "release source: %v", src)
	require.Equal(t, url, src.Str("gitUrl"))
	require.True(t, strings.HasPrefix(src.Str("imageId"), "sha256:"), "%v", src)
	logs := s.DeploymentLogs(d.Str("id"))
	require.Contains(t, logs, "Cloning "+url)
	NewProxyClient(s.Host()).WaitBody(t, "/", "from-ssh", 30*time.Second)

	// 4. The private key never reaches logs, persisted commands or any table.
	var needles []string
	for _, line := range strings.Split(string(own.PrivatePEM), "\n") {
		if line = strings.TrimSpace(line); len(line) >= 20 && !strings.HasPrefix(line, "-----") {
			needles = append(needles, line, hex.EncodeToString([]byte(line)))
		}
	}
	require.NotEmpty(t, needles)
	var payload string
	require.NoError(t, s.Pool.QueryRow(context.Background(),
		`SELECT string_agg(payload::text || coalesce(result::text,''), E'\n') FROM agent_commands`).Scan(&payload))
	require.Contains(t, payload, "knownHosts", "the command carries the pinned host key")
	require.Contains(t, payload, `"hasSshKey": true`)
	dump := s.dumpAllTables()
	for _, n := range needles {
		require.NotContains(t, logs, n, "deployment logs leak the deploy key")
		require.NotContains(t, payload, n, "agent_commands leak the deploy key")
		require.NotContains(t, dump, n, "the database holds the deploy key unencrypted")
	}
	for _, path := range []string{"/services/" + svc.ID, "/services/" + svc.ID + "/ssh", "/audit-events", "/deployments/" + d.Str("id")} {
		r := s.Do("GET", path, nil)
		require.Equal(t, http.StatusOK, r.Status, "%s: %s", path, r.Raw)
		require.NotContains(t, string(r.Raw), "PRIVATE KEY", path)
	}

	// 5. The git host presents a different key: the deploy fails loudly, the
	// refused fingerprint is shown, and the running release keeps serving.
	changed := gitsshtest.NewHostSigner(t)
	srv.SetHostKey(changed)
	d = s.WaitDeployment(s.Deploy(svc.ID), time.Minute)
	require.Equal(t, "failed", d.Str("status"), "%v", d)
	require.Contains(t, d.Str("statusMessage"), "host key", "%v", d)
	require.Contains(t, d.Str("statusMessage"), ssh.FingerprintSHA256(changed.PublicKey()), "%v", d)
	NewProxyClient(s.Host()).WaitBody(t, "/", "from-ssh", 10*time.Second)
	r = s.Do("GET", "/services/"+svc.ID+"/ssh", nil)
	pins = hostKeys(r.JSON())
	require.Len(t, pins, 1)
	require.Equal(t, ssh.FingerprintSHA256(changed.PublicKey()), pins[0].Obj("mismatch").Str("fingerprint"))
	require.Equal(t, ssh.FingerprintSHA256(host.PublicKey()), pins[0].Str("fingerprint"), "the pin is untouched")

	// 6. "Accept new host key" with a stale fingerprint is refused; the reviewed one works.
	r = s.Do("POST", "/services/"+svc.ID+"/ssh/host-keys/accept", map[string]any{"fingerprint": "SHA256:stale"})
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	r = s.Do("POST", "/services/"+svc.ID+"/ssh/host-keys/accept", map[string]any{"fingerprint": ssh.FingerprintSHA256(changed.PublicKey())})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	d = s.DeployOK(svc.ID)
	require.Equal(t, g.CommitSHA, d.Obj("release").Obj("source").Str("commitSha"))

	// The deploy key lifecycle is in the audit log (key material is not).
	rows, err := s.Pool.Query(context.Background(), `SELECT action FROM audit_events WHERE resource_id = $1 AND action LIKE 'service.ssh_%' ORDER BY id`, svc.ID)
	require.NoError(t, err)
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var a string
		require.NoError(t, rows.Scan(&a))
		actions = append(actions, a)
	}
	require.Equal(t, []string{
		"service.ssh_key.create", "service.ssh_host.pin", "service.ssh_key.rotate", "service.ssh_host.mismatch", "service.ssh_host.accept",
	}, actions)
}
