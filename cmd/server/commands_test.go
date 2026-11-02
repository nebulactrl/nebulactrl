package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/internal/upgrade"
)

// execCmd runs a command the way cobra does from main and returns its stdout
// and stderr.
func execCmd(t *testing.T, cmd *cobra.Command, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

// serverEnv sets the environment of a control plane on dsn: a fresh secret key
// and none of the optional settings the host might have.
func serverEnv(t *testing.T, dsn string) {
	t.Helper()
	t.Setenv("NEBULACTRL_DATABASE_URL", dsn)
	t.Setenv("NEBULACTRL_SECRET_KEY", base64.StdEncoding.EncodeToString(newTestKey(t)))
	for _, k := range []string{
		"NEBULACTRL_SECRET_KEY_FILE", "NEBULACTRL_SECRET_KEY_PREVIOUS", "NEBULACTRL_SECRET_KEY_PREVIOUS_FILE",
		"NEBULACTRL_BACKUP_DIR", "NEBULACTRL_FORCE_UPGRADE", "NEBULACTRL_LISTEN", "NEBULACTRL_STATUS_LISTEN", "NEBULACTRL_PUBLIC_URL",
	} {
		t.Setenv(k, "")
	}
}

// withVersion pretends the binary was built as v.
func withVersion(t *testing.T, v string) {
	t.Helper()
	old := version
	version = v
	t.Cleanup(func() { version = old })
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

func TestKeygenPrintsANewBase64Key32BytesLong(t *testing.T) {
	first, _, err := execCmd(t, keygenCmd())
	require.NoError(t, err)
	second, _, err := execCmd(t, keygenCmd())
	require.NoError(t, err)

	raw, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(first, "\n"))
	require.NoError(t, err)
	require.Len(t, raw, secretbox.KeySize)
	require.Equal(t, 1, strings.Count(first, "\n"), "one line, key only")
	require.NotEqual(t, first, second, "every run prints a different key")

	_, _, err = execCmd(t, keygenCmd(), "extra")
	require.Error(t, err, "keygen takes no arguments")
}

func TestOpenAPIWritesTheDocumentToStdoutOrAFile(t *testing.T) {
	out, errOut, err := execCmd(t, openapiCmd(), "--out", "-")
	require.NoError(t, err)
	require.Empty(t, errOut, "the stdout form prints nothing else")
	var doc struct {
		OpenAPI string         `json:"openapi"`
		Paths   map[string]any `json:"paths"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	require.True(t, strings.HasPrefix(doc.OpenAPI, "3.1"), doc.OpenAPI)
	require.NotEmpty(t, doc.Paths)
	require.True(t, strings.HasSuffix(out, "}\n"), "the document ends with a newline")

	target := filepath.Join(t.TempDir(), "nested", "dir", "openapi.json")
	stdout, errOut, err := execCmd(t, openapiCmd(), "-o", target)
	require.NoError(t, err)
	require.Empty(t, stdout)
	written, err := os.ReadFile(target)
	require.NoError(t, err, "missing parent directories are created")
	require.Equal(t, out, string(written), "file and stdout carry the same document")
	require.Equal(t, "wrote "+target+" ("+strconv.Itoa(len(written))+" bytes)\n", errOut)
}

func TestOpenAPIRefusesAnUnwritableTarget(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	_, _, err := execCmd(t, openapiCmd(), "--out", filepath.Join(blocker, "openapi.json"))
	require.Error(t, err, "a parent that is a file cannot become a directory")
	require.NoFileExists(t, filepath.Join(blocker, "openapi.json"))
}

func TestHealthcheckSucceedsOnlyOnReady(t *testing.T) {
	var paths []string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)

	_, _, err := execCmd(t, healthcheckCmd(), "--url", srv.URL+"/readyz")
	require.NoError(t, err)
	require.Equal(t, []string{"GET /readyz"}, paths)

	status = http.StatusServiceUnavailable
	_, _, err = execCmd(t, healthcheckCmd(), "--url", srv.URL+"/readyz")
	require.ErrorContains(t, err, "/readyz returned 503 Service Unavailable")
}

func TestHealthcheckDefaultsToTheListenAddressOfTheServer(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.URL.Path }))
	t.Cleanup(srv.Close)
	t.Setenv("NEBULACTRL_LISTEN", srv.Listener.Addr().String())

	_, _, err := execCmd(t, healthcheckCmd())
	require.NoError(t, err)
	require.Equal(t, "/readyz", got)
}

func TestHealthcheckFailsWhenNothingAnswersOrItIsTooSlow(t *testing.T) {
	addr := freeAddr(t)
	_, _, err := execCmd(t, healthcheckCmd(), "--url", "http://"+addr+"/readyz")
	require.Error(t, err, "connection refused is a failed check")

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); slow.Close() })
	_, _, err = execCmd(t, healthcheckCmd(), "--url", slow.URL, "--timeout", "50ms")
	require.ErrorIs(t, err, context.DeadlineExceeded)

	_, _, err = execCmd(t, healthcheckCmd(), "--url", "http://[::1")
	require.Error(t, err, "an unparsable URL is reported")
	_, _, err = execCmd(t, healthcheckCmd(), "extra")
	require.Error(t, err, "healthcheck takes no arguments")
}

func TestForceUpgradeReadsTheEnvironment(t *testing.T) {
	for value, want := range map[string]bool{
		"": false, "0": false, "false": false, "no": false, "off": false, "maybe": false,
		"1": true, "true": true, " TRUE ": true, "Yes": true, "on": true,
	} {
		t.Setenv("NEBULACTRL_FORCE_UPGRADE", value)
		require.Equal(t, want, forceUpgrade(), "NEBULACTRL_FORCE_UPGRADE=%q", value)
	}
}

func TestPreUpgradeDepsRejectsABadSecretKey(t *testing.T) {
	_, err := preUpgradeDeps(&config.Config{SecretKey: []byte("short")}, nil)
	require.ErrorContains(t, err, "secret key:")

	cfg := &config.Config{SecretKey: newTestKey(t), DatabaseURL: "postgres://x", BackupDir: "/backups"}
	deps, err := preUpgradeDeps(cfg, nil)
	require.NoError(t, err)
	require.Equal(t, "postgres://x", deps.DatabaseURL)
	require.Equal(t, "/backups", deps.BackupDir)
	require.Equal(t, backups.KeyFingerprint(cfg.SecretKey), deps.KeyFingerprint)
	require.NotNil(t, deps.Box)
}

func breakInstanceDestination(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	tag, err := pool.Exec(context.Background(),
		`UPDATE instance_backup_settings SET dest_kind = 's3', bucket = 'b', access_key_enc = '\x01', secret_key_enc = '\x02'`)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected())
}

func TestPreflightUpgradeOnADevelopmentBuildTakesNoBackup(t *testing.T) {
	pool := testutil.NewPool(t)
	withVersion(t, "dev")
	dec, err := preflightUpgrade(context.Background(), &config.Config{SecretKey: newTestKey(t)}, pool, slog.Default())
	require.NoError(t, err)
	require.Nil(t, dec.Result)
	require.Contains(t, dec.Reason, "development build")
}

func TestPreflightUpgradeOnAChangedVersionBacksUpOrRefuses(t *testing.T) {
	pool := testutil.NewPool(t)
	withVersion(t, "1.5.0")
	cfg := &config.Config{SecretKey: newTestKey(t), DatabaseURL: pool.Config().ConnString()}

	// No backup destination reachable (no backup directory): the upgrade goes on, loudly.
	dec, err := preflightUpgrade(context.Background(), cfg, pool, slog.Default())
	require.NoError(t, err)
	require.NotNil(t, dec.Result)
	require.Equal(t, backups.PreUpgradeSkipped, dec.Result.Status)
	require.Equal(t, "1.5.0", dec.Result.To)

	// A destination that cannot be written to refuses the upgrade ...
	breakInstanceDestination(t, pool)
	dec, err = preflightUpgrade(context.Background(), cfg, pool, slog.Default())
	require.ErrorIs(t, err, upgrade.ErrBackupFailed)
	require.NotNil(t, dec.Result)
	require.Equal(t, backups.PreUpgradeFailed, dec.Result.Status)

	// ... unless the operator forces it.
	t.Setenv("NEBULACTRL_FORCE_UPGRADE", "true")
	dec, err = preflightUpgrade(context.Background(), cfg, pool, slog.Default())
	require.NoError(t, err)
	require.Equal(t, backups.PreUpgradeForced, dec.Result.Status)

	// Once the version is recorded, the next start is not an upgrade.
	require.NoError(t, upgrade.Commit(context.Background(), pool, dec, "1.5.0"))
	dec, err = preflightUpgrade(context.Background(), cfg, pool, slog.Default())
	require.NoError(t, err)
	require.Nil(t, dec.Result)
	require.Equal(t, "the version did not change", dec.Reason)
}

func TestPreflightUpgradeRejectsABadSecretKey(t *testing.T) {
	pool := testutil.NewPool(t)
	_, err := preflightUpgrade(context.Background(), &config.Config{SecretKey: []byte("short")}, pool, slog.Default())
	require.ErrorContains(t, err, "secret key:")
}

func TestPreUpgradeBackupCmdRecordsASkippedBackupWhenNothingIsConfigured(t *testing.T) {
	pool := testutil.NewPool(t)
	serverEnv(t, pool.Config().ConnString())

	out, _, err := execCmd(t, preUpgradeBackupCmd(), "--to", "1.6.0")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(out, "skipped: "), out)

	up, err := db.New(pool).GetInstanceUpgrade(context.Background())
	require.NoError(t, err)
	require.Equal(t, backups.PreUpgradeSkipped, up.PreUpgradeStatus)
	require.Equal(t, "1.6.0", up.PreUpgradeToVersion, "the new control plane learns which version was backed up for")
}

func TestPreUpgradeBackupCmdExitsWithRefusedWhenTheBackupFailed(t *testing.T) {
	pool := testutil.NewPool(t)
	serverEnv(t, pool.Config().ConnString())
	breakInstanceDestination(t, pool)

	out, _, err := execCmd(t, preUpgradeBackupCmd(), "--to", "1.6.0")
	var ee *exitError
	require.ErrorAs(t, err, &ee)
	require.Equal(t, exitRefused, ee.code)
	require.ErrorContains(t, err, "use --force to override")
	require.Contains(t, out, "backup before the upgrade FAILED: ")

	var rows int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM instance_backups`).Scan(&rows))
	require.Zero(t, rows, "a refused backup records no run")

	out, _, err = execCmd(t, preUpgradeBackupCmd(), "--to", "1.6.0", "--force")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(out, "forced: Forced past a failed backup: "), out)
	up, err := db.New(pool).GetInstanceUpgrade(context.Background())
	require.NoError(t, err)
	require.Equal(t, backups.PreUpgradeForced, up.PreUpgradeStatus)
}

func TestPreUpgradeBackupCmdReportsConfigurationAndDatabaseErrors(t *testing.T) {
	t.Setenv("NEBULACTRL_DATABASE_URL", "")
	t.Setenv("NEBULACTRL_SECRET_KEY", "")
	t.Setenv("NEBULACTRL_SECRET_KEY_FILE", "")
	_, _, err := execCmd(t, preUpgradeBackupCmd())
	require.ErrorContains(t, err, "configuration:")

	serverEnv(t, "not a url")
	_, _, err = execCmd(t, preUpgradeBackupCmd())
	require.ErrorContains(t, err, "database pool:")
}

func TestConnectGivesUpOnAnUnreachableDatabaseWhenCancelled(t *testing.T) {
	_, err := connect(context.Background(), "::not a dsn::", slog.Default())
	require.ErrorContains(t, err, "database pool:")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pool, err := connect(ctx, "postgres://nebula@"+freeAddr(t)+"/nebula?connect_timeout=1", slog.Default())
	require.Nil(t, pool)
	require.ErrorContains(t, err, "database unreachable")
}

func TestConnectReturnsAWorkingPool(t *testing.T) {
	src := testutil.NewPool(t)
	pool, err := connect(context.Background(), src.Config().ConnString(), slog.Default())
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, pool.Ping(context.Background()))
}

func TestServeRefusesAnInvalidConfiguration(t *testing.T) {
	t.Setenv("NEBULACTRL_DATABASE_URL", "")
	t.Setenv("NEBULACTRL_SECRET_KEY", "")
	t.Setenv("NEBULACTRL_SECRET_KEY_FILE", "")
	err := serve(context.Background())
	require.ErrorContains(t, err, "configuration:")

	// The default command is serve: the root command fails the same way.
	_, _, err = execCmd(t, newRootCmd())
	require.ErrorContains(t, err, "configuration:")
	_, _, err = execCmd(t, newRootCmd(), "serve")
	require.ErrorContains(t, err, "configuration:")
}

func TestServeStopsBeforeMigratingWhenTheUpgradeBackupFailed(t *testing.T) {
	pool := testutil.NewPool(t)
	serverEnv(t, pool.Config().ConnString())
	withVersion(t, "1.5.0")
	breakInstanceDestination(t, pool)

	err := serve(context.Background())
	require.ErrorIs(t, err, upgrade.ErrBackupFailed)
}

func TestServeListensUntilCancelledThenShutsDownCleanly(t *testing.T) {
	pool := testutil.NewPool(t)
	serverEnv(t, pool.Config().ConnString())
	listen, statusListen := freeAddr(t), freeAddr(t)
	t.Setenv("NEBULACTRL_LISTEN", listen)
	t.Setenv("NEBULACTRL_STATUS_LISTEN", statusListen)
	t.Setenv("NEBULACTRL_PUBLIC_URL", "http://"+listen)
	t.Setenv("NEBULACTRL_LOG_LEVEL", "error")
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx) }()

	ready := func(addr string) (int, bool) {
		resp, err := http.Get("http://" + addr + "/readyz")
		if err != nil {
			return 0, false
		}
		defer resp.Body.Close()
		return resp.StatusCode, true
	}
	require.Eventually(t, func() bool {
		select {
		case err := <-done:
			t.Fatalf("serve returned early: %v", err)
		default:
		}
		code, ok := ready(listen)
		return ok && code == http.StatusOK
	}, 60*time.Second, 50*time.Millisecond, "the control plane never became ready")

	// The status listener is a separate server: it does not serve the API.
	require.Eventually(t, func() bool { _, ok := ready(statusListen); return ok }, 10*time.Second, 20*time.Millisecond)
	code, _ := ready(statusListen)
	require.NotEqual(t, http.StatusOK, code, "/readyz belongs to the main listener only")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(60 * time.Second):
		t.Fatal("serve did not shut down after cancel")
	}
	_, ok := ready(listen)
	require.False(t, ok, "the listener is closed after shutdown")
}

func TestServeReportsAListenerThatCannotBind(t *testing.T) {
	pool := testutil.NewPool(t)
	serverEnv(t, pool.Config().ConnString())
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer taken.Close()
	t.Setenv("NEBULACTRL_LISTEN", taken.Addr().String())
	t.Setenv("NEBULACTRL_STATUS_LISTEN", freeAddr(t))
	t.Setenv("NEBULACTRL_PUBLIC_URL", "http://"+taken.Addr().String())
	t.Setenv("NEBULACTRL_LOG_LEVEL", "error")
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	require.ErrorContains(t, serve(ctx), "http server:")
}

func TestRootCommandRegistersEveryOperatorCommand(t *testing.T) {
	root := newRootCmd()
	var names []string
	for _, c := range root.Commands() {
		names = append(names, c.Name())
	}
	require.ElementsMatch(t, []string{
		"serve", "keygen", "openapi", "healthcheck", "restore", "rekey", "reset-password", "reset-2fa", "pre-upgrade-backup",
	}, names)

	withVersion(t, "9.8.7")
	out, _, err := execCmd(t, newRootCmd(), "--version")
	require.NoError(t, err)
	require.Contains(t, out, "9.8.7")

	_, _, err = execCmd(t, newRootCmd(), "serve", "unexpected")
	require.Error(t, err, "serve takes no arguments")
}

// TestMainExitCodes runs the real binary entry point in a child process: main
// prints "error: ..." and exits 1, or with the code an exitError carries.
func TestMainExitCodes(t *testing.T) {
	if os.Getenv("NEBULACTRL_TEST_RUN_MAIN") == "1" {
		os.Args = append([]string{"nebulactrl-server"}, strings.Fields(os.Getenv("NEBULACTRL_TEST_ARGS"))...)
		main()
		return
	}
	pool := testutil.NewPool(t)
	breakInstanceDestination(t, pool)

	run := func(env []string, args string) (code int, stdout, stderr string) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMainExitCodes$")
		cmd.Env = append(os.Environ(), append(env, "NEBULACTRL_TEST_RUN_MAIN=1", "NEBULACTRL_TEST_ARGS="+args)...)
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		err := cmd.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), out.String(), errOut.String()
		}
		require.NoError(t, err)
		return 0, out.String(), errOut.String()
	}

	code, stdout, _ := run(nil, "keygen")
	require.Equal(t, 0, code)
	require.NotEmpty(t, strings.TrimSpace(stdout))

	code, _, stderr := run(nil, "healthcheck --url http://"+freeAddr(t)+"/readyz")
	require.Equal(t, 1, code)
	require.Contains(t, stderr, "error: ")

	code, stdout, stderr = run([]string{
		"NEBULACTRL_DATABASE_URL=" + pool.Config().ConnString(),
		"NEBULACTRL_SECRET_KEY=" + base64.StdEncoding.EncodeToString(newTestKey(t)),
		"NEBULACTRL_BACKUP_DIR=", "NEBULACTRL_FORCE_UPGRADE=",
	}, "pre-upgrade-backup")
	require.Equal(t, 3, code, "a refused backup exits 3, the code the upgrade script waits for: stdout=%s stderr=%s", stdout, stderr)
	require.Contains(t, stdout, "backup before the upgrade FAILED")
	require.Contains(t, stderr, "use --force to override")
}

func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestLogKeyRingWarnsOnlyWhenValuesAreNotOnTheCurrentKey(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()
	oldKey, newKey := newTestKey(t), newTestKey(t)
	oldBox, err := secretbox.New(oldKey)
	require.NoError(t, err)
	ring, err := secretbox.NewRing(newKey, oldKey)
	require.NoError(t, err)

	var buf bytes.Buffer
	logKeyRing(ctx, captureLogger(&buf), pool, nil)
	require.Empty(t, buf.String(), "no key ring, nothing to report")

	logKeyRing(ctx, captureLogger(&buf), pool, ring)
	require.Contains(t, buf.String(), "secret key ring")
	require.Contains(t, buf.String(), "keys=2")
	require.NotContains(t, buf.String(), "run `nebulactrl-server rekey`", "nothing sealed yet, nothing pending")

	sealed, err := oldBox.SealString(secretbox.InstanceBackupSecretKey, secretbox.InstanceRow, "s3-secret")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE instance_backup_settings SET secret_key_enc = $1`, sealed)
	require.NoError(t, err)

	buf.Reset()
	logKeyRing(ctx, captureLogger(&buf), pool, ring)
	require.Contains(t, buf.String(), "level=WARN")
	require.Contains(t, buf.String(), "run `nebulactrl-server rekey`")
	require.Contains(t, buf.String(), "values=1")

	// Once everything is on the current key the warning is gone.
	cur, err := secretbox.New(newKey)
	require.NoError(t, err)
	resealed, err := cur.SealString(secretbox.InstanceBackupSecretKey, secretbox.InstanceRow, "s3-secret")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE instance_backup_settings SET secret_key_enc = $1`, resealed)
	require.NoError(t, err)
	buf.Reset()
	logKeyRing(ctx, captureLogger(&buf), pool, ring)
	require.NotContains(t, buf.String(), "level=WARN")
}

func TestLogKeyRingReportsWhenTheValuesCannotBeCounted(t *testing.T) {
	pool := testutil.NewPool(t)
	ring, err := secretbox.New(newTestKey(t))
	require.NoError(t, err)
	pool.Close()

	var buf bytes.Buffer
	logKeyRing(context.Background(), captureLogger(&buf), pool, ring)
	require.Contains(t, buf.String(), "could not count sealed values per key")
}
