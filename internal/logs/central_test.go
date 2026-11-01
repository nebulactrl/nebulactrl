package logs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeCentral records the queries and answers with canned streams.
type fakeCentral struct {
	streams []CentralStream
	err     error

	calls  int
	tenant string
	logql  string
	from   time.Time
	to     time.Time
	limit  int
}

func (c *fakeCentral) QueryRange(_ context.Context, tenant, logql string, from, to time.Time, limit int) ([]CentralStream, error) {
	c.calls++
	c.tenant, c.logql, c.from, c.to, c.limit = tenant, logql, from, to, limit
	return c.streams, c.err
}

// builtinDrain inserts the team's built-in drain the way the instance services do:
// disabled, pointing at the agent's local Loki endpoint, with no credentials.
func (f *fixture) builtinDrain() db.LogDrain {
	f.t.Helper()
	cfg, err := json.Marshal(map[string]any{"loki": agentprotocol.LokiDrainConfig{URL: "http://127.0.0.1:18100/loki/api/v1/push"}})
	require.NoError(f.t, err)
	d, err := f.q.InsertBuiltinDrain(context.Background(), db.InsertBuiltinDrainParams{WorkspaceID: f.ws.ID, Name: "NebulaCtrl logs", Config: cfg})
	require.NoError(f.t, err)
	return d
}

func requireStatus(t *testing.T, err error, status int, code string) {
	t.Helper()
	var ae *apierr.Error
	require.True(t, errors.As(err, &ae), "want an API error, got %v", err)
	require.Equal(t, status, ae.Status, ae.Message)
	if code != "" {
		require.Equal(t, code, ae.Code)
	}
}

func TestBuiltinDrainIsProtectedButSwitchable(t *testing.T) {
	f := newFixture(t)
	d := f.builtinDrain()
	require.False(t, d.Enabled)

	v, err := f.svc.GetDrain(f.ctx, d.ID)
	require.NoError(t, err)
	require.True(t, v.Builtin)
	require.False(t, v.Enabled)
	require.Empty(t, v.Secrets)
	require.Empty(t, v.HeaderNames)
	list, err := f.svc.ListDrains(f.ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.True(t, list[0].Builtin)

	// Deleting is refused (409), and the row stays.
	requireStatus(t, f.svc.DeleteDrain(f.ctx, d.ID), http.StatusConflict, "builtin_drain")
	// Renaming, other settings and credentials are refused.
	other := "mine"
	requireStatus(t, func() error { _, err := f.svc.UpdateDrain(f.ctx, d.ID, DrainPatch{Name: &other}); return err }(), http.StatusConflict, "builtin_drain")
	elsewhere := DrainConfig{Loki: &agentprotocol.LokiDrainConfig{URL: "https://loki.example.com/loki/api/v1/push"}}
	requireStatus(t, func() error { _, err := f.svc.UpdateDrain(f.ctx, d.ID, DrainPatch{Config: &elsewhere}); return err }(), http.StatusConflict, "builtin_drain")
	tenant := DrainConfig{Loki: &agentprotocol.LokiDrainConfig{URL: "http://127.0.0.1:18100/loki/api/v1/push", TenantID: "x"}}
	requireStatus(t, func() error { _, err := f.svc.UpdateDrain(f.ctx, d.ID, DrainPatch{Config: &tenant}); return err }(), http.StatusConflict, "builtin_drain")
	requireStatus(t, func() error {
		_, err := f.svc.UpdateDrain(f.ctx, d.ID, DrainPatch{Secrets: &LogDrainSecretsInput{Token: "t"}})
		return err
	}(), http.StatusConflict, "builtin_drain")
	requireStatus(t, func() error {
		_, err := f.svc.UpdateDrain(f.ctx, d.ID, DrainPatch{Secrets: &LogDrainSecretsInput{Clear: []string{"token"}}})
		return err
	}(), http.StatusConflict, "builtin_drain")
	got, err := f.svc.GetDrain(f.ctx, d.ID)
	require.NoError(t, err)
	require.Equal(t, d.Name, got.Name)
	require.Equal(t, "http://127.0.0.1:18100/loki/api/v1/push", got.Loki.URL)
	require.False(t, got.Enabled)

	// A form that submits the values it already has, and turns the drain on, works.
	same := d.Name
	have := DrainConfig{Loki: &agentprotocol.LokiDrainConfig{URL: "http://127.0.0.1:18100/loki/api/v1/push"}}
	on := true
	got, err = f.svc.UpdateDrain(f.ctx, d.ID, DrainPatch{Name: &same, Config: &have, Secrets: &LogDrainSecretsInput{}, Enabled: &on})
	require.NoError(t, err)
	require.True(t, got.Enabled)
	require.True(t, got.Builtin)

	// Agents get it with no credentials at all: the control plane sets the tenant.
	cfg, secrets, err := f.svc.LogsConfigFor(f.ctx, f.srv1.ID)
	require.NoError(t, err)
	require.Len(t, cfg.Drains, 1)
	require.True(t, cfg.Drains[0].Enabled)
	require.Equal(t, "http://127.0.0.1:18100/loki/api/v1/push", cfg.Drains[0].Loki.URL)
	require.Empty(t, cfg.Drains[0].Loki.TenantID)
	require.Empty(t, secrets)

	// It is tested like any drain: from a connected server's agent.
	f.agents.online[f.srv1.ID] = true
	f.agents.handlers[f.srv1.ID] = func(kind string, payload json.RawMessage, secrets map[string]string) agentprotocol.ResultFrame {
		require.Equal(t, agentprotocol.KindLogsDrainTest, kind)
		require.Contains(t, string(payload), "127.0.0.1:18100")
		require.Empty(t, secrets)
		raw, _ := json.Marshal(agentprotocol.LogsDrainTestResult{OK: true, Message: "delivered", StatusCode: 204})
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: raw}
	}
	res, err := f.svc.TestDrain(f.ctx, d.ID, nil)
	require.NoError(t, err)
	require.True(t, res.OK)

	// ...and off again.
	off := false
	got, err = f.svc.UpdateDrain(f.ctx, d.ID, DrainPatch{Enabled: &off})
	require.NoError(t, err)
	require.False(t, got.Enabled)

	// Other drains are unaffected.
	v2, err := f.svc.CreateDrain(f.ctx, lokiDrain("mine"))
	require.NoError(t, err)
	require.False(t, v2.Builtin)
	require.NoError(t, f.svc.DeleteDrain(f.ctx, v2.ID))
}

func TestLogQLIsBuiltAndEscaped(t *testing.T) {
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	require.Equal(t, `{service_id="11111111-2222-3333-4444-555555555555"}`, centralLogQL(id, "", ""))
	for _, c := range []struct{ q, want string }{
		{`error`, `|= "error"`},
		{`say "hi"`, `|= "say \"hi\""`},
		{`back\slash`, `|= "back\\slash"`},
		{"two\nlines\ttab", `|= "two\nlines\ttab"`},
		{"`tick`", "|= \"`tick`\""},
		{`"} |~ ".*" or {job="x"`, `|= "\"} |~ \".*\" or {job=\"x\""`},
		{"bad\xffbyte", "|= \"bad\uFFFDbyte\""},
	} {
		got := centralLogQL(id, c.q, "")
		require.Equal(t, `{service_id="11111111-2222-3333-4444-555555555555"} `+c.want, got, c.q)
	}
	// A process narrows by the prefix its lines carry, after the user's text.
	require.Equal(t, `{service_id="11111111-2222-3333-4444-555555555555"} |= "worker | "`, centralLogQL(id, "", "worker"))
	require.Equal(t, `{service_id="11111111-2222-3333-4444-555555555555"} |= "boom" |= "worker | "`, centralLogQL(id, "boom", "worker"))
}

func TestCentralLinesKeepOnlyTheProcessPrefix(t *testing.T) {
	at := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	streams := []CentralStream{{Labels: map[string]string{"server": "s1", "stream": "stdout"}, Entries: []CentralEntry{
		{Time: at, Line: "worker | job 1"},
		{Time: at.Add(time.Second), Line: "web | GET / 200"},
		{Time: at.Add(2 * time.Second), Line: "log of worker | not a prefix"},
	}}}
	id := func(s string) string { return s }
	got := centralLines(streams, id, 10, "worker | ")
	require.Len(t, got, 1)
	require.Equal(t, "worker | job 1", got[0].Line)
	require.Len(t, centralLines(streams, id, 10, ""), 3)
}

func TestCentralWindowClampsAndValidates(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	win := func(p CentralParams) (time.Time, time.Time, error) { return centralWindow(p, now) }

	from, to, err := win(CentralParams{})
	require.NoError(t, err)
	require.Equal(t, now, to)
	require.Equal(t, now.Add(-time.Hour), from, "default: the last hour")

	from, to, err = win(CentralParams{Since: "15m"})
	require.NoError(t, err)
	require.Equal(t, now, to)
	require.Equal(t, now.Add(-15*time.Minute), from)

	from, _, err = win(CentralParams{Since: "3d"})
	require.NoError(t, err)
	require.Equal(t, now.Add(-72*time.Hour), from)

	from, _, err = win(CentralParams{Since: "2026-03-10T11:00:00Z"})
	require.NoError(t, err)
	require.Equal(t, now.Add(-time.Hour), from)

	// Longer than a week: the newest week is kept.
	from, to, err = win(CentralParams{Since: "30d"})
	require.NoError(t, err)
	require.Equal(t, now.Add(-CentralMaxWindow), from)
	require.Equal(t, now, to)
	from, to, err = win(CentralParams{From: now.Add(-100 * 24 * time.Hour), To: now.Add(-time.Hour)})
	require.NoError(t, err)
	require.Equal(t, to.Add(-CentralMaxWindow), from)
	require.Equal(t, now.Add(-time.Hour), to)

	// The end never lies in the future.
	_, to, err = win(CentralParams{To: now.Add(time.Hour)})
	require.NoError(t, err)
	require.Equal(t, now, to)

	for _, p := range []CentralParams{
		{From: now, To: now.Add(-time.Minute)},
		{From: now.Add(time.Hour)},
		{Since: "soon"},
		{Since: "-5m"},
		{Since: "0d"},
		{Since: "1h", From: now.Add(-time.Hour)},
	} {
		_, _, err := win(p)
		requireStatus(t, err, http.StatusUnprocessableEntity, "validation_failed")
	}

	require.Equal(t, CentralDefaultLimit, centralLimit(0))
	require.Equal(t, CentralDefaultLimit, centralLimit(-3))
	require.Equal(t, 5, centralLimit(5))
	require.Equal(t, CentralMaxLimit, centralLimit(50000))
}

func TestCentralLogsAreOffUntilTheBuiltinDrainIsOn(t *testing.T) {
	f := newFixture(t)
	fc := &fakeCentral{}

	// Loki is not wired: off.
	page, err := f.svc.ServiceCentralLogs(f.ctx, f.web.ID, CentralParams{})
	require.NoError(t, err)
	require.False(t, page.Enabled)
	require.Equal(t, CentralHint, page.Hint)
	require.Equal(t, "Central logs are off. Turn on Log forwarding in Team settings to collect logs here.", page.Hint)
	require.NotNil(t, page.Lines)
	require.Empty(t, page.Lines)
	raw, _ := json.Marshal(page)
	require.Contains(t, string(raw), `"lines":[]`)

	f.svc.SetCentralLogs(fc)
	// No builtin drain row: off.
	page, err = f.svc.ServiceCentralLogs(f.ctx, f.web.ID, CentralParams{})
	require.NoError(t, err)
	require.False(t, page.Enabled)
	require.Equal(t, CentralHint, page.Hint)

	// A disabled one: off, and Loki is not asked.
	d := f.builtinDrain()
	page, err = f.svc.ServiceCentralLogs(f.ctx, f.web.ID, CentralParams{})
	require.NoError(t, err)
	require.False(t, page.Enabled)
	require.Empty(t, page.Lines)
	require.Zero(t, fc.calls)

	// A user's own Loki drain does not turn it on.
	_, err = f.svc.CreateDrain(f.ctx, lokiDrain("mine"))
	require.NoError(t, err)
	page, err = f.svc.ServiceCentralLogs(f.ctx, f.web.ID, CentralParams{})
	require.NoError(t, err)
	require.False(t, page.Enabled)

	// Turned on: it queries.
	on := true
	_, err = f.svc.UpdateDrain(f.ctx, d.ID, DrainPatch{Enabled: &on})
	require.NoError(t, err)
	page, err = f.svc.ServiceCentralLogs(f.ctx, f.web.ID, CentralParams{})
	require.NoError(t, err)
	require.True(t, page.Enabled)
	require.Empty(t, page.Hint)
	require.Equal(t, 1, fc.calls)
}

func TestCentralLogsQueryTenantMergeRedactAndLimit(t *testing.T) {
	f := newFixture(t)
	d := f.builtinDrain()
	on := true
	_, err := f.svc.UpdateDrain(f.ctx, d.ID, DrainPatch{Enabled: &on})
	require.NoError(t, err)

	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	f.svc.now = func() time.Time { return now }
	t0 := now.Add(-10 * time.Minute)
	fc := &fakeCentral{streams: []CentralStream{
		{Labels: map[string]string{"server": "edge-1", "stream": "stdout", "service_id": f.web.ID.String()}, Entries: []CentralEntry{
			{Time: t0.Add(3 * time.Second), Line: "token=hunter2-secret failed"},
			{Time: t0.Add(1 * time.Second), Line: "one"},
		}},
		{Labels: map[string]string{"server": "edge-2", "stream": "stderr"}, Entries: []CentralEntry{
			{Time: t0.Add(2 * time.Second), Line: "two"},
		}},
	}}
	f.svc.SetCentralLogs(fc)

	page, err := f.svc.ServiceCentralLogs(f.ctx, f.web.ID, CentralParams{Query: `bad "x"`, Limit: 2})
	require.NoError(t, err)
	require.True(t, page.Enabled)
	require.Equal(t, f.ws.ID.String(), fc.tenant, "the tenant is the service's team")
	require.Equal(t, `{service_id="`+f.web.ID.String()+`"} |= "bad \"x\""`, fc.logql)
	require.Equal(t, now, fc.to)
	require.Equal(t, now.Add(-time.Hour), fc.from)
	require.Equal(t, 2, fc.limit)
	require.Equal(t, []CentralLogLine{
		{TS: t0.Add(3 * time.Second), Server: "edge-1", Stream: "stdout", Line: "token=•••••• failed"},
		{TS: t0.Add(2 * time.Second), Server: "edge-2", Stream: "stderr", Line: "two"},
	}, page.Lines, "newest first, redacted, cut to the limit")

	// The default limit, and the cap.
	_, err = f.svc.ServiceCentralLogs(f.ctx, f.web.ID, CentralParams{})
	require.NoError(t, err)
	require.Equal(t, CentralDefaultLimit, fc.limit)
	_, err = f.svc.ServiceCentralLogs(f.ctx, f.web.ID, CentralParams{Limit: 99999})
	require.NoError(t, err)
	require.Equal(t, CentralMaxLimit, fc.limit)

	// A window beyond a week reaches Loki as the newest week.
	_, err = f.svc.ServiceCentralLogs(f.ctx, f.web.ID, CentralParams{Since: "30d"})
	require.NoError(t, err)
	require.Equal(t, CentralMaxWindow, fc.to.Sub(fc.from))

	// Bad input is refused before Loki is asked.
	calls := fc.calls
	_, err = f.svc.ServiceCentralLogs(f.ctx, f.web.ID, CentralParams{Since: "yesterday"})
	requireStatus(t, err, http.StatusUnprocessableEntity, "")
	_, err = f.svc.ServiceCentralLogs(f.ctx, f.web.ID, CentralParams{Query: string(make([]rune, 501))})
	requireStatus(t, err, http.StatusUnprocessableEntity, "")
	require.Equal(t, calls, fc.calls)

	// A log store failure is a 502 that does not leak its answer.
	fc.err = errors.New("loki answered 500: secret internals")
	_, err = f.svc.ServiceCentralLogs(f.ctx, f.web.ID, CentralParams{})
	requireStatus(t, err, http.StatusBadGateway, "central_logs_unavailable")
	require.NotContains(t, err.Error(), "internals")
}

func TestCentralLogsProcessFollowsTheRunningRelease(t *testing.T) {
	f := newFixture(t)
	d := f.builtinDrain()
	on := true
	_, err := f.svc.UpdateDrain(f.ctx, d.ID, DrainPatch{Enabled: &on})
	require.NoError(t, err)
	t0 := time.Date(2026, 3, 10, 11, 50, 0, 0, time.UTC)
	fc := &fakeCentral{streams: []CentralStream{{Labels: map[string]string{"server": "edge-1", "stream": "stdout"}, Entries: []CentralEntry{
		{Time: t0.Add(2 * time.Second), Line: "worker | job"},
		{Time: t0.Add(1 * time.Second), Line: "web | GET /"},
	}}}}
	f.svc.SetCentralLogs(fc)
	id := f.web.ID.String()
	stage := func(cfg string) {
		_, err := f.pool.Exec(context.Background(), "UPDATE services SET config = $2 WHERE id = $1", f.web.ID, cfg)
		require.NoError(t, err)
	}

	// A worker staged on a single-process release: its lines carry no prefix,
	// so the default process is every line.
	stage(`{"image":"nginx","processes":[{"name":"worker","startCommand":["bin/work"]}]}`)
	f.run(f.web.ID, `{"config":{"image":"nginx"}}`)
	page, err := f.svc.ServiceCentralLogs(f.ctx, f.web.ID, CentralParams{Process: "web"})
	require.NoError(t, err)
	require.Equal(t, `{service_id="`+id+`"}`, fc.logql)
	require.Len(t, page.Lines, 2)

	// The default process renamed in the staged config: the lines carry the
	// running name.
	stage(`{"image":"nginx","processName":"http","processes":[{"name":"worker","startCommand":["bin/work"]}]}`)
	f.run(f.web.ID, `{"config":{"image":"nginx","processes":[{"name":"worker","startCommand":["bin/work"]}]}}`)
	page, err = f.svc.ServiceCentralLogs(f.ctx, f.web.ID, CentralParams{Process: "http"})
	require.NoError(t, err)
	require.Equal(t, `{service_id="`+id+`"} |= "web | "`, fc.logql)
	require.Len(t, page.Lines, 1)
	require.Equal(t, "web | GET /", page.Lines[0].Line)
}

func TestCentralLogsAuthorization(t *testing.T) {
	f := newFixture(t)
	d := f.builtinDrain()
	on := true
	_, err := f.svc.UpdateDrain(f.ctx, d.ID, DrainPatch{Enabled: &on})
	require.NoError(t, err)
	fc := &fakeCentral{}
	f.svc.SetCentralLogs(fc)

	// logs:read is required, even while the drain is off or the service is the caller's.
	_, err = f.svc.ServiceCentralLogs(f.tokenCtx([]string{policy.ScopeServersRead, policy.ScopeProjectsRead}), f.web.ID, CentralParams{})
	requireStatus(t, err, http.StatusForbidden, "")
	// A token restricted to project A cannot read project B's service (404).
	tok := f.tokenCtx([]string{policy.ScopeLogsRead}, f.projA.ID)
	_, err = f.svc.ServiceCentralLogs(tok, f.other.ID, CentralParams{})
	requireStatus(t, err, http.StatusNotFound, "")
	_, err = f.svc.ServiceCentralLogs(tok, f.web.ID, CentralParams{})
	require.NoError(t, err)

	// A service of another team is not found, and Loki is not asked.
	bg := context.Background()
	ws2, err := f.q.CreateWorkspace(bg, "w2")
	require.NoError(t, err)
	p2, err := f.q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws2.ID, Name: "Other", Slug: "other"})
	require.NoError(t, err)
	e2, err := f.q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: p2.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	foreign, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: e2.ID, Name: "x", Slug: "x", Kind: "app", SourceType: "image", Config: json.RawMessage(`{"image":"nginx"}`),
	})
	require.NoError(t, err)
	calls := fc.calls
	_, err = f.svc.ServiceCentralLogs(f.ctx, foreign.ID, CentralParams{})
	requireStatus(t, err, http.StatusNotFound, "")
	_, err = f.svc.ServiceCentralLogs(f.ctx, uuid.New(), CentralParams{})
	requireStatus(t, err, http.StatusNotFound, "")
	require.Equal(t, calls, fc.calls)
}
