package gitssh_test

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/nebulactrl/nebulactrl/internal/gitssh"
	"github.com/nebulactrl/nebulactrl/internal/gitssh/gitsshtest"
	"github.com/nebulactrl/nebulactrl/internal/netguard"
)

func pubOf(t *testing.T, s ssh.Signer) ssh.PublicKey { t.Helper(); return s.PublicKey() }

func TestPinsAcceptOnlyThePinnedKey(t *testing.T) {
	a, b := gitsshtest.NewHostSigner(t), gitsshtest.NewHostSigner(t)
	pin := gitssh.NewHostKey("git.example.com:2222", pubOf(t, a))
	require.Equal(t, "[git.example.com]:2222", pin.Host)
	require.Equal(t, "ssh-ed25519", pin.Type)

	pins, err := gitssh.NewPins([]string{pin.Line()})
	require.NoError(t, err)
	cb := pins.Callback()
	addr := &net.TCPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 2222}

	require.NoError(t, cb("git.example.com:2222", addr, pubOf(t, a)))
	require.Nil(t, pins.Mismatch())

	// A different key for the pinned host fails loudly with both fingerprints.
	err = cb("git.example.com:2222", addr, pubOf(t, b))
	var mm *gitssh.MismatchError
	require.ErrorAs(t, err, &mm)
	require.Equal(t, pin.Fingerprint, mm.Pinned[0].Fingerprint)
	require.Equal(t, ssh.FingerprintSHA256(pubOf(t, b)), mm.Offered.Fingerprint)
	require.Contains(t, err.Error(), "changed")
	require.Contains(t, err.Error(), "Accept new host key")
	require.Equal(t, mm, pins.Mismatch())

	// A host that was never pinned fails closed instead of being trusted.
	err = cb("other.example.com:22", addr, pubOf(t, a))
	var un *gitssh.UnpinnedError
	require.ErrorAs(t, err, &un)
	require.False(t, pins.Has("other.example.com"))
	require.True(t, pins.Has("git.example.com:2222"))
}

func TestPinsNegotiateOnlyThePinnedKeyType(t *testing.T) {
	a := gitsshtest.NewHostSigner(t)
	pins, err := gitssh.NewPins([]string{gitssh.NewHostKey("h", pubOf(t, a)).Line()})
	require.NoError(t, err)
	require.Equal(t, []string{ssh.KeyAlgoED25519}, pins.Algorithms("h:22"))
	require.Nil(t, pins.Algorithms("unpinned:22"))
}

func TestKnownHostsLineRoundTrip(t *testing.T) {
	a := gitsshtest.NewHostSigner(t)
	k := gitssh.NewHostKey("github.com:22", pubOf(t, a))
	require.Equal(t, "github.com", k.Host, "port 22 is not part of the name")

	got, err := gitssh.ParseKnownHostsLine(k.Line())
	require.NoError(t, err)
	require.Equal(t, k, got)

	got, err = gitssh.HostKeyFromAuthorized(k.Host, k.Authorized())
	require.NoError(t, err)
	require.Equal(t, k, got)

	for _, bad := range []string{"", "host", "host ssh-ed25519", "host ssh-ed25519 !!!!", "host ssh-rsa " + k.Key, "*.example.com " + k.Authorized(), "|1|salt|hash " + k.Authorized()} {
		_, err := gitssh.ParseKnownHostsLine(bad)
		require.Error(t, err, bad)
	}
	_, err = gitssh.NewPins([]string{"broken"})
	require.Error(t, err)
}

func TestScanHostKeyReadsTheKeyWithoutAuthenticating(t *testing.T) {
	t.Setenv(netguard.AllowEnv, "127.0.0.1")
	host := gitsshtest.NewHostSigner(t)
	srv := gitsshtest.Start(t, t.TempDir(), host)
	ep, err := gitssh.ParseURL("ssh://git@" + srv.Addr + "/app.git")
	require.NoError(t, err)

	got, err := gitssh.ScanHostKey(context.Background(), ep)
	require.NoError(t, err)
	require.Equal(t, ssh.FingerprintSHA256(host.PublicKey()), got.Fingerprint)
	require.Equal(t, ep.HostName(), got.Host)
	require.Zero(t, srv.AuthAttempts(), "the scan aborts before authentication")

	// The server presents a different key later: the scan shows it.
	other := gitsshtest.NewHostSigner(t)
	srv.SetHostKey(other)
	got2, err := gitssh.ScanHostKey(context.Background(), ep)
	require.NoError(t, err)
	require.Equal(t, ssh.FingerprintSHA256(other.PublicKey()), got2.Fingerprint)
	require.NotEqual(t, got.Fingerprint, got2.Fingerprint)
}

func TestScanHostKeyRefusesPrivateAddresses(t *testing.T) {
	host := gitsshtest.NewHostSigner(t)
	srv := gitsshtest.Start(t, t.TempDir(), host)
	ep, err := gitssh.ParseURL("ssh://git@" + srv.Addr + "/app.git")
	require.NoError(t, err)

	_, err = gitssh.ScanHostKey(context.Background(), ep)
	require.ErrorIs(t, err, netguard.ErrBlocked)
}

func TestScanHostKeyFailsWhenNothingListens(t *testing.T) {
	t.Setenv(netguard.AllowEnv, "127.0.0.1")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	ep, err := gitssh.ParseURL("ssh://git@" + addr + "/a.git")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = gitssh.ScanHostKey(ctx, ep)
	require.ErrorContains(t, err, "connect to")
}

// repoWithCommit makes a bare repository <root>/app.git with one commit on main.
func repoWithCommit(t *testing.T) (root, sha string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	root = t.TempDir()
	work := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return string(out)
	}
	git(work, "init", "-q")
	git(work, "commit", "-q", "--allow-empty", "-m", "init")
	git(root, "clone", "-q", "--bare", work, "app.git")
	out := git(work, "rev-parse", "HEAD")
	return root, out[:40]
}

func TestAuthResolvesRefsOverSSHAndEnforcesPins(t *testing.T) {
	root, sha := repoWithCommit(t)
	key, err := gitssh.Generate("test")
	require.NoError(t, err)
	authorized, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key.PublicKey))
	require.NoError(t, err)
	host := gitsshtest.NewHostSigner(t)
	srv := gitsshtest.Start(t, root, host, authorized)

	rawURL := "ssh://git@" + srv.Addr + "/app.git"
	ep, err := gitssh.ParseURL(rawURL)
	require.NoError(t, err)
	pin := gitssh.NewHostKey(ep.Addr(), host.PublicKey())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Pinned host + authorized key: the branch resolves.
	auth, pins, err := gitssh.Credentials{PrivateKeyPEM: key.PrivatePEM, KnownHosts: []string{pin.Line()}}.Auth(ep)
	require.NoError(t, err)
	got, err := gitssh.ResolveRef(ctx, rawURL, "main", auth)
	require.NoError(t, gitssh.Explain(err, ep, pins))
	require.Equal(t, sha, got)

	// No pin for the host: the handshake fails closed.
	auth, pins, err = gitssh.Credentials{PrivateKeyPEM: key.PrivatePEM}.Auth(ep)
	require.NoError(t, err)
	_, err = gitssh.ResolveRef(ctx, rawURL, "main", auth)
	var un *gitssh.UnpinnedError
	require.ErrorAs(t, gitssh.Explain(err, ep, pins), &un)

	// The server's host key changes: the clone fails with a mismatch that names both keys.
	attemptsBefore := srv.AuthAttempts()
	changed := gitsshtest.NewHostSigner(t)
	srv.SetHostKey(changed)
	auth, pins, err = gitssh.Credentials{PrivateKeyPEM: key.PrivatePEM, KnownHosts: []string{pin.Line()}}.Auth(ep)
	require.NoError(t, err)
	_, err = gitssh.ResolveRef(ctx, rawURL, "main", auth)
	require.Error(t, err)
	var mm *gitssh.MismatchError
	require.True(t, errors.As(gitssh.Explain(err, ep, pins), &mm), "got %v", err)
	require.Equal(t, pin.Fingerprint, mm.Pinned[0].Fingerprint)
	require.Equal(t, ssh.FingerprintSHA256(changed.PublicKey()), mm.Offered.Fingerprint)
	require.Equal(t, attemptsBefore, srv.AuthAttempts(), "a mismatch aborts before the deploy key is offered")

	// A key the server does not know is reported as such.
	srv.SetHostKey(host)
	stranger, err := gitssh.Generate("stranger")
	require.NoError(t, err)
	auth, pins, err = gitssh.Credentials{PrivateKeyPEM: stranger.PrivatePEM, KnownHosts: []string{pin.Line()}}.Auth(ep)
	require.NoError(t, err)
	_, err = gitssh.ResolveRef(ctx, rawURL, "main", auth)
	require.ErrorContains(t, gitssh.Explain(err, ep, pins), "deploy key was rejected")
}

func TestAuthNeedsAKey(t *testing.T) {
	ep, err := gitssh.ParseURL("git@github.com:o/r.git")
	require.NoError(t, err)
	_, _, err = gitssh.Credentials{}.Auth(ep)
	require.ErrorContains(t, err, "needs a deploy key")
}
