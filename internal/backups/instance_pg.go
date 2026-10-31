package backups

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// PGConn holds the libpq connection parameters of a database. pg_dump and
// pg_restore receive them through the environment (PGHOST, PGPASSWORD, ...), so
// the password never appears in a process list.
type PGConn struct {
	Host     string // host name, IP or socket directory
	Port     string
	User     string
	Password string
	Database string
	// Extra holds the other libpq environment settings (PGSSLMODE, ...).
	Extra map[string]string
}

// urlParamEnv maps connection-URL parameters to libpq environment variables.
var urlParamEnv = map[string]string{
	"sslmode":         "PGSSLMODE",
	"sslrootcert":     "PGSSLROOTCERT",
	"sslcert":         "PGSSLCERT",
	"sslkey":          "PGSSLKEY",
	"connect_timeout": "PGCONNECT_TIMEOUT",
}

// ParsePGURL parses a postgres:// or postgresql:// connection URL.
func ParsePGURL(raw string) (PGConn, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return PGConn{}, errors.New("the database URL must look like postgres://user:password@host:5432/database")
	}
	c := PGConn{Host: u.Hostname(), Port: u.Port(), User: u.User.Username(), Database: strings.TrimPrefix(u.Path, "/"), Extra: map[string]string{}}
	c.Password, _ = u.User.Password()
	q := u.Query()
	if h := q.Get("host"); h != "" && c.Host == "" {
		c.Host = h // unix socket directory: postgres:///db?host=/var/run/postgresql
	}
	if c.Database == "" {
		return PGConn{}, errors.New("the database URL names no database")
	}
	for param, env := range urlParamEnv {
		if v := q.Get(param); v != "" {
			c.Extra[env] = v
		}
	}
	return c, nil
}

// WithDatabase returns c pointed at another database of the same server.
func (c PGConn) WithDatabase(name string) PGConn {
	c.Database = name
	return c
}

// Env returns the libpq environment for c.
func (c PGConn) Env() []string {
	var env []string
	add := func(k, v string) {
		if v != "" {
			env = append(env, k+"="+v)
		}
	}
	add("PGHOST", c.Host)
	add("PGPORT", c.Port)
	add("PGUSER", c.User)
	add("PGPASSWORD", c.Password)
	add("PGDATABASE", c.Database)
	for k, v := range c.Extra {
		add(k, v)
	}
	return env
}

// Cmd is one external tool invocation.
type Cmd struct {
	Name   string
	Args   []string
	Env    []string // added to a minimal base environment (PATH only)
	Stdin  io.Reader
	Stdout io.Writer
}

// Runner executes a Cmd and returns an error carrying the tool's stderr.
type Runner func(ctx context.Context, c Cmd) error

// stderrTail keeps the last bytes written to it.
type stderrTail struct {
	buf []byte
}

const stderrKeep = 4096

func (t *stderrTail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > stderrKeep {
		t.buf = t.buf[len(t.buf)-stderrKeep:]
	}
	return len(p), nil
}

func (t *stderrTail) String() string { return strings.TrimSpace(string(bytes.ToValidUTF8(t.buf, nil))) }

// execRunner runs the tool as a local process with a minimal environment: the
// control plane's own secrets (NEBULACTRL_SECRET_KEY) are not passed on.
func execRunner(ctx context.Context, c Cmd) error {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}, c.Env...)
	cmd.Stdin, cmd.Stdout = c.Stdin, c.Stdout
	var tail stderrTail
	cmd.Stderr = &tail
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("%s was not found: install the PostgreSQL client tools (the control plane image includes them)", c.Name)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if msg := tail.String(); msg != "" {
			return fmt.Errorf("%s: %w: %s", c.Name, err, msg)
		}
		return fmt.Errorf("%s: %w", c.Name, err)
	}
	return nil
}

// PGTools runs pg_dump and pg_restore.
type PGTools struct {
	Run     Runner
	Dump    string       // binary, default pg_dump
	Restore string       // binary, default pg_restore
	Check   func() error // reports whether the tools can run; nil = look both up in PATH
}

// DefaultPGTools runs pg_dump and pg_restore from PATH.
func DefaultPGTools() PGTools {
	return PGTools{Run: execRunner, Dump: "pg_dump", Restore: "pg_restore"}
}

func (t PGTools) run() Runner {
	if t.Run != nil {
		return t.Run
	}
	return execRunner
}

func (t PGTools) dump() string {
	if t.Dump != "" {
		return t.Dump
	}
	return "pg_dump"
}

func (t PGTools) restore() string {
	if t.Restore != "" {
		return t.Restore
	}
	return "pg_restore"
}

// Available reports why the tools can not run, or nil.
func (t PGTools) Available() error {
	if t.Check != nil {
		return t.Check()
	}
	for _, bin := range []string{t.dump(), t.restore()} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s is not installed on the control plane host (the control plane image includes the PostgreSQL client tools)", bin)
		}
	}
	return nil
}

// DumpArgs are the pg_dump arguments of a control-plane backup: the custom
// format (compressed, restorable selectively), no ownership or grants (so it
// restores under any role), and no session rows: a restore signs everyone out.
func DumpArgs() []string {
	return []string{"--format=custom", "--no-owner", "--no-acl", "--exclude-table-data=sessions"}
}

// RestoreArgs are the pg_restore arguments of a restore into the empty database
// named database: one transaction, so a failed restore leaves it empty. The
// server, user and password come from the environment (PGHOST, ...).
func RestoreArgs(database string) []string {
	return []string{"--no-owner", "--no-acl", "--exit-on-error", "--single-transaction", "--dbname=" + database}
}

// DumpTo streams a custom-format dump of conn's database to w.
func (t PGTools) DumpTo(ctx context.Context, conn PGConn, w io.Writer) error {
	return t.run()(ctx, Cmd{Name: t.dump(), Args: DumpArgs(), Env: conn.Env(), Stdout: w})
}

// RestoreFrom loads a custom-format dump read from r into conn's (empty) database.
func (t PGTools) RestoreFrom(ctx context.Context, conn PGConn, r io.Reader) error {
	return t.run()(ctx, Cmd{Name: t.restore(), Args: RestoreArgs(conn.Database), Env: conn.Env(), Stdin: r, Stdout: io.Discard})
}
