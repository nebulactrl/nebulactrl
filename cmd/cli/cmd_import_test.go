package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImportComposeCLI(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.mustRun("", "projects", "create", "Acme")
	e.mustRun("", "servers", "enroll-token", "--name", "box1")

	dir := t.TempDir()
	write := func(name, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	write("docker-compose.yml", `
name: blog
services:
  web:
    image: nginx:${TAG}
    ports: ["8080:80"]
    environment:
      API_TOKEN: ${API_TOKEN}
      UPSTREAM: http://api:3000
    volumes:
      - ./conf/site.conf:/etc/nginx/conf.d/site.conf:ro
    depends_on: [api]
  api:
    image: ghcr.io/example/api:1
    expose: ["3000"]
  cache:
    image: redis:7.2
    profiles: [extras]
`)
	write(".env", "TAG=1.27\nAPI_TOKEN=sekret-canary-98765\n")
	write("conf/site.conf", "server { listen 80; }\n")
	compose := filepath.Join(dir, "docker-compose.yml")

	// Usage errors.
	require.Equal(t, 2, e.run("", "import", "compose", compose, "--project", "acme", "--env", "production", "--server", "box1", "--publish", "oops").code)

	// Plan: nothing is written, secrets are never printed, files next to the Compose file are picked up.
	plan := e.mustRun("", "import", "compose", compose, "--project", "acme", "--env", "production", "--plan")
	require.Contains(t, plan.stdout, "nginx:1.27")
	require.Contains(t, plan.stdout, "${{api.NEBULA_PRIVATE_HOST}}")
	require.Contains(t, plan.stdout, "not imported")
	require.NotContains(t, plan.all(), "sekret-canary-98765")
	svcs := decode[[]map[string]any](t, e.mustRun("", "--json", "services", "ls", "--project", "acme", "--env", "production").stdout)
	require.Empty(t, svcs)

	js := decode[map[string]any](t, e.mustRun("", "--json", "import", "compose", compose, "--project", "acme", "--env", "production", "--plan").stdout)
	require.Equal(t, true, js["installable"])

	// Import stages the two services, plus the one the flag opts into.
	out := e.mustRun("", "import", "compose", compose, "--project", "acme", "--env", "production", "--server", "box1", "--include", "cache", "--no-managed", "cache")
	require.Contains(t, out.stdout, "staged")
	require.NotContains(t, out.all(), "sekret-canary-98765")
	svcs = decode[[]map[string]any](t, e.mustRun("", "--json", "services", "ls", "--project", "acme", "--env", "production").stdout)
	require.Len(t, svcs, 3)

	// The secret is stored as a secret variable and never listed.
	vars := e.mustRun("", "vars", "ls", "acme/production/web")
	require.NotContains(t, vars.all(), "sekret-canary-98765")
}

func TestImportComposeAsStackCLI(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.mustRun("", "projects", "create", "Acme")
	e.mustRun("", "servers", "enroll-token", "--name", "box1")

	dir := t.TempDir()
	write := func(name, content string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	write("privileged.yml", `
name: ops
services:
  web:
    image: nginx:1.27
    ports: ["8080:80"]
  agent:
    image: example/agent:1
    privileged: true
`)
	write("ok.yml", `
name: blog
services:
  web:
    image: nginx:${TAG}
    ports: ["8080:80"]
    environment:
      API_TOKEN: ${API_TOKEN}
  worker:
    image: busybox:1.36
`)
	write(".env", "TAG=1.27\nAPI_TOKEN=sekret-canary-98765\n")
	base := []string{"--project", "acme", "--env", "production"}
	run := func(args ...string) []string { return append(append([]string{"import", "compose"}, args...), base...) }

	// Flags that only make sense when splitting are usage errors.
	bad := e.run("", run(filepath.Join(dir, "ok.yml"), "--as-stack", "--managed", "web")...)
	require.Equal(t, 2, bad.code)

	// A privileged service is listed as a violation and blocks the plan.
	priv := e.run("", run(filepath.Join(dir, "privileged.yml"), "--as-stack", "--plan")...)
	require.Equal(t, 1, priv.code)
	require.Contains(t, priv.all(), "COMPONENT")
	require.Contains(t, priv.all(), "agent")
	require.Contains(t, priv.all(), "[restricted]")
	require.Contains(t, priv.all(), "privileged")
	js := decode[map[string]any](t, e.run("", append([]string{"--json"}, run(filepath.Join(dir, "privileged.yml"), "--as-stack", "--plan")...)...).stdout)
	require.Equal(t, "stack", js["mode"])
	require.Equal(t, false, js["installable"])

	// The clean file plans and imports as one stack; secrets are never printed.
	ok := filepath.Join(dir, "ok.yml")
	plan := e.mustRun("", run(ok, "--as-stack", "--plan")...)
	require.Contains(t, plan.stdout, "nginx:${TAG}")
	require.Contains(t, plan.stdout, "API_TOKEN: secret")
	require.NotContains(t, plan.all(), "sekret-canary-98765")
	out := e.mustRun("", run(ok, "--as-stack", "--server", "box1", "--secret", "TAG")...)
	require.Contains(t, out.stdout, "as a stack")
	require.NotContains(t, out.all(), "sekret-canary-98765")
	svcs := decode[[]map[string]any](t, e.mustRun("", "--json", "services", "ls", "--project", "acme", "--env", "production").stdout)
	require.Len(t, svcs, 1)
	require.Equal(t, "stack", svcs[0]["kind"])

	vars := e.mustRun("", "vars", "ls", "acme/production/blog")
	require.NotContains(t, vars.all(), "sekret-canary-98765")
}
