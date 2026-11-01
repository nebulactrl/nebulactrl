package composites_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

const explainCanary = "sekret-canary-12345"

// explainSetEnv seals a release environment the way a deployment does.
func explainSetEnv(f *fx, dep db.Deployment, env services.EnvSnapshot) {
	f.t.Helper()
	require.NotNil(f.t, dep.ReleaseID)
	plain, err := json.Marshal(env)
	require.NoError(f.t, err)
	ct, err := f.box.Seal(secretbox.ReleasesEnv, dep.ReleaseID.String(), plain)
	require.NoError(f.t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE releases SET env_enc = $2 WHERE id = $1", *dep.ReleaseID, ct)
	require.NoError(f.t, err)
}

// explainSetSource replaces the source of a deployment's release.
func explainSetSource(f *fx, dep db.Deployment, src services.ReleaseSource) {
	f.t.Helper()
	raw, err := json.Marshal(src)
	require.NoError(f.t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE releases SET source = $2 WHERE id = $1", *dep.ReleaseID, raw)
	require.NoError(f.t, err)
}

func explainStr(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	s, _ := cur.(string)
	return s
}

func explainList(m map[string]any, key string) []any {
	l, _ := m[key].([]any)
	return l
}

// explainSeed makes a deployed service "api" in staging and a newer failed
// deployment of a different image.
func explainSeed(f *fx, status, message string) (db.Service, db.Deployment) {
	sv := f.service(svcOpts{env: f.staging, slug: "api", port: 80})
	bad := f.release(sv, "traefik/whoami:v2", status, message)
	return sv, bad
}

func TestExplainFailedDeploymentHealthPortMismatch(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "api", port: 80})
	good := f.release(sv, "traefik/whoami:v1", "succeeded", "")
	explainSetEnv(f, good, services.EnvSnapshot{
		Vars: map[string]string{"A": "1", "DB_PASSWORD": explainCanary, "PORT": "80"}, Secret: []string{"DB_PASSWORD"},
		Raw: map[string]services.RawVar{"A": {Value: "1"}, "DB_PASSWORD": {Value: explainCanary, Secret: true}},
	})
	bad := f.release(sv, "traefik/whoami:v2", "failed", "the new container did not become healthy")
	explainSetEnv(f, bad, services.EnvSnapshot{
		Vars: map[string]string{"A": "1", "NEW_KEY": "x", "DB_PASSWORD": explainCanary, "PORT": "80"}, Secret: []string{"DB_PASSWORD", "NEW_KEY"},
		Raw: map[string]services.RawVar{"A": {Value: "1"}, "NEW_KEY": {Value: "x", Secret: true}, "DB_PASSWORD": {Value: explainCanary, Secret: true}},
	})
	f.event(bad.ID, "pull", "started", "")
	f.event(bad.ID, "pull", "succeeded", "")
	f.event(bad.ID, "start", "succeeded", "")
	f.event(bad.ID, "health", "started", "")
	f.event(bad.ID, "health", "failed", "health check timed out after 120s")
	_, derr := f.pool.Exec(context.Background(), "UPDATE deployment_events SET created_at = created_at + interval '2 seconds' WHERE deployment_id = $1 AND step = 'health' AND status = 'failed'", bad.ID)
	require.NoError(t, derr)
	f.logLine(bad.ID, "deploy", "creating container")
	f.logLine(bad.ID, "runtime", "server booted, listening on port 3000")
	f.logLine(bad.ID, "deploy", "health probe GET / : connection refused")
	f.logLine(bad.ID, "debug", "docker inspect ok")

	m := f.must("explain_failed_deployment", map[string]any{"deployment": bad.ID.String()})
	require.Equal(t, true, m["failed"])
	require.Equal(t, "health", m["failedStep"])
	require.Equal(t, "port_mismatch", m["likelyCause"])
	require.Contains(t, m["smallestFix"], "port 3000")
	require.Contains(t, m["smallestFix"], "port 80")
	require.Equal(t, f.selector(f.staging, "api"), m["service"])
	require.Equal(t, bad.ID.String(), explainStr(m, "deployment", "id"))
	require.Equal(t, "failed", explainStr(m, "deployment", "status"))

	// Timeline marks the first failing step and carries durations.
	tl := explainList(m, "timeline")
	require.Len(t, tl, 5)
	last := tl[4].(map[string]any)
	require.Equal(t, "health", last["step"])
	require.Equal(t, true, last["failing"])
	require.GreaterOrEqual(t, last["durationMs"], float64(2000), "the failed step carries its duration")
	require.GreaterOrEqual(t, last["sincePrevMs"], float64(2000))

	// Excerpt centers on the first error line and is untrusted-wrapped.
	require.Equal(t, "first_error", m["logExcerptAnchor"])
	ex := m["logExcerpt"].(map[string]any)
	require.Equal(t, true, ex["untrusted"])
	require.Contains(t, ex["text"], "connection refused")
	require.NotContains(t, ex["text"], "docker inspect ok", "debug lines stay out of the excerpt when other lines exist")

	// Release diff against the last good release: image, key changes, no values.
	d := m["releaseDiff"].(map[string]any)
	require.Equal(t, true, d["compared"])
	require.Equal(t, good.ID.String(), d["baselineDeployment"])
	var fields []string
	for _, c := range explainList(d, "changes") {
		fields = append(fields, c.(map[string]any)["field"].(string))
	}
	require.Contains(t, fields, "image")
	require.Equal(t, []any{"NEW_KEY"}, d["variablesAdded"])
	require.Empty(t, explainList(d, "variablesRemoved"))

	// The previous release is still serving and a rollback is possible.
	sn := m["servingNow"].(map[string]any)
	require.Equal(t, true, sn["previousReleaseServing"])
	require.Equal(t, true, sn["serving"])
	rt := m["rollbackTarget"].(map[string]any)
	require.Equal(t, true, rt["available"])
	require.Equal(t, good.ID.String(), rt["deploymentId"])

	// Summary says what is serving; nextTools offer the fix path.
	require.Contains(t, m["summary"], "previous release is still serving")
	var tools []string
	for _, nt := range explainList(m, "nextTools") {
		tools = append(tools, nt.(map[string]any)["tool"].(string))
	}
	require.Contains(t, tools, "stage_changes")
	require.Contains(t, tools, "apply_changes")
	require.Contains(t, tools, "tail_logs")
	require.Empty(t, sectionErrors(m))

	raw, _ := json.Marshal(m)
	require.NotContains(t, string(raw), explainCanary)
}

func TestExplainFailedDeploymentByServiceTakesLatestFailure(t *testing.T) {
	f := newFx(t)
	sv, older := explainSeed(f, "failed", "older failure")
	_ = older
	newer := f.release(sv, "traefik/whoami:v3", "canceled", "")
	_, err := f.pool.Exec(context.Background(), "UPDATE deployments SET canceled_at_step = 'build' WHERE id = $1", newer.ID)
	require.NoError(t, err)

	m := f.must("explain_failed_deployment", map[string]any{"service": f.selector(f.staging, "api")})
	require.Equal(t, true, m["failed"])
	require.Equal(t, newer.ID.String(), explainStr(m, "deployment", "id"))
	require.Equal(t, "canceled", m["likelyCause"])
	require.Equal(t, "build", m["failedStep"])
	require.Contains(t, m["smallestFix"], "apply_changes")
}

func TestExplainFailedDeploymentNotFailed(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "api"})
	f.event(*sv.CurrentDeploymentID, "pull", "succeeded", "")

	// A succeeded deployment by id.
	m := f.must("explain_failed_deployment", map[string]any{"deployment": sv.CurrentDeploymentID.String()})
	require.Equal(t, false, m["failed"])
	require.Equal(t, "succeeded", explainStr(m, "deployment", "status"))
	require.Contains(t, m["summary"], "did not fail")
	require.Equal(t, "unknown", m["likelyCause"])

	// A service without a failure: its latest deployment is reported.
	m = f.must("explain_failed_deployment", map[string]any{"service": f.selector(f.staging, "api")})
	require.Equal(t, false, m["failed"])
	require.Contains(t, m["summary"], "no failed, rolled back or canceled deployment")
	require.Equal(t, sv.CurrentDeploymentID.String(), explainStr(m, "deployment", "id"))

	// A service that was never deployed.
	f.service(svcOpts{env: f.staging, slug: "fresh", undeployed: true})
	m = f.must("explain_failed_deployment", map[string]any{"service": f.selector(f.staging, "fresh")})
	require.Equal(t, false, m["failed"])
	require.Contains(t, m["summary"], "no deployments yet")

	// An in-flight deployment is not a failure.
	run := f.release(sv, "traefik/whoami:v2", "building", "")
	f.event(run.ID, "build", "started", "")
	m = f.must("explain_failed_deployment", map[string]any{"deployment": run.ID.String()})
	require.Equal(t, false, m["failed"])
	require.Contains(t, m["summary"], "still building at step build")
}

func TestExplainFailedDeploymentWithoutLogsScopeDegrades(t *testing.T) {
	f := newFx(t)
	_, bad := explainSeed(f, "failed", "pull access denied for private/app, repository does not exist or may require authorization")
	f.event(bad.ID, "pull", "failed", "pull access denied")
	f.logLine(bad.ID, "deploy", "secret log text that needs logs:read")

	p := f.principal(policy.ScopeProjectsRead)
	m, err := f.call(p, "explain_failed_deployment", map[string]any{"deployment": bad.ID.String()})
	require.NoError(t, err)
	require.Equal(t, []string{"logs"}, sectionErrors(m))
	require.Equal(t, "forbidden", explainList(m, "errors")[0].(map[string]any)["code"])
	require.Equal(t, "unavailable", m["logExcerptAnchor"])
	require.Equal(t, "image_pull_failed", m["likelyCause"], "explained from events and status without log access")
	raw, _ := json.Marshal(m)
	require.NotContains(t, string(raw), "secret log text")
	for _, nt := range explainList(m, "nextTools") {
		require.NotEqual(t, "tail_logs", nt.(map[string]any)["tool"], "no log tool is suggested without logs:read")
	}
}

func TestExplainFailedDeploymentWithoutReleaseOrBaseline(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "api", undeployed: true})
	dep := f.release(sv, "traefik/whoami:v1", "failed", "Assign the service to a server before deploying.")
	// No release row for the deployment.
	_, err := f.pool.Exec(context.Background(), "UPDATE deployments SET release_id = NULL WHERE id = $1", dep.ID)
	require.NoError(t, err)

	m := f.must("explain_failed_deployment", map[string]any{"deployment": dep.ID.String()})
	require.Equal(t, true, m["failed"])
	d := m["releaseDiff"].(map[string]any)
	require.Equal(t, false, d["compared"])
	require.Contains(t, d["note"], "never got a release")
	require.Equal(t, "config_invalid", m["likelyCause"])
	require.Equal(t, "no_good_release", explainStr(m, "rollbackTarget", "code"))
	require.Equal(t, "", explainStr(m, "servingNow", "deploymentId"))
	require.Contains(t, m["summary"], "Nothing is deployed yet")

	// Only the first release attempt failed: nothing to compare with.
	sv2 := f.service(svcOpts{env: f.staging, slug: "first", undeployed: true})
	dep2 := f.release(sv2, "traefik/whoami:v1", "failed", "")
	f.event(dep2.ID, "pull", "failed", "boom")
	m = f.must("explain_failed_deployment", map[string]any{"deployment": dep2.ID.String()})
	d = m["releaseDiff"].(map[string]any)
	require.Equal(t, false, d["compared"])
	require.Contains(t, d["note"], "no earlier succeeded deployment")
}

func TestExplainFailedDeploymentRollbackTargetPruned(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "api"})
	good := f.release(sv, "api:abc", "succeeded", "")
	explainSetSource(f, good, services.ReleaseSource{Type: services.SourceGit, CommitSHA: "abcdef1234567", Branch: "main"}) // built image id unknown
	bad := f.release(sv, "api:def", "failed", "")
	explainSetSource(f, bad, services.ReleaseSource{Type: services.SourceGit, CommitSHA: "fedcba7654321", Branch: "main"})
	f.event(bad.ID, "build", "failed", "")
	f.logLine(bad.ID, "build", "step 1/3")
	f.logLine(bad.ID, "build", "npm ERR! missing script: build")

	m := f.must("explain_failed_deployment", map[string]any{"deployment": bad.ID.String()})
	rt := m["rollbackTarget"].(map[string]any)
	require.Equal(t, false, rt["available"])
	require.Equal(t, "image_pruned", rt["code"])
	require.Contains(t, rt["reason"], "abcdef1")
	require.Equal(t, "build_failed", m["likelyCause"])
	require.Contains(t, m["summary"], "rollback is not possible")
	d := m["releaseDiff"].(map[string]any)
	commits := d["commits"].(map[string]any)
	require.Equal(t, "abcdef1234567", commits["from"])
	require.Equal(t, "fedcba7654321", commits["to"])
	for _, nt := range explainList(m, "nextTools") {
		require.NotEqual(t, "rollback_service", nt.(map[string]any)["tool"])
	}

	// The image is still retained: a rollback is possible.
	explainSetSource(f, good, services.ReleaseSource{Type: services.SourceGit, CommitSHA: "abcdef1234567", Branch: "main", ImageID: "sha256:abc"})
	m = f.must("explain_failed_deployment", map[string]any{"deployment": bad.ID.String()})
	rt = m["rollbackTarget"].(map[string]any)
	require.Equal(t, true, rt["available"], "%v", rt)
}

func TestExplainFailedDeploymentRollbackTargetAlreadyCurrent(t *testing.T) {
	f := newFx(t)
	sv, bad := explainSeed(f, "failed", "")
	f.event(bad.ID, "start", "failed", "container exited with code 1")
	m := f.must("explain_failed_deployment", map[string]any{"deployment": bad.ID.String()})
	// f.service made the first (succeeded) deployment current, so the target IS what runs.
	require.Equal(t, "already_current", explainStr(m, "rollbackTarget", "code"))
	require.Equal(t, "crash_on_start", m["likelyCause"])
	_ = sv
}

func TestExplainFailedDeploymentClassification(t *testing.T) {
	type tc struct {
		name, status, message string
		events                [][3]string // step, status, message
		logs                  [][2]string // stream, line
		want, under, step     string
	}
	cases := []tc{
		{name: "pull", status: "failed", events: [][3]string{{"pull", "failed", "toomanyrequests: rate limit"}}, want: "image_pull_failed", step: "pull"},
		{name: "build", status: "failed", events: [][3]string{{"build", "failed", ""}}, logs: [][2]string{{"build", "error: cannot find module 'x'"}}, want: "build_failed", step: "build"},
		{name: "predeploy", status: "failed", events: [][3]string{{"predeploy", "failed", "exit 1"}}, want: "predeploy_failed", step: "predeploy"},
		{name: "create port conflict", status: "failed", events: [][3]string{{"create", "failed", "Bind for 0.0.0.0:80 failed: port is already allocated"}}, want: "port_conflict", step: "create"},
		{name: "create config", status: "failed", events: [][3]string{{"create", "failed", "invalid memory limit"}}, want: "config_invalid", step: "create"},
		{name: "start crash", status: "failed", events: [][3]string{{"start", "failed", "container exited with code 1"}}, want: "crash_on_start", step: "start"},
		{name: "health generic", status: "failed", events: [][3]string{{"health", "failed", "health check timed out"}}, want: "health_check_failed", step: "health"},
		{name: "health refused", status: "failed", events: [][3]string{{"health", "failed", "dial tcp: connection refused"}}, want: "port_mismatch", step: "health"},
		{name: "health crash", status: "failed", events: [][3]string{{"health", "failed", "container exited (1) before healthy"}}, want: "crash_on_start", step: "health"},
		{name: "route", status: "failed", events: [][3]string{{"route", "failed", "proxy rejected the route"}}, want: "proxy_rejected", step: "route"},
		{name: "oom", status: "failed", events: [][3]string{{"start", "failed", "container OOMKilled"}}, want: "out_of_memory", step: "start"},
		{name: "disk", status: "failed", events: [][3]string{{"build", "failed", "write /var/lib/docker: no space left on device"}}, want: "disk_full", step: "build"},
		{name: "platform", status: "failed", events: [][3]string{{"start", "failed", "exec format error"}}, want: "platform_mismatch", step: "start"},
		{name: "offline", status: "failed", message: "agent1 is offline", want: "server_offline_waiting"},
		{name: "rolled back", status: "rolled_back", message: "verification failed", events: [][3]string{{"verify", "failed", "unhealthy after route switch"}}, want: "verify_failed_rolled_back", under: "health_check_failed", step: "verify"},
		{name: "rollback failed", status: "rollback_failed", events: [][3]string{{"verify", "failed", ""}}, want: "rollback_failed", under: "health_check_failed", step: "verify"},
		{name: "unknown", status: "failed", message: "something odd happened", want: "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFx(t)
			_, bad := explainSeed(f, c.status, c.message)
			for _, e := range c.events {
				f.event(bad.ID, e[0], e[1], e[2])
			}
			for _, l := range c.logs {
				f.logLine(bad.ID, l[0], l[1])
			}
			m := f.must("explain_failed_deployment", map[string]any{"deployment": bad.ID.String()})
			require.Equal(t, true, m["failed"])
			require.Equal(t, c.want, m["likelyCause"], "%v", m["smallestFix"])
			require.Equal(t, c.under, explainStr(m, "underlyingCause"))
			require.Equal(t, c.step, explainStr(m, "failedStep"))
			require.NotEmpty(t, m["smallestFix"])
			require.NotEmpty(t, m["summary"])
		})
	}
}

func TestExplainFailedDeploymentExcerptContext(t *testing.T) {
	f := newFx(t)
	_, bad := explainSeed(f, "failed", "")
	f.event(bad.ID, "build", "failed", "")
	for i := range 20 {
		f.logLine(bad.ID, "build", "step line "+string(rune('a'+i)))
	}
	f.logLine(bad.ID, "build", "ERROR: compile failed")
	for range 60 {
		f.logLine(bad.ID, "build", "after line")
	}
	m := f.must("explain_failed_deployment", map[string]any{"deployment": bad.ID.String()})
	ex := m["logExcerpt"].(map[string]any)
	text := ex["text"].(string)
	// 5 lines of context before the first error, about 40 lines in total.
	require.Contains(t, text, "step line p")
	require.NotContains(t, text, "step line o")
	require.Equal(t, float64(40), ex["lines"])
	require.Equal(t, "first_error", m["logExcerptAnchor"])
	require.Contains(t, text, "untrusted-log nonce=")

	// No error line at all: the last 30 lines.
	_, bad2 := explainSeed2(f, "calm")
	for i := range 50 {
		f.logLine(bad2.ID, "deploy", "calm line "+strings.Repeat("x", i%3))
	}
	m = f.must("explain_failed_deployment", map[string]any{"deployment": bad2.ID.String()})
	require.Equal(t, "last_lines", m["logExcerptAnchor"])
	require.Equal(t, float64(30), m["logExcerpt"].(map[string]any)["lines"])
}

// explainSeed2 adds a failed deployment to a second service.
func explainSeed2(f *fx, slug string) (db.Service, db.Deployment) {
	sv := f.service(svcOpts{env: f.staging, slug: slug})
	return sv, f.release(sv, "traefik/whoami:v2", "failed", "")
}

func TestExplainFailedDeploymentArguments(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "api"})
	f.service(svcOpts{env: f.prod, slug: "api"})

	for name, args := range map[string]map[string]any{
		"neither":     {},
		"both":        {"deployment": uuid.NewString(), "service": "x"},
		"bad uuid":    {"deployment": "nope"},
		"bad detail":  {"service": "shop/staging/api", "detail": "verbose"},
		"unknown key": {"service": "shop/staging/api", "bogus": 1},
		"ambiguous":   {"service": "api"},
		"missing":     {"service": "shop/staging/nothing"},
		"unknown dep": {"deployment": uuid.NewString()},
		"empty sel":   {"service": "  ", "deployment": ""},
	} {
		_, err := f.call(f.admin, "explain_failed_deployment", args)
		require.Error(t, err, name)
	}
	_, err := f.call(f.admin, "explain_failed_deployment", map[string]any{"service": "api"})
	require.Equal(t, mcpkit.CodeAmbiguous, errCode(err))
	_, err = f.call(f.admin, "explain_failed_deployment", map[string]any{"service": "shop/staging/api", "detail": "verbose"})
	require.Equal(t, mcpkit.CodeInvalidArgument, errCode(err))
	_, err = f.call(f.admin, "explain_failed_deployment", map[string]any{})
	require.Equal(t, mcpkit.CodeInvalidArgument, errCode(err))
}

func TestExplainFailedDeploymentToolDescriptor(t *testing.T) {
	f := newFx(t)
	tl := f.tools["explain_failed_deployment"]
	require.Equal(t, []string{policy.ScopeProjectsRead}, tl.Scopes)
	require.Equal(t, mcpkit.Hints{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: true}, tl.Hints)
	require.Equal(t, mcpkit.ToolsetCore, tl.Toolset)
}

// Slices serialize as [] never null, so the output schema holds, also when
// there is nothing to explain.
func TestExplainFailedDeploymentOutputHasNoNullSlices(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "api"})
	for name, args := range map[string]map[string]any{
		"succeeded":  {"deployment": sv.CurrentDeploymentID.String()},
		"no failure": {"service": f.selector(f.staging, "api")},
	} {
		m := f.must("explain_failed_deployment", args)
		for _, k := range []string{"timeline", "errors", "nextTools"} {
			require.IsType(t, []any{}, m[k], name+" "+k)
		}
		d := m["releaseDiff"].(map[string]any)
		for _, k := range []string{"changes", "variablesAdded", "variablesRemoved", "variablesChanged"} {
			require.IsType(t, []any{}, d[k], name+" "+k)
		}
	}
	bad := f.release(sv, "traefik/whoami:v2", "failed", "")
	m := f.must("explain_failed_deployment", map[string]any{"deployment": bad.ID.String()})
	for _, k := range []string{"timeline", "errors", "nextTools"} {
		require.IsType(t, []any{}, m[k], k)
	}
	d := m["releaseDiff"].(map[string]any)
	for _, k := range []string{"changes", "variablesAdded", "variablesRemoved", "variablesChanged"} {
		require.IsType(t, []any{}, d[k], k)
	}
}

// A worst-case detail:full result is shrunk to fit the 64 KB cap.
func TestExplainFailedDeploymentFullResultFitsCap(t *testing.T) {
	f := newFx(t)
	_, bad := explainSeed(f, "failed", strings.Repeat("s", 400))
	long := strings.Repeat("e", 320)
	for i := range 110 {
		f.event(bad.ID, "build", "started", fmt.Sprintf("%d %s", i, long))
	}
	f.event(bad.ID, "build", "failed", "the last message")
	for range 120 {
		f.logLine(bad.ID, "build", "error: "+long)
	}
	tl := f.tools["explain_failed_deployment"]
	raw, err := tl.Call(f.ctx(f.admin), f.admin, json.RawMessage(fmt.Sprintf(`{"deployment":%q,"detail":"full"}`, bad.ID)))
	require.NoError(t, err)
	b, err := json.Marshal(raw)
	require.NoError(t, err)
	require.LessOrEqual(t, len(b), mcpkit.MaxResultBytes)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	require.Contains(t, m["eventMessages"].(map[string]any)["text"], "the last message", "the newest event message survives shrinking")
}
