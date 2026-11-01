package mesh_test

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/mesh"
	"github.com/nebulactrl/nebulactrl/internal/mesh/meshtest"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var bg = context.Background()

// realDisp stores commands the way the hub does, so payload scans see what is
// persisted.
type realDisp struct{}

func (realDisp) Enqueue(ctx context.Context, tx pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	id := uuid.New()
	_, err := db.New(tx).InsertAgentCommand(ctx, db.InsertAgentCommandParams{
		ID: id, ServerID: c.ServerID, Kind: c.Kind, Payload: c.Payload, IdempotencyKey: id.String(), ExpiresAt: time.Now().Add(15 * time.Minute),
	})
	return id, err
}

func (realDisp) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, nil
}

type fixture struct {
	t          *testing.T
	pool       *pgxpool.Pool
	q          *db.Queries
	ws         uuid.UUID
	env1, env2 db.Environment
	a, b       db.Server
	hs         *meshtest.Headscale
	ts         *tailscale.Service
	svc        *mesh.Service
	admin      context.Context
}

func newFixture(t *testing.T, hsOpts ...meshtest.HeadscaleOption) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	f := &fixture{t: t, pool: pool, q: q, ws: ws.ID}
	f.env1, err = q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	f.env2, err = q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	f.a, err = q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	f.b, err = q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent2"})
	require.NoError(t, err)
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapMesh}, TunDevice: true})
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online', facts = $1", facts)
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	jm := jobs.New(pool, nil)
	f.hs = meshtest.NewHeadscale(t, hsOpts...)
	f.ts = tailscale.NewService(pool, box, jm, nil)
	f.ts.SetBaseURL("", f.hs.Client())
	tailscale.RegisterJobs(jm, f.ts)
	f.svc = mesh.NewService(mesh.Deps{Pool: pool, Bus: events.NewBus(), Jobs: jm, Dispatcher: realDisp{}, Tailscale: f.ts})
	mesh.RegisterJobs(jm, f.svc)
	f.admin = auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "t", Scopes: policy.AllScopes})
	_, err = f.ts.Put(f.admin, tailscale.PutInput{
		Provider: tailscale.ProviderHeadscale, LoginServer: f.hs.URL, ClientSecret: f.hs.APIKey(), HeadscaleCredential: tailscale.HeadscaleAPIKey,
		DefaultTags: []string{"tag:nebula-svc"}, AllowedTags: []string{"tag:nebula-svc", mesh.DefaultMeshTag},
	})
	require.NoError(t, err)
	return f
}

func (f *fixture) syncMeshCommands() []db.AgentCommand {
	var out []db.AgentCommand
	rows, err := f.pool.Query(bg, "SELECT id FROM agent_commands WHERE kind = $1 ORDER BY created_at", agentprotocol.KindSyncMesh)
	require.NoError(f.t, err)
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		require.NoError(f.t, rows.Scan(&id))
		ids = append(ids, id)
	}
	for _, id := range ids {
		c, err := f.q.GetAgentCommand(bg, id)
		require.NoError(f.t, err)
		out = append(out, c)
	}
	return out
}

func (f *fixture) enable(srv db.Server) {
	_, err := f.svc.SetServerMesh(f.admin, srv.ID, mesh.ServerMeshInput{Enabled: true, AcceptMeshChanges: true})
	require.NoError(f.t, err)
}

func TestEnablingNeedsConfirmationAndTheKeyIsMintedOnlyWhenSent(t *testing.T) {
	f := newFixture(t)
	plan, err := f.svc.SetServerMesh(f.admin, f.a.ID, mesh.ServerMeshInput{Enabled: true, DryRun: true})
	require.NoError(t, err)
	require.Equal(t, 1, plan.Plan.DevicesAdded)
	require.False(t, plan.Applied)
	require.Empty(t, plan.Plan.Blockers)

	_, err = f.svc.SetServerMesh(f.admin, f.a.ID, mesh.ServerMeshInput{Enabled: true})
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, apierr.CodeMeshConfirmationRequired, ae.Code)
	require.Empty(t, f.syncMeshCommands(), "nothing was sent without the confirmation")

	f.enable(f.a)
	cmds := f.syncMeshCommands()
	require.Len(t, cmds, 1)
	require.Empty(t, f.hs.Keys(), "no key exists before the command is sent")

	var p agentprotocol.SyncMeshPayload
	require.NoError(t, json.Unmarshal(cmds[0].Payload, &p))
	require.Len(t, p.Nodes, 1)
	require.True(t, p.Nodes[0].Provision)
	require.Equal(t, []string{mesh.DefaultMeshTag}, p.Nodes[0].Tags)

	secrets, err := f.svc.SecretsForCommand(bg, cmds[0].Kind, cmds[0].Payload)
	require.NoError(t, err)
	key := secrets[agentprotocol.SecretMeshKeyPrefix+f.a.ID.String()]
	require.NotEmpty(t, key)
	require.Len(t, f.hs.ActiveKeys(), 1, "one single-use key was minted when the command was sent")

	// The key never reaches agent_commands.
	for _, c := range f.syncMeshCommands() {
		require.NotContains(t, string(c.Payload), key)
	}
	var all string
	require.NoError(t, f.pool.QueryRow(bg, "SELECT coalesce(string_agg(payload::text || coalesce(error,''), ' '), '') FROM agent_commands").Scan(&all))
	require.NotContains(t, all, key)
	var audits string
	require.NoError(t, f.pool.QueryRow(bg, "SELECT coalesce(string_agg(metadata::text, ' '), '') FROM audit_events").Scan(&audits))
	require.NotContains(t, audits, key)

	// A set without Provision mints nothing.
	p.Nodes[0].Provision = false
	raw, _ := json.Marshal(p)
	none, err := f.svc.SecretsForCommand(bg, agentprotocol.KindSyncMesh, raw)
	require.NoError(t, err)
	require.Empty(t, none)
	require.Len(t, f.hs.ActiveKeys(), 1)
}

func (f *fixture) twoServices() (uuid.UUID, uuid.UUID) {
	f.enable(f.a)
	f.enable(f.b)
	mk := func(env db.Environment, slug string, srv db.Server) uuid.UUID {
		cfg := json.RawMessage(`{"image":"postgres:17","port":5432}`)
		sv, err := f.q.InsertService(bg, db.InsertServiceParams{ID: uuid.New(), EnvironmentID: env.ID, Name: slug, Slug: slug, Kind: "database", SourceType: "image", Config: cfg, ServerID: &srv.ID})
		require.NoError(f.t, err)
		return sv.ID
	}
	return mk(f.env1, "pg1", f.b), mk(f.env2, "pg2", f.b)
}

func TestGrantRejectsOtherEnvironment(t *testing.T) {
	f := newFixture(t)
	pg1, pg2 := f.twoServices()
	var link mesh.Link
	var exp1, exp2 mesh.Exposure
	require.NoError(t, db.InTx(bg, f.pool, func(tx pgx.Tx) error {
		var err error
		if exp1, err = f.svc.EnsureExposure(bg, tx, pg1, agentprotocol.MeshPurposeService, 5432); err != nil {
			return err
		}
		if exp2, err = f.svc.EnsureExposure(bg, tx, pg2, agentprotocol.MeshPurposeService, 5432); err != nil {
			return err
		}
		link, err = f.svc.EnsureLink(bg, tx, f.env1.ID, f.a.ID, f.b.ID, agentprotocol.MeshPurposeService)
		return err
	}))
	require.Equal(t, 5432, exp1.MeshPort, "the container port is kept when free")
	require.NotEqual(t, 5432, exp2.MeshPort, "a second provider on the same node gets a 15xxx port")

	err := db.InTx(bg, f.pool, func(tx pgx.Tx) error {
		return f.svc.Grant(bg, tx, link.ID, exp2.ID, uuid.New(), "reference")
	})
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, apierr.CodeMeshEnvironmentMismatch, ae.Code)

	// The same environment works; a replica source on a service link does not.
	require.NoError(t, db.InTx(bg, f.pool, func(tx pgx.Tx) error {
		return f.svc.Grant(bg, tx, link.ID, exp1.ID, uuid.New(), "reference")
	}))
	err = db.InTx(bg, f.pool, func(tx pgx.Tx) error { return f.svc.Grant(bg, tx, link.ID, exp1.ID, uuid.New(), "replica") })
	ae, ok = apierr.As(err)
	require.True(t, ok)
	require.Equal(t, apierr.CodeMeshPurposeMismatch, ae.Code)
}

func TestRevokingTheLastGrantReleasesLinkAndExposure(t *testing.T) {
	f := newFixture(t)
	pg1, _ := f.twoServices()
	ref := uuid.New()
	var link mesh.Link
	var exp mesh.Exposure
	require.NoError(t, db.InTx(bg, f.pool, func(tx pgx.Tx) error {
		var err error
		exp, err = f.svc.EnsureExposure(bg, tx, pg1, agentprotocol.MeshPurposeService, 5432)
		if err != nil {
			return err
		}
		if link, err = f.svc.EnsureLink(bg, tx, f.env1.ID, f.a.ID, f.b.ID, agentprotocol.MeshPurposeService); err != nil {
			return err
		}
		return f.svc.Grant(bg, tx, link.ID, exp.ID, ref, "reference")
	}))
	require.NoError(t, db.InTx(bg, f.pool, func(tx pgx.Tx) error { return f.svc.Release(bg, tx, ref) }))
	links, err := f.q.ListMeshLinks(bg, f.ws)
	require.NoError(t, err)
	require.Empty(t, links)
	exps, err := f.q.ListMeshExposures(bg, f.ws)
	require.NoError(t, err)
	require.Empty(t, exps, "the exposure had no other grant: it is released with the link")
}

func TestIntegrationInUseWhileTheMeshIsEnabled(t *testing.T) {
	f := newFixture(t)
	f.enable(f.a)
	err := f.ts.Delete(f.admin)
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, apierr.CodeIntegrationInUse, ae.Code)
	_, err = f.ts.Put(f.admin, tailscale.PutInput{
		Provider: tailscale.ProviderHeadscale, LoginServer: "https://other.example.com", ClientSecret: "hskey-api-other-0000", HeadscaleCredential: tailscale.HeadscaleAPIKey,
		DefaultTags: []string{"tag:nebula-svc"}, AllowedTags: []string{"tag:nebula-svc", mesh.DefaultMeshTag},
	})
	ae, ok = apierr.As(err)
	require.True(t, ok)
	require.Equal(t, apierr.CodeIntegrationInUse, ae.Code, "changing the login server is refused too")
}

func TestHeadscaleInFileModeFallsBackToAdvisory(t *testing.T) {
	f := newFixture(t, meshtest.WithPolicyMode(meshtest.PolicyModeFile))
	v, err := f.svc.UpdateSettings(f.admin, mesh.SettingsInput{ACLMode: new(mesh.ModeManaged)})
	require.NoError(t, err)
	require.Equal(t, mesh.ModeManaged, v.ACLMode)
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	v, err = f.svc.View(f.admin)
	require.NoError(t, err)
	require.Equal(t, mesh.ModeAdvisory, v.ACLMode, "the PUT answered `policy update is disabled`")
	require.Equal(t, mesh.StateAdvisory, v.ACLState)
	_, wrote := f.hs.Policy()
	require.False(t, wrote)
}

func TestManagedHeadscalePolicyIsWrittenAndReadBack(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.svc.ApplyPolicy(bg, f.ws))
	doc, ok := f.hs.Policy()
	require.True(t, ok)
	require.Contains(t, doc, mesh.DefaultMeshTag)
	require.Contains(t, doc, "autogroup:member")
	v, err := f.svc.View(f.admin)
	require.NoError(t, err)
	require.Equal(t, mesh.StateApplied, v.ACLState)
	n := len(f.hs.PolicyHistory())
	require.NoError(t, f.svc.ApplyACL(bg, f.ws))
	require.Len(t, f.hs.PolicyHistory(), n, "an unchanged policy is not written again")
}

func TestRevokingAServerDeletesItsDevicesAndRegeneratesTheACL(t *testing.T) {
	f := newFixture(t)
	f.enable(f.a)
	joined := f.hs.JoinNode("nc-agent1", mesh.DefaultMeshTag)
	_, err := f.pool.Exec(bg, "UPDATE server_mesh SET device_id = $1, ips = $2, state = 'online'", joined.ID, joined.IPs)
	require.NoError(t, err)

	f.svc.ServerRevoked(bg, f.ws, f.a.ID)
	_, err = f.q.GetServerMesh(bg, f.a.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	var n int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM river_job WHERE kind = 'mesh_acl_apply'").Scan(&n))
	require.Equal(t, 1, n, "the ACL regenerates: the addresses vanished")
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM river_job WHERE kind = 'tailnet_delete_device'").Scan(&n))
	require.Equal(t, 1, n, "the device is deleted from the tailnet in the background")
	var action string
	require.NoError(t, f.pool.QueryRow(bg, "SELECT action FROM audit_events WHERE action = 'mesh.node.remove'").Scan(&action))
}

func TestUnusedKeyIsRevokedByTheSweeper(t *testing.T) {
	f := newFixture(t)
	f.enable(f.a)
	cmds := f.syncMeshCommands()
	_, err := f.svc.SecretsForCommand(bg, cmds[0].Kind, cmds[0].Payload)
	require.NoError(t, err)
	row, err := f.q.GetServerMesh(bg, f.a.ID)
	require.NoError(t, err)
	require.NotEmpty(t, row.AuthKeyID)
	require.Len(t, f.hs.ActiveKeys(), 1)

	require.NoError(t, f.svc.SweepKey(bg, mesh.KeySweepArgs{NodeID: f.a.ID, NodeKind: agentprotocol.MeshKindServer, KeyID: row.AuthKeyID}))
	require.Empty(t, f.hs.ActiveKeys(), "the key that never joined is revoked")
	row, err = f.q.GetServerMesh(bg, f.a.ID)
	require.NoError(t, err)
	require.Equal(t, "needs_attention", row.State)
	require.Empty(t, row.AuthKeyID)
}

func TestSyncResultAppliesNodeStateAndAHeartbeatKeepsIt(t *testing.T) {
	f := newFixture(t)
	f.enable(f.a)
	cmds := f.syncMeshCommands()
	st, _ := json.Marshal(agentprotocol.MeshStatus{TunDevice: true, Nodes: []agentprotocol.MeshNodeStatus{{
		NodeID: f.a.ID.String(), Kind: agentprotocol.MeshKindServer, State: agentprotocol.MeshStateOnline, FQDN: "nc-agent1.tailnet.internal", IPs: []string{"100.64.0.1"}, DeviceID: "7",
	}}})
	require.NoError(t, f.svc.HandleResult(bg, f.a.ID, cmds[0].ID, agentprotocol.ResultFrame{CommandID: cmds[0].ID.String(), Outcome: agentprotocol.OutcomeSucceeded, Data: st}))
	row, err := f.q.GetServerMesh(bg, f.a.ID)
	require.NoError(t, err)
	require.Equal(t, "online", row.State)
	require.Equal(t, []string{"100.64.0.1"}, row.Ips)
	require.False(t, row.NeedsReprovision, "the spec was applied")

	// A heartbeat that reports a node the control plane does not desire is a stray: a forced sync is queued.
	require.NoError(t, f.svc.ObserveHeartbeat(bg, f.a.ID, &agentprotocol.MeshStatus{Nodes: []agentprotocol.MeshNodeStatus{
		{NodeID: uuid.NewString(), Kind: agentprotocol.MeshKindLink, State: agentprotocol.MeshStateOnline},
	}}))
	var n int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM river_job WHERE kind = 'mesh_sync'").Scan(&n))
	require.GreaterOrEqual(t, n, 1)
}
