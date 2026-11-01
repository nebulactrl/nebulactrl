package app_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// varEnv is a project with one environment and a server.
type varEnv struct {
	h                       *harness
	projectID, envID, srvID string
}

func (h *harness) newVarEnv(name string) varEnv {
	h.t.Helper()
	pid := h.createProject(name)
	r := h.do("GET", "/projects/"+pid+"/environments", "", nil)
	require.Equal(h.t, http.StatusOK, r.Status, string(r.Raw))
	envID := r.JSON()["items"].([]any)[0].(map[string]any)["id"].(string)
	r = h.do("POST", "/servers/enrollment-tokens", "", map[string]any{"name": "srv-" + name})
	require.Equal(h.t, http.StatusCreated, r.Status, string(r.Raw))
	return varEnv{h: h, projectID: pid, envID: envID, srvID: r.JSON()["server"].(map[string]any)["id"].(string)}
}

func (e varEnv) service(name, kind string, extra map[string]any) string {
	e.h.t.Helper()
	body := map[string]any{"name": name, "kind": kind, "sourceType": "image", "serverId": e.srvID,
		"config": map[string]any{"image": "traefik/whoami:latest", "port": 80}}
	for k, v := range extra {
		body[k] = v
	}
	r := e.h.do("POST", "/environments/"+e.envID+"/services", "", body)
	require.Equal(e.h.t, http.StatusCreated, r.Status, string(r.Raw))
	return r.JSON()["id"].(string)
}

func (h *harness) put(svc string, vars ...map[string]any) resp {
	h.t.Helper()
	return h.do("PUT", "/services/"+svc+"/variables", "", map[string]any{"variables": vars})
}

func v(key, value string, extra ...any) map[string]any {
	m := map[string]any{"key": key, "value": value}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i].(string)] = extra[i+1]
	}
	return m
}

func item(t *testing.T, r resp, key string) map[string]any {
	t.Helper()
	for _, it := range r.JSON()["items"].([]any) {
		if m := it.(map[string]any); m["key"] == key {
			return m
		}
	}
	t.Fatalf("variable %s not in %s", key, r.Raw)
	return nil
}

// storedValue decrypts a service variable straight from the database.
func (h *harness) storedValue(svc, key string) (value string, version int) {
	h.t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(h.t, err)
	var enc []byte
	require.NoError(h.t, h.app.Pool.QueryRow(context.Background(),
		"SELECT value_enc, version FROM service_variables WHERE service_id = $1 AND key = $2", svc, key).Scan(&enc, &version))
	value, err = box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(svc, key), enc)
	require.NoError(h.t, err)
	return value, version
}

func (h *harness) generation(svc string) int64 {
	var g int64
	require.NoError(h.t, h.app.Pool.QueryRow(context.Background(), "SELECT generation FROM services WHERE id = $1", svc).Scan(&g))
	return g
}

// markDeployed deploys the service (the command stays undelivered: no agent)
// and then flips it to succeeded, as an agent result would.
func (h *harness) markDeployed(svc string) {
	h.t.Helper()
	ctx := context.Background()
	r := h.do("POST", "/services/"+svc+"/deployments", "", map[string]any{})
	require.Equal(h.t, http.StatusAccepted, r.Status, string(r.Raw))
	dep := r.JSON()["id"].(string)
	require.Eventually(h.t, func() bool {
		var rel *uuid.UUID
		_ = h.app.Pool.QueryRow(ctx, "SELECT release_id FROM deployments WHERE id = $1", dep).Scan(&rel)
		return rel != nil
	}, 15*time.Second, 100*time.Millisecond)
	_, err := h.app.Pool.Exec(ctx, "UPDATE deployments SET status = 'succeeded', finished_at = now() WHERE id = $1", dep)
	require.NoError(h.t, err)
	_, err = h.app.Pool.Exec(ctx, "UPDATE services SET current_deployment_id = $1, applied_generation = generation WHERE id = $2", dep, svc)
	require.NoError(h.t, err)
}

func (h *harness) startApp() {
	ctx, cancel := context.WithCancel(context.Background())
	h.t.Cleanup(cancel)
	require.NoError(h.t, h.app.Start(ctx))
}

// Generators run once; the same text keeps the value; secrets are
// never returned nor persisted in commands or logs.
func TestGeneratedSecretIsStableAndNeverLeaks(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.startApp()
	e := h.newVarEnv("gen")
	svc := e.service("api", "app", nil)

	r := h.put(svc, v("KEY", "${{secret(32)}}"))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	it := item(t, r, "KEY")
	require.Equal(t, true, it["isSecret"], "secret generators force isSecret")
	require.Equal(t, true, it["set"])
	require.Nil(t, it["value"])
	require.Equal(t, "generated", it["source"])
	require.Equal(t, "secret", it["generated"].([]any)[0].(map[string]any)["fn"])
	first, ver1 := h.storedValue(svc, "KEY")
	require.Len(t, first, 32)
	require.NotContains(t, first, "${{")
	require.Equal(t, 1, ver1)

	// Replaying the same text (CI, IaC) keeps the value and the version.
	g := h.generation(svc)
	r = h.put(svc, v("KEY", "${{secret(32)}}"))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	again, ver2 := h.storedValue(svc, "KEY")
	require.Equal(t, first, again)
	require.Equal(t, 1, ver2)
	require.Equal(t, g, h.generation(svc), "a no-op write does not stage a change")
	require.NotContains(t, string(r.Raw), first)

	// Another text regenerates.
	h.put(svc, v("KEY", "${{secret(40)}}"))
	second, ver3 := h.storedValue(svc, "KEY")
	require.Len(t, second, 40)
	require.Equal(t, 2, ver3)

	// Deploy: the generated value is in neither the command payload nor the logs.
	r = h.do("POST", "/services/"+svc+"/deployments", "", map[string]any{})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	var payload string
	require.Eventually(t, func() bool {
		return h.app.Pool.QueryRow(context.Background(), "SELECT payload::text FROM agent_commands WHERE server_id = $1 AND kind = 'deploy'", e.srvID).Scan(&payload) == nil
	}, 15*time.Second, 100*time.Millisecond)
	require.NotContains(t, payload, second)
	require.Contains(t, payload, `"KEY"`)
	var logs string
	require.NoError(t, h.app.Pool.QueryRow(context.Background(), "SELECT coalesce(string_agg(line, E'\n'), '') FROM deployment_logs").Scan(&logs))
	require.NotContains(t, logs, second)
	audit := h.do("GET", "/audit-events", "", nil)
	require.NotContains(t, string(audit.Raw), second, "audit never records a secret value")
	require.Contains(t, string(audit.Raw), "fingerprintTo")
	// History keeps fingerprints, no values.
	hist := h.do("GET", "/services/"+svc+"/variables/KEY/history", "", nil)
	require.Equal(t, http.StatusOK, hist.Status, string(hist.Raw))
	require.NotContains(t, string(hist.Raw), second)
	require.Len(t, hist.JSON()["items"], 2)
}

func TestGeneratorValidationAndWarnings(t *testing.T) {
	h := newHarness(t)
	h.setup()
	svc := h.newVarEnv("val").service("api", "app", nil)

	r := h.put(svc, v("A", `${{secret(8,"ab")}}`))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	var codes []string
	for _, w := range r.JSON()["warnings"].([]any) {
		codes = append(codes, w.(map[string]any)["code"].(string))
	}
	require.Contains(t, codes, "weak_secret")

	for _, bad := range []string{`${{secret(0)}}`, `${{secret(2000)}}`, `${{secret(8,"a")}}`, `${{nope()}}`, `${{`, `${{randomInt(5,1)}}`} {
		r = h.put(svc, v("B", bad))
		require.Equal(t, http.StatusUnprocessableEntity, r.Status, bad)
		require.Equal(t, "invalid_template", r.JSON()["code"], bad)
	}
	r = h.put(svc, v("C", "${{secret(32)}}", "isSecret", false))
	require.Equal(t, http.StatusUnprocessableEntity, r.Status)
	require.Equal(t, "secret_required", r.JSON()["code"])

	// Reserved names, escape and non-secret generators.
	require.Equal(t, "reserved_variable", h.put(svc, v("NEBULA_X", "1")).JSON()["code"])
	require.Equal(t, "reserved_variable", h.put(svc, v("PORT", "1")).JSON()["code"])
	r = h.put(svc, v("LIT", "$${{literal}}"), v("U", "${{uuid()}}"), v("N", "${{randomInt(5,5)}}"))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "${{literal}}", h.resolvedPreview(svc, "LIT"))
	require.Equal(t, "5", *strPtr(item(t, r, "N")["value"]))
	require.Len(t, *strPtr(item(t, r, "U")["value"]), 36, "non-secret generator output is readable")
}

func strPtr(v any) *string {
	s, _ := v.(string)
	return &s
}

// resolvedPreview resolves a draft through the validate endpoint.
func (h *harness) resolvedPreview(svc, key string) string {
	var enc []byte
	require.NoError(h.t, h.app.Pool.QueryRow(context.Background(), "SELECT value_enc FROM service_variables WHERE service_id=$1 AND key=$2", svc, key).Scan(&enc))
	box, _ := secretbox.New(bytes.Repeat([]byte{7}, 32))
	val, _ := box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(svc, key), enc)
	r := h.do("POST", "/services/"+svc+"/variables/validate", "", map[string]any{"key": key, "value": val})
	require.Equal(h.t, http.StatusOK, r.Status, string(r.Raw))
	p, _ := r.JSON()["preview"].(string)
	return p
}

func TestCyclesRejectedAtWrite(t *testing.T) {
	h := newHarness(t)
	h.setup()
	e := h.newVarEnv("cyc")
	a, b := e.service("a", "app", nil), e.service("b", "app", nil)

	require.Equal(t, http.StatusOK, h.put(a, v("A", "${{b.X}}")).Status, "an unresolved reference is only a warning")
	r := h.put(b, v("X", "${{a.A}}"))
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "reference_cycle", r.JSON()["code"])
	require.Contains(t, string(r.Raw), "a.A")
	require.Empty(t, h.do("GET", "/services/"+b+"/variables/X/history", "", nil).JSON()["items"], "nothing was written")
}

func TestSelfCycleRejected(t *testing.T) {
	h := newHarness(t)
	h.setup()
	svc := h.newVarEnv("self").service("a", "app", nil)
	r := h.put(svc, v("A", "${{A}}"))
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "reference_cycle", r.JSON()["code"])
	r = h.put(svc, v("A", "x"), v("B", "${{A}}-y"))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
}

func TestProviderChangeRestagesConsumersOnly(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.startApp()
	e := h.newVarEnv("restage")
	db := e.service("postgres", "database", nil)
	api := e.service("api", "app", nil)
	other := e.service("other", "app", nil)
	require.Equal(t, http.StatusOK, h.put(db, v("POSTGRES_PASSWORD", "first-password-123", "isSecret", true)).Status)
	require.Equal(t, http.StatusOK, h.put(api, v("DB_PW", "${{postgres.POSTGRES_PASSWORD}}"), v("PLAIN", "x")).Status)
	require.Equal(t, http.StatusOK, h.put(other, v("Z", "z")).Status)
	h.markDeployed(api)
	h.markDeployed(other)
	apiGen, otherGen := h.generation(api), h.generation(other)
	r := h.do("GET", "/services/"+api+"/pending-changes", "", nil)
	require.Empty(t, r.JSON()["items"], "nothing staged yet")

	// Edit the provider: the consumer is restaged, the unrelated service is not.
	r = h.put(db, v("POSTGRES_PASSWORD", "second-password-456", "isSecret", true))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	restaged := r.JSON()["restaged"].([]any)
	require.Len(t, restaged, 1)
	require.Equal(t, "api", restaged[0].(map[string]any)["slug"])
	require.Equal(t, apiGen+1, h.generation(api))
	require.Equal(t, otherGen, h.generation(other))

	r = h.do("GET", "/services/"+api+"/pending-changes", "", nil)
	items := r.JSON()["items"].([]any)
	require.Len(t, items, 1, string(r.Raw))
	ch := items[0].(map[string]any)
	require.Equal(t, "variables.DB_PW", ch["field"])
	require.Equal(t, "inherited", ch["origin"])
	require.Equal(t, "postgres.POSTGRES_PASSWORD", ch["via"])
	require.Equal(t, true, ch["resolvedChanged"])
	require.NotContains(t, string(r.Raw), "password-")
	// An unrelated provider key restages nobody.
	r = h.put(db, v("OTHER", "x"))
	require.Empty(t, r.JSON()["restaged"])
	// An inherited change cannot be discarded on the consumer.
	d := h.do("DELETE", "/services/"+api+"/pending-changes/variables.DB_PW", "", nil)
	require.Equal(t, http.StatusConflict, d.Status, string(d.Raw))
	require.Equal(t, "inherited_change", d.JSON()["code"])
}

func TestSharedVariablesLayerAndRestage(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.startApp()
	pid := h.createProject("shared")
	// project has production; add staging
	r := h.do("POST", "/projects/"+pid+"/environments", "", map[string]any{"name": "staging"})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	staging := r.JSON()["id"].(string)
	r = h.do("GET", "/projects/"+pid+"/environments", "", nil)
	var prod string
	for _, it := range r.JSON()["items"].([]any) {
		if m := it.(map[string]any); m["slug"] == "production" {
			prod = m["id"].(string)
		}
	}
	srv := h.do("POST", "/servers/enrollment-tokens", "", map[string]any{"name": "s"}).JSON()["server"].(map[string]any)["id"].(string)
	mk := func(env string) string {
		r := h.do("POST", "/environments/"+env+"/services", "", map[string]any{"name": "web", "sourceType": "image", "serverId": srv,
			"config": map[string]any{"image": "traefik/whoami:latest", "port": 80}})
		require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
		return r.JSON()["id"].(string)
	}
	webProd, webStaging := mk(prod), mk(staging)
	require.Equal(t, http.StatusOK, h.put(webProd, v("X", "${{shared.X}}")).Status, "unresolved is a warning")
	require.Equal(t, http.StatusOK, h.put(webStaging, v("X", "${{shared.X}}")).Status)

	r = h.do("PUT", "/projects/"+pid+"/shared-variables", "", map[string]any{"variables": []any{v("X", "from-project")}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Len(t, r.JSON()["restaged"], 2, "a project value restages consumers in every environment")

	r = h.do("PUT", "/environments/"+prod+"/shared-variables", "", map[string]any{"variables": []any{v("X", "from-prod")}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Len(t, r.JSON()["restaged"], 1)
	require.Equal(t, "web", r.JSON()["restaged"].([]any)[0].(map[string]any)["slug"])

	require.Equal(t, "from-prod", h.resolvedPreview(webProd, "X"))
	require.Equal(t, "from-project", h.resolvedPreview(webStaging, "X"))

	// Listing marks the override and the usage.
	r = h.do("GET", "/environments/"+prod+"/shared-variables", "", nil)
	x := item(t, r, "X")
	require.Equal(t, "environment", x["layer"])
	require.Equal(t, true, x["overrides"])
	require.EqualValues(t, 1, x["usedBy"])
	u := h.do("GET", "/environments/"+prod+"/variable-usage?ns=shared&key=X", "", nil)
	require.Equal(t, "web", u.JSON()["items"].([]any)[0].(map[string]any)["slug"])

	// Deleting the env override restages the prod consumer (falls back to the project value).
	genBefore := h.generation(webProd)
	r = h.do("DELETE", "/environments/"+prod+"/shared-variables/X", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Len(t, r.JSON()["restaged"], 1)
	require.Equal(t, genBefore+1, h.generation(webProd))
	require.Equal(t, "from-project", h.resolvedPreview(webProd, "X"))

	// Shared values may not reference services; cycles are rejected.
	r = h.do("PUT", "/projects/"+pid+"/shared-variables", "", map[string]any{"variables": []any{v("BAD", "${{web.X}}")}})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "invalid_shared_reference", r.JSON()["code"])
	require.Equal(t, http.StatusOK, h.do("PUT", "/projects/"+pid+"/shared-variables", "", map[string]any{"variables": []any{v("S1", "${{shared.S2}}")}}).Status)
	r = h.do("PUT", "/projects/"+pid+"/shared-variables", "", map[string]any{"variables": []any{v("S2", "${{shared.S1}}")}})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "reference_cycle", r.JSON()["code"])

	// Share stages the explicit reference row.
	r = h.do("POST", "/environments/"+prod+"/shared-variables/X/share", "", map[string]any{"serviceIds": []string{webProd}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "already_set", r.JSON()["warnings"].([]any)[0].(map[string]any)["code"])
}

// Redaction leaves: URL-encoded and base64 forms of a secret are masked.
func TestRedactionLeavesCoverEncodedForms(t *testing.T) {
	h := newHarness(t)
	h.setup()
	e := h.newVarEnv("red")
	db := e.service("postgres", "database", nil)
	api := e.service("api", "app", nil)
	const pw = "sekret canary/12345@x"
	require.Equal(t, http.StatusOK, h.put(db, v("POSTGRES_PASSWORD", pw, "isSecret", true)).Status)
	require.Equal(t, http.StatusOK, h.put(api,
		v("DATABASE_URL", "postgres://u:${{urlencode(postgres.POSTGRES_PASSWORD)}}@postgres/app"),
		v("BASIC", "${{base64(RAW)}}"), v("RAW", "u:${{postgres.POSTGRES_PASSWORD}}")).Status)

	svc := services.NewService(h.app.Pool, h.app.Bus, h.app.Box, nil, nil)
	apiID := uuid.MustParse(api)
	red, err := svc.RedactorFor(context.Background(), apiID)
	require.NoError(t, err)
	for name, line := range map[string]string{
		"build":   "Step 3/5 : ARG PW=" + pw,
		"runtime": "connecting postgres://u:sekret+canary%2F12345%40x@postgres/app",
		"deploy":  "Authorization: Basic dTpzZWtyZXQgY2FuYXJ5LzEyMzQ1QHg=",
		"bare":    "the password is " + pw,
	} {
		out := red(line)
		require.NotContains(t, out, "canary", name)
		require.NotContains(t, out, "dTpzZWty", name)
		require.Contains(t, out, services.Mask, name)
	}
}

func TestRotationPlanAndEngineBoundBlocker(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.startApp()
	e := h.newVarEnv("rot")
	web := e.service("web", "app", nil)
	worker := e.service("worker", "worker", map[string]any{"config": map[string]any{"image": "alpine:3"}})
	db := e.service("db", "database", nil)
	require.Equal(t, http.StatusOK, h.put(web, v("SESSION_SECRET", "${{secret(32)}}")).Status)
	require.Equal(t, http.StatusOK, h.put(worker, v("S", "${{web.SESSION_SECRET}}")).Status)
	require.Equal(t, http.StatusOK, h.put(db, v("POSTGRES_PASSWORD", "${{secret(32)}}")).Status)
	old, _ := h.storedValue(web, "SESSION_SECRET")
	h.markDeployed(web)
	h.markDeployed(worker)
	var depsBefore int
	require.NoError(t, h.app.Pool.QueryRow(context.Background(), "SELECT count(*) FROM deployments").Scan(&depsBefore))

	// Dry run: a plan, no change.
	r := h.do("POST", "/services/"+web+"/variables/SESSION_SECRET/rotate", "", map[string]any{"dryRun": true})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	plan := r.JSON()
	require.Equal(t, "app-secret", plan["kind"])
	require.Equal(t, false, plan["staged"])
	require.Empty(t, plan["blockers"])
	cur, _ := h.storedValue(web, "SESSION_SECRET")
	require.Equal(t, old, cur)
	var slugs []string
	for _, a := range plan["affected"].([]any) {
		slugs = append(slugs, a.(map[string]any)["slug"].(string))
	}
	require.ElementsMatch(t, []string{"web", "worker"}, slugs)
	require.Equal(t, []any{"web", "worker"}, plan["deployOrder"], "providers first: the worker consumes web's secret")

	// Stage it: new value, consumers restaged, nothing deployed.
	web0, worker0 := h.generation(web), h.generation(worker)
	r = h.do("POST", "/services/"+web+"/variables/SESSION_SECRET/rotate", "", map[string]any{"dryRun": false})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, true, r.JSON()["staged"])
	nw, ver := h.storedValue(web, "SESSION_SECRET")
	require.NotEqual(t, old, nw)
	require.Equal(t, 2, ver)
	require.Equal(t, web0+1, h.generation(web))
	require.Equal(t, worker0+1, h.generation(worker))
	var depsAfter int
	require.NoError(t, h.app.Pool.QueryRow(context.Background(), "SELECT count(*) FROM deployments").Scan(&depsAfter))
	require.Equal(t, depsBefore, depsAfter, "rotation never deploys")

	// A manual (non-generated) variable cannot be rotated.
	h.put(web, v("MANUAL", "x"))
	r = h.do("POST", "/services/"+web+"/variables/MANUAL/rotate", "", map[string]any{"dryRun": true})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status)
	require.Equal(t, "not_rotatable", r.JSON()["code"])

	// A database that was never deployed can still be regenerated; after the first deploy it is blocked.
	r = h.do("POST", "/services/"+db+"/variables/POSTGRES_PASSWORD/rotate", "", map[string]any{"dryRun": false})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	h.markDeployed(db)
	r = h.do("POST", "/services/"+db+"/variables/POSTGRES_PASSWORD/rotate", "", map[string]any{"dryRun": false})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "rotation_unsupported", r.JSON()["code"])
	r = h.do("POST", "/services/"+db+"/variables/POSTGRES_PASSWORD/rotate", "", map[string]any{"dryRun": true})
	require.Equal(t, http.StatusOK, r.Status)
	require.Equal(t, "engine-bound", r.JSON()["kind"])
	require.NotEmpty(t, r.JSON()["blockers"])
}

// Database and .env.example key suggestions.
func TestSuggestionsFromEnvExampleAndDatabases(t *testing.T) {
	h := newHarness(t)
	h.setup()
	prev := services.RegisterExportProvider(func(in services.ExportInput) []services.Export {
		if in.Kind != services.KindDatabase {
			return nil
		}
		return []services.Export{{Key: "DATABASE_URL", Value: "postgres://" + in.Host, Description: "url"}}
	})
	t.Cleanup(func() { services.RegisterExportProvider(prev) })

	e := h.newVarEnv("sug")
	e.service("postgres", "database", nil)
	app := e.service("rails", "app", map[string]any{"sourceType": "git",
		"config": map[string]any{"gitUrl": "https://example.com/acme/rails.git", "port": 3000}})

	// A database nothing references yet is proposed before any deploy.
	r := h.do("GET", "/services/"+app+"/suggested-variables", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Len(t, r.JSON()["items"], 1, string(r.Raw))
	g := r.JSON()["items"].([]any)[0].(map[string]any)
	require.Equal(t, "DATABASE_URL", g["key"])
	require.Equal(t, "${{postgres.DATABASE_URL}}", g["value"])
	require.Equal(t, "database", g["source"])

	// The agent reported .env.example after the first checkout.
	scan := `{"commit":"abc1234","files":[{"path":".env.example","vars":[
	  {"key":"SECRET_KEY_BASE","value":"changeme"},{"key":"DATABASE_URL","value":""},{"key":"REDIS_URL","value":"redis://localhost:6379"},
	  {"key":"APP_NAME","value":"My App","comment":"display name"},{"key":"PORT","value":"3000"},{"key":"NEBULA_X","value":"1"},
	  {"key":"API_TOKEN","value":"your-token-here"}]}]}`
	_, err := h.app.Pool.Exec(context.Background(), "UPDATE services SET suggested_env = $2::jsonb WHERE id = $1", app, scan)
	require.NoError(t, err)
	r = h.do("GET", "/services/"+app+"/suggested-variables", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	byKey := map[string]map[string]any{}
	for _, it := range r.JSON()["items"].([]any) {
		m := it.(map[string]any)
		byKey[m["key"].(string)] = m
	}
	require.Equal(t, "${{secret(64)}}", byKey["SECRET_KEY_BASE"]["value"])
	require.Equal(t, true, byKey["SECRET_KEY_BASE"]["proposedSecret"])
	require.Equal(t, "${{postgres.DATABASE_URL}}", byKey["DATABASE_URL"]["value"])
	require.Equal(t, "env-example", byKey["DATABASE_URL"]["source"], "the example file's entry wins over the generic database proposal")
	require.Equal(t, "${{secret(32)}}", byKey["API_TOKEN"]["value"])
	require.Equal(t, "My App", byKey["APP_NAME"]["value"])
	require.Equal(t, true, byKey["REDIS_URL"]["needsInput"], "a localhost value is not proposed")
	require.NotEmpty(t, byKey["REDIS_URL"]["warnings"])
	require.NotContains(t, byKey, "PORT")
	require.NotContains(t, byKey, "NEBULA_X")

	// Accepting is a normal staged write; the key then disappears from the list.
	require.Equal(t, http.StatusOK, h.put(app, v("SECRET_KEY_BASE", "${{secret(64)}}")).Status)
	r = h.do("GET", "/services/"+app+"/suggested-variables", "", nil)
	for _, it := range r.JSON()["items"].([]any) {
		require.NotEqual(t, "SECRET_KEY_BASE", it.(map[string]any)["key"])
	}
}

// Build-time variables: git services only.
func TestBuildScopeRequiresGitSource(t *testing.T) {
	h := newHarness(t)
	h.setup()
	e := h.newVarEnv("bld")
	img := e.service("img", "app", nil)
	r := h.put(img, v("ARG", "1", "scope", "build"))
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "build_scope_requires_git", r.JSON()["code"])

	gitSvc := e.service("src", "app", map[string]any{"sourceType": "git", "config": map[string]any{"gitUrl": "https://example.com/a/b.git", "port": 80}})
	r = h.put(gitSvc, v("ARG", "1", "scope", "build"), v("TOKEN", "${{secret(24)}}", "scope", "both"), v("RUN", "x"))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "build", item(t, r, "ARG")["scope"])
	require.Equal(t, "both", item(t, r, "TOKEN")["scope"])
	require.Equal(t, "runtime", item(t, r, "RUN")["scope"])
	// A scope change alone is a staged change with its own field.
	r = h.do("POST", "/services/"+gitSvc+"/variables/validate", "", map[string]any{"key": "X", "value": "${{nope.KEY}}"})
	require.Equal(t, http.StatusOK, r.Status)
	require.Equal(t, true, r.JSON()["ok"])
	require.Equal(t, "unresolved_reference", r.JSON()["warnings"].([]any)[0].(map[string]any)["code"])
	r = h.do("POST", "/services/"+gitSvc+"/variables/validate", "", map[string]any{"key": "X", "value": "${{secret(0)}}"})
	require.Equal(t, false, r.JSON()["ok"])
	require.NotNil(t, r.JSON()["error"])
}

// Restore, history and the keys-only audit trail.
func TestHistoryRestoreAndAudit(t *testing.T) {
	h := newHarness(t)
	h.setup()
	svc := h.newVarEnv("hist").service("api", "app", nil)
	h.put(svc, v("MODE", "one"))
	h.put(svc, v("MODE", "two"))
	h.put(svc, v("TOKEN", "tok-value-1234", "isSecret", true))
	r := h.do("GET", "/services/"+svc+"/variables/MODE/history", "", nil)
	items := r.JSON()["items"].([]any)
	require.Len(t, items, 2)
	require.Equal(t, "two", items[0].(map[string]any)["value"])
	r = h.do("POST", "/services/"+svc+"/variables/MODE/history/1/restore", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "one", *strPtr(item(t, r, "MODE")["value"]))
	require.EqualValues(t, 3, item(t, r, "MODE")["version"])
	r = h.do("GET", "/services/"+svc+"/variables/TOKEN/history", "", nil)
	require.NotContains(t, string(r.Raw), "tok-value-1234")
	r = h.do("POST", "/services/"+svc+"/variables/TOKEN/history/1/restore", "", nil)
	require.Equal(t, http.StatusUnprocessableEntity, r.Status)
	require.Equal(t, "restore_unsupported", r.JSON()["code"])
	a := h.do("GET", "/audit-events", "", nil)
	require.NotContains(t, string(a.Raw), "tok-value-1234")
	require.True(t, strings.Contains(string(a.Raw), "variable.set"))
	require.Equal(t, http.StatusNoContent, h.do("DELETE", "/services/"+svc+"/variables/MODE", "", nil).Status)
	r = h.do("GET", "/services/"+svc+"/variables/MODE/history", "", nil)
	require.Equal(t, "delete", r.JSON()["items"].([]any)[0].(map[string]any)["op"])
}

// IDOR: a token restricted to project A cannot touch B's variables.
func TestVariablesIDOR(t *testing.T) {
	h := newHarness(t)
	h.setup()
	a, b := h.newVarEnv("a"), h.newVarEnv("b")
	svcB := b.service("api", "app", nil)
	tok := h.createToken([]string{"projects:read", "projects:write"}, []string{a.projectID})
	for _, req := range [][]string{
		{"GET", "/services/" + svcB + "/variables"}, {"PUT", "/services/" + svcB + "/variables"}, {"GET", "/services/" + svcB + "/suggested-variables"},
		{"GET", "/projects/" + b.projectID + "/shared-variables"}, {"PUT", "/projects/" + b.projectID + "/shared-variables"},
		{"GET", "/environments/" + b.envID + "/shared-variables"},
	} {
		var body any
		if req[0] == "PUT" {
			body = map[string]any{"variables": []any{v("K", "v")}}
		}
		r := h.do(req[0], req[1], tok, body)
		require.Equal(t, http.StatusNotFound, r.Status, fmt.Sprintf("%v: %s", req, r.Raw))
	}
	ro := h.createToken([]string{"projects:read"}, nil)
	r := h.do("PUT", "/projects/"+a.projectID+"/shared-variables", ro, map[string]any{"variables": []any{v("K", "v")}})
	require.Equal(t, http.StatusForbidden, r.Status)
}
