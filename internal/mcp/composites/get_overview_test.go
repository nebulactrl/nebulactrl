package composites_test

import (
	"encoding/json"
	"github.com/google/uuid"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// ovFindingKeys lists "code|subject" of the findings under m[field].
func ovFindingKeys(m map[string]any, field string) []string {
	var out []string
	fs, _ := m[field].([]any)
	for _, f := range fs {
		fm := f.(map[string]any)
		out = append(out, fm["code"].(string)+"|"+fm["subject"].(string))
	}
	return out
}

// ovEnv returns the environment row with the given path.
func ovEnv(t *testing.T, m map[string]any, path string) map[string]any {
	t.Helper()
	for _, e := range m["environments"].([]any) {
		if em := e.(map[string]any); em["path"] == path {
			return em
		}
	}
	t.Fatalf("environment %s not in overview", path)
	return nil
}

func ovNum(m map[string]any, keys ...string) int {
	var cur any = m
	for _, k := range keys {
		cur = cur.(map[string]any)[k]
	}
	return int(cur.(float64))
}

func TestGetOverviewCountsAndAttention(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})
	api := f.service(svcOpts{env: f.staging, slug: "api"})
	f.setObserved(api.ID, "running", "unhealthy")
	worker := f.service(svcOpts{env: f.staging, slug: "worker"})
	f.stage(worker.ID)
	f.service(svcOpts{env: f.staging, slug: "draft", undeployed: true})
	shop := f.service(svcOpts{env: f.prod, slug: "shop-web"})
	f.release(shop, "traefik/whoami:2", "failed", "boom")
	f.offlineServer("edge-2")

	m := f.must("get_overview", map[string]any{})

	require.Equal(t, 1, ovNum(m, "totals", "projects"))
	require.Equal(t, 2, ovNum(m, "totals", "environments"))
	require.Equal(t, 5, ovNum(m, "totals", "services", "total"))
	require.Equal(t, 3, ovNum(m, "totals", "services", "running"))
	require.Equal(t, 1, ovNum(m, "totals", "services", "unhealthy"))
	require.Equal(t, 1, ovNum(m, "totals", "services", "neverDeployed"))
	// worker (edited) and draft (never deployed, first deployment staged)
	require.Equal(t, 2, ovNum(m, "totals", "stagedServices"))
	require.Equal(t, 1, ovNum(m, "totals", "failedDeployments24h"))

	stg := ovEnv(t, m, "shop/staging")
	require.Equal(t, false, stg["production"])
	require.Equal(t, 1, ovNum(stg, "services", "unhealthy"))
	require.Equal(t, 2, ovNum(stg, "stagedServices"))
	prod := ovEnv(t, m, "shop/production")
	require.Equal(t, true, prod["production"])
	require.Equal(t, 1, ovNum(prod, "failedDeployments24h"))
	require.Nil(t, stg["serviceRows"], "concise detail has no per-service rows")

	srv := m["servers"].(map[string]any)
	require.Equal(t, true, srv["available"])
	require.Equal(t, 2, ovNum(srv, "total"))
	require.Equal(t, 1, ovNum(srv, "online"))
	require.Equal(t, 1, ovNum(srv, "offline"))

	keys := ovFindingKeys(m, "attention")
	require.Contains(t, keys, "stale_code|shop/production/shop-web")
	require.Contains(t, keys, "unhealthy|shop/staging/api")
	require.Contains(t, keys, "server_offline|edge-2")
	require.Empty(t, sectionErrors(m))
	require.Contains(t, m["summary"], "5 services")
	require.NotEmpty(t, m["nextTools"])
}

func TestGetOverviewFullDetailAndSelectors(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})
	f.service(svcOpts{env: f.prod, slug: "shop-web"})

	m := f.must("get_overview", map[string]any{"environment": "shop/staging", "detail": "full"})
	require.Equal(t, 1, ovNum(m, "totals", "environments"))
	rows := ovEnv(t, m, "shop/staging")["serviceRows"].([]any)
	require.Len(t, rows, 1)
	require.Equal(t, "shop/staging/web", rows[0].(map[string]any)["path"])

	// project + environment slug resolves like project/env.
	m = f.must("get_overview", map[string]any{"project": "shop", "environment": "production"})
	require.Equal(t, 1, ovNum(m, "totals", "environments"))
	require.NotNil(t, ovEnv(t, m, "shop/production"))

	// limit caps the listed environments, totals still cover both.
	m = f.must("get_overview", map[string]any{"limit": 1})
	require.Len(t, m["environments"], 1)
	require.Equal(t, 1, ovNum(m, "environmentsOmitted"))
	require.Equal(t, 2, ovNum(m, "totals", "environments"))
}

func TestGetOverviewPartialWithoutServersScope(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})
	f.offlineServer("edge-2")

	p := f.principal(policy.ScopeProjectsRead)
	m, err := f.call(p, "get_overview", map[string]any{})
	require.NoError(t, err)
	require.Equal(t, []string{"servers"}, sectionErrors(m))
	errs := m["errors"].([]any)
	require.Equal(t, "forbidden", errs[0].(map[string]any)["code"])
	require.Equal(t, false, m["servers"].(map[string]any)["available"])
	require.Equal(t, 1, ovNum(m, "totals", "services", "total"))
}

func TestGetOverviewRespectsProjectRestriction(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})
	// A principal limited to an unrelated project sees nothing of "shop".
	other := f.principal(policy.AllScopes...)
	other.ProjectIDs = []uuid.UUID{uuid.New()}
	m, err := f.call(other, "get_overview", map[string]any{})
	require.NoError(t, err)
	require.Equal(t, 0, ovNum(m, "totals", "services", "total"))
	b, _ := json.Marshal(m)
	require.False(t, strings.Contains(string(b), "web"), string(b))
}

func TestGetOverviewArgumentErrors(t *testing.T) {
	f := newFx(t)
	_, err := f.call(f.admin, "get_overview", map[string]any{"detail": "everything"})
	require.Equal(t, "invalid_argument", errCode(err))
	_, err = f.call(f.admin, "get_overview", map[string]any{"project": "nope"})
	require.Equal(t, "not_found", errCode(err))
	_, err = f.call(f.admin, "get_overview", map[string]any{"bogus": 1})
	require.Equal(t, "invalid_argument", errCode(err))
	_, err = f.call(f.admin, "get_overview", map[string]any{"limit": -1})
	require.Equal(t, "invalid_argument", errCode(err))
}
