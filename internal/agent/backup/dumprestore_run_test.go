package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// tolerantExec behaves like a database client that treats a failing standard
// input as the end of its input: it exits 0 after a truncated read.
type tolerantExec struct{ *scriptExec }

func (t tolerantExec) ContainerExec(ctx context.Context, c string, argv []string, spec docker.ExecSpec, stdout, stderr io.Writer) (int, error) {
	if spec.Stdin != nil {
		spec.Stdin = &swallowErr{spec.Stdin}
	}
	return t.scriptExec.ContainerExec(ctx, c, argv, spec, stdout, stderr)
}

type swallowErr struct{ r io.Reader }

func (s *swallowErr) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, io.EOF
	}
	return n, err
}

type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) log(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, s)
}

func (l *logLines) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func myReq(engine, format string, gz bool) agentprotocol.DumpRestoreRequest {
	return agentprotocol.DumpRestoreRequest{
		RequestID: "r", RestoreID: "abc", Size: 5, Format: format, Gzip: gz,
		Target: agentprotocol.BackupTarget{Engine: engine, Database: "app", User: "root"},
	}
}

func TestDumpErrorMessage(t *testing.T) {
	var err error = &DumpError{Code: agentprotocol.DumpErrFailed, Msg: "boom"}
	require.EqualError(t, err, "boom")

	pre := preFail(agentprotocol.DumpErrBadRequest, "bad %s %d", "x", 3)
	require.True(t, pre.Before)
	require.Equal(t, agentprotocol.DumpErrBadRequest, pre.Code)
	require.Equal(t, "bad x 3", pre.Msg)

	run := runFail("failed %s", "y")
	require.False(t, run.Before, "a failure while the stream is consumed is not a no-change failure")
	require.Equal(t, agentprotocol.DumpErrFailed, run.Code)
	require.Equal(t, "failed y", run.Msg)
}

func TestLineSinkSplitsAndKeepsTail(t *testing.T) {
	var got []string
	s := &lineSink{logf: func(l string) { got = append(got, l) }}
	_, _ = s.Write([]byte("one\r\ntw"))
	_, _ = s.Write([]byte("o\n\n\nthree"))
	require.Equal(t, []string{"one", "two"}, got, "lines are reported as they complete, blank lines are skipped, CR is trimmed")
	s.flush()
	require.Equal(t, []string{"one", "two", "three"}, got, "flush reports the unterminated rest")
	require.Equal(t, "one\ntwo\nthree", s.String())
	s.flush()
	require.Len(t, got, 3, "a second flush has nothing left")
}

func TestLineSinkBoundsLongLinesAndTail(t *testing.T) {
	s := &lineSink{}
	long := strings.Repeat("a", 9000)
	n, err := s.Write([]byte(long))
	require.NoError(t, err)
	require.Equal(t, 9000, n)
	require.Equal(t, long, s.String(), "a line without an end is cut off at 8192+ bytes instead of buffering without bound")
	require.Empty(t, s.buf)

	// The tail keeps the most recent lines within stderrMax.
	s = &lineSink{}
	chunk := strings.Repeat("b", 1000)
	for i := range 6 {
		_, _ = s.Write([]byte(chunk + string(rune('0'+i)) + "\n"))
	}
	tail := s.String()
	require.LessOrEqual(t, len(tail), stderrMax+len(chunk)+1)
	require.True(t, strings.HasSuffix(tail, chunk+"5"), "newest line is kept")
	require.NotContains(t, tail, chunk+"0", "oldest lines fall off")
}

func TestDumpRunExecMasksSecretsInProgressAndError(t *testing.T) {
	ex := &scriptExec{answer: func(argv []string, _ []byte) (int, string, string, error) {
		if argv[0] == "mongorestore" {
			return 1, "", "auth failed for mongopw1\n", nil
		}
		return 0, "", "", nil
	}}
	var lg logLines
	req := myReq("mongodb", agentprotocol.DumpMongo, false)
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", req, map[string]string{agentprotocol.SecretDBPassword: "mongopw1"}, strings.NewReader("x"), func() {}, lg.log)
	require.NotNil(t, derr)
	require.False(t, derr.Before)
	require.Equal(t, agentprotocol.DumpErrFailed, derr.Code)
	require.Equal(t, "mongorestore exited with status 1: auth failed for •••••• (the database may hold part of the dump)", derr.Msg)
	require.NotContains(t, lg.joined(), "mongopw1", "progress lines never carry the secret")
	require.Contains(t, lg.joined(), "auth failed for ••••••")
}

func TestDumpRunPostgresRejectsUnsafeNamesBeforeAnyExec(t *testing.T) {
	for name, mutate := range map[string]func(*agentprotocol.BackupTarget){
		"database": func(t *agentprotocol.BackupTarget) { t.Database = "--evil" },
		"user":     func(t *agentprotocol.BackupTarget) { t.User = "a;b" },
	} {
		ex := &scriptExec{}
		req := pgReq(agentprotocol.DumpSQL, false)
		mutate(&req.Target)
		started := false
		derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", req, nil, strings.NewReader(""), func() { started = true }, func(string) {})
		require.NotNil(t, derr, name)
		require.True(t, derr.Before, name)
		require.Equal(t, agentprotocol.DumpErrBadRequest, derr.Code, name)
		require.Contains(t, derr.Msg, "invalid "+name, name)
		require.False(t, started, name)
		require.Empty(t, ex.calls, name)
	}
}

func TestDumpRunPostgresInvalidGzipDiscardsScratch(t *testing.T) {
	ex := &scriptExec{answer: existsAnswer}
	started := false
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", pgReq(agentprotocol.DumpSQL, true), nil, strings.NewReader("this is not gzip"), func() { started = true }, func(string) {})
	require.NotNil(t, derr)
	require.False(t, derr.Before, "the stream was already requested")
	require.True(t, started)
	require.Contains(t, derr.Msg, "the file is not a valid gzip stream")
	stmts := ex.statements()
	require.Equal(t, `DROP DATABASE IF EXISTS "nc_restore_0a1b2c3d1111" WITH (FORCE)`, stmts[len(stmts)-1], "the scratch database does not stay behind")
	require.Nil(t, ex.tool("psql"), "the client never ran")
}

func TestDumpRunPostgresExecFailureAndCancel(t *testing.T) {
	ex := &scriptExec{answer: func(argv []string, stdin []byte) (int, string, string, error) {
		if argv[0] == "psql" && contains(argv, "ON_ERROR_STOP=1") && !contains(argv, "-c") {
			return -1, "", "", errors.New("docker gone")
		}
		return existsAnswer(argv, stdin)
	}}
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", pgReq(agentprotocol.DumpSQL, false), nil, strings.NewReader("SELECT 1;\n"), func() {}, func(string) {})
	require.NotNil(t, derr)
	require.Equal(t, "run psql: docker gone", derr.Msg)
	stmts := ex.statements()
	require.Equal(t, `DROP DATABASE IF EXISTS "nc_restore_0a1b2c3d1111" WITH (FORCE)`, stmts[len(stmts)-1])

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ex = &scriptExec{answer: func(argv []string, stdin []byte) (int, string, string, error) {
		if argv[0] == "pg_restore" {
			return -1, "", "", context.Canceled
		}
		return existsAnswer(argv, stdin)
	}}
	derr = (&DumpRunner{Docker: ex}).Run(ctx, "c", pgReq(agentprotocol.DumpPGCustom, false), nil, strings.NewReader("PGDMP"), func() {}, func(string) {})
	require.NotNil(t, derr)
	require.Equal(t, "the restore was canceled", derr.Msg)
	stmts = ex.statements()
	require.Equal(t, `DROP DATABASE IF EXISTS "nc_restore_0a1b2c3d1111" WITH (FORCE)`, stmts[len(stmts)-1], "a canceled restore cleans up its scratch database")
	for _, s := range stmts {
		require.NotContains(t, s, "RENAME")
	}
}

func TestDumpRunPostgresRefusedDumpFailsRestore(t *testing.T) {
	ex := tolerantExec{&scriptExec{answer: existsAnswer}}
	// \! after a quote that the filter cannot drop (second chunk of a long line).
	in := "SELECT '" + strings.Repeat("x", 2*sqlFilterBuffer) + `' \! id` + "\n"
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", pgReq(agentprotocol.DumpSQL, false), nil, strings.NewReader(in), func() {}, func(string) {})
	require.NotNil(t, derr)
	require.False(t, derr.Before)
	require.Equal(t, errUnsafeSQL.Error(), derr.Msg)
	stmts := ex.statements()
	require.Equal(t, `DROP DATABASE IF EXISTS "nc_restore_0a1b2c3d1111" WITH (FORCE)`, stmts[len(stmts)-1])
	for _, s := range stmts {
		require.NotContains(t, s, "RENAME", "a refused dump never replaces the live database")
	}
}

func TestDumpRunPostgresTruncatedSourceFailsRestore(t *testing.T) {
	ex := tolerantExec{&scriptExec{answer: existsAnswer}}
	src := io.MultiReader(strings.NewReader("SELECT 1;\n"), &failingReader{errors.New("connection reset")})
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", pgReq(agentprotocol.DumpSQL, false), nil, src, func() {}, func(string) {})
	require.NotNil(t, derr)
	require.Equal(t, "the dump could not be read: connection reset", derr.Msg)
	for _, s := range ex.statements() {
		require.NotContains(t, s, "RENAME", "a shortened dump must not replace the live database")
	}
}

func TestDumpRunPostgresNonZeroExitWithoutOutput(t *testing.T) {
	ex := &scriptExec{answer: func(argv []string, stdin []byte) (int, string, string, error) {
		if argv[0] == "psql" && !contains(argv, "-c") {
			return 3, "", "", nil
		}
		return existsAnswer(argv, stdin)
	}}
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", pgReq(agentprotocol.DumpSQL, false), nil, strings.NewReader("SELECT 1;\n"), func() {}, func(string) {})
	require.NotNil(t, derr)
	require.Equal(t, "psql exited with status 3: no error output", derr.Msg)
}

func TestDumpRunPostgresLogsDroppedStatementsAndKeepsRestoreWhenAnalyzeFails(t *testing.T) {
	ex := &scriptExec{answer: func(argv []string, stdin []byte) (int, string, string, error) {
		if argv[0] == "psql" && contains(argv, "-c") && argv[len(argv)-1] == "ANALYZE" {
			return 1, "", "ANALYZE blew up", nil
		}
		return existsAnswer(argv, stdin)
	}}
	var lg logLines
	in := "CREATE TABLE t (id int);\nGRANT ALL ON t TO x;\nALTER TABLE t OWNER TO x;\n"
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", pgReq(agentprotocol.DumpSQL, false), secretsPW, strings.NewReader(in), func() {}, lg.log)
	require.Nil(t, derr, "a failed ANALYZE does not fail the restore")
	require.Equal(t, "CREATE TABLE t (id int);\n", string(ex.tool("psql").stdin))
	out := lg.joined()
	require.Contains(t, out, "skipped 2 statements about roles, ownership, grants, other databases or client commands")
	require.Contains(t, out, "ANALYZE failed (the restore is kept): ANALYZE blew up")
	require.Contains(t, out, "switching app over to the restored data")
	require.True(t, strings.HasSuffix(out, "restored"))
	require.Contains(t, ex.statements(), `ALTER DATABASE "nc_restore_0a1b2c3d1111" RENAME TO "app"`)
}

func TestDumpRunPostgresSwitchOverFailureIsReported(t *testing.T) {
	ex := &scriptExec{answer: func(argv []string, stdin []byte) (int, string, string, error) {
		if argv[0] == "psql" && strings.Contains(strings.Join(argv, " "), "SELECT 1 FROM pg_database") {
			return 1, "", "connection refused", nil
		}
		return existsAnswer(argv, stdin)
	}}
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", pgReq(agentprotocol.DumpPGCustom, false), nil, strings.NewReader("PGDMP"), func() {}, func(string) {})
	require.NotNil(t, derr)
	require.False(t, derr.Before)
	require.Equal(t, "could not check the live database: connection refused", derr.Msg)
	stmts := ex.statements()
	require.Equal(t, `DROP DATABASE IF EXISTS "nc_restore_0a1b2c3d1111" WITH (FORCE)`, stmts[len(stmts)-1])
}

func TestDumpRunMySQLRejectsUnsafeNames(t *testing.T) {
	for name, mutate := range map[string]func(*agentprotocol.BackupTarget){
		"database": func(t *agentprotocol.BackupTarget) { t.Database = "a b" },
		"user":     func(t *agentprotocol.BackupTarget) { t.User = "" },
	} {
		ex := &scriptExec{}
		req := myReq("mysql", agentprotocol.DumpSQL, false)
		mutate(&req.Target)
		derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", req, nil, strings.NewReader(""), func() { t.Fatal("started") }, func(string) {})
		require.NotNil(t, derr, name)
		require.True(t, derr.Before, name)
		require.Equal(t, agentprotocol.DumpErrBadRequest, derr.Code, name)
		require.Contains(t, derr.Msg, "invalid "+name, name)
		require.Empty(t, ex.calls, name)
	}
}

func TestDumpRunMySQLFailures(t *testing.T) {
	// not a gzip stream
	ex := &scriptExec{}
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", myReq("mysql", agentprotocol.DumpSQL, true), nil, strings.NewReader("plain"), func() {}, func(string) {})
	require.NotNil(t, derr)
	require.False(t, derr.Before)
	require.Contains(t, derr.Msg, "the file is not a valid gzip stream")
	require.Empty(t, ex.calls)

	// canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ex = &scriptExec{answer: func([]string, []byte) (int, string, string, error) { return -1, "", "", context.Canceled }}
	derr = (&DumpRunner{Docker: ex}).Run(ctx, "c", myReq("mysql", agentprotocol.DumpSQL, false), nil, strings.NewReader("SELECT 1;\n"), func() {}, func(string) {})
	require.NotNil(t, derr)
	require.Equal(t, "the restore was canceled; the database may hold part of the dump", derr.Msg)

	// refused dump
	tol := tolerantExec{&scriptExec{}}
	in := "SELECT '" + strings.Repeat("x", 2*sqlFilterBuffer) + `' \! id` + "\n"
	derr = (&DumpRunner{Docker: tol}).Run(context.Background(), "c", myReq("mysql", agentprotocol.DumpSQL, false), nil, strings.NewReader(in), func() {}, func(string) {})
	require.NotNil(t, derr)
	require.Equal(t, errUnsafeSQL.Error()+" (the database may hold part of the dump)", derr.Msg)

	// truncated source
	src := io.MultiReader(strings.NewReader("SELECT 1;\n"), &failingReader{errors.New("eof early")})
	derr = (&DumpRunner{Docker: tolerantExec{&scriptExec{}}}).Run(context.Background(), "c", myReq("mysql", agentprotocol.DumpSQL, false), nil, src, func() {}, func(string) {})
	require.NotNil(t, derr)
	require.Equal(t, "the dump could not be read: eof early (the database may hold part of the dump)", derr.Msg)

	// non-zero exit without output
	ex = &scriptExec{answer: func([]string, []byte) (int, string, string, error) { return 2, "", "", nil }}
	derr = (&DumpRunner{Docker: ex}).Run(context.Background(), "c", myReq("mariadb", agentprotocol.DumpSQL, false), nil, strings.NewReader("SELECT 1;\n"), func() {}, func(string) {})
	require.NotNil(t, derr)
	require.Equal(t, "mariadb exited with status 2: no error output (the database may hold part of the dump)", derr.Msg)
}

func TestDumpRunMySQLGzipAndLogsDroppedStatements(t *testing.T) {
	zr := gzipBytes(t, "USE `x`;\nCREATE TABLE t (a int);\nCREATE DATABASE y;\n")
	ex := &scriptExec{}
	var lg logLines
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", myReq("mysql", agentprotocol.DumpSQL, true), nil, strings.NewReader(string(zr)), func() {}, lg.log)
	require.Nil(t, derr)
	require.Equal(t, "CREATE TABLE t (a int);\n", string(ex.calls[0].stdin))
	require.Equal(t, []string{"mysql", "--binary-mode", "-u", "root", "app"}, ex.calls[0].argv)
	out := lg.joined()
	require.Contains(t, out, "restoring into app with mysql")
	require.Contains(t, out, "skipped 2 statements that create or switch databases or run client commands")
	require.True(t, strings.HasSuffix(out, "restored"))
}

func TestDumpRunMongoFailures(t *testing.T) {
	req := myReq("mongodb", agentprotocol.DumpMongo, false)

	// unsafe user
	bad := req
	bad.Target.User = "a/b"
	ex := &scriptExec{}
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", bad, nil, strings.NewReader(""), func() { t.Fatal("started") }, func(string) {})
	require.NotNil(t, derr)
	require.True(t, derr.Before)
	require.Equal(t, agentprotocol.DumpErrBadRequest, derr.Code)
	require.Contains(t, derr.Msg, `invalid user "a/b"`)
	require.Empty(t, ex.calls)

	// not a gzip stream
	gzReq := req
	gzReq.Gzip = true
	derr = (&DumpRunner{Docker: ex}).Run(context.Background(), "c", gzReq, nil, strings.NewReader("plain"), func() {}, func(string) {})
	require.NotNil(t, derr)
	require.False(t, derr.Before)
	require.Contains(t, derr.Msg, "the file is not a valid gzip stream")
	require.Empty(t, ex.calls)

	// transport error
	ex = &scriptExec{answer: func([]string, []byte) (int, string, string, error) { return -1, "", "", errors.New("docker gone") }}
	derr = (&DumpRunner{Docker: ex}).Run(context.Background(), "c", req, nil, strings.NewReader("x"), func() {}, func(string) {})
	require.NotNil(t, derr)
	require.Equal(t, "run mongorestore: docker gone", derr.Msg)

	// canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	derr = (&DumpRunner{Docker: ex}).Run(ctx, "c", req, nil, strings.NewReader("x"), func() {}, func(string) {})
	require.NotNil(t, derr)
	require.Equal(t, "the restore was canceled; the database may hold part of the dump", derr.Msg)

	// non-zero exit without output
	ex = &scriptExec{answer: func([]string, []byte) (int, string, string, error) { return 4, "", "", nil }}
	derr = (&DumpRunner{Docker: ex}).Run(context.Background(), "c", req, nil, strings.NewReader("x"), func() {}, func(string) {})
	require.NotNil(t, derr)
	require.Equal(t, "mongorestore exited with status 4: no error output (the database may hold part of the dump)", derr.Msg)

	// success: gzip layer removed, --drop only with Replace
	ex = &scriptExec{}
	gzReq.Replace = false
	derr = (&DumpRunner{Docker: ex}).Run(context.Background(), "c", gzReq, nil, strings.NewReader(string(gzipBytes(t, "archive"))), func() {}, func(string) {})
	require.Nil(t, derr)
	require.Equal(t, "archive", string(ex.calls[0].stdin))
	require.NotContains(t, ex.calls[0].argv, "--drop")
	require.Nil(t, ex.calls[0].env, "mongo credentials are in the URI, not the environment")
}

func TestDumpRunPipelineStopsAtStartedBeforeReadingSource(t *testing.T) {
	ex := &scriptExec{}
	var order []string
	src := &readRecorder{r: strings.NewReader("SELECT 1;\n"), onRead: func() { order = append(order, "read") }}
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", myReq("mysql", agentprotocol.DumpSQL, false), nil, src, func() { order = append(order, "started") }, func(string) {})
	require.Nil(t, derr)
	require.Equal(t, "started", order[0], "the target is ready before the first byte is read")
}

type readRecorder struct {
	r      io.Reader
	onRead func()
}

func (r *readRecorder) Read(p []byte) (int, error) {
	r.onRead()
	return r.r.Read(p)
}

func gzipBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write([]byte(s))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}
