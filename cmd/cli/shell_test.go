package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// fakeTerminal is the server side of the terminal protocol.
type fakeTerminal struct {
	mu     sync.Mutex
	header http.Header
	open   map[string]any
	input  bytes.Buffer
	resize []map[string]any
	// script runs after the open message; it may write to the socket.
	script func(ctx context.Context, ws *websocket.Conn)
}

func (f *fakeTerminal) handler(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		f.mu.Lock()
		f.header = r.Header.Clone()
		f.mu.Unlock()
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		ctx := r.Context()
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		f.mu.Lock()
		_ = json.Unmarshal(data, &f.open)
		f.mu.Unlock()
		if f.script != nil {
			f.script(ctx, ws)
			return
		}
		_ = ws.Write(ctx, websocket.MessageText, []byte(`{"type":"opened","container":"c"}`))
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				f.mu.Lock()
				f.input.Write(data)
				f.mu.Unlock()
				if strings.Contains(string(data), "exit") {
					_ = ws.Write(ctx, websocket.MessageBinary, []byte("logout\r\n"))
					_ = ws.Write(ctx, websocket.MessageText, []byte(`{"type":"exit","exitCode":3}`))
					_ = ws.Write(ctx, websocket.MessageText, []byte(`{"type":"closed","reason":"exit"}`))
					_ = ws.Close(websocket.StatusNormalClosure, "exit")
					return
				}
				_ = ws.Write(ctx, websocket.MessageBinary, append([]byte("echo:"), data...))
				continue
			}
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			if m["type"] == "resize" {
				f.mu.Lock()
				f.resize = append(f.resize, m)
				f.mu.Unlock()
			}
		}
	})
}

func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/terminal/ws"
}

func TestRunShellRelaysAndReturnsTheExitCode(t *testing.T) {
	f := &fakeTerminal{}
	srv := httptest.NewServer(f.handler(0))
	defer srv.Close()

	pr, pw := io.Pipe()
	resizes := make(chan [2]int, 1)
	var out syncBuffer
	done := make(chan struct{})
	var code int
	var err error
	go func() {
		defer close(done)
		code, err = runShell(context.Background(), shellOpts{
			URL: wsURL(srv), Token: "nct_secret", Process: "worker", Replica: 1, Command: []string{"psql", "-U", "postgres"},
			Production: true, Cols: 120, Rows: 40, Resizes: resizes,
		}, pr, &out)
	}()

	_, _ = pw.Write([]byte("ls\n"))
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "echo:ls\n") }, 3*time.Second, 5*time.Millisecond)
	resizes <- [2]int{150, 50}
	require.Eventually(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.resize) == 1 }, 3*time.Second, 5*time.Millisecond)
	_, _ = pw.Write([]byte("exit\n"))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not end")
	}
	require.NoError(t, err)
	require.Equal(t, 3, code, "the shell's exit code is the command's exit code")
	require.Contains(t, out.String(), "logout")

	f.mu.Lock()
	defer f.mu.Unlock()
	require.Equal(t, "Bearer nct_secret", f.header.Get("Authorization"))
	require.Equal(t, "worker", f.open["process"])
	require.EqualValues(t, 1, f.open["replica"])
	require.EqualValues(t, 120, f.open["cols"])
	require.EqualValues(t, 40, f.open["rows"])
	require.Equal(t, true, f.open["confirmProduction"])
	require.Equal(t, []any{"psql", "-U", "postgres"}, f.open["command"])
	require.EqualValues(t, 150, f.resize[0]["cols"])
	require.EqualValues(t, 50, f.resize[0]["rows"])
}

func TestRunShellMapsServerErrors(t *testing.T) {
	cases := map[string]string{
		"confirmation_required": "--production",
		"too_many_sessions":     "Close one first",
		"server_offline":        "offline",
	}
	for code, want := range cases {
		f := &fakeTerminal{script: func(ctx context.Context, ws *websocket.Conn) {
			msg, _ := json.Marshal(map[string]any{"type": "error", "code": code, "message": "Close one first. The server is offline."})
			_ = ws.Write(ctx, websocket.MessageText, msg)
			_ = ws.Close(websocket.StatusNormalClosure, code)
		}}
		srv := httptest.NewServer(f.handler(0))
		_, err := runShell(context.Background(), shellOpts{URL: wsURL(srv), Token: "t"}, strings.NewReader(""), io.Discard)
		srv.Close()
		require.Error(t, err, code)
		require.Contains(t, err.Error(), want, code)
	}
}

func TestRunShellHandshakeStatuses(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusUnauthorized: "token",
		http.StatusForbidden:    "Developer role",
		http.StatusNotFound:     "not found",
		http.StatusConflict:     "no container",
	} {
		f := &fakeTerminal{}
		srv := httptest.NewServer(f.handler(status))
		_, err := runShell(context.Background(), shellOpts{URL: wsURL(srv), Token: "t"}, strings.NewReader(""), io.Discard)
		srv.Close()
		require.Error(t, err)
		require.Contains(t, err.Error(), want, "HTTP %d", status)
	}
}

func TestRunShellReportsIdleClose(t *testing.T) {
	f := &fakeTerminal{script: func(ctx context.Context, ws *websocket.Conn) {
		_ = ws.Write(ctx, websocket.MessageText, []byte(`{"type":"opened"}`))
		_ = ws.Write(ctx, websocket.MessageText, []byte(`{"type":"idle_warning","secondsLeft":60}`))
		_ = ws.Write(ctx, websocket.MessageText, []byte(`{"type":"closed","reason":"idle"}`))
		_ = ws.Close(websocket.StatusNormalClosure, "idle")
	}}
	srv := httptest.NewServer(f.handler(0))
	defer srv.Close()
	var notice bytes.Buffer
	pr, _ := io.Pipe()
	_, err := runShell(context.Background(), shellOpts{URL: wsURL(srv), Token: "t", Notice: &notice}, pr, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "idle")
	require.Contains(t, notice.String(), "closes in 60 seconds")
}

func TestShellURL(t *testing.T) {
	got, err := shellURL("https://cp.example.com/api/v1", "abc")
	require.NoError(t, err)
	require.Equal(t, "wss://cp.example.com/api/v1/services/abc/terminal/ws", got)
	got, err = shellURL("http://127.0.0.1:8080/api/v1/", "abc")
	require.NoError(t, err)
	require.Equal(t, "ws://127.0.0.1:8080/api/v1/services/abc/terminal/ws", got)
	_, err = shellURL("ftp://x/api/v1", "abc")
	require.Error(t, err)
}

// syncBuffer is a bytes.Buffer safe for the concurrent writer and reader of a test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
