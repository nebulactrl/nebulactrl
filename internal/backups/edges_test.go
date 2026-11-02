package backups

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// engineDB inserts a deployed managed database of the engine on the fixture server.
func (f *fixture) engineDB(slug, engine, version string) db.Service {
	f.t.Helper()
	cfg := `{"image":"` + engine + `:` + version + `","port":5432,"database":{"engine":"` + engine + `","version":"` + version + `","databaseName":"app","username":"app"}}`
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: "database", SourceType: "image", Config: json.RawMessage(cfg), ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(context.Background(), db.InsertDeploymentParams{ServiceID: sv.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "recreate"})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(context.Background(), db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID}))
	sv, err = f.getService(sv.ID)
	require.NoError(f.t, err)
	return sv
}

func TestObjectExtFollowsTheEnginesDumpFormat(t *testing.T) {
	for engine, ext := range map[string]string{
		"postgres": ".dump", "mysql": ".sql.gz", "mariadb": ".sql.gz", "mongodb": ".archive.gz", "valkey": ".rdb", "redis": ".rdb",
	} {
		require.Equal(t, ext, objectExt(engine), engine)
	}
}

func TestResolveTargetPicksTheCredentialsTheEngineNeeds(t *testing.T) {
	f := newFixture(t)
	resolve := func(sv db.Service) (target, error) {
		sc, err := services.LoadScopeInternal(context.Background(), f.q, sv.ID)
		require.NoError(t, err)
		return f.svc.resolveTarget(f.ctx, f.q, sc)
	}

	pg, err := resolve(f.database("pg"))
	require.NoError(t, err)
	require.Equal(t, "postgres", pg.User)
	require.Equal(t, "app", pg.Database)
	require.Equal(t, canaryDBPass, pg.Password)

	// MySQL dumps run as root: the app user cannot dump routines, events and triggers.
	my, err := resolve(f.engineDB("my", "mysql", "8"))
	require.NoError(t, err)
	require.Equal(t, "root", my.User)
	// Valkey has neither a user nor a database; MongoDB dumps every database.
	kv, err := resolve(f.engineDB("kv", "valkey", "9"))
	require.NoError(t, err)
	require.Empty(t, kv.User)
	require.Empty(t, kv.Database)
	mongo, err := resolve(f.engineDB("mongo", "mongodb", "8"))
	require.NoError(t, err)
	require.Empty(t, mongo.Database)

	// A read replica is not backed up: its primary is.
	rep := f.database("rep")
	_, err = f.pool.Exec(context.Background(), `UPDATE services SET config = jsonb_set(config, '{database,replica}', '{"primaryServiceId":"`+uuid.NewString()+`"}') WHERE id = $1`, rep.ID)
	require.NoError(t, err)
	rep, err = f.getService(rep.ID)
	require.NoError(t, err)
	_, err = resolve(rep)
	status, code := apiCode(t, err)
	require.Equal(t, 422, status)
	require.Equal(t, "backup_replica_unsupported", code)

	// An unknown engine is refused with a message that names it.
	odd := f.engineDB("odd", "cassandra", "5")
	_, err = resolve(odd)
	_, code = apiCode(t, err)
	require.Equal(t, "backup_unsupported", code)
	require.Contains(t, err.Error(), "cassandra")
}

func TestDestinationValidationRejectsSettingsThatDoNotBelongToTheKind(t *testing.T) {
	f := newFixture(t)
	create := func(in DestinationInput) error {
		_, err := f.svc.CreateDestination(f.ctx, in)
		return err
	}
	s3 := DestinationInput{Name: "s3", Kind: "s3", Bucket: "bucket", Region: "eu-1", AccessKey: "AKIAX", SecretKey: "secret"}

	bad := s3
	bad.Region = "EU_West"
	require.ErrorContains(t, create(bad), "region may only contain")
	bad = s3
	bad.LocalDir = "x"
	require.ErrorContains(t, create(bad), "localDir only applies to local destinations")
	bad = s3
	bad.Name = " "
	require.ErrorContains(t, create(bad), "name is required")
	bad = s3
	bad.Name = strings.Repeat("n", 81)
	require.ErrorContains(t, create(bad), "name is required")
	local := DestinationInput{Name: "disk", Kind: "local", LocalDir: "nightly"}
	bad = s3
	bad.AccessKey = "a"
	require.ErrorContains(t, create(bad), "accessKey and secretKey are required for s3 destinations")
	bad = local
	bad.Bucket = "b"
	require.ErrorContains(t, create(bad), "only apply to s3 destinations")
	bad = local
	bad.AccessKey, bad.SecretKey = "AKIAX", "secret"
	require.ErrorContains(t, create(bad), "credentials only apply to s3 destinations")
	list, err := f.svc.ListDestinations(f.ctx)
	require.NoError(t, err)
	require.Empty(t, list, "refusals created nothing")

	// An explicit compat mode wins over the default that follows the endpoint.
	in := s3
	in.Endpoint = "https://s3.example.com"
	in.CompatMode = new(false)
	v, err := f.svc.CreateDestination(f.ctx, in)
	require.NoError(t, err)
	require.False(t, v.CompatMode)
	in = s3
	in.Name = "s3-compat"
	in.Endpoint = "https://s3.example.com"
	v2, err := f.svc.CreateDestination(f.ctx, in)
	require.NoError(t, err)
	require.True(t, v2.CompatMode, "compat mode is on by default behind an endpoint")
}

func TestUpdatingADestinationProtectsWhereBackupsLive(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := f.s3Destination()
	local, err := f.svc.CreateDestination(f.ctx, DestinationInput{Name: "disk", Kind: "local", LocalDir: "nightly"})
	require.NoError(t, err)

	// A destination without stored backups can move; one with them cannot.
	v, err := f.svc.UpdateDestination(f.ctx, dest.ID, DestinationPatch{Bucket: new("other-bucket"), PathStyle: new(false), CompatMode: new(false)})
	require.NoError(t, err)
	require.Equal(t, "other-bucket", v.Bucket)
	require.False(t, v.PathStyle)
	require.False(t, v.CompatMode)
	f.succeededBackup(sv, mustDest(t, f, dest.ID), nil, time.Hour, "nebula/prod/x.dump")
	_, err = f.svc.UpdateDestination(f.ctx, dest.ID, DestinationPatch{Bucket: new("third-bucket")})
	status, code := apiCode(t, err)
	require.Equal(t, 409, status)
	require.Equal(t, "destination_in_use", code)
	require.Contains(t, err.Error(), "1 stored backup(s)")
	v, err = f.svc.UpdateDestination(f.ctx, dest.ID, DestinationPatch{Name: new("renamed"), Region: new("eu-2")})
	require.NoError(t, err, "what does not move the objects stays editable")
	require.Equal(t, "renamed", v.Name)

	// Names are unique and required; credentials belong to s3 destinations only.
	_, err = f.svc.UpdateDestination(f.ctx, dest.ID, DestinationPatch{Name: new("disk")})
	_, code = apiCode(t, err)
	require.Equal(t, "destination_conflict", code)
	_, err = f.svc.UpdateDestination(f.ctx, dest.ID, DestinationPatch{Name: new("  ")})
	require.ErrorContains(t, err, "name is required")
	_, err = f.svc.UpdateDestination(f.ctx, local.ID, DestinationPatch{AccessKey: new("a")})
	require.ErrorContains(t, err, "credentials only apply to s3 destinations")
	_, err = f.svc.UpdateDestination(f.ctx, local.ID, DestinationPatch{SecretKey: new("b")})
	require.ErrorContains(t, err, "credentials only apply to s3 destinations")
	_, err = f.svc.UpdateDestination(f.ctx, uuid.New(), DestinationPatch{Name: new("x")})
	status, _ = apiCode(t, err)
	require.Equal(t, 404, status)
}

func TestDeletingADestinationIsRefusedWhileAnythingStillLivesInIt(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := f.s3Destination()
	d := mustDest(t, f, dest.ID)

	// A stored backup: the objects would be orphaned.
	f.succeededBackup(sv, d, nil, time.Hour, "k/x")
	err := f.svc.DeleteDestination(f.ctx, dest.ID)
	_, code := apiCode(t, err)
	require.Equal(t, "destination_in_use", code)
	require.Contains(t, err.Error(), "delete them first")

	// A backup whose object is gone is only history: the destination is still referenced.
	_, err = f.pool.Exec(context.Background(), "UPDATE backups SET object_deleted_at = now()")
	require.NoError(t, err)
	err = f.svc.DeleteDestination(f.ctx, dest.ID)
	_, code = apiCode(t, err)
	require.Equal(t, "destination_in_use", code)
	require.Contains(t, err.Error(), "history still references")

	// A volume backup repository holds encrypted data that cannot be removed here.
	f2 := newFixture(t)
	dest2 := f2.s3Destination()
	app := f2.app("web", true)
	f2.volConfig(app, dest2, nil)
	b, err := f2.svc.CreateVolumeBackup(f2.ctx, app.ID)
	require.NoError(t, err)
	f2.finishBackup(b, snap('a'))
	_, err = f2.pool.Exec(context.Background(), "UPDATE volume_backup_configs SET enabled = false")
	require.NoError(t, err)
	_, err = f2.pool.Exec(context.Background(), "DELETE FROM volume_backups")
	require.NoError(t, err)
	_, err = f2.pool.Exec(context.Background(), "DELETE FROM volume_backup_configs")
	require.NoError(t, err)
	err = f2.svc.DeleteDestination(f2.ctx, dest2.ID)
	_, code = apiCode(t, err)
	require.Equal(t, "destination_in_use", code)
	require.Contains(t, err.Error(), "volume backup repositor")
}

func TestCreateBackupRefusesDatabasesThatCannotBeBackedUpRightNow(t *testing.T) {
	f := newFixture(t)
	dest := f.s3Destination()
	create := func(id uuid.UUID) (int, string) {
		_, err := f.svc.CreateBackup(f.ctx, id, dest.ID)
		return apiCode(t, err)
	}

	// Never deployed.
	sv := f.database("pg")
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET current_deployment_id = NULL WHERE id = $1", sv.ID)
	require.NoError(t, err)
	status, code := create(sv.ID)
	require.Equal(t, 409, status)
	require.Equal(t, "database_not_running", code)

	// Stopped, or observed not running a moment ago.
	sv2 := f.database("pg2")
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET desired_state = 'stopped' WHERE id = $1", sv2.ID)
	require.NoError(t, err)
	_, code = create(sv2.ID)
	require.Equal(t, "database_not_running", code)
	sv3 := f.database("pg3")
	_, err = f.pool.Exec(context.Background(), `UPDATE services SET observed = '{"state":"crashed"}', observed_at = now() WHERE id = $1`, sv3.ID)
	require.NoError(t, err)
	_, err = f.svc.CreateBackup(f.ctx, sv3.ID, dest.ID)
	require.ErrorContains(t, err, "the database is crashed")
	// An observation that is old says nothing about now.
	_, err = f.pool.Exec(context.Background(), `UPDATE services SET observed_at = now() - interval '10 minutes' WHERE id = $1`, sv3.ID)
	require.NoError(t, err)
	_, err = f.svc.CreateBackup(f.ctx, sv3.ID, dest.ID)
	require.NoError(t, err)

	// Not placed on a server.
	sv4 := f.database("pg4")
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET server_id = NULL WHERE id = $1", sv4.ID)
	require.NoError(t, err)
	status, code = create(sv4.ID)
	require.Equal(t, 422, status)
	require.Equal(t, "no_server", code)
}

func TestDeletingBackupsThatAreRunningNeverStoredOrOnAnOfflineServer(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := mustDest(t, f, f.s3Destination().ID)

	// A backup that failed has no object: the row simply goes, nothing is sent to an agent.
	failed, err := f.q.InsertBackup(context.Background(), db.InsertBackupParams{
		WorkspaceID: f.ws.ID, ServiceID: sv.ID, DestinationID: dest.ID, ServerID: &f.server.ID, Trigger: TriggerManual, Status: StatusQueued, Engine: "postgres",
	})
	require.NoError(t, err)
	err = f.svc.DeleteBackup(f.ctx, failed.ID)
	_, code := apiCode(t, err)
	require.Equal(t, "backup_in_progress", code, "a queued backup is still running")
	_, err = f.q.FailBackup(context.Background(), db.FailBackupParams{ID: failed.ID, Status: StatusFailed, Error: "boom"})
	require.NoError(t, err)
	require.NoError(t, f.svc.DeleteBackup(f.ctx, failed.ID))
	_, err = f.svc.GetBackup(f.ctx, failed.ID)
	status, _ := apiCode(t, err)
	require.Equal(t, 404, status)
	require.Zero(t, f.countCmds(agentprotocol.KindBackupDelete))

	// A stored backup on an offline server cannot be removed now: nothing changes.
	stored := f.succeededBackup(sv, dest, nil, time.Hour, "k/stored")
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	err = f.svc.DeleteBackup(f.ctx, stored.ID)
	status, code = apiCode(t, err)
	require.Equal(t, 409, status)
	require.Equal(t, "server_offline", code)
	v, err := f.svc.GetBackup(f.ctx, stored.ID)
	require.NoError(t, err)
	require.False(t, v.ObjectDeleted)

	// Its server no longer exists.
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'online' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE backups SET server_id = NULL WHERE id = $1", stored.ID)
	require.NoError(t, err)
	err = f.svc.DeleteBackup(f.ctx, stored.ID)
	_, code = apiCode(t, err)
	require.Equal(t, "server_unavailable", code)

	// A zero limit asks for the default page.
	list, err := f.svc.ListBackups(f.ctx, sv.ID, 0)
	require.NoError(t, err)
	require.Len(t, list, 1)
}

func TestRestoreRefusalsNameTheProblemAndStartNothing(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := mustDest(t, f, f.s3Destination().ID)
	b := f.succeededBackup(sv, dest, nil, time.Hour, "k/prod.dump")
	restore := func(in RestoreInput) (int, string) {
		_, err := f.svc.Restore(f.ctx, b.ID, in)
		return apiCode(t, err)
	}
	none := func(t *testing.T) {
		t.Helper()
		require.Zero(t, f.count("SELECT count(*) FROM backup_restores"))
		require.Empty(t, f.dep.deploys)
	}

	// Mode and naming.
	status, code := restore(RestoreInput{Mode: "sideways"})
	require.Equal(t, 422, status)
	require.Equal(t, "validation_failed", code)
	status, code = restore(RestoreInput{Name: strings.Repeat("n", 81)})
	require.Equal(t, 422, status)
	require.Equal(t, "validation_failed", code)

	// A target server that does not exist, or is offline.
	other := uuid.New()
	status, code = restore(RestoreInput{ServerID: &other})
	require.Equal(t, 422, status)
	require.Equal(t, "server_not_found", code)
	srv2, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "agent2"})
	require.NoError(t, err)
	status, code = restore(RestoreInput{ServerID: &srv2.ID})
	require.Equal(t, 409, status, "a server that never came online is offline")
	require.Equal(t, "server_offline", code)
	none(t)

	// A database that is not placed anywhere needs a server named.
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET server_id = NULL WHERE id = $1", sv.ID)
	require.NoError(t, err)
	status, code = restore(RestoreInput{})
	require.Equal(t, 422, status)
	require.Equal(t, "no_server", code)
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET server_id = $2 WHERE id = $1", sv.ID, f.server.ID)
	require.NoError(t, err)

	// A backup in a directory on another server's disk can only go back to that server.
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'online' WHERE id = $1", srv2.ID)
	require.NoError(t, err)
	local, err := f.svc.CreateDestination(f.ctx, DestinationInput{Name: "disk", Kind: "local", LocalDir: "nightly"})
	require.NoError(t, err)
	lb := f.succeededBackup(sv, mustDest(t, f, local.ID), nil, time.Hour, "k/local.dump")
	_, err = f.svc.Restore(f.ctx, lb.ID, RestoreInput{ServerID: &srv2.ID})
	_, code = apiCode(t, err)
	require.Equal(t, "local_backup_server", code)
	none(t)

	// In place: offline server, stopped database and a local backup of another server are refused.
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	status, code = restore(RestoreInput{Mode: ModeInPlace, ConfirmSlug: sv.Slug})
	require.Equal(t, 409, status)
	require.Equal(t, "server_offline", code)
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'online' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET desired_state = 'stopped' WHERE id = $1", sv.ID)
	require.NoError(t, err)
	status, code = restore(RestoreInput{Mode: ModeInPlace, ConfirmSlug: sv.Slug})
	require.Equal(t, 409, status)
	require.Equal(t, "database_not_running", code)
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET desired_state = 'running' WHERE id = $1", sv.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE backups SET server_id = $2 WHERE id = $1", lb.ID, srv2.ID)
	require.NoError(t, err)
	_, err = f.svc.Restore(f.ctx, lb.ID, RestoreInput{Mode: ModeInPlace, ConfirmSlug: sv.Slug})
	_, code = apiCode(t, err)
	require.Equal(t, "local_backup_server", code)
	none(t)

	// A read replica is never restored in place.
	_, err = f.pool.Exec(context.Background(), `UPDATE services SET config = jsonb_set(config, '{database,replica}', '{"primaryServiceId":"`+uuid.NewString()+`"}') WHERE id = $1`, sv.ID)
	require.NoError(t, err)
	status, code = restore(RestoreInput{Mode: ModeInPlace, ConfirmSlug: sv.Slug})
	require.Equal(t, 409, status)
	require.Equal(t, "replica_read_only", code)
	none(t)
}

func TestRestoreInPlaceAndANewDatabaseOnAnotherServerAreRecorded(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := mustDest(t, f, f.s3Destination().ID)
	b := f.succeededBackup(sv, dest, nil, time.Hour, "k/prod.dump")
	srv2, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "agent2"})
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'online' WHERE id = $1", srv2.ID)
	require.NoError(t, err)

	rv, err := f.svc.Restore(f.ctx, b.ID, RestoreInput{Name: "pg-elsewhere", ServerID: &srv2.ID})
	require.NoError(t, err)
	sib, err := f.getService(*rv.TargetServiceID)
	require.NoError(t, err)
	require.Equal(t, srv2.ID, *sib.ServerID, "the new database is placed on the chosen server")

	// Restores are listed per service, newest first, and read back by id.
	list, err := f.svc.ListRestores(f.ctx, sv.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, rv.ID, list[0].ID)
	_, err = f.svc.GetRestore(f.ctx, uuid.New())
	status, _ := apiCode(t, err)
	require.Equal(t, 404, status)
	_, err = f.svc.ListRestores(f.ctx, uuid.New())
	status, _ = apiCode(t, err)
	require.Equal(t, 404, status)
}

func TestScheduleInputsAreCheckedOnCreateAndUpdate(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := f.s3Destination()

	_, err := f.svc.CreateSchedule(f.ctx, sv.ID, ScheduleInput{DestinationID: dest.ID, Cron: "@daily", RetentionCount: new(1001)})
	require.ErrorContains(t, err, "retentionCount must be 0-1000")
	_, err = f.svc.CreateSchedule(f.ctx, sv.ID, ScheduleInput{DestinationID: dest.ID, Cron: "@daily", RetentionDays: new(3651)})
	require.ErrorContains(t, err, "retentionDays 0-3650")

	off, err := f.svc.CreateSchedule(f.ctx, sv.ID, ScheduleInput{DestinationID: dest.ID, Cron: "@daily", Enabled: new(false)})
	require.NoError(t, err)
	require.False(t, off.Enabled, "a schedule can be created disabled")

	// Timezone: empty means UTC; retention days and a new destination apply.
	other, err := f.svc.CreateDestination(f.ctx, DestinationInput{Name: "disk", Kind: "local", LocalDir: "nightly"})
	require.NoError(t, err)
	v, err := f.svc.UpdateSchedule(f.ctx, off.ID, SchedulePatch{Timezone: new("Europe/Berlin"), RetentionDays: new(14), DestinationID: &other.ID})
	require.NoError(t, err)
	require.Equal(t, "Europe/Berlin", v.Timezone)
	require.Equal(t, 14, v.RetentionDays)
	require.Equal(t, other.ID, v.DestinationID)
	require.Equal(t, "disk", v.DestinationName)
	v, err = f.svc.UpdateSchedule(f.ctx, off.ID, SchedulePatch{Timezone: new("")})
	require.NoError(t, err)
	require.Equal(t, "UTC", v.Timezone)
	_, err = f.svc.UpdateSchedule(f.ctx, off.ID, SchedulePatch{RetentionCount: new(-1)})
	require.ErrorContains(t, err, "retentionCount must be 0-1000")
	_, err = f.svc.UpdateSchedule(f.ctx, off.ID, SchedulePatch{DestinationID: new(uuid.New())})
	status, _ := apiCode(t, err)
	require.Equal(t, 404, status)
	_, err = f.svc.UpdateSchedule(f.ctx, off.ID, SchedulePatch{Timezone: new("Mars/Olympus")})
	require.Error(t, err)
}
