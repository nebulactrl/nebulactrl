package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	tpCatalog = cfgAPI + "/catalog/templates"
	tpPlan    = cfgAPI + "/environments/" + cfgProdID + "/templates/umami/plan"
	tpInstall = cfgAPI + "/environments/" + cfgProdID + "/templates/umami/instances"
	tpList    = cfgAPI + "/environments/" + cfgProdID + "/template-instances"
	tpInstID  = "f1f1f1f1-1111-4111-8111-f1f1f1f1f1f1"
	tpInst    = cfgAPI + "/template-instances/" + tpInstID
)

func TestTemplatesLsFiltersAndMarksRestrictiveLicences(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", tpCatalog, 200, map[string]any{"items": []any{
		map[string]any{"id": "umami", "category": "analytics", "services": 2, "license": "MIT", "licenseClass": "permissive", "summary": "Web analytics"},
		map[string]any{"id": "n8n", "category": "automation", "services": 3, "license": "SUL-1.0", "licenseClass": "source-available", "summary": "Workflows"},
		map[string]any{"id": "plex", "category": "media", "services": 1, "license": "EULA", "licenseClass": "proprietary", "summary": "Media server"},
	}})

	r := f.run("templates", "ls", "--category", "analytics", "--license", "permissive", "--search", "web stats")
	require.Equal(t, 0, r.code, r.all())
	q := f.only("GET", tpCatalog).values(t)
	require.Equal(t, "analytics", q.Get("category"))
	require.Equal(t, "permissive", q.Get("license"))
	require.Equal(t, "web stats", q.Get("q"))
	require.Equal(t, []string{"umami", "analytics", "2", "MIT", "Web", "analytics"}, bkLine(t, r.stdout, "umami"))
	require.Equal(t, []string{"n8n", "automation", "3", "SUL-1.0", "(source-available)", "Workflows"}, bkLine(t, r.stdout, "n8n"))
	require.Equal(t, []string{"plex", "media", "1", "EULA", "(proprietary)", "Media", "server"}, bkLine(t, r.stdout, "plex"))

	r = f.run("templates", "ls")
	require.Equal(t, 0, r.code, r.all())
	require.Empty(t, f.calls("GET", tpCatalog)[1].query, "no filters, no query")

	r = f.run("--json", "templates", "ls")
	require.Len(t, decode[map[string]any](t, r.stdout)["items"], 3)

	f.reply("GET", tpCatalog, 200, map[string]any{})
	r = f.run("templates", "ls")
	require.Equal(t, 0, r.code)
	require.Contains(t, r.stdout, "SUMMARY", "an empty catalog still prints the header")

	r = f.run("templates", "ls", "extra")
	require.Equal(t, 2, r.code)
}

func TestTemplatesShowListsServicesInputsAndNotes(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", tpCatalog+"/umami", 200, map[string]any{
		"id": "umami", "name": "Umami", "summary": "Web analytics",
		"upstream": map[string]any{"license": "MIT", "class": "permissive"},
		"services": []any{
			map[string]any{"key": "app", "kind": "image", "image": "ghcr.io/umami:latest", "ports": []any{
				map[string]any{"protocol": "tcp", "port": 3000, "publish": true}, map[string]any{"protocol": "tcp", "port": 9000, "publish": false}}},
			map[string]any{"key": "db", "kind": "database", "database": map[string]any{"engine": "postgres", "version": "17"}},
			map[string]any{"key": "migrate", "kind": "image", "image": "ghcr.io/umami-migrate", "oneshot": true},
		},
		"inputs": []any{
			map[string]any{"key": "APP_SECRET", "required": true, "secret": true, "default": "generated", "label": "App secret"},
			map[string]any{"key": "LANG", "required": false, "default": "en", "label": "Language"},
		},
		"notes": []string{"First login is admin/umami", "Change the password"},
	})

	r := f.run("templates", "show", "umami")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "Umami (umami) — Web analytics\nLicence: MIT [permissive]\n")
	require.Equal(t, []string{"app", "image", "ghcr.io/umami:latest", "publishes", "tcp/3000"}, bkLine(t, r.stdout, "ghcr.io/umami:latest"))
	require.Equal(t, []string{"db", "database", "managed", "postgres", "17"}, bkLine(t, r.stdout, "managed postgres"))
	require.Equal(t, []string{"migrate", "image", "ghcr.io/umami-migrate", "one-shot"}, bkLine(t, r.stdout, "one-shot"))
	require.Equal(t, []string{"APP_SECRET", "yes", "(secret)", "App", "secret"}, bkLine(t, r.stdout, "APP_SECRET"), "a secret input never shows its default")
	require.Equal(t, []string{"LANG", "no", "en", "Language"}, bkLine(t, r.stdout, "LANG"))
	require.Contains(t, r.stdout, "Note: First login is admin/umami\nNote: Change the password\n")

	r = f.run("--json", "templates", "show", "umami")
	require.Equal(t, "Umami", decode[map[string]any](t, r.stdout)["name"])

	f.problem("GET", tpCatalog+"/nope", 404, "not_found", "no such template")
	r = f.run("templates", "show", "nope")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "no such template (not_found, HTTP 404)")
}

func TestTemplatesShowWithoutInputsOrNotesPrintsOnlyTheServices(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", tpCatalog+"/bare", 200, map[string]any{"id": "bare", "name": "Bare", "summary": "s", "upstream": map[string]any{"license": "MIT", "class": "permissive"},
		"services": []any{map[string]any{"key": "app", "kind": "image", "image": "x"}}})
	r := f.run("templates", "show", "bare")
	require.Equal(t, 0, r.code, r.all())
	require.NotContains(t, r.stdout, "INPUT")
	require.NotContains(t, r.stdout, "Note:")
}

func TestTemplatesPlanResolvesTheTargetAndExitsNonZeroWhenNotInstallable(t *testing.T) {
	f := newCfgFake(t)
	dbServers(f)
	plan := map[string]any{
		"installable": true,
		"services": []any{
			map[string]any{"slug": "umami", "kind": "image", "image": "ghcr.io/umami:latest", "domain": "umami.example.com"},
			map[string]any{"slug": "umami-db", "kind": "database", "database": "postgres 17"},
		},
		"checks": []any{map[string]any{"status": "ok", "message": "server has capacity"}, map[string]any{"status": "warn", "message": "port 3000 is shared"}},
	}
	f.reply("POST", tpPlan, 200, plan)

	r := f.run("templates", "plan", "umami", "--project", "ACME-shop", "--env", "production", "--server", "edge-1", "--input", "A=1", "--input", "B=x=y")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"serverId": dbSrvID, "inputs": map[string]any{"A": "1", "B": "x=y"}}, f.only("POST", tpPlan).json(t))
	require.Equal(t, []string{"umami", "image", "ghcr.io/umami:latest", "umami.example.com"}, bkLine(t, r.stdout, "umami.example.com"))
	require.Equal(t, []string{"umami-db", "database", "postgres", "17"}, bkLine(t, r.stdout, "umami-db"))
	require.Contains(t, r.stdout, "[ok] server has capacity\n[warn] port 3000 is shared\n")

	plan["installable"] = false
	f.reply("POST", tpPlan, 200, plan)
	r = f.run("templates", "plan", "umami", "--project", "acme-shop", "--env", "production")
	require.Equal(t, 1, r.code, "a plan that cannot be installed fails the command")
	require.Empty(t, r.err, "the exit code is the signal; the checks explain why")
	require.Contains(t, r.stdout, "[warn] port 3000 is shared")
	require.NotContains(t, f.calls("POST", tpPlan)[1].json(t), "serverId")

	r = f.run("--json", "templates", "plan", "umami", "--project", "acme-shop", "--env", "production")
	require.Equal(t, 1, r.code, "--json fails a plan that cannot be installed too: %s", r.all())
	require.Empty(t, r.err)
	require.Equal(t, false, decode[map[string]any](t, r.stdout)["installable"], "the JSON is still printed")

	plan["installable"] = true
	f.reply("POST", tpPlan, 200, plan)
	r = f.run("--json", "templates", "plan", "umami", "--project", "acme-shop", "--env", "production")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, true, decode[map[string]any](t, r.stdout)["installable"])
}

func TestTemplatesTargetFlagsAreValidatedLocally(t *testing.T) {
	f := newCfgFake(t)
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"plan: bad input":    {[]string{"templates", "plan", "umami", "--project", "acme-shop", "--env", "production", "--input", "oops"}, `--input "oops": use KEY=VALUE`},
		"plan: empty key":    {[]string{"templates", "plan", "umami", "--project", "acme-shop", "--env", "production", "--input", "=v"}, "use KEY=VALUE"},
		"plan: no project":   {[]string{"templates", "plan", "umami", "--env", "production"}, "--project and --env are required"},
		"plan: no env":       {[]string{"templates", "plan", "umami", "--project", "acme-shop"}, "--project and --env are required"},
		"install: no server": {[]string{"templates", "install", "umami", "--project", "acme-shop", "--env", "production"}, "--server is required"},
		"installed: no env":  {[]string{"templates", "installed", "--project", "acme-shop"}, "--project and --env are required"},
		"install: bad input": {[]string{"templates", "install", "umami", "--project", "acme-shop", "--env", "production", "--server", "x", "--input", "nope"}, "use KEY=VALUE"},
		"update: bad input":  {[]string{"templates", "update", tpInstID, "--input", "nope"}, "use KEY=VALUE"},
		"rm: no confirm":     {[]string{"templates", "rm", tpInstID}, "--confirm <template-id> is required"},
	} {
		r := f.run(tc.args...)
		require.Equal(t, 2, r.code, "%s: %s", name, r.all())
		require.Contains(t, r.err, tc.want, name)
	}

	r := f.run("templates", "plan", "umami", "--project", "ghost", "--env", "production")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `project "ghost" not found`)
	r = f.run("templates", "plan", "umami", "--project", "acme-shop", "--env", "nope")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `environment "nope" not found`)
	dbServers(f)
	r = f.run("templates", "plan", "umami", "--project", "acme-shop", "--env", "production", "--server", "ghost")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `server "ghost" not found`)
}

func TestTemplatesInstallSendsTheRequestAndExplainsWhatHappensNext(t *testing.T) {
	f := newCfgFake(t)
	dbServers(f)
	f.reply("POST", tpInstall, 201, map[string]any{
		"instance": map[string]any{"id": tpInstID}, "services": []any{map[string]any{"slug": "umami-db"}, map[string]any{"slug": "umami"}},
		"deployments": []any{map[string]any{"id": "d1"}, map[string]any{"id": "d2"}}, "warnings": []string{"port 3000 is shared"},
	})

	r := f.run("--idempotency-key", "tpl-key", "templates", "install", "umami", "--project", "acme-shop", "--env", "production", "--server", "edge-1",
		"--name", "Analytics", "--input", "LANG=de", "--deploy")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Installed umami as "+tpInstID+" (2 services: umami, umami-db)\nStarted 2 deployments; follow them with: nebulactrl deployments status <id>\n", r.stdout)
	require.Equal(t, "Warning: port 3000 is shared\n", r.err)
	post := f.only("POST", tpInstall)
	require.Equal(t, map[string]any{"serverId": dbSrvID, "inputs": map[string]any{"LANG": "de"}, "deploy": true, "name": "Analytics"}, post.json(t))
	require.Equal(t, "tpl-key", post.header.Get("Idempotency-Key"))

	r = f.run("templates", "install", "umami", "--project", "acme-shop", "--env", "production", "--server", dbSrvID)
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "The services are staged; deploy each with `nebulactrl services deploy <service> --wait`.\n")
	require.NotContains(t, r.stdout, "Started")
	require.Equal(t, map[string]any{"serverId": dbSrvID, "inputs": map[string]any{}}, f.calls("POST", tpInstall)[1].json(t))

	r = f.run("--json", "templates", "install", "umami", "--project", "acme-shop", "--env", "production", "--server", "edge-1")
	require.Equal(t, tpInstID, decode[map[string]any](t, r.stdout)["instance"].(map[string]any)["id"])

	f.problem("POST", tpInstall, 409, "name_taken", "a service named umami already exists")
	r = f.run("templates", "install", "umami", "--project", "acme-shop", "--env", "production", "--server", "edge-1")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "a service named umami already exists (name_taken, HTTP 409)")
}

func TestTemplatesInstalledShowsWhichStacksCanBeUpdated(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", tpList, 200, []any{
		map[string]any{"id": tpInstID, "templateId": "umami", "templateVersion": "2.1", "templateRevision": 3, "updateAvailable": true, "currentRevision": 4,
			"services": []any{map[string]any{}, map[string]any{}}, "createdAt": "2026-01-02T03:04:05Z"},
		map[string]any{"id": "inst-2", "templateId": "n8n", "templateVersion": "1.0", "templateRevision": 1, "updateBlocked": "the template added a database",
			"services": []any{map[string]any{}}, "createdAt": "2026-01-03T03:04:05Z"},
		map[string]any{"id": "inst-3", "templateId": "plex", "templateVersion": "9", "templateRevision": 2, "createdAt": "2026-01-04T03:04:05Z"},
	})

	r := f.run("templates", "installed", "--project", "acme-shop", "--env", "production")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, []string{tpInstID, "umami", "2.1", "3", "rev", "4", "available", "2"}, bkLine(t, r.stdout, tpInstID)[:8])
	require.Equal(t, []string{"inst-2", "n8n", "1.0", "1", "newer", "revision", "(reinstall)", "1"}, bkLine(t, r.stdout, "inst-2")[:8])
	require.Equal(t, []string{"inst-3", "plex", "9", "2", "0"}, bkLine(t, r.stdout, "inst-3")[:5], "no update note, no services")

	r = f.run("--json", "templates", "installed", "--project", "acme-shop", "--env", "production")
	require.Len(t, decode[[]any](t, r.stdout), 3)
}

func TestTemplatesUpdateShowsTheDiffWithoutChangingAnything(t *testing.T) {
	f := newCfgFake(t)
	update := map[string]any{
		"templateId": "umami", "installedRevision": 3, "installedVersion": "2.1", "currentRevision": 4, "currentVersion": "2.2", "available": true,
		"changes": []any{
			map[string]any{"status": "apply", "service": "app", "field": "image", "from": "umami:2.1", "to": "umami:2.2"},
			map[string]any{"status": "kept", "field": "domain", "from": "a", "to": "b", "reason": "you changed it"},
			map[string]any{"status": "apply", "service": "app", "field": "APP_SECRET", "from": "x", "to": "y", "secret": true},
		},
		"apply": 2, "kept": 1, "manual": 0, "applied": 5,
	}
	f.reply("GET", tpInst+"/update", 200, update)

	r := f.run("templates", "update", tpInstID)
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "umami: revision 3 (2.1) -> 4 (2.2)\n")
	require.Equal(t, []string{"apply", "app", "image", "umami:2.1", "->", "umami:2.2"}, bkLine(t, r.stdout, "umami:2.1"))
	require.Equal(t, []string{"kept", "domain", "a", "->", "b", "you", "changed", "it"}, bkLine(t, r.stdout, "you changed it"))
	require.Equal(t, []string{"apply", "app", "APP_SECRET", "(secret)"}, bkLine(t, r.stdout, "APP_SECRET"), "a secret change shows no values")
	require.NotContains(t, r.stdout, "x -> y")
	require.Contains(t, r.stdout, "2 to stage, 1 kept, 0 manual, 5 already applied.\n")
	require.Empty(t, f.calls("POST", tpInst+"/update"), "without --apply nothing is staged")

	update["available"] = false
	f.reply("GET", tpInst+"/update", 200, update)
	r = f.run("templates", "update", tpInstID)
	require.Contains(t, r.stdout, "Up to date.\n")
	require.NotContains(t, r.stdout, "STATUS")

	update["blocked"] = "the template added a database; reinstall"
	f.reply("GET", tpInst+"/update", 200, update)
	r = f.run("templates", "update", tpInstID)
	require.Equal(t, "umami: the template added a database; reinstall\n", r.stdout)

	r = f.run("--json", "templates", "update", tpInstID)
	require.Equal(t, float64(4), decode[map[string]any](t, r.stdout)["currentRevision"])
}

func TestTemplatesUpdateApplyStagesTheChanges(t *testing.T) {
	f := newCfgFake(t)
	update := map[string]any{"templateId": "umami", "installedRevision": 3, "installedVersion": "2.1", "currentRevision": 4, "currentVersion": "2.2", "available": true}
	f.reply("GET", tpInst+"/update", 200, update)
	f.reply("POST", tpInst+"/update", 200, map[string]any{"staged": 3, "services": []string{"umami", "umami-db"}, "warnings": []string{"restart needed"}})

	r := f.run("--idempotency-key", "upd-key", "templates", "update", tpInstID, "--apply", "--input", "LANG=de")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Staged 3 change(s) in umami, umami-db. Review them in the staged bar, then deploy.\n", r.stdout)
	require.Equal(t, "Warning: restart needed\n", r.err)
	post := f.only("POST", tpInst+"/update")
	require.Equal(t, map[string]any{"revision": float64(4), "inputs": map[string]any{"LANG": "de"}}, post.json(t), "the revision the user saw is the one applied")
	require.Equal(t, "upd-key", post.header.Get("Idempotency-Key"))

	r = f.run("--json", "templates", "update", tpInstID, "--apply")
	require.Equal(t, float64(3), decode[map[string]any](t, r.stdout)["staged"])

	update["blocked"] = "reinstall the stack"
	f.reply("GET", tpInst+"/update", 200, update)
	r = f.run("templates", "update", tpInstID, "--apply")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "reinstall the stack")
	require.Len(t, f.calls("POST", tpInst+"/update"), 2, "a blocked update is never posted")

	f.problem("GET", tpInst+"/update", 404, "not_found", "no such stack")
	r = f.run("templates", "update", tpInstID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "no such stack (not_found, HTTP 404)")
}

func TestTemplatesRmSendsTheConfirmationAndVolumeChoice(t *testing.T) {
	f := newCfgFake(t)
	f.reply("DELETE", tpInst, 204, "")

	r := f.run("templates", "rm", tpInstID, "--confirm", "umami")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Deleted stack "+tpInstID+"\n", r.stdout)
	q := f.only("DELETE", tpInst).values(t)
	require.Equal(t, "umami", q.Get("confirm"))
	require.Equal(t, "false", q.Get("deleteVolumes"), "volumes survive unless asked")

	r = f.run("templates", "rm", tpInstID, "--confirm", "umami", "--delete-volumes")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "true", f.calls("DELETE", tpInst)[1].values(t).Get("deleteVolumes"))

	f.problem("DELETE", tpInst, 422, "confirm_mismatch", "confirm does not match the template id")
	r = f.run("templates", "rm", tpInstID, "--confirm", "wrong")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "confirm does not match the template id (confirm_mismatch, HTTP 422)")
	require.NotContains(t, r.stdout, "Deleted")
}
