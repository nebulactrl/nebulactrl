//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// termConn is a browser-side terminal session.
type termConn struct {
	t  testing.TB
	ws *websocket.Conn
}

func (s *Stack) dialTerminal(serviceID string) *termConn {
	s.t.Helper()
	u, err := url.Parse(s.Srv.URL)
	require.NoError(s.t, err)
	hdr := http.Header{}
	for _, c := range s.client.Jar.Cookies(u) {
		hdr.Add("Cookie", c.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(s.Srv.URL, "http")+"/api/v1/services/"+serviceID+"/terminal/ws", &websocket.DialOptions{HTTPHeader: hdr})
	require.NoError(s.t, err)
	s.t.Cleanup(func() { _ = ws.CloseNow() })
	return &termConn{t: s.t, ws: ws}
}

func (c *termConn) sendJSON(v any) {
	c.t.Helper()
	b, _ := json.Marshal(v)
	require.NoError(c.t, c.ws.Write(context.Background(), websocket.MessageText, b))
}

func (c *termConn) typeText(text string) {
	c.t.Helper()
	require.NoError(c.t, c.ws.Write(context.Background(), websocket.MessageBinary, []byte(text)))
}

// readUntil reads until the terminal output contains want or a text message
// of type want arrives; it returns the accumulated output and the last text message.
func (c *termConn) readOutputUntil(want string, timeout time.Duration) string {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var out strings.Builder
	for !strings.Contains(out.String(), want) {
		typ, data, err := c.ws.Read(ctx)
		require.NoError(c.t, err, "waiting for %q; output so far: %q", want, out.String())
		if typ == websocket.MessageBinary {
			out.Write(data)
		}
	}
	return out.String()
}

// nextText reads until a text message of the given type arrives.
func (c *termConn) nextText(typ string, timeout time.Duration) J {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		mt, data, err := c.ws.Read(ctx)
		require.NoError(c.t, err, "waiting for a %q message", typ)
		if mt != websocket.MessageText {
			continue
		}
		var m J
		require.NoError(c.t, json.Unmarshal(data, &m))
		if m.Str("type") == typ {
			return m
		}
	}
}

func (s *Stack) auditActions(prefix string) []J {
	s.t.Helper()
	rows, err := s.Pool.Query(context.Background(), `SELECT action, resource_id, metadata FROM audit_events WHERE action LIKE $1 ORDER BY id`, prefix+"%")
	require.NoError(s.t, err)
	defer rows.Close()
	var out []J
	for rows.Next() {
		var action, rid string
		var md []byte
		require.NoError(s.t, rows.Scan(&action, &rid, &md))
		var m J
		require.NoError(s.t, json.Unmarshal(md, &m))
		out = append(out, J{"action": action, "resourceId": rid, "metadata": map[string]any(m)})
	}
	return out
}

func (s *Stack) raw(method, path string, body io.Reader, headers ...string) (int, []byte) {
	s.t.Helper()
	req, err := http.NewRequest(method, s.Srv.URL+"/api/v1"+path, body)
	require.NoError(s.t, err)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := s.client.Do(req)
	require.NoError(s.t, err)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

// TestTerminalAndFiles: a real shell in a real container through the browser
// protocol (resize included), the production confirmation, audit rows without
// any I/O, the session limit, and the file browser (list, read, read-only
// production, upload once an admin allows it).
func TestTerminalAndFiles(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	s := SharedStack(t) // the stack's environment is the project's production environment
	const image = "quay.io/prometheus/busybox:latest"
	had := localImageIDs(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if id, err := sh(ctx, "", "docker", "image", "inspect", "-f", "{{.Id}}", image); err == nil && !had[id] {
			_, _ = sh(ctx, "", "docker", "image", "rm", "-f", image)
		}
	})
	r := s.Do("POST", "/environments/"+s.EnvID+"/services", map[string]any{
		"name": "shell", "sourceType": "image", "serverId": s.ServerID,
		"config": map[string]any{
			"image": image, "stopGraceSeconds": 2, "startCommand": []string{"sh", "-c", "sleep 3600"},
			"healthcheck": map[string]any{"type": "exec", "command": []string{"CMD", "test", "-d", "/"}, "intervalSeconds": 1, "deadlineSeconds": 60, "successThreshold": 1},
		},
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	svc := r.JSON().Str("id")
	s.servicesMu.Lock()
	s.serviceIDs = append(s.serviceIDs, svc)
	s.servicesMu.Unlock()
	s.DeployOK(svc)
	// Availability. The agent reports the new replicas right behind the deploy
	// result; give that report a moment to land.
	r = s.Do("GET", "/services/"+svc+"/terminal", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Eventually(t, func() bool {
		r = s.Do("GET", "/services/"+svc+"/terminal", nil)
		return r.Status == http.StatusOK && r.JSON().Bool("available")
	}, 15*time.Second, 200*time.Millisecond, string(r.Raw))
	require.True(t, r.JSON().Bool("production"))
	require.False(t, r.JSON().Obj("fileWrites").Bool("allowed"), "production files are read-only by default")

	// Production without the confirmation is refused, and nothing is audited.
	c := s.dialTerminal(svc)
	c.sendJSON(map[string]any{"type": "open", "replica": 0, "cols": 100, "rows": 30, "confirmProduction": false})
	require.Equal(t, "confirmation_required", c.nextText("error", 10*time.Second).Str("code"))
	require.Empty(t, s.auditActions("terminal."))

	// A confirmed session: real shell, resize reaches the pty, exit code comes back.
	c = s.dialTerminal(svc)
	c.sendJSON(map[string]any{"type": "open", "replica": 0, "cols": 100, "rows": 30, "confirmProduction": true})
	opened := c.nextText("opened", 30*time.Second)
	require.NotEmpty(t, opened.Str("container"))
	c.typeText("stty size\n")
	c.readOutputUntil("30 100", 10*time.Second)
	c.sendJSON(map[string]any{"type": "resize", "cols": 140, "rows": 50})
	// The pty applies the resize asynchronously: keep asking for the size until
	// the new one shows instead of sleeping for a guessed time.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(150 * time.Millisecond):
				_ = c.ws.Write(context.Background(), websocket.MessageBinary, []byte("stty size\n"))
			}
		}
	}()
	c.readOutputUntil("50 140", 10*time.Second)
	close(stop)
	c.typeText("echo $((6*7)); echo term-canary-13579 > /tmp/x.txt; exit 5\n")
	c.readOutputUntil("42", 10*time.Second)
	exit := c.nextText("exit", 10*time.Second)
	require.Equal(t, 5, exit.Int("exitCode"))
	require.Equal(t, "exit", c.nextText("closed", 10*time.Second).Str("reason"))

	require.Eventually(t, func() bool { return len(s.auditActions("terminal.")) == 2 }, 10*time.Second, 200*time.Millisecond)
	events := s.auditActions("terminal.")
	require.Equal(t, "terminal.open", events[0].Str("action"))
	require.Equal(t, "terminal.close", events[1].Str("action"))
	require.Equal(t, svc, events[1].Str("resourceId"))
	cm := events[1]["metadata"].(map[string]any)
	require.EqualValues(t, 5, cm["exitCode"])
	require.EqualValues(t, 0, cm["replica"])
	require.Equal(t, "exit", cm["reason"])
	require.Contains(t, cm, "durationSeconds")
	all := fmt.Sprint(events)
	require.NotContains(t, all, "term-canary-13579", "terminal I/O is never recorded")
	require.NotContains(t, all, "stty")

	// File browser: list, read what the shell wrote, read-only production.
	r = s.Do("GET", "/services/"+svc+"/files?replica=0&path=/", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	listing := r.JSON()
	entries, _ := listing["entries"].([]any)
	var seen []string
	for _, e := range entries {
		seen = append(seen, e.(map[string]any)["name"].(string))
	}
	require.Contains(t, seen, "bin")
	require.Contains(t, seen, "etc")
	require.False(t, listing.Bool("writable"))

	status, body := s.raw("GET", "/services/"+svc+"/files/content?replica=0&path=/tmp/x.txt", nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "term-canary-13579\n", string(body))
	status, _ = s.raw("GET", "/services/"+svc+"/files/content?replica=0&path=/tmp", nil)
	require.Equal(t, http.StatusUnprocessableEntity, status, "a directory is not a file")
	status, _ = s.raw("GET", "/services/"+svc+"/files/content?replica=0&path=/no/such/file", nil)
	require.Equal(t, http.StatusNotFound, status)

	status, body = s.raw("POST", "/services/"+svc+"/files/upload?replica=0&path=/tmp&name=up.bin", bytes.NewReader([]byte{1, 2, 3}))
	require.Equal(t, http.StatusForbidden, status, string(body))
	require.Contains(t, string(body), "production_read_only")

	// An admin allows writes for the project; the upload then lands in the container.
	r = s.Do("GET", "/services/"+svc, nil)
	project := r.JSON().Str("projectId")
	r = s.Do("PUT", "/projects/"+project+"/terminal-settings", map[string]any{"allowProductionFileWrites": true})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	payload := bytes.Repeat([]byte{0, 1, 2, 255}, 300_000) // 1.2 MB, binary
	status, body = s.raw("POST", "/services/"+svc+"/files/upload?replica=0&path=/tmp&name=up.bin", bytes.NewReader(payload))
	require.Equal(t, http.StatusCreated, status, string(body))
	status, body = s.raw("GET", "/services/"+svc+"/files/content?replica=0&path=/tmp/up.bin", nil)
	require.Equal(t, http.StatusOK, status)
	require.True(t, bytes.Equal(payload, body), "the uploaded bytes come back unchanged")
	status, _ = s.raw("POST", "/services/"+svc+"/files/upload?replica=0&path=/nowhere&name=x", bytes.NewReader([]byte("x")))
	require.Equal(t, http.StatusNotFound, status, "the target directory must exist")
	status, _ = s.raw("POST", "/services/"+svc+"/files/upload?replica=0&path=/tmp&name=..%2Fevil", bytes.NewReader([]byte("x")))
	require.Equal(t, http.StatusUnprocessableEntity, status)

	reads := s.auditActions("file.")
	var kinds []string
	for _, e := range reads {
		kinds = append(kinds, e.Str("action"))
	}
	require.Contains(t, kinds, "file.read")
	require.Contains(t, kinds, "file.upload")
	require.NotContains(t, fmt.Sprint(reads), "term-canary-13579")

	// A token without deployments:write has no terminal and no files.
	ro := s.Token([]string{"projects:read"}, nil)
	require.Equal(t, http.StatusForbidden, s.DoToken(ro, "GET", "/services/"+svc+"/terminal", nil).Status)
	require.Equal(t, http.StatusForbidden, s.DoToken(ro, "GET", "/services/"+svc+"/files?path=/", nil).Status)

	// At most 3 sessions per person.
	var live []*termConn
	for range 3 {
		l := s.dialTerminal(svc)
		l.sendJSON(map[string]any{"type": "open", "replica": 0, "cols": 80, "rows": 24, "confirmProduction": true})
		l.nextText("opened", 30*time.Second)
		live = append(live, l)
	}
	fourth := s.dialTerminal(svc)
	fourth.sendJSON(map[string]any{"type": "open", "replica": 0, "cols": 80, "rows": 24, "confirmProduction": true})
	require.Equal(t, "too_many_sessions", fourth.nextText("error", 10*time.Second).Str("code"))
	for _, l := range live {
		l.sendJSON(map[string]any{"type": "close"})
		require.Equal(t, "client_closed", l.nextText("closed", 10*time.Second).Str("reason"))
	}
}
