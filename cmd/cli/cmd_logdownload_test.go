package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWhenFlag(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	got, err := whenFlag("from", "90m", now)
	require.NoError(t, err)
	require.Equal(t, now.Add(-90*time.Minute), got)
	got, err = whenFlag("from", "2026-10-27T01:02:03Z", now)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 10, 27, 1, 2, 3, 0, time.UTC), got)
	got, err = whenFlag("to", "2026-10-27", now)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC), got)
	got, err = whenFlag("to", "", now)
	require.NoError(t, err)
	require.True(t, got.IsZero())
	_, err = whenFlag("from", "yesterday", now)
	require.Error(t, err)

	f := logFileFlags{from: "2h", to: "3h", format: "txt"}
	_, err = f.query(now)
	require.Error(t, err, "--from must be before --to")
	f = logFileFlags{format: "csv"}
	_, err = f.query(now)
	require.Error(t, err)
}

func TestDeploymentsDownloadLogsMasksSecretsAndWritesPrivateFile(t *testing.T) {
	e := newEnv(t)
	e.login()
	svcID := e.seed()
	e.mustRun("", "vars", "set", svcID, "API_KEY="+canary, "--secret")
	e.finishNext(svcID, "failed", "boom")
	r := e.mustRun("", "--json", "services", "deploy", svcID)
	depID := decode[map[string]any](t, r.stdout)["id"].(string)
	require.Eventually(t, func() bool {
		var n int
		_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM deployments WHERE id = $1 AND status = 'failed'`, depID).Scan(&n)
		return n == 1
	}, 5*time.Second, 20*time.Millisecond)
	_, err := e.pool.Exec(context.Background(),
		`INSERT INTO deployment_logs (deployment_id, stream, line) VALUES ($1, 'build', 'token '||$2), ($1, 'deploy', 'done')`, depID, canary)
	require.NoError(t, err)

	out := e.mustRun("", "deployments", "download-logs", depID)
	require.NotContains(t, out.all(), canary)
	require.Contains(t, out.stdout, "[build] token ")
	require.Contains(t, out.stdout, "[deploy] done")

	file := filepath.Join(t.TempDir(), "d.ndjson")
	e.mustRun("", "deployments", "download-logs", depID, "--format", "ndjson", "-o", file)
	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	require.NotContains(t, string(raw), canary)
	require.Contains(t, string(raw), `"stream":"deploy"`)
	st, err := os.Stat(file)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm())

	// A refusal creates no file.
	missing := filepath.Join(t.TempDir(), "none.txt")
	bad := e.run("", "deployments", "download-logs", "00000000-0000-0000-0000-000000000000", "-o", missing)
	require.NotEqual(t, 0, bad.code)
	_, err = os.Stat(missing)
	require.True(t, os.IsNotExist(err))
}

func TestTasksTimezoneThroughTheCLI(t *testing.T) {
	e := newEnv(t)
	e.login()
	svcID := e.seed()
	e.mustRun("", "tasks", "create", svcID, "--name", "nightly", "--schedule", "0 3 * * *", "--timezone", "Europe/Berlin", "--", "rake", "x")
	r := e.mustRun("", "--json", "tasks", "ls", svcID)
	require.Contains(t, r.stdout, `"timezone": "Europe/Berlin"`)
	e.mustRun("", "tasks", "update", svcID, "nightly", "--timezone", "America/New_York")
	r = e.mustRun("", "tasks", "ls", svcID)
	require.Contains(t, r.stdout, "America/New_York")
	bad := e.run("", "tasks", "update", svcID, "nightly", "--timezone", "Mars/Base")
	require.NotEqual(t, 0, bad.code)
	require.Contains(t, bad.all(), "invalid_timezone")
}

func TestTasksEnableDisableThroughTheCLI(t *testing.T) {
	e := newEnv(t)
	e.login()
	svcID := e.seed()
	e.mustRun("", "tasks", "create", svcID, "--name", "nightly", "--schedule", "0 3 * * *", "--", "rake", "x")
	enabled := func() bool {
		items := decode[[]struct {
			Enabled bool `json:"enabled"`
		}](t, e.mustRun("", "--json", "tasks", "ls", svcID).stdout)
		require.Len(t, items, 1)
		return items[0].Enabled
	}
	require.True(t, enabled())

	e.mustRun("", "tasks", "update", svcID, "nightly", "--disable")
	require.False(t, enabled())
	e.mustRun("", "tasks", "update", svcID, "nightly", "--enable")
	require.True(t, enabled())

	both := e.run("", "tasks", "update", svcID, "nightly", "--enable", "--disable")
	require.Equal(t, 2, both.code)
	require.Contains(t, both.all(), "--enable or --disable")
	none := e.run("", "tasks", "update", svcID, "nightly")
	require.Equal(t, 2, none.code)
	require.True(t, enabled(), "a refused call changes nothing")
}
