package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// DumpRunner restores a dump file that arrives as a byte stream into the
// database in a container. It runs the engine's own client inside the
// container (so the tools match the server's version) and streams the file into
// its standard input: nothing is written to disk by the agent.
//
// PostgreSQL is restored into a scratch database created from template0 and
// swapped in only when the restore succeeded. A dump of a PostGIS database
// creates its extensions and their schemas itself, so it needs a database
// that has none (the image's own database already has them), and a failed or
// abandoned restore leaves the live database untouched.
type DumpRunner struct {
	Docker Execer
}

// DumpError is a failed dump restore. Before is true when it failed before
// the stream was consumed, so nothing was changed.
type DumpError struct {
	Before bool
	Code   string
	Msg    string
}

func (e *DumpError) Error() string { return e.Msg }

func preFail(code, format string, args ...any) *DumpError {
	return &DumpError{Before: true, Code: code, Msg: fmt.Sprintf(format, args...)}
}

func runFail(format string, args ...any) *DumpError {
	return &DumpError{Code: agentprotocol.DumpErrFailed, Msg: fmt.Sprintf(format, args...)}
}

// filterFailure words what stopped the statement filter: a dump it refused, or
// a source that could not be read to the end (the client would otherwise
// see a clean, shortened input).
func filterFailure(err error) string {
	if errors.Is(err, errUnsafeSQL) {
		return err.Error()
	}
	return "the dump could not be read: " + err.Error()
}

// lineSink splits written bytes into lines and keeps the last few for error
// messages.
type lineSink struct {
	mu   sync.Mutex
	buf  []byte
	logf func(string)
	tail []string
	size int
}

func (l *lineSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.line(string(l.buf[:i]))
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > 8192 { // a line without end: do not grow without bound
		l.line(string(l.buf))
		l.buf = nil
	}
	return len(p), nil
}

func (l *lineSink) line(s string) {
	s = strings.TrimRight(s, "\r")
	if s == "" {
		return
	}
	if l.logf != nil {
		l.logf(s)
	}
	l.tail = append(l.tail, s)
	l.size += len(s)
	for l.size > stderrMax && len(l.tail) > 1 {
		l.size -= len(l.tail[0])
		l.tail = l.tail[1:]
	}
}

func (l *lineSink) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buf) > 0 {
		l.line(string(l.buf))
		l.buf = nil
	}
}

func (l *lineSink) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.tail, "\n")
}

// dumpNames are the scratch and replaced database names of one restore.
type dumpNames struct{ scratch, old, maint string }

func namesFor(restoreID, database string) dumpNames {
	short := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'f' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, strings.ToLower(restoreID))
	if len(short) > 12 {
		short = short[:12]
	}
	n := dumpNames{scratch: "nc_restore_" + short, old: "nc_replaced_" + short, maint: "postgres"}
	if database == "postgres" {
		n.maint = "template1"
	}
	return n
}

// quoteIdent quotes a name that already passed checkIdent.
func quoteIdent(s string) string { return `"` + s + `"` }

func quoteLit(s string) string { return `'` + strings.ReplaceAll(s, `'`, `''`) + `'` }

// Run restores src into the database of req.Target in container. started is
// called once the target is ready and before the first byte is read from src.
// log receives progress lines (credentials are masked). The caller must cancel
// ctx when Run returns: a client that stopped early may leave a goroutine
// blocked in src.Read.
func (r *DumpRunner) Run(ctx context.Context, container string, req agentprotocol.DumpRestoreRequest, secrets map[string]string, src io.Reader, started func(), log func(string)) *DumpError {
	t := req.Target
	if !agentprotocol.DumpFormatSupported(t.Engine, req.Format) {
		return preFail(agentprotocol.DumpErrBadRequest, "a %s dump cannot be restored into %s", req.Format, t.Engine)
	}
	pw := secrets[agentprotocol.SecretDBPassword]
	logf := func(s string) { log(scrub(s, secrets)) }

	switch t.Engine {
	case "postgres":
		return r.runPostgres(ctx, container, req, pw, src, started, logf, secrets)
	case "mysql", "mariadb":
		return r.runMySQL(ctx, container, req, pw, src, started, logf, secrets)
	case "mongodb":
		return r.runMongo(ctx, container, req, pw, src, started, logf, secrets)
	}
	return preFail(agentprotocol.DumpErrBadRequest, "restoring dump files into %s is not supported", t.Engine)
}

// pipeline layers gunzip and the engine's statement filter over src.
func pipeline(req agentprotocol.DumpRestoreRequest, src io.Reader, filter func(io.Reader) *sqlFilter) (io.Reader, *sqlFilter, func(), error) {
	in := src
	closer := func() {}
	if req.Gzip {
		gz, err := gzip.NewReader(src)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("the file is not a valid gzip stream: %w", err)
		}
		in, closer = gz, func() { _ = gz.Close() }
	}
	var f *sqlFilter
	if filter != nil && req.Format == agentprotocol.DumpSQL {
		f = filter(in)
		in = f
	}
	return in, f, closer, nil
}

// exec runs argv with in as standard input and reports its output as progress.
func (r *DumpRunner) exec(ctx context.Context, container string, argv, env []string, in io.Reader, logf func(string), secrets map[string]string) (code int, tail string, err error) {
	out := &lineSink{logf: logf}
	errs := &lineSink{logf: logf}
	code, err = r.Docker.ContainerExec(ctx, container, argv, docker.ExecSpec{Env: env, Stdin: in}, out, errs)
	out.flush()
	errs.flush()
	tail = scrub(errs.String(), secrets)
	if tail == "" {
		tail = scrub(out.String(), secrets)
	}
	return code, tail, err
}

func (r *DumpRunner) runPostgres(ctx context.Context, container string, req agentprotocol.DumpRestoreRequest, pw string, src io.Reader, started func(), logf func(string), secrets map[string]string) *DumpError {
	t := req.Target
	if err := checkIdent("database", t.Database); err != nil {
		return preFail(agentprotocol.DumpErrBadRequest, "%v", err)
	}
	if err := checkIdent("user", t.User); err != nil {
		return preFail(agentprotocol.DumpErrBadRequest, "%v", err)
	}
	sc := newPGScratch(r.Docker, container, t.User, pw, t.Database, req.RestoreID, secrets)

	logf("preparing a scratch database " + sc.names.scratch + " (the live database " + t.Database + " is not touched until the restore succeeded)")
	if err := sc.prepare(ctx); err != nil {
		return preFail(agentprotocol.DumpErrFailed, "could not prepare the restore: %v", err)
	}

	started() // the file may now be sent; a gzip header is read from it next
	in, filt, closeIn, err := pipeline(req, src, newPGSQLFilter)
	if err != nil {
		sc.discard(ctx) // a failed or abandoned restore must not leave the scratch database behind
		return runFail("%v", err)
	}
	defer closeIn()

	var argv []string
	switch req.Format {
	case agentprotocol.DumpSQL:
		argv = []string{"psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "--username", t.User, "--dbname", sc.names.scratch}
	default:
		argv = []string{"pg_restore", "--no-owner", "--no-acl", "--role=" + t.User, "--exit-on-error", "--verbose", "--username", t.User, "--dbname", sc.names.scratch}
	}
	logf("restoring into the scratch database with " + argv[0])
	code, tail, err := r.exec(ctx, container, argv, []string{"PGPASSWORD=" + pw}, in, logf, secrets)
	if err != nil {
		sc.discard(ctx)
		if ctx.Err() != nil {
			return runFail("the restore was canceled")
		}
		return runFail("run %s: %v", argv[0], err)
	}
	if ferr := filt.Err(); ferr != nil {
		sc.discard(ctx)
		return runFail("%s", filterFailure(ferr))
	}
	if code != 0 {
		sc.discard(ctx)
		if tail == "" {
			tail = "no error output"
		}
		return runFail("%s exited with status %d: %s", argv[0], code, tail)
	}
	if filt != nil && filt.Dropped() > 0 {
		logf(fmt.Sprintf("skipped %d statements about roles, ownership, grants, other databases or client commands", filt.Dropped()))
	}
	logf("analyzing the restored data")
	if err := sc.analyze(ctx); err != nil {
		logf("ANALYZE failed (the restore is kept): " + err.Error())
	}

	logf("switching " + t.Database + " over to the restored data")
	if err := sc.switchOver(ctx, logf); err != nil {
		return runFail("%v", err)
	}
	logf("restored")
	return nil
}

func (r *DumpRunner) runMySQL(ctx context.Context, container string, req agentprotocol.DumpRestoreRequest, pw string, src io.Reader, started func(), logf func(string), secrets map[string]string) *DumpError {
	t := req.Target
	if err := checkIdent("database", t.Database); err != nil {
		return preFail(agentprotocol.DumpErrBadRequest, "%v", err)
	}
	if err := checkIdent("user", t.User); err != nil {
		return preFail(agentprotocol.DumpErrBadRequest, "%v", err)
	}
	tool := "mysql"
	if t.Engine == "mariadb" {
		tool = "mariadb"
	}
	started()
	in, filt, closeIn, err := pipeline(req, src, newMySQLFilter)
	if err != nil {
		return runFail("%v", err)
	}
	defer closeIn()
	logf("restoring into " + t.Database + " with " + tool)
	code, tail, err := r.exec(ctx, container, []string{tool, "--binary-mode", "-u", t.User, t.Database}, []string{"MYSQL_PWD=" + pw}, in, logf, secrets)
	if err != nil {
		if ctx.Err() != nil {
			return runFail("the restore was canceled; the database may hold part of the dump")
		}
		return runFail("run %s: %v", tool, err)
	}
	if ferr := filt.Err(); ferr != nil {
		return runFail("%s (the database may hold part of the dump)", filterFailure(ferr))
	}
	if code != 0 {
		if tail == "" {
			tail = "no error output"
		}
		return runFail("%s exited with status %d: %s (the database may hold part of the dump)", tool, code, tail)
	}
	if filt != nil && filt.Dropped() > 0 {
		logf(fmt.Sprintf("skipped %d statements that create or switch databases or run client commands", filt.Dropped()))
	}
	logf("restored")
	return nil
}

func (r *DumpRunner) runMongo(ctx context.Context, container string, req agentprotocol.DumpRestoreRequest, pw string, src io.Reader, started func(), logf func(string), secrets map[string]string) *DumpError {
	t := req.Target
	if err := checkIdent("user", t.User); err != nil {
		return preFail(agentprotocol.DumpErrBadRequest, "%v", err)
	}
	started()
	in, _, closeIn, err := pipeline(req, src, nil)
	if err != nil {
		return runFail("%v", err)
	}
	defer closeIn()
	argv := []string{"mongorestore", "--archive", "--uri", mongoURI(t.User, pw)}
	if req.Replace {
		argv = append(argv, "--drop")
	}
	logf("restoring with mongorestore")
	code, tail, err := r.exec(ctx, container, argv, nil, in, logf, secrets)
	if err != nil {
		if ctx.Err() != nil {
			return runFail("the restore was canceled; the database may hold part of the dump")
		}
		return runFail("run mongorestore: %v", err)
	}
	if code != 0 {
		if tail == "" {
			tail = "no error output"
		}
		return runFail("mongorestore exited with status %d: %s (the database may hold part of the dump)", code, tail)
	}
	logf("restored")
	return nil
}
