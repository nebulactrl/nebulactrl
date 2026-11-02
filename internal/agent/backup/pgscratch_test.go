package backup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	dropScratch = `DROP DATABASE IF EXISTS "nc_restore_0a1b2c3d1111" WITH (FORCE)`
	renameAway  = `ALTER DATABASE "app" RENAME TO "nc_replaced_0a1b2c3d1111"`
	renameIn    = `ALTER DATABASE "nc_restore_0a1b2c3d1111" RENAME TO "app"`
	dropOld     = `DROP DATABASE IF EXISTS "nc_replaced_0a1b2c3d1111" WITH (FORCE)`
)

// stmtOf is the SQL a psql -c exec ran ("" for any other command).
func stmtOf(argv []string) string {
	if len(argv) > 0 && argv[0] == "psql" {
		for i, a := range argv {
			if a == "-c" && i+1 < len(argv) {
				return argv[i+1]
			}
		}
	}
	return ""
}

// pgFailOn answers existsAnswer except for statements that match fail; fail
// returns the stderr and exit code.
func pgFailOn(fail func(stmt string) (stderr string, failed bool)) func([]string, []byte) (int, string, string, error) {
	return func(argv []string, stdin []byte) (int, string, string, error) {
		if s := stmtOf(argv); s != "" {
			if msg, failed := fail(s); failed {
				return 1, "", msg, nil
			}
		}
		return existsAnswer(argv, stdin)
	}
}

func runPGCustom(ctx context.Context, ex Execer) *DumpError {
	return (&DumpRunner{Docker: ex}).Run(ctx, "c", pgReq(agentprotocol.DumpPGCustom, false), secretsPW, strings.NewReader("PGDMP"), func() {}, func(string) {})
}

func TestPGScratchSwitchOverWhenTargetDoesNotExist(t *testing.T) {
	// Every statement succeeds with no output: the existence probe finds no database.
	ex := &scriptExec{answer: func([]string, []byte) (int, string, string, error) { return 0, "", "", nil }}
	require.Nil(t, runPGCustom(context.Background(), ex))
	stmts := ex.statements()
	require.Equal(t, renameIn, stmts[len(stmts)-1])
	for _, s := range stmts {
		require.NotContains(t, s, "ALLOW_CONNECTIONS", "there is no live database to lock")
		require.NotContains(t, s, "pg_terminate_backend")
	}
}

func TestPGScratchSwitchOverRenameFailureWhenTargetMissing(t *testing.T) {
	ex := &scriptExec{answer: func(argv []string, _ []byte) (int, string, string, error) {
		switch s := stmtOf(argv); {
		case strings.Contains(s, "SELECT 1 FROM pg_database"):
			return 0, "", "", nil
		case s == renameIn:
			return 1, "", "permission denied to rename", nil
		}
		return 0, "", "", nil
	}}
	derr := runPGCustom(context.Background(), ex)
	require.NotNil(t, derr)
	require.Equal(t, "could not switch over: permission denied to rename", derr.Msg)
	stmts := ex.statements()
	require.Equal(t, dropScratch, stmts[len(stmts)-1])
}

func TestPGScratchSwitchOverCannotLockTarget(t *testing.T) {
	ex := &scriptExec{answer: pgFailOn(func(s string) (string, bool) {
		return "must be owner of database app", strings.Contains(s, "ALLOW_CONNECTIONS false")
	})}
	derr := runPGCustom(context.Background(), ex)
	require.NotNil(t, derr)
	require.Equal(t, "could not stop new connections to app: must be owner of database app", derr.Msg)
	stmts := ex.statements()
	require.Equal(t, dropScratch, stmts[len(stmts)-1])
	require.NotContains(t, stmts, renameAway, "the live database is untouched")
}

// cancelAfterRestore returns a context that is canceled as soon as the
// restore client finished, so that switchOver's retry wait does not sleep.
func cancelAfterRestore(inner func([]string, []byte) (int, string, string, error)) (context.Context, func([]string, []byte) (int, string, string, error)) {
	ctx, cancel := context.WithCancel(context.Background())
	return ctx, func(argv []string, stdin []byte) (int, string, string, error) {
		code, out, errs, err := inner(argv, stdin)
		if argv[0] == "pg_restore" {
			cancel()
		}
		return code, out, errs, err
	}
}

func TestPGScratchSwitchOverGivesUpWhenTargetStaysBusy(t *testing.T) {
	ctx, answer := cancelAfterRestore(pgFailOn(func(s string) (string, bool) {
		return "database \"app\" is being accessed by other users", s == renameAway
	}))
	ex := &scriptExec{answer: answer}
	derr := runPGCustom(ctx, ex)
	require.NotNil(t, derr)
	require.Equal(t, `could not switch over: database "app" is being accessed by other users`, derr.Msg)

	stmts := ex.statements()
	attempts := 0
	for _, s := range stmts {
		if s == renameAway {
			attempts++
		}
	}
	require.Equal(t, 5, attempts, "the rename is retried five times")
	require.Equal(t, dropScratch, stmts[len(stmts)-1])
	require.Contains(t, stmts, `ALTER DATABASE "app" WITH ALLOW_CONNECTIONS true`, "new connections are allowed again")
	require.NotContains(t, stmts, renameIn)
}

func TestPGScratchSwitchOverRetriesRenameAndToleratesTerminateFailure(t *testing.T) {
	renames := 0
	ctx, answer := cancelAfterRestore(pgFailOn(func(s string) (string, bool) {
		switch {
		case s == renameAway:
			renames++
			return "busy", renames == 1
		case strings.Contains(s, "pg_terminate_backend"):
			return "not allowed", true
		}
		return "", false
	}))
	var lg logLines
	ex := &scriptExec{answer: answer}
	derr := (&DumpRunner{Docker: ex}).Run(ctx, "c", pgReq(agentprotocol.DumpPGCustom, false), secretsPW, strings.NewReader("PGDMP"), func() {}, lg.log)
	require.Nil(t, derr, "the second rename succeeds")
	require.Equal(t, 2, renames)
	stmts := ex.statements()
	require.Equal(t, dropOld, stmts[len(stmts)-1])
	require.Contains(t, stmts, renameIn)
	require.Contains(t, lg.joined(), "dropping the replaced database")
}

func TestPGScratchDropOfReplacedDatabaseFailureOnlyLogs(t *testing.T) {
	ex := &scriptExec{answer: pgFailOn(func(s string) (string, bool) {
		return "database is in use", s == dropOld
	})}
	var lg logLines
	derr := (&DumpRunner{Docker: ex}).Run(context.Background(), "c", pgReq(agentprotocol.DumpPGCustom, false), secretsPW, strings.NewReader("PGDMP"), func() {}, lg.log)
	require.Nil(t, derr, "the new data is in place; a leftover database is not a failure")
	require.Contains(t, lg.joined(), "could not drop the replaced database nc_replaced_0a1b2c3d1111 (drop it by hand): database is in use")
}

func TestPGScratchSQLReportsExitStatusWithoutStderr(t *testing.T) {
	ex := &scriptExec{answer: func([]string, []byte) (int, string, string, error) { return 7, "", "", nil }}
	sc := newPGScratch(ex, "c", "u", "pw", "app", "0a1b2c3d-1111", nil)
	_, err := sc.sql(context.Background(), "postgres", "SELECT 1")
	require.EqualError(t, err, "exit status 7")
}

func TestPGScratchSQLMasksPasswordAndReturnsTrimmedOutput(t *testing.T) {
	ex := &scriptExec{answer: func(argv []string, _ []byte) (int, string, string, error) {
		if argv[len(argv)-1] == "FAIL" {
			return 1, "", "password authentication failed (pw-secret)", nil
		}
		return 0, "  42 \n", "", nil
	}}
	sc := newPGScratch(ex, "c", "u", "pw-secret", "app", "0a1b2c3d-1111", map[string]string{agentprotocol.SecretDBPassword: "pw-secret"})
	out, err := sc.sql(context.Background(), "postgres", "SELECT 42")
	require.NoError(t, err)
	require.Equal(t, "42", out)
	require.Equal(t, []string{"PGPASSWORD=pw-secret"}, ex.calls[0].env)

	_, err = sc.sql(context.Background(), "postgres", "FAIL")
	require.EqualError(t, err, "password authentication failed (••••••)")

	ex.answer = func([]string, []byte) (int, string, string, error) { return -1, "", "", errors.New("exec dead") }
	_, err = sc.sql(context.Background(), "postgres", "SELECT 1")
	require.EqualError(t, err, "exec dead")
}

func TestNamesForDerivesStableNamesFromRestoreID(t *testing.T) {
	n := namesFor("0a1b2c3d-1111-2222-3333-444455556666", "app")
	require.Equal(t, "nc_restore_0a1b2c3d1111", n.scratch)
	require.Equal(t, "nc_replaced_0a1b2c3d1111", n.old)
	require.Equal(t, "postgres", n.maint, "maintenance statements run in the postgres database")

	// Restoring the postgres database itself must not run its maintenance SQL inside it.
	require.Equal(t, "template1", namesFor("0a1b2c3d-1111", "postgres").maint)

	// Non-hex characters are dropped and a missing id still yields valid names.
	require.Equal(t, "nc_restore_abc", namesFor("a-b-c-xyz", "app").scratch)
	require.Equal(t, "nc_restore_", namesFor("", "app").scratch)
}
