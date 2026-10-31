package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/app"
	appconfig "github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

const (
	canary       = "sekret-canary-12345"
	adminEmail   = "admin@example.com"
	adminPass    = "correct horse battery"
	testPollWait = 20 * time.Millisecond
)

// env is a control plane on a fresh schema plus an isolated CLI config.
type env struct {
	t     *testing.T
	srv   *httptest.Server
	pool  *pgxpool.Pool
	token string
}

func newEnv(t *testing.T) *env { return newEnvWith(t, nil) }

// newEnvWith is newEnv with the control plane's config adjusted by tweak.
func newEnvWith(t *testing.T, tweak func(*appconfig.Config)) *env {
	t.Helper()
	pool := testutil.NewPool(t)
	cfg := &appconfig.Config{SecretKey: bytes.Repeat([]byte{7}, 32), Listen: ":0"}
	if tweak != nil {
		tweak(cfg)
	}
	a, err := app.Build(cfg, pool, nil)
	require.NoError(t, err)
	t.Cleanup(a.Close)
	srv := httptest.NewServer(a.Handler)
	t.Cleanup(srv.Close)

	// Bootstrap the admin and an API token over plain HTTP, like the web UI.
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	hc := &http.Client{Jar: jar}
	post := func(path string, body any) map[string]any {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		res, err := hc.Post(srv.URL+"/api/v1"+path, "application/json", bytes.NewReader(raw))
		require.NoError(t, err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		require.Less(t, res.StatusCode, 300, string(b))
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}
	post("/setup", map[string]any{"email": adminEmail, "password": adminPass, "name": "Admin"})
	// The CLI tests deploy to production: the test token skips the approval a
	// REST token otherwise needs there (the session user is the instance admin).
	tok := post("/tokens", map[string]any{"name": "cli-test", "scopes": []string{"admin"}, "allowProductionWithoutApproval": true})["token"].(string)

	t.Setenv(envConfig, filepath.Join(t.TempDir(), "cfg", "config.json"))
	t.Setenv(envURL, "")
	t.Setenv(envToken, "")
	return &env{t: t, srv: srv, pool: pool, token: tok}
}

// result is one CLI invocation.
type result struct {
	code        int
	stdout, err string
}

func (r result) all() string { return r.stdout + r.err }

func (e *env) run(stdin string, args ...string) result {
	e.t.Helper()
	var out, errOut bytes.Buffer
	c := &cli{in: strings.NewReader(stdin), out: &out, errOut: &errOut, pollEvery: testPollWait}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	code := c.execute(ctx, args)
	return result{code: code, stdout: out.String(), err: errOut.String()}
}

func (e *env) mustRun(stdin string, args ...string) result {
	e.t.Helper()
	r := e.run(stdin, args...)
	require.Equal(e.t, 0, r.code, "args=%v\nstdout=%s\nstderr=%s", args, r.stdout, r.err)
	return r
}

func (e *env) login() {
	e.t.Helper()
	e.mustRun(e.token, "login", "--url", e.srv.URL, "--token", "-")
}

func decode[T any](t *testing.T, s string) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal([]byte(s), &v), s)
	return v
}

// seed creates project "Acme", a server and a whoami service via the CLI and
// returns the service ID.
func (e *env) seed() string {
	e.t.Helper()
	e.mustRun("", "projects", "create", "Acme")
	e.mustRun("", "servers", "enroll-token", "--name", "box1")
	r := e.mustRun("", "--json", "services", "create", "--project", "acme", "--env", "production",
		"--name", "web", "--image", "traefik/whoami:latest", "--port", "80", "--server", "box1")
	return decode[map[string]any](e.t, r.stdout)["id"].(string)
}

// finishNext drives the next active deployment of a service to status as soon
// as the CLI has created it (no job client runs in these tests, so deployments
// stay `created` until something else moves them). Like DeployJob's
// persistAndDispatch, it writes the immutable release and links it to the
// deployment in the same statement, so release-dependent output is real.
func (e *env) finishNext(serviceID, status, message string) {
	e.t.Helper()
	const source = `{"type":"image","image":"traefik/whoami:latest","digest":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`
	go func() {
		for range 1000 {
			time.Sleep(10 * time.Millisecond)
			tag, err := e.pool.Exec(context.Background(),
				`WITH act AS (
				     SELECT id FROM deployments
				     WHERE service_id = $1 AND status NOT IN ('succeeded','failed','canceled','rolled_back','rollback_failed')
				     FOR UPDATE
				 ), rel AS (
				     INSERT INTO releases (service_id, source, config, image_ref)
				     SELECT $1, $4::jsonb, '{}'::jsonb, 'traefik/whoami:latest' FROM act LIMIT 1
				     RETURNING id
				 )
				 UPDATE deployments
				 SET status = $2, status_message = $3, finished_at = now(), release_id = (SELECT id FROM rel)
				 WHERE id IN (SELECT id FROM act)`,
				serviceID, status, message, source)
			if err == nil && tag.RowsAffected() > 0 {
				return
			}
		}
	}()
}

func TestLoginWritesPrivateConfigAndRejectsBadToken(t *testing.T) {
	e := newEnv(t)

	r := e.run("", "login", "--url", e.srv.URL, "--token", "nct_wrong")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "rejected the token")
	path, err := configPath()
	require.NoError(t, err)
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr), "nothing is written for a bad token")

	r = e.mustRun(e.token, "--json", "login", "--url", e.srv.URL+"/", "--token", "-")
	require.Equal(t, adminEmail, decode[map[string]any](t, r.stdout)["email"])
	require.NotContains(t, r.all(), e.token, "login never echoes the token")

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	cfg := decode[config](t, mustRead(t, path))
	require.Equal(t, e.srv.URL, cfg.URL)
	require.Equal(t, e.token, cfg.Token)

	// Commands use the stored config.
	require.Equal(t, 0, e.run("", "projects", "ls").code)
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func TestNotLoggedInAndUsageExitCodes(t *testing.T) {
	e := newEnv(t)
	r := e.run("", "projects", "ls")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "not logged in")

	r = e.run("", "services", "deploy")
	require.Equal(t, 2, r.code, r.err)
	r = e.run("", "nope")
	require.Equal(t, 2, r.code, r.err)
	r = e.run("", "envs", "ls")
	require.Equal(t, 2, r.code, "--project is required: %s", r.err)
}

func TestJSONOutputParses(t *testing.T) {
	e := newEnv(t)
	e.login()
	svcID := e.seed()
	e.mustRun("", "domains", "add", "acme/production/web", "whoami.localhost")

	projects := decode[[]map[string]any](t, e.mustRun("", "--json", "projects", "ls").stdout)
	require.Len(t, projects, 1)
	require.Equal(t, "acme", projects[0]["slug"])

	envs := decode[[]map[string]any](t, e.mustRun("", "--json", "envs", "ls", "--project", "Acme").stdout)
	require.Len(t, envs, 1)
	require.Equal(t, "production", envs[0]["slug"])

	svcs := decode[[]map[string]any](t, e.mustRun("", "--json", "services", "ls", "--project", "acme", "--env", "production").stdout)
	require.Len(t, svcs, 1)
	require.Equal(t, svcID, svcs[0]["id"])

	servers := decode[[]map[string]any](t, e.mustRun("", "--json", "servers", "ls").stdout)
	require.Len(t, servers, 1)
	require.Equal(t, "box1", servers[0]["name"])

	domains := decode[[]map[string]any](t, e.mustRun("", "--json", "domains", "ls", svcID).stdout)
	require.Len(t, domains, 1)
	require.Equal(t, "whoami.localhost", domains[0]["hostname"])

	vars := decode[map[string]any](t, e.mustRun("", "--json", "vars", "ls", svcID).stdout)
	require.Contains(t, vars, "items")
	require.Contains(t, vars, "platform")

	deps := decode[[]any](t, e.mustRun("", "--json", "deployments", "ls", svcID).stdout)
	require.Empty(t, deps)

	// Plain output carries the same data as a table.
	plain := e.mustRun("", "services", "ls", "--project", "acme", "--env", "production").stdout
	require.Contains(t, plain, "NAME")
	require.Contains(t, plain, "traefik/whoami:latest")

	// Lookups by a bad reference fail cleanly.
	r := e.run("", "services", "restart", "acme/production/missing")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "not found")
}

func TestVarsSetSecretNeverEchoesValue(t *testing.T) {
	e := newEnv(t)
	e.login()
	svcID := e.seed()

	// Value from argv.
	r := e.mustRun("", "vars", "set", svcID, "API_KEY="+canary, "--secret")
	require.NotContains(t, r.all(), canary)
	require.Contains(t, r.stdout, "API_KEY (secret)")

	// Value from stdin, in JSON mode.
	r = e.mustRun(canary+"\n", "--json", "vars", "set", svcID, "STDIN_KEY=-", "--secret")
	require.NotContains(t, r.all(), canary)
	decode[map[string]any](t, r.stdout)

	// A plain variable is stored readable; secrets stay masked on read.
	e.mustRun("", "vars", "set", svcID, "MODE=production")
	r = e.mustRun("", "vars", "ls", svcID)
	require.NotContains(t, r.all(), canary)
	require.Contains(t, r.stdout, "production")
	require.Contains(t, r.stdout, secretMask)
	r = e.mustRun("", "--json", "vars", "ls", svcID)
	require.NotContains(t, r.all(), canary)

	// Re-setting a secret without --secret keeps it secret.
	r = e.mustRun("", "vars", "set", svcID, "API_KEY="+canary+"-rotated")
	require.NotContains(t, r.all(), canary)
	require.Contains(t, r.err, "stays one")
	require.NotContains(t, e.mustRun("", "--json", "vars", "ls", svcID).all(), canary)

	// Failures never echo the value either (invalid key; malformed argument).
	r = e.run("", "vars", "set", svcID, "bad key="+canary, "--secret")
	require.NotEqual(t, 0, r.code)
	require.NotContains(t, r.all(), canary)
	r = e.run("", "vars", "set", svcID, canary, "--secret")
	require.Equal(t, 2, r.code)
	require.NotContains(t, r.all(), canary)

	// The value really was stored as a write-only secret.
	var secret bool
	require.NoError(t, e.pool.QueryRow(context.Background(),
		"SELECT is_secret FROM service_variables WHERE service_id = $1 AND key = 'API_KEY'", svcID).Scan(&secret))
	require.True(t, secret)

	e.mustRun("", "vars", "rm", svcID, "API_KEY", "STDIN_KEY")
	require.NotContains(t, e.mustRun("", "vars", "ls", svcID).stdout, "API_KEY")
}

func TestDeployWaitExitsNonZeroOnFailed(t *testing.T) {
	e := newEnv(t)
	e.login()
	svcID := e.seed()

	e.finishNext(svcID, "failed", "Not delivered: server offline. No changes were made.")
	r := e.run("", "services", "deploy", "acme/production/web", "--wait", "--timeout", "30s")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "failed")
	require.Contains(t, r.err, "Not delivered")

	// --json still prints the final deployment on failure.
	e.finishNext(svcID, "failed", "boom")
	r = e.run("", "--json", "services", "deploy", svcID, "--wait")
	require.Equal(t, 1, r.code, r.all())
	require.Equal(t, "failed", decode[map[string]any](t, r.stdout)["status"])

	// Every other non-succeeded terminal status is a failure too.
	for _, status := range []string{"rolled_back", "canceled", "rollback_failed"} {
		e.finishNext(svcID, status, "")
		r = e.run("", "services", "deploy", svcID, "--wait")
		require.Equal(t, 1, r.code, "%s: %s", status, r.all())
		require.Contains(t, r.err, status)
	}
}

func TestDeployWaitExitsZeroOnlyOnSucceeded(t *testing.T) {
	e := newEnv(t)
	e.login()
	svcID := e.seed()

	e.finishNext(svcID, "succeeded", "")
	r := e.run("", "services", "deploy", svcID, "--wait")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "succeeded")

	deps := decode[[]map[string]any](t, e.mustRun("", "--json", "deployments", "ls", svcID).stdout)
	require.Len(t, deps, 1)
	st := e.mustRun("", "deployments", "status", deps[0]["id"].(string))
	require.Contains(t, st.stdout, "succeeded")
	require.Contains(t, st.stdout, "traefik/whoami")
}

func TestWaitTimesOutWithNonZeroExit(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.seed()
	r := e.run("", "services", "deploy", "acme/production/web", "--wait", "--timeout", "300ms")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "timed out")
}

func TestIdempotencyKeyReplaysCreate(t *testing.T) {
	e := newEnv(t)
	e.login()
	a := decode[map[string]any](t, e.mustRun("", "--json", "--idempotency-key", "k1", "projects", "create", "Same").stdout)
	b := decode[map[string]any](t, e.mustRun("", "--json", "--idempotency-key", "k1", "projects", "create", "Same").stdout)
	require.Equal(t, a["id"], b["id"])

	// Without a key every invocation is a new request: a second project is
	// created and the server derives a unique slug for it (same-2).
	c := decode[map[string]any](t, e.mustRun("", "--json", "projects", "create", "Same").stdout)
	require.NotEqual(t, a["id"], c["id"])
	require.Equal(t, "same", a["slug"])
	require.Equal(t, "same-2", c["slug"])
}

func TestServicesLogsReportsNotDeployed(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.seed()
	r := e.run("", "services", "logs", "acme/production/web", "--tail", "5")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "not_deployed")
	r = e.run("", "services", "logs", "acme/production/web", "-f")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "not_deployed")
}

func TestServersEnrollTokenAndRevoke(t *testing.T) {
	e := newEnv(t)
	e.login()
	r := e.mustRun("", "servers", "enroll-token", "--name", "edge-1", "--public-ip", "203.0.113.7")
	require.Contains(t, r.stdout, "nct_enroll_")
	require.Contains(t, r.stdout, "Run on the host")

	r = e.mustRun("", "--json", "servers", "revoke", "edge-1")
	require.Equal(t, "revoked", decode[map[string]any](t, r.stdout)["status"])
}

func TestServicesScaleAndReplicasColumn(t *testing.T) {
	e := newEnv(t)
	e.login()
	id := e.seed()

	ls := e.mustRun("", "services", "ls", "--project", "acme", "--env", "production")
	require.Contains(t, ls.stdout, "REPLICAS")
	require.Contains(t, ls.stdout, "0/1", "ready/desired")

	r := e.mustRun("", "--json", "services", "scale", "acme/production/web", "3")
	sc := decode[map[string]any](t, r.stdout)["scaling"].(map[string]any)
	require.EqualValues(t, 3, sc["replicas"])
	ls = e.mustRun("", "services", "ls", "--project", "acme", "--env", "production")
	require.Contains(t, ls.stdout, "0/3")

	// Autoscaling replaces manual scaling.
	r = e.mustRun("", "services", "scale", id, "--autoscale", "1:5:60")
	require.Contains(t, r.stdout, "Autoscaling 1-5 replicas at 60% CPU")
	manual := e.run("", "services", "scale", id, "2")
	require.NotEqual(t, 0, manual.code)
	require.Contains(t, manual.all(), "autoscaling_enabled")
	e.mustRun("", "services", "scale", id, "--autoscale", "off")
	r = e.mustRun("", "services", "scale", id, "2")
	require.Contains(t, r.stdout, "Replicas: 2 desired")

	require.Equal(t, 2, e.run("", "services", "scale", id).code, "needs <replicas> or --autoscale")
	require.Equal(t, 2, e.run("", "services", "scale", id, "2", "--autoscale", "1:2").code)
	require.Equal(t, 2, e.run("", "services", "scale", id, "11").code)
	require.Equal(t, 2, e.run("", "services", "scale", id, "--autoscale", "x:y").code)
}
