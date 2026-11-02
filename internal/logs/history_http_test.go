package logs

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestHistoryEndpointsPassTheQueryToTheAgentAndRedact(t *testing.T) {
	f := newFixture(t)
	do := f.serve()
	o := principalOf(f.ctx)
	t0 := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	f.agents.online[f.srv1.ID] = true
	var got agentprotocol.LogsQuery
	f.agents.handlers[f.srv1.ID] = func(kind string, payload json.RawMessage, _ map[string]string) agentprotocol.ResultFrame {
		require.Equal(t, agentprotocol.KindLogsQuery, kind)
		require.NoError(t, json.Unmarshal(payload, &got))
		res := queryResult(line(2, t0.Add(time.Minute), "token hunter2-secret leaked"), line(1, t0, "started"))
		return res
	}
	base := "/services/" + f.web.ID.String() + "/logs/history"

	code, _, raw := do(o, http.MethodGet, base+"?"+url.Values{
		"q": {"level:error"}, "limit": {"25"}, "order": {"asc"}, "cursor": {"abc"},
		"from": {t0.Format(time.RFC3339)}, "to": {t0.Add(time.Hour).Format(time.RFC3339)},
	}.Encode(), nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Equal(t, f.web.ID.String(), got.ServiceID)
	require.Equal(t, "web", got.ServiceName)
	require.Equal(t, 25, got.Limit)
	require.Equal(t, agentprotocol.LogOrderAsc, got.Order)
	require.Equal(t, "abc", got.Cursor)
	require.True(t, got.From.Equal(t0))
	require.True(t, got.To.Equal(t0.Add(time.Hour)))
	require.Contains(t, got.Query, "level:error")
	page := asMap(t, raw)
	items := page["items"].([]any)
	require.Len(t, items, 2)
	require.Equal(t, "started", items[0].(map[string]any)["line"], "oldest first when asked")
	require.NotContains(t, string(raw), "hunter2-secret", "secret values are redacted")
	require.Equal(t, true, page["capturing"])

	// Defaults when nothing is asked for.
	code, _, raw = do(o, http.MethodGet, base, nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Equal(t, agentprotocol.LogQueryDefaultLimit, got.Limit)
	require.Equal(t, agentprotocol.LogOrderDesc, got.Order)
	items = asMap(t, raw)["items"].([]any)
	require.Equal(t, "started", items[1].(map[string]any)["line"], "newest first by default")

	// Bad requests: a backwards range, a query that is too long, an order that is not one, a limit out of range.
	for name, qs := range map[string]string{
		"backwards": url.Values{"from": {t0.Add(time.Hour).Format(time.RFC3339)}, "to": {t0.Format(time.RFC3339)}}.Encode(),
		"too long":  url.Values{"q": {strings.Repeat("a", 2001)}}.Encode(),
		"order":     "order=sideways",
		"limit":     "limit=5000",
	} {
		code, _, raw = do(o, http.MethodGet, base+"?"+qs, nil)
		require.Equal(t, http.StatusUnprocessableEntity, code, "%s: %s", name, raw)
	}

	// What the agent answers is mapped to the right errors.
	cases := []struct {
		frame  agentprotocol.ResultFrame
		status int
		code   string
	}{
		{agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "unsupported command"}, http.StatusConflict, "agent_outdated"},
		{agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "invalid cursor"}, http.StatusBadRequest, "invalid_cursor"},
		{agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "disk error"}, http.StatusBadGateway, "history_unavailable"},
	}
	for _, c := range cases {
		f.agents.handlers[f.srv1.ID] = func(string, json.RawMessage, map[string]string) agentprotocol.ResultFrame { return c.frame }
		code, _, raw = do(o, http.MethodGet, base, nil)
		require.Equal(t, c.status, code, string(raw))
		require.Equal(t, c.code, asMap(t, raw)["code"])
	}
	f.agents.online[f.srv1.ID] = false
	code, _, raw = do(o, http.MethodGet, base, nil)
	require.Equal(t, http.StatusServiceUnavailable, code, string(raw))
	require.Equal(t, "server_offline", asMap(t, raw)["code"])
	require.Equal(t, "edge-1", asMap(t, raw)["details"].(map[string]any)["serverName"])

	// Scope and visibility.
	ro := *o
	ro.Scopes = []string{policy.ScopeProjectsRead}
	code, _, _ = do(&ro, http.MethodGet, base, nil)
	require.Equal(t, http.StatusForbidden, code)
	code, _, _ = do(nil, http.MethodGet, base, nil)
	require.Equal(t, http.StatusUnauthorized, code)
}

func TestEnvironmentDownloadAndCentralEndpoints(t *testing.T) {
	f := newFixture(t)
	do := f.serve()
	o := principalOf(f.ctx)
	t0 := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	f.agents.online[f.srv1.ID] = true
	f.agents.handlers[f.srv1.ID] = func(string, json.RawMessage, map[string]string) agentprotocol.ResultFrame {
		return queryResult(line(1, t0, "from web hunter2-secret"))
	}
	f.agents.handlers[f.srv2.ID] = func(string, json.RawMessage, map[string]string) agentprotocol.ResultFrame {
		return queryResult(line(1, t0.Add(time.Second), "from api"))
	}
	f.agents.online[f.srv2.ID] = false

	// The environment merges the services; the one on the offline server is listed, not fatal.
	code, _, raw := do(o, http.MethodGet, "/environments/"+f.envA.ID.String()+"/logs/history", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	page := asMap(t, raw)
	require.Len(t, page["items"], 1)
	require.Equal(t, "web", page["items"].([]any)[0].(map[string]any)["serviceName"])
	un := page["unavailable"].([]any)
	require.Len(t, un, 1)
	require.Equal(t, "api", un[0].(map[string]any)["serviceName"])
	require.Equal(t, "server_offline", un[0].(map[string]any)["reason"])
	code, _, _ = do(o, http.MethodGet, "/environments/"+f.envA.ID.String()+"/logs/history?order=x", nil)
	require.Equal(t, http.StatusUnprocessableEntity, code)

	// Downloads as text or ndjson, redacted, with a file name.
	code, hdr, raw := do(o, http.MethodGet, "/services/"+f.web.ID.String()+"/logs/download", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Contains(t, hdr.Get("Content-Disposition"), "attachment")
	require.Contains(t, hdr.Get("Content-Disposition"), ".txt")
	require.Contains(t, string(raw), "from web")
	require.NotContains(t, string(raw), "hunter2-secret")
	code, hdr, raw = do(o, http.MethodGet, "/services/"+f.web.ID.String()+"/logs/download?format=ndjson", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Contains(t, hdr.Get("Content-Disposition"), ".ndjson")
	var rec map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &rec), string(raw))
	require.Equal(t, "from web ••••••", rec["line"])
	code, _, _ = do(o, http.MethodGet, "/services/"+f.api.ID.String()+"/logs/download", nil)
	require.Equal(t, http.StatusServiceUnavailable, code, "an offline server has no history to download")
	code, _, _ = do(o, http.MethodGet, "/services/"+f.web.ID.String()+"/logs/download?format=csv", nil)
	require.Equal(t, http.StatusUnprocessableEntity, code)

	// Central logs are off until the built-in drain is on.
	code, _, raw = do(o, http.MethodGet, "/services/"+f.web.ID.String()+"/logs/central?q=boom&limit=10&since=15m", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	central := asMap(t, raw)
	require.Equal(t, false, central["enabled"])
	require.NotEmpty(t, central["hint"])
	code, _, _ = do(o, http.MethodGet, "/services/"+f.web.ID.String()+"/logs/central?since=15m&from="+url.QueryEscape(t0.Format(time.RFC3339)), nil)
	require.Equal(t, http.StatusUnprocessableEntity, code, "since and from do not go together")
}
