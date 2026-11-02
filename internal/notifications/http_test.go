package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// httpCall drives one request through the registered endpoints as the principal
// carried by ctx (the way the auth middleware would attach it); a ctx without a
// principal sends the request anonymously.
func httpCall(t *testing.T, ctx context.Context, svc *Service, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	p, _ := auth.PrincipalFrom(ctx)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if p != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), p))
			}
			next.ServeHTTP(w, req)
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	Register(humachi.New(r, cfg), svc)

	rd := bytes.NewReader(nil)
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func httpJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m
}

func httpCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	return httpJSON(t, rec)["code"].(string)
}

func TestHTTPChannelLifecycle(t *testing.T) {
	f := newFixture(t)
	hookURL, c := hook(t, 200, "ok")

	// Event types: the catalogue, with the defaults flagged.
	rec := httpCall(t, f.ctx, f.svc, http.MethodGet, "/notifications/event-types", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	items := httpJSON(t, rec)["items"].([]any)
	require.Len(t, items, len(catalog))

	// Empty list is an empty array, never null.
	rec = httpCall(t, f.ctx, f.svc, http.MethodGet, "/notifications/channels", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"items":[]`)

	// Create a Slack channel: 201, the secret is masked in the response.
	rec = httpCall(t, f.ctx, f.svc, http.MethodPost, "/notifications/channels", map[string]any{
		"name": "ops", "kind": "slack", "secrets": map[string]any{"url": hookURL + "/services/T000/SECRETPATH"},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	created := httpJSON(t, rec)
	ch := created["channel"].(map[string]any)
	id := ch["id"].(string)
	require.Equal(t, "ops", ch["name"])
	require.Equal(t, true, ch["enabled"])
	require.NotContains(t, rec.Body.String(), "SECRETPATH", "the webhook URL's secret part never comes back")
	require.Nil(t, created["signingSecret"], "only webhook channels get a signing secret")

	// Same name again: 409, a replay would otherwise leak the secret.
	rec = httpCall(t, f.ctx, f.svc, http.MethodPost, "/notifications/channels", map[string]any{
		"name": "ops", "kind": "slack", "secrets": map[string]any{"url": hookURL + "/services/T000/SECRETPATH"},
	})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "channel_conflict", httpCode(t, rec))

	// Get and list agree.
	rec = httpCall(t, f.ctx, f.svc, http.MethodGet, "/notifications/channels/"+id, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "ops", httpJSON(t, rec)["name"])
	rec = httpCall(t, f.ctx, f.svc, http.MethodGet, "/notifications/channels", nil)
	require.Len(t, httpJSON(t, rec)["items"], 1)

	// Patch: rename and disable; the stored secret stays.
	rec = httpCall(t, f.ctx, f.svc, http.MethodPatch, "/notifications/channels/"+id, map[string]any{"name": "ops-2", "enabled": false})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	patched := httpJSON(t, rec)
	require.Equal(t, "ops-2", patched["name"])
	require.Equal(t, false, patched["enabled"])
	require.NotEmpty(t, patched["secrets"].(map[string]any)["url"])

	// Test send works on the disabled channel and returns the delivery.
	rec = httpCall(t, f.ctx, f.svc, http.MethodPost, "/notifications/channels/"+id+"/test", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	d := httpJSON(t, rec)
	require.Equal(t, "delivered", d["status"])
	require.Equal(t, true, d["isTest"])
	require.Len(t, c.all(), 1)

	// The log shows it, filtered by status.
	rec = httpCall(t, f.ctx, f.svc, http.MethodGet, "/notifications/channels/"+id+"/deliveries?status=delivered&size=5", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, httpJSON(t, rec)["items"], 1)
	rec = httpCall(t, f.ctx, f.svc, http.MethodGet, "/notifications/channels/"+id+"/deliveries?status=failed", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, httpJSON(t, rec)["items"])
	rec = httpCall(t, f.ctx, f.svc, http.MethodGet, "/notifications/channels/"+id+"/deliveries?status=bogus", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "the status enum is validated at the edge")

	// Slack channels have no signing secret to rotate.
	rec = httpCall(t, f.ctx, f.svc, http.MethodPost, "/notifications/channels/"+id+"/rotate-secret", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "not_a_webhook", httpCode(t, rec))

	// Delete: 204, then 404.
	rec = httpCall(t, f.ctx, f.svc, http.MethodDelete, "/notifications/channels/"+id, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	rec = httpCall(t, f.ctx, f.svc, http.MethodGet, "/notifications/channels/"+id, nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
	rec = httpCall(t, f.ctx, f.svc, http.MethodDelete, "/notifications/channels/"+id, nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
	rec = httpCall(t, f.ctx, f.svc, http.MethodPost, "/notifications/channels/"+id+"/test", nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHTTPWebhookSigningSecretAndRotation(t *testing.T) {
	f := newFixture(t)
	hookURL, c := hook(t, 200, "ok")
	rec := httpCall(t, f.ctx, f.svc, http.MethodPost, "/notifications/channels", map[string]any{
		"name": "hook", "kind": "webhook", "secrets": map[string]any{"url": hookURL},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	out := httpJSON(t, rec)
	first := out["signingSecret"].(string)
	require.NotEmpty(t, first)
	id := out["channel"].(map[string]any)["id"].(string)

	rec = httpCall(t, f.ctx, f.svc, http.MethodGet, "/notifications/channels/"+id, nil)
	require.NotContains(t, rec.Body.String(), first, "the signing secret is shown once")

	rec = httpCall(t, f.ctx, f.svc, http.MethodPost, "/notifications/channels/"+id+"/rotate-secret", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	next := httpJSON(t, rec)["signingSecret"].(string)
	require.NotEqual(t, first, next)

	rec = httpCall(t, f.ctx, f.svc, http.MethodPost, "/notifications/channels/"+id+"/test", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	req := c.all()[0]
	require.NoError(t, VerifySignature(next, req.Header.Get(SignatureHeader), req.Body, time.Now(), 0))
}

func TestHTTPTestChannelReportsAFailingReceiver(t *testing.T) {
	f := newFixture(t)
	hookURL, _ := hook(t, 500, "boom")
	v := f.slack("flaky", hookURL, nil)
	rec := httpCall(t, f.ctx, f.svc, http.MethodPost, "/notifications/channels/"+v.ID.String()+"/test", nil)
	require.Equal(t, http.StatusOK, rec.Code, "a failing receiver is a delivery result, not an HTTP error")
	d := httpJSON(t, rec)
	require.Equal(t, "failed", d["status"])
	require.EqualValues(t, 500, d["responseCode"])
	require.Contains(t, d["lastError"], "boom")
}

func TestHTTPValidation(t *testing.T) {
	f := newFixture(t)
	url, _ := hook(t, 200, "ok")
	v := f.slack("a", url, nil)
	bad := []struct {
		name string
		body map[string]any
	}{
		{"empty name", map[string]any{"name": "", "kind": "slack"}},
		{"unknown kind", map[string]any{"name": "x", "kind": "pager"}},
		{"missing slack url", map[string]any{"name": "x", "kind": "slack"}},
		{"unknown event type", map[string]any{"name": "x", "kind": "slack", "secrets": map[string]any{"url": url}, "rule": map[string]any{"eventTypes": []string{"nope"}}}},
		{"project scope without projects", map[string]any{"name": "x", "kind": "slack", "secrets": map[string]any{"url": url}, "rule": map[string]any{"eventTypes": []string{TypeDeployFailed}, "allProjects": false}}},
		{"unknown project", map[string]any{"name": "x", "kind": "slack", "secrets": map[string]any{"url": url}, "rule": map[string]any{"eventTypes": []string{TypeDeployFailed}, "allProjects": false, "projectIds": []uuid.UUID{uuid.New()}}}},
	}
	for _, tc := range bad {
		rec := httpCall(t, f.ctx, f.svc, http.MethodPost, "/notifications/channels", tc.body)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "%s: %s", tc.name, rec.Body.String())
	}
	// The same validation guards an update.
	rec := httpCall(t, f.ctx, f.svc, http.MethodPatch, "/notifications/channels/"+v.ID.String(), map[string]any{"rule": map[string]any{"eventTypes": []string{"nope"}}})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	rec = httpCall(t, f.ctx, f.svc, http.MethodPatch, "/notifications/channels/"+v.ID.String(), map[string]any{"name": ""})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	// Renaming onto an existing name conflicts.
	f.slack("b", url, nil)
	rec = httpCall(t, f.ctx, f.svc, http.MethodPatch, "/notifications/channels/"+v.ID.String(), map[string]any{"name": "b"})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	// Unknown channel on update.
	rec = httpCall(t, f.ctx, f.svc, http.MethodPatch, "/notifications/channels/"+uuid.NewString(), map[string]any{"name": "zz"})
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	// A path id that is not a UUID is rejected at the edge.
	rec = httpCall(t, f.ctx, f.svc, http.MethodGet, "/notifications/channels/not-a-uuid", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}

func TestHTTPEndpointsNeedTheAdminScope(t *testing.T) {
	f := newFixture(t)
	url, _ := hook(t, 200, "ok")
	v := f.slack("a", url, nil)
	id := v.ID.String()
	reader := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, WorkspaceID: f.ws.ID, Label: "ci", Scopes: []string{policy.ScopeProjectsRead}})
	calls := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/notifications/event-types", nil},
		{http.MethodGet, "/notifications/channels", nil},
		{http.MethodPost, "/notifications/channels", map[string]any{"name": "x", "kind": "slack", "secrets": map[string]any{"url": url}}},
		{http.MethodGet, "/notifications/channels/" + id, nil},
		{http.MethodPatch, "/notifications/channels/" + id, map[string]any{"name": "x"}},
		{http.MethodDelete, "/notifications/channels/" + id, nil},
		{http.MethodPost, "/notifications/channels/" + id + "/test", nil},
		{http.MethodPost, "/notifications/channels/" + id + "/rotate-secret", nil},
		{http.MethodGet, "/notifications/channels/" + id + "/deliveries", nil},
	}
	for _, tc := range calls {
		rec := httpCall(t, reader, f.svc, tc.method, tc.path, tc.body)
		require.Equal(t, http.StatusForbidden, rec.Code, "%s %s: %s", tc.method, tc.path, rec.Body.String())
		rec = httpCall(t, context.Background(), f.svc, tc.method, tc.path, tc.body)
		require.Equal(t, http.StatusUnauthorized, rec.Code, "anonymous %s %s: %s", tc.method, tc.path, rec.Body.String())
	}
	// Nothing was touched by the refused calls.
	got, err := f.svc.GetChannel(f.ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, "a", got.Name)
}
