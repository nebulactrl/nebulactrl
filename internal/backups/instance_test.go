package backups

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/backup"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- pure logic -------------------------------------------------------------

func TestParsePGURL(t *testing.T) {
	c, err := ParsePGURL("postgres://nebula:p%40ss%2Fw@db.internal:5433/control?sslmode=require&connect_timeout=5&application_name=x")
	require.NoError(t, err)
	require.Equal(t, "db.internal", c.Host)
	require.Equal(t, "5433", c.Port)
	require.Equal(t, "nebula", c.User)
	require.Equal(t, "p@ss/w", c.Password)
	require.Equal(t, "control", c.Database)
	require.ElementsMatch(t, []string{
		"PGHOST=db.internal", "PGPORT=5433", "PGUSER=nebula", "PGPASSWORD=p@ss/w", "PGDATABASE=control", "PGSSLMODE=require", "PGCONNECT_TIMEOUT=5",
	}, c.Env())

	c, err = ParsePGURL("postgresql://u@[::1]:5432/d")
	require.NoError(t, err)
	require.Equal(t, "::1", c.Host)
	require.Empty(t, c.Password)
	require.NotContains(t, strings.Join(c.Env(), " "), "PGPASSWORD")

	c, err = ParsePGURL("postgres:///d?host=/var/run/postgresql")
	require.NoError(t, err)
	require.Equal(t, "/var/run/postgresql", c.Host)

	require.Equal(t, "other", c.WithDatabase("other").Database)
	require.Equal(t, "d", c.Database, "WithDatabase must not change the receiver")

	for _, bad := range []string{"", "mysql://u@h/d", "postgres://u@h", "host=localhost dbname=x"} {
		_, err := ParsePGURL(bad)
		require.Error(t, err, bad)
	}
}

// The database password reaches the tools through the environment only.
func TestPGToolsPassPasswordThroughEnvironmentOnly(t *testing.T) {
	var got []Cmd
	tools := PGTools{Run: func(_ context.Context, c Cmd) error { got = append(got, c); return nil }}
	conn, err := ParsePGURL("postgres://nebula:hunter2-secret@db/control")
	require.NoError(t, err)
	require.NoError(t, tools.DumpTo(context.Background(), conn, io.Discard))
	require.NoError(t, tools.RestoreFrom(context.Background(), conn, strings.NewReader("x")))
	require.Len(t, got, 2)
	for _, c := range got {
		require.NotContains(t, strings.Join(c.Args, " "), "hunter2")
		require.Contains(t, c.Env, "PGPASSWORD=hunter2-secret")
	}
	require.Equal(t, "pg_dump", got[0].Name)
	require.Contains(t, got[0].Args, "--format=custom")
	require.Contains(t, got[0].Args, "--no-owner")
	require.Contains(t, got[0].Args, "--exclude-table-data=sessions")
	require.Equal(t, "pg_restore", got[1].Name)
	require.Contains(t, got[1].Args, "--single-transaction")
	require.Contains(t, got[1].Args, "--exit-on-error")
	require.Contains(t, got[1].Args, "--dbname=control")
}

func TestExecRunnerReportsMissingBinaryAndStderr(t *testing.T) {
	err := execRunner(context.Background(), Cmd{Name: "nebulactrl-no-such-binary"})
	require.ErrorContains(t, err, "was not found")

	err = execRunner(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "echo boom-detail >&2; exit 3"}})
	require.ErrorContains(t, err, "boom-detail")

	// The control plane's own secrets are not handed to the tool.
	t.Setenv("NEBULACTRL_SECRET_KEY", "must-not-leak")
	var out bytes.Buffer
	require.NoError(t, execRunner(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "env"}, Env: []string{"PGHOST=x"}, Stdout: &out}))
	require.NotContains(t, out.String(), "must-not-leak")
	require.Contains(t, out.String(), "PGHOST=x")
}

func TestKeyFingerprint(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	fp := KeyFingerprint(key)
	require.Len(t, fp, 12)
	require.Equal(t, fp, KeyFingerprint(key))
	require.NotEqual(t, fp, KeyFingerprint(bytes.Repeat([]byte{8}, 32)))
	require.NotContains(t, fp, hex.EncodeToString(key)[:6])
}

func TestInstanceObjectKey(t *testing.T) {
	id := uuid.MustParse("abcdef12-0000-0000-0000-000000000000")
	at := time.Date(2026, 10, 27, 3, 30, 5, 0, time.UTC)
	require.Equal(t, "nebulactrl-instance/2026/10/nebulactrl-20261027T033005Z-abcdef12.dump", instanceObjectKey("", at, id))
	require.Equal(t, "prod/eu/nebulactrl-instance/2026/10/nebulactrl-20261027T033005Z-abcdef12.dump", instanceObjectKey("/prod/eu/", at, id))
	require.NoError(t, agentprotocol.ValidObjectKey(instanceObjectKey("prod", at, id)))
}

func TestIsInstanceManifestKey(t *testing.T) {
	require.True(t, isInstanceManifestKey("nebulactrl-instance/2026/10/x.dump.manifest.json"))
	require.True(t, isInstanceManifestKey("a/b/nebulactrl-instance/2026/10/x.dump.manifest.json"))
	require.False(t, isInstanceManifestKey("nebulactrl-instance/2026/10/x.dump"))
	require.False(t, isInstanceManifestKey("databases/x.dump.manifest.json"))
	require.False(t, isInstanceManifestKey("nebulactrl-instance-other/x.dump.manifest.json"))
}

func TestParseAt(t *testing.T) {
	zero, err := ParseAt("")
	require.NoError(t, err)
	require.True(t, zero.IsZero())
	zero, err = ParseAt("Latest")
	require.NoError(t, err)
	require.True(t, zero.IsZero())

	want := time.Date(2026, 10, 27, 3, 30, 0, 0, time.UTC)
	for _, in := range []string{"2026-10-27T03:30:00Z", "2026-10-27T05:30:00+02:00", "2026-10-27 03:30", "2026-10-27T03:30", "2026-10-27 03:30:00"} {
		got, err := ParseAt(in)
		require.NoError(t, err, in)
		require.True(t, got.Equal(want), "%s -> %s", in, got)
	}
	day, err := ParseAt("2026-10-27")
	require.NoError(t, err)
	require.True(t, day.Equal(time.Date(2026, 10, 27, 23, 59, 59, 0, time.UTC)), "a date means the end of that UTC day")

	for _, bad := range []string{"yesterday", "27/10/2026", "2026-13-01"} {
		_, err := ParseAt(bad)
		require.Error(t, err, bad)
	}
}

func TestParseRestoreSource(t *testing.T) {
	s, err := ParseRestoreSource("s3://my-bucket/prod/eu/")
	require.NoError(t, err)
	require.Equal(t, RestoreSource{Bucket: "my-bucket", Prefix: "prod/eu"}, s)
	s, err = ParseRestoreSource("s3://bare")
	require.NoError(t, err)
	require.Equal(t, RestoreSource{Bucket: "bare"}, s)
	s, err = ParseRestoreSource("file:///var/backups/nebula")
	require.NoError(t, err)
	require.Equal(t, RestoreSource{Dir: "/var/backups/nebula"}, s)
	s, err = ParseRestoreSource("/var/backups/nebula")
	require.NoError(t, err)
	require.Equal(t, RestoreSource{Dir: "/var/backups/nebula"}, s)
	for _, bad := range []string{"", "s3://", "file://"} {
		_, err := ParseRestoreSource(bad)
		require.Error(t, err, bad)
	}
}

func rb(id string, at time.Time) RemoteBackup {
	return RemoteBackup{Manifest: InstanceManifest{BackupManifest: agentprotocol.BackupManifest{BackupID: id, CreatedAt: at}}}
}

func TestSelectBackup(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 10, day, 3, 30, 0, 0, time.UTC) }
	found := []RemoteBackup{rb("cccccccc-1", d(27)), rb("bbbbbbbb-1", d(26)), rb("aaaaaaaa-1", d(25))} // newest first

	got, err := SelectBackup(found, time.Time{}, "")
	require.NoError(t, err)
	require.Equal(t, "cccccccc-1", got.Manifest.BackupID)

	got, err = SelectBackup(found, d(26).Add(time.Hour), "")
	require.NoError(t, err)
	require.Equal(t, "bbbbbbbb-1", got.Manifest.BackupID, "newest at or before the time")
	got, err = SelectBackup(found, d(26), "")
	require.NoError(t, err)
	require.Equal(t, "bbbbbbbb-1", got.Manifest.BackupID, "inclusive")

	_, err = SelectBackup(found, d(24), "")
	require.ErrorContains(t, err, "oldest one is from 2026-10-25")
	_, err = SelectBackup(nil, time.Time{}, "")
	require.ErrorContains(t, err, "no control-plane backups")

	got, err = SelectBackup(found, time.Time{}, "aaaaaaaa")
	require.NoError(t, err)
	require.Equal(t, "aaaaaaaa-1", got.Manifest.BackupID)
	_, err = SelectBackup(found, time.Time{}, "zzzzzzzz")
	require.Error(t, err)
}

// putBackup stores a fake dump and its manifest the way a run does.
func putBackup(t *testing.T, st backup.Store, key string, body []byte, at time.Time, mutate func(*InstanceManifest)) InstanceManifest {
	t.Helper()
	sum := sha256.Sum256(body)
	m := InstanceManifest{
		BackupManifest: agentprotocol.BackupManifest{
			Version: 1, BackupID: uuid.NewString(), Engine: "postgres", EngineVersion: "17.2", Format: agentprotocol.FormatPGCustom,
			ObjectKey: key, SizeBytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:]), CreatedAt: at,
		},
		Kind: InstanceManifestKind, KeyFingerprint: "abc123abc123",
	}
	if mutate != nil {
		mutate(&m)
	}
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, st.Put(context.Background(), key, bytes.NewReader(body)))
	require.NoError(t, st.Put(context.Background(), backup.ManifestKey(key), bytes.NewReader(raw)))
	return m
}

func TestFindBackupsListsOnlyCommittedInstanceBackups(t *testing.T) {
	root := t.TempDir()
	st, err := backup.NewLocalStore(root, "")
	require.NoError(t, err)
	ctx := context.Background()
	t1 := time.Date(2026, 10, 25, 3, 30, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 26, 3, 30, 0, 0, time.UTC)
	putBackup(t, st, "nebulactrl-instance/2026/10/a.dump", []byte("one"), t1, nil)
	putBackup(t, st, "pre/nebulactrl-instance/2026/10/b.dump", []byte("two"), t2, nil)
	// Not ours: a database backup's manifest, a manifest of another kind, a dump
	// without a manifest (an upload that never committed) and a damaged manifest.
	putBackup(t, st, "databases/c.dump", []byte("db"), t2, nil)
	putBackup(t, st, "nebulactrl-instance/2026/10/d.dump", []byte("kind"), t2, func(m *InstanceManifest) { m.Kind = "other" })
	require.NoError(t, st.Put(ctx, "nebulactrl-instance/2026/10/e.dump", strings.NewReader("orphan")))
	require.NoError(t, st.Put(ctx, "nebulactrl-instance/2026/10/f.dump.manifest.json", strings.NewReader("{not json")))

	found, err := FindBackups(ctx, st, "")
	require.NoError(t, err)
	require.Len(t, found, 2)
	require.Equal(t, "pre/nebulactrl-instance/2026/10/b.dump", found[0].Manifest.ObjectKey, "newest first")
	require.Equal(t, "nebulactrl-instance/2026/10/a.dump", found[1].Manifest.ObjectKey)

	// A prefix narrows the search.
	found, err = FindBackups(ctx, st, "pre")
	require.NoError(t, err)
	require.Len(t, found, 1)

	// An empty or missing directory has none.
	empty, err := backup.NewLocalStore(filepath.Join(root, "missing"), "")
	require.NoError(t, err)
	found, err = FindBackups(ctx, empty, "")
	require.NoError(t, err)
	require.Empty(t, found)
}

func TestDownloadVerifiedDetectsDamage(t *testing.T) {
	st, err := backup.NewLocalStore(t.TempDir(), "")
	require.NoError(t, err)
	at := time.Now().UTC()
	body := []byte("PGDMP-pretend-dump-contents")
	m := putBackup(t, st, "nebulactrl-instance/x.dump", body, at, nil)

	tmp := func() *os.File {
		f, err := os.CreateTemp(t.TempDir(), "dl")
		require.NoError(t, err)
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	require.NoError(t, downloadVerified(context.Background(), st, m, tmp()))

	bad := m
	bad.SHA256 = strings.Repeat("0", 64)
	require.ErrorContains(t, downloadVerified(context.Background(), st, bad, tmp()), "SHA-256")
	bad = m
	bad.SizeBytes++
	require.ErrorContains(t, downloadVerified(context.Background(), st, bad, tmp()), "incomplete or damaged")
	bad = m
	bad.ObjectKey = "nebulactrl-instance/missing.dump"
	require.Error(t, downloadVerified(context.Background(), st, bad, tmp()))
}

func TestLatestMigrationReadsEmbeddedMigrations(t *testing.T) {
	require.GreaterOrEqual(t, latestMigration(), int64(1))
}

func TestHumanBytes(t *testing.T) {
	require.Equal(t, "512 B", HumanBytes(512))
	require.Equal(t, "1.5 KiB", HumanBytes(1536))
	require.Equal(t, "2.0 MiB", HumanBytes(2<<20))
}

// ---- service, against a real schema (no pg_dump needed) --------------------

const (
	canaryDBPassword = "sekret-canary-pg-24680"
	canaryS3Secret   = "sekret-canary-s3-13579"
)

type instEnv struct {
	svc  *InstanceService
	ctx  context.Context
	pool *pgxpool.Pool
	dir  string
	ws   uuid.UUID
}

// fakeDump is what the fake pg_dump writes.
var fakeDump = append([]byte("PGDMP"), bytes.Repeat([]byte("row data "), 4000)...)

// fakeTools write fakeDump on pg_dump and fail on demand.
type fakeTools struct {
	mu     sync.Mutex
	failed error
	calls  int
}

func (f *fakeTools) tools() PGTools {
	return PGTools{
		Check: func() error { return nil },
		Run: func(_ context.Context, c Cmd) error {
			f.mu.Lock()
			f.calls++
			failed := f.failed
			f.mu.Unlock()
			if c.Name != "pg_dump" {
				return errors.New("unexpected tool " + c.Name)
			}
			if failed != nil {
				_, _ = c.Stdout.Write(fakeDump[:100]) // a partial dump, then the failure
				return failed
			}
			_, err := c.Stdout.Write(fakeDump)
			return err
		},
	}
}

type adminFunc func(uuid.UUID) bool

func (f adminFunc) IsInstanceAdmin(_ context.Context, id uuid.UUID) (bool, error) { return f(id), nil }

func newInstEnv(t *testing.T, tools PGTools) *instEnv {
	t.Helper()
	return newInstEnvOn(t, testutil.NewPool(t), "postgres://nebula:"+canaryDBPassword+"@db.internal:5432/nebulactrl", tools)
}

// newInstEnvOn builds the service on pool; dbURL is the database pg_dump reads.
func newInstEnvOn(t *testing.T, pool *pgxpool.Pool, dbURL string, tools PGTools) *instEnv {
	t.Helper()
	ctx := context.Background()
	ws, err := db.New(pool).CreateWorkspace(ctx, "Team")
	require.NoError(t, err)
	admin := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO users (id, email, name, password_hash) VALUES ($1, 'admin@example.com', 'Admin', 'x')`, admin)
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{3}, 32))
	require.NoError(t, err)
	dir := t.TempDir()
	jm := jobs.New(pool, nil)
	svc := NewInstanceService(InstanceDeps{
		Pool: pool, Jobs: jm, Box: box, Admins: adminFunc(func(id uuid.UUID) bool { return id == admin }),
		DatabaseURL: dbURL, BackupDir: dir, KeyFingerprint: KeyFingerprint(bytes.Repeat([]byte{3}, 32)), Tools: tools,
	})
	RegisterInstanceJobs(jm, svc)
	pr := &auth.Principal{Type: auth.TypeUser, UserID: admin, WorkspaceID: ws.ID, Role: auth.RoleOwner, Label: "alice@example.com", Scopes: []string{"admin"}}
	return &instEnv{svc: svc, ctx: auth.WithPrincipal(ctx, pr), pool: pool, dir: dir, ws: ws.ID}
}

func (e *instEnv) enableLocal(t *testing.T, retention int) {
	t.Helper()
	_, err := e.svc.Update(e.ctx, InstanceBackupPatch{
		Enabled: new(true), Kind: new("local"), LocalDir: new("nightly"), Prefix: new("prod"), RetentionCount: &retention,
	})
	require.NoError(t, err)
}

// runOnce queues a manual backup and runs it to the end.
func (e *instEnv) runOnce(t *testing.T) db.InstanceBackup {
	t.Helper()
	v, err := e.svc.RunNow(e.ctx)
	require.NoError(t, err)
	require.Equal(t, "running", v.Status)
	e.svc.Run(e.ctx, v.ID)
	row, err := e.svc.q.GetInstanceBackup(e.ctx, v.ID)
	require.NoError(t, err)
	return row
}

func requireStatus(t *testing.T, err error, status int) {
	t.Helper()
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, status, ae.Status)
}

func TestInstanceBackupIsInstanceAdminOnly(t *testing.T) {
	e := newInstEnv(t, (&fakeTools{}).tools())
	member := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: e.ws, Role: auth.RoleOwner, Scopes: []string{"admin"}})
	token := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, UserID: uuid.New(), WorkspaceID: e.ws, Scopes: []string{"projects:read"}})
	for name, ctx := range map[string]context.Context{"team owner who is not the instance admin": member, "token without admin scope": token, "anonymous": context.Background()} {
		_, err := e.svc.Get(ctx)
		require.Error(t, err, name)
		_, err = e.svc.Update(ctx, InstanceBackupPatch{Enabled: new(true)})
		require.Error(t, err, name)
		_, err = e.svc.RunNow(ctx)
		require.Error(t, err, name)
		_, err = e.svc.Test(ctx)
		require.Error(t, err, name)
		_, err = e.svc.AcknowledgeKey(ctx)
		require.Error(t, err, name)
		_, err = e.svc.ListRuns(ctx, 10)
		require.Error(t, err, name)
	}
	_, err := e.svc.Get(member)
	requireStatus(t, err, 403)
	_, err = e.svc.Get(context.Background())
	requireStatus(t, err, 401)

	v, err := e.svc.Get(e.ctx)
	require.NoError(t, err)
	require.False(t, v.Enabled)
	require.Equal(t, DefaultInstanceSchedule, v.Schedule)
	require.Equal(t, DefaultInstanceRetention, v.RetentionCount)
	require.True(t, v.ToolsAvailable)
	require.True(t, v.LocalDirEnabled)
	require.Nil(t, v.KeyAcknowledgedAt)
	require.Equal(t, KeyFingerprint(bytes.Repeat([]byte{3}, 32)), v.KeyFingerprint)
}

func TestInstanceBackupSettingsValidation(t *testing.T) {
	e := newInstEnv(t, (&fakeTools{}).tools())

	// Enabling needs a complete destination.
	_, err := e.svc.Update(e.ctx, InstanceBackupPatch{Enabled: new(true), Kind: new("s3"), Bucket: new("my-bucket")})
	requireStatus(t, err, 422)
	require.ErrorContains(t, err, "access key")
	// A malformed bucket, schedule and retention are refused.
	_, err = e.svc.Update(e.ctx, InstanceBackupPatch{Kind: new("s3"), Bucket: new("NOT A BUCKET")})
	requireStatus(t, err, 422)
	_, err = e.svc.Update(e.ctx, InstanceBackupPatch{Schedule: new("every day")})
	requireStatus(t, err, 422)
	_, err = e.svc.Update(e.ctx, InstanceBackupPatch{Schedule: new("* * * * *")})
	requireStatus(t, err, 422)
	_, err = e.svc.Update(e.ctx, InstanceBackupPatch{RetentionCount: new(0)})
	requireStatus(t, err, 422)
	_, err = e.svc.Update(e.ctx, InstanceBackupPatch{RetentionCount: new(366)})
	requireStatus(t, err, 422)
	_, err = e.svc.Update(e.ctx, InstanceBackupPatch{Kind: new("local"), LocalDir: new("../escape")})
	requireStatus(t, err, 422)
	// Nothing was stored by the refused updates.
	v, err := e.svc.Get(e.ctx)
	require.NoError(t, err)
	require.False(t, v.Enabled)
	require.Equal(t, "local", v.Destination.Kind)

	// A complete S3 destination: keys are sealed, never returned.
	v, err = e.svc.Update(e.ctx, InstanceBackupPatch{
		Enabled: new(true), Kind: new("s3"), Endpoint: new("https://s3.example.com"), Region: new("eu-central-1"), Bucket: new("nebula-backups"),
		Prefix: new("/prod/"), AccessKey: new("AKIACANARY"), SecretKey: new(canaryS3Secret),
	})
	require.NoError(t, err)
	require.True(t, v.Enabled)
	require.True(t, v.Destination.Configured)
	require.True(t, v.Destination.OffSite)
	require.True(t, v.Destination.AccessKeySet && v.Destination.SecretKeySet)
	require.Equal(t, "prod", v.Destination.Prefix)
	require.Equal(t, "s3://nebula-backups/prod (s3.example.com)", v.Destination.Where)
	require.NotNil(t, v.NextRunAt)
	raw, _ := json.Marshal(v)
	require.NotContains(t, string(raw), canaryS3Secret)
	require.NotContains(t, string(raw), "AKIACANARY")
	row, err := e.svc.q.GetInstanceBackupSettings(e.ctx)
	require.NoError(t, err)
	require.NotContains(t, string(row.SecretKeyEnc), canaryS3Secret, "stored sealed")
	pt, err := e.svc.box.OpenString(secretbox.InstanceBackupSecretKey, secretbox.InstanceRow, row.SecretKeyEnc)
	require.NoError(t, err)
	require.Equal(t, canaryS3Secret, pt)

	// Editing without keys keeps them; moving the bucket clears the test verdict.
	ok := true
	require.NoError(t, e.svc.q.SetInstanceBackupTest(e.ctx, db.SetInstanceBackupTestParams{LastTestOk: &ok, LastTestMessage: "fine"}))
	v, err = e.svc.Update(e.ctx, InstanceBackupPatch{RetentionCount: new(30), AccessKey: new(""), SecretKey: new("")})
	require.NoError(t, err)
	require.Equal(t, 30, v.RetentionCount)
	require.True(t, v.Destination.SecretKeySet)
	require.NotNil(t, v.Destination.LastTestOK, "unrelated edits keep the verdict")
	v, err = e.svc.Update(e.ctx, InstanceBackupPatch{Bucket: new("other-bucket")})
	require.NoError(t, err)
	require.Nil(t, v.Destination.LastTestOK, "a moved destination has not been tested")

	// Switching to a local directory starts afresh: no bucket, no keys.
	v, err = e.svc.Update(e.ctx, InstanceBackupPatch{Kind: new("local"), LocalDir: new("nightly")})
	require.NoError(t, err)
	require.Equal(t, "local", v.Destination.Kind)
	require.False(t, v.Destination.OffSite, "a local directory is not off-site")
	require.Empty(t, v.Destination.Bucket)
	require.False(t, v.Destination.AccessKeySet)
	require.Equal(t, "directory "+filepath.Join(e.dir, "nightly"), v.Destination.Where)

	// Disabling clears the pending slot; credentials need an s3 destination.
	v, err = e.svc.Update(e.ctx, InstanceBackupPatch{Enabled: new(false)})
	require.NoError(t, err)
	require.Nil(t, v.NextRunAt)
	_, err = e.svc.Update(e.ctx, InstanceBackupPatch{AccessKey: new("AKIA"), SecretKey: new("x")})
	requireStatus(t, err, 422)

	// The audit log names the change but holds no secret.
	var meta string
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT string_agg(metadata::text, ' ') FROM audit_events WHERE action = $1`, ActionInstanceBackupUpdate).Scan(&meta))
	require.NotContains(t, meta, canaryS3Secret)
	require.NotContains(t, meta, "AKIACANARY")
	require.Contains(t, meta, "credentialsChanged")
}

func TestInstanceBackupLocalNeedsBackupDir(t *testing.T) {
	e := newInstEnv(t, (&fakeTools{}).tools())
	e.svc.backupDir = ""
	_, err := e.svc.Update(e.ctx, InstanceBackupPatch{Enabled: new(true)})
	requireStatus(t, err, 422)
	require.ErrorContains(t, err, "NEBULACTRL_BACKUP_DIR")
	v, err := e.svc.Get(e.ctx)
	require.NoError(t, err)
	require.False(t, v.LocalDirEnabled)
	require.False(t, v.Destination.Configured)
	_, err = e.svc.RunNow(e.ctx)
	requireStatus(t, err, 422)
}

func TestInstanceKeyAcknowledgement(t *testing.T) {
	e := newInstEnv(t, (&fakeTools{}).tools())
	v, err := e.svc.Get(e.ctx)
	require.NoError(t, err)
	require.Nil(t, v.KeyAcknowledgedAt, "the reminder shows until acknowledged")
	v, err = e.svc.AcknowledgeKey(e.ctx)
	require.NoError(t, err)
	require.NotNil(t, v.KeyAcknowledgedAt)
	v, err = e.svc.Get(e.ctx)
	require.NoError(t, err)
	require.NotNil(t, v.KeyAcknowledgedAt, "it persists")
	var n int
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT count(*) FROM audit_events WHERE action = $1`, ActionInstanceKeyAck).Scan(&n))
	require.Equal(t, 1, n)
}

func TestInstanceBackupRunWritesDumpAndManifest(t *testing.T) {
	ft := &fakeTools{}
	e := newInstEnv(t, ft.tools())
	e.enableLocal(t, 14)
	var events []Event
	e.svc.SetNotifier(NotifierFunc(func(_ context.Context, ev Event) { events = append(events, ev) }))

	row := e.runOnce(t)
	require.Equal(t, "succeeded", row.Status, row.Error)
	require.Equal(t, int64(len(fakeDump)), row.SizeBytes)
	sum := sha256.Sum256(fakeDump)
	require.Equal(t, hex.EncodeToString(sum[:]), row.Sha256)
	require.Contains(t, row.ObjectKey, "prod/nebulactrl-instance/")
	require.NotNil(t, row.FinishedAt)

	// The dump and its manifest sit in the destination directory.
	dump, err := os.ReadFile(filepath.Join(e.dir, "nightly", filepath.FromSlash(row.ObjectKey)))
	require.NoError(t, err)
	require.Equal(t, fakeDump, dump)
	st, err := backup.NewLocalStore(e.dir, "nightly")
	require.NoError(t, err)
	found, err := FindBackups(e.ctx, st, "")
	require.NoError(t, err)
	require.Len(t, found, 1)
	m := found[0].Manifest
	require.Equal(t, row.ID.String(), m.BackupID)
	require.Equal(t, row.Sha256, m.SHA256)
	require.Equal(t, KeyFingerprint(bytes.Repeat([]byte{3}, 32)), m.KeyFingerprint)
	require.Equal(t, agentprotocol.FormatPGCustom, m.Format)
	require.Equal(t, InstanceManifestKind, m.Kind)

	// What was stored holds neither the instance key nor the database password.
	require.NoError(t, filepath.WalkDir(e.dir, func(p string, d os.DirEntry, err error) error {
		require.NoError(t, err)
		if !d.IsDir() {
			b, err := os.ReadFile(p)
			require.NoError(t, err)
			require.NotContains(t, string(b), canaryDBPassword)
			require.NotContains(t, string(b), hex.EncodeToString(bytes.Repeat([]byte{3}, 32)))
		}
		return nil
	}))

	// The audit entry and the notification describe the run without secrets.
	var meta string
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT string_agg(metadata::text, ' ') FROM audit_events WHERE action = $1`, ActionInstanceBackupDone).Scan(&meta))
	require.Contains(t, meta, row.ObjectKey)
	require.Len(t, events, 1)
	require.Equal(t, NotifyBackupSucceeded, events[0].Type)
	require.True(t, events[0].Instance)
	require.Equal(t, e.ws, events[0].WorkspaceID)

	v, err := e.svc.Get(e.ctx)
	require.NoError(t, err)
	require.NotNil(t, v.LastRun)
	require.Equal(t, "succeeded", v.LastRun.Status)
	require.NotNil(t, v.LastSuccessAt)

	// Running again after it finished is allowed.
	require.Equal(t, "succeeded", e.runOnce(t).Status)
	runs, err := e.svc.ListRuns(e.ctx, 10)
	require.NoError(t, err)
	require.Len(t, runs, 2)
	require.True(t, !runs[0].StartedAt.Before(runs[1].StartedAt), "newest first")
}

func TestInstanceBackupOneRunAtATime(t *testing.T) {
	e := newInstEnv(t, (&fakeTools{}).tools())
	e.enableLocal(t, 14)
	first, err := e.svc.RunNow(e.ctx)
	require.NoError(t, err)
	_, err = e.svc.RunNow(e.ctx)
	requireStatus(t, err, 409)
	require.ErrorContains(t, err, "already running")
	e.svc.Run(e.ctx, first.ID)
	_, err = e.svc.RunNow(e.ctx)
	require.NoError(t, err)
	// Running a finished row again (a re-delivered job) does nothing.
	var n int
	e.svc.Run(e.ctx, first.ID)
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT count(*) FROM instance_backups WHERE status = 'succeeded'`).Scan(&n))
	require.Equal(t, 1, n)
}

func TestInstanceBackupFailureLeavesNoObjectAndMasksSecrets(t *testing.T) {
	ft := &fakeTools{failed: errors.New("pg_dump: error: connection to server failed: password authentication failed for password " + canaryDBPassword)}
	e := newInstEnv(t, ft.tools())
	e.enableLocal(t, 14)
	var events []Event
	e.svc.SetNotifier(NotifierFunc(func(_ context.Context, ev Event) { events = append(events, ev) }))

	row := e.runOnce(t)
	require.Equal(t, "failed", row.Status)
	require.Contains(t, row.Error, "password authentication failed")
	require.NotContains(t, row.Error, canaryDBPassword, "the database password is masked")
	require.Contains(t, row.Error, "••••••")
	require.Empty(t, row.ObjectKey)

	// No dump, manifest or half-written temp file stayed behind.
	var leftovers []string
	_ = filepath.WalkDir(e.dir, func(p string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			leftovers = append(leftovers, p)
		}
		return nil
	})
	require.Empty(t, leftovers)

	require.Len(t, events, 1)
	require.Equal(t, NotifyBackupFailed, events[0].Type)
	require.NotContains(t, events[0].Message, canaryDBPassword)
	var meta string
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT string_agg(metadata::text, ' ') FROM audit_events WHERE action = $1`, ActionInstanceBackupFailed).Scan(&meta))
	require.NotContains(t, meta, canaryDBPassword)

	// Failed runs are not restore points.
	v, err := e.svc.Get(e.ctx)
	require.NoError(t, err)
	require.Equal(t, "failed", v.LastRun.Status)
	require.Nil(t, v.LastSuccessAt)

	// A later run succeeds once the cause is gone.
	ft.mu.Lock()
	ft.failed = nil
	ft.mu.Unlock()
	require.Equal(t, "succeeded", e.runOnce(t).Status)
}

func TestInstanceBackupEmptyDumpFails(t *testing.T) {
	tools := PGTools{Check: func() error { return nil }, Run: func(context.Context, Cmd) error { return nil }}
	e := newInstEnv(t, tools)
	e.enableLocal(t, 14)
	row := e.runOnce(t)
	require.Equal(t, "failed", row.Status)
	require.Contains(t, row.Error, "no output")
}

func TestInstanceBackupMissingToolsRefusesToStart(t *testing.T) {
	e := newInstEnv(t, PGTools{Check: func() error { return errors.New("pg_dump is not installed") }, Run: func(context.Context, Cmd) error { return nil }})
	e.enableLocal(t, 14)
	_, err := e.svc.RunNow(e.ctx)
	requireStatus(t, err, 422)
	v, err := e.svc.Get(e.ctx)
	require.NoError(t, err)
	require.False(t, v.ToolsAvailable)
	require.Contains(t, v.ToolsMessage, "pg_dump")
}

func TestInstanceBackupRetentionKeepsNewestN(t *testing.T) {
	e := newInstEnv(t, (&fakeTools{}).tools())
	e.enableLocal(t, 2)
	var rows []db.InstanceBackup
	for range 4 {
		rows = append(rows, e.runOnce(t))
		time.Sleep(5 * time.Millisecond) // distinct started_at
	}
	st, err := backup.NewLocalStore(e.dir, "nightly")
	require.NoError(t, err)
	found, err := FindBackups(e.ctx, st, "")
	require.NoError(t, err)
	require.Len(t, found, 2, "only the newest two stay")
	require.Equal(t, rows[3].ID.String(), found[0].Manifest.BackupID)
	require.Equal(t, rows[2].ID.String(), found[1].Manifest.BackupID)

	runs, err := e.svc.ListRuns(e.ctx, 10)
	require.NoError(t, err)
	require.Len(t, runs, 4, "history stays")
	deleted := 0
	for _, r := range runs {
		if r.ObjectDeleted {
			deleted++
		}
	}
	require.Equal(t, 2, deleted)

	// Backups of an earlier destination are history, never pruned from the new one.
	cur, err := e.svc.q.GetInstanceBackupSettings(e.ctx)
	require.NoError(t, err)
	old, err := e.svc.q.InsertInstanceBackup(e.ctx, db.InsertInstanceBackupParams{Trigger: "manual", DestKind: "s3", DestLabel: "s3://old", DestFingerprint: "other"})
	require.NoError(t, err)
	_, err = e.svc.q.FinishInstanceBackup(e.ctx, db.FinishInstanceBackupParams{ID: old.ID, Status: "succeeded", ObjectKey: "x.dump"})
	require.NoError(t, err)
	e.svc.prune(e.ctx, cur)
	got, err := e.svc.q.GetInstanceBackup(e.ctx, old.ID)
	require.NoError(t, err)
	require.Nil(t, got.ObjectDeletedAt)
}

func TestInstanceBackupTickStartsDueBackupOnce(t *testing.T) {
	e := newInstEnv(t, (&fakeTools{}).tools())
	e.enableLocal(t, 14)
	now := time.Date(2026, 10, 27, 3, 31, 0, 0, time.UTC)
	e.svc.now = func() time.Time { return now }
	count := func() int {
		var n int
		require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT count(*) FROM instance_backups`).Scan(&n))
		return n
	}

	// Not due yet.
	_, err := e.pool.Exec(e.ctx, `UPDATE instance_backup_settings SET next_run_at = $1`, now.Add(time.Hour))
	require.NoError(t, err)
	require.NoError(t, e.svc.Tick(e.ctx))
	require.Zero(t, count())

	// Due: one run starts and the next slot is tomorrow 03:30 (UTC).
	_, err = e.pool.Exec(e.ctx, `UPDATE instance_backup_settings SET next_run_at = $1`, now.Add(-time.Minute))
	require.NoError(t, err)
	require.NoError(t, e.svc.Tick(e.ctx))
	require.Equal(t, 1, count())
	cur, err := e.svc.q.GetInstanceBackupSettings(e.ctx)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 10, 28, 3, 30, 0, 0, time.UTC), cur.NextRunAt.UTC())
	var trigger, actor string
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT trigger, actor_label FROM instance_backups`).Scan(&trigger, &actor))
	require.Equal(t, InstanceTriggerSchedule, trigger)
	require.Equal(t, "schedule", actor)

	// The same slot does not start twice, and a due slot while a run is still
	// going is skipped (not queued behind it).
	require.NoError(t, e.svc.Tick(e.ctx))
	require.Equal(t, 1, count())
	_, err = e.pool.Exec(e.ctx, `UPDATE instance_backup_settings SET next_run_at = $1`, now.Add(-time.Minute))
	require.NoError(t, err)
	require.NoError(t, e.svc.Tick(e.ctx))
	require.Equal(t, 1, count())

	// A disabled schedule never fires.
	_, err = e.pool.Exec(e.ctx, `UPDATE instance_backups SET status = 'failed', finished_at = now()`)
	require.NoError(t, err)
	_, err = e.pool.Exec(e.ctx, `UPDATE instance_backup_settings SET enabled = false, next_run_at = $1`, now.Add(-time.Hour))
	require.NoError(t, err)
	require.NoError(t, e.svc.Tick(e.ctx))
	require.Equal(t, 1, count())
}

func TestInstanceBackupTickFailsStaleRuns(t *testing.T) {
	e := newInstEnv(t, (&fakeTools{}).tools())
	e.enableLocal(t, 14)
	cur, err := e.svc.q.GetInstanceBackupSettings(e.ctx)
	require.NoError(t, err)
	stale, err := e.svc.q.InsertInstanceBackup(e.ctx, db.InsertInstanceBackupParams{Trigger: "manual", DestKind: "local", DestLabel: "x", DestFingerprint: instanceFingerprint(cur)})
	require.NoError(t, err)
	_, err = e.pool.Exec(e.ctx, `UPDATE instance_backups SET started_at = now() - interval '7 hours' WHERE id = $1`, stale.ID)
	require.NoError(t, err)
	var events []Event
	e.svc.SetNotifier(NotifierFunc(func(_ context.Context, ev Event) { events = append(events, ev) }))
	require.NoError(t, e.svc.Tick(e.ctx))
	got, err := e.svc.q.GetInstanceBackup(e.ctx, stale.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", got.Status)
	require.Contains(t, got.Error, "interrupted")
	require.Len(t, events, 1)
	require.Equal(t, NotifyBackupFailed, events[0].Type)
}

func TestInstanceBackupDestinationTest(t *testing.T) {
	e := newInstEnv(t, (&fakeTools{}).tools())
	_, err := e.svc.Test(e.ctx)
	require.NoError(t, err, "the default local destination works when NEBULACTRL_BACKUP_DIR is set")
	v, err := e.svc.Get(e.ctx)
	require.NoError(t, err)
	require.NotNil(t, v.Destination.LastTestOK)
	require.True(t, *v.Destination.LastTestOK, v.Destination.LastTestMessage)
	// The probe left nothing behind.
	require.NoError(t, filepath.WalkDir(e.dir, func(p string, d os.DirEntry, err error) error {
		require.NoError(t, err)
		require.True(t, d.IsDir(), "left %s", p)
		return nil
	}))

	// A directory that can not be written fails with a message, not an error.
	e.enableLocal(t, 14)
	require.NoError(t, os.WriteFile(filepath.Join(e.dir, "nightly"), []byte("a file, not a directory"), 0o600))
	v, err = e.svc.Test(e.ctx)
	require.NoError(t, err)
	require.NotNil(t, v.Destination.LastTestOK)
	require.False(t, *v.Destination.LastTestOK)
	require.NotEmpty(t, v.Destination.LastTestMessage)
}
