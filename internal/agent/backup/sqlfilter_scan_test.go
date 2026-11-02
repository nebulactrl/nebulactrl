package backup

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type filterCase struct {
	name string
	in   string
	out  string // what reaches the client
	drop int    // statements/lines removed
}

func runFilterCases(t *testing.T, newFilter func(io.Reader) *sqlFilter, cases []filterCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFilter(strings.NewReader(tc.in))
			b, err := io.ReadAll(f)
			require.NoError(t, err)
			require.Equal(t, tc.out, string(b))
			require.Equal(t, tc.drop, f.Dropped())
			require.NoError(t, f.Err())
		})
	}
}

// The scanner must follow psql's lexer: a backslash is a command only in code.
func TestPGSQLFilterLexing(t *testing.T) {
	ntag := "$" + strings.Repeat("a", 300) + "$"
	runFilterCases(t, newPGSQLFilter, []filterCase{
		{"dollar quote hides a command", "SELECT $tag$ \\! x $tag$;\n", "SELECT $tag$ \\! x $tag$;\n", 0},
		{"dollar quote spans lines", "DO $$\n\\! not a command\n$$;\nSELECT 1;\n", "DO $$\n\\! not a command\n$$;\nSELECT 1;\n", 0},
		{"dollar tag with digits and a near-miss delimiter inside", "SELECT $a1$ $b1$ \\! y $a $a1$;\n", "SELECT $a1$ $b1$ \\! y $a $a1$;\n", 0},
		{"command after a dollar quote closed is code", "SELECT $$x$$; \\! id\nSELECT 2;\n", "SELECT 2;\n", 1},
		{"parameter marker is not a dollar quote", "SELECT $1; \\! id\nSELECT 2;\n", "SELECT 2;\n", 1},
		{"dollar inside an identifier is not a quote", "SELECT a$b \\! id\nSELECT 2;\n", "SELECT 2;\n", 1},
		{"overlong dollar tag is refused by dropping the line", "SELECT " + ntag + " x " + ntag + ";\nSELECT 2;\n", "SELECT 2;\n", 1},
		{"nested block comments", "/* a /* \\! */ \\! b */ SELECT 1;\n", "/* a /* \\! */ \\! b */ SELECT 1;\n", 0},
		{"block comment spans lines", "/* start\n\\! id\n*/\nSELECT 1;\n", "/* start\n\\! id\n*/\nSELECT 1;\n", 0},
		{"command after a block comment", "/* c */ \\! id\nSELECT 1;\n", "SELECT 1;\n", 1},
		{"line comment ends at newline", "-- \\! x\n\\! id\nSELECT 1;\n", "-- \\! x\nSELECT 1;\n", 1},
		{"line comment ends at carriage return", "SELECT 1; -- c\r\\! id\n", "", 1},
		{"doubled quote stays in the string", "SELECT 'it''s \\! x';\n", "SELECT 'it''s \\! x';\n", 0},
		{"string continues across a newline", "SELECT 'a'\n'b \\! x';\n", "SELECT 'a'\n'b \\! x';\n", 0},
		{"continuation skips comment lines", "SELECT 'a'\n-- \\! x\n'b';\n", "SELECT 'a'\n-- \\! x\n'b';\n", 0},
		{"minus after a string is code", "SELECT 'a' - 1;\n", "SELECT 'a' - 1;\n", 0},
		{"command right after a string and minus", "SELECT 'a' -\\! x\nSELECT 2;\n", "SELECT 2;\n", 1},
		{"E-string continuation keeps backslash escapes", "SELECT E'a'\n' \\'; \\! x';\n", "SELECT E'a'\n' \\'; \\! x';\n", 0},
		{"a string after a gap without newline is a new standard string", "SELECT E'a' '\\'; \\! x';\n", "", 1},
		{"E-string with an escaped backslash ends at the quote", "SELECT E'a\\\\' \\! id\nSELECT 2;\n", "SELECT 2;\n", 1},
		{"identifier ending in e is not an E-string prefix", "SELECT one'\\'; \\! id'\nSELECT 2;\n", "SELECT 2;\n", 1},
		{"lowercase e starts an E-string", "SELECT e'\\'; \\! id';\n", "SELECT e'\\'; \\! id';\n", 0},
		{"quoted identifier hides a command", "SELECT \"a\\!b\" FROM t;\n", "SELECT \"a\\!b\" FROM t;\n", 0},
		{"quoted identifier spans lines", "SELECT \"a\n\\! id\" FROM t;\n", "SELECT \"a\n\\! id\" FROM t;\n", 0},
		{"string spans lines", "SELECT 'a\n\\! id\n';\n", "SELECT 'a\n\\! id\n';\n", 0},
	})
}

func TestPGSQLFilterSkipsRoleAndOwnershipStatements(t *testing.T) {
	var cases []filterCase
	for _, stmt := range []string{
		"RESET SESSION AUTHORIZATION;", "CREATE USER bob;", "ALTER USER bob SUPERUSER;", "DROP USER bob;",
		"CREATE GROUP g;", "ALTER GROUP g ADD USER bob;", "DROP GROUP g;", "ALTER ROLE r SET x = 1;", "DROP ROLE r;",
		"CREATE TABLESPACE ts LOCATION '/x';", "DROP TABLESPACE ts;", "COMMENT ON DATABASE app IS 'x';",
		"ALTER SYSTEM SET max_connections = 1;", "create role lower;",
	} {
		cases = append(cases, filterCase{stmt, "SELECT 0;\n" + stmt + "\nSELECT 1;\n", "SELECT 0;\nSELECT 1;\n", 1})
	}
	cases = append(cases,
		filterCase{"statement mentioning GRANT is kept", "COMMENT ON TABLE t IS 'GRANT';\n", "COMMENT ON TABLE t IS 'GRANT';\n", 0},
		filterCase{"non-owner ALTER is kept", "ALTER TABLE t ADD COLUMN owner_to int;\n", "ALTER TABLE t ADD COLUMN owner_to int;\n", 0},
		filterCase{"carriage returns do not defeat the owner match", "ALTER TABLE t OWNER TO u;\r\nSELECT 1;\r\n", "SELECT 1;\r\n", 1},
	)
	runFilterCases(t, newPGSQLFilter, cases)
}

func TestPGSQLFilterStandardConformingStrings(t *testing.T) {
	runFilterCases(t, newPGSQLFilter, []filterCase{
		{"set on with TO", "set standard_conforming_strings to on;\n", "set standard_conforming_strings to on;\n", 0},
		{"set local true", "SET LOCAL standard_conforming_strings = TRUE;\n", "SET LOCAL standard_conforming_strings = TRUE;\n", 0},
		{"set off with tab", "SET\tstandard_conforming_strings = off;\n", "", 1},
		{"set to false", "SET standard_conforming_strings TO false;\n", "", 1},
		{"set_config call", "SELECT set_config('standard_conforming_strings', 'off', false);\n", "", 1},
		{"alter database setting", "ALTER ROLE x SET standard_conforming_strings = off;\n", "", 1},
		{"do block", "DO $$ BEGIN SET standard_conforming_strings = off; END $$;\n", "", 1},
		{"unrelated set", "SET search_path = public;\n", "SET search_path = public;\n", 0},
		{"mention outside statements that can change it", "INSERT INTO t VALUES ('standard_conforming_strings');\n", "INSERT INTO t VALUES ('standard_conforming_strings');\n", 0},
	})
}

func TestPGSQLFilterRestrictCommands(t *testing.T) {
	runFilterCases(t, newPGSQLFilter, []filterCase{
		{"restrict and unrestrict are kept", "\\restrict abc123\nSELECT 1;\n\\unrestrict abc123\n", "\\restrict abc123\nSELECT 1;\n\\unrestrict abc123\n", 0},
		{"restrict with extra words is a command and dropped", "\\restrict a b\nSELECT 1;\n", "SELECT 1;\n", 1},
		{"restrict without a key is dropped", "\\restrict\nSELECT 1;\n", "SELECT 1;\n", 1},
		{"restrict inside a string is data", "SELECT 'a\n\\restrict k\n';\n", "SELECT 'a\n\\restrict k\n';\n", 0},
	})
}

func TestPGSQLFilterCopyBlocks(t *testing.T) {
	runFilterCases(t, newPGSQLFilter, []filterCase{
		{
			"copy data keeps the escapes pg_dump writes",
			"COPY t (a, b) FROM stdin;\n1\t\\N\n2\ta\\\\b\\tc\\nd\\rx\\by\\fz\\vw\\x41\\101\n\\.\nSELECT 1;\n",
			"COPY t (a, b) FROM stdin;\n1\t\\N\n2\ta\\\\b\\tc\\nd\\rx\\by\\fz\\vw\\x41\\101\n\\.\nSELECT 1;\n", 0,
		},
		{"copy mode ends at the terminator", "COPY t FROM stdin;\nrow\n\\.\n\\! id\nSELECT 1;\n", "COPY t FROM stdin;\nrow\n\\.\nSELECT 1;\n", 1},
		{"terminator with carriage return", "COPY t FROM stdin;\r\nrow\r\n\\.\r\n\\! id\r\n", "COPY t FROM stdin;\r\nrow\r\n\\.\r\n", 1},
		{"copy in a string is not copy mode", "SELECT 'x\nCOPY t FROM stdin;\n';\n\\! id\n", "SELECT 'x\nCOPY t FROM stdin;\n';\n", 1},
		{"copy to stdout is not copy mode", "COPY t TO stdout;\n\\! id\n", "COPY t TO stdout;\n", 1},
	})
	for _, bad := range []string{"\\!", "\\q", "\\i", "\\copy", "\\a"} {
		in := "COPY t FROM stdin;\nrow " + bad + " id\n\\.\n"
		_, err := io.ReadAll(newPGSQLFilter(strings.NewReader(in)))
		require.ErrorIs(t, err, errUnsafeSQL, bad)
	}
}

func TestPGSQLFilterCopyEscapeAcrossChunkBoundary(t *testing.T) {
	// The first chunk of a long COPY row ends with a backslash; the escape
	// letter is the first byte of the next chunk.
	row := func(esc string) string {
		return strings.Repeat("x", sqlFilterBuffer-1) + "\\" + esc + "tail\n"
	}
	in := "COPY t FROM stdin;\n" + row("N") + "\\.\n"
	got, err := io.ReadAll(newPGSQLFilter(strings.NewReader(in)))
	require.NoError(t, err)
	require.Equal(t, in, string(got))

	_, err = io.ReadAll(newPGSQLFilter(strings.NewReader("COPY t FROM stdin;\n" + row("q") + "\\.\n")))
	require.ErrorIs(t, err, errUnsafeSQL, "a command hidden behind a chunk boundary is refused")
}

func TestPGSQLFilterPassesLongStatementLinesThrough(t *testing.T) {
	// Statement prefixes are only matched on whole lines; a statement line
	// longer than the buffer is passed on and the next line starts fresh.
	pad := strings.Repeat("y", 2*sqlFilterBuffer+10)
	f := newPGSQLFilter(strings.NewReader("GRANT SELECT ON t TO " + pad + ";\nSELECT 1;\n"))
	got, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, "GRANT SELECT ON t TO "+pad+";\nSELECT 1;\n", string(got))
	require.Equal(t, 0, f.Dropped())
}

func TestSQLFilterReportsSourceReadError(t *testing.T) {
	boom := errors.New("connection reset")
	for name, newFilter := range map[string]func(io.Reader) *sqlFilter{"pg": newPGSQLFilter, "mysql": newMySQLFilter} {
		f := newFilter(io.MultiReader(strings.NewReader("SELECT 1;\nSELECT 2;"), &failingReader{boom}))
		got, err := io.ReadAll(f)
		require.ErrorIs(t, err, boom, name)
		require.Equal(t, "SELECT 1;\nSELECT 2;", string(got), name)
		require.ErrorIs(t, f.Err(), boom, name, "a truncated source must not look like a clean end")
		require.Equal(t, "the dump could not be read: connection reset", filterFailure(f.Err()), name)
	}
}

type failingReader struct{ err error }

func (e *failingReader) Read([]byte) (int, error) { return 0, e.err }

func TestSQLFilterErrIsNilAtCleanEndAndForNilFilter(t *testing.T) {
	f := newPGSQLFilter(strings.NewReader("SELECT 1;\n"))
	_, err := io.ReadAll(f)
	require.NoError(t, err)
	require.NoError(t, f.Err())

	var none *sqlFilter
	require.NoError(t, none.Err(), "the custom-format path has no filter")
}

func TestFilterFailureWording(t *testing.T) {
	require.Equal(t, errUnsafeSQL.Error(), filterFailure(errUnsafeSQL))
	wrapped := errors.Join(errors.New("ctx"), errUnsafeSQL)
	require.Equal(t, wrapped.Error(), filterFailure(wrapped))
	require.Equal(t, "the dump could not be read: unexpected EOF", filterFailure(io.ErrUnexpectedEOF))
}

func TestMySQLFilterLexing(t *testing.T) {
	runFilterCases(t, newMySQLFilter, []filterCase{
		{"hash comment hides a command", "SELECT 1; # \\! id\nSELECT 2;\n", "SELECT 1; # \\! id\nSELECT 2;\n", 0},
		{"dash comment hides a command", "SELECT 1; -- system id\nSELECT 2;\n", "SELECT 1; -- system id\nSELECT 2;\n", 0},
		{"dash comment ended by newline right after dashes", "SELECT 1; --\nSELECT 2;\n", "SELECT 1; --\nSELECT 2;\n", 0},
		{"double dash without a space is not a comment", "SELECT 5--\\! id\nSELECT 2;\n", "SELECT 2;\n", 1},
		{"double dash then digit is arithmetic", "SELECT 5--1;\n", "SELECT 5--1;\n", 0},
		{"single minus is arithmetic", "SELECT 3-1;\n", "SELECT 3-1;\n", 0},
		{"minus before a backslash command", "SELECT 1 -\\! x\nSELECT 2;\n", "SELECT 2;\n", 1},
		{"block comment hides a command", "/* system id */ SELECT 1;\n", "/* system id */ SELECT 1;\n", 0},
		{"block comment spans lines", "/* a\n\\! id\n*/ SELECT 1;\n", "/* a\n\\! id\n*/ SELECT 1;\n", 0},
		{"block comment with stars", "/** \\! **/ SELECT 1;\n", "/** \\! **/ SELECT 1;\n", 0},
		{"division is not a comment", "SELECT 4/2;\n", "SELECT 4/2;\n", 0},
		{"executable comment is code", "/*!40101 SET x = 1 */;\n", "/*!40101 SET x = 1 */;\n", 0},
		{"command word inside an executable comment is not first", "/*!40101 system id */;\n", "/*!40101 system id */;\n", 0},
		{"second statement on a line starting with a client command", "SELECT 1; system id\nSELECT 2;\n", "SELECT 2;\n", 1},
		{"command word as a column name", "SELECT system FROM t;\n", "SELECT system FROM t;\n", 0},
		{"command word on a continuation line is not first", "SELECT 1,\n system\n FROM t;\n", "SELECT 1,\n system\n FROM t;\n", 0},
		{"a word that only starts like a command", "systemic;\n", "systemic;\n", 0},
		{"quit before the delimiter", "quit;\nSELECT 1;\n", "SELECT 1;\n", 1},
		{"upper case exit", "EXIT\nSELECT 1;\n", "SELECT 1;\n", 1},
		{"connect", "connect other\nSELECT 1;\n", "SELECT 1;\n", 1},
		{"backtick identifier", "SELECT `system`, `a\\`b` FROM t;\n", "SELECT `system`, `a\\`b` FROM t;\n", 0},
		{"double quoted string with escaped quote", "SELECT \"a\\\"; system id\";\n", "SELECT \"a\\\"; system id\";\n", 0},
		{"null marker is not a command", "SELECT \\N;\n", "SELECT \\N;\n", 0},
		{"backslash command after a string", "SELECT 'a' \\g\nSELECT 2;\n", "SELECT 2;\n", 1},
		{"statements that merely start like skipped ones", "UPDATE t SET a = 1;\nUSER_TABLE;\n", "UPDATE t SET a = 1;\nUSER_TABLE;\n", 0},
		{"create database without a closing semicolon is kept", "CREATE DATABASE x\n", "CREATE DATABASE x\n", 0},
		{"use with a tab", "USE\tother;\nSELECT 1;\n", "SELECT 1;\n", 1},
		{"executable create database comment", "/*!32312 CREATE DATABASE x */;\nSELECT 1;\n", "SELECT 1;\n", 1},
		{"no_backslash_escapes in an executable comment", "/*!40101 SET SQL_MODE='NO_BACKSLASH_ESCAPES' */;\nSELECT 1;\n", "SELECT 1;\n", 1},
		{"mysqldump's own sql_mode line is kept", "/*!40101 SET SQL_MODE='NO_AUTO_VALUE_ON_ZERO' */;\n", "/*!40101 SET SQL_MODE='NO_AUTO_VALUE_ON_ZERO' */;\n", 0},
		{"user variable assignment is kept", "SET @OLD_SQL_MODE=@@SQL_MODE, SQL_MODE='NO_AUTO_VALUE_ON_ZERO';\n", "SET @OLD_SQL_MODE=@@SQL_MODE, SQL_MODE='NO_AUTO_VALUE_ON_ZERO';\n", 0},
		{"set with a tab", "SET\tsql_mode = 'NO_BACKSLASH_ESCAPES';\nSELECT 1;\n", "SELECT 1;\n", 1},
	})
}

func TestMySQLFilterDelimiters(t *testing.T) {
	runFilterCases(t, newMySQLFilter, []filterCase{
		{
			"slash delimiter with a comment before it",
			"DELIMITER //\nCREATE PROCEDURE p() BEGIN SELECT 1; END//\nSELECT 1 /* \\! x */ //\nDELIMITER ;\nSELECT 2;\n",
			"DELIMITER //\nCREATE PROCEDURE p() BEGIN SELECT 1; END//\nSELECT 1 /* \\! x */ //\nDELIMITER ;\nSELECT 2;\n", 0,
		},
		{"dollar delimiter", "DELIMITER $$\nSELECT 1$$\nDELIMITER ;\n", "DELIMITER $$\nSELECT 1$$\nDELIMITER ;\n", 0},
		{"command after a custom delimiter ended the statement", "DELIMITER ;;\nSELECT 1;; system id\nDELIMITER ;\n", "DELIMITER ;;\nDELIMITER ;\n", 1},
		{"delimiter with quotes is refused", "DELIMITER 'x'\nSELECT 1;\n", "SELECT 1;\n", 1},
		{"delimiter longer than eight characters", "DELIMITER ;;;;;;;;;\nSELECT 1;\n", "SELECT 1;\n", 1},
		{"delimiter followed by more text", "DELIMITER ;; \\! id\nSELECT 1;\n", "SELECT 1;\n", 1},
		{"delimiter without an argument", "DELIMITER\nSELECT 1;\n", "SELECT 1;\n", 1},
		{"delimiter with a comment starter", "DELIMITER /*\nSELECT 1;\n", "SELECT 1;\n", 1},
		{"delimiter as an indented command", "  delimiter ;;\nSELECT 1;;\n", "  delimiter ;;\nSELECT 1;;\n", 0},
	})
}

func TestValidMySQLDelimiter(t *testing.T) {
	for _, ok := range []string{"//", ";", ";;", "$$", "|", "!!", ";$|!%^&~"} {
		require.True(t, validMySQLDelimiter(ok), ok)
	}
	for _, bad := range []string{"", ";;;;;;;;;", "a", "'", `\`, "/", "/*", "--", "#", ";\n", "\"\""} {
		require.False(t, validMySQLDelimiter(bad), bad)
	}
}

func TestMySQLFilterDefinerRemoval(t *testing.T) {
	runFilterCases(t, newMySQLFilter, []filterCase{
		{"alter view", "ALTER DEFINER=`a`@`b` VIEW v AS select 1;\n", "ALTER  VIEW v AS select 1;\n", 0},
		{"unquoted account", "CREATE DEFINER=root@localhost PROCEDURE p() BEGIN END;\n", "CREATE  PROCEDURE p() BEGIN END;\n", 0},
		{"single quoted account with spaces around equals", "CREATE DEFINER = 'x'@'%' TRIGGER t BEFORE INSERT ON a FOR EACH ROW SET NEW.i = 1;\n", "CREATE  TRIGGER t BEFORE INSERT ON a FOR EACH ROW SET NEW.i = 1;\n", 0},
		{"data lines keep their text", "INSERT INTO t VALUES ('CREATE DEFINER=a@b');\n", "INSERT INTO t VALUES ('CREATE DEFINER=a@b');\n", 0},
		{"plain create without a definer", "CREATE TABLE t (id int);\n", "CREATE TABLE t (id int);\n", 0},
	})
}

func TestMySQLFilterLongLines(t *testing.T) {
	pad := strings.Repeat(" ", 2*sqlFilterBuffer)
	// A DELIMITER command that shows up after the first chunk cannot be
	// dropped any more: the stream is refused.
	_, err := io.ReadAll(newMySQLFilter(strings.NewReader("SELECT 1;" + pad + "delimiter ;;\n")))
	require.ErrorIs(t, err, errUnsafeSQL)

	// A long first chunk that starts a DELIMITER command is dropped whole.
	f := newMySQLFilter(strings.NewReader("DELIMITER " + pad + ";;\nSELECT 1;\n"))
	got, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, "SELECT 1;\n", string(got))
	require.Equal(t, 1, f.Dropped())

	// Harmless long lines pass through unchanged, including the tail chunks.
	long := "INSERT INTO t VALUES ('" + strings.Repeat("v", 3*sqlFilterBuffer) + "');\n"
	got, err = io.ReadAll(newMySQLFilter(strings.NewReader(long + "SELECT 1;\n")))
	require.NoError(t, err)
	require.Equal(t, long+"SELECT 1;\n", string(got))
}
