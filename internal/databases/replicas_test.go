package databases_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/mesh"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func blockerCodes(p databases.ReplicaPlan) []string {
	var out []string
	for _, b := range p.Blockers {
		out = append(out, b.Code)
	}
	return out
}

func TestReplicaPlanAndCreateValidations(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()

	// A primary that never ran cannot be cloned.
	pg := f.createDB("pg", "postgres", "17")
	res, err := f.dbs.AddReplica(f.ctx, pg.ID, databases.AddReplicaInput{ServerID: f.server.ID, DryRun: true})
	require.NoError(t, err)
	require.True(t, res.DryRun)
	require.Contains(t, blockerCodes(res.Plan), "primary_not_running")
	require.Nil(t, res.ServiceID)
	_, err = f.dbs.AddReplica(f.ctx, pg.ID, databases.AddReplicaInput{ServerID: f.server.ID})
	require.Equal(t, "primary_not_running", apiCode(t, err))
	all, err := f.q.ListAllEnvironmentServices(bg, f.env.ID)
	require.NoError(t, err)
	require.Len(t, all, 1, "a refused add creates nothing")

	// Only PostgreSQL has replicas.
	kv := f.createDB("cache", "valkey", "9")
	f.deployOn(kv.ID, f.server.ID)
	res, err = f.dbs.AddReplica(f.ctx, kv.ID, databases.AddReplicaInput{ServerID: f.server.ID, DryRun: true})
	require.NoError(t, err)
	require.Equal(t, []string{"replica_wrong_engine"}, blockerCodes(res.Plan))
	_, err = f.dbs.AddReplica(f.ctx, kv.ID, databases.AddReplicaInput{ServerID: f.server.ID})
	require.Equal(t, "replica_wrong_engine", apiCode(t, err))

	// An agent without the capability cannot take part.
	f.deployOn(pg.ID, f.server.ID)
	old, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "old-agent"})
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online', facts = '{}' WHERE id = $1", old.ID)
	require.NoError(t, err)
	res, err = f.dbs.AddReplica(f.ctx, pg.ID, databases.AddReplicaInput{ServerID: old.ID, DryRun: true})
	require.NoError(t, err)
	require.Contains(t, blockerCodes(res.Plan), "agent_too_old")

	// An offline target server blocks too.
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server2.ID)
	require.NoError(t, err)
	res, err = f.dbs.AddReplica(f.ctx, pg.ID, databases.AddReplicaInput{ServerID: f.server2.ID, DryRun: true})
	require.NoError(t, err)
	require.Contains(t, blockerCodes(res.Plan), "server_offline")
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", f.server2.ID)
	require.NoError(t, err)

	// Across servers the mesh decides: its blockers pass through.
	f.mesh.plan = mesh.LinkPlan{Blockers: []mesh.Blocker{{Code: "mesh_server_required", Message: "Enable mesh on agent2"}}}
	res, err = f.dbs.AddReplica(f.ctx, pg.ID, databases.AddReplicaInput{ServerID: f.server2.ID, DryRun: true})
	require.NoError(t, err)
	require.False(t, res.Plan.SameServer)
	require.Contains(t, blockerCodes(res.Plan), "mesh_server_required")
	_, err = f.dbs.AddReplica(f.ctx, pg.ID, databases.AddReplicaInput{ServerID: f.server2.ID})
	require.Equal(t, "mesh_server_required", apiCode(t, err))

	// Adding devices needs the explicit confirmation.
	f.mesh.plan = mesh.LinkPlan{DevicesAdded: 1, Blockers: []mesh.Blocker{}}
	res, err = f.dbs.AddReplica(f.ctx, pg.ID, databases.AddReplicaInput{ServerID: f.server2.ID, DryRun: true})
	require.NoError(t, err)
	require.Equal(t, 1, res.Plan.DevicesAdded)
	require.Len(t, res.Plan.Links, 1)
	_, err = f.dbs.AddReplica(f.ctx, pg.ID, databases.AddReplicaInput{ServerID: f.server2.ID})
	require.Equal(t, "mesh_confirmation_required", apiCode(t, err))

	// A replica cannot have replicas, and a primary has at most five.
	f.mesh.plan = mesh.LinkPlan{Blockers: []mesh.Blocker{}}
	first := f.addReplica(pg.ID, f.server.ID)
	_, err = f.dbs.AddReplica(f.ctx, first, databases.AddReplicaInput{ServerID: f.server.ID})
	require.Equal(t, "replica_not_primary", apiCode(t, err))
	for range databases.MaxReplicas - 1 {
		f.addReplica(pg.ID, f.server.ID)
	}
	_, err = f.dbs.AddReplica(f.ctx, pg.ID, databases.AddReplicaInput{ServerID: f.server.ID})
	require.Equal(t, "replicas_limit", apiCode(t, err))

	// Writing needs both scopes.
	ro := f.principal([]string{policy.ScopeProjectsRead}, nil)
	_, err = f.dbs.AddReplica(ro, pg.ID, databases.AddReplicaInput{ServerID: f.server.ID})
	require.Equal(t, "forbidden", apiCode(t, err))
}

func TestAddReplicaCreatesAStagedServiceWithReferencedCredentials(t *testing.T) {
	f := newReplicaFixture(t)
	pg := f.primary("pg")
	id := f.addReplica(pg.ID, f.server.ID)

	sv := f.reload(id)
	require.Equal(t, services.KindDatabase, sv.Kind)
	cfg := services.DecodeConfig(sv.Config)
	require.NotNil(t, cfg.Database)
	require.NotNil(t, cfg.Database.Replica)
	short := agentprotocol.Short(id.String())
	require.Equal(t, engines.ReplicaSpec{
		PrimaryServiceID: pg.ID.String(), Host: "pg", Port: 5432, Slot: "nc_r_" + short, User: "nebula_r_" + short,
	}, *cfg.Database.Replica)
	require.True(t, cfg.BlockEgress, "a replica never needs the internet")
	require.Equal(t, engines.ReplicaStartCommand(), cfg.StartCommand, "the guard refuses an empty data directory")
	require.Equal(t, "17", cfg.Database.Version)
	require.Equal(t, f.server.ID, *sv.ServerID)
	require.Greater(t, sv.Generation, sv.AppliedGeneration, "creating stages, never deploys")

	raw, err := services.LoadRawVars(context.Background(), f.q, f.box, id)
	require.NoError(t, err)
	require.Equal(t, "${{pg.POSTGRES_USER}}", raw["POSTGRES_USER"].Value)
	require.Equal(t, "${{pg.POSTGRES_PASSWORD}}", raw["POSTGRES_PASSWORD"].Value)
	require.True(t, raw["POSTGRES_PASSWORD"].Secret)
	require.Equal(t, "${{pg.POSTGRES_DB}}", raw["POSTGRES_DB"].Value)
	require.Len(t, raw[engines.ReplicationPasswordKey].Value, 32)
	require.True(t, raw[engines.ReplicationPasswordKey].Secret)

	// The references resolve to the primary's values.
	res := f.resolve(sv.Slug)
	require.Equal(t, f.getVar(pg.ID, "POSTGRES_PASSWORD"), res.Vars["POSTGRES_PASSWORD"])

	vols, err := f.q.ListServiceVolumes(context.Background(), id)
	require.NoError(t, err)
	require.Len(t, vols, 1)

	row := f.replica(id)
	require.Equal(t, databases.ReplicaPending, row.State)
	require.Equal(t, pg.ID, row.PrimaryServiceID)
	require.Nil(t, row.LinkID, "a same-server replica needs no mesh")
	require.Equal(t, "nc_r_"+short, row.SlotName)

	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action = 'database.replica.add'").Scan(&n))
	require.Equal(t, 1, n)
	require.Empty(t, f.mesh.grants, "no mesh on one server")
}

func TestAddReplicaAcrossServersUsesTheReplicationLink(t *testing.T) {
	f := newReplicaFixture(t)
	pg := f.primary("pg")
	f.mesh.plan = mesh.LinkPlan{DevicesAdded: 1, Blockers: []mesh.Blocker{}}
	res, err := f.dbs.AddReplica(f.ctx, pg.ID, databases.AddReplicaInput{ServerID: f.server2.ID, AcceptMeshChanges: true})
	require.NoError(t, err)
	id := *res.ServiceID

	row := f.replica(id)
	require.NotNil(t, row.LinkID)
	require.NotNil(t, row.ExposureID)
	cfg := services.DecodeConfig(f.reload(id).Config)
	require.Equal(t, agentprotocol.MeshReplicationAlias(f.server.ID.String()), cfg.Database.Replica.Host)
	require.Equal(t, 15001, cfg.Database.Replica.Port, "the replica dials the mesh port, not 5432")
	require.Equal(t, []string{"replica:" + id.String()}, f.mesh.grants, "the grant belongs to the replica")
	require.Equal(t, f.server2.ID, *f.reload(id).ServerID)
}

func TestReplicaSagaPreparesClonesDeploysAndStreams(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	api := f.app("api")
	f.setVar(api.ID, "READ_URL", "${{pg.DATABASE_READ_URL}}", false)
	id := f.addReplica(pg.ID, f.server.ID)
	short := agentprotocol.Short(id.String())
	replPW := f.getVar(id, engines.ReplicationPasswordKey)

	// 1. prepare the primary.
	done, err := f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.False(t, done)
	prep, ok := f.commandByKey("repl_prepare:" + id.String() + ":1")
	require.True(t, ok)
	require.Equal(t, agentprotocol.KindDBAdmin, prep.Kind)
	require.Equal(t, f.server.ID, prep.ServerID)
	var pp agentprotocol.DBAdminPayload
	require.NoError(t, json.Unmarshal(prep.Payload, &pp))
	require.Equal(t, agentprotocol.DBActionReplPrepare, pp.Action)
	require.Equal(t, pg.ID.String(), pp.ServiceID)
	require.Equal(t, "nebula_r_"+short, pp.Repl.Role)
	require.Equal(t, "nc_r_"+short, pp.Repl.Slot)
	require.Equal(t, 4096, pp.Repl.MaxSlotWALKeepMB, "min(4 GiB, 10 % of the disk)")
	require.NotContains(t, string(prep.Payload), replPW, "the password never rides in a payload")
	secrets, err := f.dbs.SecretsForCommand(bg, prep.ID, prep.Kind, prep.Payload)
	require.NoError(t, err)
	require.Equal(t, replPW, secrets[agentprotocol.SecretDBAdminNewPassword])
	require.Equal(t, f.getVar(pg.ID, "POSTGRES_PASSWORD"), secrets[agentprotocol.SecretDBAdminPassword])
	require.Equal(t, databases.ReplicaPreparing, f.replica(id).State)

	// Waiting for the agent changes nothing.
	done, err = f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.False(t, done)
	var cmds int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM agent_commands WHERE kind = 'db_admin'").Scan(&cmds))
	require.Equal(t, 1, cmds, "a repeated step finds its command")

	// 2. clone (same server: no link step).
	f.ok("repl_prepare:"+id.String()+":1", agentprotocol.DBAdminResult{Action: agentprotocol.DBActionReplPrepare})
	done, err = f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.False(t, done)
	clone, ok := f.commandByKey("repl_clone:" + id.String() + ":1")
	require.True(t, ok)
	var cp agentprotocol.ReplicaClonePayload
	require.NoError(t, json.Unmarshal(clone.Payload, &cp))
	require.Equal(t, id.String(), cp.ServiceID)
	require.Equal(t, "pg", cp.Host)
	require.Equal(t, 5432, cp.Port)
	require.Equal(t, "data", cp.DataVolume)
	require.Equal(t, "nebula_r_"+short, cp.User)
	require.Equal(t, []string{agentprotocol.EnvInternalNetwork(f.env.ID.String())}, cp.Networks)
	require.NotContains(t, string(clone.Payload), replPW)
	csecrets, err := f.dbs.SecretsForCommand(bg, clone.ID, clone.Kind, clone.Payload)
	require.NoError(t, err)
	require.Equal(t, replPW, csecrets[agentprotocol.SecretReplPassword])
	require.Equal(t, databases.ReplicaCloning, f.replica(id).State)
	require.Equal(t, clone.ID, *f.replica(id).CloneCommandID)

	// 3. a finished clone deploys the replica and waits for streaming.
	f.ok("repl_clone:"+id.String()+":1", agentprotocol.ReplicaCloneResult{})
	done, err = f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.False(t, done)
	row := f.replica(id)
	require.Equal(t, databases.ReplicaDeploying, row.State)
	require.NotNil(t, f.reload(id).CurrentDeploymentID, "the replica was deployed through the normal path")

	// 4. streaming moves it, once, and restages the consumers of the group.
	gen := f.generation(api.ID)
	now := time.Now()
	require.NoError(t, f.dbs.ObserveReplication(bg, id, agentprotocol.ReplicationStatus{
		Role: agentprotocol.ReplRoleReplica, InRecovery: true, ReceiverStatus: "streaming", ReceiveLSN: "0/3000000", ReplayLSN: "0/3000000", CheckedAt: now,
	}))
	require.Equal(t, databases.ReplicaStreaming, f.replica(id).State)
	require.Equal(t, gen+1, f.generation(api.ID), "reads now include the replica: the consumer is restaged, not deployed")
	done, err = f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.True(t, done)

	// Lag numbers alone restage nothing.
	size := int64(10 << 20)
	require.NoError(t, f.dbs.ObserveReplication(bg, pg.ID, agentprotocol.ReplicationStatus{
		Role: agentprotocol.ReplRolePrimary, CheckedAt: now.Add(time.Second),
		Slots:    []agentprotocol.ReplicationSlot{{Name: "nc_r_" + short, Active: true, WALStatus: "reserved"}},
		Standbys: []agentprotocol.ReplicationStandby{{ApplicationName: "nc_r_" + short, State: "streaming", LagBytes: size}},
	}))
	row = f.replica(id)
	require.Equal(t, databases.ReplicaStreaming, row.State)
	require.Equal(t, size, *row.LagBytes)
	require.Equal(t, "reserved", *row.WalStatus)
	require.Equal(t, gen+1, f.generation(api.ID))

	// The consumer's read URL now names the replica; the writer stays the primary.
	f.setVar(api.ID, "WRITE_URL", "${{pg.DATABASE_WRITE_URL}}", false)
	res := f.resolve("api")
	pw := f.getVar(pg.ID, "POSTGRES_PASSWORD")
	rslug := f.reload(id).Slug
	require.Equal(t, "postgres://postgres:"+pw+"@"+rslug+":5432/app", res.Vars["READ_URL"])
	require.Equal(t, "postgres://postgres:"+pw+"@pg:5432/app", res.Vars["WRITE_URL"])
}

func TestObservationDrivesBrokenLaggingAndRebuildStates(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	api := f.app("api")
	f.setVar(api.ID, "READ_URL", "${{pg.DATABASE_READ_URL}}", false)
	id := f.streamingReplica(pg.ID, f.server.ID)
	short := agentprotocol.Short(id.String())
	var alerts []databases.ReplicaEvent
	f.dbs.SetReplicas(databases.ReplicaDeps{Mesh: f.mesh, Links: f.links, Deployer: &fakeDeployer{f: f}, Notify: func(_ context.Context, ev databases.ReplicaEvent) {
		alerts = append(alerts, ev)
	}})
	now := time.Now()
	streaming := func(at time.Time, lag int64) agentprotocol.ReplicationStatus {
		return agentprotocol.ReplicationStatus{
			Role: agentprotocol.ReplRolePrimary, CheckedAt: at,
			Standbys: []agentprotocol.ReplicationStandby{{ApplicationName: "nc_r_" + short, State: "streaming", LagBytes: lag}},
		}
	}

	// Over the threshold for less than five minutes stays streaming; longer is lagging.
	_, err := f.pool.Exec(bg, "UPDATE database_replicas SET last_streaming_at = $2 WHERE service_id = $1", id, now.Add(-time.Minute))
	require.NoError(t, err)
	require.NoError(t, f.dbs.ObserveReplication(bg, pg.ID, streaming(now, 200<<20)))
	require.Equal(t, databases.ReplicaStreaming, f.replica(id).State)
	_, err = f.pool.Exec(bg, "UPDATE database_replicas SET last_streaming_at = $2 WHERE service_id = $1", id, now.Add(-10*time.Minute))
	require.NoError(t, err)
	gen := f.generation(api.ID)
	require.NoError(t, f.dbs.ObserveReplication(bg, pg.ID, streaming(now, 200<<20)))
	require.Equal(t, databases.ReplicaLagging, f.replica(id).State)
	require.Equal(t, gen, f.generation(api.ID), "lagging still serves reads: no restage")
	require.Len(t, alerts, 1)
	require.Equal(t, databases.NotifyReplicaLagging, alerts[0].Type)

	// Caught up again.
	require.NoError(t, f.dbs.ObserveReplication(bg, pg.ID, streaming(now.Add(time.Second), 1024)))
	require.Equal(t, databases.ReplicaStreaming, f.replica(id).State)

	// Not streaming for more than two minutes: broken, and the consumers are restaged.
	_, err = f.pool.Exec(bg, "UPDATE database_replicas SET last_streaming_at = $2 WHERE service_id = $1", id, now.Add(-10*time.Minute))
	require.NoError(t, err)
	require.NoError(t, f.dbs.ObserveReplication(bg, id, agentprotocol.ReplicationStatus{
		Role: agentprotocol.ReplRoleReplica, InRecovery: true, ReceiverStatus: "stopped", CheckedAt: now.Add(2 * time.Second),
	}))
	require.Equal(t, databases.ReplicaBroken, f.replica(id).State)
	require.Equal(t, gen+1, f.generation(api.ID), "a health-class change restages the consumers")
	require.Len(t, alerts, 2)
	require.Equal(t, databases.NotifyReplicaBroken, alerts[1].Type)
	// A repeated observation does not alert again.
	require.NoError(t, f.dbs.ObserveReplication(bg, id, agentprotocol.ReplicationStatus{
		Role: agentprotocol.ReplRoleReplica, ReceiverStatus: "stopped", CheckedAt: now.Add(3 * time.Second),
	}))
	require.Len(t, alerts, 2)

	// It heals when streaming resumes.
	require.NoError(t, f.dbs.ObserveReplication(bg, pg.ID, streaming(now.Add(4*time.Second), 0)))
	require.Equal(t, databases.ReplicaStreaming, f.replica(id).State)

	// A lost slot needs a rebuild, and only a rebuild leaves that state.
	lost := streaming(now.Add(5*time.Second), 0)
	lost.Slots = []agentprotocol.ReplicationSlot{{Name: "nc_r_" + short, WALStatus: "lost"}}
	require.NoError(t, f.dbs.ObserveReplication(bg, pg.ID, lost))
	require.Equal(t, databases.ReplicaNeedsRebuild, f.replica(id).State)
	require.NoError(t, f.dbs.ObserveReplication(bg, pg.ID, streaming(now.Add(6*time.Second), 0)))
	require.Equal(t, databases.ReplicaNeedsRebuild, f.replica(id).State)

	// A stale observation is shown as unknown.
	_, err = f.pool.Exec(bg, "UPDATE database_replicas SET state = 'streaming', observed_at = now() - interval '10 minutes' WHERE service_id = $1", id)
	require.NoError(t, err)
	v, err := f.dbs.Replication(f.ctx, pg.ID)
	require.NoError(t, err)
	require.Equal(t, databases.RolePrimary, v.Role)
	require.Len(t, v.Replicas, 1)
	require.Equal(t, "unknown", v.Replicas[0].State)
}

// A server may only report the replication state of databases placed on it:
// service IDs are not secrets, so a compromised agent must not be able to flip
// another server's replicas or stamp its own promotion-wait numbers.
func TestObservedReplicationIsBoundToTheReportingServer(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	id := f.streamingReplica(pg.ID, f.server.ID)
	short := agentprotocol.Short(id.String())
	lost := func(at time.Time) agentprotocol.Observed {
		return agentprotocol.Observed{Services: []agentprotocol.ObservedService{{ServiceID: pg.ID.String(), Replication: &agentprotocol.ReplicationStatus{
			Role: agentprotocol.ReplRolePrimary, CheckedAt: at,
			Slots: []agentprotocol.ReplicationSlot{{Name: "nc_r_" + short, WALStatus: "lost"}},
		}}}}
	}
	replicaSide := func(at time.Time) agentprotocol.Observed {
		return agentprotocol.Observed{Services: []agentprotocol.ObservedService{{ServiceID: id.String(), Replication: &agentprotocol.ReplicationStatus{
			Role: agentprotocol.ReplRoleReplica, InRecovery: true, ReceiverStatus: "streaming", ReplayLSN: "FFFFFFFF/0", CheckedAt: at,
		}}}}
	}

	// Another server's agent, naming this primary and this replica, changes nothing.
	other := uuid.New()
	require.NoError(t, f.dbs.HandleObserved(bg, other, lost(time.Now())))
	require.NoError(t, f.dbs.HandleObserved(bg, other, replicaSide(time.Now())))
	row := f.replica(id)
	require.Equal(t, databases.ReplicaStreaming, row.State)
	require.Nil(t, row.WalStatus)
	require.Nil(t, row.ReplayLsn)

	// The server the database runs on is believed, with the control plane's clock
	// when it names a time far from it.
	before := time.Now()
	require.NoError(t, f.dbs.HandleObserved(bg, f.server.ID, replicaSide(before.Add(24*time.Hour))))
	row = f.replica(id)
	require.NotNil(t, row.ObservedAt)
	require.WithinDuration(t, before, *row.ObservedAt, databases.MaxObservedSkew)
	require.NoError(t, f.dbs.HandleObserved(bg, f.server.ID, lost(time.Now())))
	require.Equal(t, databases.ReplicaNeedsRebuild, f.replica(id).State)
}

func TestGroupExportsPickReadersPerConsumer(t *testing.T) {
	srvA, srvB, srvC := uuid.New(), uuid.New(), uuid.New()
	pgID, r1, r2, r3 := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	spec := engines.Spec{Engine: engines.Postgres, Version: "17", DatabaseName: "app", Username: "postgres"}
	vars := map[string]string{"POSTGRES_PASSWORD": "pw", "POSTGRES_USER": "postgres", "POSTGRES_DB": "app"}
	run := func(consumer *uuid.UUID, group []services.GroupMember) map[string]string {
		out := map[string]string{}
		for _, e := range databases.ExportProvider(services.ExportInput{
			Slug: "pg", Kind: services.KindDatabase, Cfg: services.Config{Database: &spec}, Host: "pg", Port: 5432, Consumer: consumer, Group: group,
			Var: func(k string) (string, bool, bool) { v, ok := vars[k]; return v, k == "POSTGRES_PASSWORD", ok },
		}) {
			out[e.Key] = e.Value
		}
		return out
	}
	primary := services.GroupMember{ID: pgID, Slug: "pg", IsPrimary: true, Port: 5432, ServerID: &srvA}
	rep := func(id uuid.UUID, slug, state string, srv *uuid.UUID, mesh int) services.GroupMember {
		return services.GroupMember{ID: id, Slug: slug, State: state, Port: 5432, ServerID: srv, MeshPort: mesh}
	}

	// No group: the writer and the reader are the service itself.
	x := run(&srvA, nil)
	require.Equal(t, "postgres://postgres:pw@pg:5432/app", x["DATABASE_WRITE_URL"])
	require.Equal(t, "postgres://postgres:pw@pg:5432/app", x["DATABASE_READ_URL"])
	require.Equal(t, "pg:5432", x["DATABASE_READ_HOSTS"])

	// A replica on the consumer's server wins over one elsewhere.
	g := []services.GroupMember{primary, rep(r1, "pg-replica-1", "streaming", &srvA, 0), rep(r2, "pg-replica-2", "streaming", &srvB, 15001)}
	x = run(&srvA, g)
	require.Equal(t, "postgres://postgres:pw@pg:5432/app", x["DATABASE_WRITE_URL"])
	require.Equal(t, "postgres://postgres:pw@pg-replica-1:5432/app", x["DATABASE_READ_URL"])
	require.Equal(t, "pg-replica-1", x["DATABASE_READ_HOST"])
	require.Equal(t, "5432", x["DATABASE_READ_PORT"])
	require.Equal(t, "postgres://postgres:pw@pg:5432/app", x["DATABASE_URL"], "DATABASE_URL describes the service itself")

	// A consumer on a third server reaches the replicas through the mesh: the
	// writer's port is its own mesh port (none yet: the container port) and the
	// remote replica's is its mesh port.
	x = run(&srvC, g)
	require.Equal(t, "postgres://postgres:pw@pg-replica-1:5432,pg-replica-2:15001/app", x["DATABASE_READ_URL"], "no local replica: all streaming ones, multi-host")
	require.Equal(t, "pg-replica-1:5432,pg-replica-2:15001", x["DATABASE_READ_HOSTS"])

	// A consumer on the replica's server reads the remote replica over the mesh.
	g = []services.GroupMember{primary, rep(r2, "pg-replica-2", "lagging", &srvB, 15001)}
	x = run(&srvA, g)
	require.Equal(t, "postgres://postgres:pw@pg-replica-2:15001/app", x["DATABASE_READ_URL"])
	require.Equal(t, "15001", x["DATABASE_READ_PORT"])

	// The primary on another server is reached through its mesh port.
	remotePrimary := primary
	remotePrimary.MeshPort = 15007
	g = []services.GroupMember{remotePrimary, rep(r1, "pg-replica-1", "streaming", &srvB, 0)}
	x = run(&srvB, g)
	require.Equal(t, "postgres://postgres:pw@pg:15007/app", x["DATABASE_WRITE_URL"])
	require.Equal(t, "15007", x["DATABASE_WRITE_PORT"])
	require.Equal(t, "postgres://postgres:pw@pg-replica-1:5432/app", x["DATABASE_READ_URL"])

	// Replicas that do not serve reads are not readers; with none left the
	// reader falls back to the writer.
	g = []services.GroupMember{primary, rep(r1, "a", "broken", &srvA, 0), rep(r2, "b", "needs_rebuild", &srvA, 0), rep(r3, "c", "cloning", &srvA, 0)}
	x = run(&srvA, g)
	require.Equal(t, "postgres://postgres:pw@pg:5432/app", x["DATABASE_READ_URL"])
	require.Equal(t, "pg", x["DATABASE_READ_HOST"])

	// Writer and reader exports are the same from a replica's own view.
	g = []services.GroupMember{primary, rep(r1, "pg-replica-1", "streaming", &srvA, 0)}
	in := services.ExportInput{
		Slug: "pg-replica-1", Kind: services.KindDatabase, Cfg: services.Config{Database: &spec}, Host: "pg-replica-1", Port: 5432, Consumer: &srvA, Group: g,
		Var: func(k string) (string, bool, bool) { v, ok := vars[k]; return v, k == "POSTGRES_PASSWORD", ok },
	}
	got := map[string]string{}
	secret := map[string]bool{}
	for _, e := range databases.ExportProvider(in) {
		got[e.Key], secret[e.Key] = e.Value, e.Secret
	}
	require.Equal(t, "postgres://postgres:pw@pg:5432/app", got["DATABASE_WRITE_URL"])
	require.Equal(t, "postgres://postgres:pw@pg-replica-1:5432/app", got["DATABASE_URL"], "on a replica DATABASE_URL is the read-only server")
	require.True(t, secret["DATABASE_WRITE_URL"] && secret["DATABASE_READ_URL"])
	require.False(t, secret["DATABASE_READ_HOST"])
}

func (f *replicaFixture) addSchedule(service uuid.UUID) {
	f.t.Helper()
	bg := context.Background()
	var dest uuid.UUID
	require.NoError(f.t, f.pool.QueryRow(bg,
		"INSERT INTO backup_destinations (workspace_id, name, kind, local_dir) VALUES ($1, 'd', 'local', 'd') RETURNING id", f.ws.ID).Scan(&dest))
	_, err := f.pool.Exec(bg, "INSERT INTO backup_schedules (workspace_id, service_id, destination_id, cron, next_run_at) VALUES ($1, $2, $3, '0 3 * * *', now())", f.ws.ID, service, dest)
	require.NoError(f.t, err)
}

func TestPromoteSwitchoverMovesTheGroup(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	api := f.app("api")
	f.setVar(api.ID, "DATABASE_URL", "${{pg.DATABASE_URL}}", false)
	f.setVar(api.ID, "BOTH", "u=${{urlencode(pg.POSTGRES_USER)}} h=${{pg.NEBULA_PRIVATE_HOST}} x=${{other.X}}", false)
	f.setVar(api.ID, "UNRELATED", "plain", false)
	target := f.streamingReplica(pg.ID, f.server.ID)
	other := f.streamingReplica(pg.ID, f.server.ID)
	f.addSchedule(pg.ID)
	targetSv, otherSv := f.reload(target), f.reload(other)
	pw := f.getVar(pg.ID, "POSTGRES_PASSWORD")
	_, err := f.pool.Exec(bg, "UPDATE database_replicas SET replay_lsn = '0/1000000', observed_at = now() - interval '1 hour' WHERE service_id = $1", target)
	require.NoError(t, err)

	// The plan: switchover, who is rewritten, who must be rebuilt, what moves.
	res, err := f.dbs.Promote(f.ctx, target, databases.PromoteInput{DryRun: true})
	require.NoError(t, err)
	require.True(t, res.DryRun)
	plan := res.Plan
	require.Equal(t, databases.ModeSwitchover, plan.Mode)
	require.Empty(t, plan.Blockers)
	require.Equal(t, "pg", plan.OldPrimary.Slug)
	require.Len(t, plan.Consumers, 1)
	require.Equal(t, "api", plan.Consumers[0].Slug)
	require.ElementsMatch(t, []string{"BOTH", "DATABASE_URL"}, plan.Consumers[0].Keys)
	require.Equal(t, []databases.ServiceRef{{ID: other, Slug: otherSv.Slug}}, plan.RebuildReplicas)
	require.Equal(t, 1, plan.SchedulesMoving)

	// Real requests need the typed confirmation.
	_, err = f.dbs.Promote(f.ctx, target, databases.PromoteInput{Confirm: "nope"})
	require.Equal(t, "promote_confirmation_required", apiCode(t, err))
	res, err = f.dbs.Promote(f.ctx, target, databases.PromoteInput{Confirm: targetSv.Slug})
	require.NoError(t, err)
	require.NotNil(t, res.PromotionID)
	require.Equal(t, databases.ReplicaPromoting, f.replica(target).State)
	_, err = f.dbs.Promote(f.ctx, other, databases.PromoteInput{Confirm: otherSv.Slug})
	require.Equal(t, "promote_in_progress", apiCode(t, err), "one promotion per group at a time")
	promo := *res.PromotionID
	key := func(step string) string { return "promo:" + promo.String() + ":" + step }
	adv := func() bool {
		done, err := f.dbs.AdvancePromotion(bg, promo)
		require.NoError(t, err)
		return done
	}

	// 1. freeze the old primary.
	require.False(t, adv())
	c, ok := f.commandByKey(key("freeze"))
	require.True(t, ok)
	var fp agentprotocol.DBAdminPayload
	require.NoError(t, json.Unmarshal(c.Payload, &fp))
	require.Equal(t, agentprotocol.DBActionReplFreeze, fp.Action)
	require.Equal(t, pg.ID.String(), fp.ServiceID)
	f.ok(key("freeze"), agentprotocol.DBAdminResult{Action: agentprotocol.DBActionReplFreeze})

	// 2. fence and stop it.
	require.False(t, adv())
	_, err = f.q.GetDatabaseFence(bg, pg.ID)
	require.NoError(t, err, "the fence is persisted before the stop")
	require.Equal(t, services.DesiredStopped, f.reload(pg.ID).DesiredState)
	sc, ok := f.commandByKey(key("stop"))
	require.True(t, ok)
	require.Equal(t, agentprotocol.KindStopService, sc.Kind)
	f.ok(key("stop"), nil)

	// 3. its final position, read from the stopped data directory.
	require.False(t, adv())
	lc, ok := f.commandByKey(key("lsn"))
	require.True(t, ok)
	var lp agentprotocol.DBAdminPayload
	require.NoError(t, json.Unmarshal(lc.Payload, &lp))
	require.Equal(t, agentprotocol.DBActionReplFinalLSN, lp.Action)
	require.Equal(t, "data", lp.Repl.DataVolume)
	f.ok(key("lsn"), agentprotocol.DBAdminResult{FinalLSN: "0/5000000"})

	// 4. the replica reports that it has replayed it. The reports arrive while the
	// replica is `promoting`: they must be recorded (the wait decides from them),
	// a primary-side report must not refresh a stale position, and the state stays.
	require.False(t, adv(), "no observation since the promotion started: keep waiting")
	require.NoError(t, f.dbs.ObserveReplication(bg, pg.ID, agentprotocol.ReplicationStatus{
		Role: agentprotocol.ReplRolePrimary, CheckedAt: time.Now(),
		Standbys: []agentprotocol.ReplicationStandby{{ApplicationName: f.replica(target).SlotName, State: "streaming"}},
	}))
	require.False(t, adv(), "a primary-side report says nothing about the replica's replay position")
	require.NoError(t, f.dbs.ObserveReplication(bg, target, agentprotocol.ReplicationStatus{
		Role: agentprotocol.ReplRoleReplica, InRecovery: true, ReceiverStatus: "", ReceiveLSN: "0/4000000", ReplayLSN: "0/4000000", CheckedAt: time.Now(),
	}))
	require.Equal(t, databases.ReplicaPromoting, f.replica(target).State, "an observation never moves a promoting replica")
	require.Equal(t, "0/4000000", *f.replica(target).ReplayLsn)
	require.False(t, adv(), "replayed 0/4000000 of 0/5000000: still waiting")
	_, ok = f.commandByKey(key("promote:1"))
	require.False(t, ok)
	require.NoError(t, f.dbs.ObserveReplication(bg, target, agentprotocol.ReplicationStatus{
		Role: agentprotocol.ReplRoleReplica, InRecovery: true, ReceiveLSN: "0/5000078", ReplayLSN: "0/5000078", CheckedAt: time.Now(),
	}))
	require.Equal(t, databases.ReplicaPromoting, f.replica(target).State)
	require.False(t, adv())
	pc, ok := f.commandByKey(key("promote:1"))
	require.True(t, ok)
	require.Equal(t, target.String(), jsonField(t, pc.Payload, "serviceId"))
	var pp agentprotocol.DBAdminPayload
	require.NoError(t, json.Unmarshal(pc.Payload, &pp))
	require.Equal(t, agentprotocol.DBActionReplPromote, pp.Action)
	require.Equal(t, "0/5000000", pp.Repl.LSN, "the agent re-checks the replica's replay position")
	require.Equal(t, f.server.ID, pc.ServerID)
	gen := f.generation(api.ID)
	f.ok(key("promote:1"), agentprotocol.DBAdminResult{Action: agentprotocol.DBActionReplPromote})

	// 5. repoint, 6. links, done.
	require.True(t, adv())
	p, err := f.q.GetDatabasePromotion(bg, promo)
	require.NoError(t, err)
	require.Equal(t, "succeeded", p.Status, p.Message)

	_, err = f.q.GetDatabaseReplica(bg, target)
	require.True(t, err != nil, "the replica row is gone: it is a primary now")
	cfg := services.DecodeConfig(f.reload(target).Config)
	require.Nil(t, cfg.Database.Replica)
	require.Nil(t, cfg.StartCommand, "the image default starts a primary")
	raw, err := services.LoadRawVars(bg, f.q, f.box, target)
	require.NoError(t, err)
	require.Equal(t, "postgres", raw["POSTGRES_USER"].Value, "references became literal copies")
	require.Equal(t, pw, raw["POSTGRES_PASSWORD"].Value)
	require.True(t, raw["POSTGRES_PASSWORD"].Secret)
	require.NotContains(t, raw, engines.ReplicationPasswordKey)

	// The old primary is fenced and stopped, never unfenced; its data stays.
	_, err = f.q.GetDatabaseFence(bg, pg.ID)
	require.NoError(t, err)
	require.Equal(t, services.DesiredStopped, f.reload(pg.ID).DesiredState)
	v, err := f.dbs.Replication(f.ctx, pg.ID)
	require.NoError(t, err)
	require.Equal(t, databases.RoleFenced, v.Role)
	require.True(t, v.Rebuildable)
	vn, err := f.dbs.Replication(f.ctx, target)
	require.NoError(t, err)
	require.Equal(t, databases.RolePrimary, vn.Role)
	require.Len(t, vn.Replicas, 1, "the other replica follows the new primary")
	require.Equal(t, databases.ReplicaNeedsRebuild, vn.Replicas[0].State)
	require.NotNil(t, vn.Promotion)
	require.Equal(t, "succeeded", vn.Promotion.Status)

	// The other replica follows the new primary and needs a rebuild.
	or := f.replica(other)
	require.Equal(t, target, or.PrimaryServiceID)
	require.Equal(t, databases.ReplicaNeedsRebuild, or.State)
	oraw, err := services.LoadRawVars(bg, f.q, f.box, other)
	require.NoError(t, err)
	require.Equal(t, "${{"+targetSv.Slug+".POSTGRES_PASSWORD}}", oraw["POSTGRES_PASSWORD"].Value)
	require.Contains(t, f.mesh.released, other, "its grants to the old primary are released")

	// Backup schedules moved with the role; consumers are rewritten and staged.
	var onNew, onOld int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM backup_schedules WHERE service_id = $1", target).Scan(&onNew))
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM backup_schedules WHERE service_id = $1", pg.ID).Scan(&onOld))
	require.Equal(t, 1, onNew)
	require.Zero(t, onOld)
	require.Equal(t, "${{"+targetSv.Slug+".DATABASE_URL}}", f.getVar(api.ID, "DATABASE_URL"))
	require.Equal(t, "u=${{urlencode("+targetSv.Slug+".POSTGRES_USER)}} h=${{"+targetSv.Slug+".NEBULA_PRIVATE_HOST}} x=${{other.X}}", f.getVar(api.ID, "BOTH"))
	require.Equal(t, "plain", f.getVar(api.ID, "UNRELATED"))
	require.Greater(t, f.generation(api.ID), gen, "every rewrite is a staged change")
	require.Equal(t, f.reload(api.ID).AppliedGeneration, int64(0), "nothing was deployed")
	require.Len(t, f.links.consumers, 1)
	require.Equal(t, []uuid.UUID{api.ID}, f.links.consumers[0], "the link domain connects the rewritten consumers")

	// Audited.
	for _, action := range []string{"database.promote", "database.fence"} {
		var n int
		require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM audit_events WHERE action = $1", action).Scan(&n))
		require.Equal(t, 1, n, action)
	}

	// A fenced service cannot be deployed: it would be a second writer.
	pl, err := f.deployPlan(pg.ID)
	require.NoError(t, err)
	require.Contains(t, pl, "database_fenced")
}

func jsonField(t *testing.T, raw json.RawMessage, key string) string {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	s, _ := m[key].(string)
	return s
}

// deployPlan returns the blocker codes of deploying a service.
func (f *replicaFixture) deployPlan(id uuid.UUID) (string, error) {
	sc, err := services.LoadScopeInternal(context.Background(), f.q, id)
	if err != nil {
		return "", err
	}
	_, blocking, err := f.dbs.PlanCheck(context.Background(), f.q, sc, sc.Config())
	if err != nil {
		return "", err
	}
	var codes []string
	for _, b := range blocking {
		codes = append(codes, b.Code)
	}
	return strings.Join(codes, ","), nil
}

func TestPromoteSwitchoverAbortsWhenTheReplicaCannotCatchUp(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	target := f.streamingReplica(pg.ID, f.server.ID)
	sv := f.reload(target)
	_, err := f.pool.Exec(bg, "UPDATE database_replicas SET replay_lsn = '0/1000000', observed_at = now() WHERE service_id = $1", target)
	require.NoError(t, err)

	res, err := f.dbs.Promote(f.ctx, target, databases.PromoteInput{Confirm: sv.Slug})
	require.NoError(t, err)
	promo := *res.PromotionID
	key := func(step string) string { return "promo:" + promo.String() + ":" + step }
	adv := func() bool {
		done, err := f.dbs.AdvancePromotion(bg, promo)
		require.NoError(t, err)
		return done
	}
	require.False(t, adv())
	f.ok(key("freeze"), nil)
	require.False(t, adv())
	f.ok(key("stop"), nil)
	require.False(t, adv())
	f.ok(key("lsn"), agentprotocol.DBAdminResult{FinalLSN: "0/5000000"})
	require.False(t, adv(), "the replica is behind: it keeps waiting")
	require.Equal(t, "wait", mustPromotion(t, f, promo).Step)

	// Five minutes later it still is: the switchover is rolled back, nothing lost.
	_, err = f.pool.Exec(bg, "UPDATE agent_commands SET finished_at = now() - interval '10 minutes' WHERE idempotency_key = $1", key("lsn"))
	require.NoError(t, err)
	require.False(t, adv(), "unfence, then restart the old primary")
	require.Equal(t, "abort_restart", mustPromotion(t, f, promo).Step)
	_, err = f.q.GetDatabaseFence(bg, pg.ID)
	require.Error(t, err, "the fence is removed on abort")
	require.Equal(t, services.DesiredRunning, f.reload(pg.ID).DesiredState)
	rc, ok := f.commandByKey(key("restart"))
	require.True(t, ok)
	require.Equal(t, agentprotocol.KindRestartService, rc.Kind)
	f.ok(key("restart"), nil)
	require.False(t, adv(), "then make it writable again")
	uc, ok := f.commandByKey(key("unfreeze"))
	require.True(t, ok)
	var up agentprotocol.DBAdminPayload
	require.NoError(t, json.Unmarshal(uc.Payload, &up))
	require.Equal(t, agentprotocol.DBActionReplUnfreeze, up.Action)
	f.ok(key("unfreeze"), nil)
	require.True(t, adv())

	p := mustPromotion(t, f, promo)
	require.Equal(t, "failed", p.Status)
	require.Contains(t, p.Message, "nothing was lost")
	require.Equal(t, databases.ReplicaStreaming, f.replica(target).State, "the replica is a replica again")
	require.Equal(t, pg.ID, f.replica(target).PrimaryServiceID)
}

func mustPromotion(t *testing.T, f *replicaFixture, id uuid.UUID) db.DatabasePromotion {
	t.Helper()
	p, err := f.q.GetDatabasePromotion(context.Background(), id)
	require.NoError(t, err)
	return p
}

func TestForcedPromoteNeedsAcceptedDataLossAndFencesFirst(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	target := f.streamingReplica(pg.ID, f.server2.ID)
	pg2 := f.primary("pg2")
	t2 := f.streamingReplica(pg2.ID, f.server2.ID)
	sv := f.reload(target)
	lag := int64(3 << 20)
	_, err := f.pool.Exec(bg, "UPDATE database_replicas SET lag_bytes = $2 WHERE service_id = $1", target, lag)
	require.NoError(t, err)
	// The old primaries' server goes away.
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)

	res, err := f.dbs.Promote(f.ctx, target, databases.PromoteInput{DryRun: true})
	require.NoError(t, err)
	require.Equal(t, databases.ModeForced, res.Plan.Mode)
	require.NotEmpty(t, res.Plan.DataLossWarning)
	require.Contains(t, res.Plan.DataLossWarning, "3.0 MiB")
	require.NotNil(t, res.Plan.LagBytes)

	_, err = f.dbs.Promote(f.ctx, target, databases.PromoteInput{Confirm: sv.Slug})
	require.Equal(t, "data_loss_not_accepted", apiCode(t, err))
	_, err = f.q.GetDatabaseFence(bg, pg.ID)
	require.Error(t, err, "a refused promote fences nothing")

	res, err = f.dbs.Promote(f.ctx, target, databases.PromoteInput{Confirm: sv.Slug, AcceptDataLoss: true})
	require.NoError(t, err)
	_, err = f.q.GetDatabaseFence(bg, pg.ID)
	require.NoError(t, err, "the fence is persisted before the replica is promoted")
	promo := *res.PromotionID
	require.Equal(t, "promote", mustPromotion(t, f, promo).Step, "no freeze, stop or final position without the old primary")
	require.True(t, mustPromotion(t, f, promo).AcceptDataLoss)

	done, err := f.dbs.AdvancePromotion(bg, promo)
	require.NoError(t, err)
	require.False(t, done)
	c, ok := f.commandByKey("promo:" + promo.String() + ":promote:1")
	require.True(t, ok)
	require.Equal(t, f.server2.ID, c.ServerID)
	var pp agentprotocol.DBAdminPayload
	require.NoError(t, json.Unmarshal(c.Payload, &pp))
	require.Empty(t, pp.Repl.LSN, "a forced promote has no position to wait for")
	f.ok("promo:"+promo.String()+":promote:1", nil)
	done, err = f.dbs.AdvancePromotion(bg, promo)
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, "succeeded", mustPromotion(t, f, promo).Status)
	_, err = f.q.GetDatabaseReplica(bg, target)
	require.Error(t, err)

	// A promotion that fails to promote leaves the fence in place.
	r2, err := f.dbs.Promote(f.ctx, t2, databases.PromoteInput{Confirm: f.reload(t2).Slug, AcceptDataLoss: true})
	require.NoError(t, err)
	for n := 1; n <= 3; n++ {
		_, err := f.dbs.AdvancePromotion(bg, *r2.PromotionID)
		require.NoError(t, err)
		f.complete("promo:"+r2.PromotionID.String()+":promote:"+string(rune('0'+n)), agentprotocol.OutcomeFailed, "pg_promote timed out", nil)
	}
	done, err = f.dbs.AdvancePromotion(bg, *r2.PromotionID)
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, "failed", mustPromotion(t, f, *r2.PromotionID).Status)
	_, err = f.q.GetDatabaseFence(bg, pg2.ID)
	require.NoError(t, err, "an unreachable old primary stays fenced")
	require.Equal(t, databases.ReplicaStreaming, f.replica(t2).State)
}

func TestPromoteBlockersAndProductionGuard(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")

	// Only replicas are promoted, and only while they serve.
	_, err := f.dbs.Promote(f.ctx, pg.ID, databases.PromoteInput{DryRun: true})
	require.Equal(t, "not_a_replica", apiCode(t, err))
	id := f.addReplica(pg.ID, f.server.ID)
	res, err := f.dbs.Promote(f.ctx, id, databases.PromoteInput{DryRun: true})
	require.NoError(t, err)
	var codes []string
	for _, b := range res.Plan.Blockers {
		codes = append(codes, b.Code)
	}
	require.Contains(t, codes, "replica_not_streaming")
	_, err = f.dbs.Promote(f.ctx, id, databases.PromoteInput{Confirm: f.reload(id).Slug})
	require.Equal(t, "replica_not_streaming", apiCode(t, err))

	// A replica whose server is offline cannot be promoted.
	rep := f.streamingReplica(pg.ID, f.server2.ID)
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server2.ID)
	require.NoError(t, err)
	res, err = f.dbs.Promote(f.ctx, rep, databases.PromoteInput{DryRun: true})
	require.NoError(t, err)
	codes = nil
	for _, b := range res.Plan.Blockers {
		codes = append(codes, b.Code)
	}
	require.Contains(t, codes, "replica_agent_offline")
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", f.server2.ID)
	require.NoError(t, err)

	// The environment is production: a token that needs approval is stopped.
	guarded := auth.WithPrincipal(bg, &auth.Principal{
		Type: auth.TypeToken, UserID: f.userID, TokenID: new(uuid.UUID), WorkspaceID: f.ws.ID, Label: "ci",
		Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
	_, err = f.dbs.Promote(guarded, rep, databases.PromoteInput{Confirm: f.reload(rep).Slug})
	require.Contains(t, []string{"approval_required", "approval_unavailable"}, apiCode(t, err), "the guard stops it (no approval store in this test)")
	require.Equal(t, databases.ReplicaStreaming, f.replica(rep).State, "nothing started without the approval")
	// A plain dry run is not guarded.
	_, err = f.dbs.Promote(guarded, rep, databases.PromoteInput{DryRun: true})
	require.NoError(t, err)
}

func TestReplicaInteractionsAreBlocked(t *testing.T) {
	f := newReplicaFixture(t)
	pg := f.primary("pg")
	rid := f.streamingReplica(pg.ID, f.server.ID)

	// A major upgrade of a primary or a replica is blocked while the group exists.
	_, err := f.dbs.Upgrade(f.ctx, pg.ID, "18", "pg")
	require.Equal(t, "replicas_exist", apiCode(t, err))
	plan, err := f.dbs.PlanUpgrade(f.ctx, pg.ID, "18")
	require.NoError(t, err)
	require.NotEmpty(t, plan.Blockers)
	_, err = f.dbs.Upgrade(f.ctx, rid, "18", f.reload(rid).Slug)
	require.Equal(t, "replicas_exist", apiCode(t, err))

	// Deleting a primary with replicas, or a replica outside the replica API.
	err = f.svcs.Delete(f.ctx, pg.ID, services.DeleteInput{Confirm: "pg"})
	require.Equal(t, "replicas_exist", apiCode(t, err))
	err = f.svcs.Delete(f.ctx, rid, services.DeleteInput{Confirm: f.reload(rid).Slug})
	require.Equal(t, "replica_remove_required", apiCode(t, err))

	// A member does not move to another server.
	srv2 := f.server2.ID
	_, err = f.svcs.Update(f.ctx, rid, f.reload(rid).Generation, services.UpdateInput{ServerID: &srv2})
	require.Equal(t, "replica_pinned", apiCode(t, err))
	_, err = f.svcs.Update(f.ctx, pg.ID, f.reload(pg.ID).Generation, services.UpdateInput{ServerID: &srv2})
	require.Equal(t, "replica_pinned", apiCode(t, err))

	// A replica's credentials are the primary's.
	_, err = f.dbs.RotateCredentials(f.ctx, rid, false)
	require.Equal(t, "replica_read_only", apiCode(t, err))

	// Nothing above changed anything.
	require.Equal(t, databases.ReplicaStreaming, f.replica(rid).State)
	require.NotNil(t, f.reload(pg.ID).CurrentDeploymentID)
}

func TestRemoveReplicaDropsTheSlotReleasesTheLinkAndDeletesTheService(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	f.mesh.plan = mesh.LinkPlan{DevicesAdded: 1, Blockers: []mesh.Blocker{}}
	id := f.streamingReplica(pg.ID, f.server2.ID)
	short := agentprotocol.Short(id.String())
	api := f.app("api")
	f.setVar(api.ID, "READ", "${{pg.DATABASE_READ_URL}}", false)
	gen := f.generation(api.ID)

	r, err := f.dbs.RemoveReplica(f.ctx, id)
	require.NoError(t, err)
	require.Equal(t, databases.ReplicaRemoving, r.Status)
	require.Equal(t, databases.ReplicaRemoving, f.replica(id).State)

	key := func(a string) string { return a + ":" + id.String() + ":1" }
	done, err := f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.False(t, done)
	sc, ok := f.commandByKey(key("repl_rm_stop"))
	require.True(t, ok)
	require.Equal(t, agentprotocol.KindStopService, sc.Kind)
	require.Equal(t, f.server2.ID, sc.ServerID)
	f.ok(key("repl_rm_stop"), nil)

	done, err = f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.False(t, done)
	dc, ok := f.commandByKey(key("repl_drop"))
	require.True(t, ok)
	require.Equal(t, f.server.ID, dc.ServerID, "the slot is dropped on the primary's server")
	var dp agentprotocol.DBAdminPayload
	require.NoError(t, json.Unmarshal(dc.Payload, &dp))
	require.Equal(t, agentprotocol.DBActionReplDrop, dp.Action)
	require.Equal(t, "nebula_r_"+short, dp.Repl.Role)
	require.Equal(t, "nc_r_"+short, dp.Repl.Slot)
	f.ok(key("repl_drop"), nil)

	// The replica service is deleted now, but the replica's row stays until the
	// agent has removed the container and the data volume: "gone" must not be
	// reported while the volume still exists.
	done, err = f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.False(t, done, "the agent has not removed the container and the volume yet")
	rm, ok := f.commandByKey(key("repl_rm_remove"))
	require.True(t, ok)
	require.Equal(t, agentprotocol.KindRemoveService, rm.Kind)
	require.Equal(t, f.server2.ID, rm.ServerID)
	var rp agentprotocol.ServiceActionPayload
	require.NoError(t, json.Unmarshal(rm.Payload, &rp))
	require.True(t, rp.DeleteVolumes)
	require.Equal(t, []string{agentprotocol.VolumeName(id.String(), "data")}, rp.VolumeNames)
	_, err = f.q.GetDatabaseReplica(bg, id)
	require.NoError(t, err, "the row stays while the removal runs")
	_, err = services.LoadScopeInternal(bg, f.q, id)
	require.Error(t, err, "the replica service is deleted")
	require.Contains(t, f.mesh.released, id, "its mesh grants are released")

	done, err = f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.False(t, done, "still waiting for the agent")
	f.ok(key("repl_rm_remove"), nil)

	done, err = f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.True(t, done)
	_, err = f.q.GetDatabaseReplica(bg, id)
	require.Error(t, err)
	require.Greater(t, f.generation(api.ID), gen, "the group changed: consumers are restaged")
	var removeCmds int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM agent_commands WHERE kind = 'remove_service'").Scan(&removeCmds))
	require.Equal(t, 1, removeCmds, "one removal, however often the saga ran")
	var n int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM audit_events WHERE action = 'database.replica.remove'").Scan(&n))
	require.Equal(t, 1, n)
}

// A replica can already stream while its first deploy is still finishing (not
// yet recorded as current). The stop must wait for that deploy, not be
// skipped: a replica left running reconnects, and the primary refuses to drop
// an active slot, which then pins WAL for good.
func TestRemoveReplicaStopsAReplicaWhoseDeployIsStillFinishing(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	id := f.streamingReplica(f.primary("pg").ID, f.server2.ID)
	var dep uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg, "UPDATE deployments SET status = 'verifying' WHERE service_id = $1 RETURNING id", id).Scan(&dep))
	_, err := f.pool.Exec(bg, "UPDATE services SET current_deployment_id = NULL WHERE id = $1", id)
	require.NoError(t, err)
	_, err = f.dbs.RemoveReplica(f.ctx, id)
	require.NoError(t, err)
	key := func(a string) string { return a + ":" + id.String() + ":1" }

	done, err := f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.False(t, done)
	_, ok := f.commandByKey(key("repl_drop"))
	require.False(t, ok, "the slot is not dropped while the replica may still be connected")

	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep)
	require.NoError(t, err)
	require.NoError(t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: id, CurrentDeploymentID: &dep, AppliedGeneration: f.generation(id)}))
	done, err = f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.False(t, done)
	sc, ok := f.commandByKey(key("repl_rm_stop"))
	require.True(t, ok, "once the deploy is over the replica is stopped")
	require.Equal(t, f.server2.ID, sc.ServerID)
	_, ok = f.commandByKey(key("repl_drop"))
	require.False(t, ok, "the drop waits for the stop")
}

// removingAtDrop runs a streaming replica's removal until the slot drop is
// queued on the primary.
func (f *replicaFixture) removingAtDrop() (id uuid.UUID, key func(attempt string) string) {
	f.t.Helper()
	bg := context.Background()
	id = f.streamingReplica(f.primary("pg").ID, f.server2.ID)
	_, err := f.dbs.RemoveReplica(f.ctx, id)
	require.NoError(f.t, err)
	_, err = f.dbs.AdvanceReplica(bg, id)
	require.NoError(f.t, err)
	f.ok("repl_rm_stop:"+id.String()+":1", nil)
	_, err = f.dbs.AdvanceReplica(bg, id)
	require.NoError(f.t, err)
	key = func(attempt string) string { return "repl_drop:" + id.String() + ":1" + attempt }
	_, ok := f.commandByKey(key(""))
	require.True(f.t, ok)
	return id, key
}

func TestRemoveReplicaDropsTheSlotAgainWhenTheDropFailed(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	id, key := f.removingAtDrop()

	f.complete(key(""), agentprotocol.OutcomeFailed, `psql exited with code 3: ERROR:  replication slot "nc_r_x" is active for PID 144`, nil)
	done, err := f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.False(t, done)
	first, _ := f.commandByKey(key(""))
	again, ok := f.commandByKey(key(":r2"))
	require.True(t, ok, "a failed drop is sent again")
	require.Equal(t, first.ServerID, again.ServerID)
	require.JSONEq(t, string(first.Payload), string(again.Payload))
	_, err = services.LoadScopeInternal(bg, f.q, id)
	require.NoError(t, err, "the replica service stays until its slot is dropped")

	f.ok(key(":r2"), nil)
	done, err = f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.False(t, done)
	_, ok = f.commandByKey("repl_rm_remove:" + id.String() + ":1")
	require.True(t, ok, "after the drop the replica service is deleted")
}

func TestRemoveReplicaGivesUpOnTheSlotAfterThreeFailedDrops(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	id, key := f.removingAtDrop()

	for _, k := range []string{key(""), key(":r2"), key(":r3")} {
		f.complete(k, agentprotocol.OutcomeFailed, "psql exited with code 3", nil)
		_, err := f.dbs.AdvanceReplica(bg, id)
		require.NoError(t, err)
	}
	_, ok := f.commandByKey(key(":r4"))
	require.False(t, ok)
	_, ok = f.commandByKey("repl_rm_remove:" + id.String() + ":1")
	require.True(t, ok, "a drop that keeps failing does not hold the removal forever")
}

// removingAtDelete puts a streaming replica at the last step of its removal and
// runs it until the replica service is deleted and the agent is asked to remove
// the container and the volume.
func (f *replicaFixture) removingAtDelete(primary uuid.UUID) (id uuid.UUID, key func(attempt string) string) {
	f.t.Helper()
	bg := context.Background()
	id = f.streamingReplica(primary, f.server2.ID)
	_, err := f.pool.Exec(bg, "UPDATE database_replicas SET state = 'removing', step = 'delete#1' WHERE service_id = $1", id)
	require.NoError(f.t, err)
	done, err := f.dbs.AdvanceReplica(bg, id)
	require.NoError(f.t, err)
	require.False(f.t, done)
	key = func(attempt string) string { return "repl_rm_remove:" + id.String() + ":1" + attempt }
	_, ok := f.commandByKey(key(""))
	require.True(f.t, ok)
	return id, key
}

func TestRemoveReplicaSendsAFailedAgentRemovalAgain(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	id, key := f.removingAtDelete(f.primary("pg").ID)

	// "remove incomplete" (a volume still in use, say): the row stays and the
	// same removal is sent again under a new key.
	f.complete(key(""), agentprotocol.OutcomeFailed, "remove incomplete: remove volume "+agentprotocol.VolumeName(id.String(), "data")+": in use", nil)
	done, err := f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.False(t, done)
	first, _ := f.commandByKey(key(""))
	again, ok := f.commandByKey(key(":r2"))
	require.True(t, ok, "a failed removal is retried")
	require.Equal(t, agentprotocol.KindRemoveService, again.Kind)
	require.Equal(t, first.ServerID, again.ServerID)
	require.JSONEq(t, string(first.Payload), string(again.Payload))
	_, err = f.q.GetDatabaseReplica(bg, id)
	require.NoError(t, err, "the row stays until the removal succeeded")

	f.ok(key(":r2"), nil)
	done, err = f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.True(t, done)
	_, err = f.q.GetDatabaseReplica(bg, id)
	require.Error(t, err)
}

func TestRemoveReplicaGivesUpAfterThreeFailedAgentRemovals(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	id, key := f.removingAtDelete(f.primary("pg").ID)

	for _, k := range []string{key(""), key(":r2")} {
		f.complete(k, agentprotocol.OutcomeFailed, "remove incomplete: still in use", nil)
		done, err := f.dbs.AdvanceReplica(bg, id)
		require.NoError(t, err)
		require.False(t, done)
	}
	f.complete(key(":r3"), agentprotocol.OutcomeFailed, "remove incomplete: still in use", nil)
	done, err := f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.True(t, done, "a removal that keeps failing does not hold the row forever")
	_, ok := f.commandByKey(key(":r4"))
	require.False(t, ok)
	_, err = f.q.GetDatabaseReplica(bg, id)
	require.Error(t, err)
}

func TestRemoveReplicaDoesNotWaitForAnOfflineAgent(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	id, _ := f.removingAtDelete(f.primary("pg").ID)

	// The command stays queued and runs when the agent returns.
	_, err := f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server2.ID)
	require.NoError(t, err)
	done, err := f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.True(t, done)
	_, err = f.q.GetDatabaseReplica(bg, id)
	require.Error(t, err)
}

func TestRebuildReplicaUsesANewVolume(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	id := f.streamingReplica(pg.ID, f.server.ID)
	// Streaming replicas may be rebuilt too, but a replica in a saga may not.
	_, err := f.pool.Exec(bg, "UPDATE database_replicas SET state = 'needs_rebuild' WHERE service_id = $1", id)
	require.NoError(t, err)

	r, err := f.dbs.RebuildReplica(f.ctx, id, databases.RebuildInput{})
	require.NoError(t, err)
	require.Equal(t, databases.ReplicaPending, r.Status)
	row := f.replica(id)
	require.Equal(t, databases.ReplicaPending, row.State)
	require.Equal(t, "rebuild_stop#2", row.Step)
	vols, err := f.q.ListServiceVolumes(bg, id)
	require.NoError(t, err)
	require.Len(t, vols, 1)
	require.Equal(t, "data-rebuild", vols[0].Name)
	retired, err := f.q.ListRetiredVolumes(bg, id)
	require.NoError(t, err)
	require.Len(t, retired, 1)
	require.Equal(t, "data", retired[0].VolumeName, "the old data stays until it is deleted")
	require.Equal(t, services.DesiredStopped, f.reload(id).DesiredState)

	_, err = f.dbs.RebuildReplica(f.ctx, id, databases.RebuildInput{})
	require.Equal(t, "replica_not_rebuildable", apiCode(t, err))

	// The saga: stop, drop the old slot, prepare again with fresh command keys.
	done, err := f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.False(t, done)
	_, ok := f.commandByKey("repl_rb_stop:" + id.String() + ":2")
	require.True(t, ok)
	f.ok("repl_rb_stop:"+id.String()+":2", nil)
	done, err = f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.False(t, done)
	_, ok = f.commandByKey("repl_rb_drop:" + id.String() + ":2")
	require.True(t, ok)
	f.ok("repl_rb_drop:"+id.String()+":2", nil)
	done, err = f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	require.False(t, done)
	_, ok = f.commandByKey("repl_prepare:" + id.String() + ":2")
	require.True(t, ok, "a rebuild is a new cycle")
}

func TestFencedOldPrimaryIsRebuiltAsAReplicaOfTheNewPrimary(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	target := f.streamingReplica(pg.ID, f.server.ID)
	f.deployOn(target, f.server.ID)
	pw := f.getVar(pg.ID, "POSTGRES_PASSWORD")

	// Promote (forced, to keep the test short: a stopped primary is not running) and finish.
	_, err := f.pool.Exec(bg, "UPDATE services SET desired_state = 'stopped' WHERE id = $1", pg.ID)
	require.NoError(t, err)
	res, err := f.dbs.Promote(f.ctx, target, databases.PromoteInput{Confirm: f.reload(target).Slug, AcceptDataLoss: true})
	require.NoError(t, err)
	key := "promo:" + res.PromotionID.String() + ":promote:1"
	_, err = f.dbs.AdvancePromotion(bg, *res.PromotionID)
	require.NoError(t, err)
	f.ok(key, nil)
	done, err := f.dbs.AdvancePromotion(bg, *res.PromotionID)
	require.NoError(t, err)
	require.True(t, done)

	// Rebuild the fenced old primary: it follows the promoted replica by default.
	r, err := f.dbs.RebuildReplica(f.ctx, pg.ID, databases.RebuildInput{})
	require.NoError(t, err)
	require.Equal(t, databases.ReplicaPending, r.Status)
	row := f.replica(pg.ID)
	require.Equal(t, target, row.PrimaryServiceID)
	cfg := services.DecodeConfig(f.reload(pg.ID).Config)
	require.NotNil(t, cfg.Database.Replica)
	require.Equal(t, f.reload(target).Slug, cfg.Database.Replica.Host)
	require.Equal(t, engines.ReplicaStartCommand(), cfg.StartCommand)
	raw, err := services.LoadRawVars(bg, f.q, f.box, pg.ID)
	require.NoError(t, err)
	require.Equal(t, "${{"+f.reload(target).Slug+".POSTGRES_PASSWORD}}", raw["POSTGRES_PASSWORD"].Value)
	require.NotEqual(t, pw, raw[engines.ReplicationPasswordKey].Value)
	require.Len(t, raw[engines.ReplicationPasswordKey].Value, 32)
	retired, err := f.q.ListRetiredVolumes(bg, pg.ID)
	require.NoError(t, err)
	require.Len(t, retired, 1)
	// The fence still protects the old data until the new volume is deployed.
	_, err = f.q.GetDatabaseFence(bg, pg.ID)
	require.NoError(t, err)
	blocked, err := f.deployPlan(pg.ID)
	require.NoError(t, err)
	require.Contains(t, blocked, "database_fenced")

	// It cannot follow a replica, nor anything in another state.
	_, err = f.dbs.RebuildReplica(f.ctx, target, databases.RebuildInput{})
	require.Equal(t, "replica_not_rebuildable", apiCode(t, err))
}

func TestServerRevokedBreaksOrFailsReplicas(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	onPrimary := f.streamingReplica(pg.ID, f.server.ID)
	remote := f.streamingReplica(pg.ID, f.server2.ID)

	f.dbs.ServerRevoked(bg, f.ws.ID, f.server2.ID)
	require.Equal(t, databases.ReplicaFailed, f.replica(remote).State)
	require.Equal(t, databases.ReplicaStreaming, f.replica(onPrimary).State)

	f.dbs.ServerRevoked(bg, f.ws.ID, f.server.ID)
	r := f.replica(onPrimary)
	require.Equal(t, databases.ReplicaFailed, r.State, "it ran on the revoked server too")
	// A replica on another server whose primary was lost is broken with the hint.
	pg2 := f.primary("pg2")
	rem2 := f.streamingReplica(pg2.ID, f.server2.ID)
	_, err := f.pool.Exec(bg, "UPDATE services SET server_id = $2 WHERE id = $1", pg2.ID, f.server.ID)
	require.NoError(t, err)
	f.dbs.ServerRevoked(bg, f.ws.ID, f.server.ID)
	r = f.replica(rem2)
	require.Equal(t, databases.ReplicaBroken, r.State)
	require.Contains(t, r.Message, "Promote is available")
}

func TestReplicaMessageCarriesNoSecret(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	id := f.addReplica(pg.ID, f.server.ID)
	replPW := f.getVar(id, engines.ReplicationPasswordKey)
	pgPW := f.getVar(pg.ID, "POSTGRES_PASSWORD")
	_, err := f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	f.complete("repl_prepare:"+id.String()+":1", agentprotocol.OutcomeFailed, "role creation failed", nil)
	_, err = f.dbs.AdvanceReplica(bg, id)
	require.NoError(t, err)
	row := f.replica(id)
	require.Equal(t, databases.ReplicaFailed, row.State)
	require.Contains(t, row.Message, "role creation failed")

	// No persisted payload, audit row or view holds either password.
	var leaks int
	require.NoError(t, f.pool.QueryRow(bg, `
SELECT (SELECT count(*) FROM agent_commands WHERE payload::text LIKE '%' || $1 || '%' OR payload::text LIKE '%' || $2 || '%')
     + (SELECT count(*) FROM audit_events WHERE metadata::text LIKE '%' || $1 || '%' OR metadata::text LIKE '%' || $2 || '%')`, replPW, pgPW).Scan(&leaks))
	require.Zero(t, leaks)
	v, err := f.dbs.Replication(f.ctx, pg.ID)
	require.NoError(t, err)
	raw, _ := json.Marshal(v)
	require.NotContains(t, string(raw), replPW)
	require.NotContains(t, string(raw), pgPW)
}
