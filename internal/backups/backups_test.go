package backups

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	canaryAccess = "AKIACANARYACCESS99"
	canarySecret = "sekret-canary-s3-12345"
	canaryDBPass = "sekret-canary-db-67890"
)

// realDispatcher persists commands like the hub does (without delivering), so
// result handlers can look them up.
type realDispatcher struct {
	q    *db.Queries
	cmds []agents.NewCommand
	ids  []uuid.UUID
}

func (d *realDispatcher) Enqueue(ctx context.Context, tx pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	id := uuid.New()
	key := c.IdempotencyKey
	if key == "" {
		key = id.String()
	}
	row, err := db.New(tx).InsertAgentCommand(ctx, db.InsertAgentCommandParams{
		ID: id, ServerID: c.ServerID, DeploymentID: c.DeploymentID, Kind: c.Kind, Payload: c.Payload, IdempotencyKey: key,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		return uuid.Nil, err
	}
	d.cmds = append(d.cmds, c)
	d.ids = append(d.ids, row.ID)
	return row.ID, nil
}

func (d *realDispatcher) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

func (d *realDispatcher) last() (agents.NewCommand, uuid.UUID) {
	return d.cmds[len(d.cmds)-1], d.ids[len(d.ids)-1]
}

// fakeDeployer returns a deployment row with a chosen status.
type fakeDeployer struct {
	q       *db.Queries
	pool    *pgxpool.Pool
	status  string
	deploys []uuid.UUID
}

func (f *fakeDeployer) Deploy(ctx context.Context, serviceID uuid.UUID, _, _ string) (deployments.DeploymentView, error) {
	dep, err := f.q.InsertDeployment(ctx, db.InsertDeploymentParams{ServiceID: serviceID, Trigger: "manual", ActorType: "user", Strategy: "recreate"})
	if err != nil {
		return deployments.DeploymentView{}, err
	}
	f.deploys = append(f.deploys, serviceID)
	if f.status != "" {
		if _, err = f.pool.Exec(ctx, "UPDATE deployments SET status = $2 WHERE id = $1", dep.ID, f.status); err != nil {
			return deployments.DeploymentView{}, err
		}
	}
	return deployments.DeploymentView{ID: dep.ID, ServiceID: serviceID}, nil
}

type fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	ctx    context.Context
	box    *secretbox.Box
	disp   *realDispatcher
	dep    *fakeDeployer
	svc    *Service
	ws     db.Workspace
	proj   db.Project
	env    db.Environment
	server db.Server
	events []Event
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", srv.ID)
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)

	disp := &realDispatcher{q: q}
	dep := &fakeDeployer{q: q, pool: pool, status: "succeeded"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jm := jobs.New(pool, logger)
	f := &fixture{t: t, pool: pool, q: q, box: box, disp: disp, dep: dep, ws: ws, proj: prj, env: env, server: srv}
	bus := events.NewBus()
	svcs := services.NewService(pool, bus, box, disp, logger)
	f.svc = NewService(Deps{
		Pool: pool, Bus: bus, Box: box, Jobs: jm, Dispatcher: disp, Deployer: dep, Logger: logger,
		Provisioner: NewDatabaseProvisioner(databases.NewService(pool, bus, box, disp, svcs, logger)),
	})
	RegisterJobs(jm, f.svc)
	f.svc.SetNotifier(NotifierFunc(func(_ context.Context, ev Event) { f.events = append(f.events, ev) }))
	f.ctx = auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "alice@example.com", Scopes: policy.AllScopes})
	return f
}

// database inserts a deployed managed Postgres on the fixture server.
func (f *fixture) database(slug string) db.Service {
	f.t.Helper()
	cfg := `{"image":"postgres:17","port":5432,"healthcheck":{"type":"tcp"},"database":{"engine":"postgres","version":"17","databaseName":"app","username":"postgres"}}`
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: "database", SourceType: "image", Config: json.RawMessage(cfg), ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	for k, v := range map[string]string{"POSTGRES_USER": "postgres", "POSTGRES_DB": "app"} {
		enc, err := f.box.SealString(secretbox.ServiceVariablesValue, secretbox.RowID(sv.ID.String(), k), v)
		require.NoError(f.t, err)
		_, err = f.q.UpsertServiceVariable(context.Background(), db.UpsertServiceVariableParams{ServiceID: sv.ID, Key: k, ValueEnc: enc})
		require.NoError(f.t, err)
	}
	enc, err := f.box.SealString(secretbox.ServiceVariablesValue, secretbox.RowID(sv.ID.String(), "POSTGRES_PASSWORD"), canaryDBPass)
	require.NoError(f.t, err)
	_, err = f.q.UpsertServiceVariable(context.Background(), db.UpsertServiceVariableParams{ServiceID: sv.ID, Key: "POSTGRES_PASSWORD", ValueEnc: enc, IsSecret: true})
	require.NoError(f.t, err)
	_, err = f.q.InsertVolume(context.Background(), db.InsertVolumeParams{ServiceID: sv.ID, Name: "data", MountPath: "/var/lib/postgresql/data"})
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

func (f *fixture) getService(id uuid.UUID) (db.Service, error) {
	row, err := f.q.GetServiceScopeInternal(context.Background(), id)
	return row.Service, err
}

func (f *fixture) s3Destination() DestinationView {
	f.t.Helper()
	v, err := f.svc.CreateDestination(f.ctx, DestinationInput{
		Name: "minio", Kind: "s3", Endpoint: "https://s3.example.com", Region: "eu-1", Bucket: "backups", Prefix: "nebula/prod",
		PathStyle: true, AccessKey: canaryAccess, SecretKey: canarySecret,
	})
	require.NoError(f.t, err)
	return v
}

func (f *fixture) result(id uuid.UUID, outcome, errMsg string, data any) agentprotocol.ResultFrame {
	raw, _ := json.Marshal(data)
	return agentprotocol.ResultFrame{CommandID: id.String(), Outcome: outcome, Data: raw, Error: errMsg}
}

func TestPrune(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	mk := func(daysAgo int, locked bool) Candidate {
		return Candidate{ID: uuid.New(), CreatedAt: now.AddDate(0, 0, -daysAgo), Locked: locked}
	}
	ids := func(cs ...Candidate) []uuid.UUID {
		out := make([]uuid.UUID, len(cs))
		for i, c := range cs {
			out[i] = c.ID
		}
		return out
	}

	c0, c1, c2, c3, c4 := mk(0, false), mk(1, false), mk(2, false), mk(3, false), mk(4, false)
	all := []Candidate{c2, c0, c4, c1, c3} // unsorted on purpose

	require.Empty(t, Prune(all, 0, 0, now), "no limits, no pruning")
	require.Empty(t, Prune(nil, 3, 0, now))
	require.Equal(t, ids(c4, c3), Prune(all, 3, 0, now), "keep newest 3, oldest first in the result")
	require.Equal(t, ids(c4, c3, c2), Prune(all, 0, 1, now), "older than 1 day: ages 2,3,4 (age 1 day is exactly the cutoff and stays)")
	require.Equal(t, ids(c4, c3, c2), Prune(all, 3, 1, now), "count and days both apply")

	// Locked backups never go and do not count.
	l := mk(10, true)
	require.Equal(t, ids(c4, c3), Prune(append([]Candidate{l}, all...), 3, 0, now))
	require.Equal(t, ids(c4, c3, c2), Prune(append([]Candidate{l}, all...), 0, 1, now))

	// The newest backup always survives, even if it is older than the age limit.
	old := mk(30, false)
	require.Empty(t, Prune([]Candidate{old}, 1, 7, now))
	old2 := mk(40, false)
	require.Equal(t, ids(old2), Prune([]Candidate{old, old2}, 0, 7, now))
}

func TestDestinationValidationAndSecretsNeverReturned(t *testing.T) {
	f := newFixture(t)
	v := f.s3Destination()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	require.NotContains(t, string(raw), canaryAccess)
	require.NotContains(t, string(raw), canarySecret)
	require.True(t, v.AccessKeySet)
	require.True(t, v.SecretKeySet)
	require.True(t, v.OffSite)
	require.True(t, v.CompatMode, "custom endpoints default to compat mode")

	// Ciphertext at rest, not the plaintext.
	var ak, sk []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT access_key_enc, secret_key_enc FROM backup_destinations WHERE id = $1", v.ID).Scan(&ak, &sk))
	require.NotContains(t, string(ak), canaryAccess)
	require.NotContains(t, string(sk), canarySecret)

	local, err := f.svc.CreateDestination(f.ctx, DestinationInput{Name: "disk", Kind: "local", LocalDir: "nightly"})
	require.NoError(t, err)
	require.False(t, local.OffSite, "local directories are flagged not off-site")

	for _, bad := range []DestinationInput{
		{Name: "x", Kind: "local", LocalDir: "../etc"},
		{Name: "x", Kind: "local", LocalDir: "/abs"},
		{Name: "x", Kind: "s3", Bucket: "ab", AccessKey: "aaaa", SecretKey: "bbbb"},
		{Name: "x", Kind: "s3", Bucket: "valid-bucket", Endpoint: "ftp://x", AccessKey: "aaaa", SecretKey: "bbbb"},
		{Name: "x", Kind: "s3", Bucket: "valid-bucket", Prefix: "a/../b", AccessKey: "aaaa", SecretKey: "bbbb"},
		{Name: "x", Kind: "s3", Bucket: "valid-bucket"},
		{Name: "x", Kind: "weird"},
	} {
		_, err := f.svc.CreateDestination(f.ctx, bad)
		ae, ok := apierr.As(err)
		require.True(t, ok, "%+v", bad)
		require.Equal(t, 422, ae.Status, "%+v", bad)
	}

	_, err = f.svc.CreateDestination(f.ctx, DestinationInput{Name: "MinIO", Kind: "local"})
	ae, _ := apierr.As(err)
	require.Equal(t, "destination_conflict", ae.Code, "names are unique case-insensitively")

	// Keys are kept when an update omits them; moving is refused once backups exist.
	up, err := f.svc.UpdateDestination(f.ctx, v.ID, DestinationPatch{Name: new("minio-eu")})
	require.NoError(t, err)
	require.True(t, up.AccessKeySet)
	secrets, err := f.svc.destinationSecrets(f.ctx, mustDest(t, f, v.ID))
	require.NoError(t, err)
	require.Equal(t, canaryAccess, secrets[agentprotocol.SecretBackupAccessKey])
}

func mustDest(t *testing.T, f *fixture, id uuid.UUID) db.BackupDestination {
	t.Helper()
	d, err := f.q.GetBackupDestinationInternal(context.Background(), id)
	require.NoError(t, err)
	return d
}

// S3 credentials and the database password reach the agent only as send-time
// secrets; they are in no persisted payload.
func TestSecretsNeverInPersistedPayloads(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := f.s3Destination()

	b, err := f.svc.CreateBackup(f.ctx, sv.ID, dest.ID)
	require.NoError(t, err)
	require.Equal(t, StatusQueued, b.Status)

	cmd, cmdID := f.disp.last()
	require.Equal(t, agentprotocol.KindDBBackup, cmd.Kind)
	for _, canary := range []string{canaryAccess, canarySecret, canaryDBPass} {
		require.NotContains(t, string(cmd.Payload), canary)
	}
	var stored []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT payload FROM agent_commands WHERE id = $1", cmdID).Scan(&stored))
	for _, canary := range []string{canaryAccess, canarySecret, canaryDBPass} {
		require.NotContains(t, string(stored), canary)
	}
	var p agentprotocol.DBBackupPayload
	require.NoError(t, json.Unmarshal(stored, &p))
	require.Equal(t, "postgres", p.Target.Engine)
	require.Equal(t, "app", p.Target.Database)
	require.Equal(t, "postgres", p.Target.User)
	require.Equal(t, "backups", p.Destination.Bucket)
	require.True(t, strings.HasPrefix(p.ObjectKey, "nebula/prod/shop/production/pg/"), p.ObjectKey)
	require.True(t, strings.HasSuffix(p.ObjectKey, ".dump"))
	require.NoError(t, agentprotocol.ValidObjectKey(p.ObjectKey))

	// The dispatcher attaches them at send time.
	secrets, err := f.svc.SecretsForCommand(f.ctx, cmd.Kind, cmd.Payload)
	require.NoError(t, err)
	require.Equal(t, canaryAccess, secrets[agentprotocol.SecretBackupAccessKey])
	require.Equal(t, canarySecret, secrets[agentprotocol.SecretBackupSecretKey])
	require.Equal(t, canaryDBPass, secrets[agentprotocol.SecretDBPassword])

	// Every table the feature writes is free of the canaries.
	for _, q := range []string{
		"SELECT string_agg(t::text, ' ') FROM backups t",
		"SELECT string_agg(t::text, ' ') FROM backup_schedules t",
		"SELECT string_agg(t::text, ' ') FROM backup_restores t",
		"SELECT string_agg(t::text, ' ') FROM audit_events t",
		"SELECT string_agg(t::text, ' ') FROM agent_commands t",
	} {
		var dump *string
		require.NoError(t, f.pool.QueryRow(context.Background(), q).Scan(&dump))
		if dump != nil {
			for _, canary := range []string{canaryAccess, canarySecret, canaryDBPass} {
				require.NotContains(t, *dump, canary, q)
			}
		}
	}

	// A failure message echoing a secret is masked before it is stored.
	res := f.result(cmdID, agentprotocol.OutcomeFailed, "upload failed: invalid key "+canarySecret+" for "+canaryDBPass, agentprotocol.DBBackupResult{})
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, res))
	got, err := f.svc.GetBackup(f.ctx, b.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, got.Status)
	require.NotContains(t, got.Error, canarySecret)
	require.NotContains(t, got.Error, canaryDBPass)
	require.Contains(t, got.Error, "••••••")
	require.Len(t, f.events, 1)
	require.Equal(t, NotifyBackupFailed, f.events[0].Type)
}

func TestBackupSuccessAndStatusFlow(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := f.s3Destination()
	b, err := f.svc.CreateBackup(f.ctx, sv.ID, dest.ID)
	require.NoError(t, err)
	_, cmdID := f.disp.last()

	// An acked command shows as running.
	_, err = f.pool.Exec(context.Background(), "UPDATE agent_commands SET status = 'acked' WHERE id = $1", cmdID)
	require.NoError(t, err)
	list, err := f.svc.ListBackups(f.ctx, sv.ID, 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, StatusRunning, list[0].Status)

	data := agentprotocol.DBBackupResult{Outcome: "succeeded", SizeBytes: 4096, SHA256: strings.Repeat("a", 64), Format: agentprotocol.FormatPGCustom, ObjectKey: b.ObjectKey}
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, "succeeded", "", data)))
	// Duplicate results are harmless.
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, "succeeded", "", data)))

	got, err := f.svc.GetBackup(f.ctx, b.ID)
	require.NoError(t, err)
	require.Equal(t, StatusSucceeded, got.Status)
	require.EqualValues(t, 4096, got.SizeBytes)
	require.True(t, got.Restorable)
	require.Equal(t, TriggerManual, got.Trigger)
	require.Len(t, f.events, 1)
	require.Equal(t, NotifyBackupSucceeded, f.events[0].Type)
	require.EqualValues(t, 4096, f.events[0].SizeBytes)

	// A failed result after success does not rewrite history.
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, "failed", "late", agentprotocol.DBBackupResult{})))
	got, _ = f.svc.GetBackup(f.ctx, b.ID)
	require.Equal(t, StatusSucceeded, got.Status)
}

func TestUndeliveredBackupExpires(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := f.s3Destination()
	b, err := f.svc.CreateBackup(f.ctx, sv.ID, dest.ID)
	require.NoError(t, err)
	_, cmdID := f.disp.last()
	require.NoError(t, f.svc.HandleUndelivered(f.ctx, cmdID, "Not delivered: server offline. No changes were made."))
	got, _ := f.svc.GetBackup(f.ctx, b.ID)
	require.Equal(t, StatusExpired, got.Status)
	require.Contains(t, got.Error, "Not delivered")
}

func TestCreateBackupRefusesOfflineServerAndNonDatabases(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := f.s3Destination()
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.svc.CreateBackup(f.ctx, sv.ID, dest.ID)
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, "server_offline", ae.Code)
	require.Empty(t, f.disp.cmds, "nothing may be queued for an offline server")

	app, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: "web", Slug: "web", Kind: "app", SourceType: "image",
		Config: json.RawMessage(`{"image":"x","port":80}`), ServerID: &f.server.ID,
	})
	require.NoError(t, err)
	_, err = f.svc.CreateBackup(f.ctx, app.ID, dest.ID)
	ae, _ = apierr.As(err)
	require.Equal(t, "not_a_managed_database", ae.Code)
}

// A read replica takes no backups (a standby's pg_dump can be cancelled by
// recovery conflicts): create and schedule-create refuse it with 422, queue
// nothing, and a promote-less replica never gets a schedule.
func TestBackupsRefuseReadReplica(t *testing.T) {
	f := newFixture(t)
	primary := f.database("pg")
	dest := f.s3Destination()

	cfg := `{"image":"postgres:17","port":5432,"healthcheck":{"type":"tcp"},"database":{"engine":"postgres","version":"17","databaseName":"app","username":"postgres",` +
		`"replica":{"primaryServiceId":"` + primary.ID.String() + `","host":"pg","port":5432,"slot":"nc_r_aaaaaaaa","user":"nebula_r_aaaaaaaa"}}}`
	replica, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: "pg-r1", Slug: "pg-r1", Kind: "database", SourceType: "image",
		Config: json.RawMessage(cfg), ServerID: &f.server.ID,
	})
	require.NoError(t, err)

	_, err = f.svc.CreateBackup(f.ctx, replica.ID, dest.ID)
	ae, ok := apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, 422, ae.Status)
	require.Equal(t, "backup_replica_unsupported", ae.Code)
	require.Equal(t, databases.CodeBackupReplica, ae.Code)

	_, err = f.svc.CreateSchedule(f.ctx, replica.ID, ScheduleInput{DestinationID: dest.ID, Cron: "@daily"})
	ae, ok = apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, 422, ae.Status)
	require.Equal(t, "backup_replica_unsupported", ae.Code)
	require.Empty(t, f.disp.cmds, "nothing may be queued for a replica")

	// The primary is unaffected.
	_, err = f.svc.CreateBackup(f.ctx, primary.ID, dest.ID)
	require.NoError(t, err)
	_, err = f.svc.CreateSchedule(f.ctx, primary.ID, ScheduleInput{DestinationID: dest.ID, Cron: "@daily"})
	require.NoError(t, err)
}

func TestScheduleValidation(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := f.s3Destination()
	for expr, code := range map[string]string{
		"nope":        "invalid_cron",
		"* * * * *":   "cron_too_frequent",
		"0 0 30 2 *":  "cron_never_fires",
		"*/4 * * * *": "cron_too_frequent",
	} {
		_, err := f.svc.CreateSchedule(f.ctx, sv.ID, ScheduleInput{DestinationID: dest.ID, Cron: expr})
		ae, ok := apierr.As(err)
		require.True(t, ok, expr)
		require.Equal(t, code, ae.Code, expr)
	}
	_, err := f.svc.CreateSchedule(f.ctx, sv.ID, ScheduleInput{DestinationID: dest.ID, Cron: "@daily", Timezone: "Mars/Base"})
	ae, _ := apierr.As(err)
	require.Equal(t, "invalid_timezone", ae.Code)

	v, err := f.svc.CreateSchedule(f.ctx, sv.ID, ScheduleInput{DestinationID: dest.ID, Cron: "30 3 * * *", Timezone: "UTC", RetentionCount: new(3)})
	require.NoError(t, err)
	require.NotNil(t, v.NextRunAt)
	require.Equal(t, 3, v.RetentionCount)
	require.Equal(t, 30, v.NextRunAt.UTC().Minute())
	require.Equal(t, 3, v.NextRunAt.UTC().Hour())
	require.True(t, v.NextRunAt.After(time.Now()))

	// Destinations with schedules cannot be deleted.
	err = f.svc.DeleteDestination(f.ctx, dest.ID)
	ae, _ = apierr.As(err)
	require.Equal(t, "destination_in_use", ae.Code)
	require.NoError(t, f.svc.DeleteSchedule(f.ctx, v.ID))
	require.NoError(t, f.svc.DeleteDestination(f.ctx, dest.ID))
}

func TestTickRunsDueSchedulesAndSkipsHonestly(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := f.s3Destination()
	sch, err := f.svc.CreateSchedule(f.ctx, sv.ID, ScheduleInput{DestinationID: dest.ID, Cron: "@hourly"})
	require.NoError(t, err)
	makeDue := func() {
		_, err := f.pool.Exec(context.Background(), "UPDATE backup_schedules SET next_run_at = now() - interval '1 minute' WHERE id = $1", sch.ID)
		require.NoError(t, err)
	}

	// Not due: nothing happens.
	require.NoError(t, f.svc.Tick(context.Background()))
	require.Empty(t, f.disp.cmds)

	// Due and healthy: a queued backup and a command, next run moved forward.
	makeDue()
	require.NoError(t, f.svc.Tick(context.Background()))
	require.Len(t, f.disp.cmds, 1)
	require.Equal(t, agentprotocol.KindDBBackup, f.disp.cmds[0].Kind)
	list, err := f.svc.ListBackups(f.ctx, sv.ID, 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, TriggerSchedule, list[0].Trigger)
	require.Equal(t, StatusQueued, list[0].Status)
	scheds, _ := f.svc.ListSchedules(f.ctx, sv.ID)
	require.True(t, scheds[0].NextRunAt.After(time.Now()))
	require.NotNil(t, scheds[0].LastRunAt)
	require.NoError(t, f.svc.Tick(context.Background()))
	require.Len(t, f.disp.cmds, 1, "a schedule runs once per due instant")

	// Server offline: a skipped run with the reason, no command, no fake success.
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	makeDue()
	require.NoError(t, f.svc.Tick(context.Background()))
	require.Len(t, f.disp.cmds, 1)
	list, _ = f.svc.ListBackups(f.ctx, sv.ID, 10)
	require.Equal(t, StatusSkipped, list[0].Status)
	require.Contains(t, list[0].Error, "offline")
	require.Equal(t, NotifyBackupSkipped, f.events[len(f.events)-1].Type)

	// Stopped database: skipped too.
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'online' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET desired_state = 'stopped' WHERE id = $1", sv.ID)
	require.NoError(t, err)
	makeDue()
	require.NoError(t, f.svc.Tick(context.Background()))
	list, _ = f.svc.ListBackups(f.ctx, sv.ID, 10)
	require.Equal(t, StatusSkipped, list[0].Status)
	require.Contains(t, list[0].Error, "stopped")
	require.Len(t, f.disp.cmds, 1)

	// Disabled schedules never run.
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET desired_state = 'running' WHERE id = $1", sv.ID)
	require.NoError(t, err)
	_, err = f.svc.UpdateSchedule(f.ctx, sch.ID, SchedulePatch{Enabled: new(false)})
	require.NoError(t, err)
	makeDue()
	require.NoError(t, f.svc.Tick(context.Background()))
	require.Len(t, f.disp.cmds, 1)
}

// succeededBackup records a finished scheduled backup of age.
func (f *fixture) succeededBackup(sv db.Service, dest db.BackupDestination, sched *uuid.UUID, age time.Duration, key string) db.Backup {
	f.t.Helper()
	b, err := f.q.InsertBackup(context.Background(), db.InsertBackupParams{
		WorkspaceID: f.ws.ID, ServiceID: sv.ID, ScheduleID: sched, DestinationID: dest.ID, ServerID: &f.server.ID, Trigger: TriggerSchedule,
		Status: StatusQueued, Engine: "postgres", EngineVersion: "17", DatabaseName: "app", ObjectKey: key,
	})
	require.NoError(f.t, err)
	_, err = f.q.CompleteBackup(context.Background(), db.CompleteBackupParams{ID: b.ID, SizeBytes: 10, Sha256: strings.Repeat("b", 64), Format: agentprotocol.FormatPGCustom, ObjectKey: key})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE backups SET created_at = $2 WHERE id = $1", b.ID, time.Now().Add(-age))
	require.NoError(f.t, err)
	return b
}

func TestRetentionPrunesThroughAgentDeleteCommands(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	destV := f.s3Destination()
	dest := mustDest(t, f, destV.ID)
	sch, err := f.svc.CreateSchedule(f.ctx, sv.ID, ScheduleInput{DestinationID: dest.ID, Cron: "@daily", RetentionCount: new(2)})
	require.NoError(t, err)

	var keep []db.Backup
	for i, age := range []time.Duration{72 * time.Hour, 48 * time.Hour, 24 * time.Hour, time.Hour} {
		keep = append(keep, f.succeededBackup(sv, dest, &sch.ID, age, "k/"+string(rune('a'+i))))
	}
	// A manual backup (no schedule) is never pruned by the schedule's retention.
	manual := f.succeededBackup(sv, dest, nil, 100*time.Hour, "k/manual")

	f.svc.applyRetention(context.Background(), sch.ID)
	require.Len(t, f.disp.cmds, 1)
	cmd, cmdID := f.disp.last()
	require.Equal(t, agentprotocol.KindBackupDelete, cmd.Kind)
	var p agentprotocol.BackupDeletePayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.ElementsMatch(t, []string{"k/a", "k/b"}, p.ObjectKeys, "the two oldest scheduled backups go")
	for _, canary := range []string{canaryAccess, canarySecret} {
		require.NotContains(t, string(cmd.Payload), canary)
	}
	secrets, err := f.svc.SecretsForCommand(f.ctx, cmd.Kind, cmd.Payload)
	require.NoError(t, err)
	require.Equal(t, canarySecret, secrets[agentprotocol.SecretBackupSecretKey])

	// While deletion is pending those backups are no longer restorable.
	rows, err := f.q.ListScheduleRetentionCandidates(context.Background(), &sch.ID)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	// A failed deletion keeps the rows and retries later.
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, "failed", "access denied", agentprotocol.BackupSimpleResult{})))
	rows, _ = f.q.ListScheduleRetentionCandidates(context.Background(), &sch.ID)
	require.Len(t, rows, 4, "objects stay restorable after a failed deletion")

	f.svc.applyRetention(context.Background(), sch.ID)
	_, cmdID = f.disp.last()
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, "succeeded", "", agentprotocol.BackupSimpleResult{Outcome: "succeeded"})))
	rows, _ = f.q.ListScheduleRetentionCandidates(context.Background(), &sch.ID)
	require.Len(t, rows, 2)
	for _, r := range rows {
		require.Contains(t, []uuid.UUID{keep[2].ID, keep[3].ID}, r.ID)
	}
	_, err = f.q.GetBackupInternal(context.Background(), manual.ID)
	require.NoError(t, err, "manual backups are not subject to schedule retention")
}

func TestLockedBackupsSurviveAndCannotBeDeleted(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := mustDest(t, f, f.s3Destination().ID)
	sch, err := f.svc.CreateSchedule(f.ctx, sv.ID, ScheduleInput{DestinationID: dest.ID, Cron: "@daily", RetentionCount: new(1)})
	require.NoError(t, err)
	old := f.succeededBackup(sv, dest, &sch.ID, 90*time.Hour, "k/old")
	f.succeededBackup(sv, dest, &sch.ID, 2*time.Hour, "k/mid")
	f.succeededBackup(sv, dest, &sch.ID, time.Hour, "k/new")

	_, err = f.svc.SetLocked(f.ctx, old.ID, true)
	require.NoError(t, err)
	f.svc.applyRetention(context.Background(), sch.ID)
	cmd, _ := f.disp.last()
	var p agentprotocol.BackupDeletePayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, []string{"k/mid"}, p.ObjectKeys, "locked backups are kept")

	err = f.svc.DeleteBackup(f.ctx, old.ID)
	ae, _ := apierr.As(err)
	require.Equal(t, "backup_locked", ae.Code)
}

// Restoring to a new database creates a sibling service with the source's
// engine and settings, fresh credentials and its own volume, deploys it, and
// only then sends db_restore into the sibling.
func TestRestoreToNewCreatesSiblingDatabase(t *testing.T) {
	f := newFixture(t)
	src := f.database("pg")
	destV := f.s3Destination()
	dest := mustDest(t, f, destV.ID)
	b := f.succeededBackup(src, dest, nil, time.Hour, "nebula/prod/shop/production/pg/20261026T100000Z-abc.dump")

	rv, err := f.svc.Restore(f.ctx, b.ID, RestoreInput{Name: "pg-copy"})
	require.NoError(t, err)
	require.Equal(t, ModeNew, rv.Mode)
	require.Equal(t, RestoreProvisioning, rv.Status)
	require.NotNil(t, rv.TargetServiceID)
	require.NotEqual(t, src.ID, *rv.TargetServiceID)

	sib, err := f.getService(*rv.TargetServiceID)
	require.NoError(t, err)
	require.Equal(t, "pg-copy", sib.Slug)
	require.Equal(t, "database", sib.Kind)
	require.Equal(t, src.EnvironmentID, sib.EnvironmentID)
	srcSpec, err := databaseSpec(src.Config)
	require.NoError(t, err)
	sibSpec, err := databaseSpec(sib.Config)
	require.NoError(t, err)
	require.Equal(t, *srcSpec, *sibSpec, "same engine, version and settings")
	require.Equal(t, f.server.ID, *sib.ServerID)
	require.Equal(t, []uuid.UUID{sib.ID}, f.dep.deploys, "the new database is deployed empty")

	vols, err := f.q.ListServiceVolumes(context.Background(), sib.ID)
	require.NoError(t, err)
	require.Len(t, vols, 1)
	require.Equal(t, "/var/lib/postgresql/data", vols[0].MountPath)

	srcPass := f.variable(src.ID, "POSTGRES_PASSWORD")
	sibPass := f.variable(sib.ID, "POSTGRES_PASSWORD")
	require.Equal(t, canaryDBPass, srcPass)
	require.NotEqual(t, srcPass, sibPass, "the sibling gets its own password")
	require.Len(t, sibPass, 32)
	require.Equal(t, "app", f.variable(sib.ID, "POSTGRES_DB"))

	// The source is untouched.
	again, _ := f.getService(src.ID)
	require.Equal(t, src.Generation, again.Generation)

	// Once the deployment succeeded the job sends db_restore INTO THE SIBLING.
	done, err := f.svc.AdvanceRestore(context.Background(), rv.ID)
	require.NoError(t, err)
	require.True(t, done)
	cmd, cmdID := f.disp.last()
	require.Equal(t, agentprotocol.KindDBRestore, cmd.Kind)
	var p agentprotocol.DBRestorePayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, sib.ID.String(), p.Target.ServiceID)
	require.Equal(t, b.ObjectKey, p.ObjectKey)
	require.Equal(t, agentprotocol.FormatPGCustom, p.Format)
	require.False(t, p.Clean, "restores into a new database never drop anything")
	require.Equal(t, "app", p.Target.Database)
	for _, canary := range []string{canaryAccess, canarySecret, canaryDBPass, sibPass} {
		require.NotContains(t, string(cmd.Payload), canary)
	}
	secrets, err := f.svc.SecretsForCommand(f.ctx, cmd.Kind, cmd.Payload)
	require.NoError(t, err)
	require.Equal(t, sibPass, secrets[agentprotocol.SecretDBPassword], "the restore authenticates with the sibling's password")
	require.Equal(t, canarySecret, secrets[agentprotocol.SecretBackupSecretKey])

	got, err := f.svc.GetRestore(f.ctx, rv.ID)
	require.NoError(t, err)
	require.Equal(t, RestoreRestoring, got.Status)

	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, "succeeded", "", agentprotocol.DBRestoreResult{Outcome: "succeeded"})))
	got, _ = f.svc.GetRestore(f.ctx, rv.ID)
	require.Equal(t, RestoreSucceeded, got.Status)
	require.Equal(t, NotifyRestoreSucceeded, f.events[len(f.events)-1].Type)
}

func (f *fixture) variable(serviceID uuid.UUID, key string) string {
	f.t.Helper()
	vars, err := f.q.ListServiceVariables(context.Background(), serviceID)
	require.NoError(f.t, err)
	for _, v := range vars {
		if v.Key == key {
			s, err := f.box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(serviceID.String(), v.Key), v.ValueEnc)
			require.NoError(f.t, err)
			return s
		}
	}
	f.t.Fatalf("variable %s not found", key)
	return ""
}

func TestRestoreToNewFailsWhenSiblingDeploymentFails(t *testing.T) {
	f := newFixture(t)
	src := f.database("pg")
	dest := mustDest(t, f, f.s3Destination().ID)
	b := f.succeededBackup(src, dest, nil, time.Hour, "k/x.dump")
	f.dep.status = "failed"
	rv, err := f.svc.Restore(f.ctx, b.ID, RestoreInput{})
	require.NoError(t, err)
	done, err := f.svc.AdvanceRestore(context.Background(), rv.ID)
	require.NoError(t, err)
	require.True(t, done)
	got, _ := f.svc.GetRestore(f.ctx, rv.ID)
	require.Equal(t, RestoreFailed, got.Status)
	require.Contains(t, got.Error, "could not be deployed")
	for _, c := range f.disp.cmds {
		require.NotEqual(t, agentprotocol.KindDBRestore, c.Kind, "nothing may be restored into a database that never ran")
	}

	// While the deployment is still running the job keeps waiting.
	f.dep.status = "building"
	rv, err = f.svc.Restore(f.ctx, b.ID, RestoreInput{})
	require.NoError(t, err)
	done, err = f.svc.AdvanceRestore(context.Background(), rv.ID)
	require.NoError(t, err)
	require.False(t, done)
}

func TestRestoreGuards(t *testing.T) {
	f := newFixture(t)
	src := f.database("pg")
	dest := mustDest(t, f, f.s3Destination().ID)
	b := f.succeededBackup(src, dest, nil, time.Hour, "k/x.dump")

	// Failed backups are not restorable.
	failed, err := f.q.InsertBackup(context.Background(), db.InsertBackupParams{
		WorkspaceID: f.ws.ID, ServiceID: src.ID, DestinationID: dest.ID, ServerID: &f.server.ID, Trigger: TriggerManual, Status: StatusFailed, Engine: "postgres",
	})
	require.NoError(t, err)
	_, err = f.svc.Restore(f.ctx, failed.ID, RestoreInput{})
	ae, _ := apierr.As(err)
	require.Equal(t, "backup_not_restorable", ae.Code)

	// In place needs the typed slug.
	_, err = f.svc.Restore(f.ctx, b.ID, RestoreInput{Mode: ModeInPlace, ConfirmSlug: "nope"})
	ae, _ = apierr.As(err)
	require.Equal(t, "confirmation_required", ae.Code)
	require.Empty(t, f.disp.cmds, "nothing may run without confirmation")

	_, err = f.svc.Restore(f.ctx, b.ID, RestoreInput{Mode: "sideways"})
	ae, _ = apierr.As(err)
	require.Equal(t, 422, ae.Status)

	// Local backups only restore onto their own server.
	local, err := f.svc.CreateDestination(f.ctx, DestinationInput{Name: "disk", Kind: "local"})
	require.NoError(t, err)
	lb := f.succeededBackup(src, mustDest(t, f, local.ID), nil, time.Hour, "k/l.dump")
	other, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "agent2"})
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'online' WHERE id = $1", other.ID)
	require.NoError(t, err)
	_, err = f.svc.Restore(f.ctx, lb.ID, RestoreInput{ServerID: &other.ID})
	ae, _ = apierr.As(err)
	require.Equal(t, "local_backup_server", ae.Code)

	// Unsupported engines are refused with a clear code.
	_, err = f.pool.Exec(context.Background(), "UPDATE backups SET engine = 'valkey' WHERE id = $1", b.ID)
	require.NoError(t, err)
	_, err = f.svc.Restore(f.ctx, b.ID, RestoreInput{})
	ae, _ = apierr.As(err)
	require.Equal(t, "restore_unsupported", ae.Code)
}

// An in-place restore takes a safety backup first and only proceeds when it
// succeeded.
func TestRestoreInPlaceTakesSafetyBackupFirst(t *testing.T) {
	f := newFixture(t)
	src := f.database("pg")
	dest := mustDest(t, f, f.s3Destination().ID)
	b := f.succeededBackup(src, dest, nil, time.Hour, "k/x.dump")

	rv, err := f.svc.Restore(f.ctx, b.ID, RestoreInput{Mode: ModeInPlace, ConfirmSlug: "pg"})
	require.NoError(t, err)
	require.Equal(t, RestorePreBackup, rv.Status)
	require.NotNil(t, rv.PreBackupID)
	require.Len(t, f.disp.cmds, 1)
	cmd, preCmdID := f.disp.last()
	require.Equal(t, agentprotocol.KindDBBackup, cmd.Kind, "the safety backup runs before anything is overwritten")

	data := agentprotocol.DBBackupResult{Outcome: "succeeded", SizeBytes: 77, SHA256: strings.Repeat("c", 64), Format: agentprotocol.FormatPGCustom}
	require.NoError(t, f.svc.HandleResult(f.ctx, preCmdID, f.result(preCmdID, "succeeded", "", data)))
	require.Len(t, f.disp.cmds, 2)
	cmd, restoreCmdID := f.disp.last()
	require.Equal(t, agentprotocol.KindDBRestore, cmd.Kind)
	var p agentprotocol.DBRestorePayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.True(t, p.Clean)
	require.Equal(t, src.ID.String(), p.Target.ServiceID)

	require.NoError(t, f.svc.HandleResult(f.ctx, restoreCmdID, f.result(restoreCmdID, "failed", "pg_restore exited with status 1", agentprotocol.DBRestoreResult{})))
	got, _ := f.svc.GetRestore(f.ctx, rv.ID)
	require.Equal(t, RestoreFailed, got.Status)
	require.Contains(t, got.Error, "pg_restore")
}

func TestRestoreInPlaceAbortsWhenSafetyBackupFails(t *testing.T) {
	f := newFixture(t)
	src := f.database("pg")
	dest := mustDest(t, f, f.s3Destination().ID)
	b := f.succeededBackup(src, dest, nil, time.Hour, "k/x.dump")
	rv, err := f.svc.Restore(f.ctx, b.ID, RestoreInput{Mode: ModeInPlace, ConfirmSlug: "pg"})
	require.NoError(t, err)
	_, preCmdID := f.disp.last()
	require.NoError(t, f.svc.HandleResult(f.ctx, preCmdID, f.result(preCmdID, "failed", "disk full", agentprotocol.DBBackupResult{})))
	got, _ := f.svc.GetRestore(f.ctx, rv.ID)
	require.Equal(t, RestoreFailed, got.Status)
	require.Contains(t, got.Error, "nothing was changed")
	require.Len(t, f.disp.cmds, 1, "no restore command after a failed safety backup")
}

func TestDestinationTestResultRecorded(t *testing.T) {
	f := newFixture(t)
	dest := f.s3Destination()
	payload, _ := json.Marshal(agentprotocol.BackupTestPayload{Destination: func() agentprotocol.BackupDestination {
		d, err := f.svc.destinationFor(f.ctx, mustDest(t, f, dest.ID))
		require.NoError(t, err)
		return d
	}()})
	cmdID := uuid.New()
	_, err := f.q.InsertAgentCommand(context.Background(), db.InsertAgentCommandParams{
		ID: cmdID, ServerID: f.server.ID, Kind: agentprotocol.KindBackupTest, Payload: payload, IdempotencyKey: "t1", ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, "failed", "write: AccessDenied for "+canarySecret, agentprotocol.BackupSimpleResult{})))
	v, err := f.svc.GetDestination(f.ctx, dest.ID)
	require.NoError(t, err)
	require.NotNil(t, v.LastTestOK)
	require.False(t, *v.LastTestOK)
	require.Contains(t, v.LastTestMessage, "AccessDenied")
	require.NotContains(t, v.LastTestMessage, canarySecret)
}

func TestTokensNeedScopes(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := f.s3Destination()
	reader := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, WorkspaceID: f.ws.ID, Label: "ro", Scopes: []string{policy.ScopeProjectsRead}})
	_, err := f.svc.ListBackups(reader, sv.ID, 10)
	require.NoError(t, err)
	_, err = f.svc.CreateBackup(reader, sv.ID, dest.ID)
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, 403, ae.Status)
	_, err = f.svc.CreateDestination(reader, DestinationInput{Name: "x", Kind: "local"})
	ae, _ = apierr.As(err)
	require.Equal(t, 403, ae.Status)
	require.Empty(t, f.disp.cmds)
}
