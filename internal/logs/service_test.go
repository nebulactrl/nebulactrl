package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeAgents answers requests like agents would and records what it was asked.
type fakeAgents struct {
	mu       sync.Mutex
	online   map[uuid.UUID]bool
	handlers map[uuid.UUID]func(kind string, payload json.RawMessage, secrets map[string]string) agentprotocol.ResultFrame
	resynced []uuid.UUID
	asked    []asked
}

type asked struct {
	server  uuid.UUID
	kind    string
	payload json.RawMessage
	secrets map[string]string
}

func (f *fakeAgents) Online(id uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.online[id]
}

func (f *fakeAgents) Resync(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resynced = append(f.resynced, id)
	return nil
}

func (f *fakeAgents) Request(_ context.Context, id uuid.UUID, kind string, payload any, secrets map[string]string, _ time.Duration) (agentprotocol.ResultFrame, error) {
	raw, _ := json.Marshal(payload)
	f.mu.Lock()
	if !f.online[id] {
		f.mu.Unlock()
		return agentprotocol.ResultFrame{}, apierr.New(http.StatusServiceUnavailable, "server_offline", "the server is offline")
	}
	f.asked = append(f.asked, asked{id, kind, raw, secrets})
	h := f.handlers[id]
	f.mu.Unlock()
	if h == nil {
		return agentprotocol.ResultFrame{}, fmt.Errorf("no handler for %s", id)
	}
	return h(kind, raw, secrets), nil
}

type fakeSecrets map[uuid.UUID][]string

func (f fakeSecrets) SecretValues(_ context.Context, sc services.Scope) ([]string, error) {
	return f[sc.Service.ID], nil
}

type maskRedactor struct{ secret string }

func (m maskRedactor) RedactorFor(context.Context, uuid.UUID) (func(string) string, error) {
	return func(s string) string { return strings.ReplaceAll(s, m.secret, "••••••") }, nil
}

type fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	svc    *Service
	agents *fakeAgents
	ctx    context.Context
	ws     db.Workspace
	projA  db.Project
	projB  db.Project
	envA   db.Environment
	web    db.Service // project A, server 1
	api    db.Service // project A, server 2
	other  db.Service // project B
	srv1   db.Server
	srv2   db.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	mk := func(name, slug string) (db.Project, db.Environment) {
		p, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: name, Slug: slug})
		require.NoError(t, err)
		e, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: p.ID, Name: "production", Slug: "production", IsProduction: true})
		require.NoError(t, err)
		return p, e
	}
	pa, ea := mk("Shop", "shop")
	pb, eb := mk("Blog", "blog")
	srv1, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "edge-1"})
	require.NoError(t, err)
	srv2, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "edge-2"})
	require.NoError(t, err)
	mkSvc := func(env db.Environment, name string, srv db.Server) db.Service {
		s, err := q.InsertService(bg, db.InsertServiceParams{
			ID: uuid.New(), EnvironmentID: env.ID, Name: name, Slug: name, Kind: "app", SourceType: "image", Config: json.RawMessage(`{"image":"nginx"}`), ServerID: &srv.ID,
		})
		require.NoError(t, err)
		return s
	}
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	agents := &fakeAgents{online: map[uuid.UUID]bool{}, handlers: map[uuid.UUID]func(string, json.RawMessage, map[string]string) agentprotocol.ResultFrame{}}
	svc := NewService(Deps{Pool: pool, Bus: events.NewBus(), Box: box, Agents: agents, Redactor: maskRedactor{"hunter2-secret"}})
	f := &fixture{
		t: t, pool: pool, q: q, svc: svc, agents: agents, ws: ws, projA: pa, projB: pb, envA: ea,
		web: mkSvc(ea, "web", srv1), api: mkSvc(ea, "api", srv2), other: mkSvc(eb, "blog", srv1), srv1: srv1, srv2: srv2,
	}
	f.ctx = auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "a@example.com", Scopes: policy.AllScopes})
	return f
}

func (f *fixture) tokenCtx(scopes []string, projects ...uuid.UUID) context.Context {
	id := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, TokenID: &id, UserID: uuid.New(), WorkspaceID: f.ws.ID, Label: "tok", Scopes: scopes, ProjectIDs: projects,
	})
}

func lokiDrain(name string) DrainInput {
	return DrainInput{
		Name: name, Kind: agentprotocol.DrainLoki,
		Config:  DrainConfig{Loki: &agentprotocol.LokiDrainConfig{URL: "https://loki.example.com/loki/api/v1/push", TenantID: "t1"}},
		Secrets: LogDrainSecretsInput{Username: "grafana", Password: "pw-very-secret-1", Headers: map[string]string{"X-Api-Key": "key-very-secret-2"}},
	}
}

func queryResult(lines ...agentprotocol.HistoryLine) agentprotocol.ResultFrame {
	raw, _ := json.Marshal(agentprotocol.LogsQueryResult{Lines: lines, Capturing: true})
	return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: raw}
}

func line(seq uint64, ts time.Time, text string) agentprotocol.HistoryLine {
	return agentprotocol.HistoryLine{Seq: seq, TS: ts, Text: text}
}

func TestDrainSecretsNeverLeave(t *testing.T) {
	f := newFixture(t)
	v, err := f.svc.CreateDrain(f.ctx, lokiDrain("grafana"))
	require.NoError(t, err)
	require.Equal(t, mask, v.Secrets["password"])
	require.Equal(t, []string{"X-Api-Key"}, v.HeaderNames)

	list, err := f.svc.ListDrains(f.ctx)
	require.NoError(t, err)
	raw, _ := json.Marshal(list)
	for _, secret := range []string{"pw-very-secret-1", "key-very-secret-2"} {
		require.NotContains(t, string(raw), secret)
	}
	// ...nor in the database in the clear.
	var enc []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT secrets_enc FROM log_drains WHERE id = $1`, v.ID).Scan(&enc))
	require.NotContains(t, string(enc), "pw-very-secret-1")

	// An update without secrets keeps them; a clear removes just that one.
	name := "grafana cloud"
	_, err = f.svc.UpdateDrain(f.ctx, v.ID, DrainPatch{Name: &name})
	require.NoError(t, err)
	cfg, secrets, err := f.svc.LogsConfigFor(f.ctx, f.srv1.ID)
	require.NoError(t, err)
	require.Len(t, cfg.Drains, 1)
	// The agent starts a drain at its creation: it needs the creation time and the
	// control plane's clock to place it on its own.
	require.False(t, cfg.Now.IsZero())
	require.False(t, cfg.Drains[0].CreatedAt.IsZero())
	require.False(t, cfg.Drains[0].CreatedAt.After(cfg.Now))
	require.Equal(t, "pw-very-secret-1", secrets[agentprotocol.LogDrainSecretKey(v.ID.String(), "password")])
	require.JSONEq(t, `{"X-Api-Key":"key-very-secret-2"}`, secrets[agentprotocol.LogDrainSecretKey(v.ID.String(), "headers")])

	_, err = f.svc.UpdateDrain(f.ctx, v.ID, DrainPatch{Secrets: &LogDrainSecretsInput{Clear: []string{"headers", "username", "password"}}})
	require.NoError(t, err)
	_, secrets, err = f.svc.LogsConfigFor(f.ctx, f.srv1.ID)
	require.NoError(t, err)
	require.Empty(t, secrets)
}

func TestDrainValidationAndScope(t *testing.T) {
	f := newFixture(t)
	bad := func(in DrainInput) {
		t.Helper()
		_, err := f.svc.CreateDrain(f.ctx, in)
		var ae *apierr.Error
		require.ErrorAs(t, err, &ae)
		require.Equal(t, http.StatusUnprocessableEntity, ae.Status, ae.Message)
	}
	in := lokiDrain("x")
	in.Config.Loki.URL = "ftp://loki"
	bad(in)
	in = lokiDrain("x")
	in.Query = `level:error (`
	bad(in)
	in = lokiDrain("x")
	in.Secrets.Password = ""
	bad(in) // username without password
	in = lokiDrain("x")
	f2 := false
	in.AllProjects = &f2
	bad(in) // no project chosen
	in.ProjectIDs = []uuid.UUID{uuid.New()}
	bad(in) // unknown project
	in = lokiDrain("x")
	in.ServiceIDs = []uuid.UUID{uuid.New()}
	bad(in) // unknown service
	in = DrainInput{Name: "s", Kind: agentprotocol.DrainSyslog, Config: DrainConfig{Syslog: &agentprotocol.SyslogDrainConfig{Address: "h:514", Protocol: "udp"}}, Secrets: LogDrainSecretsInput{Token: "t"}}
	bad(in) // syslog has no credentials
	in = lokiDrain("x")
	in.BufferMB = 1
	bad(in)

	// A scoped drain, then the name is unique.
	ok := lokiDrain("scoped")
	ok.AllProjects = &f2
	ok.ProjectIDs = []uuid.UUID{f.projA.ID}
	ok.ServiceIDs = []uuid.UUID{f.web.ID}
	ok.Query = "level:error"
	v, err := f.svc.CreateDrain(f.ctx, ok)
	require.NoError(t, err)
	require.False(t, v.AllProjects)
	require.Equal(t, DefaultBufferMB, v.BufferMB)
	_, err = f.svc.CreateDrain(f.ctx, lokiDrain("SCOPED"))
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusConflict, ae.Status)

	// Only admins manage drains.
	_, err = f.svc.ListDrains(f.tokenCtx([]string{policy.ScopeProjectsRead}))
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusForbidden, ae.Status)
}

func TestConfigForServerAndResync(t *testing.T) {
	f := newFixture(t)
	f.agents.online[f.srv1.ID] = true
	_, err := f.svc.CreateDrain(f.ctx, lokiDrain("on"))
	require.NoError(t, err)
	off := false
	d2 := lokiDrain("off")
	d2.Enabled = &off
	d2v, err := f.svc.CreateDrain(f.ctx, d2)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{f.srv1.ID}, f.agents.resynced[:1], "only the connected server is resynced")

	cfg, secrets, err := f.svc.LogsConfigFor(f.ctx, f.srv1.ID)
	require.NoError(t, err)
	require.True(t, cfg.Capture)
	require.Equal(t, agentprotocol.DefaultLogRetentionDays, cfg.Retention.Days)
	require.Equal(t, "edge-1", cfg.ServerName)
	require.Len(t, cfg.Drains, 2)
	// Services of this server, with their project and environment names.
	names := map[string]string{}
	for _, s := range cfg.Services {
		names[s.Name] = s.Project + "/" + s.Environment
	}
	require.Equal(t, map[string]string{"web": "Shop/production", "blog": "Blog/production"}, names)
	// The disabled drain carries no credentials.
	require.NotContains(t, secrets, agentprotocol.LogDrainSecretKey(d2v.ID.String(), "password"))

	// Retention settings.
	_, err = f.svc.UpdateSettings(f.ctx, f.srv1.ID, SettingsInput{Capture: false, RetentionDays: 3, MaxServiceBytes: 64 << 20, MaxServerBytes: 1 << 30})
	require.NoError(t, err)
	cfg, _, err = f.svc.LogsConfigFor(f.ctx, f.srv1.ID)
	require.NoError(t, err)
	require.False(t, cfg.Capture)
	require.Equal(t, 3, cfg.Retention.Days)
	require.Equal(t, int64(64<<20), cfg.Retention.MaxServiceBytes)
	for _, in := range []SettingsInput{
		{Capture: true, RetentionDays: 0, MaxServiceBytes: 64 << 20, MaxServerBytes: 1 << 30},
		{Capture: true, RetentionDays: 91, MaxServiceBytes: 64 << 20, MaxServerBytes: 1 << 30},
		{Capture: true, RetentionDays: 7, MaxServiceBytes: 1 << 20, MaxServerBytes: 1 << 30},
		{Capture: true, RetentionDays: 7, MaxServiceBytes: 2 << 30, MaxServerBytes: 1 << 30},
	} {
		_, err = f.svc.UpdateSettings(f.ctx, f.srv1.ID, in)
		var ae *apierr.Error
		require.ErrorAs(t, err, &ae, "%+v", in)
		require.Equal(t, http.StatusUnprocessableEntity, ae.Status)
	}
}

func TestStatusReportsShowOnTheDrain(t *testing.T) {
	f := newFixture(t)
	v, err := f.svc.CreateDrain(f.ctx, lokiDrain("d"))
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, f.svc.HandleLogStatus(context.Background(), f.srv1.ID, agentprotocol.LogStatus{
		Capturing: true, TotalBytes: 1234,
		Services: []agentprotocol.LogServiceUsage{{ServiceID: f.web.ID.String(), Bytes: 1234, Lines: 50}},
		Drains: []agentprotocol.LogDrainStatus{
			{ID: v.ID.String(), State: agentprotocol.DrainRetrying, LastError: "HTTP 502", LastErrorAt: &now, SentLines: 10, DroppedLines: 2, BacklogBytes: 99},
			{ID: uuid.NewString(), State: agentprotocol.DrainOK}, // a drain the control plane does not know: ignored
		},
	}))
	got, err := f.svc.GetDrain(f.ctx, v.ID)
	require.NoError(t, err)
	require.Len(t, got.Servers, 1)
	require.Equal(t, "edge-1", got.Servers[0].ServerName)
	require.Equal(t, agentprotocol.DrainRetrying, got.Servers[0].State)
	require.Equal(t, int64(2), got.Servers[0].DroppedLines)

	st, err := f.svc.GetSettings(f.ctx, f.srv1.ID)
	require.NoError(t, err)
	require.NotNil(t, st.Usage)
	require.Equal(t, int64(1234), st.Usage.TotalBytes)

	// A report that no longer lists the drain forgets its status.
	require.NoError(t, f.svc.HandleLogStatus(context.Background(), f.srv1.ID, agentprotocol.LogStatus{}))
	got, _ = f.svc.GetDrain(f.ctx, v.ID)
	require.Empty(t, got.Servers)
}

// Drain IDs are not secrets: a server of another team must not be able to put a
// status row (its own name and a text of its choosing) on this team's drain.
func TestStatusOfAnotherTeamsServerIsIgnored(t *testing.T) {
	f := newFixture(t)
	v, err := f.svc.CreateDrain(f.ctx, lokiDrain("d"))
	require.NoError(t, err)
	bg := context.Background()
	otherTeam, err := f.q.CreateWorkspace(bg, "intruders")
	require.NoError(t, err)
	intruder, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: otherTeam.ID, Name: "evil-1"})
	require.NoError(t, err)

	require.NoError(t, f.svc.HandleLogStatus(bg, intruder.ID, agentprotocol.LogStatus{
		Drains: []agentprotocol.LogDrainStatus{{ID: v.ID.String(), State: agentprotocol.DrainRetrying, LastError: "pay us"}},
	}))
	got, err := f.svc.GetDrain(f.ctx, v.ID)
	require.NoError(t, err)
	require.Empty(t, got.Servers, "a foreign server's report never reaches the drain page")

	// The team's own server still can.
	require.NoError(t, f.svc.HandleLogStatus(bg, f.srv1.ID, agentprotocol.LogStatus{
		Drains: []agentprotocol.LogDrainStatus{{ID: v.ID.String(), State: agentprotocol.DrainOK}},
	}))
	got, err = f.svc.GetDrain(f.ctx, v.ID)
	require.NoError(t, err)
	require.Len(t, got.Servers, 1)
}

func TestServiceHistoryRedactsOffline503AndIDOR(t *testing.T) {
	f := newFixture(t)
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	f.agents.online[f.srv1.ID] = true
	f.agents.handlers[f.srv1.ID] = func(kind string, payload json.RawMessage, _ map[string]string) agentprotocol.ResultFrame {
		var q agentprotocol.LogsQuery
		require.NoError(t, json.Unmarshal(payload, &q))
		require.Equal(t, agentprotocol.KindLogsQuery, kind)
		require.Equal(t, f.web.ID.String(), q.ServiceID)
		require.Equal(t, "web", q.ServiceName)
		require.Equal(t, "level:error", q.Query)
		return queryResult(
			line(5, t0.Add(2*time.Second), "token=hunter2-secret failed"),
			line(4, t0.Add(1*time.Second), "plain"),
		)
	}
	page, err := f.svc.ServiceHistory(f.ctx, f.web.ID, HistoryParams{Query: "level:error"})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.Equal(t, "token=•••••• failed", page.Items[0].Line)
	require.Equal(t, "runtime", page.Items[0].Stream)
	require.True(t, page.Capturing)

	// The server of api is offline: the history is unavailable, with its name.
	_, err = f.svc.ServiceHistory(f.ctx, f.api.ID, HistoryParams{})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusServiceUnavailable, ae.Status)
	require.Equal(t, "server_offline", ae.Code)
	require.Contains(t, ae.Message, "edge-2")

	// A token restricted to project A cannot read project B (404), and a token
	// without logs:read is refused, even with projects:read.
	tok := f.tokenCtx([]string{policy.ScopeLogsRead}, f.projA.ID)
	_, err = f.svc.ServiceHistory(tok, f.other.ID, HistoryParams{})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusNotFound, ae.Status)
	_, err = f.svc.ServiceHistory(f.tokenCtx([]string{policy.ScopeServersRead, policy.ScopeProjectsRead}), f.web.ID, HistoryParams{})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusForbidden, ae.Status)
	_, err = f.svc.EnvironmentHistory(tok, f.other.EnvironmentID, HistoryParams{})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusNotFound, ae.Status)

	_, err = f.svc.ServiceHistory(f.ctx, f.web.ID, HistoryParams{From: t0, To: t0.Add(-time.Hour)})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status)
}

func TestEnvironmentHistoryMergesByTimeAndPages(t *testing.T) {
	f := newFixture(t)
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	f.agents.online[f.srv1.ID] = true
	f.agents.online[f.srv2.ID] = true
	// web has the odd seconds 1..9, api the even ones 2..10, newest first.
	all := map[string][]agentprotocol.HistoryLine{}
	for sec := 1; sec <= 10; sec++ {
		name := "web"
		if sec%2 == 0 {
			name = "api"
		}
		all[name] = append([]agentprotocol.HistoryLine{line(uint64(sec), t0.Add(time.Duration(sec)*time.Second), fmt.Sprintf("%s-%d", name, sec))}, all[name]...)
	}
	serve := func(name string) func(string, json.RawMessage, map[string]string) agentprotocol.ResultFrame {
		return func(_ string, payload json.RawMessage, _ map[string]string) agentprotocol.ResultFrame {
			var q agentprotocol.LogsQuery
			require.NoError(t, json.Unmarshal(payload, &q))
			var page []agentprotocol.HistoryLine
			for _, l := range all[name] { // seq descending
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
			raw, _ := json.Marshal(agentprotocol.LogsQueryResult{Lines: page, Capturing: true, NextCursor: nextCursor(all[name], page, q.Cursor)})
			return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: raw}
		}
	}
	f.agents.handlers[f.srv1.ID] = serve("web")
	f.agents.handlers[f.srv2.ID] = serve("api")

	var got []string
	cursor := ""
	for range 20 {
		page, err := f.svc.EnvironmentHistory(f.ctx, f.envA.ID, HistoryParams{Limit: 4, Cursor: cursor})
		require.NoError(t, err)
		for _, it := range page.Items {
			got = append(got, it.Line)
			require.NotEmpty(t, it.ServiceName)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	want := []string{"api-10", "web-9", "api-8", "web-7", "api-6", "web-5", "api-4", "web-3", "api-2", "web-1"}
	require.Equal(t, want, got)

	// One server offline: its service is listed, the other still answers.
	f.agents.online[f.srv2.ID] = false
	page, err := f.svc.EnvironmentHistory(f.ctx, f.envA.ID, HistoryParams{Limit: 50})
	require.NoError(t, err)
	require.Len(t, page.Items, 5)
	require.Len(t, page.Unavailable, 1)
	require.Equal(t, "api", page.Unavailable[0].ServiceName)
	require.Equal(t, "server_offline", page.Unavailable[0].Reason)
	require.Contains(t, page.Unavailable[0].Message, "edge-2")

	// Back online, everything is there again.
	f.agents.online[f.srv2.ID] = true
	page, err = f.svc.EnvironmentHistory(f.ctx, f.envA.ID, HistoryParams{Limit: 50})
	require.NoError(t, err)
	require.Len(t, page.Items, 10)
	require.Empty(t, page.Unavailable)
}

// nextCursor reproduces the agent's rule: more may follow when lines older than
// the page's last remain.
func nextCursor(all, page []agentprotocol.HistoryLine, from string) string {
	if len(page) == 0 {
		return ""
	}
	last := page[len(page)-1].Seq
	if all[len(all)-1].Seq < last {
		return fmt.Sprint(last)
	}
	return ""
}

func TestTestDrainRunsOnAConnectedServer(t *testing.T) {
	f := newFixture(t)
	v, err := f.svc.CreateDrain(f.ctx, lokiDrain("t"))
	require.NoError(t, err)
	_, err = f.svc.TestDrain(f.ctx, v.ID, nil)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "no_online_server", ae.Code)

	f.agents.online[f.srv2.ID] = true
	f.agents.handlers[f.srv2.ID] = func(kind string, payload json.RawMessage, secrets map[string]string) agentprotocol.ResultFrame {
		require.Equal(t, agentprotocol.KindLogsDrainTest, kind)
		require.Equal(t, "pw-very-secret-1", secrets[agentprotocol.LogDrainSecretKey(v.ID.String(), "password")])
		require.NotContains(t, string(payload), "pw-very-secret-1", "secrets travel beside the payload, not in it")
		raw, _ := json.Marshal(agentprotocol.LogsDrainTestResult{OK: false, Message: "HTTP 401 from loki", StatusCode: 401})
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: raw}
	}
	res, err := f.svc.TestDrain(f.ctx, v.ID, nil)
	require.NoError(t, err)
	require.False(t, res.OK)
	require.Equal(t, 401, res.StatusCode)
	require.Equal(t, "edge-2", res.ServerName)
	got, _ := f.svc.GetDrain(f.ctx, v.ID)
	require.NotNil(t, got.Test.OK)
	require.False(t, *got.Test.OK)
	require.Equal(t, "HTTP 401 from loki", got.Test.Message)
}

func TestDrainsGetTheServicesSecretsToMask(t *testing.T) {
	f := newFixture(t)
	f.svc.sec = fakeSecrets{f.web.ID: {"hunter2-secret", "short", "another-long-secret"}}

	// No enabled drain: nothing to mask, nothing sent.
	_, secrets, err := f.svc.LogsConfigFor(f.ctx, f.srv1.ID)
	require.NoError(t, err)
	require.Empty(t, secrets)

	_, err = f.svc.CreateDrain(f.ctx, lokiDrain("d"))
	require.NoError(t, err)
	_, secrets, err = f.svc.LogsConfigFor(f.ctx, f.srv1.ID)
	require.NoError(t, err)
	var values []string
	require.NoError(t, json.Unmarshal([]byte(secrets[agentprotocol.LogRedactSecretKey(f.web.ID.String())]), &values))
	require.Equal(t, []string{"hunter2-secret", "another-long-secret"}, values, "values shorter than 6 characters are not masked anywhere")
	require.NotContains(t, secrets, agentprotocol.LogRedactSecretKey(f.other.ID.String()), "a service without secrets has no entry")
}

func TestStackHistoryFiltersByComponentPrefix(t *testing.T) {
	f := newFixture(t)
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cfg := `{"stack":{"compose":"services:\n  web:\n    image: a\n  sidecar:\n    image: b\n","components":[{"name":"sidecar","index":0},{"name":"web","index":1}]}}`
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET kind = 'stack', source_type = 'compose', config = $2 WHERE id = $1", f.web.ID, cfg)
	require.NoError(t, err)
	f.agents.online[f.srv1.ID] = true
	var queries []string
	f.agents.handlers[f.srv1.ID] = func(_ string, payload json.RawMessage, _ map[string]string) agentprotocol.ResultFrame {
		var q agentprotocol.LogsQuery
		require.NoError(t, json.Unmarshal(payload, &q))
		queries = append(queries, q.Query)
		return queryResult(
			line(3, t0.Add(3*time.Second), "web | GET / 200"),
			line(2, t0.Add(2*time.Second), "sidecar | mentions web | in the text"),
			line(1, t0.Add(1*time.Second), "web | started"),
		)
	}
	page, err := f.svc.ServiceHistory(f.ctx, f.web.ID, HistoryParams{Component: "web"})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.Equal(t, "web | GET / 200", page.Items[0].Line)
	require.Equal(t, "web | started", page.Items[1].Line)
	_, err = f.svc.ServiceHistory(f.ctx, f.web.ID, HistoryParams{Component: "web", Query: "level:error OR started"})
	require.NoError(t, err)
	require.Equal(t, []string{`"web | "`, `(level:error OR started) "web | "`}, queries)

	_, err = f.svc.ServiceHistory(f.ctx, f.web.ID, HistoryParams{Component: "db"})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "unknown_component", ae.Code)
	// A component only exists for stacks.
	_, err = f.svc.ServiceHistory(f.ctx, f.api.ID, HistoryParams{Component: "web"})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status)
}

func TestProcessHistoryFiltersByProcessPrefix(t *testing.T) {
	f := newFixture(t)
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cfg := `{"image":"nginx","processes":[{"name":"worker","startCommand":["bin/work"]}]}`
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET config = $2 WHERE id = $1", f.web.ID, cfg)
	require.NoError(t, err)
	f.agents.online[f.srv1.ID] = true
	var queries []string
	f.agents.handlers[f.srv1.ID] = func(_ string, payload json.RawMessage, _ map[string]string) agentprotocol.ResultFrame {
		var q agentprotocol.LogsQuery
		require.NoError(t, json.Unmarshal(payload, &q))
		queries = append(queries, q.Query)
		return queryResult(
			line(3, t0.Add(3*time.Second), "worker | job done"),
			line(2, t0.Add(2*time.Second), "web | mentions worker | in the text"),
			line(1, t0.Add(1*time.Second), "worker | started"),
		)
	}
	page, err := f.svc.ServiceHistory(f.ctx, f.web.ID, HistoryParams{Process: "worker"})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.Equal(t, "worker | job done", page.Items[0].Line)
	require.Equal(t, "worker | started", page.Items[1].Line)
	_, err = f.svc.ServiceHistory(f.ctx, f.web.ID, HistoryParams{Process: "worker", Query: "level:error OR started"})
	require.NoError(t, err)
	require.Equal(t, []string{`"worker | "`, `(level:error OR started) "worker | "`}, queries)

	// Without a process every line comes back, prefixes and all.
	page, err = f.svc.ServiceHistory(f.ctx, f.web.ID, HistoryParams{})
	require.NoError(t, err)
	require.Len(t, page.Items, 3)

	var ae *apierr.Error
	_, err = f.svc.ServiceHistory(f.ctx, f.web.ID, HistoryParams{Process: "cron"})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status)
	require.Equal(t, "unknown_process", ae.Code)
	// A process only exists for services with processes.
	_, err = f.svc.ServiceHistory(f.ctx, f.api.ID, HistoryParams{Process: "worker"})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status)
	require.Equal(t, "validation_failed", ae.Code)
	// Component and process select by different things.
	_, err = f.svc.ServiceHistory(f.ctx, f.web.ID, HistoryParams{Component: "web", Process: "worker"})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status)
	require.Equal(t, "validation_failed", ae.Code)
}
