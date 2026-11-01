//go:build integration

package integration

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// totpCode is the RFC 6238 code (SHA-1, 6 digits, 30 s) of a base32 secret.
func totpCode(t testing.TB, secretB32 string, at time.Time) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secretB32))
	require.NoError(t, err)
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff) % 1_000_000
	return fmt.Sprintf("%06d", code)
}

// enableTwoFactor turns TOTP on for the stack's admin account.
func (s *Stack) enableTwoFactor() {
	s.t.Helper()
	r := s.Do("POST", "/me/2fa/setup", nil)
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
	secret := r.JSON().Str("secret")
	r = s.Do("POST", "/me/2fa/enable", map[string]any{"code": totpCode(s.t, secret, time.Now())})
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
}

// dialPath opens a WebSocket on an API path with the admin session cookie. It
// returns the HTTP response of a refused handshake.
func (s *Stack) dialPath(path string) (*termConn, *http.Response, error) {
	s.t.Helper()
	u, err := url.Parse(s.Srv.URL)
	require.NoError(s.t, err)
	hdr := http.Header{}
	for _, c := range s.client.Jar.Cookies(u) {
		hdr.Add("Cookie", c.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(s.Srv.URL, "http")+"/api/v1"+path, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		return nil, resp, err
	}
	s.t.Cleanup(func() { _ = ws.CloseNow() })
	return &termConn{t: s.t, ws: ws}, resp, nil
}

// refusedCode dials path and returns the HTTP status and problem code of a
// handshake the control plane refused.
func (s *Stack) refusedCode(path string) (int, string) {
	s.t.Helper()
	c, resp, err := s.dialPath(path)
	if err == nil {
		_ = c.ws.CloseNow()
		return 0, ""
	}
	require.NotNil(s.t, resp, "the handshake should be refused with an HTTP response: %v", err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, Resp{Raw: raw}.JSON().Str("code")
}

// waitShellReady keeps asking a freshly opened shell to answer until it does:
// a login shell reads its profile first, and typed-ahead input may be dropped.
// The probe's answer (ready-2) never appears in the echo of the probe itself.
func (c *termConn) waitShellReady() {
	c.t.Helper()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			_ = c.ws.Write(context.Background(), websocket.MessageBinary, []byte("echo ready-$((1+1))\n"))
			select {
			case <-stop:
				return
			case <-time.After(300 * time.Millisecond):
			}
		}
	}()
	c.readOutputUntil("ready-2", 30*time.Second)
	close(stop)
	wg.Wait()
}

// TestHostShell: the opt-in host shell on the test server. Off by default
// (403 host_shell_disabled), refused without two-factor authentication on the
// account (403 two_factor_required), then a real shell on the machine the
// agent runs on: `uname -n` answers with this host's name. The session is
// audited (open and close, with the exit code) without any of its I/O, at most
// 2 are open at once, and switching the setting off refuses new sessions.
//
// The harness agent runs on the host, so this exercises the direct-pty mode.
// (The helper-container mode, for an agent that runs in a container, is covered
// by the agent's unit tests.)
func TestHostShell(t *testing.T) {
	requireTier(t, tierFull)
	s := ExclusiveStack(t)
	path := "/servers/" + s.ServerID + "/shell/ws"

	// Off by default.
	r := s.Do("GET", "/servers/"+s.ServerID+"/host-shell", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.False(t, r.JSON().Bool("enabled"))
	require.False(t, r.JSON().Bool("available"))
	status, code := s.refusedCode(path)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "host_shell_disabled", code)

	// An admin turns it on; the account still has no 2FA.
	r = s.Do("PUT", "/servers/"+s.ServerID+"/host-shell", map[string]any{"enabled": true})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.True(t, r.JSON().Bool("enabled"))
	status, code = s.refusedCode(path)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "two_factor_required", code)
	r = s.Do("GET", "/servers/"+s.ServerID+"/host-shell", nil)
	require.False(t, r.JSON().Bool("available"))
	require.False(t, r.JSON().Bool("twoFactorEnabled"))

	// A token never gets one, whatever its scopes.
	tok := s.Token([]string{"admin"}, nil)
	require.Equal(t, http.StatusForbidden, s.DoToken(tok, "GET", "/servers/"+s.ServerID+"/host-shell", nil).Status)
	require.Equal(t, http.StatusForbidden, s.DoToken(tok, "PUT", "/servers/"+s.ServerID+"/host-shell", map[string]any{"enabled": false}).Status)

	// With 2FA the shell opens, on this machine.
	s.enableTwoFactor()
	r = s.Do("GET", "/servers/"+s.ServerID+"/host-shell", nil)
	require.True(t, r.JSON().Bool("available"), string(r.Raw))
	require.False(t, r.JSON().Bool("production"), "no production service runs on the test server yet")

	want, err := exec.Command("uname", "-n").Output()
	require.NoError(t, err)
	hostname := strings.TrimSpace(string(want))
	require.NotEmpty(t, hostname)

	c, _, err := s.dialPath(path)
	require.NoError(t, err)
	c.sendJSON(map[string]any{"type": "open", "cols": 120, "rows": 30, "confirmProduction": false})
	opened := c.nextText("opened", 30*time.Second)
	require.Equal(t, "host", opened.Str("container"), "the agent runs on the host: its own pty, no helper container")
	// A login shell runs the user's profile before it reads input (and some
	// profiles flush what was typed ahead), so wait until it answers.
	c.waitShellReady()
	c.typeText("uname -n; echo marker-$((6*7))-secret-canary-4242\n")
	out := c.readOutputUntil("marker-42-secret-canary-4242", 20*time.Second)
	require.Contains(t, out, hostname, "the shell runs on the host: uname -n prints its name")

	// A second and a third: at most 2 per person.
	c2, _, err := s.dialPath(path)
	require.NoError(t, err)
	c2.sendJSON(map[string]any{"type": "open", "cols": 80, "rows": 24, "confirmProduction": false})
	c2.nextText("opened", 30*time.Second)
	c3, _, err := s.dialPath(path)
	require.NoError(t, err)
	c3.sendJSON(map[string]any{"type": "open", "cols": 80, "rows": 24, "confirmProduction": false})
	require.Equal(t, "too_many_sessions", c3.nextText("error", 10*time.Second).Str("code"))
	c2.sendJSON(map[string]any{"type": "close"})
	require.Equal(t, "client_closed", c2.nextText("closed", 10*time.Second).Str("reason"))

	// The shell ends: the exit message and the session's end are reported. The
	// exact status is whatever the machine's own login profile makes of it (the
	// pty unit tests pin real codes), so only its presence is asserted.
	c.typeText("exit 3\n")
	exit := c.nextText("exit", 15*time.Second)
	require.Contains(t, exit, "exitCode")
	require.Equal(t, "exit", c.nextText("closed", 10*time.Second).Str("reason"))
	require.Eventually(t, func() bool { return len(s.auditActions("server.shell.")) >= 5 }, 15*time.Second, 200*time.Millisecond)
	var actions []string
	for _, e := range s.auditActions("server.shell.") {
		actions = append(actions, e.Str("action"))
		require.Equal(t, s.ServerID, e.Str("resourceId"))
	}
	require.Equal(t, []string{
		"server.shell.enable", "server.shell.open", "server.shell.open", "server.shell.close", "server.shell.close",
	}, actions[:5], "enable, then two sessions opening and closing")
	events := s.auditActions("server.shell.")
	var exited bool
	for _, e := range events {
		if e.Str("action") != "server.shell.close" {
			continue
		}
		md := e["metadata"].(map[string]any)
		require.Contains(t, md, "durationSeconds")
		require.Equal(t, s.ServerID, md["serverId"])
		if md["reason"] == "exit" {
			require.Contains(t, md, "exitCode")
			exited = true
		}
	}
	require.True(t, exited, "the session that exited is audited with its exit code")
	require.NotContains(t, fmt.Sprint(events), "secret-canary-4242", "terminal I/O is never recorded")
	require.NotContains(t, fmt.Sprint(events), "uname")

	// Off again: new sessions are refused.
	r = s.Do("PUT", "/servers/"+s.ServerID+"/host-shell", map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	status, code = s.refusedCode(path)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "host_shell_disabled", code)
}
