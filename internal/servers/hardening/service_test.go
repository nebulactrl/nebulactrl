package hardening

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/lynis"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var bg = context.Background()

func TestSupportsHardening(t *testing.T) {
	require.True(t, SupportsHardening(nil), "no facts yet: assume a current agent")
	old, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapTunnel}})
	require.False(t, SupportsHardening(old))
	cur, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapHostHardening}})
	require.True(t, SupportsHardening(cur))
	require.True(t, Handles(agentprotocol.KindHostHardening))
	require.False(t, Handles(agentprotocol.KindDeploy))
}

type fakeDisp struct {
	mu   sync.Mutex
	cmds []agents.NewCommand
}

func (d *fakeDisp) Enqueue(ctx context.Context, tx pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cmds = append(d.cmds, c)
	id := uuid.New()
	_, err := tx.Exec(ctx, `INSERT INTO agent_commands (id, server_id, kind, payload, idempotency_key, expires_at) VALUES ($1,$2,$3,$4,$5, now() + interval '15 minutes')`,
		id, c.ServerID, c.Kind, c.Payload, c.IdempotencyKey)
	return id, err
}

func (d *fakeDisp) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

func (d *fakeDisp) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.cmds)
}

func (d *fakeDisp) last(t *testing.T) agentprotocol.HardeningPayload {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	require.NotEmpty(t, d.cmds)
	c := d.cmds[len(d.cmds)-1]
	require.Equal(t, agentprotocol.KindHostHardening, c.Kind)
	var p agentprotocol.HardeningPayload
	require.NoError(t, json.Unmarshal(c.Payload, &p))
	return p
}

type fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	ws     uuid.UUID
	server db.Server
	svc    *Service
	disp   *fakeDisp
	owner  context.Context
	member context.Context
	viewer context.Context
}

func principal(ws, user uuid.UUID, role string, scopes []string) context.Context {
	return auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: user, WorkspaceID: ws, Role: role, Label: "t", Scopes: scopes})
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "Agent 1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online', created_at = now() - interval '10 days' WHERE id = $1", srv.ID)
	require.NoError(t, err)
	srv, err = q.GetServerByID(bg, srv.ID)
	require.NoError(t, err)
	disp := &fakeDisp{}
	return &fixture{
		t: t, pool: pool, q: q, ws: ws.ID, server: srv, disp: disp,
		svc:    NewService(Deps{Pool: pool, Bus: events.NewBus(), Dispatcher: disp}),
		owner:  principal(ws.ID, u.ID, auth.RoleOwner, policy.AllScopes),
		member: principal(ws.ID, u.ID, auth.RoleDeveloper, policy.AllScopes),
		viewer: principal(ws.ID, u.ID, auth.RoleViewer, []string{policy.ScopeProjectsRead, policy.ScopeServersRead}),
	}
}

func apiCode(t *testing.T, err error) string {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	return ae.Code
}

func (f *fixture) result(runID uuid.UUID, res agentprotocol.HardeningResult) {
	f.t.Helper()
	run := f.run(runID)
	data, err := json.Marshal(res)
	require.NoError(f.t, err)
	require.NoError(f.t, f.svc.HandleResult(bg, f.server.ID, *run.CommandID, agentprotocol.ResultFrame{
		CommandID: run.CommandID.String(), Outcome: agentprotocol.OutcomeSucceeded, Data: data,
	}))
}

func (f *fixture) run(id uuid.UUID) db.ServerHardeningRun {
	f.t.Helper()
	runs, err := f.q.ListServerHardeningRuns(bg, db.ListServerHardeningRunsParams{ServerID: f.server.ID, Limit: 50})
	require.NoError(f.t, err)
	for _, r := range runs {
		if r.ID == id {
			return r
		}
	}
	f.t.Fatalf("run %s not found", id)
	return db.ServerHardeningRun{}
}

func report(index int) *agentprotocol.HardeningReport {
	return &agentprotocol.HardeningReport{
		LynisVersion: "3.1.7", HardeningIndex: index, TestsDone: 238,
		Warnings: []agentprotocol.HardeningFinding{{ID: "PKGS-7392", Text: "Found one or more vulnerable packages."}},
		Suggestions: []agentprotocol.HardeningFinding{
			{ID: "SSH-7408", Text: "Consider hardening SSH configuration", Fields: []agentprotocol.HardeningDetail{{Field: "MaxAuthTries", Preferred: "3", Value: "6"}}},
			{ID: "KRNL-6000", Text: "sysctl", Fields: []agentprotocol.HardeningDetail{
				{Field: "kernel.kptr_restrict", Preferred: "2", Value: "0"},
				{Field: "net.ipv4.conf.all.forwarding", Preferred: "0", Value: "1"},
			}},
		},
	}
}

func fixStates(state string) []agentprotocol.HardeningFixState {
	var out []agentprotocol.HardeningFixState
	for _, f := range lynis.Catalog() {
		out = append(out, agentprotocol.HardeningFixState{ID: f.ID, State: state})
	}
	return out
}

func TestAuditStoresReportAndViewExplainsWhatIsLeft(t *testing.T) {
	f := newFixture(t)

	// Nothing yet.
	v, err := f.svc.Get(f.owner, f.server.ID)
	require.NoError(t, err)
	require.Nil(t, v.HardeningIndex)
	require.True(t, v.Weekly)
	require.Len(t, v.Fixes, len(lynis.Catalog()))
	require.Equal(t, stateUnknown, v.Fixes[0].State)

	run, err := f.svc.Audit(f.owner, f.server.ID, true)
	require.NoError(t, err)
	require.Equal(t, "pending", run.Status)
	p := f.disp.last(t)
	require.Equal(t, agentprotocol.HardeningActionAudit, p.Action)
	require.True(t, p.InstallLynis)
	require.Equal(t, run.ID.String(), p.RunID)
	v, _ = f.svc.Get(f.owner, f.server.ID)
	require.True(t, v.Running)

	// One run at a time.
	_, err = f.svc.Audit(f.owner, f.server.ID, false)
	require.Equal(t, "hardening_running", apiCode(t, err))

	f.result(run.ID, agentprotocol.HardeningResult{
		Action: "audit", Mode: "host", LynisInstalled: true, LynisVersion: "3.1.7", Report: report(62), Fixes: fixStates(agentprotocol.HardeningFixPending),
	})
	v, err = f.svc.Get(f.owner, f.server.ID)
	require.NoError(t, err)
	require.False(t, v.Running)
	require.NotNil(t, v.HardeningIndex)
	require.Equal(t, 62, *v.HardeningIndex)
	require.Equal(t, 62, *v.FirstIndex)
	require.Equal(t, "3.1.7", v.LynisVersion)
	require.Equal(t, "host", v.Mode)
	require.Equal(t, 1, v.Warnings)
	require.Equal(t, 2, v.Suggestions)
	require.Positive(t, v.PendingSafe)
	require.Len(t, v.Runs, 1)
	require.Equal(t, "succeeded", v.Runs[0].Status)

	byTitle := map[string]HardeningRemaining{}
	for _, r := range v.Remaining {
		byTitle[r.Title[:min(len(r.Title), 24)]] = r
	}
	var kptr, fwd, ssh *HardeningRemaining
	for i := range v.Remaining {
		r := &v.Remaining[i]
		switch {
		case r.TestID == "KRNL-6000" && r.FixID != "":
			kptr = r
		case r.TestID == "KRNL-6000":
			fwd = r
		case r.TestID == "SSH-7408":
			ssh = r
		}
	}
	require.NotNil(t, kptr)
	require.Equal(t, "sysctl-kernel", kptr.FixID)
	require.Equal(t, agentprotocol.HardeningFixPending, kptr.FixState)
	require.NotNil(t, fwd)
	require.NotEmpty(t, fwd.Never, "Docker needs forwarding: never offered")
	require.NotNil(t, ssh)
	require.Contains(t, ssh.Never, "never changes SSH")
	require.Equal(t, "https://cisofy.com/lynis/controls/SSH-7408/", ssh.Link)

	// A second audit moves the index but not the first one.
	run2, err := f.svc.Audit(f.owner, f.server.ID, false)
	require.NoError(t, err)
	require.Equal(t, 62, *run2.IndexBefore)
	f.result(run2.ID, agentprotocol.HardeningResult{Action: "audit", Mode: "host", LynisInstalled: true, Report: report(71), Fixes: fixStates(agentprotocol.HardeningFixApplied)})
	v, _ = f.svc.Get(f.owner, f.server.ID)
	require.Equal(t, 71, *v.HardeningIndex)
	require.Equal(t, 62, *v.FirstIndex)
	require.Zero(t, v.PendingSafe)
}

func TestApplyRecordsOutcomesAndIsAudited(t *testing.T) {
	f := newFixture(t)
	run, err := f.svc.Apply(f.owner, f.server.ID, nil)
	require.NoError(t, err)
	p := f.disp.last(t)
	require.Equal(t, agentprotocol.HardeningActionApply, p.Action)
	require.Equal(t, lynis.Defaults(), p.FixIDs, "no selection means the default set")
	require.NotContains(t, p.FixIDs, "auditd")

	f.result(run.ID, agentprotocol.HardeningResult{
		Action: "apply", Mode: "helper", LynisInstalled: true, Report: report(71), RebootRequired: true,
		Fixes: fixStates(agentprotocol.HardeningFixApplied),
		Outcomes: []agentprotocol.HardeningFixOutcome{
			{ID: "umask", Outcome: agentprotocol.HardeningOutcomeApplied, Message: "UMASK 027"},
			{ID: "tmp-mount", Outcome: agentprotocol.HardeningOutcomeSkipped, Message: "this host is a container"},
		},
	})
	v, _ := f.svc.Get(f.owner, f.server.ID)
	require.True(t, v.RebootRequired)
	require.Equal(t, "helper", v.Mode)
	require.Len(t, v.Runs[0].Outcomes, 2)
	require.Equal(t, "UMASK 027", v.Runs[0].Outcomes[0].Message)

	// Both the request (who) and the result (what) are in the audit log.
	rows, err := f.pool.Query(bg, `SELECT action, metadata FROM audit_events WHERE resource_id = $1 ORDER BY id`, f.server.ID.String())
	require.NoError(t, err)
	defer rows.Close()
	got := map[string]map[string]any{}
	for rows.Next() {
		var action string
		var meta map[string]any
		require.NoError(t, rows.Scan(&action, &meta))
		got[action] = meta
	}
	require.Contains(t, got, ActionApply)
	require.Contains(t, got, ActionRun)
	require.Equal(t, "applied", got[ActionRun]["outcomes"].(map[string]any)["umask"])
	require.EqualValues(t, 71, got[ActionRun]["hardeningIndex"])

	// An audit afterwards clears the reboot flag.
	a, err := f.svc.Audit(f.owner, f.server.ID, false)
	require.NoError(t, err)
	f.result(a.ID, agentprotocol.HardeningResult{Action: "audit", Mode: "helper", LynisInstalled: true, Report: report(71)})
	v, _ = f.svc.Get(f.owner, f.server.ID)
	require.False(t, v.RebootRequired)
	require.NotEmpty(t, v.Fixes[0].State, "the fix states of the earlier run are kept when a result has none")
	require.Equal(t, agentprotocol.HardeningFixApplied, v.Fixes[0].State)
}

func TestWhoMayChangeTheHost(t *testing.T) {
	f := newFixture(t)
	// A viewer can read but not run anything.
	_, err := f.svc.Get(f.viewer, f.server.ID)
	require.NoError(t, err)
	for name, fn := range map[string]func() error{
		"audit":    func() error { _, err := f.svc.Audit(f.viewer, f.server.ID, false); return err },
		"apply":    func() error { _, err := f.svc.Apply(f.viewer, f.server.ID, nil); return err },
		"rollback": func() error { _, err := f.svc.Rollback(f.viewer, f.server.ID, []string{"umask"}); return err },
		"weekly":   func() error { _, err := f.svc.SetWeekly(f.viewer, f.server.ID, false); return err },
	} {
		require.Equal(t, "forbidden", apiCode(t, fn()), name)
	}
	// A member with the write scope still cannot: Owners and Admins only.
	for name, fn := range map[string]func() error{
		"audit":    func() error { _, err := f.svc.Audit(f.member, f.server.ID, false); return err },
		"apply":    func() error { _, err := f.svc.Apply(f.member, f.server.ID, nil); return err },
		"rollback": func() error { _, err := f.svc.Rollback(f.member, f.server.ID, []string{"umask"}); return err },
		"weekly":   func() error { _, err := f.svc.SetWeekly(f.member, f.server.ID, false); return err },
	} {
		require.Equal(t, "forbidden", apiCode(t, fn()), name)
	}
	require.Zero(t, f.disp.count())

	// Another team's server is not found.
	other := newFixture(t)
	_, err = f.svc.Get(other.owner, f.server.ID)
	require.Equal(t, "not_found", apiCode(t, err))
}

func TestRequestValidation(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Apply(f.owner, f.server.ID, []string{"disable-ssh-password-auth"})
	require.Equal(t, "unknown_fix", apiCode(t, err))
	_, err = f.svc.Rollback(f.owner, f.server.ID, nil)
	require.Equal(t, "no_fix_selected", apiCode(t, err))
	_, err = f.svc.Rollback(f.owner, f.server.ID, []string{"nope"})
	require.Equal(t, "unknown_fix", apiCode(t, err))
	require.Zero(t, f.disp.count())

	// An optional fix is applied only when selected.
	_, err = f.svc.Apply(f.owner, f.server.ID, []string{"sysctl-fs-protected"})
	require.NoError(t, err)
	require.Equal(t, []string{"sysctl-fs-protected"}, f.disp.last(t).FixIDs)
}

func TestOfflineRevokedAndOldAgents(t *testing.T) {
	f := newFixture(t)
	set := func(status string, facts string) {
		var raw any
		if facts != "" {
			raw = []byte(facts)
		}
		_, err := f.pool.Exec(bg, "UPDATE servers SET status = $2, facts = $3::jsonb WHERE id = $1", f.server.ID, status, raw)
		require.NoError(t, err)
	}
	set("offline", "")
	_, err := f.svc.Audit(f.owner, f.server.ID, false)
	require.Equal(t, "server_offline", apiCode(t, err))
	set("revoked", "")
	_, err = f.svc.Audit(f.owner, f.server.ID, false)
	require.Equal(t, "server_revoked", apiCode(t, err))
	set("online", `{"os":"linux","capabilities":["tunnel"]}`)
	_, err = f.svc.Audit(f.owner, f.server.ID, false)
	require.Equal(t, "agent_too_old", apiCode(t, err))
	v, _ := f.svc.Get(f.owner, f.server.ID)
	require.False(t, v.AgentSupported)
	require.Zero(t, f.disp.count())
}

func TestFailuresAndSkippedRuns(t *testing.T) {
	f := newFixture(t)

	// The agent could not complete the run.
	run, _ := f.svc.Apply(f.owner, f.server.ID, nil)
	cid := *f.run(run.ID).CommandID
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, cid, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "host hardening apply: boom"}))
	require.Equal(t, "failed", f.run(run.ID).Status)
	require.Contains(t, f.run(run.ID).Error, "boom")
	// A duplicate result changes nothing.
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, cid, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: json.RawMessage(`{}`)}))
	require.Equal(t, "failed", f.run(run.ID).Status)

	// A server without Lynis: the run is skipped, not failed, and says so.
	run2, _ := f.svc.Audit(f.owner, f.server.ID, false)
	f.result(run2.ID, agentprotocol.HardeningResult{Action: "audit", Mode: "host", LynisInstalled: false, Message: "Lynis is not installed on this server.", Fixes: fixStates(agentprotocol.HardeningFixPending)})
	require.Equal(t, "skipped", f.run(run2.ID).Status)
	v, _ := f.svc.Get(f.owner, f.server.ID)
	require.False(t, v.LynisInstalled)
	require.Nil(t, v.HardeningIndex)
	require.Empty(t, v.Remaining)
	require.Equal(t, agentprotocol.HardeningFixPending, v.Fixes[0].State, "fix states are shown without Lynis")

	// A command that never reached the agent.
	run3, _ := f.svc.Audit(f.owner, f.server.ID, false)
	require.NoError(t, f.svc.HandleUndelivered(bg, *f.run(run3.ID).CommandID, "Not delivered: server offline. No changes were made."))
	require.Equal(t, "failed", f.run(run3.ID).Status)
	require.Contains(t, f.run(run3.ID).Error, "Not delivered")
	// Commands of other kinds and unknown commands are not ours.
	require.NoError(t, f.svc.HandleUndelivered(bg, uuid.New(), "x"))
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, uuid.New(), agentprotocol.ResultFrame{}))
}

func TestSweepStartsTheWeeklyAuditOnceAWeek(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 1, f.disp.count())
	p := f.disp.last(t)
	require.Equal(t, agentprotocol.HardeningActionAudit, p.Action)
	require.Equal(t, agentprotocol.HardeningTriggerScheduled, p.Trigger)
	require.False(t, p.InstallLynis, "a scheduled run never installs anything")

	// Pending, and then done: nothing more this week.
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 1, f.disp.count())
	runs, _ := f.q.ListServerHardeningRuns(bg, db.ListServerHardeningRunsParams{ServerID: f.server.ID, Limit: 5})
	f.result(runs[0].ID, agentprotocol.HardeningResult{Action: "audit", Mode: "host", LynisInstalled: true, Report: report(70)})
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 1, f.disp.count())

	// A week later it is due again.
	_, err := f.pool.Exec(bg, "UPDATE server_hardening_runs SET created_at = now() - interval '8 days' WHERE server_id = $1", f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 2, f.disp.count())
	// Audited by the system: the audit log has the result, not a person.
	v, _ := f.svc.Get(f.owner, f.server.ID)
	require.Equal(t, "scheduled", v.Runs[0].Trigger)
}

func TestSweepSkipsOfflineOldAndOptedOutServers(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.SetWeekly(f.owner, f.server.ID, false)
	require.NoError(t, err)
	v, _ := f.svc.Get(f.owner, f.server.ID)
	require.False(t, v.Weekly)
	require.Nil(t, v.NextAuditAt)
	require.NoError(t, f.svc.Sweep(bg))
	require.Zero(t, f.disp.count(), "weekly audit switched off")

	_, err = f.svc.SetWeekly(f.owner, f.server.ID, true)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, `UPDATE servers SET facts = '{"os":"linux","capabilities":["tunnel"]}'::jsonb WHERE id = $1`, f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Zero(t, f.disp.count(), "the agent is too old")

	_, err = f.pool.Exec(bg, `UPDATE servers SET facts = NULL, status = 'offline' WHERE id = $1`, f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Zero(t, f.disp.count(), "offline")
}
