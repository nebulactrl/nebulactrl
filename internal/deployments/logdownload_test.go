package deployments

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

func TestDeploymentLogDownload(t *testing.T) {
	f := newRouteFixture(t)
	bg := context.Background()
	web := f.web("site")
	dep := f.currentDeployment(web.ID)

	// The fixture's box has the same key: seal a secret variable for the service.
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	enc, err := box.SealString(secretbox.ServiceVariablesValue, secretbox.RowID(web.ID.String(), "API_KEY"), "sekret-canary-12345")
	require.NoError(t, err)
	_, err = f.q.UpsertServiceVariable(bg, db.UpsertServiceVariableParams{ServiceID: web.ID, Key: "API_KEY", ValueEnc: enc, IsSecret: true})
	require.NoError(t, err)

	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	for i, l := range []struct{ stream, line string }{
		{"build", "Step 1/3 : FROM alpine"},
		{"build", "pulling with token sekret-canary-12345"}, // stored before the variable existed
		{"deploy", "container started"},
		{"debug", "docker create ..."},
		{"runtime", "listening on :80"},
	} {
		_, err := f.pool.Exec(bg, `INSERT INTO deployment_logs (deployment_id, stream, line, created_at) VALUES ($1, $2, $3, $4)`,
			dep, l.stream, l.line, t0.Add(time.Duration(i)*time.Minute))
		require.NoError(t, err)
	}
	run := func(format, streams string, from, to time.Time) (*LogDownload, string) {
		t.Helper()
		dl, err := f.deps.OpenLogDownload(f.ctx, dep, format, streams, from, to)
		require.NoError(t, err)
		var buf bytes.Buffer
		dl.WriteTo(bg, services.NewLogDownloadTo(&buf, dl.Format()))
		return dl, buf.String()
	}

	// Text: the ordinary streams in recorded order, debug left out, secret masked.
	dl, txt := run("", "", time.Time{}, time.Time{})
	require.True(t, strings.HasPrefix(dl.Filename, "site-deploy-"+dep.String()[:8]+"-"), dl.Filename)
	require.True(t, strings.HasSuffix(dl.Filename, ".txt"))
	require.Equal(t,
		"2026-03-01T10:00:00Z [build] Step 1/3 : FROM alpine\n"+
			"2026-03-01T10:01:00Z [build] pulling with token ••••••\n"+
			"2026-03-01T10:02:00Z [deploy] container started\n"+
			"2026-03-01T10:04:00Z listening on :80\n", txt)
	require.NotContains(t, txt, "sekret-canary-12345")

	// Streams and a time range narrow the file; NDJSON carries the stream.
	_, nd := run("ndjson", "all", t0.Add(2*time.Minute), t0.Add(4*time.Minute))
	lines := strings.Split(strings.TrimSpace(nd), "\n")
	require.Len(t, lines, 2, "deploy and debug lines in [10:02, 10:04)")
	var first services.DownloadLine
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.Equal(t, "deploy", first.Stream)
	require.Equal(t, "container started", first.Line)
	_, onlyBuild := run("txt", "build", time.Time{}, time.Time{})
	require.Equal(t, 2, strings.Count(onlyBuild, "\n"))

	// Audited, without the log content.
	var n int
	require.NoError(t, f.pool.QueryRow(bg, `SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2`, ActionLogsDownload, dep.String()).Scan(&n))
	require.Equal(t, 3, n)

	// Validation and access.
	var ae *apierr.Error
	_, err = f.deps.OpenLogDownload(f.ctx, dep, "csv", "", time.Time{}, time.Time{})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status)
	_, err = f.deps.OpenLogDownload(f.ctx, dep, "", "nope", time.Time{}, time.Time{})
	require.ErrorAs(t, err, &ae)
	_, err = f.deps.OpenLogDownload(f.ctx, dep, "", "", t0, t0.Add(-time.Hour))
	require.ErrorAs(t, err, &ae)
	_, err = f.deps.OpenLogDownload(f.ctx, uuid.New(), "", "", time.Time{}, time.Time{})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusNotFound, ae.Status)
	tok := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeToken, TokenID: new(uuid.New()), UserID: uuid.New(), WorkspaceID: policy.WorkspaceID(f.ctx), Label: "tok", Scopes: []string{policy.ScopeProjectsRead}})
	_, err = f.deps.OpenLogDownload(tok, dep, "", "", time.Time{}, time.Time{})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusForbidden, ae.Status, "logs:read is required")
}

func TestDeploymentLogDownloadTenancyAndRedactor(t *testing.T) {
	f := newRouteFixture(t)
	bg := context.Background()
	dep := f.currentDeployment(f.web("site").ID)

	// A principal of another workspace cannot reach the deployment.
	other := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: uuid.New(), Label: "x", Scopes: policy.AllScopes})
	var ae *apierr.Error
	_, err := f.deps.OpenLogDownload(other, dep, "", "", time.Time{}, time.Time{})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusNotFound, ae.Status)

	// Without a redactor nothing is served: stored lines are never returned unmasked.
	bare := NewService(Deps{Pool: f.pool, Logger: f.deps.Logger})
	_, err = bare.OpenLogDownload(f.ctx, dep, "", "", time.Time{}, time.Time{})
	require.Error(t, err)
}
