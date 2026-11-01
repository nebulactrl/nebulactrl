package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// pagedAgent serves a two-page history and records every query it was asked.
func (f *fixture) pagedAgent(t0 time.Time, asked *[]agentprotocol.LogsQuery) {
	f.agents.online[f.srv1.ID] = true
	f.agents.handlers[f.srv1.ID] = func(_ string, payload json.RawMessage, _ map[string]string) agentprotocol.ResultFrame {
		var q agentprotocol.LogsQuery
		if err := json.Unmarshal(payload, &q); err != nil {
			panic(err)
		}
		*asked = append(*asked, q)
		res := agentprotocol.LogsQueryResult{Capturing: true}
		if q.Cursor == "" {
			res.Lines = []agentprotocol.HistoryLine{line(1, t0, "first"), line(2, t0.Add(time.Second), "token=hunter2-secret leaked")}
			res.NextCursor = "page-2"
		} else {
			res.Lines = []agentprotocol.HistoryLine{line(3, t0.Add(2*time.Second), "last")}
		}
		raw, _ := json.Marshal(res)
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: raw}
	}
}

func TestDownloadPagesRedactsAndAudits(t *testing.T) {
	f := newFixture(t)
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var asked []agentprotocol.LogsQuery
	f.pagedAgent(t0, &asked)

	from, to := t0.Add(-time.Hour), t0.Add(time.Hour)
	dl, err := f.svc.OpenDownload(f.ctx, f.web.ID, DownloadParams{From: from, To: to, Query: "level:error", Format: "ndjson"})
	require.NoError(t, err)
	require.Equal(t, services.DownloadNDJSON, dl.Format())
	require.True(t, strings.HasPrefix(dl.Filename, "web-logs-"), dl.Filename)
	require.True(t, strings.HasSuffix(dl.Filename, ".ndjson"), dl.Filename)

	var buf bytes.Buffer
	dl.WriteTo(f.ctx, services.NewLogDownloadTo(&buf, dl.Format()))

	// Every page was read, oldest first, over the requested range.
	require.Len(t, asked, 2)
	for _, q := range asked {
		require.Equal(t, agentprotocol.LogOrderAsc, q.Order)
		require.Equal(t, agentprotocol.LogQueryMaxLimit, q.Limit)
		require.True(t, q.From.Equal(from) && q.To.Equal(to), "the time range reaches the agent")
		require.Equal(t, "level:error", q.Query)
	}
	require.Equal(t, "page-2", asked[1].Cursor)

	var got []services.DownloadLine
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var d services.DownloadLine
		require.NoError(t, json.Unmarshal([]byte(l), &d), l)
		got = append(got, d)
	}
	require.Len(t, got, 3)
	require.Equal(t, []string{"first", "token=•••••• leaked", "last"}, []string{got[0].Line, got[1].Line, got[2].Line})
	require.Equal(t, "runtime", got[0].Stream)
	require.True(t, got[0].TS.Equal(t0))
	require.NotContains(t, buf.String(), "hunter2-secret")

	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2 AND NOT (metadata::text LIKE '%hunter2%')`,
		ActionServiceLogsDownload, f.web.ID.String()).Scan(&n))
	require.Equal(t, 1, n, "the export is audited once")

	// Text format: one timestamped line each.
	dl, err = f.svc.OpenDownload(f.ctx, f.web.ID, DownloadParams{})
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(dl.Filename, ".txt"), dl.Filename)
	buf.Reset()
	dl.WriteTo(f.ctx, services.NewLogDownloadTo(&buf, dl.Format()))
	require.Equal(t, "2026-01-01T12:00:00Z first\n2026-01-01T12:00:01Z token=•••••• leaked\n2026-01-01T12:00:02Z last\n", buf.String())
}

func TestDownloadRefusals(t *testing.T) {
	f := newFixture(t)
	var asked []agentprotocol.LogsQuery
	f.pagedAgent(time.Now(), &asked)
	var ae *apierr.Error

	// logs:read is required, and the project restriction hides other projects (404).
	_, err := f.svc.OpenDownload(f.tokenCtx([]string{policy.ScopeProjectsRead}), f.web.ID, DownloadParams{})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusForbidden, ae.Status)
	_, err = f.svc.OpenDownload(f.tokenCtx([]string{policy.ScopeLogsRead}, f.projA.ID), f.other.ID, DownloadParams{})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusNotFound, ae.Status)

	// A bad format and a backwards range are refused before anything is read.
	_, err = f.svc.OpenDownload(f.ctx, f.web.ID, DownloadParams{Format: "csv"})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status)
	now := time.Now()
	_, err = f.svc.OpenDownload(f.ctx, f.web.ID, DownloadParams{From: now, To: now.Add(-time.Hour)})
	require.Error(t, err)
	require.Empty(t, asked)

	// The server is offline: nothing to download, and no audit row.
	_, err = f.svc.OpenDownload(f.ctx, f.api.ID, DownloadParams{})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "server_offline", ae.Code)

	// Capture switched off: an empty file would mislead.
	f.agents.handlers[f.srv1.ID] = func(string, json.RawMessage, map[string]string) agentprotocol.ResultFrame {
		raw, _ := json.Marshal(agentprotocol.LogsQueryResult{Capturing: false})
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: raw}
	}
	_, err = f.svc.OpenDownload(f.ctx, f.web.ID, DownloadParams{})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "history_disabled", ae.Code)

	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = $1`, ActionServiceLogsDownload).Scan(&n))
	require.Zero(t, n, "refused downloads are not exports")
}

func TestDownloadNarrowsToAProcess(t *testing.T) {
	f := newFixture(t)
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cfg := `{"image":"nginx","processes":[{"name":"worker","startCommand":["bin/work"]}]}`
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET config = $2 WHERE id = $1", f.web.ID, cfg)
	require.NoError(t, err)
	var asked []agentprotocol.LogsQuery
	f.pagedAgent(t0, &asked)

	_, err = f.svc.OpenDownload(f.ctx, f.web.ID, DownloadParams{Process: "worker"})
	require.NoError(t, err)
	require.NotEmpty(t, asked)
	require.Equal(t, `"worker | "`, asked[0].Query)

	_, err = f.svc.OpenDownload(f.ctx, f.web.ID, DownloadParams{Process: "cron"})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "unknown_process", ae.Code)
}
