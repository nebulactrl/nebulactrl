package composites_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/variables"
)

const chgCanary = "sekret-canary-12345"

// chgPut sets a variable of a service through the variables service (so the
// service is staged, as a real edit would).
func chgPut(f *fx, id uuid.UUID, key, val string, secret bool) {
	f.t.Helper()
	in := variables.VariableInput{Key: key, Value: &val}
	if secret {
		t := true
		in.IsSecret = &t
	}
	_, err := f.deps.Variables.Put(f.ctx(f.admin), id, []variables.VariableInput{in})
	require.NoError(f.t, err)
}

// chgDeployments counts a service's deployments.
func chgDeployments(f *fx, id uuid.UUID) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM deployments WHERE service_id = $1", id).Scan(&n))
	return n
}

// chgJSON renders a result for substring assertions.
func chgJSON(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return string(b)
}

func chgFields(m map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	chs, _ := m["changes"].([]any)
	for _, c := range chs {
		cm := c.(map[string]any)
		out[cm["field"].(string)] = cm
	}
	return out
}

func chgReload(f *fx, id uuid.UUID) db.Service {
	f.t.Helper()
	sv, err := f.reload(id)
	require.NoError(f.t, err)
	return sv
}

func TestReviewChangesService(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "web"})
	chgPut(f, sv.ID, "DB_PASSWORD", chgCanary, true)
	chgPut(f, sv.ID, "LOG_LEVEL", "plain-value-777", false)
	cur := chgReload(f, sv.ID)
	require.Greater(t, cur.Generation, cur.AppliedGeneration)

	m := f.must("review_changes", map[string]any{"service": f.selector(f.staging, "web")})
	require.Equal(t, float64(1), m["pendingCount"])
	require.Equal(t, "shop/staging", m["environment"])
	svcs := m["services"].([]any)
	require.Len(t, svcs, 1)
	s := svcs[0].(map[string]any)
	require.Equal(t, "shop/staging/web", s["service"])
	require.Equal(t, float64(cur.Generation), s["generation"])
	require.Equal(t, float64(cur.AppliedGeneration), s["appliedGeneration"])
	require.Equal(t, false, s["production"])
	require.Equal(t, true, s["deployable"], chgJSON(t, m))
	require.Equal(t, "rolling", s["strategy"])
	require.NotEmpty(t, s["downtime"])

	fields := chgFields(s)
	require.Contains(t, fields, "variables.DB_PASSWORD")
	require.Equal(t, "added", fields["variables.DB_PASSWORD"]["kind"])
	require.Contains(t, fields, "variables.LOG_LEVEL")

	// Variables appear with keys only: neither the secret nor the plain value.
	js := chgJSON(t, m)
	require.NotContains(t, js, chgCanary)
	require.NotContains(t, js, "plain-value-777")

	// nextTools: apply_changes with the exact generation, ready to paste.
	nts := m["nextTools"].([]any)
	require.NotEmpty(t, nts)
	nt := nts[0].(map[string]any)
	require.Equal(t, "apply_changes", nt["tool"])
	items := nt["args"].(map[string]any)["services"].([]any)
	require.Equal(t, map[string]any{"service": "shop/staging/web", "expectedGeneration": float64(cur.Generation)}, items[0])
	require.Empty(t, m["errors"])
}

func TestReviewChangesNothingStaged(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})
	m := f.must("review_changes", map[string]any{"service": f.selector(f.staging, "web")})
	require.Equal(t, float64(0), m["pendingCount"])
	require.Empty(t, m["services"])
	require.Empty(t, m["nextTools"])
	require.Contains(t, m["summary"], "No staged changes")
}

func TestReviewChangesEnvironment(t *testing.T) {
	f := newFx(t)
	a := f.service(svcOpts{env: f.prod, slug: "api"})
	b := f.service(svcOpts{env: f.prod, slug: "worker"})
	f.service(svcOpts{env: f.prod, slug: "clean"})
	f.stage(a.ID)
	f.stage(b.ID)

	m := f.must("review_changes", map[string]any{"environment": f.project.Slug + "/" + f.prod.Slug, "detail": "full"})
	require.Equal(t, float64(2), m["pendingCount"])
	require.Equal(t, true, m["production"])
	svcs := m["services"].([]any)
	require.Len(t, svcs, 2)
	require.Equal(t, "shop/production/api", svcs[0].(map[string]any)["service"])
	require.Equal(t, true, svcs[0].(map[string]any)["production"])
	require.Contains(t, m["summary"], "production")
	items := m["nextTools"].([]any)[0].(map[string]any)["args"].(map[string]any)["services"].([]any)
	require.Len(t, items, 2)
}

func TestReviewChangesBlockingServiceListedWithWhy(t *testing.T) {
	f := newFx(t)
	// An unresolved reference blocks the deployment.
	sv := f.service(svcOpts{env: f.staging, slug: "web"})
	chgPut(f, sv.ID, "DB_URL", "postgres://${{missing.NEBULA_PRIVATE_HOST}}/db", false)

	m := f.must("review_changes", map[string]any{"service": f.selector(f.staging, "web")})
	s := m["services"].([]any)[0].(map[string]any)
	require.Equal(t, false, s["deployable"], chgJSON(t, m))
	require.NotEmpty(t, s["blocking"])
	require.NotEmpty(t, s["unresolvedRefs"])
	for _, nt := range m["nextTools"].([]any) {
		require.NotEqual(t, "apply_changes", nt.(map[string]any)["tool"], "a blocked service must not be offered to apply_changes")
	}
}

func TestReviewChangesArguments(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})
	f.service(svcOpts{env: f.prod, slug: "web"})

	_, err := f.call(f.admin, "review_changes", map[string]any{})
	require.Equal(t, "invalid_argument", errCode(err))
	_, err = f.call(f.admin, "review_changes", map[string]any{"service": "a", "environment": "b"})
	require.Equal(t, "invalid_argument", errCode(err))
	_, err = f.call(f.admin, "review_changes", map[string]any{"service": f.selector(f.staging, "web"), "detail": "huge"})
	require.Equal(t, "invalid_argument", errCode(err))
	_, err = f.call(f.admin, "review_changes", map[string]any{"service": f.selector(f.staging, "web"), "bogus": 1})
	require.Equal(t, "invalid_argument", errCode(err))
	// A bare slug present in two environments is ambiguous.
	_, err = f.call(f.admin, "review_changes", map[string]any{"service": "web"})
	require.Equal(t, "ambiguous_selector", errCode(err))
}
