package composites_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// rollbackFx is a service with two succeeded deployments: old (v1) and the
// running one (v2).
type rollbackFx struct {
	sv       db.Service
	old, cur db.Deployment
}

func rollbackSeed(f *fx, env db.Environment, slug string) rollbackFx {
	f.t.Helper()
	sv := f.service(svcOpts{env: env, slug: slug, image: "ghcr.io/acme/app:v1"})
	var old db.Deployment
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT id FROM deployments WHERE service_id = $1", sv.ID).Scan(&old.ID))
	old.ServiceID = sv.ID
	time.Sleep(5 * time.Millisecond)
	cur := f.release(sv, "ghcr.io/acme/app:v2", "succeeded", "")
	require.NoError(f.t, f.q.MarkServiceDeployed(context.Background(), db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &cur.ID, AppliedGeneration: sv.Generation}))
	return rollbackFx{sv: sv, old: old, cur: cur}
}

func rollbackStr(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		cur = cur.(map[string]any)[p]
	}
	return cur
}

func TestRollbackServiceDryRunPreview(t *testing.T) {
	f := newFx(t)
	r := rollbackSeed(f, f.staging, "web")
	chgPut(f, r.sv.ID, "FEATURE", chgCanary, true)

	m := f.must("rollback_service", map[string]any{"service": f.selector(f.staging, "web"), "dryRun": true})
	require.Equal(t, true, m["dryRun"])
	require.Equal(t, false, m["started"])
	require.Nil(t, m["deployment"])
	require.Equal(t, r.old.ID.String(), rollbackStr(m, "preview", "target", "deploymentId"))
	require.Equal(t, "ghcr.io/acme/app:v1", rollbackStr(m, "preview", "target", "image"))
	require.Equal(t, r.cur.ID.String(), rollbackStr(m, "preview", "current", "deploymentId"))
	require.Equal(t, deployments.RollbackDisclaimer, rollbackStr(m, "preview", "notRolledBack"))
	require.Equal(t, deployments.RollbackDisclaimer, m["disclaimer"])
	require.Contains(t, rollbackStr(m, "preview", "rollsBack").([]any)[0], "ghcr.io/acme/app:v1")
	require.NotEmpty(t, rollbackStr(m, "preview", "downtime"))
	require.NotEmpty(t, rollbackStr(m, "preview", "imageCheck"))
	require.Contains(t, rollbackStr(m, "preview", "variablesSnapshot"), "pruned", "seeded releases carry no variables")
	require.Contains(t, rollbackStr(m, "preview", "stagedChanges"), "staged")
	require.Equal(t, 2, chgDeployments(f, r.sv.ID), "a dry run starts nothing")
	require.NotContains(t, chgJSON(t, m), chgCanary)
	require.Equal(t, "rollback_service", m["nextTools"].([]any)[0].(map[string]any)["tool"])
}

func TestRollbackServiceStartsRollback(t *testing.T) {
	f := newFx(t)
	r := rollbackSeed(f, f.staging, "web")

	m := f.must("rollback_service", map[string]any{"service": f.selector(f.staging, "web")})
	require.Equal(t, true, m["started"], chgJSON(t, m))
	require.Equal(t, false, m["dryRun"])
	d := m["deployment"].(map[string]any)
	require.Equal(t, "rollback", d["trigger"])
	require.Equal(t, "queued", d["status"])
	require.Contains(t, m["disclaimer"], "Databases, volumes and external services are not rolled back.")

	id, actor, ok := applyDeployment(f, r.sv.ID, "rollback")
	require.True(t, ok)
	require.Equal(t, id.String(), d["deploymentId"])
	require.Equal(t, "mcp", actor)
	var from *uuid.UUID
	var rel uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT rolled_back_from, release_id FROM deployments WHERE id = $1", id).Scan(&from, &rel))
	require.NotNil(t, from)
	require.Equal(t, r.cur.ID, *from)
	var oldRel uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT release_id FROM deployments WHERE id = $1", r.old.ID).Scan(&oldRel))
	require.Equal(t, oldRel, rel)
	require.Equal(t, "verify_service", m["nextTools"].([]any)[0].(map[string]any)["tool"])
}

func TestRollbackServiceSelectors(t *testing.T) {
	f := newFx(t)
	r := rollbackSeed(f, f.staging, "web")
	var oldRel uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT release_id FROM deployments WHERE id = $1", r.old.ID).Scan(&oldRel))
	sha := "0123456789abcdef0123456789abcdef01234567"
	_, err := f.pool.Exec(context.Background(), `UPDATE releases SET source = jsonb_build_object('type', 'git', 'commitSha', $2::text) WHERE id = $1`, oldRel, sha)
	require.NoError(t, err)
	sel := f.selector(f.staging, "web")

	for name, to := range map[string]string{"release id": oldRel.String(), "deployment id": r.old.ID.String(), "sha7": sha[:7], "full sha": sha, "upper sha7": "0123456"} {
		m := f.must("rollback_service", map[string]any{"service": sel, "to": to, "dryRun": true})
		require.Equal(t, r.old.ID.String(), rollbackStr(m, "preview", "target", "deploymentId"), name)
	}
	m := f.must("rollback_service", map[string]any{"service": sel, "to": sha[:7], "dryRun": true})
	require.Equal(t, sha, rollbackStr(m, "preview", "rebuildCommit"), "a git release offers its commit for a rebuild")
	require.Contains(t, m["summary"], "commit 0123456")

	_, err = f.call(f.admin, "rollback_service", map[string]any{"service": sel, "to": "nonsense"})
	require.Equal(t, "invalid_argument", errCode(err))
	_, err = f.call(f.admin, "rollback_service", map[string]any{"service": sel, "to": "abcdef0", "dryRun": true})
	require.Equal(t, "not_found", errCode(err))
	_, err = f.call(f.admin, "rollback_service", map[string]any{"service": sel, "to": uuid.NewString(), "dryRun": true})
	require.Equal(t, "not_found", errCode(err))
	require.Equal(t, 2, chgDeployments(f, r.sv.ID))
}

func TestRollbackServiceErrorCodes(t *testing.T) {
	f := newFx(t)
	r := rollbackSeed(f, f.staging, "web")
	failed := f.release(r.sv, "ghcr.io/acme/app:v3", "failed", "boom")
	sel := f.selector(f.staging, "web")

	// The running deployment is already current.
	_, err := f.call(f.admin, "rollback_service", map[string]any{"service": sel, "to": r.cur.ID.String()})
	require.Equal(t, "already_current", errCode(err))
	// A deployment that never succeeded cannot be rolled back to.
	_, err = f.call(f.admin, "rollback_service", map[string]any{"service": sel, "to": failed.ID.String()})
	require.Equal(t, "not_rollbackable", errCode(err))
	// "previous" skips the failed one and the running one.
	m := f.must("rollback_service", map[string]any{"service": sel, "dryRun": true})
	require.Equal(t, r.old.ID.String(), rollbackStr(m, "preview", "target", "deploymentId"))
	require.Equal(t, 3, chgDeployments(f, r.sv.ID))

	// No earlier release at all: not_found with no candidates.
	f.service(svcOpts{env: f.staging, slug: "solo"})
	_, err = f.call(f.admin, "rollback_service", map[string]any{"service": f.selector(f.staging, "solo")})
	require.Equal(t, "not_found", errCode(err))
}

func TestRollbackServiceImagePruned(t *testing.T) {
	f := newFx(t)
	r := rollbackSeed(f, f.staging, "web")
	var oldRel uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT release_id FROM deployments WHERE id = $1", r.old.ID).Scan(&oldRel))
	sha := "fedcba9876543210fedcba9876543210fedcba98"
	// A git release built on the target without a retained image id.
	_, err := f.pool.Exec(context.Background(), `UPDATE releases SET source = jsonb_build_object('type', 'git', 'commitSha', $2::text, 'gitUrl', 'https://example.com/a.git') WHERE id = $1`, oldRel, sha)
	require.NoError(t, err)

	_, err = f.call(f.admin, "rollback_service", map[string]any{"service": f.selector(f.staging, "web")})
	require.Equal(t, "image_pruned", errCode(err))
	require.Contains(t, err.Error(), "rebuild")
	require.Equal(t, 2, chgDeployments(f, r.sv.ID), "a pruned image must not create a deployment")
}

func TestRollbackServiceWait(t *testing.T) {
	f := newFx(t)
	r := rollbackSeed(f, f.staging, "web")
	applyFinishLater(f, r.sv.ID, "rollback", "failed", "container exited", 200*time.Millisecond, "starting old release")

	m := f.must("rollback_service", map[string]any{"service": f.selector(f.staging, "web"), "waitSeconds": 10})
	d := m["deployment"].(map[string]any)
	require.Equal(t, "failed", d["outcome"], chgJSON(t, m))
	require.Contains(t, d["statusMessage"].(map[string]any)["text"], "container exited")
	require.Equal(t, "pull", d["failedStep"])
	require.Contains(t, d["logTail"].(map[string]any)["text"], "starting old release")
	require.Equal(t, "explain_failed_deployment", m["nextTools"].([]any)[0].(map[string]any)["tool"])

	_, err := f.call(f.admin, "rollback_service", map[string]any{"service": f.selector(f.staging, "web"), "waitSeconds": 99})
	require.Equal(t, "invalid_argument", errCode(err))
}

func TestRollbackServiceProductionSurfacesApproval(t *testing.T) {
	f := newFx(t)
	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: f.box})
	g.Install()
	t.Cleanup(prodguardtest.Uninstall)
	r := rollbackSeed(f, f.prod, "api")

	// The preview is free; the rollback itself needs a human.
	m := f.must("rollback_service", map[string]any{"service": f.selector(f.prod, "api"), "dryRun": true})
	require.Equal(t, true, m["production"])

	_, err := f.call(f.admin, "rollback_service", map[string]any{"service": f.selector(f.prod, "api")})
	require.Error(t, err)
	_, ok := prodguard.AsRequired(err)
	require.True(t, ok, "the approval request must be returned unchanged, got %T: %v", err, err)
	require.Equal(t, 2, chgDeployments(f, r.sv.ID))
}

func TestRollbackServiceArguments(t *testing.T) {
	f := newFx(t)
	rollbackSeed(f, f.staging, "web")
	rollbackSeed(f, f.prod, "web")
	_, err := f.call(f.admin, "rollback_service", map[string]any{"service": "web"})
	require.Equal(t, "ambiguous_selector", errCode(err))
	_, err = f.call(f.admin, "rollback_service", map[string]any{})
	require.Equal(t, "invalid_argument", errCode(err))
	_, err = f.call(f.admin, "rollback_service", map[string]any{"service": f.selector(f.staging, "web"), "detail": "x"})
	require.Equal(t, "invalid_argument", errCode(err))
	_, err = f.call(f.admin, "rollback_service", map[string]any{"service": f.selector(f.staging, "web"), "confirm": true})
	require.Equal(t, "invalid_argument", errCode(err), "a model-supplied confirm flag must not exist")
}

func TestRollbackServicePreviewVariableKeysOnly(t *testing.T) {
	f := newFx(t)
	r := rollbackSeed(f, f.staging, "web")
	seal := func(dep db.Deployment, raw map[string]services.RawVar) {
		var rel uuid.UUID
		require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT release_id FROM deployments WHERE id = $1", dep.ID).Scan(&rel))
		plain, err := json.Marshal(services.EnvSnapshot{Vars: map[string]string{}, Raw: raw})
		require.NoError(t, err)
		ct, err := f.box.Seal(secretbox.ReleasesEnv, rel.String(), plain)
		require.NoError(t, err)
		_, err = f.pool.Exec(context.Background(), "UPDATE releases SET env_enc = $2 WHERE id = $1", rel, ct)
		require.NoError(t, err)
	}
	seal(r.old, map[string]services.RawVar{
		"KEEP": {Value: "same"}, "CHANGED": {Value: "old-value-1", Secret: true}, "ONLY_OLD": {Value: chgCanary, Secret: true},
	})
	seal(r.cur, map[string]services.RawVar{
		"KEEP": {Value: "same"}, "CHANGED": {Value: "new-value-2", Secret: true}, "ONLY_NEW": {Value: "plain-value-777"},
	})

	m := f.must("rollback_service", map[string]any{"service": f.selector(f.staging, "web"), "dryRun": true})
	pv := m["preview"].(map[string]any)
	require.Contains(t, pv["variablesSnapshot"], "kept: 3 variables")
	require.Equal(t, []any{"ONLY_OLD"}, pv["variablesAdded"], "the target release has it, the running one lacks it")
	require.Equal(t, []any{"ONLY_NEW"}, pv["variablesRemoved"])
	require.Equal(t, []any{"CHANGED"}, pv["variablesChanged"])
	js := chgJSON(t, m)
	for _, secret := range []string{chgCanary, "plain-value-777", "old-value-1", "new-value-2"} {
		require.NotContains(t, js, secret)
	}
}
