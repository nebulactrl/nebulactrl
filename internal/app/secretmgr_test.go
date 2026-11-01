package app_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/secretmgr"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// canary has characters that change under URL encoding, so both forms are checked.
const vaultCanary = "vault-canary/9f3a1c+x"

// fakeBao is a minimal OpenBao/Vault KV v2 server: one secret, token auth.
type fakeBao struct {
	srv   *httptest.Server
	reads atomic.Int32
	deny  atomic.Bool
}

func newFakeBao(t *testing.T) *fakeBao {
	t.Helper()
	fb := &fakeBao{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/token/lookup-self", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "s.root-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	mux.HandleFunc("/v1/secret/data/apps/shop/db", func(w http.ResponseWriter, r *http.Request) {
		fb.reads.Add(1)
		if fb.deny.Load() || r.Header.Get("X-Vault-Token") != "s.root-token" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"data":{"data":{"password":%q,"user":"shop"},"metadata":{"version":1}}}`, vaultCanary)
	})
	fb.srv = httptest.NewServer(mux)
	t.Cleanup(fb.srv.Close)
	return fb
}

func (h *harness) addBao(fb *fakeBao) string {
	h.t.Helper()
	r := h.do("POST", "/secret-managers", "", map[string]any{"name": "bao", "kind": "vault", "address": fb.srv.URL, "secret": "s.root-token"})
	require.Equal(h.t, http.StatusCreated, r.Status, string(r.Raw))
	require.NotContains(h.t, string(r.Raw), "s.root-token", "the credential is write-only")
	require.Equal(h.t, true, r.JSON()["secretSet"])
	return r.JSON()["id"].(string)
}

// dumpDB returns every row of every table as text, plus the decrypted release
// environments and variable values, so a value can be searched for anywhere.
func (h *harness) dumpDB() string {
	h.t.Helper()
	ctx := context.Background()
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(h.t, err)
	var sb strings.Builder
	rows, err := h.app.Pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = current_schema()`)
	require.NoError(h.t, err)
	var tables []string
	for rows.Next() {
		var n string
		require.NoError(h.t, rows.Scan(&n))
		tables = append(tables, n)
	}
	rows.Close()
	for _, tb := range tables {
		var dump *string
		require.NoError(h.t, h.app.Pool.QueryRow(ctx, fmt.Sprintf(`SELECT string_agg(t::text, E'\n') FROM %q t`, tb)).Scan(&dump), tb)
		if dump != nil {
			fmt.Fprintf(&sb, "-- %s\n%s\n", tb, *dump)
		}
	}
	for _, col := range []secretbox.Column{secretbox.ReleasesEnv, secretbox.ServiceVariablesValue, secretbox.SharedVariablesValue} {
		q := fmt.Sprintf(`SELECT %s, %s FROM %s`, col.RowKey, col.Column, col.Table)
		rs, err := h.app.Pool.Query(ctx, q)
		require.NoError(h.t, err, q)
		for rs.Next() {
			var rowID string
			var enc []byte
			require.NoError(h.t, rs.Scan(&rowID, &enc))
			if len(enc) > 0 {
				plain, err := box.Open(col, rowID, enc)
				require.NoError(h.t, err)
				fmt.Fprintf(&sb, "-- decrypted %s\n%s\n", col, plain)
			}
		}
		rs.Close()
	}
	return sb.String()
}

// extRig is a deployments and a services instance sharing one external-secret
// handle (as the app wires them), built over the harness database. The harness
// app's own services are not reachable from the test package.
type extRig struct {
	deps *deployments.Service
	svcs *services.Service
}

func (h *harness) extRig() extRig {
	h.t.Helper()
	c := h.app.Core
	hub := agents.NewHub(c.Pool, c.Bus, c.Jobs, c.Config, nil)
	ext := services.NewExtSecrets(secretmgr.NewService(c.Pool, c.Box, nil))
	svcs := services.NewService(c.Pool, c.Bus, c.Box, hub, nil)
	svcs.SetExtSecrets(ext)
	deps := deployments.NewService(deployments.Deps{Pool: c.Pool, Bus: c.Bus, Jobs: c.Jobs, Box: c.Box, Dispatcher: hub, Services: svcs, ExtSecrets: ext})
	return extRig{deps: deps, svcs: svcs}
}

// deployCommand waits for the deploy command of the server and returns its id
// and persisted payload.
func (h *harness) deployCommand(serverID string) (uuid.UUID, []byte) {
	h.t.Helper()
	var id uuid.UUID
	var payload []byte
	require.Eventually(h.t, func() bool {
		return h.app.Pool.QueryRow(context.Background(),
			`SELECT id, payload FROM agent_commands WHERE server_id = $1 AND kind = 'deploy' ORDER BY created_at DESC LIMIT 1`,
			uuid.MustParse(serverID)).Scan(&id, &payload) == nil
	}, 20*time.Second, 100*time.Millisecond)
	return id, payload
}

// A vault reference is stored as a reference, fetched only when the command is
// sent, attached as a send-time secret, masked in logs and never persisted.
func TestExternalSecretFlowsToDeployWithoutBeingStored(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.startApp()
	fb := newFakeBao(t)
	e := h.newVarEnv("ext")
	api := e.service("api", "app", nil)
	integ := h.addBao(fb)

	// The test endpoint signs in and reads the secret; the value is never returned.
	r := h.do("POST", "/secret-managers/"+integ+"/test", "", map[string]any{"path": "apps/shop/db", "key": "password"})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, true, r.JSON()["ok"], string(r.Raw))
	require.Equal(t, true, r.JSON()["read"])
	require.NotContains(t, string(r.Raw), vaultCanary)
	afterTest := fb.reads.Load()
	require.EqualValues(t, 1, afterTest)

	// Variables, listings, validation and the deploy plan only handle the
	// reference: no network call, no value.
	r = h.put(api,
		v("DB_PASSWORD", "${{vault:bao/apps/shop/db#password}}"),
		v("DATABASE_URL", "postgres://shop:${{urlencode(vault:bao/apps/shop/db#password)}}@db/app"),
	)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	it := item(t, r, "DB_PASSWORD")
	require.Equal(t, true, it["computedSecret"], "an external value is always secret")
	require.NotContains(t, string(r.Raw), vaultCanary)
	require.Equal(t, http.StatusOK, h.do("GET", "/services/"+api+"/variables", "", nil).Status)
	r = h.do("POST", "/services/"+api+"/variables/validate", "", map[string]any{"key": "X", "value": "${{vault:bao/apps/shop/db#password}}"})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.NotContains(t, string(r.Raw), vaultCanary)
	r = h.do("POST", "/services/"+api+"/deployments/plan", "", map[string]any{})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.NotContains(t, string(r.Raw), vaultCanary)
	require.Equal(t, afterTest, fb.reads.Load(), "references are not fetched before a command is sent")

	// Deploy: the command payload and the release carry the reference only.
	r = h.do("POST", "/services/"+api+"/deployments", "", map[string]any{})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	cmdID, payload := h.deployCommand(e.srvID)
	require.NotContains(t, string(payload), vaultCanary)
	require.Equal(t, afterTest, fb.reads.Load())

	// Send time: the values are fetched and attached.
	rig := h.extRig()
	secrets, err := rig.deps.SecretsForCommand(context.Background(), cmdID, agentprotocol.KindDeploy, payload)
	require.NoError(t, err)
	require.Equal(t, vaultCanary, secrets[agentprotocol.SecretEnvPrefix+"DB_PASSWORD"])
	require.Equal(t, "postgres://shop:"+url.QueryEscape(vaultCanary)+"@db/app", secrets[agentprotocol.SecretEnvPrefix+"DATABASE_URL"])
	require.EqualValues(t, afterTest+1, fb.reads.Load(), "one read serves both variables of the same secret")

	// Nothing stored: not in any row, nor in a decrypted release or variable.
	dump := h.dumpDB()
	require.NotContains(t, dump, vaultCanary)
	require.NotContains(t, dump, url.QueryEscape(vaultCanary))
	require.NotContains(t, dump, "s.root-token", "the integration credential is only stored sealed")
	require.Contains(t, dump, "vault:bao/apps/shop/db#password", "the release keeps the reference")
	for _, p := range []string{"/audit-events"} {
		require.NotContains(t, string(h.do("GET", p, "", nil).Raw), vaultCanary)
	}

	// The fetched values are masked in logs from now on (also encoded).
	red, err := rig.svcs.RedactorFor(context.Background(), uuid.MustParse(api))
	require.NoError(t, err)
	for _, line := range []string{"password=" + vaultCanary, "url postgres://shop:" + url.QueryEscape(vaultCanary) + "@db/app"} {
		out := red(line)
		require.NotContains(t, out, "canary", line)
		require.Contains(t, out, services.Mask, line)
	}

	// A denied read fails the command's secrets and names the reference.
	fb.deny.Store(true)
	_, err = rig.deps.SecretsForCommand(context.Background(), cmdID, agentprotocol.KindDeploy, payload)
	require.Error(t, err)
	require.Contains(t, err.Error(), "${{vault:bao/apps/shop/db#password}}")
	require.Contains(t, err.Error(), "permission denied")
	require.NotContains(t, err.Error(), vaultCanary)
}

// The integration cannot be removed while a variable references it, and the
// admin scope gates every change.
func TestSecretManagerLifecycle(t *testing.T) {
	h := newHarness(t)
	h.setup()
	fb := newFakeBao(t)
	e := h.newVarEnv("life")
	api := e.service("api", "app", nil)
	integ := h.addBao(fb)

	r := h.do("POST", "/secret-managers", "", map[string]any{"name": "bao", "kind": "vault", "address": fb.srv.URL, "secret": "x"})
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "name_taken", r.JSON()["code"])
	r = h.do("POST", "/secret-managers", "", map[string]any{"name": "Bad Name", "kind": "vault", "address": fb.srv.URL, "secret": "x"})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status)
	r = h.do("POST", "/secret-managers", "", map[string]any{"name": "inf", "kind": "infisical", "clientId": "c", "secret": "x"})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, "an Infisical project id is required")
	require.Equal(t, "invalid_project_id", r.JSON()["code"])
	r = h.do("POST", "/secret-managers", "", map[string]any{"name": "inf", "kind": "infisical", "clientId": "c", "projectId": "proj-1", "secret": "x"})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	require.Equal(t, "https://us.infisical.com", r.JSON()["config"].(map[string]any)["siteUrl"])
	require.Contains(t, r.JSON()["example"], "${{vault:inf/")

	// Update: the address may only change together with the credential.
	r = h.do("PUT", "/secret-managers/"+integ, "", map[string]any{"address": "http://other.invalid:8200"})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "secret_required", r.JSON()["code"])
	r = h.do("PUT", "/secret-managers/"+integ, "", map[string]any{"address": fb.srv.URL, "mount": "kv"})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "kv", r.JSON()["config"].(map[string]any)["mount"])
	require.Equal(t, true, r.JSON()["secretSet"])

	// Scopes: a read-only token lists but cannot create, test or delete.
	ro := h.createToken([]string{"projects:read"}, nil)
	require.Equal(t, http.StatusOK, h.do("GET", "/secret-managers", ro, nil).Status)
	require.NotContains(t, string(h.do("GET", "/secret-managers", ro, nil).Raw), "s.root-token")
	require.Equal(t, http.StatusForbidden, h.do("POST", "/secret-managers", ro, map[string]any{"name": "x", "kind": "vault", "address": fb.srv.URL, "secret": "x"}).Status)
	require.Equal(t, http.StatusForbidden, h.do("POST", "/secret-managers/"+integ+"/test", ro, map[string]any{}).Status)
	require.Equal(t, http.StatusForbidden, h.do("DELETE", "/secret-managers/"+integ, ro, nil).Status)

	// A variable that references it blocks deletion; a team variable counts too.
	require.Equal(t, http.StatusOK, h.put(api, v("PW", "${{vault:bao/apps/shop/db#password}}")).Status)
	r = h.do("DELETE", "/secret-managers/"+integ, "", nil)
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "integration_in_use", r.JSON()["code"])
	require.Contains(t, string(r.Raw), "PW")
	require.Equal(t, http.StatusNoContent, h.do("DELETE", "/services/"+api+"/variables/PW", "", nil).Status)
	r = h.do("PUT", "/team/shared-variables", "", map[string]any{"variables": []any{v("TEAM_PW", "${{vault:bao/apps/shop/db#password}}")}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, http.StatusConflict, h.do("DELETE", "/secret-managers/"+integ, "", nil).Status)
	require.Equal(t, http.StatusOK, h.do("DELETE", "/team/shared-variables/TEAM_PW", "", nil).Status)
	require.Equal(t, http.StatusNoContent, h.do("DELETE", "/secret-managers/"+integ, "", nil).Status)

	// Audit: the changes are recorded without the credential.
	raw := string(h.do("GET", "/audit-events", "", nil).Raw)
	require.Contains(t, raw, "secret_manager.create")
	require.Contains(t, raw, "secret_manager.delete")
	require.NotContains(t, raw, "s.root-token")
}

// An unknown integration is a problem that names the reference.
func TestExternalReferenceToUnknownIntegrationFailsTheDeploy(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.startApp()
	e := h.newVarEnv("unk")
	api := e.service("api", "app", nil)
	r := h.put(api, v("PW", "${{vault:nope/apps/shop/db#password}}"))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	var found bool
	for _, w := range r.JSON()["warnings"].([]any) {
		if m := w.(map[string]any); m["code"] == "unresolved_reference" {
			require.Contains(t, m["message"], "${{vault:nope/apps/shop/db#password}}")
			found = true
		}
	}
	require.True(t, found, string(r.Raw))
	r = h.do("POST", "/services/"+api+"/deployments", "", map[string]any{})
	require.NotEqual(t, http.StatusAccepted, r.Status, "a deploy cannot start with an unresolvable reference")
	require.Contains(t, string(r.Raw), "${{vault:nope/apps/shop/db#password}}")
}

// Team shared variables: the third layer, referenced as ${{team.KEY}}.
func TestTeamSharedVariables(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.startApp()
	e := h.newVarEnv("team")
	api := e.service("api", "app", nil)
	other := h.newVarEnv("team2")
	web := other.service("web", "app", nil)

	// Write, list, secrets are hidden.
	r := h.do("PUT", "/team/shared-variables", "", map[string]any{"variables": []any{
		v("REGION", "eu-west"), v("TEAM_KEY", "team-secret-123456", "isSecret", true),
	}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	it := item(t, r, "REGION")
	require.Equal(t, "team", it["layer"])
	require.Equal(t, "eu-west", it["value"])
	sec := item(t, r, "TEAM_KEY")
	require.Nil(t, sec["value"])
	require.Equal(t, true, sec["isSecret"])
	require.NotContains(t, string(r.Raw), "team-secret-123456")

	// Services consume it through an explicit reference, in any project.
	require.Equal(t, http.StatusOK, h.put(api, v("REGION", "${{team.REGION}}")).Status)
	require.Equal(t, http.StatusOK, h.put(web, v("REGION", "${{team.REGION}}"), v("K", "${{team.TEAM_KEY}}")).Status)
	require.Equal(t, "eu-west", h.resolvedPreview(api, "REGION"))
	require.Equal(t, "eu-west", h.resolvedPreview(web, "REGION"))
	list := h.do("GET", "/team/shared-variables", "", nil)
	require.EqualValues(t, 2, item(t, list, "REGION")["usedBy"])
	require.EqualValues(t, 1, item(t, list, "TEAM_KEY")["usedBy"])
	require.Equal(t, true, h.do("GET", "/services/"+web+"/variables", "", nil).JSON()["items"] != nil)

	// Changing the value restages the consumers in every project.
	gApi, gWeb := h.generation(api), h.generation(web)
	r = h.do("PUT", "/team/shared-variables", "", map[string]any{"variables": []any{v("REGION", "us-east")}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Len(t, r.JSON()["restaged"], 2)
	require.Greater(t, h.generation(api), gApi)
	require.Greater(t, h.generation(web), gWeb)
	require.Equal(t, "us-east", h.resolvedPreview(api, "REGION"))

	// Share stages KEY=${{team.KEY}} on a service of another project.
	r = h.do("POST", "/team/shared-variables/REGION/share", "", map[string]any{"serviceIds": []string{web}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "already_set", r.JSON()["warnings"].([]any)[0].(map[string]any)["code"])

	// Validation: a team value may only reference team variables and external secrets.
	r = h.do("PUT", "/team/shared-variables", "", map[string]any{"variables": []any{v("BAD", "${{shared.X}}")}})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "invalid_shared_reference", r.JSON()["code"])
	r = h.do("PUT", "/team/shared-variables", "", map[string]any{"variables": []any{v("A", "${{team.B}}"), v("B", "${{team.A}}")}})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "reference_cycle", r.JSON()["code"])
	// A project shared value may use a team value.
	r = h.do("PUT", "/projects/"+e.projectID+"/shared-variables", "", map[string]any{"variables": []any{v("P", "p-${{team.REGION}}")}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, http.StatusOK, h.put(api, v("PV", "${{shared.P}}")).Status)
	require.Equal(t, "p-us-east", h.resolvedPreview(api, "PV"))

	// History and audit: the layer is named, secrets keep a fingerprint only.
	hist := h.do("GET", "/team/shared-variables/TEAM_KEY/history", "", nil)
	require.Equal(t, http.StatusOK, hist.Status, string(hist.Raw))
	require.NotContains(t, string(hist.Raw), "team-secret-123456")
	audit := string(h.do("GET", "/audit-events", "", nil).Raw)
	require.Contains(t, audit, "shared_variable.set")
	require.Contains(t, audit, `"layer":"team"`)
	require.NotContains(t, audit, "team-secret-123456")

	// Access: a token restricted to one project has no access; a read-only token reads but cannot write.
	restricted := h.createToken([]string{"projects:read", "projects:write"}, []string{e.projectID})
	require.Equal(t, http.StatusForbidden, h.do("GET", "/team/shared-variables", restricted, nil).Status)
	ro := h.createToken([]string{"projects:read"}, nil)
	require.Equal(t, http.StatusOK, h.do("GET", "/team/shared-variables", ro, nil).Status)
	require.Equal(t, http.StatusForbidden, h.do("PUT", "/team/shared-variables", ro, map[string]any{"variables": []any{v("Z", "1")}}).Status)

	// Delete: the reference stops resolving and the consumers are restaged.
	r = h.do("DELETE", "/team/shared-variables/REGION", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, http.StatusNotFound, h.do("DELETE", "/team/shared-variables/REGION", "", nil).Status)

	// A service cannot be named like a namespace.
	r = h.do("POST", "/environments/"+e.envID+"/services", "", map[string]any{"name": "team", "kind": "app", "sourceType": "image", "serverId": e.srvID,
		"config": map[string]any{"image": "traefik/whoami:latest", "port": 80}})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	require.NotEqual(t, "team", r.JSON()["slug"])
}
