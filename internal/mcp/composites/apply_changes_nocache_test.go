package composites_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func applyNoCacheFlag(f *fx, depID any) bool {
	f.t.Helper()
	var v bool
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT no_cache FROM deployments WHERE id = $1", depID).Scan(&v))
	return v
}

// applyAdvertiseBuildOptions makes every server's agent advertise build.options.
func applyAdvertiseBuildOptions(f *fx) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), `UPDATE servers SET facts = '{"os":"linux","capabilities":["build.options"]}'::jsonb`)
	require.NoError(f.t, err)
}

// apply_changes noCache builds the git services of the call without cache for
// those deployments only; an image service in the same call deploys as usual.
func TestApplyChangesNoCacheMarksGitServicesOnly(t *testing.T) {
	f := newFx(t)
	applyAdvertiseBuildOptions(f)
	gitID, gitGen := applyStagedService(f, "api")
	_, err := f.pool.Exec(context.Background(),
		`UPDATE services SET source_type = 'git', config = config || '{"gitUrl":"https://example.com/acme/api.git","gitBranch":"main"}'::jsonb WHERE id = $1`, gitID)
	require.NoError(t, err)
	_, imgGen := applyStagedService(f, "cache")

	m := f.must("apply_changes", map[string]any{
		"services": []any{
			map[string]any{"service": f.selector(f.staging, "api"), "expectedGeneration": gitGen},
			map[string]any{"service": f.selector(f.staging, "cache"), "expectedGeneration": imgGen},
		},
		"noCache": true,
	})
	deps := m["deployments"].([]any)
	require.Len(t, deps, 2, chgJSON(t, m))
	byService := map[string]any{}
	for _, d := range deps {
		dm := d.(map[string]any)
		byService[dm["service"].(string)] = dm["deploymentId"]
	}
	require.True(t, applyNoCacheFlag(f, byService["shop/staging/api"]), "the git service builds without cache")
	require.False(t, applyNoCacheFlag(f, byService["shop/staging/cache"]), "an image service has nothing to build")
}

func TestApplyChangesWithoutNoCacheBuildsNormally(t *testing.T) {
	f := newFx(t)
	id, gen := applyStagedService(f, "web")
	_, err := f.pool.Exec(context.Background(),
		`UPDATE services SET source_type = 'git', config = config || '{"gitUrl":"https://example.com/acme/web.git","gitBranch":"main"}'::jsonb WHERE id = $1`, id)
	require.NoError(t, err)
	m := f.must("apply_changes", map[string]any{
		"services": []any{map[string]any{"service": f.selector(f.staging, "web"), "expectedGeneration": gen}},
	})
	d := m["deployments"].([]any)[0].(map[string]any)
	require.False(t, applyNoCacheFlag(f, d["deploymentId"]))
}
