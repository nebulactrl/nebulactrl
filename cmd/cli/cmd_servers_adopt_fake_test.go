package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	adoptEnvID  = "e1e1e1e1-0000-4000-8000-000000000001"
	adoptAppID  = "a1a1a1a1-0000-4000-8000-000000000001"
	adoptDBID   = "d1d1d1d1-0000-4000-8000-000000000001"
	adoptPlan   = cfgAPI + "/servers/" + dbSrvID + "/adoption/plan"
	adoptApply  = cfgAPI + "/servers/" + dbSrvID + "/adoption"
	adoptProjs  = cfgAPI + "/projects"
	adoptSrvRef = "web-1"
)

func adoptPlanBody(blockers []string, dbSlug string) map[string]any {
	svc := func(id, slug, kind, action string, adoptable bool, extra map[string]any) map[string]any {
		m := map[string]any{
			"id": id, "slug": slug, "name": slug, "kind": kind, "sourceType": "image", "action": action, "adoptable": adoptable,
			"replicas": 1, "processes": []any{}, "containers": []any{}, "volumes": []any{}, "domains": []any{}, "env": []any{}, "notes": []any{},
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	return map[string]any{
		"serverId": dbSrvID, "held": true, "warnings": []any{}, "blockers": blockers,
		"totals": map[string]any{"adopt": 2, "leave": 0, "remove": 0, "containers": 3},
		"environments": []any{map[string]any{
			"id": adoptEnvID, "action": "create", "projectAction": "create", "projectName": "Adopted from web-1",
			"name": "production", "slug": "production", "production": true,
			"services": []any{
				svc(adoptAppID, "app", "app", "adopt", true, map[string]any{
					"image": "nginx:1.27", "replicas": 2,
					"domains": []any{map[string]any{"host": "shop.example.com", "include": true}, map[string]any{"host": "admin.example.com", "include": false, "note": "access rules cannot be read back"}},
					"env":     []any{map[string]any{"key": "API_KEY", "secret": true, "secretReason": "name"}, map[string]any{"key": "MODE", "secret": false}},
				}),
				svc(adoptDBID, dbSlug, "database", "adopt", true, map[string]any{"database": map[string]any{"engine": "postgres", "version": "17"}}),
			},
		}},
	}
}

func adoptFake(t *testing.T) *cfgFake {
	f := newCfgFake(t)
	f.reply("GET", srvList, 200, map[string]any{"items": []any{srvView(adoptSrvRef, dbSrvID, nil)}})
	f.reply("GET", adoptProjs, 200, map[string]any{"items": []any{}})
	return f
}

func TestServersAdoptPlanListsServicesAndExitsOneOnBlockers(t *testing.T) {
	f := adoptFake(t)
	f.reply("POST", adoptPlan, 200, adoptPlanBody([]string{}, "postgres"))
	r := f.run("servers", "adopt", adoptSrvRef, "--plan")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, []string{"app", "a1a1a1a1", "app", "adopt", "nginx:1.27", "2", "shop.example.com", "2", "(1", "secret)"}, bkLine(t, r.stdout, "a1a1a1a1"))
	require.Contains(t, r.stdout, "app variables (* secret): API_KEY* MODE")
	require.Contains(t, r.stdout, "domain admin.example.com left out: access rules cannot be read back")
	require.Empty(t, f.calls("POST", adoptApply), "--plan never adopts")

	f.reply("POST", adoptPlan, 200, adoptPlanBody([]string{"app: belongs to another team"}, "postgres"))
	r = f.run("servers", "adopt", adoptSrvRef, "--plan")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.stdout, "[blocked] app: belongs to another team")

	r = f.run("servers", "adopt", adoptSrvRef)
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "fix the blockers above")
	require.Empty(t, f.calls("POST", adoptApply), "a blocked plan is never applied")
}

func TestServersAdoptSendsTheResolvedChoices(t *testing.T) {
	f := adoptFake(t)
	f.reply("POST", adoptPlan, 200, adoptPlanBody([]string{}, "postgres"))
	f.reply("POST", adoptApply, 201, map[string]any{
		"projects": []any{map[string]any{"id": "p1", "name": "Shop", "slug": "shop"}},
		"services": []any{map[string]any{"id": adoptAppID, "name": "Website", "slug": "app", "environmentId": adoptEnvID, "envSlug": "production", "projectId": "p1", "projectSlug": "shop"}},
		"left":     1, "removed": 0, "reattached": 0, "warnings": []any{},
	})
	r := f.run("servers", "adopt", adoptSrvRef,
		"--leave", "d1d1d1d1", "--name", "app=Website", "--secret", "app.MODE", "--not-secret", "app.API_KEY",
		"--skip-domain", "app/shop.example.com", "--project", "e1e1e1e1=Shop", "--not-production", adoptEnvID)
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "Adopted 1 service(s) into 1 new project(s):")
	require.Contains(t, r.stdout, "shop/production/app")
	require.Contains(t, r.stdout, "1 workload(s) keep running unmanaged on web-1.")

	req := f.only("POST", adoptApply)
	require.NotEmpty(t, req.header.Get("Idempotency-Key"))
	ch := req.json(t)["choices"].(map[string]any)
	require.Equal(t, map[string]any{"projectName": "Shop", "production": false}, ch["environments"].(map[string]any)[adoptEnvID])
	svcs := ch["services"].(map[string]any)
	require.Equal(t, map[string]any{"action": "leave"}, svcs[adoptDBID])
	require.Equal(t, map[string]any{
		"name": "Website", "secret": map[string]any{"MODE": true, "API_KEY": false}, "skipHosts": []any{"shop.example.com"},
	}, svcs[adoptAppID])
	require.Len(t, f.calls("POST", adoptPlan), 2, "the choices are planned once more before the adoption")
}

func TestServersAdoptRejectsAmbiguousAndUnknownReferences(t *testing.T) {
	f := adoptFake(t)
	f.reply("POST", adoptPlan, 200, adoptPlanBody([]string{}, "app"))
	r := f.run("servers", "adopt", adoptSrvRef, "--leave", "app")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, `service "app" is ambiguous`)
	require.Contains(t, r.err, adoptAppID)
	require.Contains(t, r.err, adoptDBID)

	r = f.run("servers", "adopt", adoptSrvRef, "--remove", "nope")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, `no service "nope" on this server`)
	require.Empty(t, f.calls("POST", adoptApply))
}
