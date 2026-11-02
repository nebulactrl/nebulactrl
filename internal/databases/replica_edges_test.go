package databases_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/mesh"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// scriptedMesh answers readiness and connectivity as the test says.
type scriptedMesh struct {
	*fakeMesh
	ready mesh.Readiness
	check mesh.CheckResult
}

func (m *scriptedMesh) Ready(context.Context, uuid.UUID) (mesh.Readiness, error) { return m.ready, nil }

func (m *scriptedMesh) Check(context.Context, uuid.UUID, uuid.UUID) (mesh.CheckResult, error) {
	return m.check, nil
}

// notified records the replica events the service raises.
type notified struct {
	mu     sync.Mutex
	events []databases.ReplicaEvent
}

func (n *notified) notify(_ context.Context, ev databases.ReplicaEvent) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, ev)
}

// failingDeployer starts a deployment that ends as failed.
type failingDeployer struct {
	f   *replicaFixture
	msg string
}

func (d *failingDeployer) Deploy(ctx context.Context, id uuid.UUID, _, _ string) (deployments.DeploymentView, error) {
	sc, err := services.LoadScopeInternal(ctx, d.f.q, id)
	require.NoError(d.f.t, err)
	dep := d.f.deployOn(id, *sc.Service.ServerID)
	_, err = d.f.pool.Exec(ctx, "UPDATE deployments SET status = 'failed', status_message = $2 WHERE id = $1", dep, d.msg)
	require.NoError(d.f.t, err)
	return deployments.DeploymentView{ID: dep}, nil
}

func (f *replicaFixture) deps(m mesh.Provider, n *notified, jm *jobs.Manager) {
	d := databases.ReplicaDeps{Mesh: m, Links: f.links, Deployer: &fakeDeployer{f: f}, Jobs: jm}
	if n != nil {
		d.Notify = n.notify
	}
	f.dbs.SetReplicas(d)
}

func (f *replicaFixture) advance(id uuid.UUID) bool {
	f.t.Helper()
	done, err := f.dbs.AdvanceReplica(context.Background(), id)
	require.NoError(f.t, err)
	return done
}

func TestReplicaSagaFailuresEndAsFailedWithWhatWentWrong(t *testing.T) {
	bg := context.Background()
	for _, tc := range []struct {
		name string
		fail func(f *replicaFixture, id uuid.UUID)
		want string
	}{
		{"the primary cannot be prepared", func(f *replicaFixture, id uuid.UUID) {
			f.advance(id)
			f.complete("repl_prepare:"+id.String()+":1", agentprotocol.OutcomeFailed, "role already exists", nil)
			require.True(t, f.advance(id))
		}, "preparing the primary failed: role already exists"},
		{"the clone fails", func(f *replicaFixture, id uuid.UUID) {
			f.advance(id)
			f.ok("repl_prepare:"+id.String()+":1", agentprotocol.DBAdminResult{})
			f.advance(id)
			f.complete("repl_clone:"+id.String()+":1", agentprotocol.OutcomeFailed, "no space left on device", nil)
			require.True(t, f.advance(id))
		}, "cloning the primary failed: no space left on device"},
		{"the replica's deployment fails", func(f *replicaFixture, id uuid.UUID) {
			f.dbs.SetReplicas(databases.ReplicaDeps{Mesh: f.mesh, Links: f.links, Deployer: &failingDeployer{f: f, msg: "out of memory"}})
			f.advance(id)
			f.ok("repl_prepare:"+id.String()+":1", agentprotocol.DBAdminResult{})
			f.advance(id)
			f.ok("repl_clone:"+id.String()+":1", agentprotocol.ReplicaCloneResult{})
			require.True(t, f.advance(id))
		}, "deploying the replica failed: out of memory"},
		{"the replica lost its replica configuration", func(f *replicaFixture, id uuid.UUID) {
			_, err := f.pool.Exec(bg, "UPDATE services SET config = config #- '{database,replica}' WHERE id = $1", id)
			require.NoError(t, err)
			require.True(t, f.advance(id))
		}, "lost its replica configuration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReplicaFixture(t)
			pg := f.primary("pg")
			id := f.addReplica(pg.ID, f.server.ID)
			tc.fail(f, id)
			r := f.replica(id)
			require.Equal(t, databases.ReplicaFailed, r.State)
			require.Contains(t, r.Message, tc.want)
			require.True(t, f.advance(id), "a failed replica is settled: nothing more to advance")
		})
	}
}

func TestReplicaNeverStreamingBecomesBrokenAndNotifies(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	n := &notified{}
	f.deps(f.mesh, n, nil)
	pg := f.primary("pg")
	id := f.addReplica(pg.ID, f.server.ID)
	f.advance(id)
	f.ok("repl_prepare:"+id.String()+":1", agentprotocol.DBAdminResult{})
	f.advance(id)
	f.ok("repl_clone:"+id.String()+":1", agentprotocol.ReplicaCloneResult{})
	f.advance(id)
	require.False(t, f.advance(id), "the deployment succeeded: now the replica has to start streaming")
	require.Equal(t, databases.ReplicaDeploying, f.replica(id).State)
	require.Empty(t, n.events)

	// Within the wait nothing changes; past it the replica is called broken, once.
	_, err := f.pool.Exec(bg, "UPDATE database_replicas SET updated_at = now() - interval '11 minutes' WHERE service_id = $1", id)
	require.NoError(t, err)
	require.False(t, f.advance(id))
	r := f.replica(id)
	require.Equal(t, databases.ReplicaBroken, r.State)
	require.Contains(t, r.Message, "never began streaming")
	require.Len(t, n.events, 1)
	require.Equal(t, databases.NotifyReplicaBroken, n.events[0].Type)
	require.Equal(t, id, n.events[0].ServiceID)
	require.Equal(t, "pg", n.events[0].Primary)
	require.True(t, f.advance(id), "broken is settled for the saga; the user rebuilds it")
	require.Len(t, n.events, 1)
}

func TestAReplicaAcrossServersWaitsForTheLinkAndFailsWhenItNeverComes(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	sm := &scriptedMesh{fakeMesh: f.mesh, ready: mesh.Readiness{Online: false, ACLOK: true, Reason: "the link node is provisioning"}, check: mesh.CheckResult{Result: agentprotocol.MeshCheckConnected}}
	f.deps(sm, nil, nil)
	pg := f.primary("pg")
	id := f.addReplica(pg.ID, f.server2.ID)
	require.NotNil(t, f.replica(id).LinkID)

	f.advance(id)
	f.ok("repl_prepare:"+id.String()+":1", agentprotocol.DBAdminResult{})

	// The link is not online: the saga keeps waiting and sends no clone.
	require.False(t, f.advance(id))
	_, sent := f.commandByKey("repl_clone:" + id.String() + ":1")
	require.False(t, sent)
	require.Equal(t, databases.ReplicaPreparing, f.replica(id).State)

	// Online but the connectivity check does not pass yet.
	sm.ready = mesh.Readiness{Online: true, ACLOK: true}
	sm.check = mesh.CheckResult{Result: agentprotocol.MeshCheckTimeout}
	require.False(t, f.advance(id))
	_, sent = f.commandByKey("repl_clone:" + id.String() + ":1")
	require.False(t, sent)

	// Once it connects the clone starts.
	sm.check = mesh.CheckResult{Result: agentprotocol.MeshCheckConnected}
	require.False(t, f.advance(id))
	_, sent = f.commandByKey("repl_clone:" + id.String() + ":1")
	require.True(t, sent)

	// A link that stays down for too long fails the replica with the reason and releases its grants.
	f2 := newReplicaFixture(t)
	sm2 := &scriptedMesh{fakeMesh: f2.mesh, ready: mesh.Readiness{Online: false, Reason: "node offline"}}
	f2.deps(sm2, nil, nil)
	pg2 := f2.primary("pg")
	id2 := f2.addReplica(pg2.ID, f2.server2.ID)
	f2.advance(id2)
	f2.ok("repl_prepare:"+id2.String()+":1", agentprotocol.DBAdminResult{})
	require.False(t, f2.advance(id2), "the link is not ready yet: the saga waits")
	_, err := f2.pool.Exec(bg, "UPDATE database_replicas SET updated_at = now() - interval '1 day' WHERE service_id = $1", id2)
	require.NoError(t, err)
	require.True(t, f2.advance(id2))
	r := f2.replica(id2)
	require.Equal(t, databases.ReplicaFailed, r.State)
	require.Contains(t, r.Message, "mesh_link_not_ready")
	require.Contains(t, r.Message, "node offline")
	require.Equal(t, []uuid.UUID{id2}, f2.mesh.released, "the failed replica gives its link grants back")
}

func TestAReplicaWaitsWhileTheMeshIsPausedAndWithoutAMeshFails(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	id := f.addReplica(pg.ID, f.server2.ID)
	_, err := f.pool.Exec(bg, "INSERT INTO mesh_settings (workspace_id, paused, paused_reason) VALUES ($1, true, 'maintenance')", f.ws.ID)
	require.NoError(t, err)
	require.False(t, f.advance(id))
	_, sent := f.commandByKey("repl_prepare:" + id.String() + ":1")
	require.False(t, sent, "nothing is sent while the mesh waits")
	_, err = f.pool.Exec(bg, "UPDATE mesh_settings SET paused = false WHERE workspace_id = $1", f.ws.ID)
	require.NoError(t, err)
	f.advance(id)
	f.ok("repl_prepare:"+id.String()+":1", agentprotocol.DBAdminResult{})

	// The installation lost its mesh provider meanwhile: the link step cannot proceed.
	f.dbs.SetReplicas(databases.ReplicaDeps{Links: f.links, Deployer: &fakeDeployer{f: f}})
	require.True(t, f.advance(id))
	r := f.replica(id)
	require.Equal(t, databases.ReplicaFailed, r.State)
	require.Contains(t, r.Message, "the server mesh is not available")
}

func TestPlanReplicaNamesEveryReasonAReplicaCannotBeAdded(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	plan := func(server uuid.UUID) databases.ReplicaPlan {
		t.Helper()
		p, err := f.dbs.PlanReplica(f.ctx, pg.ID, server)
		require.NoError(t, err)
		return p
	}
	p := plan(f.server.ID)
	require.Empty(t, p.Blockers)
	require.True(t, p.SameServer)
	require.Equal(t, "agent1", p.ServerName)
	require.Contains(t, problemCodes(p.Warnings), "async")

	// The size of the primary's data is warned about before it is copied.
	refreshed, err := f.dbs.Refresh(f.ctx, pg.ID)
	require.NoError(t, err)
	c, ok := f.lastCommand(agentprotocol.KindDBAdmin)
	require.True(t, ok)
	require.Equal(t, refreshed.CommandID, c.ID)
	f.result(c, agentprotocol.OutcomeSucceeded, "", agentprotocol.DBAdminResult{Info: &agentprotocol.DBInfo{Version: "17.2", SizeBytes: 3 << 30}})
	require.Contains(t, problemCodes(plan(f.server.ID).Warnings), "clone_size")

	// Servers that cannot take it.
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'revoked' WHERE id = $1", f.server2.ID)
	require.NoError(t, err)
	require.Contains(t, problemCodes(plan(f.server2.ID).Blockers), "server_not_found")
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online', accept_workloads = false WHERE id = $1", f.server2.ID)
	require.NoError(t, err)
	require.Contains(t, problemCodes(plan(f.server2.ID).Blockers), "server_not_accepting")
	_, err = f.pool.Exec(bg, "UPDATE servers SET accept_workloads = true WHERE id = $1", f.server2.ID)
	require.NoError(t, err)
	require.Contains(t, problemCodes(plan(uuid.New()).Blockers), "server_not_found")

	// Across servers the mesh's warnings and the device count come through.
	f.mesh.plan = mesh.LinkPlan{DevicesAdded: 2, Warnings: []string{"advisory ACL"}, Blockers: []mesh.Blocker{}}
	p = plan(f.server2.ID)
	require.False(t, p.SameServer)
	require.Equal(t, 2, p.DevicesAdded)
	codes := problemCodes(p.Warnings)
	require.Contains(t, codes, "mesh_warning")
	require.Contains(t, codes, "mesh_confirmation_required")
	f.dbs.SetReplicas(databases.ReplicaDeps{Deployer: &fakeDeployer{f: f}})
	require.Contains(t, problemCodes(plan(f.server2.ID).Blockers), "mesh_not_configured")
	f.mesh.plan = mesh.LinkPlan{Blockers: []mesh.Blocker{}}
	f.deps(f.mesh, nil, nil)

	// The primary itself: a fenced old primary, a promotion, an upgrade or a rotation in flight, a stopped database.
	_, err = f.pool.Exec(bg, "INSERT INTO database_fences (service_id, reason) VALUES ($1, 'test')", pg.ID)
	require.NoError(t, err)
	require.Equal(t, []string{databases.CodeDatabaseFenced}, problemCodes(plan(f.server.ID).Blockers))
	_, err = f.pool.Exec(bg, "DELETE FROM database_fences")
	require.NoError(t, err)

	_, err = f.dbs.RotateCredentials(f.ctx, pg.ID, false)
	require.NoError(t, err)
	require.Contains(t, problemCodes(plan(f.server.ID).Blockers), "rotation_in_progress")
	rot, _ := f.lastCommand(agentprotocol.KindDBAdmin)
	f.result(rot, agentprotocol.OutcomeSucceeded, "", agentprotocol.DBAdminResult{})

	_, err = f.dbs.Upgrade(f.ctx, pg.ID, "18", pg.Slug)
	require.NoError(t, err)
	got := problemCodes(plan(f.server.ID).Blockers)
	require.Contains(t, got, "upgrade_in_progress")
	require.Contains(t, got, "primary_not_running", "the upgrade stopped the database")
	up, _ := f.lastCommand(agentprotocol.KindDBUpgrade)
	f.result(up, agentprotocol.OutcomeFailed, "", nil)

	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	require.Contains(t, problemCodes(plan(f.server.ID).Blockers), "primary_not_running")
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online', facts = '{}' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	require.Contains(t, problemCodes(plan(f.server.ID).Blockers), "agent_too_old")
}

func problemCodes(ps []databases.Problem) []string {
	out := []string{}
	for _, p := range ps {
		out = append(out, p.Code)
	}
	return out
}

func TestSweepReplicasRekicksStuckSagasOnceAndFlagsLostSlots(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	jm := jobs.New(f.pool, nil)
	n := &notified{}
	f.deps(f.mesh, n, jm)
	databases.RegisterReplicaJobs(jm, f.dbs)

	pg := f.primary("pg")
	pending := f.addReplica(pg.ID, f.server.ID)
	streaming := f.streamingReplica(pg.ID, f.server.ID)
	var kicks, before int
	kicksOf := func(id uuid.UUID) int {
		var n int
		require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM river_job WHERE kind = 'database_replica_advance' AND args->>'serviceId' = $1", id.String()).Scan(&n))
		return n
	}
	before = kicksOf(pending)
	settledBefore := kicksOf(streaming)
	require.NoError(t, f.dbs.SweepReplicas(bg))
	require.NoError(t, f.dbs.SweepReplicas(bg))
	require.Equal(t, before+1, kicksOf(pending), "a saga is kicked once however often the sweep runs")
	require.Equal(t, settledBefore, kicksOf(streaming), "a settled replica is not kicked")

	// A promotion that is running is kicked as well.
	_, err := f.pool.Exec(bg, `INSERT INTO database_promotions (workspace_id, environment_id, replica_service_id, old_primary_service_id, mode)
		VALUES ($1, $2, $3, $4, 'switchover')`, f.ws.ID, f.env.ID, streaming, pg.ID)
	require.NoError(t, err)
	require.NoError(t, f.dbs.SweepReplicas(bg))
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM river_job WHERE kind = 'database_promotion_advance'").Scan(&kicks))
	require.Equal(t, 1, kicks)
	_, err = f.pool.Exec(bg, "UPDATE database_promotions SET status = 'failed'")
	require.NoError(t, err)

	// The primary reports the replica's slot lost: the replica needs a rebuild, and the user is told.
	_, err = f.pool.Exec(bg, "UPDATE database_replicas SET wal_status = 'lost' WHERE service_id = $1", streaming)
	require.NoError(t, err)
	require.NoError(t, f.dbs.SweepReplicas(bg))
	r := f.replica(streaming)
	require.Equal(t, databases.ReplicaNeedsRebuild, r.State)
	require.Contains(t, r.Message, "replication slot")
	require.Len(t, n.events, 1)
	require.Equal(t, databases.NotifyReplicaBroken, n.events[0].Type)
	require.NoError(t, f.dbs.SweepReplicas(bg))
	require.Len(t, n.events, 1, "already flagged: not announced again")

	// Without a job manager there is nothing to kick.
	f.deps(f.mesh, nil, nil)
	require.NoError(t, f.dbs.SweepReplicas(bg))
}

func TestAnnotateDesiredTellsTheAgentWhichDatabasesToPollAndWhichAreFenced(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	rep := f.streamingReplica(pg.ID, f.server.ID)
	other := f.primary("other")
	rec := &agentprotocol.Reconcile{Services: []agentprotocol.DesiredService{
		{ServiceID: pg.ID.String()}, {ServiceID: rep.String()}, {ServiceID: other.ID.String()}, {ServiceID: uuid.NewString()},
	}}
	require.NoError(t, f.dbs.AnnotateDesired(bg, f.server.ID, rec))
	byID := map[string]agentprotocol.DesiredService{}
	for _, s := range rec.Services {
		byID[s.ServiceID] = s
	}
	require.Equal(t, agentprotocol.ReplRolePrimary, byID[pg.ID.String()].Replication.Role, "a primary with replicas is polled as a primary")
	require.Equal(t, "postgres", byID[pg.ID.String()].Replication.User)
	r := byID[rep.String()].Replication
	require.Equal(t, agentprotocol.ReplRoleReplica, r.Role)
	require.Equal(t, f.replica(rep).SlotName, r.Slot)
	require.Nil(t, byID[other.ID.String()].Replication, "a standalone database is not polled")

	// A fenced old primary is told to stay down and is not polled; a replica being removed is not polled either.
	_, err := f.pool.Exec(bg, "INSERT INTO database_fences (service_id, reason) VALUES ($1, 'switchover')", pg.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE database_replicas SET state = 'removing' WHERE service_id = $1", rep)
	require.NoError(t, err)
	rec = &agentprotocol.Reconcile{Services: []agentprotocol.DesiredService{{ServiceID: pg.ID.String()}, {ServiceID: rep.String()}}}
	require.NoError(t, f.dbs.AnnotateDesired(bg, f.server.ID, rec))
	require.True(t, rec.Services[0].Fenced)
	require.Nil(t, rec.Services[0].Replication)
	require.False(t, rec.Services[1].Fenced)
	require.Nil(t, rec.Services[1].Replication)
}

func TestDeployHooksDescribeReplicationAndExplainWhyADeployWillNotRepairIt(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	scOf := func(id uuid.UUID) services.Scope {
		sc, err := services.LoadScopeInternal(bg, f.q, id)
		require.NoError(t, err)
		return sc
	}

	// Standalone: nothing to say.
	rt, err := f.dbs.DatabaseRuntime(bg, f.q, scOf(pg.ID))
	require.NoError(t, err)
	require.Nil(t, rt)
	w, b, err := f.dbs.PlanCheck(bg, f.q, scOf(pg.ID), scOf(pg.ID).Config())
	require.NoError(t, err)
	require.Empty(t, w)
	require.Empty(t, b)

	rep := f.streamingReplica(pg.ID, f.server.ID)
	rt, err = f.dbs.DatabaseRuntime(bg, f.q, scOf(pg.ID))
	require.NoError(t, err)
	require.Equal(t, agentprotocol.ReplRolePrimary, rt.Replication.Role)
	rt, err = f.dbs.DatabaseRuntime(bg, f.q, scOf(rep))
	require.NoError(t, err)
	require.Equal(t, agentprotocol.ReplRoleReplica, rt.Replication.Role)
	require.Equal(t, f.replica(rep).SlotName, rt.Replication.Slot)
	kv := f.createDB("cache", "valkey", "9")
	rt, err = f.dbs.DatabaseRuntime(bg, f.q, scOf(kv.ID))
	require.NoError(t, err)
	require.Nil(t, rt, "only PostgreSQL replicates")

	// Redeploying a primary with replicas restarts it; a broken replica is not repaired by a deploy; versions should match.
	w, _, err = f.dbs.PlanCheck(bg, f.q, scOf(pg.ID), scOf(pg.ID).Config())
	require.NoError(t, err)
	require.Contains(t, strings.Join(w, "\n"), "1 read replica(s)")
	_, err = f.pool.Exec(bg, "UPDATE database_replicas SET state = 'needs_rebuild' WHERE service_id = $1", rep)
	require.NoError(t, err)
	rcfg := scOf(rep).Config()
	rcfg.Database.Version = "16"
	w, _, err = f.dbs.PlanCheck(bg, f.q, scOf(rep), rcfg)
	require.NoError(t, err)
	joined := strings.Join(w, "\n")
	require.Contains(t, joined, "needs rebuild; deploying it will not repair it")
	require.Contains(t, joined, "replica runs PostgreSQL 16 and its primary 17")

	// A fenced old primary cannot be deployed at all.
	_, err = f.pool.Exec(bg, "INSERT INTO database_fences (service_id, reason) VALUES ($1, 'switchover')", pg.ID)
	require.NoError(t, err)
	_, b, err = f.dbs.PlanCheck(bg, f.q, scOf(pg.ID), scOf(pg.ID).Config())
	require.NoError(t, err)
	require.Len(t, b, 1)
	require.Equal(t, databases.CodeDatabaseFenced, b[0].Code)
}

func TestRevokingAServerFailsItsReplicasAndBreaksTheReplicasOfItsPrimaries(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	local := f.streamingReplica(pg.ID, f.server.ID)
	remote := f.streamingReplica(pg.ID, f.server2.ID)

	// The replica's own server goes: that replica is failed, the other still follows a live primary.
	f.dbs.ServerRevoked(bg, f.ws.ID, f.server2.ID)
	require.Equal(t, databases.ReplicaFailed, f.replica(remote).State)
	require.Contains(t, f.replica(remote).Message, "server was revoked")
	require.Equal(t, databases.ReplicaStreaming, f.replica(local).State)

	// The primary's server goes: the replicas that followed it are broken, with the hint that promoting is possible.
	f.dbs.ServerRevoked(bg, f.ws.ID, f.server.ID)
	r := f.replica(local)
	require.Equal(t, databases.ReplicaFailed, r.State, "it ran on that server too")
}

func TestRevokingThePrimarysServerBreaksReplicasOnOtherServers(t *testing.T) {
	f := newReplicaFixture(t)
	pg := f.primary("pg")
	remote := f.streamingReplica(pg.ID, f.server2.ID)
	f.dbs.ServerRevoked(context.Background(), f.ws.ID, f.server.ID)
	r := f.replica(remote)
	require.Equal(t, databases.ReplicaBroken, r.State)
	require.Equal(t, "Primary lost: its server was revoked. Promote is available.", r.Message)
}

func TestFinishedReplicaCommandsKickTheirSagaSoTheNextStepDoesNotWait(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	jm := jobs.New(f.pool, nil)
	f.deps(f.mesh, nil, jm)
	databases.RegisterReplicaJobs(jm, f.dbs)
	pg := f.primary("pg")
	id := f.addReplica(pg.ID, f.server.ID)
	f.advance(id)
	kicks := func(kind, arg, val string) int {
		var n int
		require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM river_job WHERE kind = $1 AND args->>$2 = $3", kind, arg, val).Scan(&n))
		return n
	}
	before := kicks("database_replica_advance", "serviceId", id.String())
	prep, ok := f.commandByKey("repl_prepare:" + id.String() + ":1")
	require.True(t, ok)
	require.NoError(t, f.dbs.HandleResult(bg, prep.ID, agentprotocol.ResultFrame{CommandID: prep.ID.String(), Outcome: agentprotocol.OutcomeSucceeded}))
	require.Equal(t, before+1, kicks("database_replica_advance", "serviceId", id.String()))

	// A command that belongs to no saga (no recognisable key) kicks nothing.
	plain := f.rawCommand("refresh-1", agentprotocol.KindDBAdmin, agentprotocol.DBAdminPayload{ServiceID: pg.ID.String(), Action: agentprotocol.DBActionInfo})
	require.NoError(t, f.dbs.HandleResult(bg, plain.ID, agentprotocol.ResultFrame{CommandID: plain.ID.String(), Outcome: agentprotocol.OutcomeSucceeded}))
	require.Equal(t, before+1, kicks("database_replica_advance", "serviceId", id.String()))

	// A promotion's command kicks the promotion.
	promo := uuid.New()
	step := f.rawCommand("promo:"+promo.String()+":freeze", agentprotocol.KindDBAdmin, agentprotocol.DBAdminPayload{ServiceID: pg.ID.String(), Action: agentprotocol.DBActionReplFreeze})
	require.NoError(t, f.dbs.HandleResult(bg, step.ID, agentprotocol.ResultFrame{CommandID: step.ID.String(), Outcome: agentprotocol.OutcomeSucceeded}))
	require.Equal(t, 1, kicks("database_promotion_advance", "promotionId", promo.String()))
}

func (f *replicaFixture) rawCommand(key, kind string, payload any) db.AgentCommand {
	f.t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(f.t, err)
	c, err := f.q.InsertAgentCommand(context.Background(), db.InsertAgentCommandParams{
		ID: uuid.New(), ServerID: f.server.ID, Kind: kind, Payload: raw, IdempotencyKey: key, ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(f.t, err)
	return c
}

func TestRebuildRefusalsNameWhatIsWrongAndChangeNothing(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	rep := f.streamingReplica(pg.ID, f.server.ID)
	rebuild := func(id uuid.UUID, follow *uuid.UUID) string {
		t.Helper()
		_, err := f.dbs.RebuildReplica(f.ctx, id, databases.RebuildInput{FollowServiceID: follow})
		return apiCode(t, err)
	}
	untouched := func() {
		t.Helper()
		require.Equal(t, databases.ReplicaStreaming, f.replica(rep).State)
		require.Zero(t, f.countRows("agent_commands"), "a refused rebuild sends nothing to any agent")
	}

	kv := f.createDB("cache", "valkey", "9")
	require.Equal(t, "replica_wrong_engine", rebuild(kv.ID, nil))
	require.Equal(t, "replica_not_rebuildable", rebuild(pg.ID, nil), "a standalone primary is not rebuilt")
	_, err := f.pool.Exec(bg, "UPDATE database_replicas SET state = 'cloning' WHERE service_id = $1", rep)
	require.NoError(t, err)
	require.Equal(t, "replica_not_rebuildable", rebuild(rep, nil), "wait for it to settle first")
	_, err = f.pool.Exec(bg, "UPDATE database_replicas SET state = 'streaming' WHERE service_id = $1", rep)
	require.NoError(t, err)

	// The primary it would follow: fenced, in a promotion, not running.
	_, err = f.pool.Exec(bg, "INSERT INTO database_fences (service_id, reason) VALUES ($1, 'old')", pg.ID)
	require.NoError(t, err)
	require.Equal(t, "database_fenced", rebuild(rep, nil))
	_, err = f.pool.Exec(bg, "DELETE FROM database_fences")
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, `INSERT INTO database_promotions (workspace_id, environment_id, replica_service_id, old_primary_service_id, mode)
		VALUES ($1, $2, $3, $4, 'switchover')`, f.ws.ID, f.env.ID, rep, pg.ID)
	require.NoError(t, err)
	require.Equal(t, "promote_in_progress", rebuild(rep, nil))
	_, err = f.pool.Exec(bg, "UPDATE database_promotions SET status = 'failed'")
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE services SET desired_state = 'stopped' WHERE id = $1", pg.ID)
	require.NoError(t, err)
	require.Equal(t, "primary_not_running", rebuild(rep, nil))
	_, err = f.pool.Exec(bg, "UPDATE services SET desired_state = 'running' WHERE id = $1", pg.ID)
	require.NoError(t, err)

	// The replica itself: no server, an offline one, one across servers without a mesh or with a blocked link.
	_, err = f.pool.Exec(bg, "UPDATE services SET server_id = NULL WHERE id = $1", rep)
	require.NoError(t, err)
	require.Equal(t, "no_server", rebuild(rep, nil))
	_, err = f.pool.Exec(bg, "UPDATE services SET server_id = $2 WHERE id = $1", rep, f.server2.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server2.ID)
	require.NoError(t, err)
	require.Equal(t, "server_offline", rebuild(rep, nil))
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", f.server2.ID)
	require.NoError(t, err)
	f.mesh.plan = mesh.LinkPlan{Blockers: []mesh.Blocker{{Code: "mesh_server_required", Message: "Enable mesh on agent2"}}}
	require.Equal(t, "mesh_server_required", rebuild(rep, nil))
	f.dbs.SetReplicas(databases.ReplicaDeps{Deployer: &fakeDeployer{f: f}})
	require.Equal(t, "mesh_not_configured", rebuild(rep, nil))
	untouched()
}

func TestRebuildOfAFencedOldPrimaryNeedsAFollowTargetInItsOwnEnvironment(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	old := f.primary("old")
	next := f.primary("next")
	_, err := f.pool.Exec(bg, "INSERT INTO database_fences (service_id, reason) VALUES ($1, 'switchover')", old.ID)
	require.NoError(t, err)
	rebuild := func(follow *uuid.UUID) string {
		t.Helper()
		_, err := f.dbs.RebuildReplica(f.ctx, old.ID, databases.RebuildInput{FollowServiceID: follow})
		return apiCode(t, err)
	}

	require.Equal(t, "follow_required", rebuild(nil), "no promotion recorded: name the new primary")
	kv := f.createDB("cache", "valkey", "9")
	require.Equal(t, "replica_wrong_engine", rebuild(&kv.ID), "the primary to follow must be PostgreSQL")

	env2, err := f.q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: f.project.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	stranger, err := f.dbs.Create(f.ctx, databases.CreateInput{EnvironmentID: env2.ID, Name: "other", Engine: "postgres", Version: "17", ServerID: &f.server.ID})
	require.NoError(t, err)
	require.Equal(t, "follow_other_environment", rebuild(&stranger.ID))

	rep := f.streamingReplica(next.ID, f.server.ID)
	require.Equal(t, "replica_not_primary", rebuild(&rep), "a replica is not a primary to follow")
	_, err = f.pool.Exec(bg, "INSERT INTO database_fences (service_id, reason) VALUES ($1, 'old')", next.ID)
	require.NoError(t, err)
	require.Equal(t, "database_fenced", rebuild(&next.ID))
	_, err = f.pool.Exec(bg, "DELETE FROM database_fences WHERE service_id = $1", next.ID)
	require.NoError(t, err)

	// A primary holds at most MaxReplicas replicas, a fenced old primary included.
	for range databases.MaxReplicas - 1 {
		f.addReplica(next.ID, f.server.ID)
	}
	require.Equal(t, "replicas_limit", rebuild(&next.ID))
}

func TestRebuildOfAFencedOldPrimaryFollowsThePrimaryThatReplacedIt(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	rep := f.streamingReplica(pg.ID, f.server.ID)
	// A finished switchover recorded the replica as the new primary over the old one.
	_, err := f.pool.Exec(bg, `INSERT INTO database_promotions (workspace_id, environment_id, replica_service_id, old_primary_service_id, mode, status)
		VALUES ($1, $2, $3, $4, 'switchover', 'succeeded')`, f.ws.ID, f.env.ID, rep, pg.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "DELETE FROM database_replicas WHERE service_id = $1", rep)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE services SET config = config #- '{database,replica}' WHERE id = $1", rep)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "INSERT INTO database_fences (service_id, reason) VALUES ($1, 'switchover')", pg.ID)
	require.NoError(t, err)

	res, err := f.dbs.RebuildReplica(f.ctx, pg.ID, databases.RebuildInput{})
	require.NoError(t, err)
	require.Equal(t, databases.ReplicaPending, res.Status)
	row := f.replica(pg.ID)
	require.Equal(t, rep, row.PrimaryServiceID, "the old primary now follows the one that replaced it")
}

func TestAgentCommandOutcomesBecomeTheReasonAReplicaFails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		finish func(f *replicaFixture, key string)
		want   string
	}{
		{"failed without a message", func(f *replicaFixture, key string) { f.complete(key, agentprotocol.OutcomeFailed, "", nil) }, "the agent reported a failure"},
		{"never delivered", func(f *replicaFixture, key string) {
			_, err := f.pool.Exec(context.Background(), "UPDATE agent_commands SET status = 'expired' WHERE idempotency_key = $1", key)
			require.NoError(t, err)
		}, "Not delivered: server offline. No changes were made."},
		{"canceled with a reason", func(f *replicaFixture, key string) {
			_, err := f.pool.Exec(context.Background(), "UPDATE agent_commands SET status = 'canceled', error = 'server revoked' WHERE idempotency_key = $1", key)
			require.NoError(t, err)
		}, "server revoked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReplicaFixture(t)
			pg := f.primary("pg")
			id := f.addReplica(pg.ID, f.server.ID)
			f.advance(id)
			tc.finish(f, "repl_prepare:"+id.String()+":1")
			require.True(t, f.advance(id))
			r := f.replica(id)
			require.Equal(t, databases.ReplicaFailed, r.State)
			require.Equal(t, "preparing the primary failed: "+tc.want, r.Message)
		})
	}
}

func TestRemovingAReplicaIsRefusedWhileItIsBeingPromotedAndIsIdempotent(t *testing.T) {
	f := newReplicaFixture(t)
	pg := f.primary("pg")
	rep := f.streamingReplica(pg.ID, f.server.ID)

	_, err := f.dbs.RemoveReplica(f.ctx, pg.ID)
	require.Equal(t, "not_a_replica", apiCode(t, err))
	_, err = f.pool.Exec(context.Background(), "UPDATE database_replicas SET state = 'promoting' WHERE service_id = $1", rep)
	require.NoError(t, err)
	_, err = f.dbs.RemoveReplica(f.ctx, rep)
	require.Equal(t, "promote_in_progress", apiCode(t, err))
	require.Equal(t, databases.ReplicaPromoting, f.replica(rep).State)

	_, err = f.pool.Exec(context.Background(), "UPDATE database_replicas SET state = 'streaming' WHERE service_id = $1", rep)
	require.NoError(t, err)
	res, err := f.dbs.RemoveReplica(f.ctx, rep)
	require.NoError(t, err)
	require.Equal(t, databases.ReplicaRemoving, res.Status)
	again, err := f.dbs.RemoveReplica(f.ctx, rep)
	require.NoError(t, err)
	require.Equal(t, databases.ReplicaRemoving, again.Status)
	require.Equal(t, 1, f.auditCount("database.replica.remove"), "asking twice removes once")
}

// erroringDeployer deploys like fakeDeployer and then answers with err.
type erroringDeployer struct {
	f   *replicaFixture
	err error
}

func (d *erroringDeployer) Deploy(ctx context.Context, id uuid.UUID, _, _ string) (deployments.DeploymentView, error) {
	if d.err != nil {
		var ae *apierr.Error
		if errors.As(d.err, &ae) && ae.Code == "deployment_in_progress" {
			sc, err := services.LoadScopeInternal(ctx, d.f.q, id)
			require.NoError(d.f.t, err)
			d.f.deployOn(id, *sc.Service.ServerID)
		}
	}
	return deployments.DeploymentView{}, d.err
}

// toTheDeployStep brings a new replica to the point where its clone just finished.
func (f *replicaFixture) toTheDeployStep(id uuid.UUID) {
	f.t.Helper()
	f.advance(id)
	f.ok("repl_prepare:"+id.String()+":1", agentprotocol.DBAdminResult{})
	f.advance(id)
	f.ok("repl_clone:"+id.String()+":1", agentprotocol.ReplicaCloneResult{})
}

func TestReplicaDeployStepEndsInAFailureOrCarriesOn(t *testing.T) {
	bg := context.Background()
	t.Run("the deployer refuses", func(t *testing.T) {
		f := newReplicaFixture(t)
		pg := f.primary("pg")
		id := f.addReplica(pg.ID, f.server.ID)
		f.dbs.SetReplicas(databases.ReplicaDeps{Mesh: f.mesh, Links: f.links, Deployer: &erroringDeployer{f: f, err: errors.New("no capacity")}})
		f.toTheDeployStep(id)
		require.True(t, f.advance(id))
		r := f.replica(id)
		require.Equal(t, databases.ReplicaFailed, r.State)
		require.Equal(t, "deploying the replica failed: no capacity", r.Message)
	})
	t.Run("a deployment is already running", func(t *testing.T) {
		f := newReplicaFixture(t)
		pg := f.primary("pg")
		id := f.addReplica(pg.ID, f.server.ID)
		f.dbs.SetReplicas(databases.ReplicaDeps{Mesh: f.mesh, Links: f.links, Deployer: &erroringDeployer{f: f, err: apierr.Conflict("deployment_in_progress", "busy")}})
		f.toTheDeployStep(id)
		require.False(t, f.advance(id), "the running deployment is waited for, not treated as a failure")
		require.Equal(t, databases.ReplicaDeploying, f.replica(id).State)
	})
	t.Run("there is no deployer", func(t *testing.T) {
		f := newReplicaFixture(t)
		pg := f.primary("pg")
		id := f.addReplica(pg.ID, f.server.ID)
		f.dbs.SetReplicas(databases.ReplicaDeps{Mesh: f.mesh, Links: f.links})
		f.toTheDeployStep(id)
		require.True(t, f.advance(id))
		require.Equal(t, "deployments are not available", f.replica(id).Message)
	})
	t.Run("a stopped replica is started for the deploy", func(t *testing.T) {
		f := newReplicaFixture(t)
		pg := f.primary("pg")
		id := f.addReplica(pg.ID, f.server.ID)
		_, err := f.pool.Exec(bg, "UPDATE services SET desired_state = 'stopped' WHERE id = $1", id)
		require.NoError(t, err)
		f.toTheDeployStep(id)
		f.advance(id)
		require.Equal(t, services.DesiredRunning, f.reload(id).DesiredState)
	})
	t.Run("the replica has no server or no data volume", func(t *testing.T) {
		f := newReplicaFixture(t)
		pg := f.primary("pg")
		id := f.addReplica(pg.ID, f.server.ID)
		f.advance(id)
		f.ok("repl_prepare:"+id.String()+":1", agentprotocol.DBAdminResult{})
		_, err := f.pool.Exec(bg, "DELETE FROM volumes WHERE service_id = $1", id)
		require.NoError(t, err)
		require.True(t, f.advance(id))
		require.Contains(t, f.replica(id).Message, "the database has no data volume")

		id2 := f.addReplica(pg.ID, f.server.ID)
		f.advance(id2)
		f.ok("repl_prepare:"+id2.String()+":1", agentprotocol.DBAdminResult{})
		_, err = f.pool.Exec(bg, "UPDATE services SET server_id = NULL WHERE id = $1", id2)
		require.NoError(t, err)
		require.True(t, f.advance(id2))
		require.Equal(t, "the replica has no server", f.replica(id2).Message)
	})
}

func TestPromotionThatCannotContinueFailsWithWhy(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	p := f.startSwitchover()
	p.advance() // freeze is sent

	// A step the code does not know ends the promotion instead of looping.
	_, err := f.pool.Exec(bg, "UPDATE database_promotions SET step = 'teleport' WHERE id = $1", p.id)
	require.NoError(t, err)
	require.True(t, p.advance())
	_, status, msg := p.row()
	require.Equal(t, "failed", status)
	require.Equal(t, "unknown step teleport", msg)

	// Waiting for the replica without a recorded final position aborts the switchover.
	f2 := newReplicaFixture(t)
	p2 := f2.startSwitchover()
	_, err = f2.pool.Exec(bg, "UPDATE database_promotions SET step = 'wait', freeze_lsn = NULL WHERE id = $1", p2.id)
	require.NoError(t, err)
	require.False(t, p2.advance())
	step, status, msg := p2.row()
	require.Equal(t, "abort_restart", step)
	require.Equal(t, "running", status)
	require.Contains(t, msg, "no final position was recorded")

	// Planning a replica next to a promotion is refused with its own code.
	f3 := newReplicaFixture(t)
	p3 := f3.startSwitchover()
	plan, err := f3.dbs.PlanReplica(f3.ctx, p3.primary, f3.server2.ID)
	require.NoError(t, err)
	require.Contains(t, problemCodes(plan.Blockers), databases.CodePromoteInProgress)
}

func TestObservationsOfUnknownOrNonReplicatedServicesAreIgnored(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	rep := f.streamingReplica(pg.ID, f.server.ID)
	before := f.replica(rep)
	report := agentprotocol.ReplicationStatus{Role: agentprotocol.ReplRoleReplica, InRecovery: true, ReceiverStatus: "streaming", ReceiveLSN: "0/9000000", ReplayLSN: "0/9000000", CheckedAt: time.Now()}
	obs := agentprotocol.Observed{Services: []agentprotocol.ObservedService{
		{ServiceID: "not-a-uuid", Replication: &report},
		{ServiceID: rep.String()}, // no replication part
		{ServiceID: uuid.NewString(), Replication: &report},
	}}
	require.NoError(t, f.dbs.HandleObserved(bg, f.server.ID, obs))
	after := f.replica(rep)
	require.Equal(t, before.ReplayLsn, after.ReplayLsn, "nothing of the above reached the replica row")
	require.Equal(t, before.State, after.State)
}
