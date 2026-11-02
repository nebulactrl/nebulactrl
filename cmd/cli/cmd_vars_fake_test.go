package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	vrVars    = cfgAPI + "/services/" + fakeSvcID + "/variables"
	vrTeam    = cfgAPI + "/team/shared-variables"
	vrProject = cfgProjectPath + "/shared-variables"
	vrEnv     = cfgAPI + "/environments/" + cfgProdID + "/shared-variables"
)

func vrList() map[string]any {
	return map[string]any{
		"items": []any{
			map[string]any{"key": "PLAIN", "value": "hello", "isSecret": false, "source": "user", "scope": "runtime"},
			map[string]any{"key": "PASSWORD", "value": "s3cret", "isSecret": true, "source": "generated", "scope": "both"},
			map[string]any{"key": "URL", "computedSecret": true, "source": "reference", "scope": "runtime"},
			map[string]any{"key": "EMPTY", "source": "user", "scope": "build"},
		},
		"platform": []any{
			map[string]any{"key": "NEBULA_SERVICE", "value": "web", "description": "the slug"},
			map[string]any{"key": "NEBULA_REGION", "description": "where it runs"},
		},
		"exports": []any{
			map[string]any{"key": "HOST", "description": "internal host", "secret": false},
			map[string]any{"key": "PASSWORD", "description": "db password", "secret": true},
		},
	}
}

func TestVarsLsNeverShowsSecretValues(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", vrVars, 200, vrList())

	r := f.run("vars", "ls", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, []string{"PLAIN", "hello", "no", "user", "runtime"}, bkLine(t, r.stdout, "PLAIN"))
	require.Equal(t, []string{"PASSWORD", secretMask, "yes", "generated", "both"}, bkLine(t, r.stdout, "PASSWORD"))
	require.Equal(t, []string{"URL", secretMask, "(derived)", "no", "reference", "runtime"}, bkLine(t, r.stdout, "URL"))
	require.NotContains(t, r.stdout, "s3cret")
	require.NotContains(t, r.stdout, "PLATFORM KEY", "platform variables are opt-in")
	require.NotContains(t, r.stdout, "EXPORT")

	r = f.run("vars", "ls", fakeSvcID, "--platform", "--exports")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, []string{"NEBULA_SERVICE", "web", "the", "slug"}, bkLine(t, r.stdout, "NEBULA_SERVICE"))
	require.Equal(t, []string{"NEBULA_REGION", "-", "where", "it", "runs"}, bkLine(t, r.stdout, "NEBULA_REGION"), "an unset platform value shows a dash")
	require.Equal(t, []string{"HOST", "internal", "host", "no"}, bkLine(t, r.stdout, "HOST"))
	require.Equal(t, []string{"PASSWORD", "db", "password", "yes"}, bkLine(t, r.stdout, "db password"))
	require.Contains(t, r.stdout, "EXPORT (reference as ${{slug.KEY}})")

	r = f.run("--json", "vars", "ls", fakeSvcID)
	out := decode[map[string]any](t, r.stdout)
	require.Len(t, out["items"], 4)
	require.Len(t, out["platform"], 2)
	require.Len(t, out["exports"], 2)

	f.reply("GET", vrVars, 200, map[string]any{})
	r = f.run("--json", "vars", "ls", fakeSvcID)
	require.Equal(t, map[string]any{"items": []any{}, "platform": []any{}, "exports": []any{}}, decode[map[string]any](t, r.stdout))
}

func TestVarsSetSendsAssignmentsAndNotesWhatStaysSecret(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", vrVars, 200, vrList())
	f.reply("PUT", vrVars, 200, map[string]any{
		"items":    []any{map[string]any{"key": "PASSWORD", "isSecret": true}, map[string]any{"key": "NEW", "isSecret": false}},
		"warnings": []any{map[string]any{"message": "NEW looks like a secret"}},
		"restaged": []any{map[string]any{"slug": "worker"}},
	})

	r := f.run("vars", "set", fakeSvcID, "PASSWORD=x=y", "NEW=1")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Set PASSWORD (secret), NEW\n", r.stdout)
	require.Contains(t, r.err, "PASSWORD is already a secret and stays one\n")
	require.Contains(t, r.err, "warning: NEW looks like a secret\n")
	require.Contains(t, r.err, "Also staged: worker references a changed variable.\n")
	require.Contains(t, r.err, "Staged: run `nebulactrl services deploy "+fakeSvcID+"` to apply.\n")
	body := f.only("PUT", vrVars).json(t)
	require.Equal(t, []any{
		map[string]any{"key": "PASSWORD", "value": "x=y"},
		map[string]any{"key": "NEW", "value": "1"},
	}, body["variables"], "only the first = splits key from value")

	r = f.run("vars", "set", fakeSvcID, "NEW=2", "--secret=false", "--scope", "build", "--comment", "")
	require.Equal(t, 0, r.code, r.all())
	require.NotContains(t, r.err, "already a secret", "an explicit --secret skips the notice")
	require.Len(t, f.calls("GET", vrVars), 1, "and the lookup")
	require.Equal(t, []any{map[string]any{"key": "NEW", "value": "2", "isSecret": false, "scope": "build", "comment": ""}},
		f.calls("PUT", vrVars)[1].json(t)["variables"])

	r = f.run("--json", "vars", "set", fakeSvcID, "NEW=3", "--secret")
	require.Equal(t, 0, r.code, r.all())
	out := decode[map[string]any](t, r.stdout)
	require.Len(t, out["items"], 2)
	require.Len(t, out["warnings"], 1)
	require.Len(t, out["restaged"], 1)
	require.Empty(t, r.err, "--json is silent on stderr")
}

func TestVarsSetReadsOneValueFromStdin(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", vrVars, 200, map[string]any{})
	f.reply("PUT", vrVars, 200, map[string]any{})

	r := f.runStdin("line one\nline two\r\n", "vars", "set", fakeSvcID, "KEY=-")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, []any{map[string]any{"key": "KEY", "value": "line one\nline two"}}, f.only("PUT", vrVars).json(t)["variables"],
		"a single trailing newline is dropped, inner ones stay")
	require.NotContains(t, r.all(), "line one", "values are never echoed")
}

func TestVarsSetRejectsBadAssignmentsBeforeAnyRequest(t *testing.T) {
	f := newCfgFake(t)
	before := f.total()
	for name, tc := range map[string]struct {
		args  []string
		stdin string
		want  string
	}{
		"no equals":    {[]string{"vars", "set", fakeSvcID, "JUSTKEY"}, "", "expected KEY=VALUE arguments"},
		"empty key":    {[]string{"vars", "set", fakeSvcID, "=value"}, "", "expected KEY=VALUE arguments"},
		"duplicate":    {[]string{"vars", "set", fakeSvcID, "A=1", "A=2"}, "", "variable A given twice"},
		"two stdins":   {[]string{"vars", "set", fakeSvcID, "A=-", "B=-"}, "x", "only one KEY=- can read from stdin"},
		"no variables": {[]string{"vars", "set", fakeSvcID}, "", "usage: nebulactrl vars set"},
	} {
		r := f.runStdin(tc.stdin, tc.args...)
		require.Equal(t, 2, r.code, "%s: %s", name, r.all())
		require.Contains(t, r.err, tc.want, name)
	}
	require.Equal(t, before, f.total())
}

func TestVarsRmDeletesEveryKeyAndNamesTheOneThatFailed(t *testing.T) {
	f := newCfgFake(t)
	f.reply("DELETE", vrVars+"/A", 204, "")
	f.reply("DELETE", vrVars+"/B", 204, "")

	r := f.run("vars", "rm", fakeSvcID, "A", "B")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Deleted A, B\n", r.stdout)
	require.Contains(t, r.err, "Staged: run `nebulactrl services deploy "+fakeSvcID+"` to apply.")

	r = f.run("--json", "vars", "rm", fakeSvcID, "A")
	require.Equal(t, map[string]any{"deleted": []any{"A"}}, decode[map[string]any](t, r.stdout))

	r = f.run("vars", "rm", fakeSvcID, "A", "MISSING", "B")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "delete MISSING: no such route in the fake")
	require.Len(t, f.calls("DELETE", vrVars+"/B"), 1, "B was not attempted after the failure (only the first run deleted it)")
	require.NotContains(t, r.stdout, "Deleted")
}

func TestVarsRotatePrintsThePlanAndWhetherItWasStaged(t *testing.T) {
	f := newCfgFake(t)
	plan := map[string]any{
		"key": "PASSWORD", "staged": false,
		"affected":    []any{map[string]any{"slug": "web", "via": []string{"PASSWORD", "DATABASE_URL"}, "strategy": "rolling", "downtime": "none"}},
		"deployOrder": []string{"db", "web"},
		"warnings":    []string{"web restarts"},
		"blockers":    []any{map[string]any{"code": "deployed", "message": "database credentials cannot be rotated once deployed"}},
	}
	f.reply("POST", vrVars+"/PASSWORD/rotate", 200, plan)

	r := f.run("vars", "rotate", fakeSvcID, "PASSWORD", "--dry-run")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"dryRun": true}, f.only("POST", vrVars+"/PASSWORD/rotate").json(t))
	require.Equal(t, []string{"web", "PASSWORD,", "DATABASE_URL", "rolling", "none"}, bkLine(t, r.stdout, "web")[:5])
	require.Contains(t, r.stdout, "Deploy order: db → web\n")
	require.Contains(t, r.err, "warning: web restarts\n")
	require.Contains(t, r.err, "blocked: database credentials cannot be rotated once deployed\n")
	require.Contains(t, r.err, "Dry run: nothing changed.\n")

	plan["staged"] = true
	plan["blockers"] = []any{}
	f.reply("POST", vrVars+"/PASSWORD/rotate", 200, plan)
	r = f.run("vars", "rotate", fakeSvcID, "PASSWORD")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "Rotated PASSWORD (staged). Run `nebulactrl services deploy` for each service in the order above.\n")
	require.NotContains(t, r.err, "Dry run")
	require.Equal(t, map[string]any{"dryRun": false}, f.calls("POST", vrVars+"/PASSWORD/rotate")[1].json(t))

	r = f.run("--json", "vars", "rotate", fakeSvcID, "PASSWORD")
	require.Equal(t, "PASSWORD", decode[map[string]any](t, r.stdout)["key"])

	f.problem("POST", vrVars+"/PLAIN/rotate", 422, "not_generated", "PLAIN was not generated")
	r = f.run("vars", "rotate", fakeSvcID, "PLAIN")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "PLAIN was not generated (not_generated, HTTP 422)")
}

func TestVarsHistoryMasksSecretVersions(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", vrVars+"/TOKEN/history", 200, map[string]any{"items": []any{
		map[string]any{"version": 2, "op": "rotate", "isSecret": true, "value": "leaked?", "fingerprint": "ab12cd", "source": "generated", "actorType": "user", "createdAt": "2026-01-02T03:04:05Z"},
		map[string]any{"version": 1, "op": "set", "isSecret": false, "value": "plain", "fingerprint": "ef56", "source": "user", "actorType": "token", "createdAt": "2026-01-01T03:04:05Z"},
	}})

	r := f.run("vars", "history", fakeSvcID, "TOKEN")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, []string{"2", "rotate", secretMask, "ab12cd", "generated", "user", "2026-01-02", "03:04"}, bkLine(t, r.stdout, "ab12cd"))
	require.Equal(t, []string{"1", "set", "plain", "ef56", "user", "token", "2026-01-01", "03:04"}, bkLine(t, r.stdout, "ef56"))
	require.NotContains(t, r.stdout, "leaked?")

	r = f.run("--json", "vars", "history", fakeSvcID, "TOKEN")
	require.Len(t, decode[map[string]any](t, r.stdout)["items"], 2)

	f.reply("GET", vrVars+"/TOKEN/history", 200, map[string]any{})
	r = f.run("--json", "vars", "history", fakeSvcID, "TOKEN")
	require.Equal(t, map[string]any{"items": []any{}}, decode[map[string]any](t, r.stdout))
}

func vrShared() map[string]any {
	return map[string]any{
		"items": []any{
			map[string]any{"key": "REGION", "value": "eu", "layer": "project", "usedBy": 3},
			map[string]any{"key": "TOKEN", "value": "x", "isSecret": true, "layer": "team", "inherited": true, "usedBy": 1},
			map[string]any{"key": "DERIVED", "computedSecret": true, "layer": "environment", "overrides": true, "usedBy": 0},
		},
		"warnings": []any{map[string]any{"message": "REGION is unused in staging"}},
		"restaged": []any{map[string]any{"slug": "web"}},
	}
}

func TestSharedLsReadsTheTeamProjectOrEnvironmentLayer(t *testing.T) {
	f := newCfgFake(t)
	for _, p := range []string{vrTeam, vrProject, vrEnv} {
		f.reply("GET", p, 200, vrShared())
	}

	for _, ref := range []string{"@team", "acme-shop", "ACME-SHOP/Production"} {
		r := f.run("vars", "shared", "ls", ref)
		require.Equal(t, 0, r.code, "%s: %s", ref, r.all())
		require.Equal(t, []string{"REGION", "eu", "project", "3"}, bkLine(t, r.stdout, "REGION"), ref)
		require.Equal(t, []string{"TOKEN", secretMask, "team", "(inherited)", "1"}, bkLine(t, r.stdout, "TOKEN"), ref)
		require.Equal(t, []string{"DERIVED", secretMask, "(derived)", "environment", "(overrides", "project)", "0"}, bkLine(t, r.stdout, "DERIVED"), ref)
		require.Contains(t, r.err, "warning: REGION is unused in staging\n", ref)
		require.Contains(t, r.err, "Also staged: web resolves differently now.\n", ref)
	}
	require.Len(t, f.calls("GET", vrTeam), 1)
	require.Len(t, f.calls("GET", vrProject), 1)
	require.Len(t, f.calls("GET", vrEnv), 1)

	r := f.run("--json", "vars", "shared", "ls", "@team")
	out := decode[map[string]any](t, r.stdout)
	require.Len(t, out["items"], 3)
	require.Len(t, out["warnings"], 1)
	require.Len(t, out["restaged"], 1)
	require.Empty(t, r.err)

	r = f.run("vars", "shared", "ls", "ghost")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `project "ghost" not found`)
	r = f.run("vars", "shared", "ls", "acme-shop/nope")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `environment "nope" not found`)
}

func TestSharedSetSendsVariablesToTheMatchingEndpoint(t *testing.T) {
	f := newCfgFake(t)
	for _, p := range []string{vrTeam, vrProject, vrEnv} {
		f.reply("PUT", p, 200, vrShared())
	}

	r := f.runStdin("from-stdin\n", "vars", "shared", "set", "@team", "REGION=eu", "TOKEN=-", "--secret")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "REGION")
	require.Equal(t, []any{
		map[string]any{"key": "REGION", "value": "eu", "isSecret": true},
		map[string]any{"key": "TOKEN", "value": "from-stdin", "isSecret": true},
	}, f.only("PUT", vrTeam).json(t)["variables"])

	r = f.run("vars", "shared", "set", "acme-shop", "REGION=us")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, []any{map[string]any{"key": "REGION", "value": "us"}}, f.only("PUT", vrProject).json(t)["variables"],
		"without --secret the flag is not sent: an existing secret stays one")

	r = f.run("vars", "shared", "set", "acme-shop/production", "REGION=ap", "--secret=false")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, []any{map[string]any{"key": "REGION", "value": "ap", "isSecret": false}}, f.only("PUT", vrEnv).json(t)["variables"])

	before := f.total()
	r = f.run("vars", "shared", "set", "@team", "BROKEN")
	require.Equal(t, 2, r.code)
	require.Contains(t, r.err, "expected KEY=VALUE arguments")
	require.Equal(t, before, f.total())

	f.problem("PUT", vrProject, 403, "forbidden", "needs variables:write")
	r = f.run("vars", "shared", "set", "acme-shop", "REGION=us")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "needs variables:write (forbidden, HTTP 403)")
}

func TestSharedRmDeletesFromTheLayerAndNamesTheFailingKey(t *testing.T) {
	f := newCfgFake(t)
	// The API answers a delete with 200 and the remaining list.
	left := map[string]any{"items": []any{}, "warnings": []any{}, "restaged": []any{}}
	f.reply("DELETE", vrTeam+"/A", 200, left)
	f.reply("DELETE", vrProject+"/A", 200, left)
	f.reply("DELETE", vrProject+"/B", 200, left)
	f.reply("DELETE", vrEnv+"/A", 200, left)

	for ref, path := range map[string]string{"@team": vrTeam, "acme-shop": vrProject, "acme-shop/staging": ""} {
		if path == "" {
			f.reply("DELETE", cfgAPI+"/environments/"+cfgStagingID+"/shared-variables/A", 200, left)
			path = cfgAPI + "/environments/" + cfgStagingID + "/shared-variables"
		}
		r := f.run("vars", "shared", "rm", ref, "A")
		require.Equal(t, 0, r.code, "%s: %s", ref, r.all())
		require.Equal(t, "Deleted A\n", r.stdout, ref)
		require.Len(t, f.calls("DELETE", path+"/A"), 1, ref)
	}

	r := f.run("--json", "vars", "shared", "rm", "acme-shop", "A", "B")
	require.Equal(t, map[string]any{"deleted": []any{"A", "B"}}, decode[map[string]any](t, r.stdout))

	for _, ref := range []string{"@team", "acme-shop", "acme-shop/production"} {
		r = f.run("vars", "shared", "rm", ref, "A", "GONE")
		require.Equal(t, 1, r.code, ref)
		require.Contains(t, r.err, "delete GONE: ", ref)
	}
}

func TestSharedRotateAndShareTargetTheLayer(t *testing.T) {
	f := newCfgFake(t)
	plan := map[string]any{"key": "TOKEN", "staged": true, "affected": []any{map[string]any{"slug": "web", "via": []string{"TOKEN"}, "strategy": "rolling", "downtime": "none"}}}
	for _, p := range []string{vrTeam, vrProject, vrEnv} {
		f.reply("POST", p+"/TOKEN/rotate", 200, plan)
		f.reply("POST", p+"/TOKEN/share", 200, vrShared())
	}

	for ref, path := range map[string]string{"@team": vrTeam, "acme-shop": vrProject, "acme-shop/production": vrEnv} {
		r := f.run("vars", "shared", "rotate", ref, "TOKEN", "--dry-run")
		require.Equal(t, 0, r.code, "%s: %s", ref, r.all())
		require.Contains(t, r.stdout, "Rotated TOKEN (staged)", ref)
		require.Equal(t, map[string]any{"dryRun": true}, f.only("POST", path+"/TOKEN/rotate").json(t), ref)
	}

	r := f.run("--json", "vars", "shared", "rotate", "@team", "TOKEN")
	require.Equal(t, "TOKEN", decode[map[string]any](t, r.stdout)["key"])

	f.reply("GET", cfgAPI+"/environments/"+cfgProdID+"/services", 200, map[string]any{"items": []any{map[string]any{"id": fakeSvc2ID, "slug": "worker", "name": "worker"}}})
	for ref, tc := range map[string]struct{ path, ns string }{
		"@team": {vrTeam, "team"}, "acme-shop": {vrProject, "shared"}, "acme-shop/production": {vrEnv, "shared"},
	} {
		r = f.run("vars", "shared", "share", ref, "TOKEN", fakeSvcID, "acme-shop/production/worker")
		require.Equal(t, 0, r.code, "%s: %s", ref, r.all())
		require.Contains(t, r.stdout, "Staged TOKEN=${{"+tc.ns+".TOKEN}} on 2 service(s).\n", ref)
		require.Contains(t, r.stdout, "REGION", "the layer's variables follow")
		require.Equal(t, map[string]any{"serviceIds": []any{fakeSvcID, fakeSvc2ID}}, f.only("POST", tc.path+"/TOKEN/share").json(t), ref)
	}

	r = f.run("--json", "vars", "shared", "share", "@team", "TOKEN", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.False(t, strings.Contains(r.stdout, "Staged TOKEN"), "--json prints only the list")

	r = f.run("vars", "shared", "share", "@team", "TOKEN", "acme-shop/production/ghost")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `service "ghost" not found in acme-shop/production`)
	r = f.run("vars", "shared", "share", "@team", "TOKEN")
	require.Equal(t, 2, r.code, "at least one service is required")
}
