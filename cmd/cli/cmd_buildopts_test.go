package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeployNoCacheIsRefusedForAnImageService(t *testing.T) {
	e := newEnv(t)
	e.login()
	svcID := e.seed() // traefik/whoami: nothing to build

	r := e.run("", "services", "deploy", svcID, "--no-cache")
	require.NotEqual(t, 0, r.code, r.all())
	require.Contains(t, r.err, "git source")

	// Nothing was started by the refused request.
	deps := decode[[]any](t, e.mustRun("", "--json", "deployments", "ls", svcID).stdout)
	require.Empty(t, deps)

	// Without the flag the same service deploys.
	e.mustRun("", "services", "deploy", svcID)
}

func TestServicesUpdateSubmodulesValidatesTheMode(t *testing.T) {
	e := newEnv(t)
	e.login()
	svcID := e.seed()

	r := e.run("", "services", "update", svcID, "--submodules", "recursive")
	require.NotEqual(t, 0, r.code, r.all())
	require.Contains(t, r.err, "auto, on or off")

	r = e.run("", "services", "update", svcID)
	require.NotEqual(t, 0, r.code, r.all())
	require.Contains(t, r.err, "--submodules")

	e.mustRun("", "services", "update", svcID, "--submodules", "on")
	svc := decode[map[string]any](t, e.mustRun("", "--json", "services", "update", svcID, "--submodules", "off").stdout)
	require.Equal(t, "off", svc["config"].(map[string]any)["submodules"])

	// auto is the unset value.
	svc = decode[map[string]any](t, e.mustRun("", "--json", "services", "update", svcID, "--submodules", "auto").stdout)
	_, set := svc["config"].(map[string]any)["submodules"]
	require.False(t, set)
}
