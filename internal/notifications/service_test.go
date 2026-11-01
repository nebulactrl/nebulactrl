package notifications

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type fixture struct {
	t     *testing.T
	pool  *pgxpool.Pool
	q     *db.Queries
	svc   *Service
	jm    *jobs.Manager
	box   *secretbox.Box
	logs  *syncBuffer
	ctx   context.Context
	ws    db.Workspace
	projA db.Project
	projB db.Project
	svcA  db.Service // a service of project A
	srv   db.Server
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
	pb, _ := mk("Blog", "blog")
	sv, err := q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: ea.ID, Name: "api", Slug: "api", Kind: "app", SourceType: "image", Config: json.RawMessage(`{"image":"nginx"}`),
	})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "edge-1"})
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)

	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	jm := jobs.New(pool, logger)
	svc := NewService(Deps{Pool: pool, Bus: events.NewBus(), Jobs: jm, Box: box, PublicURL: "https://ctrl.example.com", Logger: logger})
	RegisterJobs(jm, svc)
	svc.send.http = insecureClient()
	f := &fixture{t: t, pool: pool, q: q, svc: svc, jm: jm, box: box, logs: logs, ws: ws, projA: pa, projB: pb, svcA: sv, srv: srv}
	f.ctx = auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "dev@example.com", Scopes: policy.AllScopes})
	return f
}

// hook starts a receiver that answers with status and returns its URL and requests.
func hook(t *testing.T, status int, body string) (string, *capture) {
	t.Helper()
	var c capture
	srv := httptest.NewTLSServer(c.handler(status, body))
	t.Cleanup(srv.Close)
	return srv.URL, &c
}

func (f *fixture) slack(name, url string, rule *ChannelRuleInput) ChannelView {
	f.t.Helper()
	res, err := f.svc.CreateChannel(f.ctx, ChannelInput{Name: name, Kind: KindSlack, Secrets: ChannelSecretsInput{URL: url}, Rule: rule})
	require.NoError(f.t, err)
	return res.Channel
}

func allTypes() []string {
	var out []string
	for _, e := range catalog {
		out = append(out, e.Type)
	}
	return out
}

func apiCode(t *testing.T, err error) string {
	t.Helper()
	var e *apierr.Error
	require.True(t, errors.As(err, &e), "want an API error, got %v", err)
	return e.Code
}

func (f *fixture) deliveries(channel uuid.UUID) []db.NotificationDelivery {
	f.t.Helper()
	rows, err := f.q.ListNotificationDeliveries(context.Background(), db.ListNotificationDeliveriesParams{ChannelID: channel, WorkspaceID: f.ws.ID, RowLimit: 100})
	require.NoError(f.t, err)
	return rows
}

func (f *fixture) delivery(id uuid.UUID) db.NotificationDelivery {
	f.t.Helper()
	d, err := f.q.GetNotificationDelivery(context.Background(), id)
	require.NoError(f.t, err)
	return d
}

func (f *fixture) failedDeploy(project *uuid.UUID, service *uuid.UUID, body string) Event {
	return Event{Type: TypeDeployFailed, ProjectID: project, ServiceID: service, Title: "api failed to deploy", Body: body}
}

// --- channels -------------------------------------------------------------------

func TestChannelSecretsAreSealedAndMasked(t *testing.T) {
	f := newFixture(t)
	const token = "T000/B000/SUPERSECRETTOKENVALUE"
	created, err := f.svc.CreateChannel(f.ctx, ChannelInput{Name: "ops", Kind: KindSlack, Secrets: ChannelSecretsInput{URL: "https://hooks.slack.com/services/" + token}})
	require.NoError(t, err)
	id := created.Channel.ID

	// Every read shape hides the credential; the hint names the host only.
	got, err := f.svc.GetChannel(f.ctx, id)
	require.NoError(t, err)
	list, err := f.svc.ListChannels(f.ctx)
	require.NoError(t, err)
	for _, v := range []any{created, got, list} {
		raw, _ := json.Marshal(v)
		require.NotContains(t, string(raw), "SUPERSECRETTOKENVALUE")
		require.NotContains(t, string(raw), "B000")
	}
	require.Equal(t, "https://hooks.slack.com/"+mask, got.Secrets["url"])

	// At rest the URL lives only in the sealed blob.
	row, err := f.q.GetNotificationChannelInternal(context.Background(), id)
	require.NoError(t, err)
	require.NotContains(t, string(row.SecretsEnc), "SUPERSECRETTOKENVALUE")
	require.NotContains(t, string(row.Config), "hooks.slack.com")
	sec, err := openSecrets(f.box, row.ID, row.SecretsEnc)
	require.NoError(t, err)
	require.Contains(t, sec.URL, token)

	// The audit trail names the channel, never the credential.
	var md string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT metadata::text FROM audit_events WHERE action = $1", ActionChannelCreate).Scan(&md))
	require.NotContains(t, md, "SUPERSECRETTOKENVALUE")
	require.Contains(t, md, "ops")
}

func TestWebhookSigningSecretShownOnce(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.CreateChannel(f.ctx, ChannelInput{Name: "hook", Kind: KindWebhook, Secrets: ChannelSecretsInput{URL: "https://example.com/in"}})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(res.SigningSecret, "whsec_"), "generated when omitted")
	got, err := f.svc.GetChannel(f.ctx, res.Channel.ID)
	require.NoError(t, err)
	raw, _ := json.Marshal(got)
	require.NotContains(t, string(raw), res.SigningSecret)
	require.Equal(t, mask, got.Secrets["signingSecret"])

	// A caller-chosen secret is not echoed back.
	mine, err := f.svc.CreateChannel(f.ctx, ChannelInput{Name: "mine", Kind: KindWebhook, Secrets: ChannelSecretsInput{URL: "https://example.com/in", SigningSecret: "my-own-signing-secret"}})
	require.NoError(t, err)
	require.Empty(t, mine.SigningSecret)
}

func TestCreateChannelValidation(t *testing.T) {
	f := newFixture(t)
	okEmail := ChannelConfig{Host: "smtp.example.com", From: "alerts@example.com", To: []string{"ops@example.com"}}
	cases := map[string]ChannelInput{
		"unknown kind":        {Name: "x", Kind: "pager"},
		"blank name":          {Name: "  ", Kind: KindSlack, Secrets: ChannelSecretsInput{URL: "https://hooks.slack.com/x"}},
		"slack needs url":     {Name: "x", Kind: KindSlack},
		"slack needs https":   {Name: "x", Kind: KindSlack, Secrets: ChannelSecretsInput{URL: "http://hooks.slack.com/x"}},
		"url with userinfo":   {Name: "x", Kind: KindDiscord, Secrets: ChannelSecretsInput{URL: "https://user:pw@discord.com/api/webhooks/1/t"}},
		"webhook short key":   {Name: "x", Kind: KindWebhook, Secrets: ChannelSecretsInput{URL: "https://e.com/h", SigningSecret: "short"}},
		"webhook bad scheme":  {Name: "x", Kind: KindWebhook, Secrets: ChannelSecretsInput{URL: "ftp://e.com/h"}},
		"telegram bad token":  {Name: "x", Kind: KindTelegram, Config: ChannelConfig{ChatID: "-100123"}, Secrets: ChannelSecretsInput{BotToken: "nope"}},
		"telegram bad chat":   {Name: "x", Kind: KindTelegram, Config: ChannelConfig{ChatID: "not a chat"}, Secrets: ChannelSecretsInput{BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ"}},
		"email no host":       {Name: "x", Kind: KindEmail, Config: ChannelConfig{From: "a@example.com", To: []string{"b@example.com"}}},
		"email bad from":      {Name: "x", Kind: KindEmail, Config: ChannelConfig{Host: "h.example.com", From: "nope", To: []string{"b@example.com"}}},
		"email no recipients": {Name: "x", Kind: KindEmail, Config: ChannelConfig{Host: "h.example.com", From: "a@example.com"}},
		"email bad recipient": {Name: "x", Kind: KindEmail, Config: ChannelConfig{Host: "h.example.com", From: "a@example.com", To: []string{"b@example.com", "x y"}}},
		"email bad tls":       {Name: "x", Kind: KindEmail, Config: ChannelConfig{Host: "h.example.com", From: "a@example.com", To: []string{"b@example.com"}, TLS: "ssl3"}},
		"email user no pass":  {Name: "x", Kind: KindEmail, Config: ChannelConfig{Host: "h.example.com", From: "a@example.com", To: []string{"b@example.com"}, Username: "u"}},
		"email pass no user":  {Name: "x", Kind: KindEmail, Config: okEmail, Secrets: ChannelSecretsInput{Password: "p"}},
		"unknown event":       {Name: "x", Kind: KindEmail, Config: okEmail, Rule: &ChannelRuleInput{EventTypes: []string{"deploy.exploded"}}},
		"bad severity":        {Name: "x", Kind: KindEmail, Config: okEmail, Rule: &ChannelRuleInput{EventTypes: nil, MinSeverity: "loud"}},
		"scoped, no projects": {Name: "x", Kind: KindEmail, Config: okEmail, Rule: &ChannelRuleInput{AllProjects: new(false)}},
		"scoped, ghost":       {Name: "x", Kind: KindEmail, Config: okEmail, Rule: &ChannelRuleInput{AllProjects: new(false), ProjectIDs: []uuid.UUID{uuid.New()}}},
	}
	for name, in := range cases {
		_, err := f.svc.CreateChannel(f.ctx, in)
		require.Error(t, err, name)
		require.Equal(t, "validation_failed", apiCode(t, err), name)
	}
	list, err := f.svc.ListChannels(f.ctx)
	require.NoError(t, err)
	require.Empty(t, list, "a rejected channel leaves nothing behind")
}

func TestEmailDefaultsAndRecipientDedupe(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.CreateChannel(f.ctx, ChannelInput{Name: "mail", Kind: KindEmail, Config: ChannelConfig{
		Host: "smtp.example.com", From: "NebulaCtrl <alerts@example.com>", To: []string{"ops@example.com", " OPS@example.com ", "dev@example.com"},
	}})
	require.NoError(t, err)
	c := res.Channel.Config
	require.Equal(t, TLSStartTLS, c.TLS)
	require.Equal(t, 587, c.Port)
	require.Equal(t, []string{"ops@example.com", "dev@example.com"}, c.To)
	require.Empty(t, res.Channel.Secrets, "no password: nothing stored")
}

func TestDefaultRuleSubscribesFailuresAndAlertsOnly(t *testing.T) {
	f := newFixture(t)
	v := f.slack("ops", "https://hooks.slack.com/services/a/b/c", nil)
	require.Equal(t, SeverityInfo, v.Rule.MinSeverity)
	require.True(t, v.Rule.AllProjects)
	on := map[string]bool{}
	for _, tt := range v.Rule.EventTypes {
		on[tt] = true
	}
	for _, e := range catalog {
		require.Equal(t, e.DefaultOn, on[e.Type], e.Type)
	}
	for _, want := range []string{TypeDeployFailed, TypeServerOffline, TypeServerDiskAlert, TypeBackupFailed, TypeTaskFailed, TypeScaleBlocked, TypeCertExpiring} {
		require.True(t, on[want], want)
	}
	for _, off := range []string{TypeDeploySucceeded, TypeServerOnline, TypeBackupSucceeded, TypePreviewCreated, TypeMaintenanceOn} {
		require.False(t, on[off], off)
	}
}

func TestDuplicateChannelName(t *testing.T) {
	f := newFixture(t)
	f.slack("Ops", "https://hooks.slack.com/services/a/b/c", nil)
	_, err := f.svc.CreateChannel(f.ctx, ChannelInput{Name: "ops", Kind: KindSlack, Secrets: ChannelSecretsInput{URL: "https://hooks.slack.com/services/d/e/f"}})
	require.Equal(t, "channel_conflict", apiCode(t, err))
}

func TestUpdateKeepsSecretsUnlessReplaced(t *testing.T) {
	f := newFixture(t)
	url1, c1 := hook(t, 200, "ok")
	url2, c2 := hook(t, 200, "ok")
	v := f.slack("ops", url1, nil)

	name := "ops-renamed"
	got, err := f.svc.UpdateChannel(f.ctx, v.ID, ChannelPatch{Name: &name, Secrets: &ChannelSecretsInput{}, Enabled: new(true)})
	require.NoError(t, err)
	require.Equal(t, name, got.Name)
	_, err = f.svc.TestChannel(f.ctx, v.ID)
	require.NoError(t, err)
	require.Len(t, c1.all(), 1, "the stored URL still works")

	_, err = f.svc.UpdateChannel(f.ctx, v.ID, ChannelPatch{Secrets: &ChannelSecretsInput{URL: url2}})
	require.NoError(t, err)
	_, err = f.svc.TestChannel(f.ctx, v.ID)
	require.NoError(t, err)
	require.Len(t, c2.all(), 1)
	require.Len(t, c1.all(), 1)

	var md string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT metadata::text FROM audit_events WHERE action = $1 ORDER BY id DESC LIMIT 1", ActionChannelUpdate).Scan(&md))
	require.Contains(t, md, `"secretsChanged": true`)
	require.NotContains(t, md, url2)
}

func TestUpdateRuleAndScope(t *testing.T) {
	f := newFixture(t)
	v := f.slack("ops", "https://hooks.slack.com/services/a/b/c", nil)
	got, err := f.svc.UpdateChannel(f.ctx, v.ID, ChannelPatch{Rule: &ChannelRuleInput{
		EventTypes: []string{TypeDeploySucceeded, TypeDeployFailed, TypeDeployFailed}, MinSeverity: SeverityWarning,
		AllProjects: new(false), ProjectIDs: []uuid.UUID{f.projA.ID, f.projA.ID},
	}})
	require.NoError(t, err)
	require.Equal(t, []string{TypeDeployFailed, TypeDeploySucceeded}, got.Rule.EventTypes, "catalogue order, no duplicates")
	require.Equal(t, SeverityWarning, got.Rule.MinSeverity)
	require.False(t, got.Rule.AllProjects)
	require.Equal(t, []uuid.UUID{f.projA.ID}, got.Rule.ProjectIDs)

	// Back to the whole workspace: the project list is dropped.
	got, err = f.svc.UpdateChannel(f.ctx, v.ID, ChannelPatch{Rule: &ChannelRuleInput{EventTypes: []string{TypeDeployFailed}}})
	require.NoError(t, err)
	require.True(t, got.Rule.AllProjects)
	require.Empty(t, got.Rule.ProjectIDs)
}

func TestDeleteChannelRemovesDeliveries(t *testing.T) {
	f := newFixture(t)
	url, _ := hook(t, 200, "ok")
	v := f.slack("ops", url, nil)
	_, err := f.svc.TestChannel(f.ctx, v.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.DeleteChannel(f.ctx, v.ID))
	_, err = f.svc.GetChannel(f.ctx, v.ID)
	require.Equal(t, "not_found", apiCode(t, err))
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM notification_deliveries").Scan(&n))
	require.Zero(t, n)
}

func TestChannelEndpointsNeedAdmin(t *testing.T) {
	f := newFixture(t)
	v := f.slack("ops", "https://hooks.slack.com/services/a/b/c", nil)
	reader := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, WorkspaceID: f.ws.ID, Label: "ci", Scopes: []string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite}})
	calls := map[string]func() error{
		"types": func() error { _, err := f.svc.EventTypes(reader); return err },
		"list":  func() error { _, err := f.svc.ListChannels(reader); return err },
		"get":   func() error { _, err := f.svc.GetChannel(reader, v.ID); return err },
		"create": func() error {
			_, err := f.svc.CreateChannel(reader, ChannelInput{Name: "x", Kind: KindSlack})
			return err
		},
		"update": func() error { _, err := f.svc.UpdateChannel(reader, v.ID, ChannelPatch{}); return err },
		"delete": func() error { return f.svc.DeleteChannel(reader, v.ID) },
		"test":   func() error { _, err := f.svc.TestChannel(reader, v.ID); return err },
		"rotate": func() error { _, err := f.svc.RotateSecret(reader, v.ID); return err },
		"log":    func() error { _, err := f.svc.ListDeliveries(reader, v.ID, "", "", 10); return err },
	}
	for name, call := range calls {
		require.Equal(t, "forbidden", apiCode(t, call()), name)
	}
	// Without a principal: unauthorized.
	_, err := f.svc.ListChannels(context.Background())
	require.Equal(t, "unauthorized", apiCode(t, err))
}

// --- emit -----------------------------------------------------------------------

func TestEmitMatchesRules(t *testing.T) {
	f := newFixture(t)
	u, _ := hook(t, 200, "ok")
	def := f.slack("default", u, nil)
	succ := f.slack("successes", u, &ChannelRuleInput{EventTypes: []string{TypeDeploySucceeded}})
	scoped := f.slack("blog-only", u, &ChannelRuleInput{EventTypes: allTypes(), AllProjects: new(false), ProjectIDs: []uuid.UUID{f.projB.ID}})
	errOnly := f.slack("errors", u, &ChannelRuleInput{EventTypes: allTypes(), MinSeverity: SeverityError})
	off := f.slack("disabled", u, nil)
	_, err := f.svc.UpdateChannel(f.ctx, off.ID, ChannelPatch{Enabled: new(false)})
	require.NoError(t, err)

	count := func(v ChannelView) int { return len(f.deliveries(v.ID)) }

	// A failed deploy of a project-A service: default + errors; not the success-only,
	// project-B or disabled channels.
	res, err := f.svc.emit(f.ctx, f.failedDeploy(nil, &f.svcA.ID, "Health check failed"))
	require.NoError(t, err)
	require.Equal(t, 2, res.Queued)
	require.Equal(t, 1, count(def))
	require.Equal(t, 1, count(errOnly))
	require.Zero(t, count(succ))
	require.Zero(t, count(scoped))
	require.Zero(t, count(off))

	// The project and names were looked up from the service.
	d := f.deliveries(def.ID)[0]
	var p Payload
	require.NoError(t, json.Unmarshal(d.Payload, &p))
	require.Equal(t, f.projA.ID.String(), p.ProjectID)
	require.Equal(t, f.svcA.ID.String(), p.ServiceID)
	require.Equal(t, []MessageField{{"Project", "Shop"}, {"Environment", "production"}, {"Service", "api"}}, p.Fields)
	require.Equal(t, "/p/"+f.projA.ID.String()+"/production?service="+f.svcA.ID.String(), p.URL)
	require.Equal(t, SeverityError, p.Severity, "the type's default")
	require.Equal(t, f.projA.ID, *d.ProjectID)

	// A success: only the channel that subscribes to it.
	res, err = f.svc.emit(f.ctx, Event{Type: TypeDeploySucceeded, ServiceID: &f.svcA.ID, Title: "api deployed"})
	require.NoError(t, err)
	require.Equal(t, 1, res.Queued)
	require.Equal(t, 1, count(succ))

	// A server alert has no project: whole-workspace channels get it, the
	// project-scoped one does not.
	res, err = f.svc.emit(f.ctx, Event{Type: TypeServerOffline, ServerID: &f.srv.ID, Title: "edge-1 is offline"})
	require.NoError(t, err)
	require.Equal(t, 2, res.Queued)
	require.Zero(t, count(scoped))
	var sp Payload
	require.NoError(t, json.Unmarshal(f.deliveries(def.ID)[0].Payload, &sp))
	require.Equal(t, "/servers/"+f.srv.ID.String(), sp.URL)
	require.Equal(t, []MessageField{{"Server", "edge-1"}}, sp.Fields)

	// An event of project B reaches the project-B channel.
	res, err = f.svc.emit(f.ctx, f.failedDeploy(&f.projB.ID, nil, "x"))
	require.NoError(t, err)
	require.Equal(t, 3, res.Queued)
	require.Equal(t, 1, count(scoped))
}

func TestEmitSeverityOverrideAndFilter(t *testing.T) {
	f := newFixture(t)
	u, _ := hook(t, 200, "ok")
	warn := f.slack("warn", u, &ChannelRuleInput{EventTypes: allTypes(), MinSeverity: SeverityWarning})
	// deploy.succeeded is info by default: below the minimum; escalated by the caller it passes.
	res, err := f.svc.emit(f.ctx, Event{Type: TypeDeploySucceeded, ServiceID: &f.svcA.ID, Title: "a"})
	require.NoError(t, err)
	require.Zero(t, res.Queued)
	res, err = f.svc.emit(f.ctx, Event{Type: TypeDeploySucceeded, ServiceID: &f.svcA.ID, Title: "b", Severity: "warn"})
	require.NoError(t, err)
	require.Equal(t, 1, res.Queued)
	require.Len(t, f.deliveries(warn.ID), 1)
}

func TestEmitQueuesARiverJobPerDelivery(t *testing.T) {
	f := newFixture(t)
	u, _ := hook(t, 200, "ok")
	f.slack("a", u, nil)
	f.slack("b", u, nil)
	f.svc.Emit(f.ctx, f.failedDeploy(nil, &f.svcA.ID, "x"))
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM river_job WHERE kind = 'notification_deliver' AND max_attempts = $1", MaxAttempts).Scan(&n))
	require.Equal(t, 2, n)
}

func TestEmitNeverFailsTheCaller(t *testing.T) {
	f := newFixture(t)
	require.NotPanics(t, func() {
		f.svc.Emit(f.ctx, Event{Type: "nonsense", Title: "x"})
		f.svc.Emit(f.ctx, Event{})
		f.svc.Emit(f.ctx, Event{Type: TypeDeployFailed, ServiceID: &[]uuid.UUID{uuid.New()}[0], Title: "ghost service"})
		var nilSvc *Service
		nilSvc.Emit(f.ctx, Event{Type: TypeDeployFailed})
		Emit(f.ctx, nil, Event{Type: TypeDeployFailed})
	})
	// A canceled caller context does not drop the notification.
	u, _ := hook(t, 200, "ok")
	v := f.slack("a", u, nil)
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	f.svc.Emit(ctx, f.failedDeploy(nil, &f.svcA.ID, "x"))
	require.Len(t, f.deliveries(v.ID), 1)
}

func TestEmitDedupesIdenticalEventsWithinFiveMinutes(t *testing.T) {
	f := newFixture(t)
	u, _ := hook(t, 200, "ok")
	v := f.slack("a", u, nil)
	ev := f.failedDeploy(nil, &f.svcA.ID, "same")

	res, err := f.svc.emit(f.ctx, ev)
	require.NoError(t, err)
	require.Equal(t, emitResult{Queued: 1}, res)
	res, err = f.svc.emit(f.ctx, ev)
	require.NoError(t, err)
	require.Equal(t, emitResult{Deduped: 1}, res)
	require.Len(t, f.deliveries(v.ID), 1)

	// A different body is a different event.
	res, err = f.svc.emit(f.ctx, f.failedDeploy(nil, &f.svcA.ID, "other"))
	require.NoError(t, err)
	require.Equal(t, 1, res.Queued)
	require.Len(t, f.deliveries(v.ID), 2)

	// Dedupe is per channel: another channel still gets the identical event.
	w := f.slack("b", u, nil)
	res, err = f.svc.emit(f.ctx, ev)
	require.NoError(t, err)
	require.Equal(t, emitResult{Queued: 1, Deduped: 1}, res)
	require.Len(t, f.deliveries(w.ID), 1)

	// After the window the same event is news again.
	_, err = f.pool.Exec(context.Background(), "UPDATE notification_deliveries SET created_at = now() - interval '6 minutes'")
	require.NoError(t, err)
	res, err = f.svc.emit(f.ctx, ev)
	require.NoError(t, err)
	require.Equal(t, emitResult{Queued: 2}, res)
}

func TestEmitDedupeHoldsUnderConcurrency(t *testing.T) {
	f := newFixture(t)
	u, _ := hook(t, 200, "ok")
	v := f.slack("a", u, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.svc.Emit(f.ctx, f.failedDeploy(nil, &f.svcA.ID, "burst"))
		}()
	}
	wg.Wait()
	require.Len(t, f.deliveries(v.ID), 1)
}

// --- delivery -------------------------------------------------------------------

// queued emits one event and returns its single delivery.
func (f *fixture) queued(v ChannelView) db.NotificationDelivery {
	f.t.Helper()
	res, err := f.svc.emit(f.ctx, f.failedDeploy(nil, &f.svcA.ID, uuid.NewString()))
	require.NoError(f.t, err)
	require.GreaterOrEqual(f.t, res.Queued, 1)
	return f.deliveries(v.ID)[0]
}

func TestProcessDelivers(t *testing.T) {
	f := newFixture(t)
	url, c := hook(t, 200, "ok")
	v := f.slack("a", url, nil)
	d := f.queued(v)
	require.Equal(t, StatusPending, d.Status)

	out, err := f.svc.process(f.ctx, d.ID, 1, MaxAttempts)
	require.NoError(t, err)
	require.Equal(t, outcomeDone, out.kind)
	got := f.delivery(d.ID)
	require.Equal(t, StatusDelivered, got.Status)
	require.EqualValues(t, 1, got.Attempts)
	require.EqualValues(t, 200, *got.ResponseCode)
	require.Empty(t, got.LastError)
	require.NotNil(t, got.DeliveredAt)
	require.Len(t, c.all(), 1)
	// The message carries the link resolved against the public URL.
	require.Contains(t, string(c.all()[0].Body), "https://ctrl.example.com/p/"+f.projA.ID.String()+"/production?service="+f.svcA.ID.String())

	// Running it again (a River redelivery) sends nothing.
	out, err = f.svc.process(f.ctx, d.ID, 2, MaxAttempts)
	require.NoError(t, err)
	require.Equal(t, outcomeDone, out.kind)
	require.Len(t, c.all(), 1)
}

func TestRetryScheduleIsAboutAnHour(t *testing.T) {
	require.Equal(t, 6, MaxAttempts)
	var total time.Duration
	for attempt := 1; attempt < MaxAttempts; attempt++ {
		total += retryDelay(attempt)
	}
	require.Equal(t, 5, MaxAttempts-1)
	require.Equal(t, 57*time.Minute, total)
	require.Equal(t, time.Minute, retryDelay(1))
	require.Equal(t, 30*time.Minute, retryDelay(5))
	require.Equal(t, 30*time.Minute, retryDelay(9), "never indexes past the schedule")
	require.Equal(t, time.Minute, retryDelay(0))

	w := &deliverWorker{s: &Service{now: func() time.Time { return fixtureTime }}}
	job := func(attempt int) *river.Job[DeliverArgs] {
		return &river.Job[DeliverArgs]{JobRow: &rivertype.JobRow{Attempt: attempt}}
	}
	require.Equal(t, fixtureTime.Add(8*time.Minute), w.NextRetry(job(3)))
	require.Equal(t, fixtureTime.Add(30*time.Minute), w.NextRetry(job(5)))
}

func TestProcessRetriesThenGivesUp(t *testing.T) {
	f := newFixture(t)
	url, c := hook(t, 500, "boom")
	v := f.slack("a", url, nil)
	d := f.queued(v)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	f.svc.now = func() time.Time { return now }

	for attempt := 1; attempt < MaxAttempts; attempt++ {
		out, err := f.svc.process(f.ctx, d.ID, attempt, MaxAttempts)
		require.Error(t, err)
		require.Equal(t, outcomeRetry, out.kind, "attempt %d", attempt)
		got := f.delivery(d.ID)
		require.Equal(t, StatusRetrying, got.Status)
		require.EqualValues(t, attempt, got.Attempts)
		require.EqualValues(t, 500, *got.ResponseCode)
		require.Contains(t, got.LastError, "HTTP 500: boom")
		require.WithinDuration(t, now.Add(retryDelay(attempt)), *got.NextAttemptAt, time.Second)
	}
	out, err := f.svc.process(f.ctx, d.ID, MaxAttempts, MaxAttempts)
	require.Error(t, err)
	require.Equal(t, outcomeFailed, out.kind)
	got := f.delivery(d.ID)
	require.Equal(t, StatusFailed, got.Status)
	require.EqualValues(t, MaxAttempts, got.Attempts)
	require.Len(t, c.all(), MaxAttempts)

	// A failed delivery is final: no further sends.
	out, _ = f.svc.process(f.ctx, d.ID, MaxAttempts+1, MaxAttempts)
	require.Equal(t, outcomeDone, out.kind)
	require.Len(t, c.all(), MaxAttempts)
}

func TestProcessRecoversOnALaterAttempt(t *testing.T) {
	f := newFixture(t)
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)
	v := f.slack("a", srv.URL, nil)
	d := f.queued(v)
	for attempt := 1; attempt <= 3; attempt++ {
		_, _ = f.svc.process(f.ctx, d.ID, attempt, MaxAttempts)
	}
	got := f.delivery(d.ID)
	require.Equal(t, StatusDelivered, got.Status)
	require.EqualValues(t, 3, got.Attempts)
	require.Empty(t, got.LastError, "a success clears the error")
	require.Nil(t, got.NextAttemptAt)
}

func TestProcessPermanentFailureDoesNotRetry(t *testing.T) {
	f := newFixture(t)
	url, c := hook(t, 404, "no_service")
	v := f.slack("a", url, nil)
	d := f.queued(v)
	out, err := f.svc.process(f.ctx, d.ID, 1, MaxAttempts)
	require.Error(t, err)
	require.Equal(t, outcomeFailed, out.kind, "a 404 will not fix itself")
	got := f.delivery(d.ID)
	require.Equal(t, StatusFailed, got.Status)
	require.EqualValues(t, 1, got.Attempts)
	require.EqualValues(t, 404, *got.ResponseCode)
	require.Contains(t, got.LastError, "no_service")
	require.Len(t, c.all(), 1)
}

func TestProcessRateLimitSnoozesWithoutUsingAnAttempt(t *testing.T) {
	f := newFixture(t)
	f.svc.rateBurst, f.svc.rateEvery = 2, time.Hour
	url, c := hook(t, 200, "ok")
	v := f.slack("a", url, nil)
	for range 3 {
		f.queued(v)
	}
	all := f.deliveries(v.ID)
	require.Len(t, all, 3)
	sent, snoozed := 0, 0
	for _, d := range all {
		out, err := f.svc.process(f.ctx, d.ID, 1, MaxAttempts)
		require.NoError(t, err)
		switch out.kind {
		case outcomeDone:
			sent++
		case outcomeSnooze:
			snoozed++
			require.Greater(t, out.wait, time.Duration(0))
			got := f.delivery(d.ID)
			require.Equal(t, StatusPending, got.Status, "still queued")
			require.Zero(t, got.Attempts, "no attempt used")
		}
	}
	require.Equal(t, 2, sent)
	require.Equal(t, 1, snoozed)
	require.Len(t, c.all(), 2)

	// Limits are per channel: another channel is unaffected.
	url2, c2 := hook(t, 200, "ok")
	w := f.slack("b", url2, nil)
	d := f.queued(w)
	_, err := f.svc.process(f.ctx, d.ID, 1, MaxAttempts)
	require.NoError(t, err)
	require.Len(t, c2.all(), 1)
}

func TestProcessDisabledChannelFailsTheDelivery(t *testing.T) {
	f := newFixture(t)
	url, c := hook(t, 200, "ok")
	v := f.slack("a", url, nil)
	d := f.queued(v)
	_, err := f.svc.UpdateChannel(f.ctx, v.ID, ChannelPatch{Enabled: new(false)})
	require.NoError(t, err)
	out, _ := f.svc.process(f.ctx, d.ID, 1, MaxAttempts)
	require.Equal(t, outcomeFailed, out.kind)
	require.Equal(t, StatusFailed, f.delivery(d.ID).Status)
	require.Empty(t, c.all())
}

func TestSecretsNeverReachDeliveryLogsOrSlog(t *testing.T) {
	f := newFixture(t)
	const token = "T000/B000/SUPERSECRETTOKENVALUE"
	// The receiver echoes the secret path back in its error body.
	var path string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "internal error for "+r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	v := f.slack("a", srv.URL+"/services/"+token, nil)
	d := f.queued(v)
	_, err := f.svc.process(f.ctx, d.ID, 1, MaxAttempts)
	require.Error(t, err)
	require.Contains(t, path, "SUPERSECRETTOKENVALUE", "the receiver did see it")
	require.NotContains(t, err.Error(), "SUPERSECRETTOKENVALUE")

	// An unreachable receiver: the transport error would print the URL.
	srv.Close()
	d2 := f.queued(v)
	_, err = f.svc.process(f.ctx, d2.ID, 1, MaxAttempts)
	require.Error(t, err)

	for _, row := range f.deliveries(v.ID) {
		raw, _ := json.Marshal(row)
		require.NotContains(t, string(raw), "SUPERSECRETTOKENVALUE")
		require.NotContains(t, string(raw), "B000")
	}
	view, _ := f.svc.ListDeliveries(f.ctx, v.ID, "", "", 50)
	raw, _ := json.Marshal(view)
	require.NotContains(t, string(raw), "SUPERSECRETTOKENVALUE")
	require.NotContains(t, f.logs.String(), "SUPERSECRETTOKENVALUE")
	require.NotContains(t, f.logs.String(), "B000")

	// Nothing else in the database holds it in clear text.
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM notification_deliveries WHERE row_to_json(notification_deliveries)::text LIKE '%SUPERSECRET%')
		     + (SELECT count(*) FROM audit_events WHERE metadata::text LIKE '%SUPERSECRET%')
		     + (SELECT count(*) FROM river_job WHERE args::text LIKE '%SUPERSECRET%' OR errors::text LIKE '%SUPERSECRET%')`).Scan(&n))
	require.Zero(t, n)
}

func TestSMTPSecretNeverReachesDeliveryLog(t *testing.T) {
	f := newFixture(t)
	fake := newFakeSMTP(t, func(s *fakeSMTP) {
		s.starttls, s.auth, s.user, s.pass = true, []string{"PLAIN"}, "mailer", "right-password"
	})
	cfg := fake.emailConfig(TLSStartTLS)
	cfg.Username = "mailer"
	res, err := f.svc.CreateChannel(f.ctx, ChannelInput{Name: "mail", Kind: KindEmail, Config: cfg, Secrets: ChannelSecretsInput{Password: "wrong-password-xyz"},
		Rule: &ChannelRuleInput{EventTypes: []string{TypeDeployFailed}}})
	require.NoError(t, err)
	require.Equal(t, mask, res.Channel.Secrets["password"])
	d := f.queued(res.Channel)
	out, _ := f.svc.process(f.ctx, d.ID, 1, MaxAttempts)
	require.Equal(t, outcomeFailed, out.kind, "535 is permanent")
	got := f.delivery(d.ID)
	require.EqualValues(t, 535, *got.ResponseCode)
	require.NotContains(t, got.LastError, "wrong-password-xyz")
	require.NotContains(t, f.logs.String(), "wrong-password-xyz")
}

func TestEmailChannelDeliversThroughTheQueueLogic(t *testing.T) {
	f := newFixture(t)
	fake := newFakeSMTP(t, func(s *fakeSMTP) {
		s.starttls, s.auth, s.user, s.pass = true, []string{"PLAIN", "LOGIN"}, "mailer", "pw-123456"
	})
	cfg := fake.emailConfig(TLSStartTLS)
	cfg.Username = "mailer"
	res, err := f.svc.CreateChannel(f.ctx, ChannelInput{Name: "mail", Kind: KindEmail, Config: cfg, Secrets: ChannelSecretsInput{Password: "pw-123456"}})
	require.NoError(t, err)
	d := f.queued(res.Channel)
	_, err = f.svc.process(f.ctx, d.ID, 1, MaxAttempts)
	require.NoError(t, err)
	require.Equal(t, StatusDelivered, f.delivery(d.ID).Status)
	require.EqualValues(t, 250, *f.delivery(d.ID).ResponseCode)
	mails := fake.mails()
	require.Len(t, mails, 1)
	require.Contains(t, mails[0].Data, "Subject: [NebulaCtrl] api failed to deploy")
}

func TestTelegramChannelEndToEnd(t *testing.T) {
	f := newFixture(t)
	url, c := hook(t, 200, `{"ok":true}`)
	f.svc.send.telegramBase = url
	res, err := f.svc.CreateChannel(f.ctx, ChannelInput{Name: "tg", Kind: KindTelegram, Config: ChannelConfig{ChatID: "-100123"}, Secrets: ChannelSecretsInput{BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ"}})
	require.NoError(t, err)
	require.Equal(t, mask, res.Channel.Secrets["botToken"])
	d := f.queued(res.Channel)
	_, err = f.svc.process(f.ctx, d.ID, 1, MaxAttempts)
	require.NoError(t, err)
	require.Equal(t, "/bot123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ/sendMessage", c.all()[0].Path)
}

// --- test send, rotate, log ---------------------------------------------------------

func TestTestChannel(t *testing.T) {
	f := newFixture(t)
	url, c := hook(t, 200, "ok")
	v := f.slack("a", url, nil)

	d, err := f.svc.TestChannel(f.ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, StatusDelivered, d.Status)
	require.True(t, d.IsTest)
	require.Equal(t, 1, d.Attempts)
	require.Equal(t, 200, *d.ResponseCode)
	require.Contains(t, string(c.all()[0].Body), "Test notification from NebulaCtrl")
	require.Contains(t, string(c.all()[0].Body), "dev@example.com")

	// Not deduplicated: a second test sends again.
	_, err = f.svc.TestChannel(f.ctx, v.ID)
	require.NoError(t, err)
	require.Len(t, c.all(), 2)

	got, err := f.svc.GetChannel(f.ctx, v.ID)
	require.NoError(t, err)
	require.True(t, *got.Test.OK)
	require.NotNil(t, got.Test.At)
	require.Zero(t, got.Stats.Delivered, "test sends do not count as traffic")

	// Works on a disabled channel too.
	_, err = f.svc.UpdateChannel(f.ctx, v.ID, ChannelPatch{Enabled: new(false)})
	require.NoError(t, err)
	_, err = f.svc.TestChannel(f.ctx, v.ID)
	require.NoError(t, err)

	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action = $1", ActionChannelTest).Scan(&n))
	require.Equal(t, 3, n)
}

func TestTestChannelReportsFailure(t *testing.T) {
	f := newFixture(t)
	url, _ := hook(t, 403, "invalid_token")
	v := f.slack("a", url, nil)
	d, err := f.svc.TestChannel(f.ctx, v.ID)
	require.NoError(t, err, "a failing channel is a result, not an API error")
	require.Equal(t, StatusFailed, d.Status)
	require.Equal(t, 403, *d.ResponseCode)
	require.Contains(t, d.LastError, "invalid_token")
	got, err := f.svc.GetChannel(f.ctx, v.ID)
	require.NoError(t, err)
	require.False(t, *got.Test.OK)
	require.Contains(t, got.Test.Message, "invalid_token")
}

func TestRotateSecret(t *testing.T) {
	f := newFixture(t)
	url, c := hook(t, 200, "ok")
	created, err := f.svc.CreateChannel(f.ctx, ChannelInput{Name: "hook", Kind: KindWebhook, Secrets: ChannelSecretsInput{URL: url}})
	require.NoError(t, err)
	first := created.SigningSecret

	next, err := f.svc.RotateSecret(f.ctx, created.Channel.ID)
	require.NoError(t, err)
	require.NotEqual(t, first, next)

	_, err = f.svc.TestChannel(f.ctx, created.Channel.ID)
	require.NoError(t, err)
	req := c.all()[0]
	require.NoError(t, VerifySignature(next, req.Header.Get(SignatureHeader), req.Body, time.Now(), 0))
	require.Error(t, VerifySignature(first, req.Header.Get(SignatureHeader), req.Body, time.Now(), 0), "the old secret no longer verifies")

	chat := f.slack("chat", "https://hooks.slack.com/services/a/b/c", nil)
	_, err = f.svc.RotateSecret(f.ctx, chat.ID)
	require.Equal(t, "not_a_webhook", apiCode(t, err))
}

func TestListDeliveriesPagesAndFilters(t *testing.T) {
	f := newFixture(t)
	url, _ := hook(t, 200, "ok")
	v := f.slack("a", url, nil)
	other := f.slack("b", url, nil)
	for range 5 {
		_, err := f.svc.TestChannel(f.ctx, v.ID)
		require.NoError(t, err)
	}
	_, err := f.svc.TestChannel(f.ctx, other.ID)
	require.NoError(t, err)

	p1, err := f.svc.ListDeliveries(f.ctx, v.ID, "", "", 2)
	require.NoError(t, err)
	require.Len(t, p1.Items, 2)
	require.NotEmpty(t, p1.NextCursor)
	seen := map[uuid.UUID]bool{}
	for _, d := range p1.Items {
		seen[d.ID] = true
	}
	p2, err := f.svc.ListDeliveries(f.ctx, v.ID, "", p1.NextCursor, 10)
	require.NoError(t, err)
	require.Len(t, p2.Items, 3)
	require.Empty(t, p2.NextCursor)
	for _, d := range p2.Items {
		require.False(t, seen[d.ID], "no overlap between pages")
		require.Equal(t, v.ID, d.ChannelID, "another channel's log never leaks in")
	}
	require.True(t, p1.Items[0].CreatedAt.After(p2.Items[0].CreatedAt) || p1.Items[0].CreatedAt.Equal(p2.Items[0].CreatedAt))

	failed, err := f.svc.ListDeliveries(f.ctx, v.ID, StatusFailed, "", 10)
	require.NoError(t, err)
	require.Empty(t, failed.Items)
	_, err = f.svc.ListDeliveries(f.ctx, v.ID, "weird", "", 10)
	require.Equal(t, "validation_failed", apiCode(t, err))
	_, err = f.svc.ListDeliveries(f.ctx, uuid.New(), "", "", 10)
	require.Equal(t, "not_found", apiCode(t, err))
}

func TestChannelStatsCountLast24Hours(t *testing.T) {
	f := newFixture(t)
	ok, _ := hook(t, 200, "ok")
	v := f.slack("a", ok, nil)
	d1 := f.queued(v)
	_, err := f.svc.process(f.ctx, d1.ID, 1, MaxAttempts)
	require.NoError(t, err)
	bad, _ := hook(t, 404, "gone")
	_, err = f.svc.UpdateChannel(f.ctx, v.ID, ChannelPatch{Secrets: &ChannelSecretsInput{URL: bad}})
	require.NoError(t, err)
	d2 := f.queued(v)
	_, _ = f.svc.process(f.ctx, d2.ID, 1, MaxAttempts)
	got, err := f.svc.GetChannel(f.ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, 1, got.Stats.Delivered)
	require.Equal(t, 1, got.Stats.Failed)
	require.NotNil(t, got.Stats.LastDeliveryAt)
}

func TestPruneDropsOldDeliveries(t *testing.T) {
	f := newFixture(t)
	url, _ := hook(t, 200, "ok")
	v := f.slack("a", url, nil)
	for range 2 {
		_, err := f.svc.TestChannel(f.ctx, v.ID)
		require.NoError(t, err)
	}
	_, err := f.pool.Exec(context.Background(), "UPDATE notification_deliveries SET created_at = now() - interval '31 days' WHERE id = (SELECT id FROM notification_deliveries LIMIT 1)")
	require.NoError(t, err)
	require.NoError(t, (&pruneWorker{s: f.svc}).Work(context.Background(), nil))
	require.Len(t, f.deliveries(v.ID), 1)
}

// TestEmitDeliversThroughRiver runs the real worker: Emit queues, River sends.
func TestEmitDeliversThroughRiver(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.jm.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = f.jm.Stop(ctx)
	})
	ok, c := hook(t, 200, "ok")
	gone, g := hook(t, 404, "no_service")
	good := f.slack("good", ok, nil)
	dead := f.slack("dead", gone, nil)

	f.svc.Emit(f.ctx, f.failedDeploy(nil, &f.svcA.ID, "x"))
	require.Eventually(t, func() bool {
		return len(f.deliveries(good.ID)) == 1 && f.deliveries(good.ID)[0].Status == StatusDelivered &&
			len(f.deliveries(dead.ID)) == 1 && f.deliveries(dead.ID)[0].Status == StatusFailed
	}, 20*time.Second, 50*time.Millisecond)
	require.Len(t, c.all(), 1)
	require.Len(t, g.all(), 1, "a permanent failure is sent once, not retried")
	require.Eventually(t, func() bool {
		var state string
		err := f.pool.QueryRow(context.Background(), "SELECT state FROM river_job WHERE kind = 'notification_deliver' AND args->>'deliveryId' = $1", f.deliveries(dead.ID)[0].ID.String()).Scan(&state)
		return err == nil && state == "cancelled"
	}, 10*time.Second, 50*time.Millisecond)
}

func TestEventTypesCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Catalog() {
		require.False(t, seen[e.Type], "duplicate "+e.Type)
		seen[e.Type] = true
		require.NotEmpty(t, e.Label)
		require.NotEmpty(t, e.Group)
		require.NotEmpty(t, NormalizeSeverity(e.Severity))
	}
	// Exactly the types of the cross-unit contract.
	require.ElementsMatch(t, []string{
		"deploy.succeeded", "deploy.failed", "deploy.rolled_back", "server.offline", "server.online", "server.disk_alert", "server.cleanup", "server.reboot_required",
		"backup.failed", "backup.succeeded", "task.failed", "preview.created", "preview.deleted", "scale.blocked", "cert.expiring",
		"dns.sync_failed", "cloudflare.ips_changed", "cloudflare.ips_stale", "maintenance.on", "maintenance.off",
		"database.replica.broken", "database.replica.lagging",
		"service.crashed", "service.unhealthy", "service.restart_loop", "service.recovered", "backup.missed",
		"storage.quota_warning", "storage.quota_full",
		"uptime.down", "uptime.up",
	}, allTypes())
	require.Equal(t, SeverityError, NormalizeSeverity("critical"))
	require.Equal(t, SeverityWarning, NormalizeSeverity(" WARN "))
	require.Equal(t, "", NormalizeSeverity("loud"))
}

// insecureClient talks to httptest TLS servers (slack and discord URLs must be
// https) with the production dialer guard and redirect policy.
func insecureClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:     guardedDialer().DialContext,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test servers
		},
		Timeout:       httpTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
