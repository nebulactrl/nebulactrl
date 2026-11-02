package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

func (f *fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func requireCode(t *testing.T, err error, status int, code string) {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "want an API error, got %v", err)
	require.Equal(t, status, ae.Status, ae.Message)
	require.Equal(t, code, ae.Code, ae.Message)
}

func syslogDrain(name string) DrainInput {
	return DrainInput{Name: name, Kind: agentprotocol.DrainSyslog, Config: DrainConfig{Syslog: &agentprotocol.SyslogDrainConfig{Address: "logs.example.com:514", Protocol: "tcp"}}}
}

func TestDrainValidationRefusesBadInputAndStoresNothing(t *testing.T) {
	f := newFixture(t)
	no := false
	cases := []struct {
		name string
		mod  func(*DrainInput)
	}{
		{"blank name", func(in *DrainInput) { in.Name = "   " }},
		{"name too long", func(in *DrainInput) { in.Name = strings.Repeat("n", 81) }},
		{"unknown kind", func(in *DrainInput) { in.Kind = "splunk" }},
		{"loki without settings", func(in *DrainInput) { in.Config = DrainConfig{} }},
		{"loki with plain http url", func(in *DrainInput) { in.Config.Loki.URL = "ftp://loki.example.com" }},
		{"username without password", func(in *DrainInput) { in.Secrets = LogDrainSecretsInput{Username: "u"} }},
		{"password without username", func(in *DrainInput) { in.Secrets = LogDrainSecretsInput{Password: "p"} }},
		{"clear of something unknown", func(in *DrainInput) { in.Secrets = LogDrainSecretsInput{Clear: []string{"cookie"}} }},
		{"invalid header name", func(in *DrainInput) { in.Secrets = LogDrainSecretsInput{Headers: map[string]string{"bad header": "v"}} }},
		{"empty header name", func(in *DrainInput) { in.Secrets = LogDrainSecretsInput{Headers: map[string]string{"": "v"}} }},
		{"reserved header", func(in *DrainInput) {
			in.Secrets = LogDrainSecretsInput{Headers: map[string]string{"Content-Type": "x"}}
		}},
		{"header value with a newline", func(in *DrainInput) { in.Secrets = LogDrainSecretsInput{Headers: map[string]string{"X-A": "a\r\nb"}} }},
		{"header value too long", func(in *DrainInput) {
			in.Secrets = LogDrainSecretsInput{Headers: map[string]string{"X-A": strings.Repeat("v", 4097)}}
		}},
		{"too many headers", func(in *DrainInput) {
			h := map[string]string{}
			for i := range 21 {
				h["X-H"+string(rune('a'+i))] = "v"
			}
			in.Secrets = LogDrainSecretsInput{Headers: h}
		}},
		{"no project chosen", func(in *DrainInput) { in.AllProjects = &no }},
		{"project that does not exist", func(in *DrainInput) { in.AllProjects, in.ProjectIDs = &no, []uuid.UUID{uuid.New()} }},
		{"service that does not exist", func(in *DrainInput) { in.ServiceIDs = []uuid.UUID{uuid.New()} }},
		{"query too long", func(in *DrainInput) { in.Query = strings.Repeat("a", 1001) }},
		{"query that does not compile", func(in *DrainInput) { in.Query = "level:" }},
		{"buffer too small", func(in *DrainInput) { in.BufferMB = 1 }},
		{"buffer too large", func(in *DrainInput) { in.BufferMB = MaxBufferMB + 1 }},
	}
	for _, c := range cases {
		in := lokiDrain("d")
		cfg := *in.Config.Loki
		in.Config.Loki = &cfg
		c.mod(&in)
		_, err := f.svc.CreateDrain(f.ctx, in)
		ae, ok := apierr.As(err)
		require.True(t, ok, "%s: %v", c.name, err)
		require.Equal(t, http.StatusUnprocessableEntity, ae.Status, "%s: %s", c.name, ae.Message)
	}
	require.Zero(t, f.count(`SELECT count(*) FROM log_drains WHERE NOT builtin`), "refused drains are not stored")

	// Syslog has no credentials and no other kind's settings.
	sys := syslogDrain("sys")
	sys.Secrets = LogDrainSecretsInput{Token: "t"}
	_, err := f.svc.CreateDrain(f.ctx, sys)
	requireCode(t, err, 422, "validation_failed")
	_, err = f.svc.CreateDrain(f.ctx, syslogDrain("sys"))
	require.NoError(t, err)
	otlp := DrainInput{Name: "otel", Kind: agentprotocol.DrainOTLP, Config: DrainConfig{Loki: lokiDrain("x").Config.Loki}}
	_, err = f.svc.CreateDrain(f.ctx, otlp)
	requireCode(t, err, 422, "validation_failed")
	http1 := DrainInput{Name: "intake", Kind: agentprotocol.DrainHTTP}
	_, err = f.svc.CreateDrain(f.ctx, http1)
	requireCode(t, err, 422, "validation_failed")

	// Names are unique per team; limits themselves are allowed.
	_, err = f.svc.CreateDrain(f.ctx, syslogDrain("sys"))
	requireCode(t, err, 409, "drain_conflict")
	ok := lokiDrain("limits")
	ok.BufferMB = MinBufferMB
	ok.Query = `level:error -service:web`
	v, err := f.svc.CreateDrain(f.ctx, ok)
	require.NoError(t, err)
	require.Equal(t, MinBufferMB, v.BufferMB)
	require.Equal(t, `level:error -service:web`, v.Query)
	require.True(t, v.Enabled)
	require.True(t, v.AllProjects)
	require.Empty(t, v.ProjectIDs)
	require.NotNil(t, v.ServiceIDs)

	// Only admins manage drains, and tokens must hold the admin scope.
	_, err = f.svc.CreateDrain(f.tokenCtx([]string{policy.ScopeLogsRead}), lokiDrain("nope"))
	requireCode(t, err, 403, "forbidden")
	_, err = f.svc.ListDrains(f.tokenCtx([]string{policy.ScopeLogsRead}))
	requireCode(t, err, 403, "forbidden")
}

func TestDrainScopeAndHeaders(t *testing.T) {
	f := newFixture(t)
	no := false
	in := lokiDrain("scoped")
	in.AllProjects = &no
	in.ProjectIDs = []uuid.UUID{f.projA.ID, f.projA.ID}
	in.ServiceIDs = []uuid.UUID{f.web.ID, f.web.ID}
	in.Enabled = &no
	in.Secrets = LogDrainSecretsInput{Headers: map[string]string{"X-Keep": "v", "X-Dropped": ""}, Username: "u", Password: "p"}
	v, err := f.svc.CreateDrain(f.ctx, in)
	require.NoError(t, err)
	require.False(t, v.Enabled)
	require.False(t, v.AllProjects)
	require.Equal(t, []uuid.UUID{f.projA.ID}, v.ProjectIDs, "duplicates collapse")
	require.Equal(t, []uuid.UUID{f.web.ID}, v.ServiceIDs)
	require.Equal(t, []string{"X-Keep"}, v.HeaderNames, "a header without a value is not stored")

	// Changing the scope: all projects, or another project; sealed credentials survive.
	yes := true
	v, err = f.svc.UpdateDrain(f.ctx, v.ID, DrainPatch{AllProjects: &yes})
	require.NoError(t, err)
	require.True(t, v.AllProjects)
	require.Empty(t, v.ProjectIDs)
	ids := []uuid.UUID{f.projB.ID}
	v, err = f.svc.UpdateDrain(f.ctx, v.ID, DrainPatch{AllProjects: &no, ProjectIDs: &ids, ServiceIDs: &[]uuid.UUID{}})
	require.NoError(t, err)
	require.Equal(t, ids, v.ProjectIDs)
	require.Empty(t, v.ServiceIDs)
	require.Equal(t, mask, v.Secrets["password"])
	_, err = f.svc.UpdateDrain(f.ctx, v.ID, DrainPatch{ServiceIDs: &[]uuid.UUID{uuid.New()}})
	requireCode(t, err, 422, "validation_failed")
	other := uuid.New()
	_, err = f.svc.UpdateDrain(f.ctx, v.ID, DrainPatch{ProjectIDs: &[]uuid.UUID{other}})
	requireCode(t, err, 422, "validation_failed")

	// The query and buffer.
	q, mb := "level:warn", 64
	v, err = f.svc.UpdateDrain(f.ctx, v.ID, DrainPatch{Query: &q, BufferMB: &mb})
	require.NoError(t, err)
	require.Equal(t, "level:warn", v.Query)
	require.Equal(t, 64, v.BufferMB)
	bad, tooBig := "level:", MaxBufferMB+1
	_, err = f.svc.UpdateDrain(f.ctx, v.ID, DrainPatch{Query: &bad})
	requireCode(t, err, 422, "validation_failed")
	_, err = f.svc.UpdateDrain(f.ctx, v.ID, DrainPatch{BufferMB: &tooBig})
	requireCode(t, err, 422, "validation_failed")
}

func TestUpdateDrainSecretsAndAudit(t *testing.T) {
	f := newFixture(t)
	v, err := f.svc.CreateDrain(f.ctx, lokiDrain("grafana"))
	require.NoError(t, err)
	secretsChanged := func() string {
		var s string
		require.NoError(t, f.pool.QueryRow(context.Background(),
			`SELECT metadata->>'secretsChanged' FROM audit_events WHERE action = $1 AND resource_id = $2 ORDER BY created_at DESC, id DESC LIMIT 1`, ActionDrainUpdate, v.ID.String()).Scan(&s))
		return s
	}

	// The same credentials again are not a change; a new password is.
	_, err = f.svc.UpdateDrain(f.ctx, v.ID, DrainPatch{Secrets: &LogDrainSecretsInput{Password: "pw-very-secret-1"}})
	require.NoError(t, err)
	require.Equal(t, "false", secretsChanged())
	_, err = f.svc.UpdateDrain(f.ctx, v.ID, DrainPatch{Secrets: &LogDrainSecretsInput{Password: "rotated-password-3"}})
	require.NoError(t, err)
	require.Equal(t, "true", secretsChanged())
	_, secrets, err := f.svc.LogsConfigFor(f.ctx, f.srv1.ID)
	require.NoError(t, err)
	require.Equal(t, "rotated-password-3", secrets[agentprotocol.LogDrainSecretKey(v.ID.String(), "password")])
	require.Zero(t, f.count(`SELECT count(*) FROM audit_events WHERE metadata::text LIKE '%rotated-password-3%'`), "values are never audited")

	// Clearing the username alone leaves a password without a user: refused.
	_, err = f.svc.UpdateDrain(f.ctx, v.ID, DrainPatch{Secrets: &LogDrainSecretsInput{Clear: []string{"username"}}})
	requireCode(t, err, 422, "validation_failed")
	// Clearing both removes basic auth; the headers go with "headers".
	v, err = f.svc.UpdateDrain(f.ctx, v.ID, DrainPatch{Secrets: &LogDrainSecretsInput{Clear: []string{"username", "password", "headers"}, Token: "bearer-token-9"}})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"token": mask}, v.Secrets)
	require.Empty(t, v.HeaderNames)

	// Rename: conflicts and blanks.
	other, err := f.svc.CreateDrain(f.ctx, syslogDrain("other"))
	require.NoError(t, err)
	name := "grafana"
	_, err = f.svc.UpdateDrain(f.ctx, other.ID, DrainPatch{Name: &name})
	requireCode(t, err, 409, "drain_conflict")
	blank := " "
	_, err = f.svc.UpdateDrain(f.ctx, other.ID, DrainPatch{Name: &blank})
	requireCode(t, err, 422, "validation_failed")
	_, err = f.svc.UpdateDrain(f.ctx, uuid.New(), DrainPatch{Name: &name})
	requireCode(t, err, 404, "not_found")
	// A new config replaces the kind's settings only.
	cfg := DrainConfig{Syslog: &agentprotocol.SyslogDrainConfig{Address: "10.0.0.5:6514", Protocol: "tcp+tls"}}
	got, err := f.svc.UpdateDrain(f.ctx, other.ID, DrainPatch{Config: &cfg})
	require.NoError(t, err)
	require.Equal(t, "10.0.0.5:6514", got.Syslog.Address)
	require.Nil(t, got.Loki)
	// Not an admin, and a drain of another team is invisible.
	_, err = f.svc.UpdateDrain(f.tokenCtx([]string{policy.ScopeLogsRead}), other.ID, DrainPatch{})
	requireCode(t, err, 403, "forbidden")
	ws2, err := f.q.CreateWorkspace(context.Background(), "other team")
	require.NoError(t, err)
	p := *principalOf(f.ctx)
	p.WorkspaceID = ws2.ID
	foreign := auth.WithPrincipal(context.Background(), &p)
	_, err = f.svc.GetDrain(foreign, other.ID)
	requireCode(t, err, 404, "not_found")
	requireCode(t, f.svc.DeleteDrain(foreign, other.ID), 404, "not_found")
}

func principalOf(ctx context.Context) *auth.Principal {
	p, _ := auth.PrincipalFrom(ctx)
	return p
}

func TestBuiltinDrainCanOnlyBeSwitchedAndScoped(t *testing.T) {
	f := newFixture(t)
	builtin := f.builtinDrain()
	name := "Renamed"
	yes := true

	_, err := f.svc.UpdateDrain(f.ctx, builtin.ID, DrainPatch{Name: &name})
	requireCode(t, err, 409, "builtin_drain")
	_, err = f.svc.UpdateDrain(f.ctx, builtin.ID, DrainPatch{Config: &DrainConfig{Syslog: &agentprotocol.SyslogDrainConfig{Address: "x:1", Protocol: "udp"}}})
	requireCode(t, err, 409, "builtin_drain")
	_, err = f.svc.UpdateDrain(f.ctx, builtin.ID, DrainPatch{Secrets: &LogDrainSecretsInput{Token: "t"}})
	requireCode(t, err, 409, "builtin_drain")
	// A form that submits what is already there is fine, and so are the switch and the scope.
	same := builtin.Name
	v, err := f.svc.UpdateDrain(f.ctx, builtin.ID, DrainPatch{Name: &same, Enabled: &yes, Secrets: &LogDrainSecretsInput{}})
	require.NoError(t, err)
	require.True(t, v.Enabled)
	require.True(t, v.Builtin)
	requireCode(t, f.svc.DeleteDrain(f.ctx, builtin.ID), 409, "builtin_drain")
	require.Equal(t, 1, f.count(`SELECT count(*) FROM log_drains WHERE id = $1`, builtin.ID))
}

func TestDeleteDrainRemovesItAndAsksAgentsToResync(t *testing.T) {
	f := newFixture(t)
	f.agents.online[f.srv1.ID] = true
	v, err := f.svc.CreateDrain(f.ctx, lokiDrain("gone"))
	require.NoError(t, err)
	require.Contains(t, f.agents.resynced, f.srv1.ID, "creating a drain pushes the configuration to online servers")
	f.agents.resynced = nil

	require.NoError(t, f.svc.DeleteDrain(f.ctx, v.ID))
	require.Equal(t, []uuid.UUID{f.srv1.ID}, f.agents.resynced, "only connected servers are resynced")
	_, err = f.svc.GetDrain(f.ctx, v.ID)
	requireCode(t, err, 404, "not_found")
	requireCode(t, f.svc.DeleteDrain(f.ctx, v.ID), 404, "not_found")
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2`, ActionDrainDelete, v.ID.String()))
}

func TestTestDrainSendsFromAConnectedServer(t *testing.T) {
	f := newFixture(t)
	v, err := f.svc.CreateDrain(f.ctx, lokiDrain("grafana"))
	require.NoError(t, err)

	_, err = f.svc.TestDrain(f.ctx, v.ID, nil)
	requireCode(t, err, 409, "no_online_server")
	f.agents.online[f.srv2.ID] = true
	f.agents.handlers[f.srv2.ID] = func(kind string, payload json.RawMessage, secrets map[string]string) agentprotocol.ResultFrame {
		require.Equal(t, agentprotocol.KindLogsDrainTest, kind)
		require.Equal(t, "pw-very-secret-1", secrets[agentprotocol.LogDrainSecretKey(v.ID.String(), "password")])
		raw, _ := json.Marshal(agentprotocol.LogsDrainTestResult{OK: true, Message: "204 No Content", StatusCode: 204})
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: raw}
	}
	_, err = f.svc.TestDrain(f.ctx, v.ID, &f.srv1.ID)
	requireCode(t, err, 409, "server_offline")

	res, err := f.svc.TestDrain(f.ctx, v.ID, nil)
	require.NoError(t, err)
	require.True(t, res.OK)
	require.Equal(t, 204, res.StatusCode)
	require.Equal(t, "edge-2", res.ServerName)
	require.Equal(t, f.srv2.ID, res.ServerID)
	got, err := f.svc.GetDrain(f.ctx, v.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Test.OK)
	require.True(t, *got.Test.OK)
	require.Equal(t, "204 No Content", got.Test.Message)
	require.NotNil(t, got.Test.At)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action = $1`, ActionDrainTest))

	// A refused test is recorded as such, with the agent's reason (or a default one).
	f.agents.handlers[f.srv2.ID] = func(string, json.RawMessage, map[string]string) agentprotocol.ResultFrame {
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "unsupported command"}
	}
	res, err = f.svc.TestDrain(f.ctx, v.ID, &f.srv2.ID)
	require.NoError(t, err)
	require.False(t, res.OK)
	require.Equal(t, "unsupported command", res.Message)
	f.agents.handlers[f.srv2.ID] = func(string, json.RawMessage, map[string]string) agentprotocol.ResultFrame {
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed}
	}
	res, err = f.svc.TestDrain(f.ctx, v.ID, nil)
	require.NoError(t, err)
	require.Equal(t, "the agent could not run the request", res.Message)
	got, _ = f.svc.GetDrain(f.ctx, v.ID)
	require.False(t, *got.Test.OK)
	f.agents.handlers[f.srv2.ID] = func(string, json.RawMessage, map[string]string) agentprotocol.ResultFrame {
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: []byte("not json")}
	}
	_, err = f.svc.TestDrain(f.ctx, v.ID, nil)
	require.ErrorContains(t, err, "decode test result")

	_, err = f.svc.TestDrain(f.ctx, uuid.New(), nil)
	requireCode(t, err, 404, "not_found")
	_, err = f.svc.TestDrain(f.tokenCtx([]string{policy.ScopeLogsRead}), v.ID, nil)
	requireCode(t, err, 403, "forbidden")
}

func TestRequestErrorsBecomeAPIErrors(t *testing.T) {
	requireCode(t, mapRequestError(context.DeadlineExceeded), 504, "server_timeout")
	requireCode(t, mapRequestError(errors.Join(errors.New("x"), context.DeadlineExceeded)), 504, "server_timeout")
	requireCode(t, mapRequestError(apierr.New(503, "server_offline", "off")), 503, "server_offline")
	requireCode(t, mapRequestError(apierr.Conflict("busy", "busy")), 409, "busy")
	requireCode(t, mapRequestError(errors.New("connection reset")), 503, "server_offline")
}

func TestHeaderNames(t *testing.T) {
	for n, want := range map[string]bool{
		"X-Api-Key": true, "x_key": true, "A1": true, "": false, "bad header": false, "x:y": false, "é": false, strings.Repeat("a", 100): true, strings.Repeat("a", 101): false,
	} {
		require.Equal(t, want, validHeaderName(n), n)
	}
}

func TestLogsConfigForSkipsDrainsItCannotRead(t *testing.T) {
	f := newFixture(t)
	f.svc.sec = fakeSecrets{f.web.ID: {"hunter2-secret", "ab"}}
	good, err := f.svc.CreateDrain(f.ctx, lokiDrain("good"))
	require.NoError(t, err)
	broken, err := f.svc.CreateDrain(f.ctx, lokiDrain("broken-config"))
	require.NoError(t, err)
	locked, err := f.svc.CreateDrain(f.ctx, lokiDrain("broken-secrets"))
	require.NoError(t, err)
	f.exec(`UPDATE log_drains SET config = '"not an object"'::jsonb WHERE id = $1`, broken.ID)
	f.exec(`UPDATE log_drains SET secrets_enc = '\x0102'::bytea WHERE id = $1`, locked.ID)

	cfg, secrets, err := f.svc.LogsConfigFor(f.ctx, f.srv1.ID)
	require.NoError(t, err)
	ids := []string{}
	for _, d := range cfg.Drains {
		ids = append(ids, d.ID)
	}
	require.Contains(t, ids, good.ID.String())
	require.NotContains(t, ids, broken.ID.String(), "a drain whose settings cannot be read is left out, not fatal")
	require.NotContains(t, ids, locked.ID.String(), "so is one whose credentials cannot be opened")
	// Secret values of the services on this server go along for masking (short ones are not worth masking).
	key := agentprotocol.LogRedactSecretKey(f.web.ID.String())
	require.Contains(t, secrets, key)
	require.JSONEq(t, `["hunter2-secret"]`, secrets[key])
	require.Equal(t, f.srv1.ID.String() != "", true)
	require.Equal(t, "edge-1", cfg.ServerName)

	// With every drain off nothing is masked and no credentials leave.
	f.exec(`UPDATE log_drains SET enabled = false`)
	cfg, secrets, err = f.svc.LogsConfigFor(f.ctx, f.srv1.ID)
	require.NoError(t, err)
	require.Empty(t, secrets)
	for _, d := range cfg.Drains {
		require.False(t, d.Enabled)
	}
	_, _, err = f.svc.LogsConfigFor(f.ctx, uuid.New())
	require.ErrorContains(t, err, "load server")
}

func TestHandleLogStatusStoresUsageAndDrainHealth(t *testing.T) {
	f := newFixture(t)
	d1, err := f.svc.CreateDrain(f.ctx, lokiDrain("one"))
	require.NoError(t, err)
	d2, err := f.svc.CreateDrain(f.ctx, syslogDrain("two"))
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	long := strings.Repeat("e", 600)

	require.NoError(t, f.svc.HandleLogStatus(context.Background(), f.srv1.ID, agentprotocol.LogStatus{
		TotalBytes: 1234,
		Services:   []agentprotocol.LogServiceUsage{{ServiceID: f.web.ID.String(), Bytes: 1000}},
		Drains: []agentprotocol.LogDrainStatus{
			{ID: d1.ID.String(), State: "failing", LastError: long, LastErrorAt: &now, SentLines: 10, DroppedLines: 2, BacklogBytes: 99},
			{ID: d2.ID.String(), State: "ok", LastSuccessAt: &now, SentLines: 5},
			{ID: "not-a-uuid", State: "ok"},
		},
	}))
	list, err := f.svc.ListDrains(f.ctx)
	require.NoError(t, err)
	by := map[uuid.UUID]LogDrainView{}
	for _, d := range list {
		by[d.ID] = d
	}
	require.Len(t, by[d1.ID].Servers, 1)
	st := by[d1.ID].Servers[0]
	require.Equal(t, "failing", st.State)
	require.Len(t, st.LastError, 500, "error messages are cut")
	require.EqualValues(t, 10, st.SentLines)
	require.EqualValues(t, 2, st.DroppedLines)
	require.EqualValues(t, 99, st.BacklogBytes)
	require.Equal(t, "edge-1", st.ServerName)
	require.Equal(t, "ok", by[d2.ID].Servers[0].State)

	settings, err := f.svc.GetSettings(f.ctx, f.srv1.ID)
	require.NoError(t, err)
	require.NotNil(t, settings.Usage)
	require.EqualValues(t, 1234, settings.Usage.TotalBytes)
	require.Len(t, settings.Usage.Services, 1)

	// A later report without a drain drops that drain's stale health.
	require.NoError(t, f.svc.HandleLogStatus(context.Background(), f.srv1.ID, agentprotocol.LogStatus{
		Drains: []agentprotocol.LogDrainStatus{{ID: d2.ID.String(), State: "ok"}},
	}))
	got, err := f.svc.GetDrain(f.ctx, d1.ID)
	require.NoError(t, err)
	require.Empty(t, got.Servers)
	got, err = f.svc.GetDrain(f.ctx, d2.ID)
	require.NoError(t, err)
	require.Len(t, got.Servers, 1)
	require.Equal(t, "xx", truncate("xxxx", 2))
	require.Equal(t, "xx", truncate("xx", 2))
}

func TestServerLogSettingsValidationAndPush(t *testing.T) {
	f := newFixture(t)
	const mb = int64(1) << 20
	in := SettingsInput{Capture: true, RetentionDays: 14, MaxServiceBytes: 64 * mb, MaxServerBytes: 256 * mb}

	// Defaults before anything is set.
	v, err := f.svc.GetSettings(f.ctx, f.srv1.ID)
	require.NoError(t, err)
	require.Equal(t, 7, v.RetentionDays)
	require.True(t, v.Capture)
	require.False(t, v.Online)
	require.Nil(t, v.Usage)

	for name, mod := range map[string]func(*SettingsInput){
		"zero retention":        func(i *SettingsInput) { i.RetentionDays = 0 },
		"retention too long":    func(i *SettingsInput) { i.RetentionDays = agentprotocol.MaxLogRetentionDays + 1 },
		"service cap too small": func(i *SettingsInput) { i.MaxServiceBytes = agentprotocol.MinLogMaxBytes - 1 },
		"server cap too small":  func(i *SettingsInput) { i.MaxServerBytes = agentprotocol.MinLogMaxBytes - 1 },
		"service above server":  func(i *SettingsInput) { i.MaxServiceBytes, i.MaxServerBytes = 512*mb, 256*mb },
	} {
		bad := in
		mod(&bad)
		_, err := f.svc.UpdateSettings(f.ctx, f.srv1.ID, bad)
		requireCode(t, err, 422, "validation_failed")
		_ = name
	}
	f.agents.online[f.srv1.ID] = true
	v, err = f.svc.UpdateSettings(f.ctx, f.srv1.ID, in)
	require.NoError(t, err)
	require.Equal(t, 14, v.RetentionDays)
	require.Equal(t, 64*mb, v.MaxServiceBytes)
	require.True(t, v.Online)
	require.Equal(t, []uuid.UUID{f.srv1.ID}, f.agents.resynced, "the agent applies the change at once")
	in.Capture = false
	v, err = f.svc.UpdateSettings(f.ctx, f.srv1.ID, in)
	require.NoError(t, err)
	require.False(t, v.Capture)
	require.Equal(t, 2, f.count(`SELECT count(*) FROM audit_events WHERE action = $1`, ActionSettingsUpdate))

	_, err = f.svc.GetSettings(f.ctx, uuid.New())
	requireCode(t, err, 404, "not_found")
	_, err = f.svc.UpdateSettings(f.tokenCtx([]string{policy.ScopeServersRead}), f.srv1.ID, in)
	requireCode(t, err, 403, "forbidden")
	_, err = f.svc.GetSettings(f.tokenCtx([]string{policy.ScopeLogsRead}), f.srv1.ID)
	requireCode(t, err, 403, "forbidden")
	ws2, err := f.q.CreateWorkspace(context.Background(), "other")
	require.NoError(t, err)
	p := *principalOf(f.ctx)
	p.WorkspaceID = ws2.ID
	_, err = f.svc.GetSettings(auth.WithPrincipal(context.Background(), &p), f.srv1.ID)
	requireCode(t, err, 404, "not_found")
}

// ---- HTTP ----

func (f *fixture) serve() func(p *auth.Principal, method, path string, body any) (int, http.Header, []byte) {
	r := chi.NewRouter()
	var cur *auth.Principal
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if cur != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), cur))
			}
			next.ServeHTTP(w, req)
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	Register(humachi.New(r, cfg), f.svc)
	return func(p *auth.Principal, method, path string, body any) (int, http.Header, []byte) {
		f.t.Helper()
		cur = p
		var rd *bytes.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			require.NoError(f.t, err)
			rd = bytes.NewReader(raw)
		} else {
			rd = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rd)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code, rec.Header(), rec.Body.Bytes()
	}
}

func asMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m), string(raw))
	return m
}

func TestDrainAndSettingsEndpoints(t *testing.T) {
	f := newFixture(t)
	do := f.serve()
	o := principalOf(f.ctx)

	code, _, raw := do(o, http.MethodPost, "/log-drains", map[string]any{
		"name": "grafana", "kind": "loki", "config": map[string]any{"loki": map[string]any{"url": "https://loki.example.com/loki/api/v1/push", "tenantId": "t"}},
		"secrets": map[string]any{"token": "tok-very-secret-77"}, "bufferMb": 32,
	})
	require.Equal(t, http.StatusCreated, code, string(raw))
	d := asMap(t, raw)
	id := d["id"].(string)
	require.Equal(t, true, d["enabled"])
	require.Equal(t, float64(32), d["bufferMb"])
	require.NotContains(t, string(raw), "tok-very-secret-77")
	require.Equal(t, map[string]any{"token": mask}, d["secrets"])
	code, _, raw = do(o, http.MethodPost, "/log-drains", map[string]any{"name": "grafana", "kind": "syslog", "config": map[string]any{"syslog": map[string]any{"address": "a:514", "protocol": "udp"}}})
	require.Equal(t, http.StatusConflict, code, string(raw))
	require.Equal(t, "drain_conflict", asMap(t, raw)["code"])
	code, _, raw = do(o, http.MethodPost, "/log-drains", map[string]any{"name": "x", "kind": "syslog", "config": map[string]any{}})
	require.Equal(t, http.StatusUnprocessableEntity, code, string(raw))

	code, _, raw = do(o, http.MethodGet, "/log-drains", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Len(t, asMap(t, raw)["items"], 1)
	code, _, raw = do(o, http.MethodGet, "/log-drains/"+id, nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	code, _, _ = do(o, http.MethodGet, "/log-drains/"+uuid.NewString(), nil)
	require.Equal(t, http.StatusNotFound, code)

	code, _, raw = do(o, http.MethodPatch, "/log-drains/"+id, map[string]any{
		"enabled": false, "name": "grafana cloud", "config": map[string]any{"loki": map[string]any{"url": "https://loki2.example.com/loki/api/v1/push"}},
		"allProjects": false, "projectIds": []string{f.projA.ID.String()}, "serviceIds": []string{f.web.ID.String()}, "query": "level:error", "bufferMb": 64,
		"secrets": map[string]any{"clear": []string{"token"}},
	})
	require.Equal(t, http.StatusOK, code, string(raw))
	d = asMap(t, raw)
	require.Equal(t, false, d["enabled"])
	require.Equal(t, "grafana cloud", d["name"])
	require.Equal(t, "https://loki2.example.com/loki/api/v1/push", d["loki"].(map[string]any)["url"])
	require.Equal(t, false, d["allProjects"])
	require.Equal(t, []any{f.projA.ID.String()}, d["projectIds"])
	require.Equal(t, "level:error", d["query"])
	require.Empty(t, d["secrets"])

	// Testing needs a connected server; the answer carries the agent's verdict.
	code, _, raw = do(o, http.MethodPost, "/log-drains/"+id+"/test", map[string]any{})
	require.Equal(t, http.StatusConflict, code, string(raw))
	require.Equal(t, "no_online_server", asMap(t, raw)["code"])
	f.agents.online[f.srv1.ID] = true
	f.agents.handlers[f.srv1.ID] = func(string, json.RawMessage, map[string]string) agentprotocol.ResultFrame {
		data, _ := json.Marshal(agentprotocol.LogsDrainTestResult{OK: true, Message: "accepted", StatusCode: 204})
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: data}
	}
	code, _, raw = do(o, http.MethodPost, "/log-drains/"+id+"/test", map[string]any{"serverId": f.srv1.ID.String()})
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Equal(t, true, asMap(t, raw)["ok"])
	require.Equal(t, "edge-1", asMap(t, raw)["serverName"])

	// Settings of a server.
	code, _, raw = do(o, http.MethodGet, "/servers/"+f.srv1.ID.String()+"/log-settings", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Equal(t, float64(7), asMap(t, raw)["retentionDays"])
	code, _, raw = do(o, http.MethodPut, "/servers/"+f.srv1.ID.String()+"/log-settings", map[string]any{
		"capture": true, "retentionDays": 30, "maxServiceBytes": 100 << 20, "maxServerBytes": 1 << 30,
	})
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Equal(t, float64(30), asMap(t, raw)["retentionDays"])
	require.Equal(t, true, asMap(t, raw)["online"])
	code, _, raw = do(o, http.MethodPut, "/servers/"+f.srv1.ID.String()+"/log-settings", map[string]any{
		"capture": true, "retentionDays": 30, "maxServiceBytes": 1 << 30, "maxServerBytes": 100 << 20,
	})
	require.Equal(t, http.StatusUnprocessableEntity, code, string(raw))
	code, _, _ = do(o, http.MethodGet, "/servers/"+uuid.NewString()+"/log-settings", nil)
	require.Equal(t, http.StatusNotFound, code)

	code, _, _ = do(o, http.MethodDelete, "/log-drains/"+id, nil)
	require.Equal(t, http.StatusNoContent, code)
	code, _, _ = do(o, http.MethodDelete, "/log-drains/"+id, nil)
	require.Equal(t, http.StatusNotFound, code)

	// Signed out, and drains need the admin scope.
	code, _, _ = do(nil, http.MethodGet, "/log-drains", nil)
	require.Equal(t, http.StatusUnauthorized, code)
	ro := *o
	ro.Scopes = []string{policy.ScopeLogsRead}
	code, _, _ = do(&ro, http.MethodGet, "/log-drains", nil)
	require.Equal(t, http.StatusForbidden, code)
}
