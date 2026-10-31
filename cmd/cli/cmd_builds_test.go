package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func (e *env) registry(name string) string {
	e.t.Helper()
	var id string
	require.NoError(e.t, e.pool.QueryRow(context.Background(),
		`INSERT INTO registries (workspace_id, name, kind, host) SELECT id, $1, 'external', 'registry.example.com' FROM workspaces LIMIT 1 RETURNING id::text`, name).Scan(&id))
	return id
}

func TestServersUpdateSetsTheBuildRole(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.mustRun("", "servers", "enroll-token", "--name", "builder")

	r := e.mustRun("", "--json", "servers", "update", "builder", "--role", "builds", "--max-builds", "4", "--allow-emulation", "--allow-previews")
	v := decode[map[string]any](t, r.stdout)
	require.Equal(t, true, v["buildEnabled"])
	require.Equal(t, false, v["acceptWorkloads"])
	require.Equal(t, float64(4), v["buildMaxConcurrent"])
	require.Equal(t, true, v["buildAllowEmulation"])
	require.Equal(t, true, v["buildAllowPreviews"])

	r = e.mustRun("", "servers", "update", "builder", "--role", "both", "--allow-emulation=false")
	require.Contains(t, r.stdout, "role both")
	require.Contains(t, r.stdout, "emulation off")
	require.Contains(t, r.stdout, "previews on")
	r = e.mustRun("", "servers", "ls")
	require.Contains(t, r.stdout, "ROLE")
	require.Contains(t, r.stdout, "both")

	for _, args := range [][]string{
		{"servers", "update", "builder"},
		{"servers", "update", "builder", "--role", "everything"},
		{"servers", "update", "builder", "--max-builds", "17"},
	} {
		r = e.run("", args...)
		require.Equal(t, 2, r.code, "%v: %s", args, r.all())
	}
}

func TestProjectsUpdateSetsTheBuildPlacement(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.mustRun("", "projects", "create", "Acme")
	e.mustRun("", "servers", "enroll-token", "--name", "builder")
	e.mustRun("", "servers", "update", "builder", "--role", "builds")
	reg := e.registry("main")

	r := e.mustRun("", "--json", "projects", "update", "acme", "--build-location", "build-server", "--build-server", "builder", "--registry", reg)
	v := decode[map[string]any](t, r.stdout)
	require.Equal(t, "build_server", v["buildLocation"])
	require.Equal(t, reg, v["registryId"])
	require.NotEmpty(t, v["buildServerId"])

	// Placement is replaced as a unit; the name changes on its own.
	r = e.mustRun("", "--json", "projects", "update", "acme", "--name", "Acme 2")
	require.Equal(t, "build_server", decode[map[string]any](t, r.stdout)["buildLocation"])
	r = e.mustRun("", "--json", "projects", "update", "acme", "--build-location", "target")
	v = decode[map[string]any](t, r.stdout)
	require.Equal(t, "target", v["buildLocation"])
	require.Empty(t, v["buildServerId"])

	r = e.run("", "projects", "update", "acme", "--registry", reg)
	require.Equal(t, 2, r.code, r.all())
	r = e.run("", "projects", "update", "acme", "--build-location", "cloud")
	require.Equal(t, 2, r.code, r.all())
}

func TestServicesCreateAndUpdateBuildOverride(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.mustRun("", "projects", "create", "Acme")
	e.mustRun("", "servers", "enroll-token", "--name", "box1")
	e.mustRun("", "servers", "enroll-token", "--name", "builder")
	e.mustRun("", "servers", "update", "builder", "--role", "builds")
	reg := e.registry("main")

	r := e.mustRun("", "--json", "services", "create", "--project", "acme", "--env", "production", "--name", "api",
		"--git", "https://example.com/acme/app.git", "--server", "box1", "--port", "8080",
		"--build-location", "build-server", "--build-server", "builder", "--registry", reg)
	cfg := decode[map[string]any](t, r.stdout)["config"].(map[string]any)
	require.Equal(t, "build_server", cfg["buildLocation"])
	require.Equal(t, reg, cfg["registryId"])
	require.NotEmpty(t, cfg["buildServerId"])

	// Update keeps the rest of the config and stages the change.
	r = e.mustRun("", "--json", "services", "update", "acme/production/api", "--build-location", "target", "--build-server", "none", "--registry", "none")
	v := decode[map[string]any](t, r.stdout)
	cfg = v["config"].(map[string]any)
	require.Equal(t, "target", cfg["buildLocation"])
	require.Empty(t, cfg["buildServerId"])
	require.Empty(t, cfg["registryId"])
	require.Equal(t, "https://example.com/acme/app.git", cfg["gitUrl"])
	require.Equal(t, float64(8080), cfg["port"])
	require.Equal(t, true, v["hasStagedChanges"])

	r = e.mustRun("", "services", "update", "acme/production/api", "--build-location", "inherit")
	require.Contains(t, r.stdout, "build location inherit")

	r = e.run("", "services", "create", "--project", "acme", "--env", "production", "--name", "img",
		"--image", "traefik/whoami:latest", "--build-location", "target")
	require.Equal(t, 2, r.code, "build flags apply to git sources only: %s", r.all())
	r = e.run("", "services", "update", "acme/production/api")
	require.Equal(t, 2, r.code, r.all())
}

func TestBuildSettingsShowAndSet(t *testing.T) {
	e := newEnv(t)
	e.login()
	reg := e.registry("main")

	r := e.mustRun("", "--json", "build-settings")
	require.Equal(t, "target", decode[map[string]any](t, r.stdout)["buildLocation"])

	r = e.mustRun("", "build-settings", "--build-location", "build-server", "--registry", reg)
	require.Contains(t, r.stdout, "Default build location: build_server")
	require.Contains(t, r.stdout, reg)

	// Changing one half keeps the other; none clears the registry.
	r = e.mustRun("", "--json", "build-settings", "--build-location", "target")
	v := decode[map[string]any](t, r.stdout)
	require.Equal(t, "target", v["buildLocation"])
	require.Equal(t, reg, v["registryId"])
	r = e.mustRun("", "--json", "build-settings", "--registry", "none")
	require.Empty(t, decode[map[string]any](t, r.stdout)["registryId"])

	r = e.run("", "build-settings", "--build-location", "inherit")
	require.Equal(t, 2, r.code, r.all())
}
