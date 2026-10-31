package agentupgrade

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/internal/version"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var bg = context.Background()

const (
	controlVersion = "1.5.0"
	binarySHA      = "7d4f1c9a0b3e2f58a6c1d9e4b7f20a35c8d6e1f4a9b2c3d4e5f60718293a4b5c"
)

// ---- pure decisions ----------------------------------------------------------

func factsJSON(arch string, caps ...string) json.RawMessage {
	b, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Arch: arch, Capabilities: caps})
	return b
}

func TestEvaluate(t *testing.T) {
	now := time.Now()
	has := func(string) bool { return true }
	hasNone := func(string) bool { return false }
	capable := factsJSON("amd64", agentprotocol.CapAgentUpgrade)
	running := &db.ServerAgentUpgrade{State: StateRestarting, RequestedAt: now.Add(-time.Minute)}
	stale := &db.ServerAgentUpgrade{State: StateQueued, RequestedAt: now.Add(-2 * StaleAfter)}

	cases := []struct {
		name        string
		status, ver string
		facts       json.RawMessage
		bin         func(string) bool
		prev        *db.ServerAgentUpgrade
		upgradable  bool
		code        string
		skew        version.Skew
		supported   bool
	}{
		{"behind and capable", "online", "1.4.0", capable, has, nil, true, "", version.SkewBehind, true},
		{"after a failed attempt", "online", "1.4.0", capable, has, &db.ServerAgentUpgrade{State: StateFailed, RequestedAt: now}, true, "", version.SkewBehind, true},
		{"a stale in-flight upgrade does not block", "online", "1.4.0", capable, has, stale, true, "", version.SkewBehind, true},
		{"development agent: order unknown, administrator may upgrade", "online", "dev", capable, has, nil, true, "", version.SkewUnknown, true},
		{"offline", "offline", "1.4.0", capable, has, nil, false, CodeServerOffline, version.SkewBehind, true},
		{"never connected", "pending", "", nil, has, nil, false, CodeNotUpgradable, version.SkewUnknown, false},
		{"revoked", "revoked", "1.4.0", capable, has, nil, false, CodeNotUpgradable, version.SkewBehind, true},
		{"current", "online", "1.5.0", capable, has, nil, false, CodeAlreadyCurrent, version.SkewCurrent, true},
		{"newer than the control plane", "online", "1.6.0", capable, has, nil, false, CodeAgentNewer, version.SkewAhead, true},
		{"agent predates self-upgrade", "online", "1.2.0", factsJSON("amd64"), has, nil, false, CodeAgentTooOld, version.SkewBehind, false},
		{"upgrade already running", "online", "1.4.0", capable, has, running, false, CodeUpgradeInProgress, version.SkewBehind, true},
		{"no binary for the architecture", "online", "1.4.0", capable, hasNone, nil, false, CodeNoAgentBinary, version.SkewBehind, true},
		{"no architecture in the facts", "online", "1.4.0", factsJSON("", agentprotocol.CapAgentUpgrade), has, nil, false, CodeNoAgentBinary, version.SkewBehind, true},
	}
	for _, c := range cases {
		e := Evaluate(c.status, c.ver, controlVersion, c.facts, c.bin, c.prev, now)
		require.Equal(t, c.upgradable, e.Upgradable, c.name)
		require.Equal(t, c.code, e.Code, c.name)
		require.Equal(t, c.skew, e.Skew, c.name)
		require.Equal(t, c.supported, e.Supported, c.name)
		if !c.upgradable {
			require.NotEmpty(t, e.Reason, c.name)
		}
	}
}

func TestShouldAutoUpgrade(t *testing.T) {
	behind := Eligibility{Skew: version.SkewBehind, Upgradable: true}
	unknown := Eligibility{Skew: version.SkewUnknown, Upgradable: true}
	failedSame := &db.ServerAgentUpgrade{State: StateFailed, TargetVersion: "1.5.0"}
	failedOld := &db.ServerAgentUpgrade{State: StateFailed, TargetVersion: "1.4.0"}

	require.True(t, shouldAutoUpgrade(true, behind, nil, "1.5.0"))
	require.False(t, shouldAutoUpgrade(false, behind, nil, "1.5.0"), "the setting is off")
	require.False(t, shouldAutoUpgrade(true, unknown, nil, "1.5.0"), "a development build is left to the administrator")
	require.False(t, shouldAutoUpgrade(true, Eligibility{Skew: version.SkewBehind}, nil, "1.5.0"), "not upgradable")
	require.False(t, shouldAutoUpgrade(true, behind, failedSame, "1.5.0"), "no retry loop for a target that failed")
	require.True(t, shouldAutoUpgrade(true, behind, failedOld, "1.5.0"), "a newer target gets a new attempt")
}

// ---- service with a database ---------------------------------------------------

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

func (d *fakeDisp) lastPayload(t *testing.T) agentprotocol.UpgradePayload {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	require.NotEmpty(t, d.cmds)
	c := d.cmds[len(d.cmds)-1]
	require.Equal(t, agentprotocol.KindAgentUpgrade, c.Kind)
	var p agentprotocol.UpgradePayload
	require.NoError(t, json.Unmarshal(c.Payload, &p))
	return p
}

type fakeBins struct{ arches map[string]agents.BinaryInfo }

func (f fakeBins) AgentBinaryInfo(arch string) (agents.BinaryInfo, error) {
	if b, ok := f.arches[arch]; ok {
		return b, nil
	}
	return agents.BinaryInfo{}, agents.ErrNoAgentBinary
}

type adminFunc func(uuid.UUID) bool

func (f adminFunc) IsInstanceAdmin(_ context.Context, id uuid.UUID) (bool, error) { return f(id), nil }

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
	admin  uuid.UUID
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
	disp := &fakeDisp{}
	jm := jobs.New(pool, nil)
	svc := NewService(Deps{
		Pool: pool, Bus: events.NewBus(), Dispatcher: disp, Jobs: jm, Admins: adminFunc(func(id uuid.UUID) bool { return id == u.ID }),
		Binaries: fakeBins{arches: map[string]agents.BinaryInfo{"amd64": {SHA256: binarySHA, Size: 61_000_000}}},
		Version:  func() string { return controlVersion },
	})
	RegisterJobs(jm, svc)
	f := &fixture{
		t: t, pool: pool, q: q, ws: ws.ID, svc: svc, disp: disp, admin: u.ID,
		owner:  principal(ws.ID, u.ID, auth.RoleOwner, policy.AllScopes),
		member: principal(ws.ID, u.ID, auth.RoleDeveloper, policy.AllScopes),
		viewer: principal(ws.ID, u.ID, auth.RoleViewer, []string{policy.ScopeProjectsRead, policy.ScopeServersRead}),
	}
	f.server = f.addServer("Agent 1", "1.4.0", "online", agentprotocol.CapAgentUpgrade)
	return f
}

func (f *fixture) addServer(name, agentVersion, status string, caps ...string) db.Server {
	f.t.Helper()
	srv, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.ws, Name: name})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, `UPDATE servers SET status = $2, agent_version = $3, facts = $4, last_seen_at = now() WHERE id = $1`,
		srv.ID, status, agentVersion, factsJSON("amd64", caps...))
	require.NoError(f.t, err)
	srv, err = f.q.GetServerByID(bg, srv.ID)
	require.NoError(f.t, err)
	return srv
}

func (f *fixture) hello(srv db.Server, agentVersion string, upgrade *agentprotocol.UpgradeReport) {
	f.t.Helper()
	_, err := f.pool.Exec(bg, `UPDATE servers SET agent_version = $2, status = 'online' WHERE id = $1`, srv.ID, agentVersion)
	require.NoError(f.t, err)
	var facts agentprotocol.Facts
	require.NoError(f.t, json.Unmarshal(srv.Facts, &facts))
	facts.AgentUpgrade = upgrade
	f.svc.OnHello(bg, srv.ID, agentprotocol.Hello{AgentVersion: agentVersion, ProtocolVersion: agentprotocol.ProtocolVersion, Facts: facts})
}

func (f *fixture) upgrade(srv db.Server) db.ServerAgentUpgrade {
	f.t.Helper()
	row, err := f.q.GetServerAgentUpgrade(bg, srv.ID)
	require.NoError(f.t, err)
	return row
}

func (f *fixture) result(row db.ServerAgentUpgrade, outcome, errMsg string) {
	f.t.Helper()
	require.NoError(f.t, f.svc.HandleResult(bg, row.ServerID, *row.CommandID, agentprotocol.ResultFrame{CommandID: row.CommandID.String(), Outcome: outcome, Error: errMsg}))
}

func (f *fixture) audited(action string) []map[string]any {
	f.t.Helper()
	rows, err := f.pool.Query(bg, `SELECT metadata FROM audit_events WHERE action = $1 ORDER BY id`, action)
	require.NoError(f.t, err)
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var m map[string]any
		require.NoError(f.t, rows.Scan(&m))
		out = append(out, m)
	}
	return out
}

func apiCode(t *testing.T, err error) string {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	return ae.Code
}

func TestConnectingAgentBehindIsUpgradedAutomatically(t *testing.T) {
	f := newFixture(t)
	f.hello(f.server, "1.4.0", nil)

	require.Equal(t, 1, f.disp.count())
	p := f.disp.lastPayload(t)
	require.Equal(t, controlVersion, p.Version)
	require.Equal(t, binarySHA, p.SHA256, "the checksum the agent verifies comes with the command")
	require.EqualValues(t, 61_000_000, p.Size)
	require.False(t, p.Force)

	row := f.upgrade(f.server)
	require.Equal(t, StateQueued, row.State)
	require.Equal(t, TriggerAuto, row.Trigger)
	require.Equal(t, "1.4.0", row.FromVersion)
	require.Equal(t, controlVersion, row.TargetVersion)
	require.Len(t, f.audited(ActionUpgradeAuto), 1, "audited")

	// The command row holds no secret: just the version and the digest.
	var raw string
	require.NoError(t, f.pool.QueryRow(bg, `SELECT payload::text FROM agent_commands WHERE id = $1`, *row.CommandID).Scan(&raw))
	require.Contains(t, raw, binarySHA)

	// A reconnect while it runs does not queue a second one.
	f.hello(f.server, "1.4.0", nil)
	require.Equal(t, 1, f.disp.count())
}

func TestAutoUpgradeSkipsWhatItShould(t *testing.T) {
	f := newFixture(t)
	// Current agent.
	f.hello(f.server, controlVersion, nil)
	require.Zero(t, f.disp.count())
	// A newer agent is never downgraded.
	f.hello(f.server, "1.6.0", nil)
	require.Zero(t, f.disp.count())
	// A development agent has no provable order.
	f.hello(f.server, "dev", nil)
	require.Zero(t, f.disp.count())
	// An agent that predates self-upgrade.
	old := f.addServer("Old", "1.2.0", "online")
	f.hello(old, "1.2.0", nil)
	require.Zero(t, f.disp.count())
	// The setting is off.
	_, err := f.svc.SetAutoUpgrade(f.owner, false)
	require.NoError(t, err)
	f.hello(f.server, "1.4.0", nil)
	require.Zero(t, f.disp.count())
	_, err = f.svc.SetAutoUpgrade(f.owner, true)
	require.NoError(t, err)
	f.hello(f.server, "1.4.0", nil)
	require.Equal(t, 1, f.disp.count())
}

func TestUpgradeLifecycleSucceeds(t *testing.T) {
	f := newFixture(t)
	f.hello(f.server, "1.4.0", nil)
	row := f.upgrade(f.server)

	f.result(row, agentprotocol.OutcomeSucceeded, "")
	row = f.upgrade(f.server)
	require.Equal(t, StateRestarting, row.State)

	// The agent comes back with the target version.
	f.hello(f.server, controlVersion, &agentprotocol.UpgradeReport{State: agentprotocol.UpgradeSwapped, From: "1.4.0", To: controlVersion})
	row = f.upgrade(f.server)
	require.Equal(t, StateSucceeded, row.State)
	require.NotNil(t, row.FinishedAt)
	require.Contains(t, row.Message, "1.4.0")
	done := f.audited(ActionUpgraded)
	require.Len(t, done, 1)
	require.Equal(t, controlVersion, done[0]["to"])
	require.Equal(t, 1, f.disp.count(), "no further command")
}

func TestUpgradeFinishesEvenWhenTheResultWasLost(t *testing.T) {
	f := newFixture(t)
	f.hello(f.server, "1.4.0", nil)
	// The agent restarted into the new version before its result was processed.
	f.hello(f.server, controlVersion, nil)
	require.Equal(t, StateSucceeded, f.upgrade(f.server).State)
	// The late result changes nothing.
	f.result(f.upgrade(f.server), agentprotocol.OutcomeSucceeded, "")
	require.Equal(t, StateSucceeded, f.upgrade(f.server).State)
}

func TestRolledBackUpgradeFailsAndIsNotRetriedAutomatically(t *testing.T) {
	f := newFixture(t)
	f.hello(f.server, "1.4.0", nil)
	f.result(f.upgrade(f.server), agentprotocol.OutcomeSucceeded, "")

	// The new binary did not start; the old one is back and says why.
	f.hello(f.server, "1.4.0", &agentprotocol.UpgradeReport{State: agentprotocol.UpgradeRolledBack, From: "1.4.0", To: controlVersion, Message: "1.5.0 started 3 times without staying connected"})
	row := f.upgrade(f.server)
	require.Equal(t, StateFailed, row.State)
	require.Contains(t, row.Message, "rolled the upgrade back")
	require.Contains(t, row.Message, "without staying connected")
	require.Len(t, f.audited(ActionUpgradeFailed), 1)

	// No retry loop: another connect does not queue the same version again.
	f.hello(f.server, "1.4.0", nil)
	require.Equal(t, 1, f.disp.count())

	// An administrator can retry (the agent needs force to retry a rolled-back version).
	_, err := f.svc.UpgradeServer(f.owner, f.server.ID, true)
	require.NoError(t, err)
	require.Equal(t, 2, f.disp.count())
	require.True(t, f.disp.lastPayload(t).Force)
	require.Equal(t, StateQueued, f.upgrade(f.server).State)
}

func TestRestartedAgentStillOnTheOldVersionFails(t *testing.T) {
	f := newFixture(t)
	f.hello(f.server, "1.4.0", nil)
	f.result(f.upgrade(f.server), agentprotocol.OutcomeSucceeded, "")
	f.hello(f.server, "1.4.0", nil)
	row := f.upgrade(f.server)
	require.Equal(t, StateFailed, row.State)
	require.Contains(t, row.Message, "still runs 1.4.0")
}

func TestRefusedAndUndeliveredUpgradesFail(t *testing.T) {
	f := newFixture(t)
	f.hello(f.server, "1.4.0", nil)
	f.result(f.upgrade(f.server), agentprotocol.OutcomeFailed, "checksum mismatch: the downloaded binary has SHA-256 aa")
	row := f.upgrade(f.server)
	require.Equal(t, StateFailed, row.State)
	require.Contains(t, row.Message, "checksum mismatch")

	g := newFixture(t)
	g.hello(g.server, "1.4.0", nil)
	row = g.upgrade(g.server)
	require.NoError(t, g.svc.HandleUndelivered(bg, *row.CommandID, "Not delivered: server offline. No changes were made."))
	require.Equal(t, StateFailed, g.upgrade(g.server).State)
	// Results and expiry of other commands are ignored.
	require.NoError(t, g.svc.HandleResult(bg, g.server.ID, uuid.New(), agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded}))
	require.NoError(t, g.svc.HandleUndelivered(bg, uuid.New(), "x"))
}

func TestManualUpgradeAuthorizationAndRefusals(t *testing.T) {
	f := newFixture(t)

	_, err := f.svc.UpgradeServer(f.member, f.server.ID, false)
	require.Equal(t, "forbidden", apiCode(t, err), "only Owners and Admins")
	_, err = f.svc.UpgradeServer(f.viewer, f.server.ID, false)
	require.Error(t, err)
	_, err = f.svc.UpgradeServer(f.owner, uuid.New(), false)
	require.Equal(t, "not_found", apiCode(t, err))
	require.Zero(t, f.disp.count())

	off := f.addServer("Offline", "1.4.0", "offline", agentprotocol.CapAgentUpgrade)
	_, err = f.svc.UpgradeServer(f.owner, off.ID, false)
	require.Equal(t, CodeServerOffline, apiCode(t, err))
	old := f.addServer("Old", "1.2.0", "online")
	_, err = f.svc.UpgradeServer(f.owner, old.ID, false)
	require.Equal(t, CodeAgentTooOld, apiCode(t, err))
	cur := f.addServer("Current", controlVersion, "online", agentprotocol.CapAgentUpgrade)
	_, err = f.svc.UpgradeServer(f.owner, cur.ID, false)
	require.Equal(t, CodeAlreadyCurrent, apiCode(t, err))
	require.Zero(t, f.disp.count())

	v, err := f.svc.UpgradeServer(f.owner, f.server.ID, false)
	require.NoError(t, err)
	require.Equal(t, StateQueued, v.State)
	require.Equal(t, TriggerManual, v.Trigger)
	_, err = f.svc.UpgradeServer(f.owner, f.server.ID, false)
	require.Equal(t, CodeUpgradeInProgress, apiCode(t, err))
	manual := f.audited(ActionUpgrade)
	require.Len(t, manual, 1)
	require.Equal(t, f.admin.String(), manual[0]["requestedBy"])
	require.Equal(t, 1, f.disp.count())
}

func TestManualUpgradeWithoutABinaryIsRefused(t *testing.T) {
	f := newFixture(t)
	arm := f.addServer("ARM", "1.4.0", "online", agentprotocol.CapAgentUpgrade)
	_, err := f.pool.Exec(bg, `UPDATE servers SET facts = $2 WHERE id = $1`, arm.ID, factsJSON("arm64", agentprotocol.CapAgentUpgrade))
	require.NoError(t, err)
	_, err = f.svc.UpgradeServer(f.owner, arm.ID, false)
	require.Equal(t, CodeNoAgentBinary, apiCode(t, err))
}

func TestOverviewShowsSkewPerServer(t *testing.T) {
	f := newFixture(t)
	f.addServer("Current", controlVersion, "online", agentprotocol.CapAgentUpgrade)
	f.addServer("Ahead", "2.0.0", "online", agentprotocol.CapAgentUpgrade)
	f.addServer("Old", "1.2.0", "online")
	f.addServer("Dev", "dev", "online", agentprotocol.CapAgentUpgrade)
	gone := f.addServer("Gone", "1.0.0", "revoked")
	_ = gone

	v, err := f.svc.Get(f.viewer)
	require.NoError(t, err)
	require.Equal(t, controlVersion, v.ControlPlaneVersion)
	require.True(t, v.AgentAutoUpgrade, "default on")
	require.Nil(t, v.PreUpgradeBackup)
	byName := map[string]ServerUpgradeView{}
	for _, s := range v.Servers {
		byName[s.Name] = s
	}
	require.Len(t, byName, 5, "revoked servers are not listed")
	require.Equal(t, "behind", byName["Agent 1"].Skew)
	require.True(t, byName["Agent 1"].Upgradable)
	require.Equal(t, "current", byName["Current"].Skew)
	require.Equal(t, "ahead", byName["Ahead"].Skew)
	require.Equal(t, "behind", byName["Old"].Skew)
	require.False(t, byName["Old"].Upgradable)
	require.Contains(t, byName["Old"].Reason, "installer")
	require.Equal(t, "unknown", byName["Dev"].Skew)
	require.Equal(t, 2, v.Behind, "Agent 1 and Old")
	require.Equal(t, 2, v.Upgradable, "Agent 1 and the development agent")

	// The pre-upgrade backup outcome is part of the overview.
	require.NoError(t, f.q.RecordPreUpgradeBackup(bg, db.RecordPreUpgradeBackupParams{
		PreUpgradeStatus: "succeeded", PreUpgradeMessage: "ok", PreUpgradeFromVersion: "1.4.0", PreUpgradeToVersion: "1.5.0",
	}))
	v, err = f.svc.Get(f.viewer)
	require.NoError(t, err)
	require.NotNil(t, v.PreUpgradeBackup)
	require.Equal(t, "succeeded", v.PreUpgradeBackup.Status)
}

func TestAutoUpgradeSettingNeedsTheInstanceAdmin(t *testing.T) {
	f := newFixture(t)
	other := principal(f.ws, uuid.New(), auth.RoleOwner, policy.AllScopes)
	_, err := f.svc.SetAutoUpgrade(other, false)
	require.Equal(t, "forbidden", apiCode(t, err))
	_, err = f.svc.SetAutoUpgrade(f.viewer, false)
	require.Error(t, err)

	v, err := f.svc.SetAutoUpgrade(f.owner, false)
	require.NoError(t, err)
	require.False(t, v.AgentAutoUpgrade)
	require.Len(t, f.audited(ActionAutoSetting), 1)
}

func TestRolloutUpgradesOneServerAtATimeAndStopsAtAFailure(t *testing.T) {
	f := newFixture(t)
	b := f.addServer("Agent 2", "1.3.0", "online", agentprotocol.CapAgentUpgrade)
	c := f.addServer("Agent 3", "1.3.0", "online", agentprotocol.CapAgentUpgrade)
	f.addServer("Current", controlVersion, "online", agentprotocol.CapAgentUpgrade)

	out, err := f.svc.UpgradeAll(f.member, nil, false)
	require.Equal(t, "forbidden", apiCode(t, err))
	require.Zero(t, f.disp.count())

	out, err = f.svc.UpgradeAll(f.owner, nil, false)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{f.server.ID, b.ID, c.ID}, out.ServerIDs, "name order; current agents are not part of it")
	require.Len(t, f.audited(ActionRollout), 1)

	args := RolloutArgs{ServerIDs: out.ServerIDs, StartedAt: time.Now().Add(-time.Second).UTC()}
	step := func() step {
		t.Helper()
		st, err := f.svc.RolloutStep(bg, args)
		require.NoError(t, err)
		return st
	}

	// Server 1: started, then waited for.
	require.Equal(t, stepWait, step())
	require.Equal(t, 1, f.disp.count())
	require.Equal(t, stepWait, step(), "still waiting; nothing else is started")
	require.Equal(t, 1, f.disp.count())
	f.result(f.upgrade(f.server), agentprotocol.OutcomeSucceeded, "")
	require.Equal(t, stepWait, step())
	f.hello(f.server, controlVersion, nil)
	require.Equal(t, stepNext, step())

	// Server 2 is next; its upgrade fails and the rollout stops with server 3 untouched.
	args.Index = 1
	require.Equal(t, stepWait, step())
	require.Equal(t, 2, f.disp.count())
	f.result(f.upgrade(b), agentprotocol.OutcomeFailed, "the directory of the agent binary is not writable")
	require.Equal(t, stepDone, step())
	require.Len(t, f.audited(ActionRolloutHalt), 1)
	_, err = f.q.GetServerAgentUpgrade(bg, c.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows, "server 3 was never touched")
	require.Equal(t, 2, f.disp.count())
}

func TestRolloutSkipsAServerThatWentOfflineAndTimesOut(t *testing.T) {
	f := newFixture(t)
	b := f.addServer("Agent 2", "1.3.0", "online", agentprotocol.CapAgentUpgrade)
	args := RolloutArgs{ServerIDs: []uuid.UUID{f.server.ID, b.ID}, StartedAt: time.Now().Add(-time.Hour).UTC()}

	_, err := f.pool.Exec(bg, `UPDATE servers SET status = 'offline' WHERE id = $1`, f.server.ID)
	require.NoError(t, err)
	st, err := f.svc.RolloutStep(bg, args)
	require.NoError(t, err)
	require.Equal(t, stepNext, st, "an offline server is skipped, not a failure")
	require.Zero(t, f.disp.count())

	args.Index = 1
	st, err = f.svc.RolloutStep(bg, args)
	require.NoError(t, err)
	require.Equal(t, stepWait, st)
	// The agent never comes back.
	_, err = f.pool.Exec(bg, `UPDATE server_agent_upgrades SET requested_at = now() - interval '11 minutes' WHERE server_id = $1`, b.ID)
	require.NoError(t, err)
	st, err = f.svc.RolloutStep(bg, args)
	require.NoError(t, err)
	require.Equal(t, stepDone, st)
	require.Equal(t, StateFailed, f.upgrade(b).State)
	require.Contains(t, f.upgrade(b).Message, "in time")
}

func TestUpgradeAllWithNothingToDo(t *testing.T) {
	f := newFixture(t)
	_, err := f.pool.Exec(bg, `UPDATE servers SET agent_version = $2 WHERE id = $1`, f.server.ID, controlVersion)
	require.NoError(t, err)
	_, err = f.svc.UpgradeAll(f.owner, nil, false)
	require.Equal(t, "nothing_to_upgrade", apiCode(t, err))
	_, err = f.svc.UpgradeAll(f.owner, []uuid.UUID{uuid.New()}, false)
	require.Equal(t, "not_found", apiCode(t, err))
}
