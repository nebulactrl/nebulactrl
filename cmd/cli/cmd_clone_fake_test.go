package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	clSvc  = cfgAPI + "/services/" + fakeSvcID
	clEnv  = cfgAPI + "/environments/" + cfgProdID
	clMove = clSvc + "/move"
)

func clResult(extra map[string]any) map[string]any {
	m := map[string]any{"dryRun": false, "services": []any{
		map[string]any{"name": "web copy", "slug": "web-copy", "kind": "image"},
		map[string]any{"name": "db copy", "slug": "db-copy", "kind": "database", "fresh": true},
	}}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestServicesCloneSendsTheTargetAndPrintsTheStagedClone(t *testing.T) {
	f := newCfgFake(t)
	dbServers(f)
	f.reply("GET", clSvc, 200, map[string]any{"id": fakeSvcID, "projectId": cfgProjectID})
	f.reply("POST", clSvc+"/clone", 200, clResult(map[string]any{
		"skipped":         []any{map[string]any{"slug": "cache", "reason": "ephemeral"}},
		"domains":         []any{map[string]any{"service": "web", "hostname": "shop.example.com", "action": "cloned", "to": "shop-staging.example.com"}, map[string]any{"service": "web", "hostname": "x.example.com", "action": "dropped"}, map[string]any{"service": "web", "hostname": "y.example.com", "action": "skipped", "to": "y", "reason": "taken"}},
		"references":      []any{map[string]any{"service": "web", "key": "DB", "ref": "pg.URL", "message": "no service pg here"}},
		"sharedVariables": 2, "links": 1, "warnings": []string{"volumes are empty"},
	}))

	r := f.run("--idempotency-key", "clone-key", "services", "clone", fakeSvcID, "--env", "staging", "--name", "Web Staging", "--server", "edge-1",
		"--domains", "suffix", "--domain-suffix", "-stg")
	require.Equal(t, 0, r.code, r.all())
	post := f.only("POST", clSvc+"/clone")
	require.Equal(t, map[string]any{"name": "Web Staging", "environmentId": cfgStagingID, "serverId": dbSrvID, "domains": "suffix", "domainSuffix": "-stg"}, post.json(t))
	require.Equal(t, "clone-key", post.header.Get("Idempotency-Key"))
	require.Equal(t, []string{"web", "copy", "web-copy", "image"}, bkLine(t, r.stdout, "web-copy"))
	require.Equal(t, []string{"db", "copy", "db-copy", "database", "new,", "empty", "database"}, bkLine(t, r.stdout, "db-copy"))
	require.Contains(t, r.stdout, "skipped cache: ephemeral\n")
	require.Equal(t, []string{"web", "shop.example.com", "cloned", "shop-staging.example.com"}, bkLine(t, r.stdout, "shop.example.com"))
	require.Equal(t, []string{"web", "x.example.com", "dropped", "-"}, bkLine(t, r.stdout, "x.example.com"))
	require.Equal(t, []string{"web", "y.example.com", "skipped", "y", "taken"}, bkLine(t, r.stdout, "y.example.com"))
	require.Contains(t, r.stdout, "References that do not resolve in the target environment:\n  web DB = ${{pg.URL}}: no service pg here\n")
	require.Contains(t, r.stdout, "2 shared variable(s) copied.\n1 link(s) copied.\nnote: volumes are empty\nStaged: deploy the clone to start it.\n")
	require.NotContains(t, r.stdout, "the new environment")
}

func TestServicesCloneDefaultsAndDryRun(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", clSvc+"/clone", 200, clResult(map[string]any{"dryRun": true}))

	r := f.run("services", "clone", fakeSvcID, "--dry-run")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"domains": "drop", "dryRun": true}, f.only("POST", clSvc+"/clone").json(t), "nothing but the defaults and the dry run")
	require.NotContains(t, r.stdout, "Staged:", "a dry run stages nothing")
	require.Contains(t, r.stdout, "SERVICE")

	r = f.run("--json", "services", "clone", fakeSvcID)
	require.Equal(t, true, decode[map[string]any](t, r.stdout)["dryRun"])
}

func TestServicesCloneChecksFlagsAndResolvesProjectOfTheTargetEnv(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", clSvc+"/clone", 200, clResult(nil))

	before := f.total()
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"bad domains":     {[]string{"--domains", "keep"}, "--domains must be drop or suffix"},
		"suffix w/o mode": {[]string{"--domain-suffix", "-x"}, "--domain-suffix only applies with --domains suffix"},
		"project w/o env": {[]string{"--project", "acme-shop"}, "--project needs --env"},
	} {
		r := f.run(append([]string{"services", "clone", fakeSvcID}, tc.args...)...)
		require.Equal(t, 2, r.code, "%s: %s", name, r.all())
		require.Contains(t, r.err, tc.want, name)
	}
	require.Equal(t, before, f.total())

	r := f.run("services", "clone", fakeSvcID, "--project", "acme-shop", "--env", "production")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, cfgProdID, f.only("POST", clSvc+"/clone").json(t)["environmentId"])
	require.Empty(t, f.calls("GET", clSvc), "with --project the service is not looked up")

	r = f.run("services", "clone", fakeSvcID, "--project", "ghost", "--env", "production")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `project "ghost" not found`)

	f.problem("GET", clSvc, 404, "not_found", "service gone")
	r = f.run("services", "clone", fakeSvcID, "--env", "staging")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "service gone (not_found, HTTP 404)")

	f.reply("GET", clSvc, 200, map[string]any{"id": fakeSvcID, "projectId": cfgProjectID})
	r = f.run("services", "clone", fakeSvcID, "--env", "nope")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `environment "nope" not found`)
	dbServers(f)
	r = f.run("services", "clone", fakeSvcID, "--server", "ghost")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `server "ghost" not found`)

	f.problem("POST", clSvc+"/clone", 409, "name_taken", "a service with that slug exists")
	r = f.run("services", "clone", fakeSvcID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "a service with that slug exists (name_taken, HTTP 409)")
}

func TestEnvsCloneCreatesANonProductionEnvironment(t *testing.T) {
	f := newCfgFake(t)
	dbServers(f)
	f.reply("POST", clEnv+"/clone", 200, clResult(map[string]any{"environment": map[string]any{"name": "Staging 2", "slug": "staging-2"}}))

	r := f.run("envs", "clone", "Production", "--project", "acme-shop", "--name", "Staging 2", "--server", "edge-1", "--domains", "suffix", "--dry-run")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"name": "Staging 2", "serverId": dbSrvID, "domains": "suffix", "dryRun": true}, f.only("POST", clEnv+"/clone").json(t))
	require.Contains(t, r.stdout, "Cloned into the new environment Staging 2 (staging-2), not production.\n")

	f.reply("POST", clEnv+"/clone", 200, clResult(map[string]any{"dryRun": true, "environment": map[string]any{"name": "Staging 2", "slug": "staging-2"}}))
	r = f.run("envs", "clone", "production", "--project", "acme-shop", "--name", "Staging 2")
	require.Contains(t, r.stdout, "Would clone into the new environment Staging 2")

	r = f.run("--json", "envs", "clone", cfgProdID, "--project", cfgProjectID, "--name", "X")
	require.Equal(t, "staging-2", decode[map[string]any](t, r.stdout)["environment"].(map[string]any)["slug"])

	before := f.total()
	r = f.run("envs", "clone", "production", "--project", "acme-shop")
	require.Equal(t, 2, r.code)
	require.Contains(t, r.err, "--project and --name are required")
	r = f.run("envs", "clone", "production", "--name", "X")
	require.Equal(t, 2, r.code)
	r = f.run("envs", "clone", "production", "--project", "acme-shop", "--name", "X", "--domains", "keep")
	require.Equal(t, 2, r.code)
	require.Equal(t, before, f.total())

	r = f.run("envs", "clone", "nope", "--project", "acme-shop", "--name", "X")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `environment "nope" not found`)
	r = f.run("envs", "clone", "production", "--project", "acme-shop", "--name", "X", "--server", "ghost")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `server "ghost" not found`)

	f.problem("POST", clEnv+"/clone", 422, "production_target", "the clone cannot be production")
	r = f.run("envs", "clone", "production", "--project", "acme-shop", "--name", "X")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the clone cannot be production (production_target, HTTP 422)")
}

func TestServicesMovePrintsTheOutcomeOfThePlan(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", clSvc, 200, map[string]any{"id": fakeSvcID, "projectId": cfgProjectID})
	f.reply("POST", clMove, 200, map[string]any{
		"applied": true, "to": map[string]any{"name": "Staging", "slug": "staging"}, "slug": "web-2", "slugChanged": true,
		"outgoing": []any{map[string]any{"service": "web", "key": "DB", "ref": "pg.URL", "message": "pg stays behind", "status": "broken"}},
		"incoming": []any{map[string]any{"service": "worker", "keys": []string{"API_URL", "API_KEY"}}},
		"links":    []string{"web-pg"}, "warnings": []string{"redeploy to apply"},
	})

	r := f.run("services", "move", fakeSvcID, "--env", "staging", "--force")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"environmentId": cfgStagingID, "force": true}, f.only("POST", clMove).json(t))
	require.Equal(t, "Moved to Staging / staging as web-2 (staged: deploy to apply).\n"+
		"The slug becomes web-2.\n"+
		"  web DB = ${{pg.URL}}: pg stays behind (broken)\n"+
		"  worker references the service in API_URL, API_KEY\n"+
		"  links removed: web-pg\n"+
		"note: redeploy to apply\n", r.stdout)

	f.reply("POST", clMove, 200, map[string]any{"applied": false, "blocked": true})
	r = f.run("services", "move", fakeSvcID, "--env", "staging", "--dry-run", "--project", "acme-shop")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Blocked: the move breaks references (use --force to move anyway).\n", r.stdout)
	require.Equal(t, map[string]any{"environmentId": cfgStagingID, "dryRun": true}, f.calls("POST", clMove)[1].json(t))

	f.reply("POST", clMove, 200, map[string]any{})
	r = f.run("services", "move", fakeSvcID, "--env", "staging")
	require.Equal(t, "The move is clean.\n", r.stdout)

	r = f.run("--json", "services", "move", fakeSvcID, "--env", "staging")
	require.Equal(t, 0, r.code, r.all())
}

func TestServicesMoveExplainsHowToGetPastABlockedMove(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", clSvc, 200, map[string]any{"id": fakeSvcID, "projectId": cfgProjectID})
	f.problem("POST", clMove, 409, "move_blocked", "the move breaks 2 references")

	r := f.run("services", "move", fakeSvcID, "--env", "staging")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the move breaks 2 references (move_blocked, HTTP 409)\nrun with --dry-run to see the references and links, or --force to move anyway")

	f.problem("POST", clMove, 403, "forbidden", "needs services:write")
	r = f.run("services", "move", fakeSvcID, "--env", "staging")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "needs services:write (forbidden, HTTP 403)")
	require.NotContains(t, r.err, "--force", "only a blocked move hints at --force")

	before := f.total()
	r = f.run("services", "move", fakeSvcID)
	require.Equal(t, 2, r.code)
	require.Contains(t, r.err, "--env is required")
	require.Equal(t, before, f.total())

	r = f.run("services", "move", fakeSvcID, "--env", "nope")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `environment "nope" not found`)
}
