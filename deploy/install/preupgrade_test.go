package installdeploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dcStub fakes `docker compose`: ps lists RUNNING; exec of the control plane's
// pre-upgrade-backup exits SERVER_RC after printing SERVER_OUT; exec of pg_dump
// writes "PGDMP" (or fails when DUMP_FAIL=1). Every call is logged to CALLS.
const dcStub = `
dc() {
	echo "dc $*" >>"$CALLS"
	case "$1" in
	ps) printf '%s\n' $RUNNING ;;
	exec)
		case "$*" in
		*pre-upgrade-backup*) echo "$SERVER_OUT"; return "$SERVER_RC" ;;
		*pg_dump*) [ "${DUMP_FAIL:-0}" = 1 ] && return 1; printf PGDMP; return 0 ;;
		esac ;;
	esac
}
`

func preUpgradeEnv(t *testing.T, withEnvFile bool) (dir string, args []string) {
	t.Helper()
	dir = t.TempDir()
	if withEnvFile {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("POSTGRES_PASSWORD=pw\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, []string{"--backup-dir", filepath.Join(dir, "backups"), "--version", "1.5.0"}
}

func runPre(t *testing.T, dir string, args []string, vars string) (string, error, string) {
	t.Helper()
	calls := filepath.Join(dir, "calls")
	out, err := runInstaller(t, dir, args, "CALLS='"+calls+"'\n"+vars+"\n"+dcStub+"\nstep_prebackup\n")
	b, _ := os.ReadFile(calls)
	return out, err, string(b)
}

func TestPreUpgradeBackupSucceedsThroughTheRunningControlPlane(t *testing.T) {
	dir, args := preUpgradeEnv(t, true)
	out, err, calls := runPre(t, dir, args, "RUNNING='nebulactrl postgres'; SERVER_RC=0; SERVER_OUT='succeeded: Backed up to directory /x (10 bytes) before upgrading.'")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "Backed up to directory /x") {
		t.Errorf("the result of the backup is not shown:\n%s", out)
	}
	if !strings.Contains(calls, "pre-upgrade-backup --to 1.5.0") {
		t.Errorf("the target version is not passed, so the new control plane would back up again:\n%s", calls)
	}
	if strings.Contains(calls, "--force") || strings.Contains(calls, "pg_dump") {
		t.Errorf("unexpected calls:\n%s", calls)
	}
}

func TestPreUpgradeBackupFailureStopsTheUpgrade(t *testing.T) {
	dir, args := preUpgradeEnv(t, true)
	out, err, _ := runPre(t, dir, args, "RUNNING='nebulactrl postgres'; SERVER_RC=3; SERVER_OUT='backup before the upgrade FAILED: disk full'")
	if err == nil {
		t.Fatalf("a failed backup must stop the installer:\n%s", out)
	}
	for _, want := range []string{"backup before the upgrade failed", "disk full", "Nothing was changed", "--force-upgrade"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestForceUpgradeGoesOnAfterAFailedBackup(t *testing.T) {
	dir, args := preUpgradeEnv(t, true)
	out, err, calls := runPre(t, dir, append(args, "--force-upgrade"), "RUNNING='nebulactrl postgres'; SERVER_RC=3; SERVER_OUT='backup before the upgrade FAILED: disk full'")
	if err != nil {
		t.Fatalf("--force-upgrade must continue: %v\n%s", err, out)
	}
	if !strings.Contains(out, "continuing because of --force-upgrade") {
		t.Errorf("the override is not announced:\n%s", out)
	}
	if !strings.Contains(calls, "--force") {
		t.Errorf("the control plane is not told it is forced, so it would record a failure:\n%s", calls)
	}
}

func TestOldControlPlaneIsBackedUpWithPgDump(t *testing.T) {
	dir, args := preUpgradeEnv(t, true)
	out, err, calls := runPre(t, dir, args, "RUNNING='nebulactrl postgres'; SERVER_RC=1; SERVER_OUT='Error: unknown command \"pre-upgrade-backup\"'")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	dumps, _ := filepath.Glob(filepath.Join(dir, "backups", "pre-upgrade-*.dump"))
	if len(dumps) != 1 {
		t.Fatalf("want one dump, got %v\n%s\n%s", dumps, out, calls)
	}
	if b, _ := os.ReadFile(dumps[0]); string(b) != "PGDMP" {
		t.Errorf("dump content = %q", b)
	}
	if fi, _ := os.Stat(dumps[0]); fi.Mode().Perm() != 0o600 {
		t.Errorf("a dump holds every secret in the database; mode = %v", fi.Mode().Perm())
	}
	if strings.Contains(calls, "pw") {
		t.Errorf("the database password must not appear on a command line:\n%s", calls)
	}
}

func TestPgDumpFailureStopsTheUpgradeUnlessForced(t *testing.T) {
	dir, args := preUpgradeEnv(t, true)
	vars := "RUNNING='postgres'; DUMP_FAIL=1"
	out, err, _ := runPre(t, dir, args, vars)
	if err == nil || !strings.Contains(out, "pg_dump of the database failed") {
		t.Fatalf("want a stop, got %v\n%s", err, out)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "backups", "pre-upgrade-*.dump")); len(left) != 0 {
		t.Errorf("a failed dump must not stay behind: %v", left)
	}
	out, err, _ = runPre(t, dir, append(args, "--force-upgrade"), vars)
	if err != nil {
		t.Fatalf("forced: %v\n%s", err, out)
	}
}

func TestNoBackupWhereThereIsNothingToBackUp(t *testing.T) {
	// A new installation has no .env yet.
	dir, args := preUpgradeEnv(t, false)
	out, err, calls := runPre(t, dir, args, "RUNNING='nebulactrl postgres'; SERVER_RC=0; SERVER_OUT=x")
	if err != nil || calls != "" || strings.Contains(out, "Backup before the upgrade") {
		t.Errorf("a new install must skip silently: %v\n%s\n%s", err, out, calls)
	}
	// A stopped stack.
	dir, args = preUpgradeEnv(t, true)
	out, err, _ = runPre(t, dir, args, "RUNNING=''")
	if err != nil || !strings.Contains(out, "nothing to back up from") {
		t.Errorf("stopped stack: %v\n%s", err, out)
	}
	// --check changes nothing.
	dir, args = preUpgradeEnv(t, true)
	out, err, calls = runPre(t, dir, append(args, "--check"), "RUNNING='nebulactrl postgres'; SERVER_RC=0; SERVER_OUT=x")
	if err != nil || calls != "" {
		t.Errorf("--check must not back up: %v\n%s\n%s", err, out, calls)
	}
}

func TestNonReleaseVersionsDoNotPassTo(t *testing.T) {
	dir, args := preUpgradeEnv(t, true)
	args[len(args)-1] = "latest"
	_, err, calls := runPre(t, dir, args, "RUNNING='nebulactrl postgres'; SERVER_RC=0; SERVER_OUT=ok")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(calls, "--to") {
		t.Errorf("'latest' is not a version the marker can name:\n%s", calls)
	}
}
