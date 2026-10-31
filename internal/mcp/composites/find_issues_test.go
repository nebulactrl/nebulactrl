package composites_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/variables"
)

const fiCanary = "sekret-canary-12345"

// fiDomain inserts a domain row with the given DNS state.
func fiDomain(f *fx, svc uuid.UUID, host string, dnsOK *bool, status string) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO domains (service_id, hostname, target_port, status, status_message, dns_ok, dns_checked_at)
		 VALUES ($1, $2, 80, $3, 'A record points elsewhere', $4, now())`, svc, host, status, dnsOK)
	require.NoError(f.t, err)
}

func fiFinding(m map[string]any, code, subject string) map[string]any {
	fs, _ := m["findings"].([]any)
	for _, x := range fs {
		fm := x.(map[string]any)
		if fm["code"] == code && fm["subject"] == subject {
			return fm
		}
	}
	return nil
}

func TestFindIssuesDetectsAndRanks(t *testing.T) {
	f := newFx(t)
	ctx := f.ctx(f.admin)

	// Runtime state.
	api := f.service(svcOpts{env: f.staging, slug: "api"})
	f.setObserved(api.ID, "running", "unhealthy")
	crash := f.service(svcOpts{env: f.staging, slug: "crash"})
	f.setObserved(crash.ID, "restarting", "unknown")
	gone := f.service(svcOpts{env: f.prod, slug: "gone"})
	f.setObserved(gone.ID, "exited", "unknown")

	// Failed latest deployment while the older release serves; failed rollback.
	stale := f.service(svcOpts{env: f.prod, slug: "shop-web"})
	f.release(stale, "traefik/whoami:2", "failed", "pull access denied")
	rb := f.service(svcOpts{env: f.staging, slug: "rb"})
	f.release(rb, "traefik/whoami:3", "rollback_failed", "agent lost")

	// Staged and forgotten, with an unresolved reference and a secret.
	forgotten := f.service(svcOpts{env: f.staging, slug: "forgotten"})
	_, err := f.deps.Variables.Put(ctx, forgotten.ID, []variables.VariableInput{
		{Key: "DB_HOST", Value: new("${{ghost.NEBULA_PRIVATE_HOST}}")},
		{Key: "API_KEY", Value: new(fiCanary), IsSecret: new(true)},
	})
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET updated_at = now() - interval '3 days' WHERE id = $1", forgotten.ID)
	require.NoError(t, err)

	// Offline host with a deployment waiting on it.
	srv := f.offlineServer("edge-2")
	wait := f.service(svcOpts{env: f.staging, slug: "waiting", server: &srv})
	f.release(wait, "traefik/whoami:4", "releasing", "")

	// Domains.
	no, yes := false, true
	web := f.service(svcOpts{env: f.staging, slug: "web"})
	fiDomain(f, web.ID, "bad.example.com", &no, "pending")
	fiDomain(f, web.ID, "good.example.com", &yes, "active")
	fiDomain(f, web.ID, "broken.example.com", nil, "error")

	m := f.must("find_issues", map[string]any{})
	b, _ := json.Marshal(m)
	require.NotContains(t, string(b), fiCanary)
	require.Empty(t, sectionErrors(m), string(b))

	for _, want := range []struct{ code, subject, severity string }{
		{"unhealthy", "shop/staging/api", "warning"},
		{"crash_looping", "shop/staging/crash", "warning"},
		{"stopped_unexpectedly", "shop/production/gone", "critical"}, // production lifts to critical
		{"stale_code", "shop/production/shop-web", "warning"},
		{"rollback_failed", "shop/staging/rb", "critical"},
		{"staged_changes_forgotten", "shop/staging/forgotten", "warning"},
		{"deployment_waiting_on_offline_server", "shop/staging/waiting", "critical"},
		{"server_offline", "edge-2", "critical"},
		{"domain_dns_mismatch", "bad.example.com", "warning"},
		{"domain_error", "broken.example.com", "warning"},
	} {
		got := fiFinding(m, want.code, want.subject)
		require.NotNil(t, got, "missing %s on %s in %s", want.code, want.subject, string(b))
		require.Equal(t, want.severity, got["severity"], want.code)
		require.NotEmpty(t, got["title"])
		require.NotNil(t, got["suggest"], want.code+" has no suggested tool")
		require.NotEmpty(t, got["evidence"], want.code)
	}
	require.Nil(t, fiFinding(m, "domain_dns_mismatch", "good.example.com"))

	// The unresolved reference blocks applying the staged change.
	require.NotNil(t, fiFinding(m, "unresolved_reference", "shop/staging/forgotten"), string(b))

	// Ranked: criticals first.
	fs := m["findings"].([]any)
	seenNonCritical := false
	for _, x := range fs {
		sev := x.(map[string]any)["severity"]
		if sev != "critical" {
			seenNonCritical = true
		} else {
			require.False(t, seenNonCritical, "a critical finding follows a non-critical one")
		}
	}
	counts := m["counts"].(map[string]any)
	require.GreaterOrEqual(t, int(counts["critical"].(float64)), 4)
	require.NotEmpty(t, m["nextTools"])
	require.Contains(t, m["summary"], "finding")
}

func TestFindIssuesSeverityAndLimit(t *testing.T) {
	f := newFx(t)
	api := f.service(svcOpts{env: f.staging, slug: "api"})
	f.setObserved(api.ID, "running", "unhealthy")
	stopped := f.service(svcOpts{env: f.staging, slug: "idle"})
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET desired_state = 'stopped' WHERE id = $1", stopped.ID)
	require.NoError(t, err)
	rb := f.service(svcOpts{env: f.prod, slug: "rb"})
	f.release(rb, "traefik/whoami:3", "rollback_failed", "x")

	m := f.must("find_issues", map[string]any{"severity": "critical"})
	for _, x := range m["findings"].([]any) {
		require.Equal(t, "critical", x.(map[string]any)["severity"])
	}
	require.Len(t, m["findings"], 1)

	m = f.must("find_issues", map[string]any{"severity": "info"})
	require.NotNil(t, fiFinding(m, "stopped", "shop/staging/idle"))

	m = f.must("find_issues", map[string]any{"severity": "info", "limit": 1})
	require.Len(t, m["findings"], 1)
	require.Equal(t, 2, int(m["omitted"].(float64)))

	m = f.must("find_issues", map[string]any{"environment": "shop/production", "severity": "info"})
	require.Nil(t, fiFinding(m, "unhealthy", "shop/staging/api"))
	require.NotNil(t, fiFinding(m, "rollback_failed", "shop/production/rb"))

	_, err = f.call(f.admin, "find_issues", map[string]any{"severity": "panic"})
	require.Equal(t, "invalid_argument", errCode(err))
	_, err = f.call(f.admin, "find_issues", map[string]any{"project": "nope"})
	require.Equal(t, "not_found", errCode(err))
}

func TestFindIssuesHealthyEstateAndPartialWithoutServers(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})
	m := f.must("find_issues", map[string]any{})
	require.Empty(t, m["findings"])
	require.Contains(t, m["summary"], "No problems")

	// Without servers:read the server section is skipped (reported), but an
	// offline host is still found through the services placed on it.
	srv := f.offlineServer("edge-2")
	f.service(svcOpts{env: f.staging, slug: "remote", server: &srv})
	p := f.principal(policy.ScopeProjectsRead)
	m, err := f.call(p, "find_issues", map[string]any{})
	require.NoError(t, err)
	require.Equal(t, []string{"servers"}, sectionErrors(m))
	require.Equal(t, "forbidden", m["errors"].([]any)[0].(map[string]any)["code"])
	got := fiFinding(m, "server_offline", "edge-2")
	require.NotNil(t, got)
	require.True(t, strings.Contains(strings.Join(anyStrings(got["evidence"]), " "), "placed on it"))
}

func anyStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
