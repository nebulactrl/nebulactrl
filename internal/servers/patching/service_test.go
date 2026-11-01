package patching

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
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var bg = context.Background()

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

func (d *fakeDisp) last(t *testing.T) agentprotocol.PatchingPayload {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	require.NotEmpty(t, d.cmds)
	c := d.cmds[len(d.cmds)-1]
	require.Equal(t, agentprotocol.KindHostPatching, c.Kind)
	var p agentprotocol.PatchingPayload
	require.NoError(t, json.Unmarshal(c.Payload, &p))
	return p
}

type fakeEmitter struct {
	mu  sync.Mutex
	evs []notifications.Event
}

func (e *fakeEmitter) Emit(_ context.Context, ev notifications.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.evs = append(e.evs, ev)
}

func (e *fakeEmitter) ofType(t string) []notifications.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []notifications.Event
	for _, ev := range e.evs {
		if ev.Type == t {
			out = append(out, ev)
		}
	}
	return out
}

type fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	ws     uuid.UUID
	server db.Server
	svc    *Service
	disp   *fakeDisp
	emit   *fakeEmitter
	owner  context.Context // the instance admin
	other  context.Context // another Owner: not the instance admin
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
	u2, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "b@example.com", Name: "B", PasswordHash: "x"})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "Agent 1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", srv.ID)
	require.NoError(t, err)
	srv, err = q.GetServerByID(bg, srv.ID)
	require.NoError(t, err)
	disp, emit := &fakeDisp{}, &fakeEmitter{}
	return &fixture{
		t: t, pool: pool, q: q, ws: ws.ID, server: srv, disp: disp, emit: emit,
		svc:    NewService(Deps{Pool: pool, Bus: events.NewBus(), Dispatcher: disp, Notifier: emit}),
		owner:  principal(ws.ID, u.ID, auth.RoleOwner, policy.AllScopes),
		other:  principal(ws.ID, u2.ID, auth.RoleOwner, policy.AllScopes),
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

func (f *fixture) run(id uuid.UUID) db.ServerPatchRun {
	f.t.Helper()
	r, err := f.q.GetServerPatchRun(bg, db.GetServerPatchRunParams{ID: id, ServerID: f.server.ID})
	require.NoError(f.t, err)
	return r
}

func (f *fixture) answer(commandID uuid.UUID, res agentprotocol.PatchingResult) {
	f.t.Helper()
	data, err := json.Marshal(res)
	require.NoError(f.t, err)
	require.NoError(f.t, f.svc.HandleResult(bg, f.server.ID, commandID, agentprotocol.ResultFrame{
		CommandID: commandID.String(), Outcome: agentprotocol.OutcomeSucceeded, Data: data,
	}))
}

func (f *fixture) auditActions() []string {
	f.t.Helper()
	rows, err := f.pool.Query(bg, "SELECT action FROM audit_events WHERE resource_id = $1 AND action <> 'server.patches.check' ORDER BY id", f.server.ID.String())
	require.NoError(f.t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		require.NoError(f.t, rows.Scan(&a))
		out = append(out, a)
	}
	return out
}

func state(updates, security int, reboot bool, boot string, pkgs ...agentprotocol.PatchPackage) agentprotocol.PatchState {
	return agentprotocol.PatchState{
		Manager: "apt", Supported: true, Updates: updates, Security: security, RebootRequired: reboot, BootID: boot, Packages: pkgs,
	}
}

// check runs a manual check as the owner and has the agent answer it with st.
func (f *fixture) check(st agentprotocol.PatchState) db.ServerPatchRun {
	f.t.Helper()
	r, err := f.svc.Check(f.owner, f.server.ID)
	require.NoError(f.t, err)
	row := f.run(r.ID)
	f.answer(*row.CommandID, agentprotocol.PatchingResult{Action: agentprotocol.PatchingActionCheck, State: st})
	return f.run(r.ID)
}

// addProductionService puts a service of a production environment on the server.
func (f *fixture) addProductionService(name string) {
	f.t.Helper()
	p, err := f.q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: f.ws, Name: "Shop", Slug: "shop-" + uuid.NewString()[:6]})
	require.NoError(f.t, err)
	e, err := f.q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: p.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, `INSERT INTO services (environment_id, name, slug, kind, source_type, server_id) VALUES ($1,$2,$3,'app','image',$4)`,
		e.ID, name, name, f.server.ID)
	require.NoError(f.t, err)
}
