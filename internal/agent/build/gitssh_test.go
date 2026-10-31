package build

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/nebulactrl/nebulactrl/internal/gitssh"
	"github.com/nebulactrl/nebulactrl/internal/gitssh/gitsshtest"
)

func sshRepo(t *testing.T) (root, sha string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	root, work := t.TempDir(), t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return string(out)
	}
	git(work, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(work, "Dockerfile"), []byte("FROM scratch\n"), 0o644))
	git(work, "add", "-A")
	git(work, "commit", "-q", "-m", "init")
	git(root, "clone", "-q", "--bare", work, "app.git")
	return root, git(work, "rev-parse", "HEAD")[:40]
}

func TestGitCloneOverSSHUsesTheDeployKeyAndThePinnedHostKey(t *testing.T) {
	root, sha := sshRepo(t)
	key, err := gitssh.Generate("t")
	require.NoError(t, err)
	authorized, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key.PublicKey))
	require.NoError(t, err)
	host := gitsshtest.NewHostSigner(t)
	srv := gitsshtest.Start(t, root, host, authorized)
	url := "ssh://git@" + srv.Addr + "/app.git"
	ep, err := gitssh.ParseURL(url)
	require.NoError(t, err)
	pin := gitssh.NewHostKey(ep.Addr(), host.PublicKey()).Line()

	req := Request{
		DeploymentID: "d", ServiceSlug: "web", GitURL: url, CommitSHA: sha,
		SSHKey: string(key.PrivatePEM), KnownHosts: []string{pin},
	}
	require.NoError(t, req.Validate())

	// Pinned host + authorized key: the clone checks out the commit.
	dir := filepath.Join(t.TempDir(), "src")
	require.NoError(t, gitClone(context.Background(), req, dir))
	_, err = os.Stat(filepath.Join(dir, "Dockerfile"))
	require.NoError(t, err)

	// The host presents another key: the clone fails with the mismatch, before
	// the deploy key is offered.
	srv.SetHostKey(gitsshtest.NewHostSigner(t))
	before := srv.AuthAttempts()
	err = gitClone(context.Background(), req, filepath.Join(t.TempDir(), "src"))
	var mm *gitssh.MismatchError
	require.ErrorAs(t, err, &mm)
	require.Contains(t, err.Error(), "changed")
	require.Equal(t, before, srv.AuthAttempts())

	// No pin at all: the agent never trusts a key on first use.
	srv.SetHostKey(host)
	req.KnownHosts = nil
	err = gitClone(context.Background(), req, filepath.Join(t.TempDir(), "src"))
	var un *gitssh.UnpinnedError
	require.ErrorAs(t, err, &un)
}

func TestSSHURLNeedsADeployKey(t *testing.T) {
	req := Request{DeploymentID: "d", ServiceSlug: "web", GitURL: "git@github.com:acme/shop.git", CommitSHA: "0123456789abcdef"}
	require.ErrorContains(t, req.Validate(), "deploy key")

	// The HTTPS token is never used for an SSH URL, and without a key go-git
	// must not be allowed to fall back to the host's ssh-agent.
	req.Token = "tok-123456"
	require.ErrorContains(t, req.Validate(), "deploy key")

	req.SSHKey = "key"
	require.NoError(t, req.Validate())
	req.GitURL = "ssh://git@host/"
	require.ErrorContains(t, req.Validate(), "invalid ssh git url")
}
