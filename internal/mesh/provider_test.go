package mesh_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/mesh"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func (f *fixture) inTx(fn func(tx pgx.Tx) error) error {
	return db.InTx(bg, f.pool, fn)
}

// service inserts a service of the given kind; server nil leaves it unplaced.
func (f *fixture) service(env db.Environment, slug, kind string, server *db.Server) db.Service {
	f.t.Helper()
	var sid *uuid.UUID
	if server != nil {
		sid = &server.ID
	}
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: env.ID, Name: slug, Slug: slug, Kind: kind, SourceType: "image",
		Config: json.RawMessage(`{"image":"postgres:17","port":5432}`), ServerID: sid,
	})
	require.NoError(f.t, err)
	return sv
}

func (f *fixture) ensureExposure(svc uuid.UUID, purpose string, port int) (mesh.Exposure, error) {
	var exp mesh.Exposure
	err := f.inTx(func(tx pgx.Tx) error {
		var err error
		exp, err = f.svc.EnsureExposure(bg, tx, svc, purpose, port)
		return err
	})
	return exp, err
}

func (f *fixture) ensureLink(env, local, remote uuid.UUID, purpose string) (mesh.Link, error) {
	var l mesh.Link
	err := f.inTx(func(tx pgx.Tx) error {
		var err error
		l, err = f.svc.EnsureLink(bg, tx, env, local, remote, purpose)
		return err
	})
	return l, err
}

func (f *fixture) plan(consumer, provider uuid.UUID, purpose string) (mesh.LinkPlan, error) {
	return f.svc.PlanLink(bg, f.q, f.env1.ID, consumer, provider, purpose)
}

func linkBlockerCodes(p mesh.LinkPlan) []string {
	out := []string{}
	for _, b := range p.Blockers {
		out = append(out, b.Code)
	}
	return out
}

func TestAllocPortKeepsTheContainerPortForServicesAndFallsBackToTheMeshRange(t *testing.T) {
	svc, repl := agentprotocol.MeshPurposeService, agentprotocol.MeshPurposeReplication
	p, ok := mesh.AllocPort(svc, 5432, nil)
	require.True(t, ok)
	require.Equal(t, 5432, p, "a service keeps its port while it is free on the node")
	p, ok = mesh.AllocPort(svc, 5432, []int32{5432})
	require.True(t, ok)
	require.Equal(t, agentprotocol.MeshPortMin, p, "a taken port falls back to the first free one of the range")
	p, ok = mesh.AllocPort(svc, 5432, []int32{5432, int32(agentprotocol.MeshPortMin), int32(agentprotocol.MeshPortMin) + 1})
	require.True(t, ok)
	require.Equal(t, agentprotocol.MeshPortMin+2, p)
	p, ok = mesh.AllocPort(repl, 5432, nil)
	require.True(t, ok)
	require.Equal(t, agentprotocol.MeshPortMin, p, "replication never reuses the container port")

	used := make([]int32, 0, agentprotocol.MeshPortMax-agentprotocol.MeshPortMin+1)
	for port := agentprotocol.MeshPortMin; port <= agentprotocol.MeshPortMax; port++ {
		used = append(used, int32(port))
	}
	_, ok = mesh.AllocPort(repl, 5432, used)
	require.False(t, ok, "a full range has no port left")
	p, ok = mesh.AllocPort(svc, 5432, used)
	require.True(t, ok)
	require.Equal(t, 5432, p, "a service still keeps its own free port when the range is full")
}

func TestPlanLinkCountsTheDeviceAWarnsAboutAdvisoryAclAndNamesEveryBlocker(t *testing.T) {
	f := newFixture(t)
	f.enable(f.a)
	f.enable(f.b)
	svc := agentprotocol.MeshPurposeService

	_, err := f.plan(f.a.ID, f.b.ID, "bogus")
	requireAPICode(t, err, "invalid_purpose")

	_, err = f.svc.PlanLink(bg, f.q, uuid.New(), f.a.ID, f.b.ID, svc)
	require.Error(t, err, "an environment that does not exist cannot be planned for")

	p, err := f.plan(f.a.ID, f.b.ID, svc)
	require.NoError(t, err)
	require.Empty(t, p.Blockers)
	require.Equal(t, 1, p.DevicesAdded)
	require.False(t, p.LinkExists)
	require.Len(t, p.Warnings, 1)
	require.Contains(t, p.Warnings[0], "advisory")

	// A managed ACL needs no manual rule: no warning.
	f.setSettings("acl_mode = 'managed'")
	p, err = f.plan(f.a.ID, f.b.ID, svc)
	require.NoError(t, err)
	require.Empty(t, p.Warnings)
	f.setSettings("acl_mode = 'advisory'")

	// The same link in the other direction or for the other purpose is a different link.
	_, err = f.ensureLink(f.env1.ID, f.a.ID, f.b.ID, svc)
	require.NoError(t, err)
	p, err = f.plan(f.a.ID, f.b.ID, svc)
	require.NoError(t, err)
	require.True(t, p.LinkExists)
	require.Zero(t, p.DevicesAdded)
	require.Empty(t, p.Warnings)
	p, err = f.plan(f.b.ID, f.a.ID, svc)
	require.NoError(t, err)
	require.False(t, p.LinkExists)
	p, err = f.plan(f.a.ID, f.b.ID, agentprotocol.MeshPurposeReplication)
	require.NoError(t, err)
	require.False(t, p.LinkExists)

	// A server of another team, or one that does not exist, is not found.
	p, err = f.plan(f.a.ID, uuid.New(), svc)
	require.NoError(t, err)
	require.Contains(t, linkBlockerCodes(p), "not_found")

	// A server without a node blocks, naming it.
	c, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.ws, Name: "agent3"})
	require.NoError(t, err)
	f.setFacts(c, agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapMesh}, TunDevice: true})
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", c.ID)
	require.NoError(t, err)
	p, err = f.plan(f.a.ID, c.ID, svc)
	require.NoError(t, err)
	require.Equal(t, []string{apierr.CodeMeshServerRequired}, linkBlockerCodes(p))
	require.Contains(t, p.Blockers[0].Message, "agent3")

	// The consumer needs /dev/net/tun (its link node), the provider does not.
	f.setFacts(f.a, agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapMesh}})
	p, err = f.plan(f.a.ID, f.b.ID, svc)
	require.NoError(t, err)
	require.Equal(t, []string{apierr.CodeMeshNoTun}, linkBlockerCodes(p))
	p, err = f.plan(f.b.ID, f.a.ID, svc)
	require.NoError(t, err)
	require.Empty(t, p.Blockers)
	f.setFacts(f.a, agentprotocol.Facts{OS: "linux", TunDevice: true})
	p, err = f.plan(f.b.ID, f.a.ID, svc)
	require.NoError(t, err)
	require.Equal(t, []string{apierr.CodeMeshAgentTooOld}, linkBlockerCodes(p), "either side's agent must manage mesh nodes")
	f.setFacts(f.a, agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapMesh}, TunDevice: true})

	f.setSettings("paused = true, paused_reason = 'maintenance', coordinator_ok = false")
	p, err = f.plan(f.a.ID, f.b.ID, svc)
	require.NoError(t, err)
	require.Equal(t, []string{apierr.CodeMeshPaused, apierr.CodeMeshCoordinatorDown}, linkBlockerCodes(p))

	_, err = f.pool.Exec(bg, "DELETE FROM tailnet_integrations")
	require.NoError(t, err)
	p, err = f.plan(f.a.ID, f.b.ID, svc)
	require.NoError(t, err)
	require.Equal(t, []string{apierr.CodeMeshNotConfigured, apierr.CodeMeshPaused, apierr.CodeMeshCoordinatorDown}, linkBlockerCodes(p))
}

func TestEnsureExposureRefusesWhatCannotBeReachedOverTheMesh(t *testing.T) {
	f := newFixture(t)
	f.enable(f.b)
	svc := agentprotocol.MeshPurposeService
	pg := f.service(f.env1, "pg", "database", &f.b)

	_, err := f.ensureExposure(pg.ID, "bogus", 5432)
	requireAPICode(t, err, "invalid_purpose")
	_, err = f.ensureExposure(uuid.New(), svc, 5432)
	requireAPICode(t, err, "not_found")
	for _, kind := range []string{"route", "stack"} {
		_, err = f.ensureExposure(f.service(f.env1, "x-"+kind, kind, &f.b).ID, svc, 80)
		requireAPICode(t, err, apierr.CodeMeshNotExposable)
		require.Contains(t, err.Error(), "no single TCP port")
	}
	for _, port := range []int{0, -1, 65536} {
		_, err = f.ensureExposure(pg.ID, svc, port)
		requireAPICode(t, err, apierr.CodeMeshNotExposable)
		require.Contains(t, err.Error(), "no TCP port", "port %d", port)
	}
	_, err = f.ensureExposure(f.service(f.env1, "floating", "app", nil).ID, svc, 80)
	requireAPICode(t, err, apierr.CodeMeshNotExposable)
	require.Contains(t, err.Error(), "not placed on a server")

	// The server must have its node: the error names the server.
	_, err = f.ensureExposure(f.service(f.env1, "on-a", "app", &f.a).ID, svc, 80)
	requireAPICode(t, err, apierr.CodeMeshServerRequired)
	require.Contains(t, err.Error(), f.a.Name)

	// A deleted service is gone.
	gone := f.service(f.env1, "gone", "database", &f.b)
	_, err = f.pool.Exec(bg, "UPDATE services SET deleted_at = now() WHERE id = $1", gone.ID)
	require.NoError(t, err)
	_, err = f.ensureExposure(gone.ID, svc, 5432)
	requireAPICode(t, err, "not_found")
	require.Zero(t, f.count("SELECT count(*) FROM mesh_exposures"), "refusals create no exposure")
}

func TestEnsureExposureAllocatesPortsKeepsThemAndFollowsAServiceThatMoves(t *testing.T) {
	f := newFixture(t)
	f.enable(f.a)
	f.enable(f.b)
	svc, repl := agentprotocol.MeshPurposeService, agentprotocol.MeshPurposeReplication
	pg := f.service(f.env1, "pg", "database", &f.b)

	first, err := f.ensureExposure(pg.ID, svc, 5432)
	require.NoError(t, err)
	require.Equal(t, 5432, first.MeshPort)
	require.Equal(t, f.b.ID, first.ServerID)
	require.Equal(t, f.env1.ID, first.EnvironmentID)
	again, err := f.ensureExposure(pg.ID, svc, 5432)
	require.NoError(t, err)
	require.Equal(t, first.ID, again.ID, "ensuring twice returns the same exposure")
	require.Equal(t, 1, f.count("SELECT count(*) FROM mesh_exposures"))

	// The replication exposure of the same service is its own, in the 15xxx range.
	r, err := f.ensureExposure(pg.ID, repl, 5432)
	require.NoError(t, err)
	require.NotEqual(t, first.ID, r.ID)
	require.Equal(t, agentprotocol.MeshPortMin, r.MeshPort)

	// Exposing sends the server's node the new set (forced: serve forwards change).
	forced := "SELECT count(*) FROM river_job WHERE kind = 'mesh_sync' AND args->>'serverId' = $1 AND (args->>'force')::boolean"
	require.Positive(t, f.count(forced, f.b.ID.String()))

	// When the service moves to the other server its old exposure (with grants) is released and a new one is made there.
	_, err = f.pool.Exec(bg, "UPDATE services SET server_id = $2 WHERE id = $1", pg.ID, f.a.ID)
	require.NoError(t, err)
	require.Zero(t, f.count(forced, f.a.ID.String()))
	moved, err := f.ensureExposure(pg.ID, svc, 5432)
	require.NoError(t, err)
	require.NotEqual(t, first.ID, moved.ID)
	require.Equal(t, f.a.ID, moved.ServerID)
	require.Positive(t, f.count(forced, f.a.ID.String()), "the new node serves it")
	exps, err := f.q.ListMeshExposures(bg, f.ws)
	require.NoError(t, err)
	require.Len(t, exps, 2, "the old service exposure is gone; the replication one remains")
	require.NotContains(t, []uuid.UUID{exps[0].ID, exps[1].ID}, first.ID)
}

func TestEnsureLinkValidatesItsEndsAndCreatesAProvisioningNode(t *testing.T) {
	f := newFixture(t)
	f.enable(f.a)
	svc := agentprotocol.MeshPurposeService

	_, err := f.ensureLink(f.env1.ID, f.a.ID, f.b.ID, "bogus")
	requireAPICode(t, err, "invalid_purpose")
	_, err = f.ensureLink(f.env1.ID, f.a.ID, f.a.ID, svc)
	requireAPICode(t, err, "invalid_link")
	_, err = f.ensureLink(uuid.New(), f.a.ID, f.b.ID, svc)
	requireAPICode(t, err, "not_found")
	_, err = f.ensureLink(f.env1.ID, f.a.ID, uuid.New(), svc)
	requireAPICode(t, err, "not_found")

	other, err := f.q.CreateWorkspace(bg, "other")
	require.NoError(t, err)
	stranger, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: other.ID, Name: "elsewhere"})
	require.NoError(t, err)
	_, err = f.ensureLink(f.env1.ID, f.a.ID, stranger.ID, svc)
	requireAPICode(t, err, "not_found")

	// The remote server has no node yet: named in the error.
	_, err = f.ensureLink(f.env1.ID, f.a.ID, f.b.ID, svc)
	requireAPICode(t, err, apierr.CodeMeshServerRequired)
	require.Contains(t, err.Error(), "agent2")
	f.enable(f.b)
	require.Zero(t, f.count("SELECT count(*) FROM mesh_links"), "every refusal left no link behind")

	f.setSettings("paused = true, paused_reason = 'maintenance'")
	_, err = f.ensureLink(f.env1.ID, f.a.ID, f.b.ID, svc)
	requireAPICode(t, err, apierr.CodeMeshPaused)
	f.setSettings("paused = false")

	before := f.count("SELECT count(*) FROM river_job WHERE kind = 'mesh_sync'")
	l, err := f.ensureLink(f.env1.ID, f.a.ID, f.b.ID, svc)
	require.NoError(t, err)
	require.Equal(t, "provisioning", l.State)
	require.Equal(t, f.a.ID, l.ServerID)
	require.Equal(t, f.b.ID, l.RemoteServerID)
	require.Equal(t, svc, l.Purpose)
	row, err := f.q.GetMeshLinkByID(bg, l.ID)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(row.Hostname, "nc-"+agentprotocol.Short(f.env1.ID.String())+"-agent1-to-agent2"), row.Hostname)
	require.True(t, row.NeedsReprovision)
	require.NotEmpty(t, row.IdentityHash)
	require.Equal(t, before+1, f.count("SELECT count(*) FROM river_job WHERE kind = 'mesh_sync'"))

	again, err := f.ensureLink(f.env1.ID, f.a.ID, f.b.ID, svc)
	require.NoError(t, err)
	require.Equal(t, l.ID, again.ID, "ensuring twice returns the same link")
	require.Equal(t, 1, f.count("SELECT count(*) FROM mesh_links"))
}

func TestGrantNeedsBothEndsAndTheLinkToReachTheExposuresServer(t *testing.T) {
	f := newLinkFixture(t)
	err := f.inTx(func(tx pgx.Tx) error { return f.svc.Grant(bg, tx, uuid.New(), f.exp.ID, uuid.New(), "reference") })
	requireAPICode(t, err, "not_found")
	err = f.inTx(func(tx pgx.Tx) error { return f.svc.Grant(bg, tx, f.link.ID, uuid.New(), uuid.New(), "reference") })
	requireAPICode(t, err, "not_found")

	// A link to b cannot reach an exposure on a, and an unknown source is no source of this purpose.
	onA := f.service(f.env1, "on-a", "database", &f.a)
	expA, err := f.ensureExposure(onA.ID, agentprotocol.MeshPurposeService, 5432)
	require.NoError(t, err)
	err = f.inTx(func(tx pgx.Tx) error { return f.svc.Grant(bg, tx, f.link.ID, expA.ID, uuid.New(), "reference") })
	requireAPICode(t, err, apierr.CodeMeshPurposeMismatch)
	err = f.inTx(func(tx pgx.Tx) error { return f.svc.Grant(bg, tx, f.link.ID, f.exp.ID, uuid.New(), "mystery") })
	requireAPICode(t, err, apierr.CodeMeshPurposeMismatch)
	require.Equal(t, 1, f.count("SELECT count(*) FROM mesh_grants"), "only the fixture's grant exists")

	// Canvas edges are service grants too.
	require.NoError(t, f.inTx(func(tx pgx.Tx) error { return f.svc.Grant(bg, tx, f.link.ID, f.exp.ID, uuid.New(), "edge") }))
	require.Equal(t, 2, f.count("SELECT count(*) FROM mesh_grants"))
}

func TestRevokingOneOfSeveralGrantsKeepsTheLinkAndTheLastOneReleasesItWithItsDevice(t *testing.T) {
	f := newLinkFixture(t)
	second := uuid.New()
	require.NoError(t, f.inTx(func(tx pgx.Tx) error { return f.svc.Grant(bg, tx, f.link.ID, f.exp.ID, second, "edge") }))
	_, err := f.pool.Exec(bg, "DELETE FROM river_job WHERE kind = 'mesh_acl_apply'")
	require.NoError(t, err)

	require.NoError(t, f.inTx(func(tx pgx.Tx) error { return f.svc.Revoke(bg, tx, f.link.ID, f.exp.ID, f.ref, "reference") }))
	require.Equal(t, 1, f.count("SELECT count(*) FROM mesh_grants"))
	require.Equal(t, 1, f.count("SELECT count(*) FROM mesh_links"), "another grant still needs the link")
	require.Equal(t, 1, f.count("SELECT count(*) FROM mesh_exposures"))
	require.Zero(t, f.count("SELECT count(*) FROM river_job WHERE kind = 'tailnet_delete_device'"))
	require.Equal(t, 1, f.count("SELECT count(*) FROM river_job WHERE kind = 'mesh_acl_apply'"), "the access rules regenerate")

	// Revoking a grant that does not exist changes nothing: the link is still needed.
	require.NoError(t, f.inTx(func(tx pgx.Tx) error { return f.svc.Revoke(bg, tx, f.link.ID, f.exp.ID, uuid.New(), "reference") }))
	require.Equal(t, 1, f.count("SELECT count(*) FROM mesh_grants"))

	require.NoError(t, f.inTx(func(tx pgx.Tx) error { return f.svc.Revoke(bg, tx, f.link.ID, f.exp.ID, second, "edge") }))
	require.Zero(t, f.count("SELECT count(*) FROM mesh_links"))
	require.Zero(t, f.count("SELECT count(*) FROM mesh_exposures"))
	require.Equal(t, 1, f.count("SELECT count(*) FROM river_job WHERE kind = 'tailnet_delete_device'"), "the link's device leaves the tailnet")
}

func TestReleaseOfAReferenceWithoutGrantsDoesNothing(t *testing.T) {
	f := newLinkFixture(t)
	require.NoError(t, f.inTx(func(tx pgx.Tx) error { return f.svc.Release(bg, tx, uuid.New()) }))
	require.Equal(t, 1, f.count("SELECT count(*) FROM mesh_grants"))
	require.Equal(t, 1, f.count("SELECT count(*) FROM mesh_links"))
}

func TestReadyExplainsWhyALinkIsNotUsableYet(t *testing.T) {
	f := newLinkFixture(t)
	r, err := f.svc.Ready(bg, uuid.New())
	require.NoError(t, err)
	require.False(t, r.Online)
	require.Equal(t, "the link does not exist", r.Reason)

	// Fresh from the first provisioning.
	r, err = f.svc.Ready(bg, f.link.ID)
	require.NoError(t, err)
	require.False(t, r.Online)
	require.Equal(t, "the link node is provisioning", r.Reason)
	_, err = f.pool.Exec(bg, "UPDATE mesh_links SET state = 'needs_attention', message = 'key expired' WHERE id = $1", f.link.ID)
	require.NoError(t, err)
	r, err = f.svc.Ready(bg, f.link.ID)
	require.NoError(t, err)
	require.Equal(t, "the link node is needs_attention: key expired", r.Reason)

	// Online and reported just now, but the advisory ACL has not been applied.
	_, err = f.pool.Exec(bg, "UPDATE mesh_links SET state = 'online', message = '', observed_at = now() WHERE id = $1", f.link.ID)
	require.NoError(t, err)
	f.setSettings("acl_mode = 'managed', acl_state = 'pending'")
	r, err = f.svc.Ready(bg, f.link.ID)
	require.NoError(t, err)
	require.True(t, r.Online)
	require.False(t, r.ACLOK)
	require.Equal(t, "the access rules are not applied yet (pending)", r.Reason)
	f.setSettings("acl_state = 'error', acl_message = 'policy rejected'")
	r, err = f.svc.Ready(bg, f.link.ID)
	require.NoError(t, err)
	require.Equal(t, "the access rules are not applied yet (error): policy rejected", r.Reason)

	// Applied, advisory (the connectivity check decides) and off all let traffic through.
	for _, set := range []string{"acl_state = 'applied'", "acl_mode = 'advisory', acl_state = 'advisory'", "acl_mode = 'off', acl_state = 'unknown'"} {
		f.setSettings(set)
		r, err = f.svc.Ready(bg, f.link.ID)
		require.NoError(t, err)
		require.True(t, r.Online && r.ACLOK, set)
		require.Empty(t, r.Reason, set)
	}

	// A node that stopped reporting is not online any more.
	f.svc.SetClock(func() time.Time { return time.Now().Add(2 * time.Minute) })
	r, err = f.svc.Ready(bg, f.link.ID)
	require.NoError(t, err)
	require.False(t, r.Online)
	require.Equal(t, "the link node has not reported for a while", r.Reason)
	f.svc.SetClock(time.Now)

	f.setSettings("paused = true, paused_reason = 'maintenance'")
	r, err = f.svc.Ready(bg, f.link.ID)
	require.NoError(t, err)
	require.Equal(t, "the mesh is paused: maintenance", r.Reason)
}

func TestCheckRunsTheLinksConnectivityCheckThroughTheAgent(t *testing.T) {
	f := newLinkFixture(t)
	disp := &answerDisp{status: "succeeded", outcome: agentprotocol.OutcomeSucceeded, result: &agentprotocol.MeshCheckResult{Result: agentprotocol.MeshCheckRefused, Message: "connection refused"}}
	svc := f.withAgent(disp)

	_, err := svc.Check(bg, uuid.New(), f.exp.ID)
	requireAPICode(t, err, "not_found")
	_, err = svc.Check(bg, f.link.ID, uuid.New())
	requireAPICode(t, err, "not_found")

	res, err := svc.Check(bg, f.link.ID, f.exp.ID)
	require.NoError(t, err)
	require.Equal(t, mesh.CheckResult{Result: agentprotocol.MeshCheckRefused, Message: "connection refused"}, res)
	require.Equal(t, []agentprotocol.MeshCheckPayload{{LinkID: f.link.ID.String(), Port: f.exp.MeshPort}}, disp.asked)
	var serverID uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg, "SELECT server_id FROM agent_commands WHERE kind = $1", agentprotocol.KindMeshCheck).Scan(&serverID))
	require.Equal(t, f.a.ID, serverID, "the check runs where the link node runs")

	// The agent answered with a failure: the path is not proven, with the agent's reason.
	disp.status, disp.outcome, disp.errMsg, disp.result = "failed", agentprotocol.OutcomeFailed, "container not running", nil
	res, err = svc.Check(bg, f.link.ID, f.exp.ID)
	require.NoError(t, err)
	require.Equal(t, mesh.CheckResult{Result: agentprotocol.MeshCheckNoPath, Message: "container not running"}, res)
	disp.errMsg, disp.cmdErr = "", "agent refused the command"
	res, err = svc.Check(bg, f.link.ID, f.exp.ID)
	require.NoError(t, err)
	require.Equal(t, mesh.CheckResult{Result: agentprotocol.MeshCheckNoPath, Message: "agent refused the command"}, res)

	// A succeeded command whose data cannot be read is no proof either.
	disp.status, disp.outcome, disp.cmdErr, disp.rawData = "succeeded", agentprotocol.OutcomeSucceeded, "", json.RawMessage(`"garbage"`)
	res, err = svc.Check(bg, f.link.ID, f.exp.ID)
	require.NoError(t, err)
	require.Equal(t, mesh.CheckResult{Result: agentprotocol.MeshCheckNoPath, Message: "unreadable result"}, res)

	// A command that expired or was canceled never reached the agent: a timeout that says the server is offline.
	for _, status := range []string{"expired", "canceled"} {
		disp.status, disp.outcome, disp.rawData = status, "", nil
		res, err = svc.Check(bg, f.link.ID, f.exp.ID)
		require.NoError(t, err, status)
		require.Equal(t, agentprotocol.MeshCheckTimeout, res.Result, status)
		require.Equal(t, mesh.ErrCheckNotDelivered.Error(), res.Message, status)
	}
}

func TestAwaitCommandEndsOnAnswerDeadlineOrCancellation(t *testing.T) {
	f := newFixture(t)
	svc := f.withAgent(realDisp{})
	enqueue := func() uuid.UUID {
		var id uuid.UUID
		require.NoError(t, f.inTx(func(tx pgx.Tx) error {
			var err error
			id, err = realDisp{}.Enqueue(bg, tx, agents.NewCommand{ServerID: f.a.ID, Kind: agentprotocol.KindMeshCheck, Payload: json.RawMessage(`{}`)})
			return err
		}))
		return id
	}

	// Nobody answers: the deadline ends the wait with the "no answer" sentinel.
	_, err := svc.AwaitCommand(bg, enqueue(), 30*time.Millisecond)
	require.ErrorIs(t, err, mesh.ErrCheckNoAnswer)

	// A caller that gives up ends it with its own error.
	ctx, cancel := context.WithCancel(bg)
	cancel()
	_, err = svc.AwaitCommand(ctx, enqueue(), time.Minute)
	require.ErrorIs(t, err, context.Canceled)

	// A command that finished while waiting is returned with its result.
	id := enqueue()
	go func() {
		time.Sleep(10 * time.Millisecond)
		_, _ = f.pool.Exec(bg, "UPDATE agent_commands SET status = 'failed', error = 'boom' WHERE id = $1", id)
	}()
	cmd, err := svc.AwaitCommand(bg, id, time.Minute)
	require.NoError(t, err)
	require.Equal(t, "failed", cmd.Status)
	require.Equal(t, "boom", cmd.Error)

	// A command that does not exist is an error, not a wait.
	_, err = svc.AwaitCommand(bg, uuid.New(), time.Minute)
	require.Error(t, err)
}

func TestRouteOfDescribesHowAConsumerReachesAProviderOnAnotherServer(t *testing.T) {
	f := newFixture(t)
	pg1, _ := f.twoServices()
	route := func(consumer uuid.UUID, provider uuid.UUID) (mesh.Route, bool) {
		t.Helper()
		r, ok, err := f.svc.RouteOf(bg, f.q, consumer, provider)
		require.NoError(t, err)
		return r, ok
	}

	_, ok := route(f.a.ID, uuid.New())
	require.False(t, ok, "an unknown service has no route")
	_, ok = route(f.b.ID, pg1)
	require.False(t, ok, "a provider on the consumer's own server needs no route")
	_, ok = route(f.a.ID, pg1)
	require.False(t, ok, "nothing exposes the provider yet")

	exp, err := f.ensureExposure(pg1, agentprotocol.MeshPurposeService, 5432)
	require.NoError(t, err)
	r, ok := route(f.a.ID, pg1)
	require.True(t, ok)
	require.Equal(t, mesh.Route{Host: "pg1", Port: exp.MeshPort, State: "pending"}, r, "exposed, but the consumer has no link yet")

	link, err := f.ensureLink(f.env1.ID, f.a.ID, f.b.ID, agentprotocol.MeshPurposeService)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE mesh_links SET state = 'online' WHERE id = $1", link.ID)
	require.NoError(t, err)
	r, ok = route(f.a.ID, pg1)
	require.True(t, ok)
	require.Equal(t, mesh.Route{Host: "pg1", Port: exp.MeshPort, LinkID: link.ID, State: "online"}, r)

	// A replication link of the same pair is not the service route.
	_, err = f.pool.Exec(bg, "DELETE FROM mesh_links WHERE id = $1", link.ID)
	require.NoError(t, err)
	_, err = f.ensureLink(f.env1.ID, f.a.ID, f.b.ID, agentprotocol.MeshPurposeReplication)
	require.NoError(t, err)
	r, ok = route(f.a.ID, pg1)
	require.True(t, ok)
	require.Equal(t, "pending", r.State)
	require.Equal(t, uuid.Nil, r.LinkID)

	// A deleted provider has no route.
	_, err = f.pool.Exec(bg, "UPDATE services SET deleted_at = now() WHERE id = $1", pg1)
	require.NoError(t, err)
	_, ok = route(f.a.ID, pg1)
	require.False(t, ok)
}
