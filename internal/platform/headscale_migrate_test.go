package platform_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/mesh"
	"github.com/nebulactrl/nebulactrl/internal/mesh/meshtest"
	"github.com/nebulactrl/nebulactrl/internal/platform"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	legacyPolicyPath = "/etc/headscale/policy.hujson"
	legacyConfigPath = "/etc/headscale/config.yaml"
	legacyConfig     = "server_url: https://hs.example.test\npolicy:\n  mode: file\n  path: /etc/headscale/policy.hujson\n"
	migrationKey     = "hskey-api-migrate"
)

// migDeployer is the deployer of a Headscale redeploy: it records whether the
// mesh was paused when the deployment started, and a successful one applies the
// staged generation and flips the fake Headscale to policy.mode: database (the
// new config.yaml took effect).
type migDeployer struct {
	inner *fakeDeployer
	hs    *meshtest.Headscale
	pool  *pgxpool.Pool
	ws    uuid.UUID

	mu             sync.Mutex
	fail           bool
	pausedAtDeploy []bool
}

func (d *migDeployer) DeployStaged(ctx context.Context, envID uuid.UUID, ids []uuid.UUID, msg string) (deployments.StagedResult, error) {
	var paused bool
	_ = d.pool.QueryRow(ctx, `SELECT paused FROM mesh_settings WHERE workspace_id = $1`, d.ws).Scan(&paused)
	d.mu.Lock()
	d.pausedAtDeploy = append(d.pausedAtDeploy, paused)
	fail := d.fail
	d.mu.Unlock()
	d.inner.status = "succeeded"
	if fail {
		d.inner.status = "failed"
	}
	res, err := d.inner.DeployStaged(ctx, envID, ids, msg)
	if err == nil && !fail {
		if _, err := d.pool.Exec(ctx, `UPDATE services SET applied_generation = generation WHERE id = ANY($1)`, ids); err != nil {
			return res, err
		}
		d.hs.SetPolicyMode(meshtest.PolicyModeDatabase)
	}
	return res, err
}

func (d *migDeployer) setFail(v bool) {
	d.mu.Lock()
	d.fail = v
	d.mu.Unlock()
}

func (d *migDeployer) deploys() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.pausedAtDeploy)
}

// migFixture is an existing install: a ready platform Headscale that still runs
// with policy.mode: file, wired to a fake Headscale and the real mesh service.
type migFixture struct {
	*fixture
	hs   *meshtest.Headscale
	ts   *tailscale.Service
	mesh *mesh.Service
	dep  *migDeployer
	id   uuid.UUID
	skew time.Duration
}

func newMigration(t *testing.T) *migFixture {
	t.Helper()
	f := newFixture(t)
	m := &migFixture{fixture: f}
	// Provisioned by the fixture's own service: its job manager knows the domain jobs.
	f.agents.out["headscale users create"] = "User created"
	f.agents.out["headscale apikeys create"] = migrationKey + "\n"
	r := f.provision()
	m.id = r.View.ID
	m.hs = meshtest.NewHeadscale(t, meshtest.WithAPIKey(migrationKey), meshtest.WithUsers(map[string]string{"nebulactrl": "7"}), meshtest.WithPolicyMode(meshtest.PolicyModeFile))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	jm := jobs.New(f.pool, logger)
	m.ts = tailscale.NewService(f.pool, f.box, jm, logger)
	m.ts.SetBaseURL("", m.hs.Client())
	tailscale.RegisterJobs(jm, m.ts)
	m.mesh = mesh.NewService(mesh.Deps{Pool: f.pool, Bus: bus, Jobs: jm, Dispatcher: rowDispatcher{}, Tailscale: m.ts})
	mesh.RegisterJobs(jm, m.mesh)
	m.dep = &migDeployer{inner: f.deployer, hs: m.hs, pool: f.pool, ws: f.ws.ID}
	svc := platform.NewService(platform.Deps{
		Pool: f.pool, Bus: bus, Jobs: jm, Box: f.box, Services: f.svcs, Deploy: m.dep, Backups: f.backups,
		Tailnet: m.ts, Mesh: m.mesh, Agents: f.agents, Logger: logger,
		Now: func() time.Time { return time.Now().Add(m.skew) },
	})
	platform.RegisterJobs(jm, svc)
	platform.RegisterMigrationJobs(jm, svc)
	f.svc = svc

	_, err := f.pool.Exec(bg, `UPDATE platform_services SET public_url = $2, wiring = $3 WHERE id = $1`, m.id, m.hs.URL,
		`{"type":"`+platform.WiringTailnet+`","id":"tailscale","name":"Tailscale integration"}`)
	require.NoError(t, err)
	m.setConfig(legacyFiles())
	_, err = f.pool.Exec(bg, `UPDATE services SET applied_generation = generation WHERE id = $1`, m.service().ID)
	require.NoError(t, err)
	f.makeReady(m.id)

	// The integration of the earlier version: an API key, only the service tag.
	_, err = m.ts.Put(f.ctx, tailscale.PutInput{
		Provider: tailscale.ProviderHeadscale, LoginServer: m.hs.URL, ClientSecret: migrationKey, HeadscaleUser: "nebulactrl",
		HeadscaleCredential: tailscale.HeadscaleAPIKey, DefaultTags: []string{"tag:nebula-svc"}, AllowedTags: []string{"tag:nebula-svc"},
	})
	require.NoError(t, err)
	return m
}

var bg = context.Background()

func legacyFiles() []services.FileMount {
	return []services.FileMount{
		{Path: legacyConfigPath, Mode: "0644", Content: legacyConfig},
		{Path: legacyPolicyPath, Mode: "0644", Content: `{"acls":[]}`},
	}
}

func (m *migFixture) service() db.Service {
	rows, err := m.q.ListServicesIncludingDeleted(bg, m.row(m.id).ServiceIds)
	require.NoError(m.t, err)
	require.Len(m.t, rows, 1)
	return rows[0]
}

func (m *migFixture) setConfig(files []services.FileMount) {
	m.t.Helper()
	svc := m.service()
	cfg := services.DecodeConfig(svc.Config)
	cfg.Files = files
	_, err := m.svcs.Update(m.ctx, svc.ID, svc.Generation, services.UpdateInput{Config: &cfg})
	require.NoError(m.t, err)
}

func (m *migFixture) files() map[string]string {
	out := map[string]string{}
	for _, f := range services.DecodeConfig(m.service().Config).Files {
		out[f.Path] = f.Content
	}
	return out
}

func (m *migFixture) migrate() {
	m.t.Helper()
	require.NoError(m.t, m.svc.MigrateHeadscalePolicy(bg))
}

func (m *migFixture) settings() db.MeshSetting {
	s, err := m.q.GetMeshSettings(bg, m.ws.ID)
	require.NoError(m.t, err)
	return s
}

func (m *migFixture) requirePaused(reasonContains ...string) {
	m.t.Helper()
	s := m.settings()
	require.True(m.t, s.Paused, "the mesh must be paused")
	require.True(m.t, strings.HasPrefix(s.PausedReason, platform.MigrationPausedReason), "reason: %s", s.PausedReason)
	for _, c := range reasonContains {
		require.Contains(m.t, s.PausedReason, c)
	}
}

func (m *migFixture) requireRunning() {
	m.t.Helper()
	s := m.settings()
	require.False(m.t, s.Paused)
	require.Empty(m.t, s.PausedReason)
}

func (m *migFixture) requireMigrated() {
	m.t.Helper()
	files := m.files()
	require.NotContains(m.t, files, legacyPolicyPath, "the static policy file is gone")
	require.Contains(m.t, files[legacyConfigPath], "mode: database")
	require.NotContains(m.t, files[legacyConfigPath], "mode: file")
}

// seedNode is a server that already has a mesh node (state worth protecting).
func (m *migFixture) seedNode() {
	m.t.Helper()
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapMesh}, TunDevice: true})
	_, err := m.pool.Exec(bg, `UPDATE servers SET facts = $1 WHERE id = $2`, facts, m.server.ID)
	require.NoError(m.t, err)
	require.NoError(m.t, m.q.EnsureMeshSettings(bg, m.ws.ID))
	_, err = m.q.UpdateMeshSettings(bg, db.UpdateMeshSettingsParams{WorkspaceID: m.ws.ID, Enabled: new(true)})
	require.NoError(m.t, err)
	_, err = m.pool.Exec(bg, `INSERT INTO server_mesh (server_id, workspace_id, hostname, state, device_id, ips, fqdn) VALUES ($1, $2, 'nc-agent1', 'online', '5', '{100.64.0.9}', 'nc-agent1.tailnet.internal')`, m.server.ID, m.ws.ID)
	require.NoError(m.t, err)
}

func (m *migFixture) nodeRow() db.ServerMesh {
	r, err := m.q.GetServerMesh(bg, m.server.ID)
	require.NoError(m.t, err)
	return r
}

func (m *migFixture) drive() (time.Duration, bool) { return m.run(m.id, 12) }

// The whole migration: the pause comes first, nothing of the mesh moves while
// it is on, Headscale is redeployed in database mode, the policy is written and
// read back, then the pause is lifted. Running it again changes nothing.
func TestMigrationPausesFirstRedeploysWritesThePolicyThenUnpauses(t *testing.T) {
	m := newMigration(t)
	m.seedNode()
	nodeBefore := m.nodeRow()

	// Every change of the service happens with the mesh already paused.
	_, err := m.pool.Exec(bg, `
		CREATE TABLE migration_probe (paused boolean);
		CREATE FUNCTION migration_probe_fn() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN INSERT INTO migration_probe SELECT paused FROM mesh_settings; RETURN NEW; END $$;
		CREATE TRIGGER migration_probe_trg BEFORE UPDATE ON services FOR EACH ROW EXECUTE FUNCTION migration_probe_fn();`)
	require.NoError(t, err)

	m.migrate()
	m.requirePaused()
	require.Equal(t, platform.MigrationPausedReason, m.settings().PausedReason)
	m.requireMigrated()
	row := m.row(m.id)
	require.Equal(t, platform.StatusProvisioning, row.Status)
	require.Equal(t, platform.StepDeploy, row.Step)
	require.Equal(t, meshtest.PolicyModeFile, m.hs.PolicyMode(), "not redeployed yet")
	require.Zero(t, m.dep.deploys())

	// While paused nothing of the mesh moves and the existing state is untouched.
	require.NoError(t, m.mesh.ApplyACL(bg, m.ws.ID))
	require.NoError(t, m.mesh.SyncServer(bg, m.server.ID, true))
	require.NoError(t, m.mesh.Reconcile(bg))
	payload, _ := json.Marshal(agentprotocol.SyncMeshPayload{Nodes: []agentprotocol.MeshNodeSpec{{
		NodeID: m.server.ID.String(), Kind: agentprotocol.MeshKindServer, Hostname: "nc-agent1", Provision: true,
	}}})
	_, err = m.mesh.SecretsForCommand(bg, agentprotocol.KindSyncMesh, payload)
	require.Equal(t, "mesh_paused", apiCode(err), "no key is minted while paused")
	other, err := m.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: m.ws.ID, Name: "agent2"})
	require.NoError(t, err)
	_, err = m.pool.Exec(bg, `UPDATE servers SET status = 'online', facts = (SELECT facts FROM servers WHERE id = $2) WHERE id = $1`, other.ID, m.server.ID)
	require.NoError(t, err)
	_, err = m.mesh.SetServerMesh(m.ctx, other.ID, mesh.ServerMeshInput{Enabled: true, AcceptMeshChanges: true})
	require.Equal(t, "mesh_paused", apiCode(err), "a new node cannot be synced while paused")
	require.Empty(t, m.hs.Calls(), "the coordinator was not asked to do anything")
	require.Empty(t, m.hs.Keys())
	_, wrote := m.hs.Policy()
	require.False(t, wrote)
	require.Equal(t, nodeBefore, m.nodeRow(), "the existing node row is untouched")
	var syncs int
	require.NoError(t, m.pool.QueryRow(bg, `SELECT count(*) FROM agent_commands WHERE kind = $1`, agentprotocol.KindSyncMesh).Scan(&syncs))
	require.Zero(t, syncs)

	// Redeploy, wire, write the policy, lift the pause.
	_, done := m.drive()
	require.True(t, done)
	require.Equal(t, platform.StatusReady, m.row(m.id).Status, m.row(m.id).StatusMessage)
	require.Equal(t, []bool{true}, m.dep.pausedAtDeploy, "Headscale was redeployed while the mesh was paused")
	require.Equal(t, meshtest.PolicyModeDatabase, m.hs.PolicyMode())
	doc, stored := m.hs.Policy()
	require.True(t, stored, "the policy was written")
	require.Contains(t, doc, mesh.DefaultMeshTag)
	require.Contains(t, doc, "autogroup:member")
	require.Positive(t, m.hs.Count("GET", "/api/v1/policy"), "and read back")
	s := m.settings()
	require.Equal(t, mesh.ModeManaged, s.AclMode)
	require.Equal(t, mesh.StateApplied, s.AclState)
	m.requireRunning()

	// The wiring also allows the mesh tag, or the mesh could not be enabled.
	integ, err := m.ts.Get(m.ctx)
	require.NoError(t, err)
	require.Contains(t, integ.AllowedTags, mesh.DefaultMeshTag)

	var probes, unpaused int
	require.NoError(t, m.pool.QueryRow(bg, `SELECT count(*), count(*) FILTER (WHERE NOT paused) FROM migration_probe`).Scan(&probes, &unpaused))
	require.Positive(t, probes)
	require.Zero(t, unpaused, "the service was never changed before the pause was set")

	// Idempotent: a second run finds a migrated Headscale and does nothing.
	writes := len(m.hs.PolicyHistory())
	m.migrate()
	m.migrate()
	require.Equal(t, 1, m.dep.deploys(), "no second redeploy")
	require.Len(t, m.hs.PolicyHistory(), writes, "the policy was not written again")
	require.Equal(t, platform.StatusReady, m.row(m.id).Status)
	m.requireRunning()
	require.NoError(t, m.mesh.ApplyACL(bg, m.ws.ID))
	require.Len(t, m.hs.PolicyHistory(), writes)
}

// The new configuration is refused: the mesh stays paused with the reason, the
// service is untouched and nothing is deployed; a later run retries and succeeds.
func TestMigrationFailureWhenTheConfigurationIsRefusedStaysPausedAndRetries(t *testing.T) {
	m := newMigration(t)
	_, err := m.pool.Exec(bg, `UPDATE platform_services SET public_url = 'http://' || repeat('a', 70000) WHERE id = $1`, m.id)
	require.NoError(t, err)
	genBefore := m.service().Generation

	m.migrate()
	m.requirePaused("failed", "refused", "Retry")
	require.Equal(t, platform.StatusReady, m.row(m.id).Status, "the platform service is still ready")
	require.Equal(t, genBefore, m.service().Generation)
	require.Contains(t, m.files(), legacyPolicyPath, "the configuration was not changed")
	require.Zero(t, m.dep.deploys())

	_, err = m.pool.Exec(bg, `UPDATE platform_services SET public_url = $2 WHERE id = $1`, m.id, m.hs.URL)
	require.NoError(t, err)
	m.migrate()
	require.Equal(t, platform.MigrationPausedReason, m.settings().PausedReason, "the failure is cleared while it is retried")
	m.requireMigrated()
	_, done := m.drive()
	require.True(t, done)
	m.requireRunning()
	require.Equal(t, mesh.StateApplied, m.settings().AclState)
}

// The redeploy fails: the mesh stays paused and says why; Retry on the platform
// service resumes the migration.
func TestMigrationFailureWhenTheRedeployFailsStaysPausedAndRetries(t *testing.T) {
	m := newMigration(t)
	m.dep.setFail(true)
	m.migrate()
	_, done := m.drive()
	require.True(t, done)
	row := m.row(m.id)
	require.Equal(t, platform.StatusFailed, row.Status)
	m.requirePaused("failed", "The deployment failed", "Retry")
	require.Equal(t, meshtest.PolicyModeFile, m.hs.PolicyMode())
	_, wrote := m.hs.Policy()
	require.False(t, wrote)

	// A re-run while it is failed does not start a second migration.
	m.migrate()
	require.Equal(t, 1, m.dep.deploys())
	m.requirePaused("failed")

	m.dep.setFail(false)
	v, err := m.svc.Retry(m.ctx, m.id)
	require.NoError(t, err)
	require.Equal(t, platform.StepDeploy, v.Step)
	m.requirePaused()
	_, done = m.drive()
	require.True(t, done)
	require.Equal(t, platform.StatusReady, m.row(m.id).Status, m.row(m.id).StatusMessage)
	m.requireRunning()
	_, stored := m.hs.Policy()
	require.True(t, stored)
}

// The policy cannot be written yet: the wiring waits (still paused, retried by
// the job); when it never works the platform service fails, the pause says why,
// and Retry finishes it.
func TestMigrationFailureWhenThePolicyCannotBeWrittenStaysPausedAndRetries(t *testing.T) {
	m := newMigration(t)
	m.migrate()

	// Wiring step: the first policy write fails once, then works.
	m.hs.FailNext("PUT", "/api/v1/policy", 500)
	wait, done := m.drive()
	require.False(t, done)
	require.Positive(t, wait)
	require.Contains(t, m.row(m.id).StatusMessage, "write the Headscale policy")
	m.requirePaused()
	require.NotEqual(t, mesh.StateApplied, m.settings().AclState)
	_, done = m.drive()
	require.True(t, done)
	m.requireRunning()
	require.Equal(t, mesh.StateApplied, m.settings().AclState)
}

func TestMigrationFailureWhenHeadscaleNeverAnswersTimesOutPausedAndRetries(t *testing.T) {
	m := newMigration(t)
	m.migrate()
	m.hs.SetDown(true)
	wait, done := m.drive()
	require.False(t, done)
	require.Positive(t, wait)
	m.requirePaused()

	m.skew = 20 * time.Minute
	_, done = m.drive()
	require.True(t, done)
	require.Equal(t, platform.StatusFailed, m.row(m.id).Status)
	m.requirePaused("failed", "Timed out", "Retry")

	m.hs.SetDown(false)
	m.skew = 0
	_, err := m.svc.Retry(m.ctx, m.id)
	require.NoError(t, err)
	_, done = m.drive()
	require.True(t, done)
	require.Equal(t, platform.StatusReady, m.row(m.id).Status, m.row(m.id).StatusMessage)
	m.requireRunning()
}

// The mesh is in use when the migration wires Headscale again: the integration
// edit (same tailnet, mesh tag allowed) must not be refused as "in use".
func TestMigrationWiringWorksWhileTheMeshHasNodes(t *testing.T) {
	m := newMigration(t)
	m.seedNode()
	m.migrate()
	_, done := m.drive()
	require.True(t, done)
	require.Equal(t, platform.StatusReady, m.row(m.id).Status, m.row(m.id).StatusMessage)
	m.requireRunning()
	require.Equal(t, "online", m.nodeRow().State)
}

// The configuration was staged but the redeploy never started (the restart
// failed after it): the pause must not be lifted while Headscale still runs the
// old mode; the next run starts the redeploy.
func TestMigrationStagedButNotRedeployedKeepsThePauseAndRedeploys(t *testing.T) {
	m := newMigration(t)
	require.NoError(t, m.q.EnsureMeshSettings(bg, m.ws.ID))
	require.NoError(t, m.q.SetMeshPaused(bg, db.SetMeshPausedParams{WorkspaceID: m.ws.ID, Paused: true, PausedReason: platform.MigrationPausedReason}))
	m.setConfig([]services.FileMount{{Path: legacyConfigPath, Mode: "0644", Content: "server_url: x\npolicy:\n  mode: database\n"}})
	require.Greater(t, m.service().Generation, m.service().AppliedGeneration)

	m.migrate()
	m.requirePaused()
	row := m.row(m.id)
	require.Equal(t, platform.StatusProvisioning, row.Status)
	require.Equal(t, platform.StepDeploy, row.Step, "the redeploy was started")
	_, done := m.drive()
	require.True(t, done)
	require.Equal(t, []bool{true}, m.dep.pausedAtDeploy)
	m.requireRunning()
}

// A finished migration whose pause outlived a crash is lifted; an operator's own
// pause never is; a fresh install (nothing to migrate) is left alone.
func TestMigrationLiftsOnlyItsOwnStrayPause(t *testing.T) {
	m := newMigration(t)
	m.setConfig([]services.FileMount{{Path: legacyConfigPath, Mode: "0644", Content: "server_url: x\npolicy:\n  mode: database\n"}})
	_, err := m.pool.Exec(bg, `UPDATE services SET applied_generation = generation WHERE id = $1`, m.service().ID)
	require.NoError(t, err)

	require.NoError(t, m.q.EnsureMeshSettings(bg, m.ws.ID))
	require.NoError(t, m.q.SetMeshPaused(bg, db.SetMeshPausedParams{WorkspaceID: m.ws.ID, Paused: true, PausedReason: "operator maintenance"}))
	m.migrate()
	require.True(t, m.settings().Paused)
	require.Equal(t, "operator maintenance", m.settings().PausedReason)
	require.Zero(t, m.dep.deploys())

	require.NoError(t, m.q.SetMeshPaused(bg, db.SetMeshPausedParams{WorkspaceID: m.ws.ID, Paused: true, PausedReason: platform.MigrationPausedReason}))
	m.migrate()
	m.requireRunning()
	require.Zero(t, m.dep.deploys(), "nothing to redeploy")
	require.Equal(t, platform.StatusReady, m.row(m.id).Status)
}

func TestMigrationSkipsAFreshHeadscaleAndCreatesNoMeshSettings(t *testing.T) {
	m := newMigration(t)
	m.setConfig([]services.FileMount{{Path: legacyConfigPath, Mode: "0644", Content: "server_url: x\npolicy:\n  mode: database\n"}})
	_, err := m.pool.Exec(bg, `UPDATE services SET applied_generation = generation WHERE id = $1`, m.service().ID)
	require.NoError(t, err)
	m.migrate()
	var n int
	require.NoError(t, m.pool.QueryRow(bg, `SELECT count(*) FROM mesh_settings`).Scan(&n))
	require.Zero(t, n)
	require.Zero(t, m.dep.deploys())
	require.Equal(t, platform.StatusReady, m.row(m.id).Status)
}
