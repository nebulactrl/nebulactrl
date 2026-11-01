package composites_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/variables"
)

const diagCanary = "sekret-canary-12345"

// diagFindingCodes returns the codes of a result's findings.
func diagFindingCodes(m map[string]any) []string {
	var out []string
	fs, _ := m["findings"].([]any)
	for _, f := range fs {
		out = append(out, f.(map[string]any)["code"].(string))
	}
	return out
}

func diagNextTools(m map[string]any) []string {
	var out []string
	ns, _ := m["nextTools"].([]any)
	for _, n := range ns {
		out = append(out, n.(map[string]any)["tool"].(string))
	}
	return out
}

func diagNoCanary(t *testing.T, m map[string]any) {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	require.NotContains(t, string(b), diagCanary)
}

func TestDiagnoseServiceHappyPath(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "web"})
	f.ag.lines[sv.ID.String()] = []string{"listening on :80", "ERROR: db timeout", "request served"}

	secret, notSecret := true, false
	val, plain := diagCanary, "hello"
	_, err := f.deps.Variables.Put(f.ctx(f.admin), sv.ID, []variables.VariableInput{
		{Key: "API_TOKEN", Value: &val, IsSecret: &secret},
		{Key: "GREETING", Value: &plain, IsSecret: &notSecret},
		{Key: "BROKEN", Value: new("${{ghost.KEY}}"), IsSecret: &notSecret},
	})
	require.NoError(t, err)
	_, err = f.deps.Domains.Create(f.ctx(f.admin), sv.ID, "web.apps.example.com", 80, domains.RuleInput{})
	require.NoError(t, err)

	// A newer failed deployment while the old release still serves, plus staged edits (the Put above staged).
	failed := f.release(sv, "traefik/whoami:broken", "failed", "pull access denied")
	f.event(failed.ID, "pull", "failed", "pull access denied")

	m := f.must("diagnose_service", map[string]any{"service": f.selector(f.staging, "web")})

	require.Equal(t, "degraded", m["verdict"], "running and healthy, but the stale-code warning degrades it")
	codes := diagFindingCodes(m)
	require.Contains(t, codes, "stale_code")
	require.Contains(t, codes, "staged_changes")
	require.Contains(t, codes, "errors_in_logs")
	require.Empty(t, sectionErrors(m))

	svc := m["service"].(map[string]any)
	require.Equal(t, sv.ID.String(), svc["id"])
	require.Equal(t, "shop/staging/web", svc["selector"])
	require.Equal(t, "traefik/whoami:latest", svc["source"])
	require.Equal(t, false, svc["production"])
	require.Equal(t, true, svc["pending"])
	require.Equal(t, "running", svc["observedState"])
	require.Equal(t, "agent1", svc["server"])

	deps := m["deployments"].([]any)
	require.Len(t, deps, 2)
	d0 := deps[0].(map[string]any)
	require.Equal(t, failed.ID.String(), d0["id"])
	require.Equal(t, "pull", d0["failedStep"])
	msg := d0["statusMessage"].(map[string]any)
	require.Equal(t, true, msg["untrusted"])
	require.Contains(t, msg["text"], "pull access denied")
	require.Equal(t, true, deps[1].(map[string]any)["current"])

	vars := m["variables"].(map[string]any)
	require.EqualValues(t, 3, vars["total"])
	require.EqualValues(t, 1, vars["secrets"])
	require.Len(t, vars["unresolved"], 1)
	require.Equal(t, "BROKEN", vars["unresolved"].([]any)[0].(map[string]any)["key"])
	require.Contains(t, codes, "unresolved_refs")
	for _, k := range vars["keys"].([]any) {
		require.NotContains(t, k.(map[string]any), "value")
	}

	doms := m["domains"].([]any)
	require.Len(t, doms, 1)
	require.Equal(t, "web.apps.example.com", doms[0].(map[string]any)["hostname"])

	logs := m["logs"].(map[string]any)
	require.Equal(t, true, logs["untrusted"])
	require.Contains(t, logs["text"], "ERROR: db timeout")
	require.EqualValues(t, 3, logs["lines"])

	srv := m["server"].(map[string]any)
	require.Equal(t, "online", srv["status"])
	require.NotEmpty(t, m["health"].(map[string]any)["type"])

	next := diagNextTools(m)
	require.Contains(t, next, "explain_failed_deployment")
	require.Contains(t, next, "review_changes")
	require.Contains(t, next, "verify_service")
	require.NotEmpty(t, m["summary"])
	diagNoCanary(t, m)
}

func TestDiagnoseServiceDown(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "api"})
	f.setObserved(sv.ID, "exited", "unknown")

	m := f.must("diagnose_service", map[string]any{"service": sv.ID.String(), "detail": "full"})
	require.Equal(t, "down", m["verdict"])
	require.Contains(t, diagFindingCodes(m), "container_exited")
	require.Contains(t, diagNextTools(m), "service_restart")
	require.Equal(t, "critical", m["findings"].([]any)[0].(map[string]any)["severity"])
}

func TestDiagnoseServiceNotDeployed(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "fresh", undeployed: true})

	m := f.must("diagnose_service", map[string]any{"service": sv.ID.String()})
	require.Equal(t, "unknown", m["verdict"])
	require.Equal(t, "the service has no running deployment", m["logsReason"])
	require.Empty(t, sectionErrors(m), "a service without a running deployment explains instead of failing")
	require.Contains(t, diagFindingCodes(m), "not_deployed")
	require.Zero(t, m["logs"].(map[string]any)["lines"])
}

func TestDiagnoseServicePartialFailures(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "web"})
	f.ag.logErr = errors.New("boom")

	m := f.must("diagnose_service", map[string]any{"service": sv.ID.String()})
	require.Equal(t, []string{"logs"}, sectionErrors(m), "only the runtime log section fails")
	require.Equal(t, "healthy", m["verdict"])
	require.Len(t, m["deployments"], 1)
	require.NotContains(t, m["summary"], "boom")

	f.ag.logErr = nil
	noLogs := f.principal(policy.ScopeProjectsRead)
	out, err := f.call(noLogs, "diagnose_service", map[string]any{"service": sv.ID.String()})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"logs", "server"}, sectionErrors(out))
	for _, e := range out["errors"].([]any) {
		require.Equal(t, "forbidden", e.(map[string]any)["code"])
	}
	require.Equal(t, "healthy", out["verdict"], "scope-limited sections do not change the verdict")
	require.Len(t, out["deployments"], 1)
}

func TestDiagnoseServiceArguments(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.prod, slug: "web"})
	f.service(svcOpts{env: f.staging, slug: "web"})

	_, err := f.call(f.admin, "diagnose_service", map[string]any{"service": "shop/staging/web", "detail": "everything"})
	require.Equal(t, "invalid_argument", errCode(err))
	_, err = f.call(f.admin, "diagnose_service", map[string]any{})
	require.Equal(t, "invalid_argument", errCode(err))
	_, err = f.call(f.admin, "diagnose_service", map[string]any{"service": "web"})
	require.Equal(t, "ambiguous_selector", errCode(err))
	var te *mcpkit.Error
	require.ErrorAs(t, err, &te)
	require.Len(t, te.Data.(map[string]any)["choices"], 2)
	_, err = f.call(f.admin, "diagnose_service", map[string]any{"service": "shop/staging/nope"})
	require.Equal(t, "not_found", errCode(err))
	_, err = f.call(f.admin, "diagnose_service", map[string]any{"service": "shop/staging/web", "bogus": 1})
	require.Equal(t, "invalid_argument", errCode(err))
}
