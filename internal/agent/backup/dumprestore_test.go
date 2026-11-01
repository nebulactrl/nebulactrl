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

func filterAll(t *testing.T, f *sqlFilter) string {
	t.Helper()
	b, err := io.ReadAll(f)
	require.NoError(t, err)
	return string(b)
}

func TestPGSQLFilterDropsRolesGrantsAndDatabases(t *testing.T) {
	in := strings.Join([]string{
		"-- PostgreSQL database dump",
		"CREATE DATABASE app WITH TEMPLATE = template0 ENCODING = 'UTF8';",
		"ALTER DATABASE app OWNER TO postgres;",
		`\connect app`,
		"DROP DATABASE IF EXISTS app;",
		"SET SESSION AUTHORIZATION 'someone';",
		"CREATE EXTENSION IF NOT EXISTS postgis WITH SCHEMA public;",
		"CREATE TABLE public.places (id integer, name text);",
		"ALTER TABLE public.places OWNER TO appuser;",
		"alter table only public.places owner to appuser;",
		"GRANT SELECT ON TABLE public.places TO readonly;",
		"REVOKE ALL ON TABLE public.places FROM PUBLIC;",
		"ALTER DEFAULT PRIVILEGES FOR ROLE x IN SCHEMA public GRANT SELECT ON TABLES TO y;",
		"CREATE ROLE appuser;",
		"COPY public.places (id, name) FROM stdin;",
		"1\tGRANT SELECT ON x TO y;",
		"ALTER TABLE x OWNER TO y;",
		`\.`,
		"INSERT INTO public.places VALUES (1, 'GRANT ALL ON x TO y;');",
		"ALTER TABLE ONLY public.places",
		"    ADD CONSTRAINT places_pkey PRIMARY KEY (id);",
		`\restrict abc`,
		"",
	}, "\n")
	f := newPGSQLFilter(strings.NewReader(in))
	got := filterAll(t, f)

	for _, gone := range []string{"CREATE DATABASE", "ALTER DATABASE", `\connect`, "DROP DATABASE", "SESSION AUTHORIZATION", "OWNER TO appuser", "owner to appuser",
		"GRANT SELECT ON TABLE", "REVOKE ALL", "ALTER DEFAULT PRIVILEGES", "CREATE ROLE"} {
		require.NotContains(t, got, gone)
	}
	// Kept: DDL, COPY data (even when a row looks like a statement), INSERTs, multi-line statements.
	for _, kept := range []string{"CREATE EXTENSION IF NOT EXISTS postgis", "CREATE TABLE public.places", "1\tGRANT SELECT ON x TO y;",
		"ALTER TABLE x OWNER TO y;", "INSERT INTO public.places VALUES (1, 'GRANT ALL ON x TO y;');",
		"ADD CONSTRAINT places_pkey PRIMARY KEY (id);", `\restrict abc`, `\.`} {
		require.Contains(t, got, kept)
	}
	require.Equal(t, 11, f.Dropped())
}

func TestSQLFilterKeepsLongLinesAndFinalLine(t *testing.T) {
	long := "INSERT INTO t VALUES ('" + strings.Repeat("x", 3*sqlFilterBuffer) + "');"
	in := "SELECT 1;\n" + long + "\nGRANT ALL ON t TO u;\nSELECT 2;"
	got := filterAll(t, newPGSQLFilter(strings.NewReader(in)))
	require.Equal(t, "SELECT 1;\n"+long+"\nSELECT 2;", got)
}

func TestMySQLFilter(t *testing.T) {
	in := strings.Join([]string{
		"-- MySQL dump 10.13",
		"CREATE DATABASE /*!32312 IF NOT EXISTS*/ `src` /*!40100 DEFAULT CHARACTER SET utf8mb4 */;",
		"USE `src`;",
		"/*!40000 DROP DATABASE IF EXISTS `src`*/;",
		"CREATE TABLE `t` (`id` int);",
		"INSERT INTO `t` VALUES (1),(2);",
		"INSERT INTO `t` VALUES ('CREATE DEFINER=`a`@`b` PROCEDURE');",
		"/*!50013 DEFINER=`root`@`localhost` SQL SECURITY DEFINER */",
		"/*!50001 VIEW `v` AS select 1 */;",
		"/*!50003 CREATE*/ /*!50017 DEFINER=`root`@`%`*/ /*!50003 TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET NEW.id = 1 */;;",
		"CREATE DEFINER=`root`@`localhost` PROCEDURE `p`() BEGIN SELECT 1; END ;;",
		"",
	}, "\n")
	f := newMySQLFilter(strings.NewReader(in))
	got := filterAll(t, f)
	require.NotContains(t, got, "CREATE DATABASE")
	require.NotContains(t, got, "USE `src`")
	require.NotContains(t, got, "DROP DATABASE")
	require.NotContains(t, got, "DEFINER=`root`")
	require.Contains(t, got, "SQL SECURITY DEFINER */")
	require.Contains(t, got, "CREATE  PROCEDURE `p`()")
	require.Contains(t, got, "INSERT INTO `t` VALUES (1),(2);")
	require.Contains(t, got, "'CREATE DEFINER=`a`@`b` PROCEDURE'") // data is never rewritten
	require.Equal(t, 3, f.Dropped())
}

func TestPGSQLFilterDropsClientCommandsOutsideQuotes(t *testing.T) {
	kept := []string{
		`SELECT 'a\b', E'it\'s \! not a command', $$ \! also data $$, "id\!";`,
		`-- \! a comment`,
		`/* \! a block comment */`,
		`SET standard_conforming_strings = on;`,
		`\restrict abc`,
		"COPY t (a) FROM stdin;",
		`\N`,
		`a\\b`,
		`\.`,
		"SELECT 2;",
	}
	dropped := []string{
		`\! id`,
		`\i /etc/passwd`,
		`  \o /tmp/x`,
		`\copy t from program 'id'`,
		`SELECT 1 \g |cat`,
		`SELECT 1; \! id`,
		`SET standard_conforming_strings = off;`,
	}
	var in []string
	in = append(in, dropped[:3]...)
	in = append(in, kept[:5]...)
	in = append(in, dropped[3:]...)
	in = append(in, kept[5:]...)
	f := newPGSQLFilter(strings.NewReader(strings.Join(in, "\n") + "\n"))
	require.Equal(t, strings.Join(kept, "\n")+"\n", filterAll(t, f))
	require.Equal(t, len(dropped), f.Dropped())
}

func TestPGSQLFilterRefusesCommandsHiddenInCopyData(t *testing.T) {
	// psql keeps a parenthesis open across the ";" of the COPY line, so it
	// is not in COPY mode and would run the next line.
	in := "SELECT (1;\nCOPY t FROM stdin;\n\\! id\n\\.\n"
	_, err := io.ReadAll(newPGSQLFilter(strings.NewReader(in)))
	require.ErrorIs(t, err, errUnsafeSQL)
}

func TestSQLFilterLongLines(t *testing.T) {
	pad := strings.Repeat("x", 3*sqlFilterBuffer)

	// A long line that is a command is dropped whole.
	f := newPGSQLFilter(strings.NewReader(`\! ` + pad + "\nSELECT 2;"))
	require.Equal(t, "SELECT 2;", filterAll(t, f))
	require.Equal(t, 1, f.Dropped())

	// A command after the first chunk of a line cannot be dropped any more:
	// the stream is refused.
	for name, newFilter := range map[string]func(io.Reader) *sqlFilter{"pg": newPGSQLFilter, "mysql": newMySQLFilter} {
		in := "SELECT '" + pad + `' \! id` + "\n"
		_, err := io.ReadAll(newFilter(strings.NewReader(in)))
		require.ErrorIs(t, err, errUnsafeSQL, name)
	}
}

func TestMySQLFilterDropsClientCommands(t *testing.T) {
	kept := []string{
		"CREATE TABLE t (id int, `source` text);",
		"INSERT INTO t VALUES ('first",
		"system rm -rf /", // inside a string
		"last');",
		"DELIMITER ;;",
		"CREATE PROCEDURE p() BEGIN",
		"  SELECT 1;",
		"END ;;",
		"DELIMITER ;",
		`/*M!999999\- enable the sandbox mode */`,
		`SELECT 'a\'; system id';`,
		"SELECT 1;",
	}
	dropped := []string{
		"system id",
		`\! id`,
		"  source /etc/passwd",
		`SELECT 1; \! id`,
		"tee /tmp/x",
		"pager cat",
		"SET sql_mode='NO_BACKSLASH_ESCAPES';",
	}
	var in []string
	in = append(in, kept[:4]...)
	in = append(in, dropped[:3]...)
	in = append(in, kept[4:9]...)
	in = append(in, dropped[3:]...)
	in = append(in, kept[9:]...)
	f := newMySQLFilter(strings.NewReader(strings.Join(in, "\n") + "\n"))
	require.Equal(t, strings.Join(kept, "\n")+"\n", filterAll(t, f))
	require.Equal(t, len(dropped), f.Dropped())
}

// scriptExec records every exec and answers by a rule.
type scriptExec struct {
	mu     sync.Mutex
	calls  []execCall
	answer func(argv []string, stdin []byte) (code int, stdout, stderr string, err error)
}

type execCall struct {
	argv  []string
	env   []string
	stdin []byte
}

func (s *scriptExec) ContainerExec(_ context.Context, _ string, argv []string, spec docker.ExecSpec, stdout, stderr io.Writer) (int, error) {
	var stdin []byte
	if spec.Stdin != nil {
		var err error
		if stdin, err = io.ReadAll(spec.Stdin); err != nil {
			return -1, err
		}
	}
	s.mu.Lock()
	s.calls = append(s.calls, execCall{argv: argv, env: spec.Env, stdin: stdin})
	s.mu.Unlock()
	code, out, errs, err := 0, "", "", error(nil)
	if s.answer != nil {
		code, out, errs, err = s.answer(argv, stdin)
	}
	_, _ = io.WriteString(stdout, out)
	_, _ = io.WriteString(stderr, errs)
	return code, err
}

func (s *scriptExec) statements() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		if c.argv[0] == "psql" {
			for i, a := range c.argv {
				if a == "-c" && i+1 < len(c.argv) {
					out = append(out, c.argv[i+1])
				}
			}
		}
	}
	return out
}

func (s *scriptExec) tool(name string) *execCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.calls {
		if s.calls[i].argv[0] == name && !(name == "psql" && contains(s.calls[i].argv, "-c")) {
			return &s.calls[i]
		}
	}
	return nil
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func pgReq(format string, gz bool) agentprotocol.DumpRestoreRequest {
	return agentprotocol.DumpRestoreRequest{
		RequestID: "r1", RestoreID: "0a1b2c3d-1111-2222-3333-444455556666",
		Target: agentprotocol.BackupTarget{ServiceID: "svc", Engine: "postgres", Database: "app", User: "nebula"},
		Format: format, Gzip: gz, Size: 10,
	}
}

var secretsPW = map[string]string{agentprotocol.SecretDBPassword: "s3cret-pw"}

func existsAnswer(argv []string, _ []byte) (int, string, string, error) {
	if argv[0] == "psql" && strings.Contains(strings.Join(argv, " "), "SELECT 1 FROM pg_database") {
		return 0, "1\n", "", nil
	}
	return 0, "", "", nil
}

func TestDumpRestorePostgresCustomSwapsScratchDatabase(t *testing.T) {
	ex := &scriptExec{answer: existsAnswer}
	var lines []string
	startedAt := -1
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c1", pgReq(agentprotocol.DumpPGCustom, false), secretsPW,
		strings.NewReader("PGDMPdata"), func() { startedAt = len(ex.calls) }, func(l string) { lines = append(lines, l) })
	require.Nil(t, derr)

	stmts := ex.statements()
	require.Equal(t, []string{
		`DROP DATABASE IF EXISTS "nc_restore_0a1b2c3d1111" WITH (FORCE)`,
		`CREATE DATABASE "nc_restore_0a1b2c3d1111" TEMPLATE template0 OWNER "nebula"`,
		`ANALYZE`,
		`SELECT 1 FROM pg_database WHERE datname = 'app'`,
		`ALTER DATABASE "app" WITH ALLOW_CONNECTIONS false`,
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = 'app' AND pid <> pg_backend_pid()`,
		`ALTER DATABASE "app" RENAME TO "nc_replaced_0a1b2c3d1111"`,
		`ALTER DATABASE "nc_restore_0a1b2c3d1111" RENAME TO "app"`,
		`DROP DATABASE IF EXISTS "nc_replaced_0a1b2c3d1111" WITH (FORCE)`,
	}, stmts)
	require.Equal(t, 2, startedAt, "ready is signalled after the scratch database exists, before the file is read")

	pr := ex.tool("pg_restore")
	require.NotNil(t, pr)
	require.Equal(t, []string{"pg_restore", "--no-owner", "--no-acl", "--role=nebula", "--exit-on-error", "--verbose", "--username", "nebula", "--dbname", "nc_restore_0a1b2c3d1111"}, pr.argv)
	require.Equal(t, []string{"PGPASSWORD=s3cret-pw"}, pr.env, "the password travels in the environment, never in argv")
	require.Equal(t, "PGDMPdata", string(pr.stdin))
	for _, c := range ex.calls {
		for _, a := range c.argv {
			require.NotContains(t, a, "s3cret-pw")
		}
	}
}

func TestDumpRestorePostgresPlainSQLGzipIsFiltered(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("CREATE TABLE t (id int);\nALTER TABLE t OWNER TO other;\nGRANT ALL ON t TO other;\n"))
	require.NoError(t, zw.Close())

	ex := &scriptExec{answer: existsAnswer}
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c1", pgReq(agentprotocol.DumpSQL, true), secretsPW, &gz, func() {}, func(string) {})
	require.Nil(t, derr)
	p := ex.tool("psql")
	require.NotNil(t, p)
	require.Contains(t, p.argv, "ON_ERROR_STOP=1")
	require.Equal(t, "CREATE TABLE t (id int);\n", string(p.stdin))
}

func TestDumpRestorePostgresFailureDropsScratchAndKeepsLiveDatabase(t *testing.T) {
	ex := &scriptExec{answer: func(argv []string, stdin []byte) (int, string, string, error) {
		if argv[0] == "pg_restore" {
			return 1, "", "pg_restore: error: could not execute query: ERROR: relation \"x\" already exists with s3cret-pw", nil
		}
		return existsAnswer(argv, stdin)
	}}
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c1", pgReq(agentprotocol.DumpPGCustom, false), secretsPW, strings.NewReader("PGDMP"), func() {}, func(string) {})
	require.NotNil(t, derr)
	require.False(t, derr.Before)
	require.Contains(t, derr.Msg, "pg_restore exited with status 1")
	require.Contains(t, derr.Msg, "already exists")
	require.NotContains(t, derr.Msg, "s3cret-pw", "secrets are masked")

	stmts := ex.statements()
	require.Equal(t, `DROP DATABASE IF EXISTS "nc_restore_0a1b2c3d1111" WITH (FORCE)`, stmts[len(stmts)-1])
	for _, s := range stmts {
		require.NotContains(t, s, "RENAME", "the live database is never renamed after a failed restore")
	}
}

func TestDumpRestorePostgresPrepareFailureIsBeforeData(t *testing.T) {
	ex := &scriptExec{answer: func(argv []string, _ []byte) (int, string, string, error) {
		return 1, "", "FATAL: out of disk", nil
	}}
	started := false
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c1", pgReq(agentprotocol.DumpPGCustom, false), secretsPW, strings.NewReader("x"), func() { started = true }, func(string) {})
	require.NotNil(t, derr)
	require.True(t, derr.Before)
	require.False(t, started)
	require.Contains(t, derr.Msg, "out of disk")
}

func TestDumpRestorePostgresRollsBackWhenSwapFails(t *testing.T) {
	ex := &scriptExec{answer: func(argv []string, stdin []byte) (int, string, string, error) {
		j := strings.Join(argv, " ")
		if strings.Contains(j, `RENAME TO "app"`) && strings.Contains(j, "nc_restore_") {
			return 1, "", "ERROR: boom", nil
		}
		return existsAnswer(argv, stdin)
	}}
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c1", pgReq(agentprotocol.DumpPGCustom, false), secretsPW, strings.NewReader("x"), func() {}, func(string) {})
	require.NotNil(t, derr)
	stmts := ex.statements()
	require.Contains(t, stmts, `ALTER DATABASE "nc_replaced_0a1b2c3d1111" RENAME TO "app"`, "the live database is put back")
	require.Contains(t, stmts, `ALTER DATABASE "app" WITH ALLOW_CONNECTIONS true`)
}

func TestDumpRestoreRejectsMismatchedFormat(t *testing.T) {
	ex := &scriptExec{}
	req := pgReq(agentprotocol.DumpMongo, false)
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", req, secretsPW, strings.NewReader(""), func() {}, func(string) {})
	require.NotNil(t, derr)
	require.True(t, derr.Before)
	require.Empty(t, ex.calls)
}

func TestDumpRestoreMySQLAndMongo(t *testing.T) {
	ex := &scriptExec{}
	req := agentprotocol.DumpRestoreRequest{
		RequestID: "r", RestoreID: "abc", Size: 5, Format: agentprotocol.DumpSQL,
		Target: agentprotocol.BackupTarget{Engine: "mariadb", Database: "app", User: "root"},
	}
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", req, map[string]string{agentprotocol.SecretDBPassword: "rootpw1"},
		strings.NewReader("USE `x`;\nCREATE TABLE t (a int);\n"), func() {}, func(string) {})
	require.Nil(t, derr)
	require.Equal(t, []string{"mariadb", "--binary-mode", "-u", "root", "app"}, ex.calls[0].argv)
	require.Equal(t, []string{"MYSQL_PWD=rootpw1"}, ex.calls[0].env)
	require.Equal(t, "CREATE TABLE t (a int);\n", string(ex.calls[0].stdin))

	ex = &scriptExec{}
	req = agentprotocol.DumpRestoreRequest{
		RequestID: "r", RestoreID: "abc", Size: 5, Format: agentprotocol.DumpMongo, Replace: true,
		Target: agentprotocol.BackupTarget{Engine: "mongodb", User: "root"},
	}
	derr = (&DumpRunner{Docker: ex}).Run(context.Background(), "c", req, map[string]string{agentprotocol.SecretDBPassword: "mongopw1"},
		strings.NewReader("\x6d\xe2\x99\x81..."), func() {}, func(string) {})
	require.Nil(t, derr)
	require.Equal(t, "mongorestore", ex.calls[0].argv[0])
	require.Contains(t, ex.calls[0].argv, "--drop")
	require.NotContains(t, ex.calls[0].argv, "--gzip", "a gzip layer is removed by the agent, not by mongorestore")
}

func TestDumpRestoreToolFailureMessage(t *testing.T) {
	ex := &scriptExec{answer: func(argv []string, _ []byte) (int, string, string, error) {
		return 1, "", "ERROR 1064 (42000) at line 3: You have an error in your SQL syntax", nil
	}}
	req := agentprotocol.DumpRestoreRequest{
		RequestID: "r", RestoreID: "abc", Size: 5, Format: agentprotocol.DumpSQL,
		Target: agentprotocol.BackupTarget{Engine: "mysql", Database: "app", User: "root"},
	}
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", req, nil, strings.NewReader("garbage;"), func() {}, func(string) {})
	require.NotNil(t, derr)
	require.Contains(t, derr.Msg, "line 3")
	require.Contains(t, derr.Msg, "may hold part of the dump")

	ex2 := &scriptExec{answer: func(argv []string, _ []byte) (int, string, string, error) {
		return -1, "", "", errors.New("docker gone")
	}}
	derr = (&DumpRunner{Docker: ex2}).Run(context.Background(), "c", req, nil, strings.NewReader("x;"), func() {}, func(string) {})
	require.NotNil(t, derr)
	require.Contains(t, derr.Msg, "docker gone")
}
