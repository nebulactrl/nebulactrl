package variables_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/variables"
)

func TestRotateServiceVariablePlansStagesAndRestagesReferrers(t *testing.T) {
	f := newFx(t)
	env := f.newEnv("rot")
	app, dep := f.addSvc(env, "app"), f.addSvc(env, "dep")
	_, err := f.svc.Put(f.ctx, app.ID, []variables.VariableInput{in("SESSION", "${{secret(16)}}"), in("PLAIN", "v")})
	require.NoError(t, err)
	_, err = f.svc.Put(f.ctx, dep.ID, []variables.VariableInput{in("UPSTREAM_SESSION", "${{app.SESSION}}")})
	require.NoError(t, err)
	old := f.value(app.ID, "SESSION")
	genApp, genDep := f.gen(app.ID), f.gen(dep.ID)

	// Missing and hand-written variables cannot be rotated.
	_, err = f.svc.RotateVariable(f.ctx, app.ID, "NOPE", false)
	require.Equal(t, "not_found", apiCode(t, err))
	_, err = f.svc.RotateVariable(f.ctx, app.ID, "PLAIN", false)
	require.Equal(t, "not_rotatable", apiCode(t, err))

	// A dry run reports who is affected, in deploy order, and writes nothing.
	plan, err := f.svc.RotateVariable(f.ctx, app.ID, "SESSION", true)
	require.NoError(t, err)
	require.False(t, plan.Staged)
	require.Equal(t, variables.KindAppSecret, plan.Kind)
	require.Equal(t, []string{"app", "dep"}, plan.DeployOrder, "the provider deploys before its consumer")
	require.Len(t, plan.Affected, 2)
	require.Equal(t, []string{"SESSION"}, plan.Affected[0].Via)
	require.Equal(t, old, f.value(app.ID, "SESSION"))
	require.Equal(t, genApp, f.gen(app.ID))
	require.Equal(t, genDep, f.gen(dep.ID))

	plan, err = f.svc.RotateVariable(f.ctx, app.ID, "SESSION", false)
	require.NoError(t, err)
	require.True(t, plan.Staged)
	require.NotEqual(t, old, f.value(app.ID, "SESSION"))
	require.Len(t, f.value(app.ID, "SESSION"), 16)
	require.Greater(t, f.gen(app.ID), genApp)
	require.Greater(t, f.gen(dep.ID), genDep, "the referring service is restaged")
	require.Equal(t, 1, f.auditCount("variable.rotate", app.ID))
	hist, err := f.svc.History(f.ctx, app.ID, "SESSION")
	require.NoError(t, err)
	require.Equal(t, "rotate", hist[0].Op)
}

func TestRotateBlocksCredentialsTheEngineReadsOnlyOnInit(t *testing.T) {
	f := newFx(t)
	env := f.newEnv("rotblock")
	app := f.addSvc(env, "app")
	_, err := f.q.InsertVolume(context.Background(), db.InsertVolumeParams{ServiceID: app.ID, Name: "data", MountPath: "/data"})
	require.NoError(t, err)
	_, err = f.svc.Put(f.ctx, app.ID, []variables.VariableInput{in("DB_PASSWORD", "${{secret(20)}}")})
	require.NoError(t, err)

	// Never deployed: the data directory does not exist yet, so it may be regenerated.
	plan, err := f.svc.RotateVariable(f.ctx, app.ID, "DB_PASSWORD", true)
	require.NoError(t, err)
	require.Equal(t, variables.KindEngineBound, plan.Kind)
	require.Empty(t, plan.Blockers)

	// Once deployed, the plan carries the blocker and staging is refused.
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET applied_generation = 1 WHERE id = $1", app.ID)
	require.NoError(t, err)
	before := f.value(app.ID, "DB_PASSWORD")
	plan, err = f.svc.RotateVariable(f.ctx, app.ID, "DB_PASSWORD", true)
	require.NoError(t, err)
	require.Len(t, plan.Blockers, 1)
	require.Equal(t, "rotation_unsupported", plan.Blockers[0].Code)
	_, err = f.svc.RotateVariable(f.ctx, app.ID, "DB_PASSWORD", false)
	require.Equal(t, "rotation_unsupported", apiCode(t, err))
	require.Equal(t, before, f.value(app.ID, "DB_PASSWORD"))
}
