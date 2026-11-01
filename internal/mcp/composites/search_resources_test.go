package composites_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// srMatches returns the matches of a search_resources result.
func srMatches(m map[string]any) []map[string]any {
	var out []map[string]any
	for _, x := range m["matches"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func srFind(m map[string]any, typ, path string) map[string]any {
	for _, x := range srMatches(m) {
		if x["type"] == typ && x["path"] == path {
			return x
		}
	}
	return nil
}

func TestSearchResourcesFindsEveryType(t *testing.T) {
	f := newFx(t)
	web := f.service(svcOpts{env: f.staging, slug: "web"})
	gw := f.service(svcOpts{env: f.staging, slug: "api-gateway", image: "ghcr.io/acme/gateway:1.4"})
	no := false
	fiDomain(f, web.ID, "shop.example.com", &no, "pending")

	// Deployment with a commit, release with an image digest.
	dep := f.release(gw, "registry.example.com/acme/api@sha256:abcdef0123456789", "succeeded", "")
	_, err := f.pool.Exec(context.Background(), "UPDATE deployments SET commit_sha = 'deadbeef1234567890' WHERE id = $1", dep.ID)
	require.NoError(t, err)

	// Services: fuzzy (separators ignored), prefix and image.
	m := f.must("search_resources", map[string]any{"query": "apigate"})
	got := srFind(m, "service", "shop/staging/api-gateway")
	require.NotNil(t, got, "%v", m["matches"])
	require.Equal(t, gw.ID.String(), got["id"])
	require.Equal(t, "slug", got["matchedOn"])
	m = f.must("search_resources", map[string]any{"query": "GHCR.io/acme", "types": []string{"service"}})
	require.Equal(t, "image", srFind(m, "service", "shop/staging/api-gateway")["matchedOn"])

	// Ranking: the exact slug beats a substring match.
	m = f.must("search_resources", map[string]any{"query": "web"})
	ms := srMatches(m)
	require.Equal(t, "shop/staging/web", ms[0]["path"])
	require.Equal(t, float64(100), ms[0]["score"])

	// Projects and environments.
	m = f.must("search_resources", map[string]any{"query": "SHOP"})
	require.NotNil(t, srFind(m, "project", "shop"))
	m = f.must("search_resources", map[string]any{"query": "staging", "types": []string{"environment"}})
	require.NotNil(t, srFind(m, "environment", "shop/staging"))

	// Domains, case-insensitive; the path works as a service selector.
	m = f.must("search_resources", map[string]any{"query": "SHOP.Example"})
	d := srFind(m, "domain", "shop.example.com")
	require.NotNil(t, d)
	require.Contains(t, d["detail"], "shop/staging/web")
	require.Contains(t, d["detail"], "dns not ok")

	// Servers by name, public IP and base domain.
	for _, q := range []string{"agent1", "203.0.113", "apps.example"} {
		m = f.must("search_resources", map[string]any{"query": q, "types": []string{"server"}})
		require.NotNil(t, srFind(m, "server", f.server.ID.String()), q)
	}

	// Deployment id prefix and commit SHA.
	m = f.must("search_resources", map[string]any{"query": dep.ID.String()[:8]})
	require.NotNil(t, srFind(m, "deployment", dep.ID.String()), "%v", m["matches"])
	m = f.must("search_resources", map[string]any{"query": "DEADBEEF"})
	cm := srFind(m, "deployment", dep.ID.String())
	require.NotNil(t, cm)
	require.Equal(t, "commitSha", cm["matchedOn"])

	// Image digest finds the release.
	m = f.must("search_resources", map[string]any{"query": "sha256:abcdef01"})
	rel := srFind(m, "release", "shop/staging/api-gateway")
	require.NotNil(t, rel, "%v", m["matches"])
	require.Contains(t, rel["detail"], dep.ID.String())

	// Next tool points at the best match.
	require.Equal(t, "diagnose_service", m["nextTools"].([]any)[0].(map[string]any)["tool"])
	require.Empty(t, sectionErrors(m))
}

func TestSearchResourcesTypesLimitAndNoMatch(t *testing.T) {
	f := newFx(t)
	for _, s := range []string{"web-a", "web-b", "web-c"} {
		f.service(svcOpts{env: f.staging, slug: s})
	}
	m := f.must("search_resources", map[string]any{"query": "web", "limit": 2})
	require.Len(t, m["matches"], 2)
	require.Equal(t, float64(3), m["total"])
	require.Contains(t, m["summary"], "1 more")

	m = f.must("search_resources", map[string]any{"query": "web", "types": []string{"server"}})
	require.Empty(t, m["matches"])
	require.Contains(t, m["summary"], "No match")

	_, err := f.call(f.admin, "search_resources", map[string]any{"query": "w"})
	require.Equal(t, "invalid_argument", errCode(err))
	_, err = f.call(f.admin, "search_resources", map[string]any{"query": "web", "types": []string{"pod"}})
	require.Equal(t, "invalid_argument", errCode(err))
	_, err = f.call(f.admin, "search_resources", map[string]any{})
	require.Equal(t, "invalid_argument", errCode(err))
}

func TestSearchResourcesPartialAndPolicy(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})

	// Without servers:read servers are not searched and the section says so.
	p := f.principal(policy.ScopeProjectsRead)
	m, err := f.call(p, "search_resources", map[string]any{"query": "agent1"})
	require.NoError(t, err)
	require.Equal(t, []string{"servers"}, sectionErrors(m))
	require.Equal(t, "forbidden", m["errors"].([]any)[0].(map[string]any)["code"])
	require.Empty(t, m["matches"])
	m, err = f.call(p, "search_resources", map[string]any{"query": "web"})
	require.NoError(t, err)
	require.NotNil(t, srFind(m, "service", "shop/staging/web"))

	// A caller limited to another project finds nothing of this one.
	other := f.principal(policy.AllScopes...)
	other.ProjectIDs = []uuid.UUID{uuid.New()}
	m, err = f.call(other, "search_resources", map[string]any{"query": "web"})
	require.NoError(t, err)
	require.Empty(t, m["matches"])
	m, err = f.call(other, "search_resources", map[string]any{"query": "shop"})
	require.NoError(t, err)
	require.Empty(t, m["matches"])
}
