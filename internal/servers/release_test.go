package servers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// recDisp records the commands the services enqueue.
type recDisp struct {
	mu   sync.Mutex
	cmds []agents.NewCommand
}

func (d *recDisp) Enqueue(_ context.Context, _ pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cmds = append(d.cmds, c)
	return uuid.New(), nil
}

func (d *recDisp) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

func (d *recDisp) commands() []agents.NewCommand {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]agents.NewCommand(nil), d.cmds...)
}

// vetoGuard is a database guard that refuses to delete one service.
type vetoGuard struct{ refuse uuid.UUID }

func (g vetoGuard) BeforeDelete(_ context.Context, _ pgx.Tx, sc services.Scope) error {
	if sc.Service.ID == g.refuse {
		return apierr.Conflict("replicas_exist", "this database has read replicas: remove them first")
	}
	return nil
}

func (vetoGuard) BeforeMove(context.Context, *db.Queries, services.Scope, *uuid.UUID) error {
	return nil
}

// releaseFixture is the remove fixture with the real service domain wired as
// the releaser.
type releaseFixture struct {
	*removeFixture
	disp *recDisp
	bus  *events.Bus
	svcs *services.Service
}

func newReleaseFixture(t *testing.T) *releaseFixture {
	t.Helper()
	f := newRemoveFixture(t)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	disp := &recDisp{}
	bus := events.NewBus()
	svcs := services.NewService(f.pool, bus, box, disp, nil)
	f.svc.SetServiceReleaser(svcs)
	return &releaseFixture{removeFixture: f, disp: disp, bus: bus, svcs: svcs}
}

// deployed places a service on the server whose current deployment runs there.
func (f *releaseFixture) deployed(t *testing.T, name string) uuid.UUID {
	t.Helper()
	bg := context.Background()
	svc, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env, Name: name, Slug: name, Kind: "app", SourceType: "image",
		Config: json.RawMessage(`{"image":"nginx:1"}`), ServerID: &f.server.ID,
	})
	require.NoError(t, err)
	snap, _ := json.Marshal(services.Snapshot{Config: services.DecodeConfig(svc.Config), ServerID: &f.server.ID})
	src, _ := json.Marshal(services.ReleaseSource{Type: "image"})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: svc.ID, Source: src, Config: snap})
	require.NoError(t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: svc.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "recreate"})
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(t, err)
	require.NoError(t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: svc.ID, CurrentDeploymentID: &dep.ID, AppliedGeneration: svc.Generation}))
	return svc.ID
}

func TestRemoveReleaseDeletesTheServicesWithoutTouchingTheHost(t *testing.T) {
	f := newReleaseFixture(t)
	f.goOnline(t, f.server.ID, true)
	web := f.deployed(t, "web")
	worker := f.deployed(t, "worker")
	f.addService(t, "app", true) // already deleted: not counted
	// web has a domain whose DNS record NebulaCtrl tracks.
	_, err := f.pool.Exec(context.Background(), "INSERT INTO domains (service_id, hostname, target_port, cf_zone_id) VALUES ($1, 'web.example.com', 80, 'z1')", web)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), `INSERT INTO cloudflare_records (zone_id, record_id, workspace_id, owner_kind, owner_id, hostname, type, content, proxied)
		SELECT cf_zone_id, 'r1', $2, 'domain', id, hostname, 'A', '203.0.113.7', true FROM domains WHERE service_id = $1`, web, f.ws)
	require.NoError(t, err)

	feed := f.bus.Subscribe(context.Background())

	res, err := f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge", Mode: servers.ModeRelease})
	require.NoError(t, err)
	require.Equal(t, servers.ModeRelease, res.Mode)
	require.True(t, res.Removed)
	require.True(t, res.HostUntouched)

	require.False(t, f.exists(t, f.server.ID))
	require.Empty(t, f.agent.sent(), "the agent was never asked to clean")
	require.Empty(t, f.queued, "no removal job")
	for _, c := range f.disp.commands() {
		require.NotEqual(t, agentprotocol.KindRemoveService, c.Kind, "no remove_service reaches the host")
	}
	require.Empty(t, f.disp.commands())

	for _, id := range []uuid.UUID{web, worker} {
		var deleted bool
		require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT deleted_at IS NOT NULL FROM services WHERE id = $1", id).Scan(&deleted))
		require.True(t, deleted, "the service is soft-deleted")
	}

	// The record stays at Cloudflare for the instance that adopts the host: it is
	// no longer tracked, so no later collection of orphan records deletes it.
	var tracked int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM cloudflare_records WHERE record_id = 'r1'").Scan(&tracked))
	require.Zero(t, tracked)

	rows := f.audit(t, servers.ActionRemoved)
	require.Len(t, rows, 1)
	require.Equal(t, "release", rows[0]["mode"])
	require.Equal(t, true, rows[0]["hostUntouched"])
	require.EqualValues(t, 2, rows[0]["services"])

	del := f.audit(t, "service.delete")
	require.Len(t, del, 2)
	for _, m := range del {
		require.Equal(t, true, m["hostUntouched"])
		require.Equal(t, "server released", m["reason"])
	}

	deletedEvents := 0
	for len(feed) > 0 {
		if ev := <-feed; ev.Type == services.EventServiceDeleted {
			deletedEvents++
		}
	}
	require.Equal(t, 2, deletedEvents, "the deletions are published after the commit")
}

func TestRemoveReleaseIsRefusedWhileADeploymentRuns(t *testing.T) {
	f := newReleaseFixture(t)
	f.goOnline(t, f.server.ID, true)
	web := f.deployed(t, "web")
	_, err := f.pool.Exec(context.Background(),
		"INSERT INTO deployments (service_id, server_id, trigger, actor_type, status, strategy) VALUES ($1, $2, 'manual', 'user', 'building', 'recreate')", web, f.server.ID)
	require.NoError(t, err)

	_, err = f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge", Mode: servers.ModeRelease})
	requireAPIError(t, err, http.StatusConflict, "deployment_in_progress")
	require.True(t, f.exists(t, f.server.ID))
	var deleted bool
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT deleted_at IS NOT NULL FROM services WHERE id = $1", web).Scan(&deleted))
	require.False(t, deleted)
}

func TestRemoveReleaseRefusesTheControlPlaneServer(t *testing.T) {
	f := newReleaseFixture(t)
	f.goOnline(t, f.server.ID, true)
	f.deployed(t, "web")
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET is_control_plane = true WHERE id = $1", f.server.ID)
	require.NoError(t, err)

	_, err = f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge", Mode: servers.ModeRelease})
	requireAPIError(t, err, http.StatusConflict, "server_is_control_plane")
	require.True(t, f.exists(t, f.server.ID))
}

// An offline server is released the same way: its host cannot be asked anyway.
func TestRemoveReleaseOfAnOfflineServerWithNoServices(t *testing.T) {
	f := newReleaseFixture(t)
	res, err := f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge", Mode: servers.ModeRelease})
	require.NoError(t, err)
	require.True(t, res.Removed)
	require.False(t, f.exists(t, f.server.ID))
	rows := f.audit(t, servers.ActionRemoved)
	require.Len(t, rows, 1)
	require.EqualValues(t, 0, rows[0]["services"])
}

// The server JSON reports the adoption state and the unmanaged count.
func TestServerJSONReportsAdoption(t *testing.T) {
	f, _ := newUnrestrictedFixture(t)
	sv, err := f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.Nil(t, sv.Adoption)

	facts, err := json.Marshal(agentprotocol.Facts{ManagedServices: []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}})
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET adoption_state = 'held', facts = $2 WHERE id = $1", f.server.ID, facts)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "INSERT INTO server_unmanaged_services (server_id, service_id) VALUES ($1, $2)", f.server.ID, uuid.New())
	require.NoError(t, err)

	sv, err = f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, &servers.ServerAdoption{State: "held", Found: 3, Unmanaged: 1}, sv.Adoption)

	list, _, err := f.svc.List(f.admin, "", 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, sv.Adoption, list[0].Adoption)

	// Reviewed: the hold is gone, the unmanaged services remain.
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET adoption_state = '' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	sv, err = f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, &servers.ServerAdoption{State: "", Found: 0, Unmanaged: 1}, sv.Adoption)
}

// Releasing deletes the production services of the server: the production
// guard is asked before anything changes, like for a revoke.
func TestRemoveReleaseAsksTheProductionGuard(t *testing.T) {
	f := newReleaseFixture(t)
	f.goOnline(t, f.server.ID, true)
	web := f.deployed(t, "web")
	_, err := f.pool.Exec(context.Background(), "UPDATE environments SET is_production = true WHERE id = $1", f.env)
	require.NoError(t, err)

	guarded := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: uuid.New(), WorkspaceID: f.ws, Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
	// No approval machinery is installed in this test: the guard fails closed,
	// which proves it was consulted before any state changed.
	_, err = f.svc.Remove(guarded, f.server.ID, servers.RemoveInput{Confirm: "edge", Mode: servers.ModeRelease})
	requireAPIError(t, err, http.StatusServiceUnavailable, "approval_unavailable")

	var status string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT status FROM servers WHERE id = $1", f.server.ID).Scan(&status))
	require.Equal(t, "online", status, "the agent was not revoked")
	var deleted bool
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT deleted_at IS NOT NULL FROM services WHERE id = $1", web).Scan(&deleted))
	require.False(t, deleted)

	// A revoked server still hosts its production services, and releasing
	// deletes them: the guard is asked for it too.
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'revoked' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.svc.Remove(guarded, f.server.ID, servers.RemoveInput{Confirm: "edge", Mode: servers.ModeRelease})
	requireAPIError(t, err, http.StatusServiceUnavailable, "approval_unavailable")
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT deleted_at IS NOT NULL FROM services WHERE id = $1", web).Scan(&deleted))
	require.False(t, deleted, "a refused release of a revoked server deletes nothing")

	// A principal the guard does not apply to (a person) releases without an approval.
	_, err = f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge", Mode: servers.ModeRelease})
	require.NoError(t, err)
}

// A release that a database guard vetoes changes nothing: the agent is not
// revoked and no service of the server is deleted, not even those released
// before the one that was refused.
func TestRemoveReleaseThatIsVetoedLeavesTheServerAndItsServicesAlone(t *testing.T) {
	f := newReleaseFixture(t)
	f.goOnline(t, f.server.ID, true)
	web := f.deployed(t, "web")
	other := f.deployed(t, "other") // created last: released first, then web is refused
	f.svcs.SetDatabaseGuard(vetoGuard{refuse: web})

	_, err := f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge", Mode: servers.ModeRelease})
	requireAPIError(t, err, http.StatusConflict, "replicas_exist")

	require.True(t, f.exists(t, f.server.ID))
	var status string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT status FROM servers WHERE id = $1", f.server.ID).Scan(&status))
	require.Equal(t, "online", status, "the revoke rolled back with the release")
	for _, id := range []uuid.UUID{web, other} {
		var deleted bool
		require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT deleted_at IS NOT NULL FROM services WHERE id = $1", id).Scan(&deleted))
		require.False(t, deleted, "no service was deleted")
	}
	require.Empty(t, f.audit(t, "server.revoke"))
	require.Empty(t, f.audit(t, "service.delete"))
	require.Empty(t, f.audit(t, servers.ActionRemoved))
	require.Empty(t, f.disp.commands())
}
