package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServicesCloneEnvsCloneAndMove(t *testing.T) {
	e := newEnv(t)
	e.login()
	svcID := e.seed()
	e.mustRun("", "vars", "set", svcID, "API_KEY="+canary, "--secret")
	e.mustRun("", "vars", "set", svcID, "MODE=production")

	// A dry run changes nothing.
	r := e.mustRun("", "--json", "services", "clone", "acme/production/web", "--dry-run")
	plan := decode[map[string]any](t, r.stdout)
	require.Equal(t, true, plan["dryRun"])
	require.Len(t, plan["services"], 1)
	require.NotContains(t, r.all(), canary)
	require.NotContains(t, e.mustRun("", "services", "ls", "--project", "acme", "--env", "production").stdout, "web-copy")

	// A real clone: staged, secret copied sealed and never printed.
	r = e.mustRun("", "services", "clone", "acme/production/web")
	require.Contains(t, r.stdout, "web-copy")
	require.Contains(t, r.stdout, "Staged")
	require.NotContains(t, r.all(), canary)
	var secret bool
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT v.is_secret FROM service_variables v JOIN services s ON s.id = v.service_id WHERE s.slug = 'web-copy' AND v.key = 'API_KEY'`).Scan(&secret))
	require.True(t, secret)

	// Clone the whole environment; the clone is not production.
	r = e.mustRun("", "--json", "envs", "clone", "production", "--project", "acme", "--name", "Staging")
	res := decode[map[string]any](t, r.stdout)
	env := res["environment"].(map[string]any)
	require.Equal(t, "staging", env["slug"])
	require.Equal(t, false, env["isProduction"])
	require.Len(t, res["services"], 2, "web and web-copy")
	require.NotContains(t, r.all(), canary)
	ls := e.mustRun("", "services", "ls", "--project", "acme", "--env", "staging")
	require.Contains(t, ls.stdout, "web-copy")

	// Move: the plan is clean (no references), then the move is staged.
	r = e.mustRun("", "services", "move", "acme/production/web-copy", "--env", "staging", "--dry-run")
	require.Contains(t, r.stdout, "The move is clean")
	require.Contains(t, r.stdout, "web-copy-2", "staging already has a web-copy: the slug changes")
	r = e.mustRun("", "services", "move", "acme/production/web-copy", "--env", "staging")
	require.Contains(t, r.stdout, "Moved to")
	require.Contains(t, e.mustRun("", "services", "ls", "--project", "acme", "--env", "staging").stdout, "web-copy-2")
	require.NotContains(t, e.mustRun("", "services", "ls", "--project", "acme", "--env", "production").stdout, "web-copy")

	// Usage errors.
	require.Equal(t, 2, e.run("", "services", "move", svcID).code, "--env is required")
	require.Equal(t, 2, e.run("", "services", "clone", svcID, "--domains", "keep").code)
	require.Equal(t, 2, e.run("", "envs", "clone", "production", "--project", "acme").code, "--name is required")
}
