package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func newRunner(ex *fakeExec, st Store) *Runner {
	return &Runner{
		Docker: ex,
		Stores: func(agentprotocol.BackupDestination, map[string]string) (Store, error) { return st, nil },
	}
}

// scriptRunner is a runner over a scripted exec, for the restores that run psql.
func scriptRunner(ex *scriptExec, st Store) *Runner {
	return &Runner{
		Docker: ex,
		Stores: func(agentprotocol.BackupDestination, map[string]string) (Store, error) { return st, nil },
	}
}

func pgPayload() agentprotocol.DBBackupPayload {
	return agentprotocol.DBBackupPayload{
		BackupID:    "b1",
		Target:      agentprotocol.BackupTarget{ServiceID: "s1", Engine: "postgres", Version: "17", Database: "app", User: "postgres"},
		Destination: agentprotocol.BackupDestination{Kind: agentprotocol.DestLocal},
		ObjectKey:   "p/e/db/20261026-b1.dump",
	}
}

var secretsPG = map[string]string{agentprotocol.SecretDBPassword: "s3cr3t-db-pass"}

func TestBackupSuccessWritesObjectAndManifest(t *testing.T) {
	dump := bytes.Repeat([]byte("PGDMP-row\n"), 1000)
	ex := &fakeExec{stdout: dump}
	st := newMemStore()
	res, err := newRunner(ex, st).Backup(context.Background(), "c1", pgPayload(), secretsPG, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(dump)
	if res.Outcome != agentprotocol.OutcomeSucceeded || res.SizeBytes != int64(len(dump)) || res.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("result: %+v", res)
	}
	if !bytes.Equal(st.objs["p/e/db/20261026-b1.dump"], dump) {
		t.Fatal("stored object differs from the dump")
	}
	var man agentprotocol.BackupManifest
	if err := json.Unmarshal(st.objs["p/e/db/20261026-b1.dump.manifest.json"], &man); err != nil {
		t.Fatal(err)
	}
	if man.Engine != "postgres" || man.Format != agentprotocol.FormatPGCustom || man.SHA256 != res.SHA256 || man.SizeBytes != res.SizeBytes {
		t.Fatalf("manifest: %+v", man)
	}
	// Credentials travel in the exec env, never in argv.
	if strings.Contains(strings.Join(ex.gotArgv, " "), "s3cr3t-db-pass") {
		t.Fatalf("password leaked into argv: %v", ex.gotArgv)
	}
	if len(ex.gotEnv) != 1 || ex.gotEnv[0] != "PGPASSWORD=s3cr3t-db-pass" {
		t.Fatalf("env: %v", ex.gotEnv)
	}
}

// A dump that dies (non-zero exit) after partial output must never produce a
// complete object: the upload sees a read error, not EOF.
func TestFailedDumpNeverCompletesUpload(t *testing.T) {
	ex := &fakeExec{stdout: bytes.Repeat([]byte("x"), 4096), failMid: true, exit: 1, stderr: "pg_dump: error: connection to server failed for s3cr3t-db-pass"}
	st := newMemStore()
	res, err := newRunner(ex, st).Backup(context.Background(), "c1", pgPayload(), secretsPG, func(string) {})
	if err == nil || res.Outcome != agentprotocol.OutcomeFailed {
		t.Fatalf("expected failure, got %+v %v", res, err)
	}
	if st.aborted != 1 {
		t.Fatalf("upload must be aborted, aborted=%d", st.aborted)
	}
	if len(st.keys()) != 0 {
		t.Fatalf("no object or manifest may exist after a failed dump: %v", st.keys())
	}
	if !strings.Contains(res.Message, "exited with status 1") || !strings.Contains(res.Message, "connection to server failed") {
		t.Fatalf("message lacks the dump error: %q", res.Message)
	}
	if strings.Contains(res.Message, "s3cr3t-db-pass") {
		t.Fatalf("secret leaked in message: %q", res.Message)
	}
}

func TestExecTransportErrorFailsBackup(t *testing.T) {
	ex := &fakeExec{stdout: []byte("abc"), err: errors.New("docker daemon gone")}
	st := newMemStore()
	_, err := newRunner(ex, st).Backup(context.Background(), "c1", pgPayload(), secretsPG, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "docker daemon gone") {
		t.Fatalf("err: %v", err)
	}
	if len(st.keys()) != 0 {
		t.Fatalf("objects left: %v", st.keys())
	}
}

func TestEmptyDumpFails(t *testing.T) {
	st := newMemStore()
	_, err := newRunner(&fakeExec{}, st).Backup(context.Background(), "c1", pgPayload(), secretsPG, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "no output") {
		t.Fatalf("err: %v", err)
	}
	if len(st.keys()) != 0 {
		t.Fatalf("objects left: %v", st.keys())
	}
}

func TestMySQLDumpIsGzipped(t *testing.T) {
	sql := []byte("CREATE TABLE t (id int);\nINSERT INTO t VALUES (1);\n")
	ex := &fakeExec{stdout: sql}
	st := newMemStore()
	p := pgPayload()
	p.Target = agentprotocol.BackupTarget{ServiceID: "s1", Engine: "mysql", Database: "app", User: "root"}
	res, err := newRunner(ex, st).Backup(context.Background(), "c1", p, map[string]string{agentprotocol.SecretDBPassword: "rootpw1"}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != agentprotocol.FormatMySQLGz {
		t.Fatalf("format %s", res.Format)
	}
	gz, err := gzip.NewReader(bytes.NewReader(st.objs[p.ObjectKey]))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(gz)
	if !bytes.Equal(got, sql) {
		t.Fatalf("gunzipped %q", got)
	}
	if ex.gotArgv[0] != "mysqldump" || ex.gotEnv[0] != "MYSQL_PWD=rootpw1" {
		t.Fatalf("argv=%v env=%v", ex.gotArgv, ex.gotEnv)
	}
}

func TestRejectsBadIdentifiersAndKeys(t *testing.T) {
	st := newMemStore()
	for _, mut := range []func(*agentprotocol.DBBackupPayload){
		func(p *agentprotocol.DBBackupPayload) { p.Target.Database = "-o evil" },
		func(p *agentprotocol.DBBackupPayload) { p.Target.User = "a b" },
		func(p *agentprotocol.DBBackupPayload) { p.ObjectKey = "../escape" },
		func(p *agentprotocol.DBBackupPayload) { p.ObjectKey = "/abs" },
		func(p *agentprotocol.DBBackupPayload) { p.Target.Engine = "clickhouse" },
	} {
		ex := &fakeExec{stdout: []byte("x")}
		p := pgPayload()
		mut(&p)
		if _, err := newRunner(ex, st).Backup(context.Background(), "c1", p, secretsPG, func(string) {}); err == nil {
			t.Errorf("payload %+v should be rejected", p)
		}
		if ex.calls != 0 {
			t.Errorf("nothing may be executed for a rejected payload")
		}
	}
}

// A PostgreSQL restore, in either mode, goes into an empty scratch database made
// from template0 and replaces the target afterwards: the target of a PostGIS
// image already holds the extensions and schemas (tiger, topology) the dump
// creates, so restoring into it fails with `schema "tiger" already exists`.
func TestRestorePostgresUsesScratchDatabaseInBothModes(t *testing.T) {
	dump := bytes.Repeat([]byte("D"), 2048)
	sum := sha256.Sum256(dump)
	for _, clean := range []bool{false, true} {
		st := newMemStore()
		st.objs["k.dump"] = dump
		ex := &scriptExec{answer: existsAnswer}
		p := agentprotocol.DBRestorePayload{
			RestoreID: "0a1b2c3d-1111-2222-3333-444455556666",
			Target:    agentprotocol.BackupTarget{Engine: "postgres", Database: "app", User: "postgres"},
			ObjectKey: "k.dump", Format: agentprotocol.FormatPGCustom, SHA256: hex.EncodeToString(sum[:]), Clean: clean,
		}
		res, err := scriptRunner(ex, st).Restore(context.Background(), "c2", p, secretsPG, func(string) {})
		if err != nil || res.Outcome != agentprotocol.OutcomeSucceeded {
			t.Fatalf("clean=%v: %+v %v", clean, res, err)
		}
		pr := ex.tool("pg_restore")
		if pr == nil {
			t.Fatalf("clean=%v: pg_restore did not run", clean)
		}
		if !bytes.Equal(pr.stdin, dump) {
			t.Fatal("stdin differs from the stored dump")
		}
		want := []string{"pg_restore", "--no-owner", "--no-acl", "--exit-on-error", "--username", "postgres", "--dbname", "nc_restore_0a1b2c3d1111"}
		if strings.Join(pr.argv, " ") != strings.Join(want, " ") {
			t.Fatalf("clean=%v: argv %v", clean, pr.argv)
		}
		if strings.Join(pr.env, " ") != "PGPASSWORD=s3cr3t-db-pass" {
			t.Fatalf("the password travels in the environment: %v", pr.env)
		}
		wantStmts := []string{
			`DROP DATABASE IF EXISTS "nc_restore_0a1b2c3d1111" WITH (FORCE)`,
			`CREATE DATABASE "nc_restore_0a1b2c3d1111" TEMPLATE template0 OWNER "postgres"`,
			`ANALYZE`,
			`SELECT 1 FROM pg_database WHERE datname = 'app'`,
			`ALTER DATABASE "app" WITH ALLOW_CONNECTIONS false`,
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = 'app' AND pid <> pg_backend_pid()`,
			`ALTER DATABASE "app" RENAME TO "nc_replaced_0a1b2c3d1111"`,
			`ALTER DATABASE "nc_restore_0a1b2c3d1111" RENAME TO "app"`,
			`DROP DATABASE IF EXISTS "nc_replaced_0a1b2c3d1111" WITH (FORCE)`,
		}
		if got := ex.statements(); strings.Join(got, "\n") != strings.Join(wantStmts, "\n") {
			t.Fatalf("clean=%v: statements\n%s", clean, strings.Join(got, "\n"))
		}
	}
}

// When the target database does not exist yet the scratch database is renamed into place.
func TestRestorePostgresMissingTargetIsRenamedInto(t *testing.T) {
	st := newMemStore()
	st.objs["k.dump"] = []byte("PGDMP")
	ex := &scriptExec{}
	p := agentprotocol.DBRestorePayload{
		RestoreID: "0a1b2c3d-1111", Target: agentprotocol.BackupTarget{Engine: "postgres", Database: "app", User: "postgres"},
		ObjectKey: "k.dump", Format: agentprotocol.FormatPGCustom,
	}
	if _, err := scriptRunner(ex, st).Restore(context.Background(), "c", p, secretsPG, func(string) {}); err != nil {
		t.Fatal(err)
	}
	stmts := ex.statements()
	if last := stmts[len(stmts)-1]; last != `ALTER DATABASE "nc_restore_0a1b2c3d1111" RENAME TO "app"` {
		t.Fatalf("last statement %q", last)
	}
}

// A failed pg_restore, or a checksum that does not match, drops the scratch
// database and never touches the target.
func TestRestorePostgresFailureLeavesTargetUntouched(t *testing.T) {
	cases := map[string]struct {
		answer func([]string, []byte) (int, string, string, error)
		sha    string
		want   string
	}{
		"pg_restore fails": {
			answer: func(argv []string, in []byte) (int, string, string, error) {
				if argv[0] == "pg_restore" {
					return 1, "", `pg_restore: error: could not execute query: ERROR: relation "x" already exists with s3cr3t-db-pass`, nil
				}
				return existsAnswer(argv, in)
			},
			want: "already exists",
		},
		"checksum mismatch": {answer: existsAnswer, sha: strings.Repeat("0", 64), want: "checksum mismatch"},
	}
	for name, c := range cases {
		st := newMemStore()
		st.objs["k.dump"] = []byte("PGDMP")
		ex := &scriptExec{answer: c.answer}
		p := agentprotocol.DBRestorePayload{
			RestoreID: "0a1b2c3d-1111", Target: agentprotocol.BackupTarget{Engine: "postgres", Database: "app", User: "postgres"},
			ObjectKey: "k.dump", Format: agentprotocol.FormatPGCustom, SHA256: c.sha,
		}
		_, err := scriptRunner(ex, st).Restore(context.Background(), "c", p, secretsPG, func(string) {})
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "s3cr3t-db-pass") {
			t.Fatalf("%s: err %v", name, err)
		}
		stmts := ex.statements()
		if last := stmts[len(stmts)-1]; last != `DROP DATABASE IF EXISTS "nc_restore_0a1b2c3d1111" WITH (FORCE)` {
			t.Fatalf("%s: the scratch database is dropped last, got %q", name, last)
		}
		for _, s := range stmts {
			if strings.Contains(s, "RENAME") || strings.Contains(s, "ALLOW_CONNECTIONS") {
				t.Fatalf("%s: the target must not be touched: %q", name, s)
			}
		}
	}
}

// The scratch database could not be created: nothing was restored or changed.
func TestRestorePostgresPrepareFailure(t *testing.T) {
	st := newMemStore()
	st.objs["k.dump"] = []byte("PGDMP")
	ex := &scriptExec{answer: func([]string, []byte) (int, string, string, error) {
		return 1, "", "ERROR: permission denied to create database", nil
	}}
	p := agentprotocol.DBRestorePayload{
		RestoreID: "r", Target: agentprotocol.BackupTarget{Engine: "postgres", Database: "app", User: "postgres"},
		ObjectKey: "k.dump", Format: agentprotocol.FormatPGCustom,
	}
	_, err := scriptRunner(ex, st).Restore(context.Background(), "c", p, secretsPG, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "could not prepare the restore") || ex.tool("pg_restore") != nil {
		t.Fatalf("err %v", err)
	}
}

func TestRestoreMongoCleanDrops(t *testing.T) {
	st := newMemStore()
	st.objs["k"] = []byte("x")
	ex := &fakeExec{}
	p := agentprotocol.DBRestorePayload{Target: agentprotocol.BackupTarget{Engine: "mongodb", User: "u"}, ObjectKey: "k", Format: agentprotocol.FormatMongoArchive}
	if _, err := newRunner(ex, st).Restore(context.Background(), "c", p, secretsPG, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(ex.gotArgv, " "), "--drop") {
		t.Fatalf("argv %v", ex.gotArgv)
	}
	p.Clean = true
	if _, err := newRunner(ex, st).Restore(context.Background(), "c", p, secretsPG, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(ex.gotArgv, " "), "--drop") {
		t.Fatalf("in-place restore must drop: %v", ex.gotArgv)
	}
}

func TestRestoreGunzipsMySQLAndVerifiesChecksum(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte("SELECT 1;"))
	_ = gz.Close()
	st := newMemStore()
	st.objs["k.sql.gz"] = buf.Bytes()
	ex := &fakeExec{}
	p := agentprotocol.DBRestorePayload{
		Target:    agentprotocol.BackupTarget{Engine: "mariadb", Database: "app", User: "root"},
		ObjectKey: "k.sql.gz", Format: agentprotocol.FormatMySQLGz, SHA256: strings.Repeat("0", 64),
	}
	_, err := newRunner(ex, st).Restore(context.Background(), "c", p, nil, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum mismatch, got %v", err)
	}
	if string(ex.gotStdin) != "SELECT 1;" || ex.gotArgv[0] != "mariadb" {
		t.Fatalf("stdin=%q argv=%v", ex.gotStdin, ex.gotArgv)
	}
	p.SHA256 = ""
	if _, err := newRunner(ex, st).Restore(context.Background(), "c", p, nil, func(string) {}); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreFailureAndFormatMismatch(t *testing.T) {
	st := newMemStore()
	st.objs["k"] = []byte("x")
	ex := &fakeExec{exit: 3, stderr: "mongorestore: error: boom"}
	p := agentprotocol.DBRestorePayload{Target: agentprotocol.BackupTarget{Engine: "mongodb", User: "u"}, ObjectKey: "k", Format: agentprotocol.FormatMongoArchive}
	if _, err := newRunner(ex, st).Restore(context.Background(), "c", p, nil, func(string) {}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err %v", err)
	}
	p.Target.Engine = "postgres"
	p.Format = agentprotocol.FormatMySQLGz
	if _, err := newRunner(&fakeExec{}, st).Restore(context.Background(), "c", p, nil, func(string) {}); err == nil {
		t.Fatal("format mismatch must fail")
	}
	p.ObjectKey = "missing"
	p.Format = agentprotocol.FormatPGCustom
	if _, err := newRunner(&fakeExec{}, st).Restore(context.Background(), "c", p, nil, func(string) {}); err == nil {
		t.Fatal("missing object must fail")
	}
}

func TestDeleteAndTest(t *testing.T) {
	st := newMemStore()
	st.objs["a"], st.objs["a.manifest.json"], st.objs["b"] = []byte("1"), []byte("2"), []byte("3")
	r := newRunner(&fakeExec{}, st)
	if _, err := r.Delete(context.Background(), agentprotocol.BackupDeletePayload{ObjectKeys: []string{"a", "missing"}}, nil); err != nil {
		t.Fatal(err)
	}
	if ks := st.keys(); len(ks) != 1 || ks[0] != "b" {
		t.Fatalf("keys %v", ks)
	}
	res, err := r.Test(context.Background(), agentprotocol.BackupTestPayload{Prefix: "pre"}, nil)
	if err != nil || res.Outcome != agentprotocol.OutcomeSucceeded {
		t.Fatalf("test: %+v %v", res, err)
	}
	if ks := st.keys(); len(ks) != 1 {
		t.Fatalf("probe object must be removed: %v", ks)
	}
}

type errReader struct{ n int }

func (e *errReader) Read(p []byte) (int, error) {
	if e.n <= 0 {
		return 0, errors.New("boom")
	}
	n := min(len(p), e.n)
	for i := range n {
		p[i] = 'z'
	}
	e.n -= n
	return n, nil
}

func TestLocalStoreAtomicAndSafe(t *testing.T) {
	root := t.TempDir()
	st, err := NewLocalStore(root, "sub")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.Put(ctx, "a/b/obj", &errReader{n: 100}); err == nil {
		t.Fatal("expected read error")
	}
	// Nothing, not even a partial temp file, remains.
	var left []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, _ error) error {
		if !d.IsDir() {
			left = append(left, p)
		}
		return nil
	})
	if len(left) != 0 {
		t.Fatalf("files left after failed put: %v", left)
	}
	if err := st.Put(ctx, "a/b/obj", strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, "sub", "a", "b", "obj"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("stat %v %v", fi, err)
	}
	if n, err := st.Size(ctx, "a/b/obj"); err != nil || n != 5 {
		t.Fatalf("size %d %v", n, err)
	}
	if err := st.Delete(ctx, "a/b/obj"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Size(ctx, "a/b/obj"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "a")); err == nil {
		t.Fatal("empty directories should be pruned")
	}
	for _, bad := range []string{"../x", "/etc/passwd", "a/../../b", "a//b", ""} {
		if err := st.Put(ctx, bad, strings.NewReader("x")); err == nil {
			t.Errorf("key %q must be rejected", bad)
		}
	}
	if _, err := NewLocalStore(root, "../outside"); err == nil {
		t.Fatal("sub dir traversal must be rejected")
	}
	if _, err := NewLocalStore("", ""); err == nil {
		t.Fatal("empty root must be rejected")
	}
}
