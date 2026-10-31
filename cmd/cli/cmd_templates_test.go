package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTemplatesCLIListShowPlanInstallDelete(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.mustRun("", "projects", "create", "Acme")
	e.mustRun("", "servers", "enroll-token", "--name", "box1")

	list := decode[map[string]any](t, e.mustRun("", "--json", "templates", "ls", "--category", "web").stdout)
	require.NotEmpty(t, list["items"])
	plain := e.mustRun("", "templates", "ls", "--search", "umami").stdout
	require.Contains(t, plain, "umami")

	show := e.mustRun("", "templates", "show", "n8n").stdout
	require.Contains(t, show, "source-available")
	require.Contains(t, show, "managed postgres")

	// A missing template is a clean not-found error.
	r := e.run("", "templates", "show", "nope")
	require.Equal(t, 1, r.code)

	// Inputs must be KEY=VALUE.
	r = e.run("", "templates", "plan", "umami", "--project", "acme", "--env", "production", "--input", "oops")
	require.Equal(t, 2, r.code, r.all())

	plan := decode[map[string]any](t, e.mustRun("", "--json", "templates", "plan", "umami", "--project", "acme", "--env", "production", "--server", "box1").stdout)
	require.Contains(t, plan, "services")

	// Install stages two services; the stack is listed and deletes as a unit.
	inst := decode[map[string]any](t, e.mustRun("", "--json", "templates", "install", "umami", "--project", "acme", "--env", "production", "--server", "box1").stdout)
	id := inst["instance"].(map[string]any)["id"].(string)
	svcs := decode[[]map[string]any](t, e.mustRun("", "--json", "services", "ls", "--project", "acme", "--env", "production").stdout)
	require.Len(t, svcs, 2)
	installed := decode[[]map[string]any](t, e.mustRun("", "--json", "templates", "installed", "--project", "acme", "--env", "production").stdout)
	require.Len(t, installed, 1)

	r = e.run("", "templates", "rm", id, "--confirm", "wrong")
	require.Equal(t, 1, r.code)
	e.mustRun("", "templates", "rm", id, "--confirm", "umami")
	svcs = decode[[]map[string]any](t, e.mustRun("", "--json", "services", "ls", "--project", "acme", "--env", "production").stdout)
	require.Empty(t, svcs)
}
