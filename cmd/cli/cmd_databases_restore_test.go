package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const restoreSvc = "11111111-1111-1111-1111-111111111111"
const restoreID = "22222222-2222-2222-2222-222222222222"

// fakeRestoreAPI is the control plane's dump restore API, scripted.
type fakeRestoreAPI struct {
	mu        sync.Mutex
	t         *testing.T
	createdBy map[string]any
	gets      int
	uploaded  []byte
	uploadLen int64
	auth      string
	query     string
	// statuses are answered by successive GETs (the last one repeats).
	statuses []map[string]any
	// uploadStatus/uploadBody replace the upload answer.
	uploadStatus int
	uploadBody   string
	dropUpload   bool
}

func view(status string, extra map[string]any) map[string]any {
	m := map[string]any{
		"id": restoreID, "mode": "new", "sourceServiceId": restoreSvc, "status": status, "sizeBytes": 0, "bytesReceived": 0,
		"maxUploadBytes": 5 << 30, "createdAt": time.Now().UTC().Format(time.RFC3339),
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func (f *fakeRestoreAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/services/"+restoreSvc+"/dump-restores", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewDecoder(r.Body).Decode(&f.createdBy)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(view("provisioning", map[string]any{"targetServiceId": "33333333-3333-3333-3333-333333333333"}))
	})
	mux.HandleFunc("GET /api/v1/dump-restores/"+restoreID, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		i := min(f.gets, len(f.statuses)-1)
		f.gets++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.statuses[i])
	})
	mux.HandleFunc("PUT /api/v1/dump-restores/"+restoreID+"/upload", func(w http.ResponseWriter, r *http.Request) {
		if f.dropUpload {
			// Refuse and hang up without reading the body, like a server that rejects the file.
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.uploaded, f.uploadLen, f.auth, f.query = b, r.ContentLength, r.Header.Get("Authorization"), r.URL.RawQuery
		status, body := f.uploadStatus, f.uploadBody
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if status == 0 {
			status = http.StatusAccepted
			body = mustJSON(view("restoring", map[string]any{"sizeBytes": len(b)}))
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
	return mux
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func restoreRun(t *testing.T, f *fakeRestoreAPI, args ...string) (int, string, string) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	t.Setenv(envConfig, filepath.Join(t.TempDir(), "cfg", "config.json"))
	var out, errOut bytes.Buffer
	c := &cli{in: strings.NewReader(""), out: &out, errOut: &errOut, pollEvery: time.Millisecond}
	require.NoError(t, c.dial(config{URL: srv.URL, Token: "nct_secret_token"}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code := c.execute(ctx, args)
	return code, out.String(), errOut.String()
}

func dumpFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "shop.pgdump")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func TestDatabasesRestoreUploadsAndWaits(t *testing.T) {
	f := &fakeRestoreAPI{t: t, statuses: []map[string]any{
		view("provisioning", nil),
		view("awaiting_upload", map[string]any{"targetServiceId": "33333333-3333-3333-3333-333333333333"}),
		view("restoring", map[string]any{"log": "10:00:00 restoring into the scratch database\n"}),
		view("succeeded", map[string]any{
			"targetServiceId": "33333333-3333-3333-3333-333333333333",
			"log":             "10:00:00 restoring into the scratch database\n10:00:09 restored\n",
		}),
	}}
	content := "PGDMP" + strings.Repeat("x", 100_000)
	code, out, errOut := restoreRun(t, f, "databases", "restore", restoreSvc, "--file", dumpFile(t, content), "--name", "shop-restored")
	require.Equal(t, 0, code, "stdout=%s stderr=%s", out, errOut)

	require.Equal(t, content, string(f.uploaded), "the file goes out byte for byte")
	require.EqualValues(t, len(content), f.uploadLen, "with an exact Content-Length")
	require.Equal(t, "Bearer nct_secret_token", f.auth)
	require.Equal(t, "fileName=shop.pgdump", f.query)
	require.Equal(t, "new", f.createdBy["mode"])
	require.Equal(t, "shop-restored", f.createdBy["name"])
	require.Equal(t, "shop.pgdump", f.createdBy["fileName"])
	require.EqualValues(t, len(content), f.createdBy["sizeBytes"], "the size is announced up front")

	require.Contains(t, out, "Restored into the new database 33333333-3333-3333-3333-333333333333")
	for _, want := range []string{"the new database is 33333333", "uploading 97.7 KiB of 97.7 KiB (100%)", "restoring into the scratch database", "10:00:09 restored"} {
		require.Contains(t, errOut, want)
	}
	require.Equal(t, 1, strings.Count(errOut, "restoring into the scratch database"), "a log line is printed once")
	require.NotContains(t, out+errOut, "nct_secret_token")
	_ = sha256.Sum256
}

func TestDatabasesRestoreFailureExitsNonZero(t *testing.T) {
	f := &fakeRestoreAPI{t: t, statuses: []map[string]any{
		view("awaiting_upload", nil),
		view("failed", map[string]any{"error": "pg_restore exited with status 1: relation \"x\" already exists"}),
	}}
	code, _, errOut := restoreRun(t, f, "databases", "restore", restoreSvc, "--file", dumpFile(t, "PGDMP-data"))
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "failed: pg_restore exited with status 1")
}

func TestDatabasesRestoreNoWaitReturnsAfterTheUpload(t *testing.T) {
	f := &fakeRestoreAPI{t: t, statuses: []map[string]any{view("awaiting_upload", nil)}}
	code, out, errOut := restoreRun(t, f, "databases", "restore", restoreSvc, "--file", dumpFile(t, "PGDMP-data"), "--wait=false")
	require.Equal(t, 0, code, errOut)
	require.Equal(t, "PGDMP-data", string(f.uploaded))
	require.Contains(t, out, "is running on the server")
}

func TestDatabasesRestoreJSON(t *testing.T) {
	f := &fakeRestoreAPI{t: t, statuses: []map[string]any{
		view("awaiting_upload", nil), view("succeeded", map[string]any{"format": "pg_custom"}),
	}}
	code, out, errOut := restoreRun(t, f, "--json", "databases", "restore", restoreSvc, "--file", dumpFile(t, "PGDMP"))
	require.Equal(t, 0, code, errOut)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	require.Equal(t, "succeeded", got["status"])
	require.Equal(t, "pg_custom", got["format"])
	require.Empty(t, errOut, "--json keeps progress off stderr too")
}

func TestDatabasesRestoreInPlaceSendsConfirmationAndDestination(t *testing.T) {
	f := &fakeRestoreAPI{t: t, statuses: []map[string]any{view("awaiting_upload", nil), view("succeeded", nil)}}
	code, _, errOut := restoreRun(t, f, "databases", "restore", restoreSvc, "--file", dumpFile(t, "PGDMP"), "--in-place", "--confirm", "main")
	require.Equal(t, 0, code, errOut)
	require.Equal(t, "in_place", f.createdBy["mode"])
	require.Equal(t, "main", f.createdBy["confirmSlug"])
	require.NotContains(t, f.createdBy, "name")
	require.Contains(t, errOut, "safety backup")
}

func TestDatabasesRestoreUploadRefusal(t *testing.T) {
	f := &fakeRestoreAPI{
		t: t, statuses: []map[string]any{view("awaiting_upload", nil)},
		uploadStatus: http.StatusUnprocessableEntity,
		uploadBody:   `{"status":422,"code":"unsupported_dump_format","title":"Unprocessable Entity","detail":"this is a MySQL/MariaDB SQL dump; it cannot be restored into a PostgreSQL database"}`,
	}
	code, _, errOut := restoreRun(t, f, "databases", "restore", restoreSvc, "--file", dumpFile(t, "-- MySQL dump\n"))
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "MySQL/MariaDB SQL dump")
	require.Contains(t, errOut, "unsupported_dump_format")
}

func TestDatabasesRestoreConnectionClosedByServerReportsItsReason(t *testing.T) {
	// The server hung up on the upload; the restore's own error explains why.
	f := &fakeRestoreAPI{
		t: t, dropUpload: true,
		statuses: []map[string]any{view("awaiting_upload", nil), view("awaiting_upload", map[string]any{"error": "the file is empty"})},
	}
	code, _, errOut := restoreRun(t, f, "databases", "restore", restoreSvc, "--file", dumpFile(t, strings.Repeat("x", 1<<20)))
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "upload failed")
}

func TestDatabasesRestoreArguments(t *testing.T) {
	f := &fakeRestoreAPI{t: t, statuses: []map[string]any{view("awaiting_upload", nil)}}
	code, _, errOut := restoreRun(t, f, "databases", "restore", restoreSvc)
	require.Equal(t, 2, code)
	require.Contains(t, errOut, "--file")
	code, _, errOut = restoreRun(t, f, "databases", "restore", restoreSvc, "--file", dumpFile(t, "x"), "--in-place")
	require.Equal(t, 2, code)
	require.Contains(t, errOut, "--confirm")
	code, _, _ = restoreRun(t, f, "databases", "restore", restoreSvc, "--file", filepath.Join(t.TempDir(), "missing.dump"))
	require.NotEqual(t, 0, code)
	code, _, errOut = restoreRun(t, f, "databases", "restore", restoreSvc, "--file", dumpFile(t, ""))
	require.Equal(t, 2, code)
	require.Contains(t, errOut, "empty")
	require.Nil(t, f.createdBy, "nothing is created for a bad invocation")
}

func TestNewLogLines(t *testing.T) {
	log := "a\nb\nc\n"
	require.Equal(t, []string{"a", "b", "c"}, newLogLines("", log))
	require.Equal(t, []string{"c"}, newLogLines("b", log))
	require.Empty(t, newLogLines("c", log))
	require.Equal(t, []string{"a", "b", "c"}, newLogLines("gone", log), "a line that rolled out of the log restarts it")
}

func TestFormatBytes(t *testing.T) {
	require.Equal(t, "5.0 GiB", formatBytes(5<<30))
	require.Equal(t, "512 B", formatBytes(512))
	require.Equal(t, "97.7 KiB", formatBytes(100_000))
}
