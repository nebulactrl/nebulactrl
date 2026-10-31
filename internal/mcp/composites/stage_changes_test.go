package composites_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

func stageSteps(m map[string]any) map[string]string {
	out := map[string]string{}
	for _, s := range m["steps"].([]any) {
		sm := s.(map[string]any)
		out[sm["step"].(string)] = sm["status"].(string)
	}
	return out
}

func TestStageChangesHappyPath(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "web"})
	before := chgReload(f, sv.ID)

	m := f.must("stage_changes", map[string]any{
		"service": f.selector(f.staging, "web"),
		"ifMatch": before.Generation,
		"config": map[string]any{
			"port":        8080,
			"healthcheck": map[string]any{"path": "/healthz"},
		},
		"variables": []any{
			map[string]any{"key": "API_TOKEN", "value": chgCanary, "secret": true},
			map[string]any{"key": "SESSION_KEY", "generate": map[string]any{"length": 40, "charset": "hex"}},
			map[string]any{"key": "LOG_LEVEL", "value": "plain-value-777"},
		},
		"domains": map[string]any{"add": []any{map[string]any{"hostname": "shop.example.org"}}},
		"volumes": map[string]any{"add": []any{map[string]any{"name": "data", "mountPath": "/data"}}},
	})
	require.Equal(t, true, m["staged"], chgJSON(t, m))
	require.Empty(t, m["failed"])
	require.Empty(t, m["errors"])
	steps := stageSteps(m)
	require.Equal(t, "staged", steps["config"])
	require.Equal(t, "staged", steps["variables.set"])
	require.Equal(t, "staged", steps["domains.add:shop.example.org"])
	require.Equal(t, "staged", steps["volumes.add:data"])

	// The generation went up; nothing was applied or deployed.
	after := chgReload(f, sv.ID)
	require.Greater(t, after.Generation, before.Generation)
	require.Equal(t, before.AppliedGeneration, after.AppliedGeneration)
	require.Equal(t, float64(after.Generation), m["generation"])
	require.Equal(t, float64(after.AppliedGeneration), m["appliedGeneration"])
	require.Empty(t, f.ag.commands("deploy"))
	require.Equal(t, 1, chgDeployments(f, sv.ID), "stage_changes must never deploy")
	require.Equal(t, 8080, services.DecodeConfig(after.Config).Port)
	require.Equal(t, "/healthz", services.DecodeConfig(after.Config).Healthcheck.Path)

	// Touched paths are reported, secrets masked, values never shown.
	fields := chgFields(m)
	require.Contains(t, fields, "config.port")
	require.Equal(t, "80", fields["config.port"]["from"])
	require.Equal(t, "8080", fields["config.port"]["to"])
	require.Contains(t, fields, "variables.API_TOKEN")
	require.Contains(t, fields, "variables.SESSION_KEY")
	require.Contains(t, fields, "domains.shop.example.org")
	require.Contains(t, fields, "volumes.data")
	require.GreaterOrEqual(t, m["pendingCount"], float64(len(fields)))
	js := chgJSON(t, m)
	require.NotContains(t, js, chgCanary)
	require.NotContains(t, js, "plain-value-777")

	// Stored: the user's secret is secret, the generated one exists and is secret.
	list, err := f.deps.Variables.List(f.ctx(f.admin), sv.ID)
	require.NoError(t, err)
	got := map[string]bool{}
	for _, v := range list.Items {
		got[v.Key] = v.IsSecret
	}
	require.True(t, got["API_TOKEN"])
	require.True(t, got["SESSION_KEY"])
	require.False(t, got["LOG_LEVEL"])

	// The next step: review, then apply with the generation returned here.
	require.Contains(t, m["next"], "review_changes")
	nts := m["nextTools"].([]any)
	require.Equal(t, "review_changes", nts[0].(map[string]any)["tool"])
	apply := nts[len(nts)-1].(map[string]any)
	require.Equal(t, "apply_changes", apply["tool"])
	require.Equal(t, float64(after.Generation), apply["args"].(map[string]any)["services"].([]any)[0].(map[string]any)["expectedGeneration"])

	// Repeating the same call is safe: nothing moves.
	m2 := f.must("stage_changes", map[string]any{
		"service": f.selector(f.staging, "web"),
		"config":  map[string]any{"port": 8080},
		"variables": []any{
			map[string]any{"key": "API_TOKEN", "value": chgCanary, "secret": true},
			map[string]any{"key": "SESSION_KEY", "generate": map[string]any{"length": 40, "charset": "hex"}},
		},
		"domains": map[string]any{"add": []any{map[string]any{"hostname": "shop.example.org"}}},
		"volumes": map[string]any{"add": []any{map[string]any{"name": "data", "mountPath": "/data"}}},
	})
	require.Equal(t, true, m2["staged"])
	require.Equal(t, m["generation"], m2["generation"], "an identical re-run must not bump the generation")
	steps2 := stageSteps(m2)
	require.Equal(t, "unchanged", steps2["config"])
	require.Equal(t, "unchanged", steps2["domains.add:shop.example.org"])
	require.Equal(t, "unchanged", steps2["volumes.add:data"])
}

func TestStageChangesDeleteAndRemove(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "web"})
	chgPut(f, sv.ID, "OLD", "x", false)
	_, err := f.deps.Services.AddVolume(f.ctx(f.admin), sv.ID, "cache", "/cache")
	require.NoError(t, err)
	_, err = f.deps.Domains.Create(f.ctx(f.admin), sv.ID, "old.example.org", 0, domains.RuleInput{})
	require.NoError(t, err)

	m := f.must("stage_changes", map[string]any{
		"service":   f.selector(f.staging, "web"),
		"variables": []any{map[string]any{"key": "OLD", "delete": true}, map[string]any{"key": "NEVER_SET", "delete": true}},
		"volumes":   map[string]any{"remove": []any{"cache", "ghost"}},
		"domains":   map[string]any{"remove": []any{"old.example.org"}},
	})
	require.Equal(t, true, m["staged"], chgJSON(t, m))
	steps := stageSteps(m)
	require.Equal(t, "staged", steps["variables.delete:OLD"])
	require.Equal(t, "unchanged", steps["variables.delete:NEVER_SET"])
	require.Equal(t, "staged", steps["volumes.remove:cache"])
	require.Equal(t, "unchanged", steps["volumes.remove:ghost"])
	require.Equal(t, "staged", steps["domains.remove:old.example.org"])
	vols, err := f.deps.Services.ListVolumes(f.ctx(f.admin), sv.ID)
	require.NoError(t, err)
	require.Empty(t, vols)
	doms, err := f.deps.Domains.List(f.ctx(f.admin), sv.ID)
	require.NoError(t, err)
	require.Empty(t, doms)
}

func TestStageChangesIfMatchConflict(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "web"})
	cur := chgReload(f, sv.ID)

	_, err := f.call(f.admin, "stage_changes", map[string]any{
		"service": f.selector(f.staging, "web"), "ifMatch": cur.Generation + 5,
		"config": map[string]any{"port": 9090}, "variables": []any{map[string]any{"key": "A", "value": chgCanary, "secret": true}},
	})
	require.Equal(t, "generation_conflict", errCode(err))
	require.NotContains(t, err.Error(), chgCanary)
	after := chgReload(f, sv.ID)
	require.Equal(t, cur.Generation, after.Generation, "a conflict must stage nothing")
	require.Equal(t, 80, services.DecodeConfig(after.Config).Port)

	// The right generation passes.
	_, err = f.call(f.admin, "stage_changes", map[string]any{
		"service": f.selector(f.staging, "web"), "ifMatch": cur.Generation, "config": map[string]any{"port": 9090},
	})
	require.NoError(t, err)
}

func TestStageChangesValidatesBeforeWriting(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "web"})
	cur := chgReload(f, sv.ID)
	sel := f.selector(f.staging, "web")
	good := map[string]any{"port": 9090}

	cases := map[string]map[string]any{
		"duplicate keys":     {"variables": []any{map[string]any{"key": "A", "value": "1"}, map[string]any{"key": "A", "value": "2"}}},
		"bad key":            {"variables": []any{map[string]any{"key": "1BAD", "value": "1"}}},
		"reserved key":       {"variables": []any{map[string]any{"key": "NEBULA_X", "value": "1"}}},
		"value and generate": {"variables": []any{map[string]any{"key": "A", "value": "1", "generate": map[string]any{}}}},
		"delete with value":  {"variables": []any{map[string]any{"key": "A", "value": "1", "delete": true}}},
		"bad charset":        {"variables": []any{map[string]any{"key": "A", "generate": map[string]any{"charset": "emoji"}}}},
		"bad length":         {"variables": []any{map[string]any{"key": "A", "generate": map[string]any{"length": 5000}}}},
		"bad template":       {"variables": []any{map[string]any{"key": "A", "value": "${{", "secret": true}}},
		"empty variable":     {"variables": []any{map[string]any{"key": "A"}}},
		"bad hostname":       {"domains": map[string]any{"add": []any{map[string]any{"hostname": "not a host"}}}},
		"add and remove":     {"domains": map[string]any{"add": []any{map[string]any{"hostname": "a.example.org"}}, "remove": []any{"a.example.org"}}},
		"bad volume name":    {"volumes": map[string]any{"add": []any{map[string]any{"name": "Bad Name", "mountPath": "/data"}}}},
		"bad mount path":     {"volumes": map[string]any{"add": []any{map[string]any{"name": "data", "mountPath": "/"}}}},
		"invalid config":     {"config": map[string]any{"port": 99999}},
		"unknown config key": {"config": map[string]any{"nonsense": true}},
		"unknown field":      {"nonsense": true},
		"nothing":            {},
	}
	for name, extra := range cases {
		args := map[string]any{"service": sel, "config": good}
		if name == "nothing" || name == "invalid config" || name == "unknown config key" {
			delete(args, "config")
		}
		for k, v := range extra {
			args[k] = v
		}
		_, err := f.call(f.admin, "stage_changes", args)
		require.Error(t, err, name)
		code := errCode(err)
		require.NotEmpty(t, code, "%s: %v", name, err)
		after := chgReload(f, sv.ID)
		require.Equal(t, cur.Generation, after.Generation, "%s: the valid config part must not be staged when another part is invalid", name)
		require.Equal(t, 80, services.DecodeConfig(after.Config).Port, name)
	}

	_, err := f.call(f.admin, "stage_changes", map[string]any{"service": sel, "detail": "huge", "config": good})
	require.Equal(t, "invalid_argument", errCode(err))
}

func TestStageChangesPartialFailureIsReported(t *testing.T) {
	f := newFx(t)
	other := f.service(svcOpts{env: f.staging, slug: "other"})
	_, err := f.deps.Domains.Create(f.ctx(f.admin), other.ID, "taken.example.org", 0, domains.RuleInput{})
	require.NoError(t, err)
	web := f.service(svcOpts{env: f.staging, slug: "web"})
	before := chgReload(f, web.ID)

	// The hostname is free as far as this service knows; the conflict with
	// "other" surfaces when it is written, after the config was staged.
	m := f.must("stage_changes", map[string]any{
		"service":   f.selector(f.staging, "web"),
		"config":    map[string]any{"port": 9090},
		"variables": []any{map[string]any{"key": "A_KEY", "value": chgCanary, "secret": true}},
		"domains":   map[string]any{"add": []any{map[string]any{"hostname": "taken.example.org"}}},
	})
	require.Equal(t, false, m["staged"], chgJSON(t, m))
	steps := stageSteps(m)
	require.Equal(t, "staged", steps["config"])
	require.Equal(t, "staged", steps["variables.set"])
	require.Equal(t, "failed", steps["domains.add:taken.example.org"])
	failed := m["failed"].([]any)
	require.Len(t, failed, 1)
	require.Equal(t, "domain_conflict", failed[0].(map[string]any)["code"])
	require.Equal(t, []string{"domains.add:taken.example.org"}, sectionErrors(m))
	require.Contains(t, m["summary"], "Partially staged")
	require.NotContains(t, chgJSON(t, m), chgCanary)
	// No pretend rollback: what was staged stays staged.
	after := chgReload(f, web.ID)
	require.Greater(t, after.Generation, before.Generation)
	require.Equal(t, 9090, services.DecodeConfig(after.Config).Port)
	require.Empty(t, f.ag.commands("deploy"))
}

func TestStageChangesFirstWriteFailureIsAToolError(t *testing.T) {
	f := newFx(t)
	other := f.service(svcOpts{env: f.staging, slug: "other"})
	_, err := f.deps.Domains.Create(f.ctx(f.admin), other.ID, "taken.example.org", 0, domains.RuleInput{})
	require.NoError(t, err)
	web := f.service(svcOpts{env: f.staging, slug: "web"})
	before := chgReload(f, web.ID)

	_, err = f.call(f.admin, "stage_changes", map[string]any{
		"service": f.selector(f.staging, "web"),
		"domains": map[string]any{"add": []any{map[string]any{"hostname": "taken.example.org"}}},
	})
	require.Equal(t, "domain_conflict", errCode(err))
	require.Equal(t, before.Generation, chgReload(f, web.ID).Generation)
}

func TestStageChangesSelectorErrors(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})
	f.service(svcOpts{env: f.prod, slug: "web"})
	_, err := f.call(f.admin, "stage_changes", map[string]any{"service": "web", "config": map[string]any{"port": 81}})
	require.Equal(t, "ambiguous_selector", errCode(err))
	_, err = f.call(f.admin, "stage_changes", map[string]any{"service": "shop/staging/nope", "config": map[string]any{"port": 81}})
	require.Equal(t, "not_found", errCode(err))
	_, err = f.call(f.admin, "stage_changes", map[string]any{"config": map[string]any{"port": 81}})
	require.Equal(t, "invalid_argument", errCode(err))
}

func TestStageChangesDoesNotLeakIntoAudit(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})
	f.must("stage_changes", map[string]any{
		"service":   f.selector(f.staging, "web"),
		"variables": []any{map[string]any{"key": "API_TOKEN", "value": chgCanary, "secret": true}},
	})
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM audit_events WHERE metadata::text LIKE '%' || $1 || '%'", chgCanary).Scan(&n))
	require.Zero(t, n, "the secret value must not reach the audit log")
}
