package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	cfToken    = "7|coolify-cli-token-canary"
	cfPassword = "db-pass-canary-4242"
	cfSecret   = "app-secret-canary-777"
	cfLocked   = "locked-canary-value-1"
	cfGHCR     = "ghcr-canary-token-9"
	cfAppUUID  = "webapp0000000000000000aa"
	cfDBUUID   = "shopdb000000000000000bbb"
)

// fakeCoolifyServer is a tiny Coolify v4 API: one project with a web application and a Postgres database.
func fakeCoolifyServer(t *testing.T) *httptest.Server {
	t.Helper()
	app := map[string]any{
		"id": 1, "uuid": cfAppUUID, "name": "web", "environment_id": 1, "build_pack": "dockerimage", "docker_registry_image_name": "ghcr.io/acme/web",
		"docker_registry_image_tag": "1.4.0", "ports_exposes": "3000", "fqdn": "https://shop.example.com", "health_check_enabled": true, "health_check_path": "/up",
		"health_check_interval": 5, "health_check_timeout": 5, "health_check_retries": 5, "custom_docker_run_options": `--entrypoint "node server.js --port 3000"`,
		"limits_memory": "0", "limits_cpus": "0", "limits_cpu_shares": 1024,
	}
	db := map[string]any{
		"uuid": cfDBUUID, "name": "shop-db", "database_type": "standalone-postgresql", "image": "postgres:16-alpine", "environment_id": 1,
		"postgres_user": "shop", "postgres_password": cfPassword, "postgres_db": "shop",
		"internal_db_url": "postgres://shop:" + cfPassword + "@" + cfDBUUID + ":5432/shop",
	}
	env := func(k, v string, locked bool) map[string]any {
		e := map[string]any{"key": k, "value": v, "real_value": v, "is_runtime": true, "is_buildtime": false}
		if locked {
			e["is_shown_once"] = true
			delete(e, "value")
			delete(e, "real_value")
		}
		return e
	}
	envs := []map[string]any{
		env("DB_HOST", cfDBUUID, false), env("DB_PASSWORD", cfPassword, false), env("APP_SECRET", cfSecret, false),
		env("APP_ENV", "production", false), env("LOCKED_TOKEN", "", true),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+cfToken {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch p := strings.TrimPrefix(r.URL.Path, "/api/v1"); p {
		case "/version":
			_, _ = w.Write([]byte("4.5.0"))
		case "/projects":
			reply([]map[string]any{{"id": 1, "uuid": "proj-shop", "name": "Shop"}})
		case "/projects/proj-shop/environments":
			reply([]map[string]any{{"id": 1, "name": "production", "project_id": 1}})
		case "/applications":
			reply([]map[string]any{app})
		case "/applications/" + cfAppUUID:
			reply(app)
		case "/applications/" + cfAppUUID + "/envs":
			reply(envs)
		case "/databases":
			reply([]map[string]any{db})
		case "/databases/" + cfDBUUID:
			reply(db)
		case "/services", "/servers", "/applications/" + cfAppUUID + "/scheduled-tasks":
			reply([]any{})
		case "/applications/" + cfAppUUID + "/storages", "/databases/" + cfDBUUID + "/storages":
			reply(map[string]any{"persistent_storages": []any{}, "file_storages": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestImportCoolifyCLI(t *testing.T) {
	cf := fakeCoolifyServer(t)
	e := newEnv(t)
	e.login()
	e.mustRun("", "servers", "enroll-token", "--name", "box1")
	t.Setenv("COOLIFY_TOKEN", cfToken)
	t.Setenv("COOLIFY_URL", "")
	t.Setenv("LOCKED_VAL", cfLocked)
	t.Setenv("GHCR_TOKEN", cfGHCR)
	canaries := []string{cfToken, cfPassword, cfSecret, cfLocked, cfGHCR}
	noLeak := func(r result) {
		t.Helper()
		for _, c := range canaries {
			require.NotContains(t, r.all(), c)
		}
	}

	// Usage errors.
	require.Equal(t, 2, e.run("", "import", "coolify", "--plan").code, "--url is required")
	require.Equal(t, 2, e.run("", "import", "coolify", "--url", cf.URL).code, "--server is required to import")
	t.Setenv("COOLIFY_TOKEN", "")
	missing := e.run("", "import", "coolify", "--url", cf.URL, "--plan")
	require.Equal(t, 2, missing.code)
	require.Contains(t, missing.err, "read:sensitive")
	t.Setenv("COOLIFY_TOKEN", cfToken)

	// A wrong token is refused without echoing it.
	bad := e.run("", "import", "coolify", "--url", cf.URL, "--plan", "--token", "wrong-canary-token")
	require.Equal(t, 1, bad.code)
	require.Contains(t, bad.err, "Coolify refused the token")
	require.NotContains(t, bad.all(), "wrong-canary-token")

	// Plan: nothing is written, values are never printed.
	plan := e.mustRun("", "import", "coolify", "--url", cf.URL, "--plan")
	noLeak(plan)
	require.Contains(t, plan.stdout, "Project Shop → shop")
	require.Contains(t, plan.stdout, "managed postgres 16")
	require.Contains(t, plan.stdout, "ghcr.io/acme/web:1.4.0")
	require.Contains(t, plan.stdout, "shop.example.com")
	require.Contains(t, plan.stdout, "APP_SECRET*", "secrets are marked, by key")
	require.Contains(t, plan.stdout, "DB_HOST→")
	require.Contains(t, plan.stdout, "DB_PASSWORD*→")
	require.Contains(t, plan.stdout, "LOCKED_TOKEN?")
	require.Contains(t, plan.stdout, "${{shop-db.NEBULA_PRIVATE_HOST}}")
	require.Contains(t, plan.stdout, "ghcr.io")
	require.Contains(t, plan.stdout, "nebulactrl databases restore shop-db --file")
	require.Contains(t, plan.stdout, "Alpine")
	projects := decode[[]map[string]any](t, e.mustRun("", "--json", "projects", "ls").stdout)
	require.Empty(t, projects)

	dry := e.mustRun("", "--json", "import", "coolify", "--url", cf.URL, "--dry-run")
	noLeak(dry)
	js := decode[map[string]any](t, dry.stdout)
	require.Equal(t, true, js["installable"])
	require.Equal(t, "4.5.0", js["coolifyVersion"])

	// Unknown resources and bad values are usage errors.
	require.Equal(t, 2, e.run("", "import", "coolify", "--url", cf.URL, "--plan", "--exclude", "nope").code)
	require.Equal(t, 2, e.run("", "import", "coolify", "--url", cf.URL, "--plan", "--port", "web=abc").code)
	require.Equal(t, 2, e.run("", "import", "coolify", "--url", cf.URL, "--plan", "--secret", "web").code)
	require.Equal(t, 2, e.run("", "import", "coolify", "--url", cf.URL, "--plan", "--registry-user", "ghcr.io=acme").code, "a user needs a token")

	// Choices change the plan.
	excluded := e.mustRun("", "import", "coolify", "--url", cf.URL, "--plan", "--exclude", "shop-db", "--not-secret", "web.APP_ENV", "--rename-project", "Shop=Storefront")
	require.Contains(t, excluded.stdout, "Project Storefront → storefront")
	require.Contains(t, excluded.stdout, "Not imported:")
	require.Contains(t, excluded.stdout, "Left out in the import")
	e.mustRun("", "projects", "ls")

	// Import: everything is staged.
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte(cfToken+"\n"), 0o600))
	t.Setenv("COOLIFY_TOKEN", "")
	out := e.mustRun("", "import", "coolify", "--url", cf.URL, "--token-file", tokenFile, "--server", "box1",
		"--registry-user", "ghcr.io=acme", "--registry-token-env", "ghcr.io=GHCR_TOKEN", "--set-env", "web.LOCKED_TOKEN=LOCKED_VAL")
	noLeak(out)
	require.Contains(t, out.stdout, "Imported 1 project(s) and 2 service(s)")
	require.Contains(t, out.stdout, "shop/production/web")
	require.Contains(t, out.stdout, "nothing is deployed")
	require.Contains(t, out.stdout, "nebulactrl databases restore shop-db --file")

	svcs := decode[[]map[string]any](t, e.mustRun("", "--json", "services", "ls", "--project", "shop", "--env", "production").stdout)
	require.Len(t, svcs, 2)
	for _, s := range svcs {
		require.Empty(t, s["currentDeploymentId"], "staged, not deployed")
	}
	vars := e.mustRun("", "vars", "ls", "shop/production/web")
	noLeak(vars)
	require.Contains(t, vars.stdout, "LOCKED_TOKEN")
	require.Contains(t, vars.stdout, "DB_HOST")
	require.Contains(t, vars.stdout, "${{shop-db.NEBULA_PRIVATE_HOST}}")

	// A second run does not overwrite the first: the project name is taken and gets a suffix.
	again := e.mustRun("", "import", "coolify", "--url", cf.URL, "--token-file", tokenFile, "--server", "box1")
	require.Contains(t, again.stdout, "shop-coolify/production/web")
	projects = decode[[]map[string]any](t, e.mustRun("", "--json", "projects", "ls").stdout)
	require.Len(t, projects, 2)
}
