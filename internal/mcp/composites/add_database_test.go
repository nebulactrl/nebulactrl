package composites_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/variables"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const addDatabaseCanary = "sekret-canary-12345"

// addDatabaseFx is newFx with the database export provider registered (the
// app's wiring does this at startup) and a canary secret on the app.
func addDatabaseFx(t *testing.T) (*fx, db.Service) {
	t.Helper()
	f := newFx(t)
	databases.RegisterExports()
	t.Cleanup(func() { services.RegisterExportProvider(nil) })
	app := f.service(svcOpts{env: f.staging, slug: "web"})
	yes := true
	canary := addDatabaseCanary
	_, err := f.deps.Variables.Put(f.ctx(f.admin), app.ID, []variables.VariableInput{{Key: "API_KEY", Value: &canary, IsSecret: &yes}})
	require.NoError(t, err)
	return f, app
}

func addDatabaseOnlineServer(f *fx, name string) db.Server {
	f.t.Helper()
	bg := context.Background()
	srv, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.ws.ID, Name: name})
	require.NoError(f.t, err)
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapProxyV2}})
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online', facts = $2, last_seen_at = now() WHERE id = $1", srv.ID, facts)
	require.NoError(f.t, err)
	srv, err = f.q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID})
	require.NoError(f.t, err)
	return srv
}

func addDatabaseCount(f *fx, query string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func addDatabaseObj(m map[string]any, key string) map[string]any {
	o, _ := m[key].(map[string]any)
	return o
}

func addDatabaseJSON(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func addDatabaseVarStatus(m map[string]any) map[string]string {
	out := map[string]string{}
	vars, _ := addDatabaseObj(m, "app")["variables"].([]any)
	for _, v := range vars {
		o := v.(map[string]any)
		out[o["key"].(string)] = o["status"].(string)
	}
	return out
}

func addDatabaseNextTool(m map[string]any, tool string) map[string]any {
	next, _ := m["nextTools"].([]any)
	for _, n := range next {
		if o := n.(map[string]any); o["tool"] == tool {
			return o
		}
	}
	return nil
}

func TestAddDatabaseHappyPathStagesDatabaseVariablesAndLink(t *testing.T) {
	f, app := addDatabaseFx(t)
	appBefore := app.Generation

	m := f.must("add_database", map[string]any{"environment": "shop/staging", "engine": "postgres", "app": "shop/staging/web", "detail": "full"})
	require.Empty(t, sectionErrors(m), addDatabaseJSON(m))

	d := addDatabaseObj(m, "database")
	require.Equal(t, "shop/staging/postgres", d["selector"])
	require.Equal(t, "postgres", d["slug"])
	require.Equal(t, "postgres", d["engine"])
	require.NotEmpty(t, d["version"])
	require.EqualValues(t, 5432, d["port"])
	require.Contains(t, d["volume"], "data:")
	require.Equal(t, "agent1", d["server"], "the database defaults to the app's server")
	require.Equal(t, true, d["staged"])
	require.Equal(t, false, m["resumed"])

	// The service is staged: generation ahead of the applied one, never deployed.
	dbID := d["id"].(string)
	row, err := f.reload(uuid.MustParse(dbID))
	require.NoError(t, err)
	require.Greater(t, row.Generation, row.AppliedGeneration)
	require.Nil(t, row.CurrentDeploymentID)

	// Link edge app -> database.
	require.Equal(t, 1, addDatabaseCount(f, "SELECT count(*) FROM service_links WHERE from_service_id = $1 AND to_service_id = $2", app.ID, row.ID))
	require.NotEmpty(t, addDatabaseObj(m, "app")["linkId"])

	// Variable keys on the app, staged as references; the app's generation moved.
	require.Equal(t, map[string]string{"DATABASE_HOST": "staged", "DATABASE_PORT": "staged", "DATABASE_URL": "staged"}, addDatabaseVarStatus(m))
	list, err := f.deps.Variables.List(f.ctx(f.admin), app.ID)
	require.NoError(t, err)
	have := map[string]bool{}
	for _, v := range list.Items {
		have[v.Key] = true
	}
	require.True(t, have["DATABASE_HOST"] && have["DATABASE_PORT"] && have["DATABASE_URL"], "%v", have)
	appAfter, err := f.reload(app.ID)
	require.NoError(t, err)
	require.Greater(t, appAfter.Generation, appBefore)
	require.EqualValues(t, appAfter.Generation, addDatabaseObj(m, "app")["generation"])

	// Export catalog (full): references only, never values.
	exports, _ := m["exports"].([]any)
	require.NotEmpty(t, exports)

	// No secret material anywhere.
	out := addDatabaseJSON(m)
	require.NotContains(t, out, addDatabaseCanary)
	require.NotContains(t, out, "postgres://")
	require.NotContains(t, out, "PASSWORD\":\"")

	// Next steps: review, then apply with the expected generations.
	require.NotNil(t, addDatabaseNextTool(m, "review_changes"))
	apply := addDatabaseNextTool(m, "apply_changes")
	require.NotNil(t, apply)
	svcs := apply["args"].(map[string]any)["services"].([]any)
	require.Len(t, svcs, 2)
	require.Equal(t, "shop/staging/postgres", svcs[0].(map[string]any)["service"], "database first")
	require.EqualValues(t, row.Generation, svcs[0].(map[string]any)["expectedGeneration"])
	require.Equal(t, "shop/staging/web", svcs[1].(map[string]any)["service"])
	require.EqualValues(t, appAfter.Generation, svcs[1].(map[string]any)["expectedGeneration"])
}

func TestAddDatabaseCustomPrefixAndRedisDefaults(t *testing.T) {
	f, app := addDatabaseFx(t)
	m := f.must("add_database", map[string]any{"environment": "shop/staging", "engine": "redis", "acceptLicense": true, "app": "web"})
	require.Empty(t, sectionErrors(m), addDatabaseJSON(m))
	require.Equal(t, map[string]string{"REDIS_HOST": "staged", "REDIS_PORT": "staged", "REDIS_URL": "staged"}, addDatabaseVarStatus(m))

	m = f.must("add_database", map[string]any{"environment": "shop/staging", "engine": "mysql", "name": "billing", "prefix": "billing_db", "app": "web"})
	require.Equal(t, map[string]string{"BILLING_DB_HOST": "staged", "BILLING_DB_PORT": "staged", "BILLING_DB_URL": "staged"}, addDatabaseVarStatus(m))
	require.Equal(t, "billing", addDatabaseObj(m, "database")["slug"])
	_ = app
}

func TestAddDatabaseWithoutAppOnlyCreatesTheDatabase(t *testing.T) {
	f, _ := addDatabaseFx(t)
	m := f.must("add_database", map[string]any{"environment": "shop/staging", "engine": "mariadb", "version": "", "name": "Orders DB"})
	require.Empty(t, sectionErrors(m))
	require.Equal(t, "orders-db", addDatabaseObj(m, "database")["slug"])
	require.Empty(t, addDatabaseObj(m, "app")["selector"])
	require.Equal(t, 0, addDatabaseCount(f, "SELECT count(*) FROM service_links"))
	require.NotNil(t, addDatabaseNextTool(m, "variable_set"))
	require.Equal(t, []any{}, addDatabaseObj(m, "app")["variables"])
}

func TestAddDatabaseWarnsWhenPlacedOnAnotherServer(t *testing.T) {
	f, _ := addDatabaseFx(t)
	other := addDatabaseOnlineServer(f, "agent2")
	m := f.must("add_database", map[string]any{"environment": "shop/staging", "engine": "postgres", "app": "web", "server": other.Name})
	require.Empty(t, sectionErrors(m), addDatabaseJSON(m))
	require.Equal(t, "agent2", addDatabaseObj(m, "database")["server"])
	warns, _ := m["warnings"].([]any)
	var joined string
	for _, w := range warns {
		joined += w.(string) + "\n"
	}
	require.Contains(t, joined, services.CrossServerWarning)
	require.Contains(t, joined, "agent1")
	require.Contains(t, joined, "agent2")
	// Exactly one copy of the warning (the link's own warning is deduplicated).
	require.Equal(t, 1, strings.Count(joined, services.CrossServerWarning))
}

func TestAddDatabaseSameNameIsAConflictWithTheExistingSelector(t *testing.T) {
	f, _ := addDatabaseFx(t)
	f.must("add_database", map[string]any{"environment": "shop/staging", "engine": "postgres"})
	_, err := f.call(f.admin, "add_database", map[string]any{"environment": "shop/staging", "engine": "postgres"})
	require.Equal(t, "conflict", errCode(err))
	require.Contains(t, err.Error(), "shop/staging/postgres")
	require.Equal(t, 1, addDatabaseCount(f, "SELECT count(*) FROM services WHERE slug LIKE 'postgres%'"))
	// A user service of that name conflicts too.
	f.service(svcOpts{env: f.staging, slug: "cache"})
	_, err = f.call(f.admin, "add_database", map[string]any{"environment": "shop/staging", "engine": "redis", "acceptLicense": true, "name": "cache"})
	require.Equal(t, "conflict", errCode(err))
}

func TestAddDatabaseArgumentErrors(t *testing.T) {
	f, _ := addDatabaseFx(t)
	for name, args := range map[string]map[string]any{
		"no environment":      {"engine": "postgres"},
		"no engine":           {"environment": "shop/staging"},
		"unknown engine":      {"environment": "shop/staging", "engine": "oracle"},
		"unknown version":     {"environment": "shop/staging", "engine": "postgres", "version": "1"},
		"bad prefix":          {"environment": "shop/staging", "engine": "postgres", "prefix": "no spaces!"},
		"bad detail":          {"environment": "shop/staging", "engine": "postgres", "detail": "verbose"},
		"unknown environment": {"environment": "shop/nope", "engine": "postgres"},
	} {
		_, err := f.call(f.admin, "add_database", args)
		require.Error(t, err, name)
		if name == "unknown environment" {
			require.Equal(t, "not_found", errCode(err), name)
		} else {
			require.Equal(t, "invalid_argument", errCode(err), name)
		}
	}
	require.Equal(t, 0, addDatabaseCount(f, "SELECT count(*) FROM services WHERE kind = 'database'"))

	_, err := f.call(f.admin, "add_database", map[string]any{"environment": "shop/staging", "engine": "redis"})
	require.Equal(t, "license_not_accepted", errCode(err))
	_, err = f.call(f.admin, "add_database", map[string]any{"environment": "shop/staging", "engine": "postgres", "server": "ghost"})
	require.Equal(t, "invalid_argument", errCode(err))
}

func TestAddDatabaseAmbiguousAppSelector(t *testing.T) {
	f, _ := addDatabaseFx(t)
	f.service(svcOpts{env: f.prod, slug: "web"})
	_, err := f.call(f.admin, "add_database", map[string]any{"environment": "shop/staging", "engine": "postgres", "app": "web"})
	require.Equal(t, "ambiguous_selector", errCode(err))
	require.Equal(t, 0, addDatabaseCount(f, "SELECT count(*) FROM services WHERE kind = 'database'"))
}

// An app of another environment is refused before anything is created.
func TestAddDatabaseAppInAnotherEnvironmentCreatesNothing(t *testing.T) {
	f, _ := addDatabaseFx(t)
	f.service(svcOpts{env: f.prod, slug: "api"})
	_, err := f.call(f.admin, "add_database", map[string]any{"environment": "shop/staging", "engine": "postgres", "app": "shop/production/api"})
	require.Equal(t, "invalid_argument", errCode(err))
	require.Contains(t, err.Error(), "Nothing was created")
	require.Equal(t, 0, addDatabaseCount(f, "SELECT count(*) FROM services WHERE kind = 'database'"))
}

// When the link fails after the database exists, the database stays staged and
// the result says what exists and what failed.
func TestAddDatabasePartialFailureKeepsStagedDatabase(t *testing.T) {
	f, app := addDatabaseFx(t)
	bg := context.Background()
	_, err := f.pool.Exec(bg, `CREATE FUNCTION add_database_test_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'boom'; END $$`)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, `CREATE TRIGGER add_database_test_fail BEFORE INSERT ON service_links FOR EACH ROW EXECUTE FUNCTION add_database_test_fail()`)
	require.NoError(t, err)

	m := f.must("add_database", map[string]any{"environment": "shop/staging", "engine": "postgres", "app": "web"})
	require.Equal(t, []string{"link"}, sectionErrors(m), addDatabaseJSON(m))
	d := addDatabaseObj(m, "database")
	require.Equal(t, true, d["staged"])
	require.Equal(t, 1, addDatabaseCount(f, "SELECT count(*) FROM services WHERE kind = 'database'"))
	require.Equal(t, map[string]string{"DATABASE_HOST": "staged", "DATABASE_PORT": "staged", "DATABASE_URL": "staged"}, addDatabaseVarStatus(m))
	require.Empty(t, addDatabaseObj(m, "app")["linkId"])
	require.Contains(t, m["summary"], "stays staged")
	require.Equal(t, 0, addDatabaseCount(f, "SELECT count(*) FROM service_links WHERE from_service_id = $1", app.ID))
	require.NotContains(t, addDatabaseJSON(m), "boom", "internal error text must not leak")
}

func TestAddDatabaseDoesNotOverwriteExistingKeys(t *testing.T) {
	f, app := addDatabaseFx(t)
	lit := "literal-host"
	_, err := f.deps.Variables.Put(f.ctx(f.admin), app.ID, []variables.VariableInput{{Key: "DATABASE_HOST", Value: &lit}})
	require.NoError(t, err)

	m := f.must("add_database", map[string]any{"environment": "shop/staging", "engine": "postgres", "app": "web"})
	st := addDatabaseVarStatus(m)
	require.Equal(t, "skipped", st["DATABASE_HOST"])
	require.Equal(t, "staged", st["DATABASE_URL"])
	warns, _ := m["warnings"].([]any)
	require.NotEmpty(t, warns)
	list, err := f.deps.Variables.List(f.ctx(f.admin), app.ID)
	require.NoError(t, err)
	for _, v := range list.Items {
		if v.Key == "DATABASE_HOST" {
			require.Equal(t, lit, *v.Value)
		}
	}
}

func TestAddDatabaseNeedsProjectsWriteOnTheServices(t *testing.T) {
	f, _ := addDatabaseFx(t)
	ro := f.principal(policy.ScopeProjectsRead)
	tl := f.tools["add_database"]
	require.Equal(t, []string{policy.ScopeProjectsWrite}, tl.Scopes)
	raw, _ := json.Marshal(map[string]any{"environment": "shop/staging", "engine": "postgres"})
	_, err := tl.Call(f.ctx(ro), ro, raw)
	require.Error(t, err, "the domain service re-checks the scope")
	require.Equal(t, 0, addDatabaseCount(f, "SELECT count(*) FROM services WHERE kind = 'database'"))
}

func TestAddDatabaseDeployStartsOnlyTheDatabase(t *testing.T) {
	f, app := addDatabaseFx(t)
	m := f.must("add_database", map[string]any{"environment": "shop/staging", "engine": "postgres", "app": "web", "deploy": true})
	require.Empty(t, sectionErrors(m), addDatabaseJSON(m))
	d := addDatabaseObj(m, "database")
	require.NotEmpty(t, d["deploymentId"])
	require.Equal(t, 1, addDatabaseCount(f, "SELECT count(*) FROM deployments WHERE service_id = $1::uuid", d["id"]))
	require.Equal(t, 1, addDatabaseCount(f, "SELECT count(*) FROM deployments WHERE service_id = $1", app.ID), "the app keeps only its seeded deployment")
	apply := addDatabaseNextTool(m, "apply_changes")
	require.NotNil(t, apply)
	svcs := apply["args"].(map[string]any)["services"].([]any)
	require.Len(t, svcs, 1, "only the app is left to apply")
	require.Equal(t, "shop/staging/web", svcs[0].(map[string]any)["service"])
}

// On production the deploy needs approval: the error is returned unchanged
// (never an errors[] entry) and the retry continues with the staged database.
func TestAddDatabaseDeployOnProductionSurfacesApprovalAndResumes(t *testing.T) {
	f, _ := addDatabaseFx(t)
	prodguard.New(prodguard.Deps{Pool: f.pool, Box: f.box}).Install()
	t.Cleanup(prodguardtest.Uninstall)
	f.service(svcOpts{env: f.prod, slug: "api"})
	args := map[string]any{"environment": "shop/production", "engine": "postgres", "app": "api", "deploy": true}
	for i := range 2 {
		_, err := f.call(f.admin, "add_database", args)
		var ar *prodguard.ErrApprovalRequired
		require.True(t, errors.As(err, &ar), "call %d: %v", i, err)
	}
	require.Equal(t, 1, addDatabaseCount(f, "SELECT count(*) FROM services WHERE kind = 'database'"), "the retry reuses the staged database")
	require.Equal(t, 0, addDatabaseCount(f, "SELECT count(*) FROM deployments d JOIN services s ON s.id = d.service_id WHERE s.kind = 'database'"))
	// Without deploy a production environment stages like any other.
	m := f.must("add_database", map[string]any{"environment": "shop/production", "engine": "postgres", "name": "reports"})
	require.Empty(t, sectionErrors(m))
	warns, _ := m["warnings"].([]any)
	require.Contains(t, warns[0], "production")
}
