package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
)

const (
	cfgProjectID = "11111111-aaaa-4aaa-8aaa-111111111111"
	cfgProdID    = "22222222-bbbb-4bbb-8bbb-222222222222"
	cfgStagingID = "33333333-cccc-4ccc-8ccc-333333333333"

	cfgAPI          = "/api/v1"
	cfgProjectPath  = cfgAPI + "/projects/" + cfgProjectID
	cfgValidatePath = cfgProjectPath + "/config/validate"
	cfgPlanPath     = cfgProjectPath + "/config/plan"
	cfgApplyPath    = cfgProjectPath + "/config/apply"
	cfgExportPath   = cfgProjectPath + "/config/export"
	cfgSourcePath   = cfgAPI + "/environments/" + cfgStagingID + "/config-source"
)

// A small file that is valid offline: one image service.
const cfgValidFile = `schema = 1
project = "acme-shop"

[services.web]
image = "traefik/whoami:latest"
port = 80
`

// The same file with an unknown key on line 6.
const cfgUnknownKeyFile = `schema = 1
project = "acme-shop"

[services.web]
image = "traefik/whoami:latest"
bogus_key = 1
`

// ---- fake control plane -----------------------------------------------------

type cfgReq struct {
	method, path, query string
	header              http.Header
	body                []byte
}

func (r cfgReq) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(r.body, &m), string(r.body))
	return m
}

func (r cfgReq) values(t *testing.T) url.Values {
	t.Helper()
	v, err := url.ParseQuery(r.query)
	require.NoError(t, err)
	return v
}

type cfgReply struct {
	status int
	ctype  string
	body   string
}

func jsonReply(status int, v any) cfgReply {
	if s, ok := v.(string); ok {
		return cfgReply{status, "application/json", s}
	}
	return cfgReply{status, "application/json", mustJSON(v)}
}

func problemReply(status int, code, detail string, details map[string]any) cfgReply {
	m := map[string]any{"code": code, "status": status, "title": http.StatusText(status), "detail": detail}
	if details != nil {
		m["details"] = details
	}
	return cfgReply{status, "application/problem+json", mustJSON(m)}
}

// cfgFake is the control plane's config API, scripted. It also serves the
// project and environment lookups every command starts with.
type cfgFake struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []cfgReq
	routes map[string]func(cfgReq) cfgReply
}

func newCfgFake(t *testing.T) *cfgFake {
	t.Helper()
	f := &cfgFake{t: t, routes: map[string]func(cfgReq) cfgReply{}}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	cfgDir(t) // an empty config and working directory: no login and no stray nebula.toml above the tests
	f.reply("GET", cfgAPI+"/projects", 200, map[string]any{"items": []any{
		map[string]any{"id": cfgProjectID, "slug": "acme-shop", "name": "Acme Shop"},
	}})
	f.reply("GET", cfgProjectPath+"/environments", 200, map[string]any{"items": []any{
		map[string]any{"id": cfgProdID, "slug": "production", "name": "Production", "kind": "standard", "isProduction": true},
		map[string]any{"id": cfgStagingID, "slug": "staging", "name": "Staging", "kind": "standard"},
	}})
	return f
}

func (f *cfgFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	req := cfgReq{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, header: r.Header.Clone(), body: body}
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	fn := f.routes[r.Method+" "+r.URL.Path]
	f.mu.Unlock()
	rep := problemReply(404, "not_found", "no such route in the fake: "+r.Method+" "+r.URL.Path, nil)
	if fn != nil {
		rep = fn(req)
	}
	w.Header().Set("Content-Type", rep.ctype)
	w.WriteHeader(rep.status)
	_, _ = io.WriteString(w, rep.body)
}

func (f *cfgFake) handle(method, path string, fn func(cfgReq) cfgReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[method+" "+path] = fn
}

func (f *cfgFake) reply(method, path string, status int, v any) {
	rep := jsonReply(status, v)
	f.handle(method, path, func(cfgReq) cfgReply { return rep })
}

func (f *cfgFake) replyRaw(method, path string, rep cfgReply) {
	f.handle(method, path, func(cfgReq) cfgReply { return rep })
}

func (f *cfgFake) calls(method, path string) []cfgReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []cfgReq
	for _, r := range f.reqs {
		if r.method == method && r.path == path {
			out = append(out, r)
		}
	}
	return out
}

func (f *cfgFake) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func (f *cfgFake) run(args ...string) result {
	f.t.Helper()
	var out, errOut bytes.Buffer
	c := &cli{in: strings.NewReader(""), out: &out, errOut: &errOut, pollEvery: time.Millisecond}
	require.NoError(f.t, c.dial(config{URL: f.srv.URL, Token: "nct_test"}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code := c.execute(ctx, args)
	return result{code: code, stdout: out.String(), err: errOut.String()}
}

// cfgRunOffline runs a command without any control plane: the client is not
// connected, so a command that needs the API fails with "not logged in".
func cfgRunOffline(t *testing.T, args ...string) result {
	t.Helper()
	cfgEnv(t)
	var out, errOut bytes.Buffer
	c := &cli{in: strings.NewReader(""), out: &out, errOut: &errOut, pollEvery: time.Millisecond}
	code := c.execute(context.Background(), args)
	return result{code: code, stdout: out.String(), err: errOut.String()}
}

// cfgEnv points the CLI at an empty config file so nothing of the developer's
// own login is read.
func cfgEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envConfig, filepath.Join(t.TempDir(), "cfg", "config.json"))
	t.Setenv(envURL, "")
	t.Setenv(envToken, "")
}

// cfgDir is cfgEnv plus an empty working directory.
func cfgDir(t *testing.T) {
	t.Helper()
	cfgEnv(t)
	t.Chdir(t.TempDir())
}

func cfgWrite(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func cfgRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	cfgEnv(t)
	dir := t.TempDir()
	for name, content := range files {
		cfgWrite(t, filepath.Join(dir, name), content)
	}
	t.Chdir(dir)
	return dir
}

// ---- fixtures ---------------------------------------------------------------

// A plan with a new, an updated, an unchanged and an unlisted service, and a warning.
const cfgGoldenPlan = `{
  "planId": "pl_7d3c1f0a9b2e4c55e6a1d8f0b3c47a92", "schema": 1, "valid": true,
  "project": {"id": "11111111-aaaa-4aaa-8aaa-111111111111", "slug": "acme-shop"},
  "environment": {"id": "22222222-bbbb-4bbb-8bbb-222222222222", "slug": "production", "isProduction": true},
  "source": {"kind": "git", "repo": "acme/shop", "branch": "main", "path": "nebula.toml",
             "commit": "3f9c2a1d5e8b7c6a9f0e1d2c3b4a596877665544", "fileSha256": "ab", "bytes": 14538},
  "summary": {"create": 1, "update": 1, "unchanged": 1, "notInFile": 1, "adopted": 0, "staged": 3, "immediate": 1,
              "deferred": 0, "pruned": 0, "errors": 0, "warnings": 1, "requiresDeploy": ["web"]},
  "diagnostics": [{"severity": "warning", "code": "secret_missing_value", "message": "STRIPE_SECRET_KEY has no value yet",
                   "path": "services.web.secrets.keys[0]", "line": 31, "column": 9}],
  "services": [
    {"slug": "queue", "kind": "image", "action": "create", "managed": false, "state": "new", "changes": []},
    {"slug": "web", "kind": "git", "action": "update", "managed": true, "state": "file_changed", "changes": [
      {"path": "services.web.port", "line": 14, "field": "config.port", "op": "set", "applies": "staged",
       "reason": "file_changed", "from": "3000", "to": "8080", "masked": false},
      {"path": "services.web.variables.NODE_ENV", "field": "variables.NODE_ENV", "op": "set", "applies": "staged",
       "reason": "drift", "from": "development", "to": "production", "masked": false},
      {"path": "services.web.variables.SESSION_SECRET", "field": "variables.SESSION_SECRET", "op": "add",
       "applies": "staged", "reason": "new", "from": "", "to": "generated(secret(48))", "masked": true},
      {"path": "services.web.scale.replicas", "field": "scale.replicas", "op": "set", "applies": "immediate",
       "reason": "file_changed", "from": "1", "to": "2", "masked": false}]},
    {"slug": "worker", "kind": "git", "action": "unchanged", "managed": true, "state": "in_sync", "changes": []},
    {"slug": "legacy-cron", "kind": "image", "action": "not_in_file", "managed": false, "changes": []}],
  "shared": {"project": [], "environment": []},
  "requiredScopes": ["projects:write"],
  "guard": {"productionApproval": "not_required"}
}`

// cfgGoldenText is the human rendering of cfgGoldenPlan, character for
// character.
const cfgGoldenText = `Plan for acme-shop / production        nebula.toml @ 3f9c2a1 (14.2 KB)

  + services.queue              new image service                         staged
  ~ services.web
      ~ port                    3000 → 8080                               staged
      ~ variables.NODE_ENV      "development" → "production"              staged    edited in dashboard since last apply
      + variables.SESSION_SECRET  generated(secret(48))                   staged
      ~ scale.replicas          1 → 2                                     applies at once
  = services.worker             no changes
  ? services.legacy-cron        not in file (kept)

  1 warning:
    nebula.toml:31:9: warning[secret_missing_value] services.web.secrets.keys[0]: STRIPE_SECRET_KEY has no value yet

1 create, 1 update (3 staged, 1 immediate), 1 unchanged, 1 not in file.
Plan id pl_7d3c1f0a… — pass --plan-id to apply to fail if anything changed meanwhile.
`

// cfgPlanFor builds a small plan: one service, either changed (a staged port
// change) or not, and valid or carrying one error.
func cfgPlanFor(envSlug string, valid, changed bool) map[string]any {
	summary := map[string]any{"create": 0, "update": 0, "unchanged": 0, "notInFile": 0, "staged": 0, "immediate": 0,
		"deferred": 0, "pruned": 0, "errors": 0, "warnings": 0, "requiresDeploy": []any{}}
	svc := map[string]any{"slug": "web", "kind": "image", "action": "unchanged", "managed": true, "changes": []any{}}
	if changed {
		summary["update"], summary["staged"], summary["requiresDeploy"] = 1, 1, []any{"web"}
		svc["action"] = "update"
		svc["changes"] = []any{map[string]any{"path": "services.web.port", "field": "config.port", "op": "set",
			"applies": "staged", "reason": "file_changed", "from": "80", "to": "8080"}}
	} else {
		summary["unchanged"] = 1
	}
	diags := []any{}
	if !valid {
		summary["errors"] = 1
		diags = append(diags, map[string]any{"severity": "error", "code": "unknown_server",
			"message": `unknown server "fra-9"`, "path": "services.web.server", "line": 5, "column": 10})
	}
	return map[string]any{
		"planId": "pl_" + strings.Repeat("a", 32), "schema": 1, "valid": valid,
		"project":     map[string]any{"id": cfgProjectID, "slug": "acme-shop"},
		"environment": map[string]any{"id": cfgStagingID, "slug": envSlug, "isProduction": envSlug == "production"},
		"source":      map[string]any{"kind": "inline", "fileSha256": "ab", "bytes": 120},
		"summary":     summary, "diagnostics": diags, "services": []any{svc},
		"shared":         map[string]any{"project": []any{}, "environment": []any{}},
		"requiredScopes": []any{"projects:write"}, "guard": map[string]any{"productionApproval": "not_required"},
	}
}

func cfgApplyFor(status string, steps []map[string]any, deployments []string) map[string]any {
	st := make([]any, len(steps))
	for i, s := range steps {
		st[i] = s
	}
	deps := make([]any, len(deployments))
	for i, d := range deployments {
		deps[i] = d
	}
	return map[string]any{
		"planId": "pl_" + strings.Repeat("a", 32), "status": status, "steps": st, "deployments": deps,
		"services": []any{map[string]any{"slug": "web", "serviceId": "svc-1", "action": "update", "generation": 13}},
		"plan":     cfgPlanFor("staging", true, true),
	}
}

func cfgStep(step, status, msg string) map[string]any {
	return map[string]any{"step": step, "status": status, "message": msg}
}

// ---- plan -------------------------------------------------------------------

func TestConfigPlanTextMatchesTheGolden(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	f.reply("POST", cfgPlanPath, 200, cfgGoldenPlan)

	r := f.run("config", "plan", "--env", "production")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, cfgGoldenText, r.stdout)

	reqs := f.calls("POST", cfgPlanPath)
	require.Len(t, reqs, 1)
	body := reqs[0].json(t)
	require.Equal(t, "production", body["environment"])
	require.Equal(t, cfgValidFile, body["source"].(map[string]any)["file"])
	require.NotContains(t, body, "planId")
	require.NotContains(t, body, "prune")
	require.Equal(t, "Bearer nct_test", reqs[0].header.Get("Authorization"))
}

func TestConfigPlanJSONIsTheAPIBody(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	f.reply("POST", cfgPlanPath, 200, cfgGoldenPlan)

	r := f.run("--json", "config", "plan", "--env", "production")
	require.Equal(t, 0, r.code, r.all())
	require.JSONEq(t, cfgGoldenPlan, r.stdout)
	require.Empty(t, r.err, "progress and hints stay off stdout and, in JSON mode, are not printed at all")
}

func TestConfigPlanDetailedExitCode(t *testing.T) {
	for _, tc := range []struct {
		name            string
		valid, changed  bool
		detailed        bool
		wantCode        int
		wantPlanMessage string
	}{
		{"no changes", true, false, true, 0, ""},
		{"changes pending", true, true, true, 3, ""},
		{"changes pending without the flag", true, true, false, 0, ""},
		{"invalid", false, true, true, 1, "The plan is not valid"},
		{"invalid without the flag", false, true, false, 1, "The plan is not valid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCfgFake(t)
			cfgWrite(t, "nebula.toml", cfgValidFile)
			f.reply("POST", cfgPlanPath, 200, cfgPlanFor("staging", tc.valid, tc.changed))
			args := []string{"config", "plan", "--env", "staging"}
			if tc.detailed {
				args = append(args, "--detailed-exit-code")
			}
			r := f.run(args...)
			require.Equal(t, tc.wantCode, r.code, r.all())
			require.Contains(t, r.stdout, "Plan for acme-shop / staging")
			if tc.wantPlanMessage != "" {
				require.Contains(t, r.stdout, tc.wantPlanMessage)
				require.Contains(t, r.stdout, "nebula.toml:5:10: error[unknown_server] services.web.server: unknown server \"fra-9\"")
			}
			require.Empty(t, r.err, "exit codes carry the result; nothing is printed as an error")
		})
	}
}

func TestConfigPlanDetailedExitCodeUsageIs2(t *testing.T) {
	f := newCfgFake(t)
	r := f.run("config", "plan", "--no-such-flag")
	require.Equal(t, 2, r.code, r.all())
	require.Zero(t, f.total())
}

func TestConfigPlanSendsOnlyAndPrune(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	f.reply("POST", cfgPlanPath, 200, cfgPlanFor("staging", true, false))

	r := f.run("config", "plan", "--env", "staging", "--only", "services.web", "--only", "variables.APP_NAME", "--prune")
	require.Equal(t, 0, r.code, r.all())
	body := f.calls("POST", cfgPlanPath)[0].json(t)
	require.Equal(t, []any{"services.web", "variables.APP_NAME"}, body["only"])
	require.Equal(t, true, body["prune"])
}

func TestConfigPlanFromGitSendsNoFile(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", cfgPlanPath, 200, cfgPlanFor("staging", true, false))

	r := f.run("config", "plan", "--from-git", "--project", "acme-shop", "--env", "staging")
	require.Equal(t, 0, r.code, r.all())
	src := f.calls("POST", cfgPlanPath)[0].json(t)["source"].(map[string]any)
	require.Equal(t, map[string]any{"git": map[string]any{"ref": nil}}, src)

	r = f.run("config", "plan", "--from-git", "--env", "staging")
	require.Equal(t, 2, r.code, "--from-git has no local file to take the project from\n"+r.all())
	cfgWrite(t, "nebula.toml", cfgValidFile)
	r = f.run("config", "plan", "--from-git", "--project", "acme-shop", "--file", "nebula.toml")
	require.Equal(t, 2, r.code, r.all())
}

func TestConfigPlanMultipleEnvironments(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	f.handle("POST", cfgPlanPath, func(r cfgReq) cfgReply {
		return jsonReply(200, cfgPlanFor(r.json(t)["environment"].(string), true, true))
	})

	r := f.run("config", "plan", "--env", "production", "--env", "staging", "--detailed-exit-code", "--plan-out", "plan.json")
	require.Equal(t, 3, r.code, r.all())
	require.Contains(t, r.stdout, "Plan for acme-shop / production")
	require.Contains(t, r.stdout, "Plan for acme-shop / staging")
	require.Len(t, f.calls("POST", cfgPlanPath), 2)
	for _, env := range []string{"production", "staging"} {
		raw, err := os.ReadFile("plan." + env + ".json")
		require.NoError(t, err)
		require.Contains(t, string(raw), `"slug": "`+env+`"`)
	}
	require.NoFileExists(t, "plan.json")

	r = f.run("--json", "config", "plan", "--env", "production", "--env", "staging")
	require.Equal(t, 0, r.code, r.all())
	var plans []map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &plans), r.stdout)
	require.Len(t, plans, 2)
	require.Equal(t, "production", plans[0]["environment"].(map[string]any)["slug"])
	require.Equal(t, "staging", plans[1]["environment"].(map[string]any)["slug"])
}

func TestConfigPlanOneEnvironmentFailingDoesNotHideTheOther(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	f.handle("POST", cfgPlanPath, func(r cfgReq) cfgReply {
		if r.json(t)["environment"] == "staging" {
			return problemReply(500, "internal_error", "boom", nil)
		}
		return jsonReply(200, cfgPlanFor("production", true, false))
	})

	r := f.run("config", "plan", "--env", "production", "--env", "staging")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.stdout, "Plan for acme-shop / production")
	require.Contains(t, r.err, "environment staging")
	require.Contains(t, r.err, "boom")
}

func TestConfigPlanWritesAPrivatePlanFileThatApplyAccepts(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	plan := cfgPlanFor("staging", true, true)
	f.reply("POST", cfgPlanPath, 200, plan)
	f.reply("POST", cfgApplyPath, 201, cfgApplyFor("staged", []map[string]any{cfgStep("config:web", "applied", "")}, nil))

	r := f.run("config", "plan", "--env", "staging", "--plan-out", "plan.json")
	require.Equal(t, 0, r.code, r.all())
	raw, err := os.ReadFile("plan.json")
	require.NoError(t, err)
	require.JSONEq(t, mustJSON(plan), string(raw))
	st, err := os.Stat("plan.json")
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm())

	// The environment and the plan id come from the file.
	r = f.run("config", "apply", "--plan", "plan.json")
	require.Equal(t, 0, r.code, r.all())
	body := f.calls("POST", cfgApplyPath)[0].json(t)
	require.Equal(t, "pl_"+strings.Repeat("a", 32), body["planId"])
	require.Equal(t, "staging", body["environment"])

	r = f.run("config", "apply", "--plan", "plan.json", "--env", "production")
	require.Equal(t, 2, r.code, "a plan belongs to the environment it was made for\n"+r.all())
	require.Len(t, f.calls("POST", cfgApplyPath), 1)
}

func TestConfigApplyRefusesAPlanFileThatIsNotValid(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	cfgWrite(t, "bad.json", mustJSON(cfgPlanFor("staging", false, true)))
	cfgWrite(t, "junk.json", `{"hello":"world"}`)

	r := f.run("config", "apply", "--plan", "bad.json")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "not valid")
	r = f.run("config", "apply", "--plan", "junk.json")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "not a plan")
	require.Zero(t, f.total())
}

func TestConfigPlanRendersNoTerminalControlCharacters(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	p := cfgPlanFor("staging", true, true)
	p["diagnostics"] = []any{map[string]any{"severity": "warning", "code": "x", "message": "\x1b[31mred\x1b[0m",
		"path": "services.web\x1b]0;pwn\x07", "line": 2, "column": 1}}
	p["services"].([]any)[0].(map[string]any)["changes"] = []any{map[string]any{"path": "services.web.command", "field": "config.startCommand",
		"op": "set", "applies": "staged", "from": "a", "to": "b\x1b[2Jc\nd"},
		map[string]any{"path": "services.web.variables.X", "field": "variables.X", "op": "set", "applies": "staged", "from": "", "to": "\x1b[2J"}}
	f.reply("POST", cfgPlanPath, 200, p)

	r := f.run("config", "plan", "--env", "staging")
	require.Equal(t, 0, r.code, r.all())
	require.NotContains(t, r.stdout, "\x1b")
	require.NotContains(t, r.stdout, "\x07")
	require.Contains(t, r.stdout, `"\x1b[2J"`, "variable values are quoted, escapes visible")
}

// ---- apply ------------------------------------------------------------------

func TestConfigApplyShowsThePlanThenTheResult(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	f.reply("POST", cfgApplyPath, 201, cfgApplyFor("staged", []map[string]any{
		cfgStep("config:web", "applied", ""), cfgStep("scale:web", "deferred", "service is not deployed yet"),
	}, nil))

	r := f.run("config", "apply", "--env", "staging")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "Plan for acme-shop / staging")
	require.Contains(t, r.stdout, "\n\nResult: staged.\n")
	require.Contains(t, r.stdout, "\n  applied       config:web\n")
	require.Contains(t, r.stdout, "\n  deferred      scale:web  service is not deployed yet\n")
	require.Contains(t, r.stdout, "\nServices: web (update, generation 13)\n")
	require.NotContains(t, r.stdout, "Deployments:")
	require.Contains(t, r.err, "Staged changes take effect on the next deploy of web")

	body := f.calls("POST", cfgApplyPath)[0].json(t)
	require.NotContains(t, body, "deploy", "apply never deploys unless asked")
	require.NotContains(t, body, "planId")
}

func TestConfigApplyJSONIsTheAPIBody(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	body := cfgApplyFor("staged", []map[string]any{cfgStep("config:web", "applied", "")}, nil)
	f.reply("POST", cfgApplyPath, 201, body)

	r := f.run("--json", "config", "apply", "--env", "staging")
	require.Equal(t, 0, r.code, r.all())
	require.JSONEq(t, mustJSON(body), r.stdout)
	require.Empty(t, r.err)
}

func TestConfigApplyExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		result   string
		steps    []map[string]any
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{"staged", 201, "staged", nil, 0, "Result: staged.", ""},
		{"nothing to do", 200, "noop", nil, 0, "Result: nothing to apply.", ""},
		{"partial", 201, "partial", []map[string]any{cfgStep("uptime:web", "failed", "needs admin"), cfgStep("tasks:web:digest", "not_attempted", "")},
			1, "failed        uptime:web  needs admin", "apply partial: 1 step failed"},
		{"failed", 200, "failed", []map[string]any{cfgStep("config:web", "failed", "boom")}, 1, "Result: failed", "apply failed: 1 step failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCfgFake(t)
			cfgWrite(t, "nebula.toml", cfgValidFile)
			f.reply("POST", cfgApplyPath, tc.status, cfgApplyFor(tc.result, tc.steps, nil))
			r := f.run("config", "apply", "--env", "staging")
			require.Equal(t, tc.wantCode, r.code, r.all())
			require.Contains(t, r.stdout, tc.wantOut)
			if tc.wantErr != "" {
				require.Contains(t, r.err, tc.wantErr)
			}
		})
	}
}

func TestConfigApplyChecksFlagsBeforeTalkingToTheServer(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	for name, args := range map[string][]string{
		"prune needs yes":     {"config", "apply", "--env", "staging", "--prune"},
		"wait needs deploy":   {"config", "apply", "--env", "staging", "--wait"},
		"plan id and plan":    {"config", "apply", "--env", "staging", "--plan-id", "pl_x", "--plan", "p.json"},
		"plan id of two envs": {"config", "apply", "--env", "staging", "--env", "production", "--plan-id", "pl_x"},
		"unknown flag":        {"config", "apply", "--nope"},
		"positional argument": {"config", "apply", "stray"},
		"wait with yes":       {"config", "apply", "--env", "staging", "--wait", "--yes"},
	} {
		r := f.run(args...)
		require.Equal(t, 2, r.code, "%s\n%s", name, r.all())
	}
	require.Empty(t, f.calls("POST", cfgApplyPath))
}

func TestConfigApplyPruneWithYesIsSent(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	f.reply("POST", cfgApplyPath, 201, cfgApplyFor("staged", nil, nil))
	r := f.run("config", "apply", "--env", "staging", "--prune", "--yes", "--only", "services.web")
	require.Equal(t, 0, r.code, r.all())
	body := f.calls("POST", cfgApplyPath)[0].json(t)
	require.Equal(t, true, body["prune"])
	require.Equal(t, []any{"services.web"}, body["only"])
}

func TestConfigApplySendsPlanIDAndIdempotencyKey(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	f.reply("POST", cfgApplyPath, 201, cfgApplyFor("staged", nil, nil))

	r := f.run("--idempotency-key", "key-1", "config", "apply", "--env", "staging", "--plan-id", "pl_abc")
	require.Equal(t, 0, r.code, r.all())
	req := f.calls("POST", cfgApplyPath)[0]
	require.Equal(t, "key-1", req.header.Get("Idempotency-Key"))
	require.Equal(t, "pl_abc", req.json(t)["planId"])

	// Several environments: one key per environment, derived from the given one.
	r = f.run("--idempotency-key", "key-2", "config", "apply", "--env", "production", "--env", "staging")
	require.Equal(t, 0, r.code, r.all())
	reqs := f.calls("POST", cfgApplyPath)
	require.Len(t, reqs, 3)
	require.Equal(t, "key-2:production", reqs[1].header.Get("Idempotency-Key"))
	require.Equal(t, "key-2:staging", reqs[2].header.Get("Idempotency-Key"))

	// Without a key every call gets a fresh random one.
	r = f.run("config", "apply", "--env", "staging")
	require.Equal(t, 0, r.code, r.all())
	reqs = f.calls("POST", cfgApplyPath)
	require.NotEmpty(t, reqs[3].header.Get("Idempotency-Key"))
}

func TestConfigApplyStalePlanPrintsTheFreshPlan(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	fresh := cfgPlanFor("staging", true, true)
	f.replyRaw("POST", cfgApplyPath, problemReply(409, "plan_stale", "the environment changed since the plan was made",
		map[string]any{"plan": fresh}))

	r := f.run("config", "apply", "--env", "staging", "--plan-id", "pl_old")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.stdout, "Plan for acme-shop / staging", "the fresh plan is shown to review")
	require.Contains(t, r.err, "plan_stale")
	require.Contains(t, r.err, "the environment changed since the plan was made")

	r = f.run("--json", "config", "apply", "--env", "staging", "--plan-id", "pl_old")
	require.Equal(t, 1, r.code, r.all())
	require.JSONEq(t, mustJSON(fresh), r.stdout, "in JSON mode stdout holds the fresh plan")
}

func TestConfigApplyBlockedPrintsThePlanWithItsDiagnostics(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	f.replyRaw("POST", cfgApplyPath, problemReply(422, "config_blocked", "the plan has errors",
		map[string]any{"plan": cfgPlanFor("staging", false, true)}))
	r := f.run("config", "apply", "--env", "staging")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.stdout, "error[unknown_server]")
	require.Contains(t, r.err, "config_blocked")
}

func TestConfigApplyShowsMissingScopesAndApprovalLinks(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	f.replyRaw("POST", cfgApplyPath, problemReply(403, "insufficient_scope", "the token cannot apply this plan",
		map[string]any{"scopes": []any{"projects:write", "admin"}, "approvalUrl": "https://nebula.example.com/approvals/7"}))
	r := f.run("config", "apply", "--env", "staging")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "insufficient_scope")
	require.Contains(t, r.err, "scopes: projects:write, admin")
	require.Contains(t, r.err, "approvalUrl: https://nebula.example.com/approvals/7")
}

func TestConfigApplyDeployWait(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	body := cfgApplyFor("staged", nil, []string{"dep-1", "dep-2"})
	f.reply("POST", cfgApplyPath, 201, body)
	f.reply("GET", cfgAPI+"/deployments/dep-1", 200, map[string]any{"id": "dep-1", "status": "succeeded", "strategy": "recreate"})
	f.reply("GET", cfgAPI+"/deployments/dep-2", 200, map[string]any{"id": "dep-2", "status": "succeeded", "strategy": "recreate"})

	r := f.run("config", "apply", "--env", "staging", "--deploy", "--wait")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, true, f.calls("POST", cfgApplyPath)[0].json(t)["deploy"])
	require.Contains(t, r.stdout, "Deployments: dep-1, dep-2")
	require.Contains(t, r.err+r.stdout, "Deployment dep-1 succeeded")
	require.Contains(t, r.err+r.stdout, "Deployment dep-2 succeeded")

	// JSON mode keeps stdout to the apply body; the waiting reports on stderr.
	r = f.run("--json", "config", "apply", "--env", "staging", "--deploy", "--wait")
	require.Equal(t, 0, r.code, r.all())
	require.JSONEq(t, mustJSON(body), r.stdout)

	// A deployment that fails fails the command.
	f.reply("GET", cfgAPI+"/deployments/dep-2", 200, map[string]any{"id": "dep-2", "status": "failed", "statusMessage": "boom", "strategy": "recreate"})
	r = f.run("config", "apply", "--env", "staging", "--deploy", "--wait")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "deployment dep-2 failed: boom")

	// Without --wait nothing is polled.
	before := len(f.calls("GET", cfgAPI+"/deployments/dep-1"))
	r = f.run("config", "apply", "--env", "staging", "--deploy")
	require.Equal(t, 0, r.code, r.all())
	require.Len(t, f.calls("GET", cfgAPI+"/deployments/dep-1"), before)
}

// ---- validate ---------------------------------------------------------------

func TestConfigValidateRendersDiagnostics(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgUnknownKeyFile)
	f.reply("POST", cfgValidatePath, 200, map[string]any{
		"valid": false, "schema": 1, "environments": []any{}, "services": []any{},
		"diagnostics": []any{
			map[string]any{"severity": "warning", "code": "unresolved_reference", "message": "no value yet", "path": "services.web.variables.X"},
			map[string]any{"severity": "error", "code": "unknown_key", "message": `unknown key "healthcheck"`, "path": "services.web.healthcheck",
				"line": 6, "column": 1, "endLine": 6, "endColumn": 12, "hint": "write `health_check`", "suggestions": []any{"health_check"}},
		},
	})
	r := f.run("config", "validate")
	require.Equal(t, 1, r.code, r.all())
	require.Equal(t, "nebula.toml:6:1: error[unknown_key] services.web.healthcheck: unknown key \"healthcheck\"; did you mean \"health_check\"?\n"+
		"  hint: write `health_check`\n"+
		"nebula.toml: warning[unresolved_reference] services.web.variables.X: no value yet\n"+
		"nebula.toml: invalid. 1 error, 1 warning.\n", r.stdout)
	require.Empty(t, r.err)

	body := f.calls("POST", cfgValidatePath)[0].json(t)
	require.Equal(t, cfgUnknownKeyFile, body["file"])
	require.NotContains(t, body, "environment")
}

func TestConfigValidateValidSummaryAndEnvironment(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	f.reply("POST", cfgValidatePath, 200, map[string]any{
		"valid": true, "schema": 1, "environments": []any{"staging"},
		"services":    []any{map[string]any{"slug": "web", "kind": "git"}, map[string]any{"slug": "db", "kind": "database"}},
		"diagnostics": []any{map[string]any{"severity": "warning", "code": "unresolved_reference", "message": "m", "path": "p"}},
	})
	r := f.run("config", "validate", "--env", "staging")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "nebula.toml: warning[unresolved_reference] p: m\n"+
		"nebula.toml: valid. schema 1, 2 services (web git, db database), 1 environment (staging), 1 warning.\n", r.stdout)
	require.Equal(t, "staging", f.calls("POST", cfgValidatePath)[0].json(t)["environment"], "the environment is sent as its slug")
}

func TestConfigValidateJSONIsTheAPIBody(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	body := map[string]any{"valid": false, "schema": 1, "environments": []any{}, "services": []any{},
		"diagnostics": []any{map[string]any{"severity": "error", "code": "missing_source", "message": "m", "path": "services.web"}}}
	f.reply("POST", cfgValidatePath, 200, body)
	r := f.run("--json", "config", "validate")
	require.Equal(t, 1, r.code, "invalid is exit 1 in JSON mode too\n"+r.all())
	require.JSONEq(t, mustJSON(body), r.stdout)
}

func TestConfigValidateNeedsAProjectOnline(t *testing.T) {
	f := newCfgFake(t)
	cfgWrite(t, "nebula.toml", "schema = 1\n")
	r := f.run("config", "validate")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "--offline")
	cfgWrite(t, "nebula.toml", "schema = 1\n")
	f.reply("POST", cfgValidatePath, 200, map[string]any{"valid": true, "schema": 1, "environments": []any{}, "services": []any{}, "diagnostics": []any{}})
	r = f.run("config", "validate", "--project", "acme-shop")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "valid. schema 1, no services.")
}

func TestConfigValidateOfflineNeedsNoControlPlane(t *testing.T) {
	cfgDir(t)
	cfgWrite(t, "nebula.toml", cfgValidFile)
	r := cfgRunOffline(t, "config", "validate", "--offline")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "nebula.toml: valid. schema 1, 1 service (web image)")
	require.NotContains(t, r.err, "not logged in")
}

func TestConfigValidateOfflineReportsErrorsWithPositions(t *testing.T) {
	cfgDir(t)
	cfgWrite(t, "nebula.toml", cfgUnknownKeyFile)
	r := cfgRunOffline(t, "config", "validate", "--offline")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.stdout, "nebula.toml:6:1: error[unknown_key] ")
	require.Contains(t, r.stdout, "bogus_key")
	require.Contains(t, r.stdout, "nebula.toml: invalid. 1 error")

	r = cfgRunOffline(t, "--json", "config", "validate", "--offline")
	require.Equal(t, 1, r.code, r.all())
	var res cfgValidateResult
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &res), r.stdout)
	require.False(t, res.Valid)
	require.Equal(t, 1, res.Schema)
	require.NotEmpty(t, res.Diagnostics)
	require.Equal(t, "unknown_key", res.Diagnostics[0].Code)
	require.Equal(t, 6, res.Diagnostics[0].Line)
}

func TestConfigValidateOfflineSyntaxErrorsAndMissingFile(t *testing.T) {
	cfgDir(t)
	cfgWrite(t, "nebula.toml", "schema = 1\n[services.web\nimage = \"x\"\n")
	r := cfgRunOffline(t, "config", "validate", "--offline")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.stdout, "error[toml_syntax]")

	r = cfgRunOffline(t, "config", "validate", "--offline", "--file", "missing.toml")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "missing.toml")
}

func TestConfigValidateOfflineChecksEnvironmentBlocks(t *testing.T) {
	cfgDir(t)
	cfgWrite(t, "nebula.toml", cfgValidFile+"\n[environments.staging.services.web]\nbogus_key = 1\n")
	r := cfgRunOffline(t, "config", "validate", "--offline")
	require.Equal(t, 1, r.code, "an error inside an environment block is found without naming the environment\n"+r.all())
	require.Contains(t, r.stdout, "error[unknown_key]")
	// --env limits the resolution to that environment; the file-level strict
	// decoding still sees the whole file.
	r = cfgRunOffline(t, "config", "validate", "--offline", "--env", "staging")
	require.Equal(t, 1, r.code, r.all())
}

func TestConfigValidateOfflineReadsAttachments(t *testing.T) {
	cfgDir(t)
	stack := "schema = 1\n\n[services.app]\n\n[services.app.stack]\ncompose_file = \"compose.yml\"\n"
	cfgWrite(t, "nebula.toml", stack)
	r := cfgRunOffline(t, "config", "validate", "--offline")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.stdout, "attachment_missing")

	cfgWrite(t, "compose.yml", "services:\n  app:\n    image: nginx:1.27\n")
	r = cfgRunOffline(t, "config", "validate", "--offline")
	require.NotContains(t, r.stdout, "attachment_missing", r.all())
}

// ---- file discovery and attachments -----------------------------------------

func TestConfigFileDiscoveryWalksUpToTheGitRoot(t *testing.T) {
	root := t.TempDir()
	cfgWrite(t, filepath.Join(root, "nebula.toml"), cfgValidFile)
	cfgWrite(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	cfgWrite(t, filepath.Join(root, "apps", "web", "src", "keep"), "")
	t.Chdir(filepath.Join(root, "apps", "web", "src"))

	r := cfgRunOffline(t, "config", "validate", "--offline")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "../../../nebula.toml: valid.", "the diagnostics name the file relative to where you are")

	// The nearest file wins.
	cfgWrite(t, filepath.Join(root, "apps", "web", "nebula.toml"), cfgUnknownKeyFile)
	r = cfgRunOffline(t, "config", "validate", "--offline")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.stdout, "../nebula.toml:6:1")

	// --file picks one explicitly.
	r = cfgRunOffline(t, "config", "validate", "--offline", "--file", "../../../nebula.toml")
	require.Equal(t, 0, r.code, r.all())
	r = cfgRunOffline(t, "config", "validate", "--offline", "-f", "../../../nebula.toml")
	require.Equal(t, 0, r.code, r.all())
}

func TestConfigFileDiscoveryStopsAtTheGitRoot(t *testing.T) {
	parent := t.TempDir()
	cfgWrite(t, filepath.Join(parent, "nebula.toml"), cfgValidFile)
	cfgWrite(t, filepath.Join(parent, "repo", ".git", "HEAD"), "ref: refs/heads/main\n")
	cfgWrite(t, filepath.Join(parent, "repo", "apps", "keep"), "")
	t.Chdir(filepath.Join(parent, "repo", "apps"))

	r := cfgRunOffline(t, "config", "validate", "--offline")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "no nebula.toml found")
	require.Contains(t, r.err, "config init")
}

func TestConfigFileDiscoveryWithoutAGitRootLooksOnlyHere(t *testing.T) {
	parent := t.TempDir()
	cfgWrite(t, filepath.Join(parent, "nebula.toml"), cfgValidFile)
	cfgWrite(t, filepath.Join(parent, "sub", "keep"), "")
	t.Chdir(filepath.Join(parent, "sub"))
	r := cfgRunOffline(t, "config", "validate", "--offline")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "no nebula.toml found")
}

func TestConfigAttachmentsAreSentRelativeToTheFile(t *testing.T) {
	f := newCfgFake(t)
	outside := t.TempDir()
	cfgWrite(t, filepath.Join(outside, "secret.txt"), "TOP SECRET")
	dir := cfgRepo(t, map[string]string{
		"deploy/nebula.toml": `schema = 1
project = "acme-shop"

[services.app.stack]
compose_file = "compose.yml"

[services.web]
image = "nginx"

[services.web.files."/etc/app.conf"]
source = "conf/app.conf"

[services.web.files."/etc/dup.conf"]
source = "conf/app.conf"

[services.web.files."/etc/gone.conf"]
source = "conf/missing.conf"

[services.web.files."/etc/abs.conf"]
source = "/etc/passwd"

[services.web.files."/etc/up.conf"]
source = "../secret.txt"

[services.web.files."/etc/link.conf"]
source = "link.txt"

[environments.staging.services.web.files."/etc/staging.conf"]
source = "staging.conf"
`,
		"deploy/compose.yml":       "services: {}\n",
		"deploy/conf/app.conf":     "listen 80;\n",
		"deploy/staging.conf":      "staging\n",
		"deploy/big.bin":           "",
		"deploy/conf/not-sent.txt": "not referenced",
	})
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "deploy", "link.txt")))
	f.reply("POST", cfgPlanPath, 200, cfgPlanFor("staging", true, false))

	r := f.run("config", "plan", "--env", "staging", "--file", "deploy/nebula.toml")
	require.Equal(t, 0, r.code, r.all())
	src := f.calls("POST", cfgPlanPath)[0].json(t)["source"].(map[string]any)
	require.Equal(t, map[string]any{
		"compose.yml":   "services: {}\n",
		"conf/app.conf": "listen 80;\n",
		"staging.conf":  "staging\n",
	}, src["attachments"], "only referenced, readable files inside the file's directory are sent")
	require.NotContains(t, mustJSON(src), "TOP SECRET")
	require.Contains(t, r.err, `not sending "/etc/passwd"`)
	require.Contains(t, r.err, `not sending "../secret.txt"`)
	require.Contains(t, r.err, `not sending "link.txt": it resolves outside`)
}

func TestConfigAttachmentsOversizedAreNotSent(t *testing.T) {
	f := newCfgFake(t)
	cfgRepo(t, map[string]string{
		"nebula.toml": "schema = 1\nproject = \"acme-shop\"\n[services.app.stack]\ncompose_file = \"big.yml\"\n",
		"big.yml":     strings.Repeat("x", attachmentLimit+1),
	})
	f.reply("POST", cfgPlanPath, 200, cfgPlanFor("staging", true, false))
	r := f.run("config", "plan", "--env", "staging")
	require.Equal(t, 0, r.code, r.all())
	require.NotContains(t, f.calls("POST", cfgPlanPath)[0].json(t)["source"], "attachments")
	require.Contains(t, r.err, "larger than")
}

func TestAttachmentRefs(t *testing.T) {
	var root map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{
	  "services": {"a": {"stack": {"compose_file": "c.yml"}, "files": {"/x": {"source": "s1"}, "/y": {"content": "inline"}}}},
	  "environments": {"staging": {"services": {"a": {"files": {"/z": {"source": "s2"}}}, "b": {"stack": {"compose_file": "c.yml"}}}}}
	}`), &root))
	require.Equal(t, []string{"c.yml", "s1", "s2"}, attachmentRefs(root))
	require.Empty(t, attachmentRefs(map[string]any{"services": "not a table"}))
}

// ---- export -----------------------------------------------------------------

func TestConfigExportWritesTheFileAndWarnsOnStderr(t *testing.T) {
	f := newCfgFake(t)
	file := "#:schema https://x/schema/nebula.toml.json\nschema = 1\nproject = \"acme-shop\"\n"
	f.reply("GET", cfgExportPath, 200, map[string]any{"file": file, "warnings": []any{
		map[string]any{"severity": "warning", "code": "secret_flag_differs", "message": "API_TOKEN is not flagged secret", "path": "services.web.variables.API_TOKEN"}}})

	r := f.run("config", "export", "--project", "acme-shop", "--env", "production")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, file, r.stdout)
	require.Contains(t, r.err, "warning[secret_flag_differs] services.web.variables.API_TOKEN: API_TOKEN is not flagged secret")
	req := f.calls("GET", cfgExportPath)[0]
	require.Equal(t, "production", req.values(t).Get("environment"), "the environment is sent as its slug")
	require.Equal(t, "application/json", req.header.Get("Accept"))
	require.False(t, req.values(t).Has("defaults"))

	r = f.run("config", "export", "--project", "acme-shop", "--env", "Staging", "-o", "out.toml")
	require.Equal(t, 0, r.code, r.all())
	require.Empty(t, r.stdout)
	raw, err := os.ReadFile("out.toml")
	require.NoError(t, err)
	require.Equal(t, file, string(raw))
	require.Contains(t, r.err, "Wrote out.toml")
	require.Equal(t, "staging", f.calls("GET", cfgExportPath)[1].values(t).Get("environment"), "names resolve to slugs")
}

func TestConfigExportAllBaseAndDefaults(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", cfgExportPath, 200, map[string]any{"file": "schema = 1", "warnings": []any{}})
	r := f.run("config", "export", "--project", "acme-shop", "--all", "--base", "staging", "--defaults")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "schema = 1\n", r.stdout, "a missing trailing newline is added")
	q := f.calls("GET", cfgExportPath)[0].values(t)
	require.Equal(t, "all", q.Get("environment"))
	require.Equal(t, "staging", q.Get("base"))
	require.Equal(t, "true", q.Get("defaults"))

	require.Equal(t, 2, f.run("config", "export", "--project", "acme-shop", "--all", "--env", "staging").code)
	require.Equal(t, 2, f.run("config", "export", "--project", "acme-shop", "--base", "staging").code)
}

func TestConfigExportJSONAndPlainTextAnswers(t *testing.T) {
	f := newCfgFake(t)
	body := map[string]any{"file": "schema = 1\n", "warnings": []any{}}
	f.reply("GET", cfgExportPath, 200, body)
	r := f.run("--json", "config", "export", "--project", "acme-shop", "--env", "production")
	require.Equal(t, 0, r.code, r.all())
	require.JSONEq(t, mustJSON(body), r.stdout)

	// A server that answers with the file as plain text still works.
	f.replyRaw("GET", cfgExportPath, cfgReply{200, "text/plain; charset=utf-8", "schema = 1\n"})
	r = f.run("config", "export", "--project", "acme-shop", "--env", "production")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "schema = 1\n", r.stdout)
	r = f.run("--json", "config", "export", "--project", "acme-shop", "--env", "production")
	require.Equal(t, 0, r.code, r.all())
	require.JSONEq(t, `{"file":"schema = 1\n","warnings":null}`, r.stdout)
}

func TestConfigExportProjectDefaultsToTheFilesProject(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", cfgExportPath, 200, map[string]any{"file": "schema = 1\n", "warnings": []any{}})
	r := f.run("config", "export", "--env", "production")
	require.Equal(t, 2, r.code, r.all())
	cfgWrite(t, "nebula.toml", cfgValidFile)
	r = f.run("config", "export", "--env", "production")
	require.Equal(t, 0, r.code, r.all())
}

// ---- init -------------------------------------------------------------------

const cfgGitConfig = `[core]
	repositoryformatversion = 0
[remote "origin"]
	url = https://x-access-token:ghp_SUPERSECRET@github.com/acme/shop.git
	fetch = +refs/heads/*:refs/remotes/origin/*
[branch "develop"]
	remote = origin
`

func TestConfigInitScaffoldsADockerfileRepository(t *testing.T) {
	dir := cfgRepo(t, map[string]string{
		"Dockerfile":        "FROM node:22\nEXPOSE 8080/tcp\nCMD [\"node\", \"server.js\"]\n",
		"package.json":      "{}",
		".env.example":      "DATABASE_URL=postgres://localhost/x\nexport API_KEY=\n# COMMENT=1\nDATABASE_URL=dup\n",
		".git/config":       cfgGitConfig,
		".git/HEAD":         "ref: refs/heads/develop\n",
		"unrelated/main.go": "package main",
	})
	r := cfgRunOffline(t, "config", "init")
	require.Equal(t, 0, r.code, r.all())
	raw, err := os.ReadFile(filepath.Join(dir, "nebula.toml"))
	require.NoError(t, err)
	text := string(raw)
	require.True(t, strings.HasPrefix(text, "#:schema https://nebulactrl.com/schema/nebula.toml.json\n"), text)
	require.Contains(t, text, "schema = 1\n")
	require.Contains(t, text, "# project = ")
	require.Contains(t, text, "[services."+slugify(filepath.Base(dir))+"]\nport = 8080")
	require.Contains(t, text, `url = "https://github.com/acme/shop.git"`)
	require.Contains(t, text, `branch = "develop"`)
	require.Contains(t, text, "[services."+slugify(filepath.Base(dir))+".build]\ntype = \"dockerfile\"\ndockerfile = \"Dockerfile\"")
	require.Contains(t, text, "# DATABASE_URL = \"\"\n# API_KEY = \"\"\n")
	require.Equal(t, 1, strings.Count(text, "# DATABASE_URL = "), "names are listed once")
	require.NotContains(t, text, "ghp_SUPERSECRET", "credentials of the remote never reach the file")
	require.NotContains(t, text, "postgres://localhost", ".env.example values are never copied")
	require.Contains(t, r.stdout, "Wrote nebula.toml")

	// What it writes is a file the validator accepts.
	_, _, ds := projectconfig.Parse(raw, nil)
	require.False(t, ds.HasErrors(), "%v", ds)
	v := cfgRunOffline(t, "config", "validate", "--offline")
	require.Equal(t, 0, v.code, v.all())
}

func TestConfigInitRefusesToOverwriteWithoutForce(t *testing.T) {
	cfgRepo(t, map[string]string{"Dockerfile": "FROM scratch\n", "nebula.toml": "# mine\n"})
	r := cfgRunOffline(t, "config", "init")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "already exists")
	require.Contains(t, r.err, "--force")
	raw, err := os.ReadFile("nebula.toml")
	require.NoError(t, err)
	require.Equal(t, "# mine\n", string(raw))

	r = cfgRunOffline(t, "config", "init", "--force", "--git", "https://github.com/acme/shop.git")
	require.Equal(t, 0, r.code, r.all())
	raw, err = os.ReadFile("nebula.toml")
	require.NoError(t, err)
	require.Contains(t, string(raw), "schema = 1")
}

func TestConfigInitFromAComposeFile(t *testing.T) {
	cfgRepo(t, map[string]string{"docker-compose.yml": "services:\n  app:\n    image: nginx:1.27\n", "package.json": "{}"})
	r := cfgRunOffline(t, "config", "init", "--name", "My Shop")
	require.Equal(t, 0, r.code, r.all())
	raw, err := os.ReadFile("nebula.toml")
	require.NoError(t, err)
	text := string(raw)
	require.Contains(t, text, "[services.my-shop]\nname = \"My Shop\"\n")
	require.Contains(t, text, "[services.my-shop.stack]\ncompose_file = \"docker-compose.yml\"")
	require.NotContains(t, text, ".git]", "a compose file is a stack, not a git service")
	require.Contains(t, r.stdout, "stack service")
	_, _, ds := projectconfig.Parse(raw, cfgAttachments(map[string]string{"docker-compose.yml": "services:\n  app:\n    image: nginx:1.27\n"}))
	require.False(t, ds.HasErrors(), "%v", ds)
}

// cfgAttachments turns text files into the format's attachments.
func cfgAttachments(files map[string]string) projectconfig.Attachments {
	a := projectconfig.Attachments{}
	for k, v := range files {
		a[k] = []byte(v)
	}
	return a
}

func TestConfigInitFromALanguageManifestWithoutARemote(t *testing.T) {
	cfgRepo(t, map[string]string{"go.mod": "module x\n\ngo 1.24\n"})
	r := cfgRunOffline(t, "config", "init")
	require.Equal(t, 0, r.code, r.all())
	raw, err := os.ReadFile("nebula.toml")
	require.NoError(t, err)
	text := string(raw)
	require.Contains(t, text, "# url = ")
	require.Contains(t, text, "type = \"auto\"")
	require.Contains(t, text, "# Detected go.")
	require.Contains(t, text, "branch = \"main\"")
	require.Contains(t, r.stdout, "set git.url")
	require.Contains(t, r.err, "not complete yet", "the starter says what is still missing")
}

func TestConfigInitWithNothingToGoOnFailsWithoutWritingAFile(t *testing.T) {
	cfgRepo(t, map[string]string{"README.md": "hello"})
	r := cfgRunOffline(t, "config", "init")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "nothing to scaffold from")
	require.Contains(t, r.err, "--image")
	require.NoFileExists(t, "nebula.toml")
}

func TestConfigInitImageService(t *testing.T) {
	cfgRepo(t, map[string]string{"README.md": "x"})
	r := cfgRunOffline(t, "config", "init", "--image", "nginx:1.27", "--name", "Edge", "--project", "acme-shop")
	require.Equal(t, 0, r.code, r.all())
	raw, err := os.ReadFile("nebula.toml")
	require.NoError(t, err)
	text := string(raw)
	require.Contains(t, text, "project = \"acme-shop\"\n")
	require.Contains(t, text, "[services.edge]\nname = \"Edge\"\n")
	require.Contains(t, text, "image = \"nginx:1.27\"\n")
	_, _, ds := projectconfig.Parse(raw, nil)
	require.False(t, ds.HasErrors(), "%v", ds)
}

func TestConfigInitUsageErrors(t *testing.T) {
	cfgRepo(t, map[string]string{"Dockerfile": "FROM scratch\n"})
	for name, args := range map[string][]string{
		"image and git":         {"config", "init", "--image", "nginx", "--git", "https://github.com/a/b.git"},
		"credentials in --git":  {"config", "init", "--git", "https://user:token@github.com/a/b.git"},
		"whitespace in --image": {"config", "init", "--image", "nginx latest"},
		"env without project":   {"config", "init", "--env", "production"},
		"export with --image":   {"config", "init", "--project", "p", "--env", "production", "--image", "nginx"},
		"positional":            {"config", "init", "stray"},
	} {
		r := cfgRunOffline(t, args...)
		require.Equal(t, 2, r.code, "%s\n%s", name, r.all())
	}
	require.NoFileExists(t, "nebula.toml")
}

func TestConfigInitSchemaLinePointsAtTheLoggedInInstance(t *testing.T) {
	f := newCfgFake(t)
	cfgRepo(t, map[string]string{"Dockerfile": "FROM scratch\n"})
	r := f.run("config", "init", "--git", "https://github.com/acme/shop.git")
	require.Equal(t, 0, r.code, r.all())
	raw, err := os.ReadFile("nebula.toml")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(raw), "#:schema "+f.srv.URL+"/schema/nebula.toml.json\n"), string(raw))
}

func TestConfigInitWithProjectAndEnvIsExport(t *testing.T) {
	f := newCfgFake(t)
	cfgRepo(t, map[string]string{"README.md": "x"})
	file := "schema = 1\nproject = \"acme-shop\"\n"
	f.reply("GET", cfgExportPath, 200, map[string]any{"file": file, "warnings": []any{}})

	r := f.run("config", "init", "--project", "acme-shop", "--env", "production")
	require.Equal(t, 0, r.code, r.all())
	raw, err := os.ReadFile("nebula.toml")
	require.NoError(t, err)
	require.Equal(t, file, string(raw))
	require.Equal(t, "production", f.calls("GET", cfgExportPath)[0].values(t).Get("environment"))

	r = f.run("config", "init", "--project", "acme-shop", "--env", "production")
	require.Equal(t, 1, r.code, "an existing file is not overwritten by an export either\n"+r.all())
	require.Len(t, f.calls("GET", cfgExportPath), 1, "the refusal comes before the request")

	r = f.run("--json", "config", "init", "--project", "acme-shop", "--env", "production", "--force")
	require.Equal(t, 0, r.code, r.all())
	require.JSONEq(t, mustJSON(map[string]any{"file": file, "warnings": []any{}}), r.stdout)
}

func TestConfigInitJSON(t *testing.T) {
	cfgRepo(t, map[string]string{"Dockerfile": "FROM scratch\nEXPOSE 3000\n", ".git/config": cfgGitConfig})
	r := cfgRunOffline(t, "--json", "config", "init")
	require.Equal(t, 0, r.code, r.all())
	var out struct {
		File        string `json:"file"`
		Summary     string `json:"summary"`
		Diagnostics []any  `json:"diagnostics"`
	}
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &out), r.stdout)
	require.Equal(t, "nebula.toml", out.File)
	require.Contains(t, out.Summary, "git service")
	require.NotNil(t, out.Diagnostics)
}

func TestScaffoldHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"My Shop":                           "my-shop",
		"  --weird__Name--  ":               "weird-name",
		"":                                  "app",
		"!!!":                               "app",
		"shared":                            "shared-app",
		"Vault":                             "vault-app",
		strings.Repeat("a", 80):             strings.Repeat("a", 63),
		"café":                              "caf",
		"a" + strings.Repeat("-", 70) + "b": "a-b",
	} {
		require.Equal(t, want, slugify(in), "slugify(%q)", in)
	}

	for in, want := range map[string]string{
		"https://github.com/acme/shop.git":           "https://github.com/acme/shop.git",
		"https://user:pw@github.com/acme/shop.git":   "https://github.com/acme/shop.git",
		"https://ghp_token@github.com/acme/shop.git": "https://github.com/acme/shop.git",
		"ssh://git@github.com/acme/shop.git":         "ssh://git@github.com/acme/shop.git",
		"ssh://deploy:pw@github.com/acme/shop.git":   "ssh://github.com/acme/shop.git",
		"git@github.com:acme/shop.git":               "git@github.com:acme/shop.git",
		"deploy@github.com:acme/shop.git":            "",
		"/srv/git/shop.git":                          "",
		"../shop":                                    "",
		"file:///srv/git/shop.git":                   "",
		"https://host/with space":                    "",
		"":                                           "",
		"https://":                                   "",
	} {
		require.Equal(t, want, cleanGitURL(in), "cleanGitURL(%q)", in)
	}

	for in, want := range map[string]string{
		"https://github.com/acme/shop.git": "acme/shop",
		"git@gitlab.com:acme/shop.git":     "acme/shop",
		"https://example.com/acme/shop":    "",
		"https://github.com/acme":          "",
	} {
		require.Equal(t, want, repoFullName(in), "repoFullName(%q)", in)
	}

	require.Equal(t, `"a \"b\" \\ c\n\t\u001B\u007F é"`, tomlString("a \"b\" \\ c\n\t\x1b\x7f é"))
}

// ---- schema -----------------------------------------------------------------

func TestConfigSchemaPrintsTheEmbeddedSchema(t *testing.T) {
	r := cfgRunOffline(t, "config", "schema")
	require.Equal(t, 0, r.code, r.all())
	want := string(projectconfig.Schema())
	if !strings.HasSuffix(want, "\n") {
		want += "\n"
	}
	require.Equal(t, want, r.stdout)
	require.True(t, json.Valid([]byte(r.stdout)))
}

func TestConfigSchemaRemote(t *testing.T) {
	f := newCfgFake(t)
	f.replyRaw("GET", "/schema/nebula.toml.json", cfgReply{200, "application/schema+json", `{"title":"remote"}`})
	r := f.run("config", "schema", "--remote")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "{\"title\":\"remote\"}\n", r.stdout)
	require.Empty(t, f.calls("GET", "/schema/nebula.toml.json")[0].header.Get("Authorization"), "the schema is public: no token is sent")

	f.replyRaw("GET", "/schema/nebula.toml.json", problemReply(404, "not_found", "no schema", nil))
	r = f.run("config", "schema", "--remote")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "HTTP 404")

	r = cfgRunOffline(t, "config", "schema", "--remote")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "not logged in")
}

// ---- source -----------------------------------------------------------------

const cfgConnID = "44444444-dddd-4ddd-8ddd-444444444444"

func cfgSourceReply(status string) map[string]any {
	return map[string]any{
		"mode": "explicit", "repo": "acme/shop", "branch": "develop", "path": "nebula.toml", "resolvedPath": "deploy/nebula.toml",
		"autoApply": true, "status": status, "statusDetail": "services.web.port differs", "commit": "3f9c2a1d5e8b7c6a9f0e",
		"fileSha256": "abcd", "checkedAt": "2026-10-29T10:00:00Z",
		"services": []any{map[string]any{"slug": "web", "state": "file_changed", "changes": 3}},
	}
}

func TestConfigSourceShow(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", cfgSourcePath, 200, cfgSourceReply("differs"))
	r := f.run("config", "source", "show", "--project", "acme-shop", "--env", "staging")
	require.Equal(t, 0, r.code, r.all())
	for _, want := range []string{
		"Config source of acme-shop / staging",
		"Binding        explicit: acme/shop@develop, path nebula.toml",
		"Resolved path  deploy/nebula.toml",
		"Auto-apply     on",
		"Status         differs @ 3f9c2a1 (checked ",
		"Detail         services.web.port differs",
		"File SHA-256   abcd",
		"SERVICE", "web", "file_changed", "3",
	} {
		require.Contains(t, r.stdout, want)
	}

	r = f.run("--json", "config", "source", "show", "--project", "acme-shop", "--env", "staging")
	require.Equal(t, 0, r.code, r.all())
	require.JSONEq(t, mustJSON(cfgSourceReply("differs")), r.stdout)

	f.reply("GET", cfgSourcePath, 200, map[string]any{"mode": "none", "autoApply": false, "status": "unknown", "services": []any{}})
	r = f.run("config", "source", "show", "--project", "acme-shop", "--env", "staging")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "Binding        none")
	require.Contains(t, r.stdout, "Auto-apply     off")
}

func TestConfigSourceChoosesTheOnlyStandardEnvironment(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", cfgSourcePath, 200, cfgSourceReply("in_sync"))
	r := f.run("config", "source", "show", "--project", "acme-shop")
	require.Equal(t, 2, r.code, "two standard environments: --env is needed\n"+r.all())
	require.Contains(t, r.err, "pass --env")

	f.reply("GET", cfgProjectPath+"/environments", 200, map[string]any{"items": []any{
		map[string]any{"id": cfgProdID, "slug": "production", "kind": "preview"},
		map[string]any{"id": cfgStagingID, "slug": "staging", "kind": "standard"},
	}})
	r = f.run("config", "source", "show", "--project", "acme-shop")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "acme-shop / staging")
}

func TestConfigSourceSetSendsOnlyTheFlagsGiven(t *testing.T) {
	f := newCfgFake(t)
	f.reply("PUT", cfgSourcePath, 200, cfgSourceReply("unknown"))
	f.reply("GET", cfgAPI+"/git/connections", 200, map[string]any{"items": []any{
		map[string]any{"id": cfgConnID, "name": "github-acme"},
		map[string]any{"id": "55555555-eeee-4eee-8eee-555555555555", "name": "other"},
	}})

	r := f.run("config", "source", "set", "--project", "acme-shop", "--env", "staging", "--repo", "acme/shop", "--branch", "develop")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"repo": "acme/shop", "branch": "develop"}, f.calls("PUT", cfgSourcePath)[0].json(t))
	require.Contains(t, r.stdout, "Config source of acme-shop / staging")

	r = f.run("config", "source", "set", "--project", "acme-shop", "--env", "staging", "--connection", "GitHub-Acme", "--path", "deploy/nebula.toml", "--auto-apply")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"connectionId": cfgConnID, "path": "deploy/nebula.toml", "autoApply": true}, f.calls("PUT", cfgSourcePath)[1].json(t))

	r = f.run("config", "source", "set", "--project", "acme-shop", "--env", "staging", "--no-auto-apply")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"autoApply": false}, f.calls("PUT", cfgSourcePath)[2].json(t))

	r = f.run("config", "source", "set", "--project", "acme-shop", "--env", "staging", "--connection", "nope")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, `git connection "nope" not found`)
	require.Len(t, f.calls("PUT", cfgSourcePath), 3)
}

func TestConfigSourceSetUsageErrors(t *testing.T) {
	f := newCfgFake(t)
	for name, args := range map[string][]string{
		"nothing to set":  {"config", "source", "set", "--project", "acme-shop", "--env", "staging"},
		"both auto flags": {"config", "source", "set", "--project", "acme-shop", "--env", "staging", "--auto-apply", "--no-auto-apply"},
	} {
		r := f.run(args...)
		require.Equal(t, 2, r.code, "%s\n%s", name, r.all())
	}
	require.Empty(t, f.calls("PUT", cfgSourcePath))
}

func TestConfigSourceSetAutoApplyRefusalIsReported(t *testing.T) {
	f := newCfgFake(t)
	f.replyRaw("PUT", cfgAPI+"/environments/"+cfgProdID+"/config-source",
		problemReply(422, "production_auto_apply_unsupported", "production never auto-applies", nil))
	r := f.run("config", "source", "set", "--project", "acme-shop", "--env", "production", "--auto-apply")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "production_auto_apply_unsupported")
}

func TestConfigSourceClear(t *testing.T) {
	f := newCfgFake(t)
	f.replyRaw("DELETE", cfgSourcePath, cfgReply{204, "application/json", ""})
	r := f.run("config", "source", "clear", "--project", "acme-shop", "--env", "staging")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "Cleared the config source of acme-shop/staging")
	require.Len(t, f.calls("DELETE", cfgSourcePath), 1)

	r = f.run("--json", "config", "source", "clear", "--project", "acme-shop", "--env", "staging")
	require.Equal(t, 0, r.code, r.all())
	require.JSONEq(t, `{"cleared":true,"project":"acme-shop","environment":"staging"}`, r.stdout)
}

func TestConfigSourceCheckExitCodes(t *testing.T) {
	for status, want := range map[string]int{
		"in_sync": 0, "differs": 0, "unknown": 0,
		"invalid": 1, "not_found": 1, "unreachable": 1, "error": 1,
	} {
		f := newCfgFake(t)
		f.reply("POST", cfgSourcePath+"/check", 200, cfgSourceReply(status))
		r := f.run("config", "source", "check", "--project", "acme-shop", "--env", "staging", "--sha", "abc123")
		require.Equal(t, want, r.code, "%s\n%s", status, r.all())
		require.Contains(t, r.stdout, "Status         "+status)
		require.Equal(t, map[string]any{"sha": "abc123"}, f.calls("POST", cfgSourcePath+"/check")[0].json(t))
	}
	f := newCfgFake(t)
	f.reply("POST", cfgSourcePath+"/check", 200, cfgSourceReply("in_sync"))
	require.Equal(t, 0, f.run("config", "source", "check", "--project", "acme-shop", "--env", "staging").code)
	require.Equal(t, map[string]any{}, f.calls("POST", cfgSourcePath+"/check")[0].json(t), "no --sha: read the branch head")
}

// ---- rendering --------------------------------------------------------------

func TestLayoutRowsKeepsColumnsAndGrowsForLongNames(t *testing.T) {
	rows := []planRow{
		{indent: 2, marker: "+", name: "services.q", desc: "new", applies: "staged"},
		{indent: 6, marker: "~", name: "variables.A_VERY_LONG_VARIABLE_NAME_INDEED", desc: "1 → 2", applies: "applies at once", note: ""},
		{indent: 6, marker: "~", name: "x", desc: "a" + strings.Repeat("b", 50), applies: "staged", note: "edited"},
	}
	lines := layoutRows(rows)
	col := func(line, sub string) int {
		at := strings.Index(line, sub)
		require.GreaterOrEqual(t, at, 0, "%q not found in %q", sub, line)
		return runeLen(line[:at])
	}
	require.Equal(t, 32, col(lines[0], "new"))
	require.Equal(t, 6+2+runeLen(rows[1].name)+2, col(lines[1], "1 → 2"), "a long name pushes its own description")
	require.Equal(t, 32, col(lines[2], "ab"))
	// A description wider than its slot moves the label column for every row.
	ac := col(lines[2], "staged")
	require.Equal(t, ac, col(lines[0], "staged"))
	require.Equal(t, ac, col(lines[1], "applies at once"))
	require.Equal(t, 32+51+2, ac)
	require.Equal(t, ac+planNoteWidth, col(lines[2], "edited"))
	for _, l := range lines {
		require.Equal(t, strings.TrimRight(l, " "), l, "no trailing spaces")
	}
}

func TestWritePlanSummaryLines(t *testing.T) {
	for name, tc := range map[string]struct {
		summary cfgPlanSummary
		want    string
	}{
		"nothing":        {cfgPlanSummary{}, "no changes."},
		"unchanged only": {cfgPlanSummary{Unchanged: 2, NotInFile: 1}, "no changes, 2 unchanged, 1 not in file."},
		"plural":         {cfgPlanSummary{Create: 2, Update: 3, Staged: 4, Immediate: 2, Deferred: 1}, "2 creates, 3 updates (4 staged, 2 immediate, 1 deferred)."},
		"shared only":    {cfgPlanSummary{Staged: 1}, "shared changes (1 staged)."},
		"prune":          {cfgPlanSummary{Update: 1, Staged: 1, Pruned: 2, Adopted: 1}, "1 update (1 staged), 1 adopted, 2 pruned."},
	} {
		require.Equal(t, tc.want, summaryLine(&cfgPlan{Summary: tc.summary}), name)
	}
}

func TestWritePlanRendersOtherServiceStates(t *testing.T) {
	var p cfgPlan
	require.NoError(t, json.Unmarshal([]byte(cfgGoldenPlan), &p))
	p.Services = []cfgPlanService{
		{Slug: "db", Kind: "database", Action: "blocked", Blockers: []string{"docker_unrestricted", "unknown_server"}},
		{Slug: "old", Action: "not_in_file", Managed: true},
		{Slug: "api", Kind: "git", Action: "update", Released: []string{"services.api.tasks.old-report"}, Changes: []cfgChange{
			{Path: "services.api.variables.OLD", Field: "variables.OLD", Op: "remove", Applies: "staged", From: "x"},
			{Path: "services.api.domains.a.example.com", Field: "domains.a.example.com", Op: "keep"},
			{Path: "services.api.uptime", Field: "uptime", Op: "set", Applies: "deferred", Reason: "conflict", From: "", To: "reminder 60"},
		}},
	}
	p.Diagnostics = nil
	p.Valid = false
	p.Diagnostics = append(p.Diagnostics, errDiag("services.db.server", "unknown server"), noteDiag("deferred"))
	var b strings.Builder
	writePlan(&b, &p, "nebula.toml", false)
	out := b.String()
	for _, want := range []string{
		"  ! services.db",
		"blocked: docker_unrestricted, unknown_server",
		"not in file (kept; it was managed by the file)",
		"      - variables.OLD",
		`"x" → (removed)`,
		"kept (no longer in the file)",
		"(none) → reminder 60",
		"deferred  edited in dashboard and in the file",
		"      ? tasks.old-report",
		"released (stays as it is)",
		"  1 error:\n    nebula.toml:3:4: error[unknown_server] services.db.server: unknown server",
		"  1 note:\n    nebula.toml: info[deferred] m",
		"The plan is not valid: fix the errors above; nothing can be applied.",
	} {
		require.Contains(t, out, want)
	}
	require.NotContains(t, out, "Plan id")
}

func errDiag(path, msg string) projectconfig.Diagnostic {
	return projectconfig.Diagnostic{Severity: "error", Code: "unknown_server", Path: path, Message: msg, Line: 3, Column: 4}
}

func noteDiag(code string) projectconfig.Diagnostic {
	return projectconfig.Diagnostic{Severity: "info", Code: code, Message: "m"}
}

func TestVerbosePlanListsFieldsOfNewServices(t *testing.T) {
	var p cfgPlan
	require.NoError(t, json.Unmarshal([]byte(cfgGoldenPlan), &p))
	p.Services[0].Changes = []cfgChange{{Path: "services.queue.image", Field: "config.image", Op: "add", Applies: "staged", Reason: "new", To: "redis:7"}}
	p.Services[3].Unmanaged = map[string]int{"variables": 6, "tasks": 1, "domains": 0}
	p.Services[3].Action = "unchanged"
	var plain, verbose strings.Builder
	writePlan(&plain, &p, "nebula.toml", false)
	writePlan(&verbose, &p, "nebula.toml", true)
	require.NotContains(t, plain.String(), "redis:7")
	require.NotContains(t, plain.String(), "unmanaged")
	require.Contains(t, verbose.String(), "      + image")
	require.Contains(t, verbose.String(), "redis:7")
	require.Contains(t, verbose.String(), "1 tasks, 6 variables not in the file (untouched)")
}

func TestFormatDiagnostic(t *testing.T) {
	d := projectconfig.Diagnostic{Severity: "error", Code: "unknown_key", Message: `unknown key "x"`, Path: "services.web.x",
		Line: 19, Column: 1, Suggestions: []string{"a", "b", "c"}, Environment: "staging"}
	require.Equal(t, `nebula.toml:19:1: error[unknown_key] services.web.x: unknown key "x"; did you mean "a", "b" or "c"? [environment staging]`,
		formatDiagnostic("nebula.toml", d))
	d = projectconfig.Diagnostic{Severity: "warning", Code: "w", Message: "m"}
	require.Equal(t, "dir/nebula.toml: warning[w] m", formatDiagnostic("dir/nebula.toml", d))
	d.Line = 4
	require.Equal(t, "f:4: warning[w] m", formatDiagnostic("f", d), "a line without a column")
	d.Suggestions = []string{"only"}
	require.Equal(t, `f:4: warning[w] m; did you mean "only"?`, formatDiagnostic("f", d))
}

func TestWriteApplyResultForANoop(t *testing.T) {
	var res cfgApplyResult
	require.NoError(t, json.Unmarshal([]byte(mustJSON(cfgApplyFor("noop", nil, nil))), &res))
	var b strings.Builder
	writeApplyResult(&b, &res, "nebula.toml", false)
	require.Contains(t, b.String(), "Result: nothing to apply.")
}

// ---- command tree -----------------------------------------------------------

func TestConfigCommandTreeIsDocumentable(t *testing.T) {
	root := newRoot(&cli{})
	cfg, _, err := root.Find([]string{"config"})
	require.NoError(t, err)
	var names []string
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		require.NotEmpty(t, cmd.Short, cmd.CommandPath())
		cmd.LocalFlags().VisitAll(func(fl *pflag.Flag) {
			require.NotEmpty(t, fl.Usage, "%s --%s", cmd.CommandPath(), fl.Name)
			require.NotContains(t, fl.DefValue, "/", "defaults in the generated reference must not depend on this machine: %s --%s", cmd.CommandPath(), fl.Name)
		})
		for _, sub := range cmd.Commands() {
			names = append(names, sub.CommandPath())
			walk(sub)
		}
	}
	walk(cfg)
	require.ElementsMatch(t, []string{
		"nebulactrl config init", "nebulactrl config validate", "nebulactrl config plan", "nebulactrl config apply",
		"nebulactrl config export", "nebulactrl config schema", "nebulactrl config source",
		"nebulactrl config source show", "nebulactrl config source set", "nebulactrl config source clear", "nebulactrl config source check",
	}, names)

	for _, path := range [][]string{{"config", "plan"}, {"config", "apply"}, {"config", "validate"}} {
		cmd, _, err := root.Find(path)
		require.NoError(t, err)
		require.NotNil(t, cmd.Flags().ShorthandLookup("f"), "%v takes -f/--file", path)
	}

	r := cfgRunOffline(t, "config", "nope")
	require.Equal(t, 2, r.code, r.all())
}
