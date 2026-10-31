//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// logReceiver is an HTTP JSON intake: it records every record a drain delivers.
type logReceiver struct {
	mu      sync.Mutex
	records []map[string]any
	auth    []string
	fail    int // answer 503 this many times first
}

func (r *logReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail > 0 {
		r.fail--
		http.Error(w, "try later", http.StatusServiceUnavailable)
		return
	}
	var batch []map[string]any
	if err := json.Unmarshal(body, &batch); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	r.records = append(r.records, batch...)
	r.auth = append(r.auth, req.Header.Get("X-Intake-Key"))
}

func (r *logReceiver) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, rec := range r.records {
		out = append(out, fmt.Sprint(rec["message"]))
	}
	return out
}

func (r *logReceiver) has(substr string) (map[string]any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.records {
		if strings.Contains(fmt.Sprint(rec["message"]), substr) {
			return rec, true
		}
	}
	return nil, false
}

// historyLines returns the lines of a history query, newest first.
func (s *Stack) historyLines(serviceID string, query url.Values) []string {
	s.t.Helper()
	r := s.Do("GET", "/services/"+serviceID+"/logs/history?"+query.Encode(), nil)
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
	var out []string
	for _, it := range r.JSON().Items() {
		out = append(out, it.Str("line"))
	}
	return out
}

func containsAll(lines []string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for _, l := range lines {
			if strings.Contains(l, sub) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Log history and drains: the agent stores every managed container's output
// whether or not anyone watches; the history API reads it back (time range,
// the search language, redaction); a drain ships the lines from the agent to an
// HTTP JSON intake, retrying a failing destination without losing a line.
func TestLogHistoryAndDrain(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	app := appFixture(t)
	s := SharedStack(t)
	svc := s.NewService(SvcSpec{
		Name: "web", Image: app.AppImage, Domain: s.Host(),
		Vars:    map[string]string{"VERSION": "v1", "SECRET_ECHO": "HIST_KEY"},
		Secrets: map[string]string{"HIST_KEY": "hist-canary-98765"},
	})
	s.DeployOK(svc.ID)
	pc := NewProxyClient(s.Host())
	pc.WaitBody(t, "/", "v1", 20*time.Second)
	logLines := func(msgs ...string) {
		t.Helper()
		q := url.Values{}
		for _, m := range msgs {
			q.Add("msg", m)
		}
		status, body, err := pc.Get("/log?" + q.Encode())
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, body)
	}

	// A drain to an HTTP JSON intake, scoped to this project. It fails twice first
	// (503): lines must be retried, not lost. Wait until the agent reports it: from
	// then on it ships what is written.
	rcv := &logReceiver{fail: 2}
	intake := httptest.NewServer(rcv)
	t.Cleanup(intake.Close)
	r := s.Do("POST", "/log-drains", map[string]any{
		"name": "intake-" + svc.Slug, "kind": "http", "allProjects": false, "projectIds": []string{s.ProjectID},
		"config":  map[string]any{"http": map[string]any{"url": intake.URL, "extraFields": map[string]string{"ddsource": "nebulactrl"}}},
		"secrets": map[string]any{"headers": map[string]string{"X-Intake-Key": "intake-canary-13579"}},
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	drainID := r.JSON().Str("id")
	require.NotContains(t, string(r.Raw), "intake-canary-13579", "credentials are never returned")
	t.Cleanup(func() { s.Do("DELETE", "/log-drains/"+drainID, nil) })
	require.True(t, eventually(30*time.Second, 500*time.Millisecond, func() bool {
		d := s.Do("GET", "/log-drains/"+drainID, nil)
		return d.Status == http.StatusOK && len(d.JSON()["servers"].([]any)) > 0
	}), "the agent never reported the drain")

	// The live view is opened and closed: capture does not depend on it.
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", s.Srv.URL+"/api/v1/services/"+svc.ID+"/logs?follow=true&tail=5", nil)
	require.NoError(t, err)
	res, err := s.client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	logLines("level=info history-canary live one")
	buf := make([]byte, 512)
	_, _ = res.Body.Read(buf) // the viewer sees something...
	cancel()                  // ...and goes away
	_ = res.Body.Close()

	logLines(
		"level=error history-canary disk full on /data",
		"level=info history-canary request served",
		`{"level":"warn","msg":"history-canary slow query","ms":1500}`,
		"history-canary token="+"hist-canary-98765",
	)

	// History has everything, including what was written while no one watched.
	var all []string
	require.True(t, eventually(30*time.Second, 500*time.Millisecond, func() bool {
		all = s.historyLines(svc.ID, url.Values{"limit": {"500"}})
		return containsAll(all, "live one", "disk full", "request served", "slow query")
	}), "history is missing lines: %v", all)
	require.True(t, containsAll(all, "listening on"), "output from before the first request is there too: %v", all)
	for _, l := range all {
		require.NotContains(t, l, "hist-canary-98765", "secret values are redacted from history")
	}
	require.True(t, containsAll(all, "token=••••••"), "the redaction marker replaces the secret: %v", all)

	// The query language runs on the agent.
	got := s.historyLines(svc.ID, url.Values{"q": {"history-canary level:error"}})
	require.Len(t, got, 1, "%v", got)
	require.Contains(t, got[0], "disk full")
	got = s.historyLines(svc.ID, url.Values{"q": {"@ms:>1000"}})
	require.Len(t, got, 1, "%v", got)
	require.Contains(t, got[0], "slow query")
	got = s.historyLines(svc.ID, url.Values{"q": {`"history-canary" -level:error -level:warn`}})
	require.True(t, containsAll(got, "live one", "request served") && !containsAll(got, "disk full"), "%v", got)

	// Time range: nothing before the service existed, everything up to now.
	past := time.Now().Add(-48 * time.Hour)
	got = s.historyLines(svc.ID, url.Values{"from": {past.Format(time.RFC3339)}, "to": {past.Add(time.Hour).Format(time.RFC3339)}})
	require.Empty(t, got)
	got = s.historyLines(svc.ID, url.Values{"from": {time.Now().Add(-time.Hour).Format(time.RFC3339)}, "q": {"history-canary"}})
	require.GreaterOrEqual(t, len(got), 5)

	// Paging: two lines at a time, newest first, no repeats.
	page1 := s.Do("GET", "/services/"+svc.ID+"/logs/history?limit=2&q=history-canary", nil)
	require.Equal(t, http.StatusOK, page1.Status, string(page1.Raw))
	require.Len(t, page1.JSON().Items(), 2)
	next := page1.JSON().Str("nextCursor")
	require.NotEmpty(t, next)
	page2 := s.Do("GET", "/services/"+svc.ID+"/logs/history?limit=2&q=history-canary&cursor="+url.QueryEscape(next), nil)
	require.Equal(t, http.StatusOK, page2.Status, string(page2.Raw))
	seen := map[string]bool{}
	for _, it := range append(page1.JSON().Items(), page2.JSON().Items()...) {
		require.False(t, seen[it.Str("line")], "repeated line %q", it.Str("line"))
		seen[it.Str("line")] = true
	}

	// The environment view merges services; this environment has this one.
	er := s.Do("GET", "/environments/"+s.EnvID+"/logs/history?q=history-canary", nil)
	require.Equal(t, http.StatusOK, er.Status, string(er.Raw))
	require.NotEmpty(t, er.JSON().Items())
	require.Equal(t, "web", er.JSON().Items()[0].Str("serviceName"))

	// The drain delivered them despite the first two failures, labelled and with its header.
	require.True(t, eventually(60*time.Second, 500*time.Millisecond, func() bool {
		_, a := rcv.has("disk full")
		_, b := rcv.has("slow query")
		_, c := rcv.has("request served")
		return a && b && c
	}), "the intake got %v", rcv.messages())
	rec, _ := rcv.has("disk full")
	require.Equal(t, "web", rec["service"])
	require.Equal(t, "error", rec["level"])
	require.Equal(t, "nebulactrl", rec["ddsource"])
	require.NotEmpty(t, rec["timestamp"])
	require.Equal(t, "intake-canary-13579", rcv.auth[0], "the header credential reached the intake")
	for _, m := range rcv.messages() {
		require.NotContains(t, m, "hist-canary-98765", "secrets never leave the host: drains ship redacted lines or none")
	}
	_, started := rcv.has("listening on")
	require.False(t, started, "a drain ships what is written after it was created, not the history")

	// The agent's report shows the drain delivering.
	require.True(t, eventually(60*time.Second, time.Second, func() bool {
		d := s.Do("GET", "/log-drains/"+drainID, nil).JSON()
		srv := d["servers"].([]any)
		if len(srv) == 0 {
			return false
		}
		st := srv[0].(map[string]any)
		return st["state"] == "ok" && st["sentLines"].(float64) >= 3
	}), "drain status never showed delivery")

	// Test-send works from the agent's host.
	tr := s.Do("POST", "/log-drains/"+drainID+"/test", map[string]any{})
	require.Equal(t, http.StatusOK, tr.Status, string(tr.Raw))
	require.True(t, tr.JSON().Bool("ok"), string(tr.Raw))
	require.True(t, eventually(10*time.Second, 200*time.Millisecond, func() bool { _, ok := rcv.has("NebulaCtrl log drain test"); return ok }), "%v", rcv.messages())

	// Secrets in the database are sealed.
	var enc string
	require.NoError(t, s.Pool.QueryRow(context.Background(), `SELECT secrets_enc::text FROM log_drains WHERE id = $1`, drainID).Scan(&enc))
	require.NotContains(t, enc, "intake-canary-13579")
}
