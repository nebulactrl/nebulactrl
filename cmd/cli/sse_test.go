package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadSSE(t *testing.T) {
	stream := ": ping\n\n" +
		"data: {\"line\":\"one\"}\n\n" +
		"event: custom\r\ndata: a\ndata: b\r\n\r\n" +
		"event: end\ndata: {}\n\n" +
		"data: after-end\n\n"
	var got []sseEvent
	err := readSSE(strings.NewReader(stream), func(ev sseEvent) bool {
		got = append(got, ev)
		return ev.Name != "end"
	})
	require.NoError(t, err)
	require.Equal(t, []sseEvent{
		{Name: "message", Data: `{"line":"one"}`},
		{Name: "custom", Data: "a\nb"},
		{Name: "end", Data: "{}"},
	}, got)
}

func TestReadSSEFlushesUnterminatedEvent(t *testing.T) {
	var got []sseEvent
	require.NoError(t, readSSE(strings.NewReader("data: tail"), func(ev sseEvent) bool {
		got = append(got, ev)
		return true
	}))
	require.Equal(t, []sseEvent{{Name: "message", Data: "tail"}}, got)
}

// logServer serves the service log endpoint as SSE and records the request.
func logServer(t *testing.T, events []string) (*httptest.Server, *atomic.Pointer[http.Request]) {
	t.Helper()
	var seen atomic.Pointer[http.Request]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Clone(context.Background()))
		if r.Header.Get("Authorization") != "Bearer nct_test" {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":401,"code":"unauthorized","title":"Unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, e := range events {
			fmt.Fprint(w, e)
			fl.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

const svcID = "0b6f4a2e-7c1d-4f5a-9d3e-0123456789ab"

func offlineCLI(t *testing.T, url string, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv(envConfig, t.TempDir()+"/config.json")
	t.Setenv(envURL, url)
	t.Setenv(envToken, "nct_test")
	var out, errOut bytes.Buffer
	c := &cli{in: strings.NewReader(""), out: &out, errOut: &errOut, pollEvery: time.Millisecond}
	code := c.execute(context.Background(), args)
	return code, out.String(), errOut.String()
}

func TestServicesLogsFollowConsumesSSE(t *testing.T) {
	srv, last := logServer(t, []string{
		": ping\n\n",
		`data: {"ts":"2026-01-02T03:04:05Z","line":"hello","serviceId":"` + svcID + "\"}\n\n",
		`data: {"ts":"2026-01-02T03:04:06Z","line":"world","serviceId":"` + svcID + "\"}\n\n",
		"event: end\ndata: {}\n\n",
		`data: {"ts":"2026-01-02T03:04:07Z","line":"never shown"}` + "\n\n",
	})

	code, out, errOut := offlineCLI(t, srv.URL, "services", "logs", svcID, "-f", "--tail", "7")
	require.Equal(t, 0, code, errOut)
	require.Equal(t, "hello\nworld\n", out)
	seen := last.Load()
	require.NotNil(t, seen)
	require.Equal(t, "/api/v1/services/"+svcID+"/logs", seen.URL.Path)
	require.Equal(t, "true", seen.URL.Query().Get("follow"))
	require.Equal(t, "7", seen.URL.Query().Get("tail"))

	code, out, errOut = offlineCLI(t, srv.URL, "services", "logs", svcID, "-f", "--timestamps")
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "2026-01-02T03:04:05Z hello\n")

	// --json emits one raw event per line.
	code, out, errOut = offlineCLI(t, srv.URL, "--json", "services", "logs", svcID, "-f")
	require.Equal(t, 0, code, errOut)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 2)
	for _, l := range lines {
		var e map[string]any
		require.NoError(t, json.Unmarshal([]byte(l), &e), l)
	}
}

func TestFollowSurfacesProblemResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":503,"code":"server_offline","title":"Service Unavailable","detail":"server box1 is offline"}`))
	}))
	t.Cleanup(srv.Close)
	code, out, errOut := offlineCLI(t, srv.URL, "services", "logs", svcID, "-f")
	require.Equal(t, 1, code)
	require.Empty(t, out)
	require.Contains(t, errOut, "server box1 is offline")
	require.Contains(t, errOut, "server_offline")
}

// Backing up a read replica is refused by the API; the CLI prints the reason,
// the code and where to go instead, and exits non-zero.
func TestBackupsOnReplicaSurfacesRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/backup-destinations" {
			_, _ = w.Write([]byte(`{"items":[{"id":"dest-1","name":"minio"}]}`))
			return
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"status":422,"code":"backup_replica_unsupported","title":"Unprocessable Entity","detail":"this database is a read replica: back up its primary instead (backups run on the current primary only)"}`))
	}))
	t.Cleanup(srv.Close)
	for _, args := range [][]string{
		{"backups", "create", svcID, "--destination", "minio"},
		{"backups", "schedules", "add", svcID, "--destination", "minio", "--cron", "@daily"},
	} {
		code, out, errOut := offlineCLI(t, srv.URL, args...)
		require.Equal(t, 1, code, "%v: %s", args, errOut)
		require.Empty(t, out)
		require.Contains(t, errOut, "read replica")
		require.Contains(t, errOut, "backup_replica_unsupported")
		require.Contains(t, errOut, "databases replicas ls")
	}
}

func TestProblemNeverEchoesSubmittedValues(t *testing.T) {
	e := problem(422, []byte(`{"status":422,"code":"validation_failed","title":"Unprocessable Entity","errors":[{"location":"body.variables[0].key","message":"invalid key","value":"sekret-canary-12345"}]}`))
	require.NotContains(t, e.Error(), "sekret-canary-12345")
	require.Contains(t, e.Error(), "body.variables[0].key: invalid key")
}

func TestServiceRefRequiresIDOrTriple(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	code, _, errOut := offlineCLI(t, srv.URL, "services", "restart", "just-a-name")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "project/env/service-slug")
}

func TestVersionFlag(t *testing.T) {
	var out bytes.Buffer
	require.Equal(t, 0, run(context.Background(), []string{"--version"}, strings.NewReader(""), &out, &out))
	require.Contains(t, out.String(), version)
}
