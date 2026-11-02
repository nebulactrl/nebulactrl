package tailscale

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

func (e *env) raw(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	p, _ := auth.PrincipalFrom(ctx)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	Register(humachi.New(r, huma.DefaultConfig("t", "0")), e.svc)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func parse(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m), w.Body.String())
	return m
}

func (e *env) connect(t *testing.T) {
	t.Helper()
	_, err := e.svc.Put(e.admin, PutInput{ClientID: "client-1", ClientSecret: testSecret})
	require.NoError(t, err)
}

func TestTailscaleRoutes(t *testing.T) {
	e := newEnv(t)

	w := e.raw(e.admin, http.MethodGet, "/integrations/tailscale", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, false, parse(t, w)["configured"])
	require.Equal(t, http.StatusForbidden, e.raw(e.user, http.MethodGet, "/integrations/tailscale", "").Code)

	// Without a connection there is nothing to test, list or remove, and the
	// device picker explains why.
	w = e.raw(e.admin, http.MethodPost, "/integrations/tailscale/test", "")
	require.Equal(t, http.StatusNotFound, w.Code)
	w = e.raw(e.admin, http.MethodGet, "/integrations/tailscale/devices", "")
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.Equal(t, "tailnet_not_configured", parse(t, w)["code"])
	require.Equal(t, http.StatusNotFound, e.raw(e.admin, http.MethodDelete, "/integrations/tailscale", "").Code)

	// Connect: input is validated, the secret is write-only.
	body := `{"clientId":"client-1","clientSecret":"` + testSecret + `","defaultTags":["tag:nebula-svc"],"allowedTags":["tag:nebula-svc","tag:edge"]}`
	require.Equal(t, http.StatusForbidden, e.raw(e.user, http.MethodPut, "/integrations/tailscale", body).Code)
	w = e.raw(e.admin, http.MethodPut, "/integrations/tailscale", `{"clientId":"client-1","clientSecret":"`+testSecret+`","defaultTags":["nebula"]}`)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.Equal(t, "invalid_tag", parse(t, w)["code"])
	w = e.raw(e.admin, http.MethodPut, "/integrations/tailscale", `{"provider":"wireguard","clientId":"c"}`)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, "the provider is an enum")
	w = e.raw(e.admin, http.MethodPut, "/integrations/tailscale", body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	v := parse(t, w)
	require.Equal(t, true, v["configured"])
	require.Equal(t, true, v["secretSet"])
	require.NotContains(t, w.Body.String(), testSecret)

	w = e.raw(e.admin, http.MethodPost, "/integrations/tailscale/test", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	res := parse(t, w)
	require.Equal(t, true, res["ok"])
	require.NotEmpty(t, res["checks"])
	require.Equal(t, http.StatusForbidden, e.raw(e.user, http.MethodPost, "/integrations/tailscale/test", "").Code)

	w = e.raw(e.admin, http.MethodGet, "/integrations/tailscale/devices", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	items := parse(t, w)["items"].([]any)
	require.Len(t, items, 2)
	require.Equal(t, "nNODE1CNTRL", items[0].(map[string]any)["id"], "the stable node id is what the picker returns")
	require.Equal(t, "web", items[0].(map[string]any)["hostname"])
	require.Equal(t, http.StatusForbidden, e.raw(e.user, http.MethodGet, "/integrations/tailscale/devices", "").Code)

	require.Equal(t, http.StatusForbidden, e.raw(e.user, http.MethodDelete, "/integrations/tailscale", "").Code)
	require.Equal(t, http.StatusNoContent, e.raw(e.admin, http.MethodDelete, "/integrations/tailscale", "").Code)
	w = e.raw(e.admin, http.MethodGet, "/integrations/tailscale", "")
	require.Equal(t, false, parse(t, w)["configured"])
}

func (e *env) deviceDeletes() []string {
	var out []string
	for _, r := range e.fake.reqs() {
		if r.Method == http.MethodDelete {
			out = append(out, r.Path)
		}
	}
	return out
}

func TestDisconnectRemovesEveryDeviceThenTheIntegration(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()

	// A team without an integration has nothing to remove; others cannot disconnect.
	n, err := e.svc.Disconnect(e.admin)
	require.NoError(t, err)
	require.Zero(t, n)
	_, err = e.svc.Disconnect(e.user)
	require.Equal(t, "forbidden", apiCode(t, err))
	require.Empty(t, e.deviceDeletes())

	e.connect(t)
	srv, err := e.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: e.ws, Name: "edge"})
	require.NoError(t, err)
	_, err = e.pool.Exec(bg, "UPDATE servers SET tailnet_device_id = 'dev-edge', tailnet_hostname = 'edge' WHERE id = $1", srv.ID)
	require.NoError(t, err)
	// A removal that was queued, whose row is already forgotten, must not be left behind.
	jm := jobs.New(e.pool, nil)
	RegisterJobs(jm, e.svc)
	require.NoError(t, jm.Insert(bg, TailnetDeleteDeviceArgs{WorkspaceID: e.ws, DeviceID: "dev-queued"}, nil))
	// A job of another team is not ours to run.
	require.NoError(t, jm.Insert(bg, TailnetDeleteDeviceArgs{WorkspaceID: uuid.New(), DeviceID: "dev-foreign"}, nil))

	n, err = e.svc.Disconnect(e.admin)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.ElementsMatch(t, []string{"/api/v2/device/dev-edge", "/api/v2/device/dev-queued"}, e.deviceDeletes())
	v, err := e.svc.Get(e.admin)
	require.NoError(t, err)
	require.False(t, v.Configured, "the integration is removed with its devices")
}

func TestDisconnectStopsWhenADeviceCannotBeDeleted(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	e.connect(t)
	srv, err := e.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: e.ws, Name: "edge"})
	require.NoError(t, err)
	_, err = e.pool.Exec(bg, "UPDATE servers SET tailnet_device_id = 'dev-edge' WHERE id = $1", srv.ID)
	require.NoError(t, err)

	e.fake.deviceCode = http.StatusInternalServerError
	n, err := e.svc.Disconnect(e.admin)
	require.Error(t, err)
	require.Contains(t, err.Error(), "delete tailnet device dev-edge")
	require.Zero(t, n)
	v, err := e.svc.Get(e.admin)
	require.NoError(t, err)
	require.True(t, v.Configured, "no device is left behind without a way to delete it")

	// A device that is already gone is fine.
	e.fake.deviceCode = http.StatusNotFound
	n, err = e.svc.Disconnect(e.admin)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestDisconnectRevokesUnusedKeysButRefusesWhileAServiceIsOnTheTailnet(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	e.connect(t)
	prj, err := e.q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: e.ws, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	envRow, err := e.q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production"})
	require.NoError(t, err)
	sv, err := e.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: envRow.ID, Name: "web", Slug: "web", Kind: "app", SourceType: "image",
		Config: json.RawMessage(`{"image":"nginx:1","port":80}`),
	})
	require.NoError(t, err)
	_, err = e.pool.Exec(bg, `INSERT INTO service_tailnet (service_id, workspace_id, provider, hostname, device_id, auth_key_id) VALUES ($1, $2, 'tailscale', 'web', 'dev-web', 'key-web')`, sv.ID, e.ws)
	require.NoError(t, err)

	n, err := e.svc.Disconnect(e.admin)
	require.Equal(t, 1, n, "the sidecar's device is removed")
	require.Equal(t, "integration_in_use", apiCode(t, err), "a service that is still on the tailnet keeps the integration")
	deletes := e.deviceDeletes()
	require.Contains(t, deletes, "/api/v2/device/dev-web")
	var keyDeletes int
	for _, p := range deletes {
		if strings.Contains(p, "/keys/key-web") {
			keyDeletes++
		}
	}
	require.Equal(t, 1, keyDeletes, "the key the device was registered with is revoked")
	v, err := e.svc.Get(e.admin)
	require.NoError(t, err)
	require.True(t, v.Configured)
}
