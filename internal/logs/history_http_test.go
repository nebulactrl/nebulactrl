package logs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
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

func TestParseSources(t *testing.T) {
	for in, want := range map[string][]string{
		"":                  {"runtime"},
		"runtime":           {"runtime"},
		"requests":          {"requests"},
		"runtime,requests":  {"runtime", "requests"},
		"requests, runtime": {"runtime", "requests"},
		"requests,requests": {"requests"},
	} {
		got, err := parseSources(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, in := range []string{"metrics", "runtime,", ",requests", "runtime,all", "Runtime"} {
		_, err := parseSources(in)
		var ae *apierr.Error
		require.ErrorAs(t, err, &ae, in)
		require.Equal(t, http.StatusUnprocessableEntity, ae.Status, in)
	}
}

func TestAgentQueryNarrowsRuntimeByPrefixAndRequestsByAttribute(t *testing.T) {
	p := HistoryParams{Process: "web"}
	require.Equal(t, `"web | "`, p.agentQuery(agentprotocol.LogSourceRuntime))
	require.Equal(t, `@process:"web"`, p.agentQuery(agentprotocol.LogSourceRequests))
	require.Empty(t, p.prefixOf(agentprotocol.LogSourceRequests), "request lines carry no prefix")
	require.Equal(t, "web | ", p.prefixOf(agentprotocol.LogSourceRuntime))
	require.Equal(t, `@process:"we\"b\\x"`, HistoryParams{Process: `we"b\x`}.agentQuery(agentprotocol.LogSourceRequests), "quotes and backslashes are escaped")

	p = HistoryParams{Component: "web", Query: "status:5*"}
	require.Equal(t, `(status:5*) "web | "`, p.agentQuery(agentprotocol.LogSourceRuntime))
	require.Equal(t, `(status:5*) @component:"web"`, p.agentQuery(agentprotocol.LogSourceRequests))

	p = HistoryParams{Query: "  "}
	require.Equal(t, "  ", p.agentQuery(agentprotocol.LogSourceRequests))
}

func TestHistorySourcesSelectTheStoreAndTheStream(t *testing.T) {
	f := newFixture(t)
	do := f.serve()
	o := principalOf(f.ctx)
	t0 := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	f.agents.online[f.srv1.ID] = true
	var mu sync.Mutex
	var asked []agentprotocol.LogsQuery
	f.agents.handlers[f.srv1.ID] = func(_ string, payload json.RawMessage, _ map[string]string) agentprotocol.ResultFrame {
		var q agentprotocol.LogsQuery
		require.NoError(t, json.Unmarshal(payload, &q))
		mu.Lock()
		asked = append(asked, q)
		mu.Unlock()
		if q.Source == agentprotocol.LogSourceRequests {
			return queryResult(line(2, t0.Add(2*time.Second), `{"method":"GET","path":"/","status":200}`), line(1, t0, `{"method":"POST","path":"/x","status":500}`))
		}
		return queryResult(line(1, t0.Add(time.Second), "started"))
	}
	reset := func() []agentprotocol.LogsQuery {
		mu.Lock()
		defer mu.Unlock()
		got := asked
		asked = nil
		return got
	}
	streams := func(raw []byte) []string {
		var out []string
		for _, it := range asMap(t, raw)["items"].([]any) {
			m := it.(map[string]any)
			out = append(out, m["stream"].(string))
		}
		return out
	}
	for _, base := range []string{"/services/" + f.web.ID.String() + "/logs/history", "/environments/" + f.envA.ID.String() + "/logs/history"} {
		// Default: the runtime store only.
		code, _, raw := do(o, http.MethodGet, base, nil)
		require.Equal(t, http.StatusOK, code, string(raw))
		got := reset()
		require.NotEmpty(t, got)
		for _, q := range got {
			require.Equal(t, agentprotocol.LogSourceRuntime, q.Source)
		}
		require.Equal(t, []string{"runtime"}, streams(raw))

		// Requests only: the request store, and the lines say so.
		code, _, raw = do(o, http.MethodGet, base+"?sources=requests", nil)
		require.Equal(t, http.StatusOK, code, string(raw))
		for _, q := range reset() {
			require.Equal(t, agentprotocol.LogSourceRequests, q.Source)
		}
		require.Equal(t, []string{"requests", "requests"}, streams(raw))

		// Both: merged by time, newest first.
		code, _, raw = do(o, http.MethodGet, base+"?sources=runtime,requests", nil)
		require.Equal(t, http.StatusOK, code, string(raw))
		sources := map[string]int{}
		for _, q := range reset() {
			sources[q.Source]++
		}
		require.Equal(t, 1, sources["runtime"])
		require.Equal(t, 1, sources["requests"])
		require.Equal(t, []string{"requests", "runtime", "requests"}, streams(raw))

		code, _, raw = do(o, http.MethodGet, base+"?sources=metrics", nil)
		require.Equal(t, http.StatusUnprocessableEntity, code, string(raw))
		require.Empty(t, reset(), "an invalid source asks no agent")
	}
}

func TestEnvironmentHistoryCursorCoversEverySourceOfEveryService(t *testing.T) {
	f := newFixture(t)
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	f.agents.online[f.srv1.ID] = true
	f.agents.online[f.srv2.ID] = true
	// web: runtime 1,5,9 requests 2,6,10; api: runtime 3,7,11 requests 4,8,12 (seconds; the sequence number is the second).
	all := map[string][]agentprotocol.HistoryLine{}
	for sec := 1; sec <= 12; sec++ {
		name := "web"
		if (sec-1)%4 >= 2 {
			name = "api"
		}
		source := agentprotocol.LogSourceRuntime
		if sec%2 == 0 {
			source = agentprotocol.LogSourceRequests
		}
		key := name + "/" + source
		all[key] = append([]agentprotocol.HistoryLine{line(uint64(sec), t0.Add(time.Duration(sec)*time.Second), fmt.Sprintf("%s-%d", key, sec))}, all[key]...)
	}
	require.Len(t, all, 4)
	serve := func(name string) func(string, json.RawMessage, map[string]string) agentprotocol.ResultFrame {
		return func(_ string, payload json.RawMessage, _ map[string]string) agentprotocol.ResultFrame {
			var q agentprotocol.LogsQuery
			require.NoError(t, json.Unmarshal(payload, &q))
			mine := all[name+"/"+q.Source]
			var page []agentprotocol.HistoryLine
			for _, l := range mine { // seq descending
				if q.Cursor != "" {
					var c uint64
					_, _ = fmt.Sscan(q.Cursor, &c)
					if l.Seq >= c {
						continue
					}
				}
				page = append(page, l)
				if len(page) == q.Limit {
					break
				}
			}
			raw, _ := json.Marshal(agentprotocol.LogsQueryResult{Lines: page, Capturing: true, NextCursor: nextCursor(mine, page)})
			return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: raw}
		}
	}
	f.agents.handlers[f.srv1.ID] = serve("web")
	f.agents.handlers[f.srv2.ID] = serve("api")

	var got []string
	cursor := ""
	for range 20 {
		page, err := f.svc.EnvironmentHistory(f.ctx, f.envA.ID, HistoryParams{Limit: 5, Cursor: cursor, Sources: "runtime,requests"})
		require.NoError(t, err)
		for _, it := range page.Items {
			got = append(got, it.Line)
			source := strings.SplitN(strings.SplitN(it.Line, "/", 2)[1], "-", 2)[0]
			require.Equal(t, source, it.Stream, it.Line)
		}
		if page.NextCursor == "" {
			break
		}
		cur, err := decodeEnvCursor(page.NextCursor)
		require.NoError(t, err)
		for key := range cur.C {
			require.Contains(t, []string{
				cursorKey(f.web.ID, "runtime"), cursorKey(f.web.ID, "requests"), cursorKey(f.api.ID, "runtime"), cursorKey(f.api.ID, "requests"),
			}, key)
		}
		cursor = page.NextCursor
	}
	var want []string
	for sec := 12; sec >= 1; sec-- {
		for key, lines := range all {
			for _, l := range lines {
				if l.Seq == uint64(sec) {
					want = append(want, fmt.Sprintf("%s-%d", key, sec))
				}
			}
		}
	}
	require.Equal(t, want, got, "every line of every pair once, newest first")

	// A cursor from before sources existed named bare service IDs: they are the runtime pairs.
	old := base64.RawURLEncoding.EncodeToString([]byte(`{"c":{"` + f.web.ID.String() + `":"3"},"d":["` + f.api.ID.String() + `"]}`))
	cur, err := decodeEnvCursor(old)
	require.NoError(t, err)
	require.Equal(t, map[string]string{cursorKey(f.web.ID, "runtime"): "3"}, cur.C)
	require.Equal(t, []string{cursorKey(f.api.ID, "runtime")}, cur.Done)
	// A current cursor is left as it is, and survives a round trip.
	cur, err = decodeEnvCursor(envCursor{C: map[string]string{cursorKey(f.web.ID, "requests"): "7"}, Done: []string{cursorKey(f.api.ID, "runtime")}}.encode())
	require.NoError(t, err)
	require.Equal(t, map[string]string{cursorKey(f.web.ID, "requests"): "7"}, cur.C)
	require.Equal(t, []string{cursorKey(f.api.ID, "runtime")}, cur.Done)

	// Continuing with it asks web's runtime store from its cursor and never asks api again.
	f.agents.asked = nil
	var gotCursor string
	f.agents.handlers[f.srv1.ID] = func(_ string, payload json.RawMessage, _ map[string]string) agentprotocol.ResultFrame {
		var q agentprotocol.LogsQuery
		require.NoError(t, json.Unmarshal(payload, &q))
		require.Equal(t, agentprotocol.LogSourceRuntime, q.Source)
		gotCursor = q.Cursor
		return queryResult()
	}
	_, err = f.svc.EnvironmentHistory(f.ctx, f.envA.ID, HistoryParams{Cursor: old})
	require.NoError(t, err)
	require.Equal(t, "3", gotCursor)
	require.Len(t, f.agents.asked, 1)
	require.Equal(t, f.srv1.ID, f.agents.asked[0].server)

	// What is not a cursor is still refused.
	_, err = f.svc.EnvironmentHistory(f.ctx, f.envA.ID, HistoryParams{Cursor: "not a cursor!"})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "invalid_cursor", ae.Code)
}

// setAgentFacts gives a server the facts of an agent that advertises caps.
func (f *fixture) setAgentFacts(id uuid.UUID, caps ...string) {
	f.t.Helper()
	raw, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Arch: "amd64", Capabilities: caps})
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET facts = $2 WHERE id = $1", id, raw)
	require.NoError(f.t, err)
}

func TestOldAgentsAreNotAskedForRequestLogs(t *testing.T) {
	f := newFixture(t)
	do := f.serve()
	o := principalOf(f.ctx)
	t0 := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	f.agents.online[f.srv1.ID] = true
	f.agents.online[f.srv2.ID] = true
	answer := func(kind string, _ json.RawMessage, _ map[string]string) agentprotocol.ResultFrame {
		if kind == agentprotocol.KindLogsFacets {
			return facetsFrame(agentprotocol.LogsFacetsResult{Matched: 2, Capturing: true})
		}
		return queryResult(line(1, t0, "a line"))
	}
	f.agents.handlers[f.srv1.ID] = answer // web: an agent from before request logs
	f.agents.handlers[f.srv2.ID] = answer // api: a current one
	f.setAgentFacts(f.srv1.ID, agentprotocol.CapProcesses)
	f.setAgentFacts(f.srv2.ID, agentprotocol.CapProcesses, agentprotocol.CapRequestLogs)

	askedFor := func() map[string]int { // "<server>/<source>" → questions
		out := map[string]int{}
		for _, a := range f.agents.asked {
			name := "web"
			if a.server == f.srv2.ID {
				name = "api"
			}
			var q struct {
				Source string `json:"source"`
			}
			require.NoError(t, json.Unmarshal(a.payload, &q))
			out[name+"/"+q.Source]++
		}
		f.agents.asked = nil
		return out
	}
	outdated := func(page map[string]any) {
		un := page["unavailable"].([]any)
		require.Len(t, un, 1)
		require.Equal(t, "web", un[0].(map[string]any)["serviceName"])
		require.Equal(t, "agent_outdated", un[0].(map[string]any)["reason"])
		require.Equal(t, "edge-1", un[0].(map[string]any)["serverName"])
	}
	env := "/environments/" + f.envA.ID.String() + "/logs/"
	svc := "/services/" + f.web.ID.String() + "/logs/"

	// Environment history: web's requests are not asked, its runtime is; api answers both.
	code, _, raw := do(o, http.MethodGet, env+"history?sources=runtime,requests", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	page := asMap(t, raw)
	outdated(page)
	require.Equal(t, map[string]int{"web/runtime": 1, "api/runtime": 1, "api/requests": 1}, askedFor())
	require.Len(t, page["items"], 3, "web's runtime line and both of api's")

	// Environment facets: the same.
	code, _, raw = do(o, http.MethodGet, env+"facets?sources=runtime,requests", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	page = asMap(t, raw)
	outdated(page)
	require.Equal(t, map[string]int{"web/runtime": 1, "api/runtime": 1, "api/requests": 1}, askedFor())
	require.EqualValues(t, 6, page["matched"])

	// One service: its requests are a 409 without asking the agent; its runtime is fine.
	for _, kind := range []string{"history", "facets"} {
		code, _, raw = do(o, http.MethodGet, svc+kind+"?sources=requests", nil)
		require.Equal(t, http.StatusConflict, code, kind+": "+string(raw))
		require.Equal(t, "agent_outdated", asMap(t, raw)["code"], kind)
		code, _, raw = do(o, http.MethodGet, svc+kind+"?sources=runtime,requests", nil)
		require.Equal(t, http.StatusConflict, code, kind+": "+string(raw))
		require.Equal(t, map[string]int{"web/runtime": 1}, askedFor(), kind+": the runtime source may be asked, the requests never")
		code, _, raw = do(o, http.MethodGet, svc+kind, nil)
		require.Equal(t, http.StatusOK, code, kind+": "+string(raw))
		require.Equal(t, map[string]int{"web/runtime": 1}, askedFor(), kind)
	}

	// An agent that never reported facts counts as current.
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET facts = NULL WHERE id = $1", f.srv1.ID)
	require.NoError(t, err)
	code, _, raw = do(o, http.MethodGet, svc+"history?sources=requests", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Equal(t, map[string]int{"web/requests": 1}, askedFor())
}

func TestRequestsCapturingIsFalseWhenEveryAgentHasRequestLogsOff(t *testing.T) {
	f := newFixture(t)
	do := f.serve()
	o := principalOf(f.ctx)
	f.agents.online[f.srv1.ID] = true
	f.agents.online[f.srv2.ID] = true
	serve := func(requests bool) func(string, json.RawMessage, map[string]string) agentprotocol.ResultFrame {
		return func(_ string, payload json.RawMessage, _ map[string]string) agentprotocol.ResultFrame {
			var q agentprotocol.LogsFacetsQuery
			require.NoError(t, json.Unmarshal(payload, &q))
			return facetsFrame(agentprotocol.LogsFacetsResult{Capturing: q.Source == agentprotocol.LogSourceRuntime || requests})
		}
	}
	f.agents.handlers[f.srv1.ID] = serve(false)
	f.agents.handlers[f.srv2.ID] = serve(false)
	env := "/environments/" + f.envA.ID.String() + "/logs/facets?sources=runtime,requests"

	code, _, raw := do(o, http.MethodGet, env, nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	page := asMap(t, raw)
	require.Equal(t, true, page["capturing"], "the runtime store is on")
	require.Equal(t, false, page["requestsCapturing"], "request logs are off on every server")

	f.agents.handlers[f.srv2.ID] = serve(true)
	code, _, raw = do(o, http.MethodGet, env, nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Equal(t, true, asMap(t, raw)["requestsCapturing"], "one server stores them")

	// A server that cannot be asked says nothing: the answer is not "on".
	f.agents.online[f.srv2.ID] = false
	f.agents.handlers[f.srv1.ID] = serve(false)
	code, _, raw = do(o, http.MethodGet, env, nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Equal(t, false, asMap(t, raw)["requestsCapturing"])
}
