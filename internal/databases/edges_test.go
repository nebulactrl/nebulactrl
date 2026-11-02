package databases_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestRotationPlanWarnsAboutAnOfflineServerAndBlocksWhileOtherWorkRuns(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)

	plan := func() databases.RotationPlan {
		t.Helper()
		res, err := f.dbs.RotateCredentials(f.ctx, pg.ID, true)
		require.NoError(t, err)
		return res.Plan
	}
	p := plan()
	require.Equal(t, databases.ModeExec, p.Mode)
	require.Empty(t, p.Blockers)

	// The password is changed through the agent: an offline server delays it, and the plan says so.
	_, err := f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	require.Contains(t, strings.Join(plan().Warnings, "\n"), "agent1 is offline: the rotation waits until the agent is back")
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", f.server.ID)
	require.NoError(t, err)

	// A deployment in progress blocks it, and a real rotation is refused with the blocker.
	_, err = f.pool.Exec(bg, "INSERT INTO deployments (service_id, trigger, actor_type, strategy, status) VALUES ($1, 'manual', 'user', 'recreate', 'building')", pg.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"A deployment of this database is in progress."}, plan().Blockers)
	_, err = f.dbs.RotateCredentials(f.ctx, pg.ID, false)
	require.Equal(t, "rotation_blocked", apiCode(t, err))
	require.Zero(t, f.countRows("database_rotations"), "a refused rotation records nothing")
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'failed' WHERE service_id = $1 AND status = 'building'", pg.ID)
	require.NoError(t, err)

	// A version upgrade in progress blocks it too.
	_, err = f.dbs.Upgrade(f.ctx, pg.ID, "18", pg.Slug)
	require.NoError(t, err)
	require.Equal(t, []string{"A version upgrade is in progress."}, plan().Blockers)
}

func (f *fixture) countRows(table string) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n))
	return n
}

func TestRotationFailureWithoutAMessageGetsADefaultAndLeavesThePasswordAlone(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)
	old := f.getVar(pg.ID, "POSTGRES_PASSWORD")

	res, err := f.dbs.RotateCredentials(f.ctx, pg.ID, false)
	require.NoError(t, err)
	cmd, ok := f.lastCommand(agentprotocol.KindDBAdmin)
	require.True(t, ok)
	f.result(cmd, agentprotocol.OutcomeFailed, "", nil)

	v, err := f.dbs.Get(f.ctx, pg.ID)
	require.NoError(t, err)
	require.NotNil(t, v.Rotation)
	require.Equal(t, "failed", v.Rotation.Status)
	require.Equal(t, "the agent could not change the password", v.Rotation.Error)
	require.Equal(t, *res.RotationID, v.Rotation.ID)
	require.Equal(t, old, f.getVar(pg.ID, "POSTGRES_PASSWORD"))

	// The same result delivered again (the agent resends until acknowledged) changes nothing.
	f.result(cmd, agentprotocol.OutcomeFailed, "late duplicate", nil)
	v, err = f.dbs.Get(f.ctx, pg.ID)
	require.NoError(t, err)
	require.Equal(t, "the agent could not change the password", v.Rotation.Error)
	require.Equal(t, 1, f.auditCount("database.rotate_credentials.failed"))
}

func (f *fixture) auditCount(action string) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action = $1", action).Scan(&n))
	return n
}

func TestUpgradePlanBlocksOnRotationAndDeploymentAndWarnsAboutStagedChanges(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)

	// Staged changes ride along with the new version.
	f.setVar(pg.ID, "EXTRA", "1")
	_, err := f.pool.Exec(bg, "UPDATE services SET generation = applied_generation + 1 WHERE id = $1", pg.ID)
	require.NoError(t, err)
	p, err := f.dbs.PlanUpgrade(f.ctx, pg.ID, "18")
	require.NoError(t, err)
	require.Contains(t, strings.Join(p.Warnings, "\n"), "staged changes")
	require.Empty(t, p.Blockers)

	// A rotation that waits for its agent blocks the upgrade (and the other way round).
	_, err = f.dbs.RotateCredentials(f.ctx, pg.ID, false)
	require.NoError(t, err)
	p, err = f.dbs.PlanUpgrade(f.ctx, pg.ID, "18")
	require.NoError(t, err)
	require.Equal(t, []string{"A credential rotation is in progress."}, p.Blockers)
	_, err = f.dbs.Upgrade(f.ctx, pg.ID, "18", pg.Slug)
	require.Equal(t, "upgrade_blocked", apiCode(t, err))
	cmd, _ := f.lastCommand(agentprotocol.KindDBAdmin)
	f.result(cmd, agentprotocol.OutcomeSucceeded, "", agentprotocol.DBAdminResult{})

	_, err = f.pool.Exec(bg, "INSERT INTO deployments (service_id, trigger, actor_type, strategy, status) VALUES ($1, 'manual', 'user', 'recreate', 'queued')", pg.ID)
	require.NoError(t, err)
	p, err = f.dbs.PlanUpgrade(f.ctx, pg.ID, "18")
	require.NoError(t, err)
	require.Equal(t, []string{"A deployment of this database is in progress."}, p.Blockers)
}

func TestUpgradeFailureWithoutAMessageRestartsTheOldDatabaseWithTheDefaultReason(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)
	_, err := f.dbs.Upgrade(f.ctx, pg.ID, "18", pg.Slug)
	require.NoError(t, err)
	require.Equal(t, "stopped", f.reload(pg.ID).DesiredState)
	cmd, _ := f.lastCommand(agentprotocol.KindDBUpgrade)

	f.result(cmd, agentprotocol.OutcomeFailed, "", nil)
	require.Equal(t, "running", f.reload(pg.ID).DesiredState, "the old database runs again")
	v, err := f.dbs.Get(f.ctx, pg.ID)
	require.NoError(t, err)
	require.NotNil(t, v.Upgrade)
	require.Equal(t, "failed", v.Upgrade.Status)
	require.Equal(t, "the upgrade failed", v.Upgrade.Error)
	require.Equal(t, "17", services.DecodeConfig(f.reload(pg.ID).Config).Database.Version, "the version did not change")
	require.Equal(t, 1, f.auditCount("database.upgrade.failed"))

	// Only the first line of a multi-line failure reaches the audit log.
	_, err = f.dbs.Upgrade(f.ctx, pg.ID, "18", pg.Slug)
	require.NoError(t, err)
	cmd, _ = f.lastCommand(agentprotocol.KindDBUpgrade)
	f.result(cmd, agentprotocol.OutcomeFailed, "pg_upgrade failed\nsecret detail on line two", nil)
	var meta string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT metadata::text FROM audit_events WHERE action = 'database.upgrade.failed' ORDER BY created_at DESC LIMIT 1").Scan(&meta))
	require.Contains(t, meta, "pg_upgrade failed")
	require.NotContains(t, meta, "line two")
}

func TestUpgradeOfADatabaseWithoutADataVolumeIsRefused(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)
	_, err := f.pool.Exec(context.Background(), "DELETE FROM volumes WHERE service_id = $1", pg.ID)
	require.NoError(t, err)
	_, err = f.dbs.Upgrade(f.ctx, pg.ID, "18", pg.Slug)
	require.Equal(t, "data_volume_missing", apiCode(t, err))
	require.Zero(t, f.countRows("database_upgrades"))
	require.Equal(t, "running", f.reload(pg.ID).DesiredState, "the database was not stopped for nothing")
}

func TestInfoResultsAreStoredAndReportedPerOutcome(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")

	// A stopped database is not asked.
	f.deploy(pg.ID)
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET desired_state = 'stopped' WHERE id = $1", pg.ID)
	require.NoError(t, err)
	_, err = f.dbs.Refresh(f.ctx, pg.ID)
	require.Equal(t, "service_stopped", apiCode(t, err))
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET desired_state = 'running' WHERE id = $1", pg.ID)
	require.NoError(t, err)

	_, err = f.dbs.Refresh(f.ctx, pg.ID)
	require.NoError(t, err)
	cmd, ok := f.lastCommand(agentprotocol.KindDBAdmin)
	require.True(t, ok)
	f.result(cmd, agentprotocol.OutcomeFailed, "", nil)
	v, err := f.dbs.Get(f.ctx, pg.ID)
	require.NoError(t, err)
	require.NotNil(t, v.Info)
	require.Equal(t, "the agent could not read the database", v.Info.Error)

	// A later success replaces the error with the snapshot.
	f.result(cmd, agentprotocol.OutcomeSucceeded, "", agentprotocol.DBAdminResult{Info: &agentprotocol.DBInfo{Version: "17.2", SizeBytes: 4096, Connections: 3}})
	v, err = f.dbs.Get(f.ctx, pg.ID)
	require.NoError(t, err)
	require.Empty(t, v.Info.Error)
	require.Equal(t, "17.2", v.Info.Version)
	require.EqualValues(t, 4096, v.Info.SizeBytes)
	require.EqualValues(t, 3, v.Info.Connections)

	// A result for a database that was deleted meanwhile, or for an unknown command, is ignored.
	_, err = f.pool.Exec(context.Background(), "DELETE FROM services WHERE id = $1", pg.ID)
	require.NoError(t, err)
	f.result(cmd, agentprotocol.OutcomeSucceeded, "", agentprotocol.DBAdminResult{Info: &agentprotocol.DBInfo{Version: "x"}})
	require.NoError(t, f.dbs.HandleResult(context.Background(), uuid.New(), agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed}))
}

func TestAnUndeliveredCommandFailsItsOperationWithTheOfflineReason(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)
	_, err := f.dbs.RotateCredentials(f.ctx, pg.ID, false)
	require.NoError(t, err)
	cmd, _ := f.lastCommand(agentprotocol.KindDBAdmin)

	require.NoError(t, f.dbs.HandleUndelivered(context.Background(), cmd.ID, ""))
	v, err := f.dbs.Get(f.ctx, pg.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", v.Rotation.Status)
	require.Equal(t, "Not delivered: server offline. No changes were made.", v.Rotation.Error)
	require.True(t, databases.Handles(agentprotocol.KindDBUpgrade))
	require.True(t, databases.Handles(agentprotocol.KindDBAdmin))
	require.False(t, databases.Handles(agentprotocol.KindDeploy))
}

// The suggested public port is the engine's own when free, then the same number in the
// 10000s and 20000s, then the first free port above 30000.
func TestPublicPortSuggestionSkipsPortsTheServerAlreadyPublishes(t *testing.T) {
	f := newFixture(t)
	f.proxyServer("198.51.100.7")
	target := f.createDB("target", "postgres", "17")
	suggested := func() int {
		v, err := f.dbs.PublicPort(f.ctx, target.ID)
		require.NoError(t, err)
		return v.SuggestedPort
	}
	require.Equal(t, 5432, suggested())
	for i, want := range []int{15432, 25432, 30000} {
		other := f.createDB("other-"+string(rune('a'+i)), "postgres", "17")
		taken := suggested()
		require.Equal(t, []int{5432, 15432, 25432}[i], taken)
		_, err := f.dbs.SetPublicPort(f.ctx, other.ID, databases.PublicPortInput{ListenPort: taken, AllowCIDRs: []string{"203.0.113.0/24"}})
		require.NoError(t, err)
		require.Equal(t, want, suggested(), "after %d is published", taken)
	}

	// Publishing needs a database that is placed on a server.
	floating := f.createDB("floating", "postgres", "17")
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET server_id = NULL WHERE id = $1", floating.ID)
	require.NoError(t, err)
	_, err = f.dbs.SetPublicPort(f.ctx, floating.ID, databases.PublicPortInput{ListenPort: 15433, AllowCIDRs: []string{"203.0.113.0/24"}})
	require.Equal(t, "no_server", apiCode(t, err))
	v, err := f.dbs.PublicPort(f.ctx, floating.ID)
	require.NoError(t, err)
	require.Equal(t, 5432, v.SuggestedPort, "without a server there is nothing to collide with")
}

func TestStoredDatabaseConfigWithAnUnknownEngineIsRefusedEverywhere(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	raw, err := json.Marshal(map[string]any{"image": "x", "database": map[string]any{"engine": "cassandra", "version": "5"}})
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET config = $2 WHERE id = $1", pg.ID, raw)
	require.NoError(t, err)
	_, err = f.dbs.Get(f.ctx, pg.ID)
	require.Equal(t, "invalid_database", apiCode(t, err))
	_, err = f.dbs.RotateCredentials(f.ctx, pg.ID, true)
	require.Equal(t, "invalid_database", apiCode(t, err))
}

func TestSecretsForCommandRefusesPayloadsItCannotAttributeToADatabase(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	secrets := func(kind string, payload string) (map[string]string, error) {
		return f.dbs.SecretsForCommand(bg, uuid.New(), kind, json.RawMessage(payload))
	}

	// Commands of other kinds carry no database secrets.
	got, err := secrets(agentprotocol.KindDeploy, `{}`)
	require.NoError(t, err)
	require.Empty(t, got)

	for name, payload := range map[string]string{
		"not json":               `nope`,
		"no service id":          `{"action":"info"}`,
		"a service that is gone": `{"action":"info","serviceId":"` + uuid.NewString() + `"}`,
	} {
		_, err := secrets(agentprotocol.KindDBAdmin, payload)
		require.Error(t, err, name)
	}
	_, err = secrets(agentprotocol.KindDBReplicaClone, `nope`)
	require.Error(t, err)
	_, err = secrets(agentprotocol.KindDBReplicaClone, `{"serviceId":"x"}`)
	require.Error(t, err)

	// A rotation payload without its rotation id cannot get the new password.
	_, err = secrets(agentprotocol.KindDBAdmin, `{"action":"rotate_password","serviceId":"`+pg.ID.String()+`"}`)
	require.Error(t, err)

	// A prepare whose role belongs to no replica of the primary gets nothing, not someone else's password.
	f.addReplica(pg.ID, f.server.ID)
	_, err = secrets(agentprotocol.KindDBAdmin, `{"action":"`+agentprotocol.DBActionReplPrepare+`","serviceId":"`+pg.ID.String()+`","repl":{"role":"nebula_r_unknown","slot":"s"}}`)
	require.ErrorContains(t, err, "no replica uses the role")

	// The password of the database itself is attached to a plain admin command.
	got, err = secrets(agentprotocol.KindDBAdmin, `{"action":"info","serviceId":"`+pg.ID.String()+`"}`)
	require.NoError(t, err)
	require.Equal(t, f.getVar(pg.ID, "POSTGRES_PASSWORD"), got[agentprotocol.SecretDBAdminPassword])
}

func TestReplicationPanelOffersPromoteAndRebuildPerState(t *testing.T) {
	f := newReplicaFixture(t)
	bg := context.Background()
	pg := f.primary("pg")
	rep := f.streamingReplica(pg.ID, f.server.ID)
	view := func() databases.ReplicationView {
		t.Helper()
		v, err := f.dbs.Replication(f.ctx, rep)
		require.NoError(t, err)
		return v
	}
	for _, tc := range []struct {
		state                string
		canPromote, rebuilds bool
	}{
		{databases.ReplicaStreaming, true, true},
		{databases.ReplicaLagging, true, true},
		{databases.ReplicaBroken, true, true},
		{databases.ReplicaNeedsRebuild, false, true},
		{databases.ReplicaFailed, false, true},
		{databases.ReplicaCloning, false, false},
		{databases.ReplicaRemoving, false, false},
	} {
		_, err := f.pool.Exec(bg, "UPDATE database_replicas SET state = $2 WHERE service_id = $1", rep, tc.state)
		require.NoError(t, err)
		v := view()
		require.Equal(t, databases.RoleReplica, v.Role, tc.state)
		require.Equal(t, tc.canPromote, v.CanPromote, tc.state)
		require.Equal(t, tc.rebuilds, v.Rebuildable, tc.state)
	}

	// While a promotion runs nobody can start another, and the panel shows it.
	_, err := f.pool.Exec(bg, "UPDATE database_replicas SET state = 'streaming' WHERE service_id = $1", rep)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, `INSERT INTO database_promotions (workspace_id, environment_id, replica_service_id, old_primary_service_id, mode, message)
		VALUES ($1, $2, $3, $4, 'switchover', 'Freezing the old primary.')`, f.ws.ID, f.env.ID, rep, pg.ID)
	require.NoError(t, err)
	v := view()
	require.False(t, v.CanPromote)
	require.NotNil(t, v.Promotion)
	require.Equal(t, "running", v.Promotion.Status)
	require.Equal(t, "Freezing the old primary.", v.Promotion.Message)

	// A finished promotion is shown for a week, then it is history.
	_, err = f.pool.Exec(bg, "UPDATE database_promotions SET status = 'succeeded', finished_at = now()")
	require.NoError(t, err)
	require.NotNil(t, view().Promotion)
	_, err = f.pool.Exec(bg, "UPDATE database_promotions SET finished_at = now() - interval '8 days'")
	require.NoError(t, err)
	require.Nil(t, view().Promotion)

	// A fenced old primary is only offered a rebuild.
	_, err = f.pool.Exec(bg, "INSERT INTO database_fences (service_id, reason) VALUES ($1, 'switchover')", pg.ID)
	require.NoError(t, err)
	pv, err := f.dbs.Replication(f.ctx, pg.ID)
	require.NoError(t, err)
	require.Equal(t, databases.RoleFenced, pv.Role)
	require.True(t, pv.Rebuildable)
	require.False(t, pv.CanPromote)
}

func TestUpgradeToAFreeVolumeNameWhenTheDefaultIsTaken(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)
	_, err := f.q.InsertVolume(context.Background(), db.InsertVolumeParams{ServiceID: pg.ID, Name: "data-pg18", MountPath: "/other"})
	require.NoError(t, err)
	_, err = f.dbs.Upgrade(f.ctx, pg.ID, "18", pg.Slug)
	require.NoError(t, err)
	cmd, ok := f.lastCommand(agentprotocol.KindDBUpgrade)
	require.True(t, ok)
	var p agentprotocol.DBUpgradePayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, "data", p.OldVolume)
	require.Equal(t, "data-pg18-2", p.NewVolume, "the converted data never lands on a volume that already exists")
}
