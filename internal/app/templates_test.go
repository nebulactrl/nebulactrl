package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/templates"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// templateServer returns an online amd64 server with a base domain.
func (h *harness) templateServer(name string) string {
	h.t.Helper()
	id := h.newOnlineServer(name, 16*gib, 2*gib)
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Arch: "amd64", CPUs: 8, Capabilities: []string{agentprotocol.CapProxyV2}, MemTotal: uint64(16 * gib), DiskTotal: uint64(200 * gib)})
	_, err := h.app.Pool.Exec(context.Background(),
		`UPDATE servers SET facts=$2, base_domain='apps.example.test', public_ip='203.0.113.7', proxy_status='ok' WHERE id=$1`, uuid.MustParse(id), facts)
	require.NoError(h.t, err)
	return id
}

func TestCatalogListAndGet(t *testing.T) {
	h := newHarness(t)
	h.setup()

	r := h.do("GET", "/catalog/templates", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	items := r.JSON()["items"].([]any)
	require.GreaterOrEqual(t, len(items), 25)
	cats := r.JSON()["categories"].([]any)
	require.Len(t, cats, len(templates.Categories))
	for _, c := range cats {
		require.Positive(t, c.(map[string]any)["count"], c)
	}

	r = h.do("GET", "/catalog/templates?q=minecraft&category=games", "", nil)
	require.Equal(t, http.StatusOK, r.Status)
	require.NotEmpty(t, r.JSON()["items"])

	r = h.do("GET", "/catalog/templates/umami", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "umami", r.JSON()["id"])

	require.Equal(t, http.StatusNotFound, h.do("GET", "/catalog/templates/nope", "", nil).Status)
}

func TestTemplateInstallUmami(t *testing.T) {
	h := newHarness(t)
	h.setup()
	envID := h.newEnv("Tpl")
	srv := h.templateServer("tpl-host")

	// Plan: nothing is written; secrets are listed by name only.
	r := h.do("POST", "/environments/"+envID+"/templates/umami/plan", "", map[string]any{"serverId": srv})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	plan := r.JSON()
	require.Equal(t, true, plan["installable"], string(r.Raw))
	require.Contains(t, string(r.Raw), "app.APP_SECRET")
	require.Len(t, plan["services"], 2)
	require.Empty(t, h.do("GET", "/environments/"+envID+"/services", "", nil).JSON()["items"])

	// Install (staged).
	r = h.do("POST", "/environments/"+envID+"/templates/umami/instances", "", map[string]any{"serverId": srv}, "Idempotency-Key", "tpl-1")
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	res := r.JSON()
	svcs := res["services"].([]any)
	require.Len(t, svcs, 2)
	byKey := map[string]map[string]any{}
	for _, s := range svcs {
		m := s.(map[string]any)
		byKey[m["name"].(string)] = m
	}
	app, db := byKey["Umami"], byKey["Umami DB"]
	require.NotNil(t, app)
	require.NotNil(t, db)
	require.Equal(t, "database", db["kind"])
	require.Equal(t, true, app["hasStagedChanges"])
	require.Contains(t, app["config"].(map[string]any)["image"], "@sha256:")
	require.Len(t, app["domains"], 1)
	require.True(t, strings.HasSuffix(app["domains"].([]any)[0].(map[string]any)["hostname"].(string), ".apps.example.test"))

	// An idempotent replay returns the identical body and creates nothing new.
	again := h.do("POST", "/environments/"+envID+"/templates/umami/instances", "", map[string]any{"serverId": srv}, "Idempotency-Key", "tpl-1")
	require.Equal(t, http.StatusCreated, again.Status)
	require.JSONEq(t, string(r.Raw), string(again.Raw))
	require.Len(t, h.do("GET", "/environments/"+envID+"/services", "", nil).JSON()["items"], 2)

	// Variables: the reference is plain, the generated secret is never readable.
	vars := h.variables(app["id"].(string))
	require.Contains(t, vars["DATABASE_URL"]["value"], "${{")
	require.Equal(t, true, vars["APP_SECRET"]["isSecret"])
	require.Nil(t, vars["APP_SECRET"]["value"])

	// The dependsOn link and the canvas group exist.
	r = h.do("GET", "/environments/"+envID+"/links", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.NotEmpty(t, r.JSON()["items"])

	// The stack is listed and deletes as a unit.
	r = h.do("GET", "/environments/"+envID+"/template-instances", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	var insts []map[string]any
	require.NoError(t, json.Unmarshal(r.Raw, &insts))
	require.Len(t, insts, 1)
	id := insts[0]["id"].(string)
	require.Equal(t, http.StatusUnprocessableEntity, h.do("DELETE", "/template-instances/"+id+"?confirm=wrong", "", nil).Status)
	require.Equal(t, http.StatusNoContent, h.do("DELETE", "/template-instances/"+id+"?confirm=umami", "", nil).Status)
	require.Empty(t, h.do("GET", "/environments/"+envID+"/services", "", nil).JSON()["items"])
}

func TestTemplateInstallGameServerPublishesPorts(t *testing.T) {
	h := newHarness(t)
	h.setup()
	envID := h.newEnv("Games")
	srv := h.templateServer("game-host")

	// The EULA must be accepted explicitly.
	r := h.do("POST", "/environments/"+envID+"/templates/minecraft-java/instances", "", map[string]any{"serverId": srv})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "missing_input", r.JSON()["code"])

	body := map[string]any{"serverId": srv, "inputs": map[string]string{"EULA": "TRUE"}}
	r = h.do("POST", "/environments/"+envID+"/templates/minecraft-java/plan", "", body)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, true, r.JSON()["proxyRestart"])

	r = h.do("POST", "/environments/"+envID+"/templates/minecraft-java/instances", "", body)
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	kinds := map[string]int{}
	for _, s := range r.JSON()["services"].([]any) {
		kinds[s.(map[string]any)["kind"].(string)]++
	}
	require.Equal(t, 1, kinds["route"], "one TCP route for 25565")

	// A second install collides on the published port.
	r = h.do("POST", "/environments/"+envID+"/templates/minecraft-java/plan", "", body)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, false, r.JSON()["installable"], string(r.Raw))
	r = h.do("POST", "/environments/"+envID+"/templates/minecraft-java/instances", "", body)
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "preflight_failed", r.JSON()["code"])
}

func TestTemplateInstallScopesAndLicense(t *testing.T) {
	h := newHarness(t)
	h.setup()
	envID := h.newEnv("Scopes")
	srv := h.templateServer("scope-host")

	ro := h.createToken([]string{"projects:read"}, nil)
	r := h.do("POST", "/environments/"+envID+"/templates/umami/instances", ro, map[string]any{"serverId": srv})
	require.Equal(t, http.StatusForbidden, r.Status, string(r.Raw))
	// Planning is read-only.
	r = h.do("POST", "/environments/"+envID+"/templates/umami/plan", ro, map[string]any{"serverId": srv})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))

	// Deploying needs deployments:write on top of projects:write.
	rw := h.createToken([]string{"projects:read", "projects:write"}, nil)
	r = h.do("POST", "/environments/"+envID+"/templates/umami/instances", rw, map[string]any{"serverId": srv, "deploy": true})
	require.Equal(t, http.StatusForbidden, r.Status, string(r.Raw))

	// Source-available templates warn.
	r = h.do("POST", "/environments/"+envID+"/templates/n8n/plan", "", map[string]any{"serverId": srv})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	var warned bool
	for _, c := range r.JSON()["checks"].([]any) {
		m := c.(map[string]any)
		warned = warned || (m["id"] == "license" && m["status"] == "warn")
	}
	require.True(t, warned, string(r.Raw))
}

func TestTemplateInstallAndDeployStartsOrderedDeployments(t *testing.T) {
	h := newHarness(t)
	h.setup()
	envID := h.newEnv("Deploy")
	srv := h.templateServer("deploy-host")

	r := h.do("POST", "/environments/"+envID+"/templates/umami/instances", "", map[string]any{"serverId": srv, "deploy": true})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	deps := r.JSON()["deployments"].([]any)
	require.Len(t, deps, 2, "one deployment per service: the database first, then the app")
	first := deps[0].(map[string]any)
	second := deps[1].(map[string]any)
	byID := map[string]string{}
	for _, s := range r.JSON()["services"].([]any) {
		m := s.(map[string]any)
		byID[m["id"].(string)] = m["name"].(string)
	}
	require.Equal(t, "Umami DB", byID[first["serviceId"].(string)], "providers deploy first")
	require.Equal(t, "Umami", byID[second["serviceId"].(string)])
}

func TestCatalogSearchAndLicenceFilter(t *testing.T) {
	h := newHarness(t)
	h.setup()

	// Words are ANDed over name, summary, tags, licence and engines; the best match comes first.
	r := h.do("GET", "/catalog/templates?q=umami", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	items := r.JSON()["items"].([]any)
	require.NotEmpty(t, items)
	require.Equal(t, "umami", items[0].(map[string]any)["id"])
	require.NotEmpty(t, items[0].(map[string]any)["version"], "the card carries the pinned app version")

	// The licence filter narrows the list and the category counts follow it.
	r = h.do("GET", "/catalog/templates?license=source-available", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	items = r.JSON()["items"].([]any)
	require.NotEmpty(t, items)
	for _, it := range items {
		require.Equal(t, "source-available", it.(map[string]any)["licenseClass"])
	}
	total := 0
	for _, c := range r.JSON()["categories"].([]any) {
		total += int(c.(map[string]any)["count"].(float64))
	}
	require.Equal(t, len(items), total)
}

// tamperSnapshot rewrites the stored snapshot of an instance, as if it had
// been installed from an older revision.
func (h *harness) tamperSnapshot(instanceID string, mutate func(tpl map[string]any)) {
	h.t.Helper()
	ctx := context.Background()
	var raw []byte
	require.NoError(h.t, h.app.Pool.QueryRow(ctx, `SELECT template_snapshot FROM template_instances WHERE id=$1`, uuid.MustParse(instanceID)).Scan(&raw))
	var tpl map[string]any
	require.NoError(h.t, json.Unmarshal(raw, &tpl))
	mutate(tpl)
	out, err := json.Marshal(tpl)
	require.NoError(h.t, err)
	_, err = h.app.Pool.Exec(ctx, `UPDATE template_instances SET template_snapshot=$2, template_revision=0, template_version='3.0.0' WHERE id=$1`, uuid.MustParse(instanceID), out)
	require.NoError(h.t, err)
}

func TestTemplateUpdateStagesChangesAndKeepsUserEdits(t *testing.T) {
	h := newHarness(t)
	h.setup()
	ctx := context.Background()
	envID := h.newEnv("Upd")
	srv := h.templateServer("upd-host")

	r := h.do("POST", "/environments/"+envID+"/templates/umami/instances", "", map[string]any{"serverId": srv})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	inst := r.JSON()["instance"].(map[string]any)
	instID := inst["id"].(string)
	// The install records the template version and snapshot, and is up to date.
	require.NotEmpty(t, inst["templateVersion"])
	require.Equal(t, inst["templateRevision"], inst["currentRevision"])
	require.Equal(t, false, inst["updateAvailable"])
	var snap int
	require.NoError(t, h.app.Pool.QueryRow(ctx, `SELECT length(template_snapshot::text) FROM template_instances WHERE id=$1`, uuid.MustParse(instID)).Scan(&snap))
	require.Positive(t, snap)
	var appID string
	for _, s := range r.JSON()["services"].([]any) {
		if m := s.(map[string]any); m["name"] == "Umami" {
			appID = m["id"].(string)
		}
	}
	require.NotEmpty(t, appID)
	current := h.do("GET", "/services/"+appID, "", nil).JSON()["config"].(map[string]any)

	r = h.do("GET", "/template-instances/"+instID+"/update", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, false, r.JSON()["available"])

	// Pretend the stack came from revision 0: an older image and start period,
	// the database URL built from the host, and a variable the template dropped.
	oldDigest := "sha256:" + strings.Repeat("b", 64)
	h.tamperSnapshot(instID, func(tpl map[string]any) {
		app := tpl["services"].([]any)[0].(map[string]any)
		app["image"], app["digest"] = "ghcr.io/umami-software/umami:3.0.0", oldDigest
		app["healthcheck"].(map[string]any)["startPeriodSeconds"] = 5
		env := app["env"].(map[string]any)
		env["DATABASE_URL"] = "${{db.DATABASE_HOST}}"
		env["OLD_ONLY"] = "x"
	})
	// The live service is what that old install left, plus one edit of the user's.
	_, err := h.app.Pool.Exec(ctx, `UPDATE services SET config = jsonb_set(jsonb_set(config, '{image}', to_jsonb($2::text)), '{healthcheck,startPeriodSeconds}', '99') WHERE id=$1`,
		uuid.MustParse(appID), "ghcr.io/umami-software/umami:3.0.0@"+oldDigest)
	require.NoError(t, err)
	h.putVars(appID, map[string]any{"key": "DATABASE_URL", "value": "${{umami-db.DATABASE_HOST}}"})

	// The list says an update is available.
	r = h.do("GET", "/environments/"+envID+"/template-instances", "", nil)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(r.Raw, &list))
	require.Equal(t, true, list[0]["updateAvailable"], string(r.Raw))
	require.Equal(t, "3.0.0", list[0]["templateVersion"])

	// The diff: image and URL are applied, your start period is kept, the dropped variable is manual.
	r = h.do("GET", "/template-instances/"+instID+"/update", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	u := r.JSON()
	require.Equal(t, true, u["available"])
	status := map[string]string{}
	for _, c := range u["changes"].([]any) {
		m := c.(map[string]any)
		status[m["field"].(string)] = m["status"].(string)
	}
	require.Equal(t, "apply", status["image"], string(r.Raw))
	require.Equal(t, "apply", status["env.DATABASE_URL"])
	require.Equal(t, "kept", status["healthcheck.startPeriodSeconds"])
	require.Equal(t, "manual", status["env.OLD_ONLY"])
	require.EqualValues(t, 2, u["apply"])
	require.EqualValues(t, 1, u["kept"])
	require.EqualValues(t, 1, u["manual"])
	// The diff writes nothing.
	require.Equal(t, "ghcr.io/umami-software/umami:3.0.0@"+oldDigest, h.do("GET", "/services/"+appID, "", nil).JSON()["config"].(map[string]any)["image"])

	// A read-only token may look, not stage; a stale revision is refused.
	ro := h.createToken([]string{"projects:read"}, nil)
	require.Equal(t, http.StatusOK, h.do("GET", "/template-instances/"+instID+"/update", ro, nil).Status)
	require.Equal(t, http.StatusForbidden, h.do("POST", "/template-instances/"+instID+"/update", ro, map[string]any{"revision": u["currentRevision"]}).Status)
	r = h.do("POST", "/template-instances/"+instID+"/update", "", map[string]any{"revision": 99})
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "template_changed", r.JSON()["code"])

	// Stage it.
	r = h.do("POST", "/template-instances/"+instID+"/update", "", map[string]any{"revision": u["currentRevision"]}, "Idempotency-Key", "upd-1")
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.EqualValues(t, 2, r.JSON()["staged"])
	require.Equal(t, []any{"umami"}, r.JSON()["services"])

	after := h.do("GET", "/services/"+appID, "", nil).JSON()
	cfg := after["config"].(map[string]any)
	require.Equal(t, current["image"], cfg["image"], "the image is the template's current one")
	require.EqualValues(t, 99, cfg["healthcheck"].(map[string]any)["startPeriodSeconds"], "the user's edit survives")
	require.Equal(t, true, after["hasStagedChanges"])
	require.Equal(t, "${{umami-db.DATABASE_URL}}", h.variables(appID)["DATABASE_URL"]["value"])
	// A secret that the service already has is untouched, and nothing deployed.
	require.Equal(t, true, h.variables(appID)["APP_SECRET"]["isSecret"])
	require.Nil(t, after["currentDeploymentId"])

	// The instance now records the new revision, and the update is gone.
	r = h.do("GET", "/environments/"+envID+"/template-instances", "", nil)
	require.NoError(t, json.Unmarshal(r.Raw, &list))
	require.Equal(t, false, list[0]["updateAvailable"])
	require.Equal(t, list[0]["currentRevision"], list[0]["templateRevision"])
	require.Equal(t, current["image"].(string)[len("ghcr.io/umami-software/umami:"):len("ghcr.io/umami-software/umami:")+5], list[0]["templateVersion"])
	require.Equal(t, false, h.do("GET", "/template-instances/"+instID+"/update", "", nil).JSON()["available"])

	// An audit row records the update.
	var n int
	require.NoError(t, h.app.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='template.update' AND resource_id=$1`, instID).Scan(&n))
	require.Equal(t, 1, n)
}

func TestTemplateUpdateBlockedForInstallsWithoutSnapshot(t *testing.T) {
	h := newHarness(t)
	h.setup()
	ctx := context.Background()
	envID := h.newEnv("Legacy")
	srv := h.templateServer("legacy-host")
	r := h.do("POST", "/environments/"+envID+"/templates/umami/instances", "", map[string]any{"serverId": srv})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	instID := r.JSON()["instance"].(map[string]any)["id"].(string)

	// An install from before versions were recorded: no snapshot, an older revision.
	_, err := h.app.Pool.Exec(ctx, `UPDATE template_instances SET template_snapshot=NULL, template_revision=0, template_version='' WHERE id=$1`, uuid.MustParse(instID))
	require.NoError(t, err)

	r = h.do("GET", "/template-instances/"+instID+"/update", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, false, r.JSON()["available"])
	require.Contains(t, r.JSON()["blocked"], "installed before template updates existed")
	r = h.do("POST", "/template-instances/"+instID+"/update", "", map[string]any{"revision": 1})
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "update_unavailable", r.JSON()["code"])

	var list []map[string]any
	require.NoError(t, json.Unmarshal(h.do("GET", "/environments/"+envID+"/template-instances", "", nil).Raw, &list))
	require.Equal(t, false, list[0]["updateAvailable"])
	require.NotEmpty(t, list[0]["updateBlocked"])

	// Another workspace's instance is a 404, not a leak.
	require.Equal(t, http.StatusNotFound, h.do("GET", "/template-instances/"+uuid.NewString()+"/update", "", nil).Status)
}
