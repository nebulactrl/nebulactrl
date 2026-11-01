package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

const combineRepo = "https://example.com/acme/shop.git"

// combineSeed creates project Acme, an online server whose agent runs several
// processes, and two git services of one repository: web (port 3000, the
// target) and worker (port 3001, with a start command and a domain). Their
// APP_ENV differs, ONLY_HERE exists on the worker only and SAME is the same.
func (e *env) combineSeed() {
	e.t.Helper()
	e.mustRun("", "projects", "create", "Acme")
	e.mustRun("", "servers", "enroll-token", "--name", "box1")
	_, err := e.pool.Exec(context.Background(),
		`UPDATE servers SET status = 'online', facts = '{"os":"linux","capabilities":["service.processes"]}'::jsonb WHERE name = 'box1'`)
	require.NoError(e.t, err)
	for _, s := range []struct{ name, port string }{{"web", "3000"}, {"worker", "3001"}} {
		e.mustRun("", "services", "create", "--project", "acme", "--env", "production", "--name", s.name,
			"--git", combineRepo, "--server", "box1", "--port", s.port)
	}
	e.mustRun("", "services", "processes", "set", "acme/production/worker", "--default", "--command", "bin/worker")
	e.mustRun("", "vars", "set", "acme/production/web", "APP_ENV=production", "SAME=1")
	e.mustRun("", "vars", "set", "acme/production/worker", "APP_ENV=staging", "SAME=1", "ONLY_HERE=yes")
	e.mustRun("", "domains", "add", "acme/production/worker", "api.example.com")
}

// deployedTarget gives a service a succeeded deployment and makes it the
// current one, as a real deploy would. Aborting a combine reverts the target to
// its current release, so the target has to have one.
func (e *env) deployedTarget(ref string) {
	e.t.Helper()
	var id string
	for _, s := range decode[[]map[string]any](e.t, e.mustRun("", "--json", "services", "ls", "--project", "acme", "--env", "production").stdout) {
		if "acme/production/"+s["slug"].(string) == ref {
			id = s["id"].(string)
		}
	}
	require.NotEmpty(e.t, id)
	e.finishNext(id, "succeeded", "ok")
	e.mustRun("", "services", "deploy", ref, "--wait")
	_, err := e.pool.Exec(context.Background(),
		`UPDATE services SET current_deployment_id = (SELECT id FROM deployments WHERE service_id = $1 ORDER BY created_at DESC LIMIT 1), applied_generation = generation WHERE id = $1`, id)
	require.NoError(e.t, err)
}

func TestCombinePlanRefusesWhatCannotBeCombined(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.seed() // an image service
	e.mustRun("", "services", "create", "--project", "acme", "--env", "production", "--name", "api",
		"--git", combineRepo, "--server", "box1", "--port", "3001")

	r := e.run("", "services", "combine", "acme/production/api", "--into", "acme/production/web")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.stdout, "Blocking:")
	require.Contains(t, r.stdout, "not_combinable")
	require.Contains(t, r.stdout, "web is built from an image")
	require.Contains(t, r.err, "cannot be combined")

	// --yes does not get around a blocker, and the JSON plan carries it.
	r = e.run("", "--json", "services", "combine", "acme/production/api", "--into", "acme/production/web", "--yes")
	require.Equal(t, 1, r.code, r.all())
	plan := decode[map[string]any](t, r.stdout)
	require.NotEmpty(t, plan["blocking"])
	require.NotEmpty(t, plan["fingerprint"])

	for name, args := range map[string][]string{
		"one service only": {"services", "combine", "acme/production/api"},
		"bad process name": {"services", "combine", "acme/production/api", "--into", "acme/production/web", "--process-name", "nope"},
		"bad var":          {"services", "combine", "acme/production/api", "--into", "acme/production/web", "--var", "APP_ENV"},
	} {
		require.Equal(t, 2, e.run("", args...).code, name)
	}
}

func TestCombinePlanPreviewThenStartStatusAbort(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.combineSeed()
	e.deployedTarget("acme/production/web")
	web, worker := "acme/production/web", "acme/production/worker"

	// Without --yes nothing changes: the plan is printed.
	r := e.mustRun("", "services", "combine", worker, "--into", web)
	require.Contains(t, r.stdout, "Combine into web")
	require.Contains(t, r.stdout, "api.example.com", "the domain that moves")
	require.Contains(t, r.stdout, "ONLY_HERE only in worker")
	require.Contains(t, r.stdout, "APP_ENV differs")
	require.Contains(t, r.stdout, "web=production")
	require.Contains(t, r.stdout, "worker=staging")
	require.Contains(t, r.stdout, "choose with --var APP_ENV=<service>")
	require.Contains(t, r.all(), "Nothing changed")
	require.Len(t, e.processList(), 1, "the preview stages nothing")

	r = e.mustRun("", "--json", "services", "combine", worker, "--into", web)
	plan := decode[map[string]any](t, r.stdout)
	require.Empty(t, plan["blocking"])
	require.Len(t, plan["members"], 2)
	require.Len(t, plan["domainMoves"], 1)
	require.Equal(t, "api.example.com", plan["domainMoves"].([]any)[0].(map[string]any)["hostname"])
	require.Len(t, plan["variables"].(map[string]any)["conflicts"], 1)

	// Every conflicting variable needs a choice, and only conflicting ones can be chosen.
	r = e.run("", "services", "combine", worker, "--into", web, "--process-name", "worker=queue", "--yes")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "APP_ENV")
	require.Contains(t, r.err, "--var APP_ENV=")
	r = e.run("", "services", "combine", worker, "--into", web, "--var", "SAME=web", "--yes")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "SAME")
	require.Len(t, e.processList(), 1, "a refused start stages nothing")

	// Start: the target gains the worker as a process; nothing is deleted.
	r = e.mustRun("", "--json", "services", "combine", worker, "--into", web,
		"--var", "APP_ENV=worker", "--process-name", "worker=queue", "--yes")
	started := decode[map[string]any](t, r.stdout)
	id := started["id"].(string)
	require.NotEmpty(t, id)
	require.Equal(t, "staged", started["state"])
	require.Len(t, started["sourceServiceIds"], 1)

	ps := e.processList()
	require.Equal(t, []string{"web", "queue"}, processNamesOfList(ps))
	queue := processByName(t, ps, "queue")
	require.Equal(t, []any{"bin/worker"}, queue["command"])
	require.EqualValues(t, 3001, queue["port"])
	require.Contains(t, e.mustRun("", "services", "ls", "--project", "acme", "--env", "production").stdout, "worker", "the source keeps existing")

	r = e.mustRun("", "services", "combine", "status", id)
	require.Contains(t, r.stdout, "Combine "+id+": staged")
	require.Contains(t, r.stdout, started["targetServiceId"].(string))
	r = e.mustRun("", "--json", "services", "combine", "status", id)
	require.Equal(t, "staged", decode[map[string]any](t, r.stdout)["state"])

	// A second combine of the same services is refused while this one is active.
	r = e.run("", "services", "combine", worker, "--into", web, "--yes")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.all(), "combine_in_progress")

	// Abort takes the staged process back.
	r = e.mustRun("", "services", "combine", "abort", id)
	require.Contains(t, r.stdout, "aborted")
	require.Equal(t, []string{"web"}, processNamesOfList(e.processList()))
	r = e.run("", "services", "combine", "retry", id)
	require.Equal(t, 1, r.code, "only a failed combine can be retried: %s", r.all())
	require.Contains(t, r.all(), "invalid_state")
}

func TestCombineWaitTimesOutWhileTheDeploymentIsPending(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.combineSeed()

	r := e.run("", "services", "combine", "acme/production/worker", "--into", "acme/production/web",
		"--var", "APP_ENV=web", "--process-name", "worker=queue", "--yes", "--wait", "--timeout", "300ms")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "timed out after")
	require.Contains(t, r.err, "staged")
}
