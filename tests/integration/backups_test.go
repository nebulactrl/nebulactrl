//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
)

// seaweedImage is the S3-compatible store the backup tests use. It is a test
// fixture only (never shipped): SeaweedFS is Apache-2.0, developed since 2014
// and actively maintained. Removed again by the test.
const seaweedImage = "chrislusf/seaweedfs:4.51"

const (
	s3AccessKey = "AKIAITESTBACKUPS01"
	s3SecretKey = "itest-backup-secret-key-0001"
	s3Bucket    = "nebulactrl-backups"
)

// pgExec runs a psql statement in the database container of serviceID.
func pgExec(t *testing.T, s *Stack, serviceID, sql string) string {
	t.Helper()
	return pgExecDB(t, s, serviceID, "app", sql)
}

func pgExecDB(t *testing.T, s *Stack, serviceID, database, sql string) string {
	t.Helper()
	cs := s.RunningContainers(serviceID)
	require.NotEmpty(t, cs, "no running container for %s", serviceID)
	var out, errb bytes.Buffer
	code, err := dockerEng.ContainerExec(context.Background(), cs[0].ID,
		[]string{"psql", "-U", "postgres", "-d", database, "-tA", "-v", "ON_ERROR_STOP=1", "-c", sql},
		docker.ExecSpec{}, &out, &errb)
	require.NoError(t, err)
	require.Zero(t, code, "psql %q: %s", sql, errb.String())
	return strings.TrimSpace(out.String())
}

func waitPostgres(t *testing.T, s *Stack, serviceID string) {
	t.Helper()
	ok := eventually(90*time.Second, time.Second, func() bool {
		cs := s.RunningContainers(serviceID)
		if len(cs) == 0 {
			return false
		}
		var out, errb bytes.Buffer
		code, err := dockerEng.ContainerExec(context.Background(), cs[0].ID,
			[]string{"psql", "-U", "postgres", "-d", "app", "-tA", "-c", "select 1"}, docker.ExecSpec{}, &out, &errb)
		return err == nil && code == 0
	})
	require.True(t, ok, "postgres of %s never accepted connections", serviceID)
}

func (s *Stack) createPostgres(name string) Service {
	s.t.Helper()
	r := s.Do("POST", "/environments/"+s.EnvID+"/databases", map[string]any{
		"name": name, "engine": "postgres", "version": "17", "serverId": s.ServerID,
	})
	require.Equal(s.t, http.StatusCreated, r.Status, string(r.Raw))
	svc := Service{ID: r.JSON().Str("id"), Slug: r.JSON().Str("slug")}
	s.servicesMu.Lock()
	s.serviceIDs = append(s.serviceIDs, svc.ID)
	s.servicesMu.Unlock()
	return svc
}

func (s *Stack) waitBackup(id string, timeout time.Duration) J {
	s.t.Helper()
	var b J
	ok := eventually(timeout, 500*time.Millisecond, func() bool {
		r := s.Do("GET", "/backups/"+id, nil)
		if r.Status != http.StatusOK {
			return false
		}
		b = r.JSON()
		switch b.Str("status") {
		case "succeeded", "failed", "skipped", "expired":
			return true
		}
		return false
	})
	require.True(s.t, ok, "backup %s did not finish within %s: %v", id, timeout, b)
	return b
}

func (s *Stack) waitRestore(id string, timeout time.Duration) J {
	s.t.Helper()
	var rs J
	ok := eventually(timeout, time.Second, func() bool {
		r := s.Do("GET", "/backup-restores/"+id, nil)
		if r.Status != http.StatusOK {
			return false
		}
		rs = r.JSON()
		return rs.Str("status") == "succeeded" || rs.Str("status") == "failed"
	})
	require.True(s.t, ok, "restore %s did not finish within %s: %v", id, timeout, rs)
	return rs
}

func (s *Stack) backup(serviceID, destID string) J {
	s.t.Helper()
	r := s.Do("POST", "/services/"+serviceID+"/backups", map[string]any{"destinationId": destID})
	require.Equal(s.t, http.StatusAccepted, r.Status, string(r.Raw))
	return s.waitBackup(r.JSON().Str("id"), 2*time.Minute)
}

// restoreNew restores into a new sibling database and returns its service.
func (s *Stack) restoreNew(backupID, name string) Service {
	s.t.Helper()
	r := s.Do("POST", "/backups/"+backupID+"/restore", map[string]any{"mode": "new", "name": name})
	require.Equal(s.t, http.StatusAccepted, r.Status, string(r.Raw))
	rs := s.waitRestore(r.JSON().Str("id"), 4*time.Minute)
	require.Equal(s.t, "succeeded", rs.Str("status"), "restore: %v", rs)
	id := rs.Str("targetServiceId")
	require.NotEmpty(s.t, id)
	s.servicesMu.Lock()
	s.serviceIDs = append(s.serviceIDs, id)
	s.servicesMu.Unlock()
	return Service{ID: id, Slug: s.Service(id).Str("slug")}
}

func startSeaweed(t *testing.T) (endpoint string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	cfg := fmt.Sprintf(`{"identities":[{"name":"itest","credentials":[{"accessKey":%q,"secretKey":%q}],"actions":["Admin","Read","Write","List","Tagging"]}]}`, s3AccessKey, s3SecretKey)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "s3.json"), []byte(cfg), 0o644))
	port, err := freePort()
	require.NoError(t, err)
	name := fmt.Sprintf("nebulactrl-itest-seaweed-%d", port)
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, _ = sh(cctx, "", "docker", "rm", "-f", "-v", name)
	})
	out, err := sh(ctx, "", "docker", "run", "-d", "--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:8333", port),
		"-v", dir+":/etc/seaweed:ro",
		seaweedImage, "server", "-dir=/data", "-s3", "-s3.port=8333", "-s3.config=/etc/seaweed/s3.json",
		"-master.volumeSizeLimitMB=64", "-volume.max=8")
	require.NoError(t, err, out)
	require.NoError(t, func() error {
		wctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		return waitTCP(wctx, fmt.Sprintf("127.0.0.1:%d", port))
	}())
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

func s3Client(endpoint string) *s3.Client {
	return s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String(endpoint), UsePathStyle: true,
		Credentials:                aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(s3AccessKey, s3SecretKey, "")),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

func listKeys(ctx context.Context, t *testing.T, c *s3.Client) []string {
	t.Helper()
	out, err := c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s3Bucket), Prefix: aws.String("nebula/")})
	require.NoError(t, err)
	var keys []string
	for _, o := range out.Contents {
		keys = append(keys, aws.ToString(o.Key))
	}
	return keys
}

// TestDatabaseBackupsPostgres covers the backup feature end to end on a real
// Postgres container: a streamed dump to a local directory and to an
// S3-compatible store (SeaweedFS), a failed dump that leaves nothing behind,
// restore into a new sibling database and in place, deletion of stored
// objects, and a scheduled run.
func TestDatabaseBackupsPostgres(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	s := SharedStackN(t, 2) // environment + the internal network of the databases
	ctx := context.Background()
	// The data volumes outlive their containers; remove them with the test.
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		s.servicesMu.Lock()
		ids := append([]string(nil), s.serviceIDs...)
		s.servicesMu.Unlock()
		for _, id := range ids {
			short := id
			if len(short) > 8 {
				short = short[:8]
			}
			out, _ := sh(cctx, "", "docker", "volume", "ls", "-q", "--filter", "name=nc-vol-"+short)
			for _, v := range strings.Fields(out) {
				_, _ = sh(cctx, "", "docker", "volume", "rm", "-f", v)
			}
		}
	})

	src := s.createPostgres("pg")
	s.DeployOK(src.ID)
	waitPostgres(t, s, src.ID)
	pgExec(t, s, src.ID, "CREATE TABLE notes (id int primary key, body text); INSERT INTO notes SELECT g, 'row ' || g FROM generate_series(1, 500) g")
	require.Equal(t, "500", pgExec(t, s, src.ID, "SELECT count(*) FROM notes"))

	// ---- local directory destination -------------------------------------
	r := s.Do("POST", "/backup-destinations", map[string]any{"name": "host disk", "kind": "local", "localDir": "itest"})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	local := r.JSON()
	require.False(t, local.Bool("offSite"), "local destinations are flagged not off-site")

	r = s.Do("POST", "/backup-destinations/"+local.Str("id")+"/test", map[string]any{"serverId": s.ServerID})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.True(t, r.JSON().Bool("lastTestOk"), "destination test: %s", r.JSON().Str("lastTestMessage"))

	b1 := s.backup(src.ID, local.Str("id"))
	require.Equal(t, "succeeded", b1.Str("status"), "backup: %v", b1)
	require.Equal(t, "pg_custom", b1.Str("format"))
	key := b1.Str("objectKey")
	require.True(t, strings.HasSuffix(key, ".dump"), key)
	file := filepath.Join(s.StateDir, "backups", "itest", filepath.FromSlash(key))
	raw, err := os.ReadFile(file)
	require.NoError(t, err, "the dump must exist in the local destination")
	require.Equal(t, b1.Int("sizeBytes"), len(raw))
	sum := sha256.Sum256(raw)
	require.Equal(t, hex.EncodeToString(sum[:]), b1.Str("sha256"))
	require.True(t, bytes.HasPrefix(raw, []byte("PGDMP")), "custom-format pg_dump magic")
	mraw, err := os.ReadFile(file + ".manifest.json")
	require.NoError(t, err, "manifest sidecar")
	var man map[string]any
	require.NoError(t, json.Unmarshal(mraw, &man))
	require.Equal(t, "postgres", man["engine"])
	require.Equal(t, b1.Str("sha256"), man["sha256"])
	// No leftovers: the streaming upload leaves no partial files.
	entries, _ := filepath.Glob(filepath.Join(filepath.Dir(file), ".partial-*"))
	require.Empty(t, entries)

	// ---- restore to a new database ---------------------------------------
	pgExec(t, s, src.ID, "INSERT INTO notes VALUES (1000, 'written after the backup')")
	sib := s.restoreNew(b1.Str("id"), "pg-restored")
	require.NotEqual(t, src.ID, sib.ID)
	waitPostgres(t, s, sib.ID)
	require.Equal(t, "500", pgExec(t, s, sib.ID, "SELECT count(*) FROM notes"), "the restored database holds the backup's rows")
	require.Equal(t, "row 250", pgExec(t, s, sib.ID, "SELECT body FROM notes WHERE id = 250"))
	require.Equal(t, "0", pgExec(t, s, sib.ID, "SELECT count(*) FROM notes WHERE id = 1000"), "rows written after the backup are not in it")
	require.Equal(t, "501", pgExec(t, s, src.ID, "SELECT count(*) FROM notes"), "the source database is untouched")

	// ---- in-place restore -------------------------------------------------
	r = s.Do("POST", "/backups/"+b1.Str("id")+"/restore", map[string]any{"mode": "in_place", "confirmSlug": "wrong"})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	r = s.Do("POST", "/backups/"+b1.Str("id")+"/restore", map[string]any{"mode": "in_place", "confirmSlug": src.Slug})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	rs := s.waitRestore(r.JSON().Str("id"), 3*time.Minute)
	require.Equal(t, "succeeded", rs.Str("status"), "in-place restore: %v", rs)
	require.NotEmpty(t, rs.Str("preBackupId"), "a safety backup precedes an in-place restore")
	pre := s.Do("GET", "/backups/"+rs.Str("preBackupId"), nil).JSON()
	require.Equal(t, "succeeded", pre.Str("status"))
	require.Equal(t, "pre_restore", pre.Str("trigger"))
	require.Equal(t, "500", pgExec(t, s, src.ID, "SELECT count(*) FROM notes"), "the in-place restore brought the database back to the backup")

	// ---- S3-compatible destination ---------------------------------------
	endpoint := startSeaweed(t)
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, _ = sh(cctx, "", "docker", "image", "rm", "-f", seaweedImage)
	})
	cl := s3Client(endpoint)
	require.True(t, eventually(60*time.Second, time.Second, func() bool {
		_, err := cl.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(s3Bucket)})
		return err == nil
	}), "bucket creation against SeaweedFS")

	r = s.Do("POST", "/backup-destinations", map[string]any{
		"name": "seaweed", "kind": "s3", "endpoint": endpoint, "region": "us-east-1", "bucket": s3Bucket, "prefix": "nebula",
		"pathStyle": true, "accessKey": s3AccessKey, "secretKey": s3SecretKey,
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	s3d := r.JSON()
	require.True(t, s3d.Bool("offSite"))
	require.True(t, s3d.Bool("compatMode"))
	require.NotContains(t, string(r.Raw), s3SecretKey, "the secret key is never returned")

	r = s.Do("POST", "/backup-destinations/"+s3d.Str("id")+"/test", map[string]any{"serverId": s.ServerID})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.True(t, r.JSON().Bool("lastTestOk"), "S3 destination test: %s", r.JSON().Str("lastTestMessage"))
	require.Empty(t, listKeys(ctx, t, cl), "the probe object is removed again")

	b2 := s.backup(src.ID, s3d.Str("id"))
	require.Equal(t, "succeeded", b2.Str("status"), "S3 backup: %v", b2)
	obj, err := cl.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s3Bucket), Key: aws.String(b2.Str("objectKey"))})
	require.NoError(t, err)
	body, err := io.ReadAll(obj.Body)
	_ = obj.Body.Close()
	require.NoError(t, err)
	require.Equal(t, b2.Int("sizeBytes"), len(body))
	sum2 := sha256.Sum256(body)
	require.Equal(t, hex.EncodeToString(sum2[:]), b2.Str("sha256"), "the stored object is the dump that was hashed while streaming")
	require.Contains(t, listKeys(ctx, t, cl), b2.Str("objectKey")+".manifest.json")

	sib2 := s.restoreNew(b2.Str("id"), "pg-from-s3")
	waitPostgres(t, s, sib2.ID)
	require.Equal(t, "500", pgExec(t, s, sib2.ID, "SELECT count(*) FROM notes"), "restore from the S3 object")

	// ---- a failed dump leaves no object (and no manifest) ----------------
	before := listKeys(ctx, t, cl)
	// Rename the database away: pg_dump then exits non-zero ("does not exist").
	pgExecDB(t, s, src.ID, "postgres", "ALTER DATABASE app RENAME TO app_away")
	bad := s.backup(src.ID, s3d.Str("id"))
	require.Equal(t, "failed", bad.Str("status"), "backup: %v", bad)
	require.Contains(t, bad.Str("error"), "does not exist")
	require.Equal(t, before, listKeys(ctx, t, cl), "a failed dump must not leave an object or a manifest in the bucket")
	badLocal := s.backup(src.ID, local.Str("id"))
	require.Equal(t, "failed", badLocal.Str("status"))
	left, _ := filepath.Glob(filepath.Join(s.StateDir, "backups", "itest", "*", "*", "*", "*"))
	for _, f := range left {
		require.NotContains(t, filepath.Base(f), badLocal.Str("id"))
		require.False(t, strings.HasPrefix(filepath.Base(f), ".partial-"), "partial file left: %s", f)
	}
	pgExecDB(t, s, src.ID, "postgres", "ALTER DATABASE app_away RENAME TO app")

	// ---- secrets never leak ------------------------------------------------
	for _, q := range []string{
		"SELECT coalesce(string_agg(t::text, ' '), '') FROM agent_commands t",
		"SELECT coalesce(string_agg(t::text, ' '), '') FROM backups t",
		"SELECT coalesce(string_agg(t::text, ' '), '') FROM backup_restores t",
		"SELECT coalesce(string_agg(t::text, ' '), '') FROM audit_events t",
	} {
		var dump string
		require.NoError(t, s.Pool.QueryRow(ctx, q).Scan(&dump))
		require.NotContains(t, dump, s3SecretKey, q)
		require.NotContains(t, dump, s3AccessKey, q)
	}

	// ---- deleting a backup removes the stored object ----------------------
	r = s.Do("DELETE", "/backups/"+b2.Str("id"), nil)
	require.Equal(t, http.StatusNoContent, r.Status, string(r.Raw))
	require.True(t, eventually(60*time.Second, time.Second, func() bool {
		keys := listKeys(ctx, t, cl)
		return !hasKey(keys, b2.Str("objectKey")) && !hasKey(keys, b2.Str("objectKey")+".manifest.json")
	}), "the S3 object and its manifest are deleted through the agent")
	require.Empty(t, listKeys(ctx, t, cl), "nothing else is left in the bucket")
	require.True(t, eventually(30*time.Second, time.Second, func() bool {
		return s.Do("GET", "/backups/"+b2.Str("id"), nil).Status == http.StatusNotFound
	}), "the row disappears once the object is gone")

	r = s.Do("PUT", "/backups/"+b1.Str("id")+"/lock", map[string]any{"locked": true})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	r = s.Do("DELETE", "/backups/"+b1.Str("id"), nil)
	require.Equal(t, http.StatusConflict, r.Status, "locked backups can not be deleted: %s", r.Raw)
	r = s.Do("PUT", "/backups/"+b1.Str("id")+"/lock", map[string]any{"locked": false})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	r = s.Do("DELETE", "/backups/"+b1.Str("id"), nil)
	require.Equal(t, http.StatusNoContent, r.Status, string(r.Raw))
	require.True(t, eventually(30*time.Second, time.Second, func() bool {
		_, err := os.Stat(file)
		return os.IsNotExist(err)
	}), "the local dump is deleted through the agent")

	// ---- a schedule runs on its own ---------------------------------------
	r = s.Do("POST", "/services/"+src.ID+"/backup-schedules", map[string]any{
		"destinationId": local.Str("id"), "cron": "@daily", "retentionCount": 1,
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	sched := r.JSON()
	_, err = s.Pool.Exec(ctx, "UPDATE backup_schedules SET next_run_at = now() - interval '1 minute' WHERE id = $1", sched.Str("id"))
	require.NoError(t, err)
	var scheduled J
	require.True(t, eventually(150*time.Second, 2*time.Second, func() bool {
		r := s.Do("GET", "/services/"+src.ID+"/backups", nil)
		for _, b := range r.JSON().Items() {
			if b.Str("trigger") == "schedule" && b.Str("status") == "succeeded" {
				scheduled = b
				return true
			}
		}
		return false
	}), "the schedule tick must start and finish a scheduled backup")
	require.Equal(t, sched.Str("id"), scheduled.Str("scheduleId"))
	next := s.Do("GET", "/services/"+src.ID+"/backup-schedules", nil).JSON().Items()[0]
	require.NotEmpty(t, next.Str("lastRunAt"))
}

func hasKey(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
