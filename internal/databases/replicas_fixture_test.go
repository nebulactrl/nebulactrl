package databases_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/mesh"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// pgDispatcher stores commands as agent_commands rows like the hub does,
// including its idempotency: the same key returns the same command.
type pgDispatcher struct{}

func (pgDispatcher) Enqueue(ctx context.Context, tx pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	key := c.IdempotencyKey
	if key == "" {
		key = uuid.NewString()
	}
	row, err := db.New(tx).InsertAgentCommand(ctx, db.InsertAgentCommandParams{
		ID: uuid.New(), ServerID: c.ServerID, DeploymentID: c.DeploymentID, Kind: c.Kind, Payload: c.Payload,
		IdempotencyKey: key, ExpiresAt: time.Now().Add(time.Hour),
	})
	return row.ID, err
}

func (pgDispatcher) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

// fakeMesh is a mesh.Provider that writes the real link and exposure rows (the
// replica rows reference them) and records what was asked.
type fakeMesh struct {
	mu       sync.Mutex
	plan     mesh.LinkPlan
	released []uuid.UUID
	grants   []string
	port     int
}

func (m *fakeMesh) PlanLink(context.Context, *db.Queries, uuid.UUID, uuid.UUID, uuid.UUID, string) (mesh.LinkPlan, error) {
	return m.plan, nil
}

func (m *fakeMesh) EnsureExposure(ctx context.Context, tx pgx.Tx, serviceID uuid.UUID, purpose string, containerPort int) (mesh.Exposure, error) {
	m.mu.Lock()
	m.port++
	port := 15000 + m.port
	m.mu.Unlock()
	var e mesh.Exposure
	err := tx.QueryRow(ctx, `
INSERT INTO mesh_exposures (workspace_id, server_id, service_id, environment_id, purpose, container_port, mesh_port)
SELECT p.workspace_id, s.server_id, s.id, s.environment_id, $2, $3, $4
FROM services s JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id WHERE s.id = $1
ON CONFLICT (service_id, purpose) DO UPDATE SET container_port = EXCLUDED.container_port
RETURNING id, server_id, service_id, environment_id, purpose, container_port, mesh_port`, serviceID, purpose, containerPort, port).
		Scan(&e.ID, &e.ServerID, &e.ServiceID, &e.EnvironmentID, &e.Purpose, &e.ContainerPort, &e.MeshPort)
	return e, err
}

func (m *fakeMesh) EnsureLink(ctx context.Context, tx pgx.Tx, env, local, remote uuid.UUID, purpose string) (mesh.Link, error) {
	var l mesh.Link
	err := tx.QueryRow(ctx, `
INSERT INTO mesh_links (workspace_id, environment_id, server_id, remote_server_id, purpose, hostname)
SELECT p.workspace_id, e.id, $2, $3, $4, 'link' FROM environments e JOIN projects p ON p.id = e.project_id WHERE e.id = $1
ON CONFLICT (environment_id, server_id, remote_server_id, purpose) DO UPDATE SET purpose = EXCLUDED.purpose
RETURNING id, environment_id, server_id, remote_server_id, purpose, state`, env, local, remote, purpose).
		Scan(&l.ID, &l.EnvironmentID, &l.ServerID, &l.RemoteServerID, &l.Purpose, &l.State)
	return l, err
}

func (m *fakeMesh) Grant(_ context.Context, _ pgx.Tx, _, _, refID uuid.UUID, source string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.grants = append(m.grants, source+":"+refID.String())
	return nil
}

func (m *fakeMesh) Revoke(context.Context, pgx.Tx, uuid.UUID, uuid.UUID, uuid.UUID, string) error {
	return nil
}

func (m *fakeMesh) Release(_ context.Context, _ pgx.Tx, refID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.released = append(m.released, refID)
	return nil
}

func (m *fakeMesh) Ready(context.Context, uuid.UUID) (mesh.Readiness, error) {
	return mesh.Readiness{Online: true, ACLOK: true}, nil
}

func (m *fakeMesh) Check(context.Context, uuid.UUID, uuid.UUID) (mesh.CheckResult, error) {
	return mesh.CheckResult{Result: agentprotocol.MeshCheckConnected}, nil
}

func (m *fakeMesh) RouteOf(context.Context, *db.Queries, uuid.UUID, uuid.UUID) (mesh.Route, bool, error) {
	return mesh.Route{}, false, nil
}

var _ mesh.Provider = (*fakeMesh)(nil)

// fakeDeployer records a succeeded deployment like a finished deploy would.
type fakeDeployer struct{ f *replicaFixture }

func (d *fakeDeployer) Deploy(ctx context.Context, id uuid.UUID, _, _ string) (deployments.DeploymentView, error) {
	sc, err := services.LoadScopeInternal(ctx, d.f.q, id)
	require.NoError(d.f.t, err)
	dep := d.f.deployOn(id, *sc.Service.ServerID)
	return deployments.DeploymentView{ID: dep}, nil
}

// fakeLinks records the consumers Promote asked the link domain to connect.
type fakeLinks struct {
	mu        sync.Mutex
	consumers [][]uuid.UUID
}

func (l *fakeLinks) ReconcileAdd(_ context.Context, _ uuid.UUID, consumers []uuid.UUID) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.consumers = append(l.consumers, consumers)
	return nil
}

type replicaFixture struct {
	*fixture
	mesh    *fakeMesh
	links   *fakeLinks
	server2 db.Server
}

const replFacts = `{"capabilities":["db.replication","mesh"],"diskTotal":107374182400,"tunDevice":true}`

func newReplicaFixture(t *testing.T) *replicaFixture {
	t.Helper()
	f := newFixture(t)
	rf := &replicaFixture{fixture: f, mesh: &fakeMesh{plan: mesh.LinkPlan{Blockers: []mesh.Blocker{}}}, links: &fakeLinks{}}
	bg := context.Background()
	srv2, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "agent2"})
	require.NoError(t, err)
	rf.server2 = srv2
	for _, id := range []uuid.UUID{f.server.ID, srv2.ID} {
		_, err := f.pool.Exec(bg, "UPDATE servers SET status = 'online', facts = $2 WHERE id = $1", id, replFacts)
		require.NoError(t, err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := databases.NewService(f.pool, events.NewBus(), f.box, pgDispatcher{}, f.svcs, logger)
	svc.SetReplicas(databases.ReplicaDeps{Mesh: rf.mesh, Links: rf.links, Deployer: &fakeDeployer{f: rf}})
	f.dbs = svc
	f.svcs.SetDatabaseGuard(svc)
	return rf
}

// deployOn gives a service a succeeded deployment on a server.
func (f *replicaFixture) deployOn(id, server uuid.UUID) uuid.UUID {
	f.t.Helper()
	bg := context.Background()
	sc, err := services.LoadScopeInternal(bg, f.q, id)
	require.NoError(f.t, err)
	cfg := sc.Config()
	snap, _ := json.Marshal(services.Snapshot{Config: cfg})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceImage, Image: cfg.Image})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: id, Source: src, Config: snap, ImageRef: cfg.Image})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{
		ServiceID: id, ReleaseID: &rel.ID, ServerID: &server, Trigger: "manual", ActorType: "user", Strategy: "recreate",
	})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: id, CurrentDeploymentID: &dep.ID, AppliedGeneration: sc.Service.Generation}))
	return dep.ID
}

// primary creates and deploys a Postgres 17 primary on server 1.
func (f *replicaFixture) primary(name string) services.ServiceView {
	f.t.Helper()
	v := f.createDB(name, "postgres", "17")
	f.deployOn(v.ID, f.server.ID)
	return v
}

func (f *replicaFixture) addReplica(primary uuid.UUID, server uuid.UUID) uuid.UUID {
	f.t.Helper()
	res, err := f.dbs.AddReplica(f.ctx, primary, databases.AddReplicaInput{ServerID: server, AcceptMeshChanges: true})
	require.NoError(f.t, err)
	require.NotNil(f.t, res.ServiceID)
	return *res.ServiceID
}

// streamingReplica adds a replica and puts it in the streaming state, deployed.
func (f *replicaFixture) streamingReplica(primary uuid.UUID, server uuid.UUID) uuid.UUID {
	f.t.Helper()
	id := f.addReplica(primary, server)
	f.deployOn(id, server)
	_, err := f.pool.Exec(context.Background(),
		"UPDATE database_replicas SET state = 'streaming', step = 'done#1', message = '', observed_at = now(), last_streaming_at = now(), lag_bytes = 0 WHERE service_id = $1", id)
	require.NoError(f.t, err)
	return id
}

func (f *replicaFixture) replica(id uuid.UUID) db.DatabaseReplica {
	f.t.Helper()
	r, err := f.q.GetDatabaseReplica(context.Background(), id)
	require.NoError(f.t, err)
	return r
}

// commandByKey returns the command enqueued under key.
func (f *replicaFixture) commandByKey(key string) (db.AgentCommand, bool) {
	f.t.Helper()
	c, err := f.q.GetAgentCommandByKey(context.Background(), key)
	if err != nil {
		return db.AgentCommand{}, false
	}
	return c, true
}

// complete finishes the command enqueued under key the way the hub does.
func (f *replicaFixture) complete(key, outcome, errMsg string, data any) {
	f.t.Helper()
	c, ok := f.commandByKey(key)
	require.True(f.t, ok, "no command %q", key)
	raw, _ := json.Marshal(data)
	frame, _ := json.Marshal(agentprotocol.ResultFrame{CommandID: c.ID.String(), Outcome: outcome, Data: raw, Error: errMsg})
	status := "succeeded"
	if outcome != agentprotocol.OutcomeSucceeded {
		status = "failed"
	}
	_, err := f.q.FinishAgentCommand(context.Background(), db.FinishAgentCommandParams{ID: c.ID, ServerID: c.ServerID, Status: status, Result: frame, Error: errMsg})
	require.NoError(f.t, err)
}

func (f *replicaFixture) ok(key string, data any) {
	f.t.Helper()
	f.complete(key, agentprotocol.OutcomeSucceeded, "", data)
}

func (f *replicaFixture) generation(id uuid.UUID) int64 { return f.reload(id).Generation }
