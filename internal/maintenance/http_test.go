package maintenance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/maintenance"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// httpCall drives one request through the registered endpoints as the principal
// carried by ctx (a ctx without one is an anonymous caller).
func httpCall(t *testing.T, ctx context.Context, svc *maintenance.Service, method, path string, body any) *httptest.ResponseRecorder {
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
	maintenance.Register(humachi.New(r, cfg), svc)

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

func TestHTTPSetMaintenanceOnAndOff(t *testing.T) {
	f := newFixture(t)
	sv := f.deployedWeb("web", "app.example.com")
	path := "/services/" + sv.ID.String() + "/maintenance"
	eta := time.Now().Add(time.Hour).UTC().Truncate(time.Second)

	rec := httpCall(t, f.ctx, f.svc, http.MethodPut, path, map[string]any{
		"enabled": true, "title": "Upgrading", "message": "Back soon", "eta": eta.Format(time.RFC3339),
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotEmpty(t, rec.Header().Get("ETag"), "the response carries the service's generation as an ETag")
	m := httpJSON(t, rec)["maintenance"].(map[string]any)
	require.Equal(t, true, m["enabled"])
	require.Equal(t, "Upgrading", m["title"])
	got, err := time.Parse(time.RFC3339, m["eta"].(string))
	require.NoError(t, err)
	require.True(t, got.Equal(eta), "eta %s, want %s", got, eta)
	require.Len(t, f.syncs(), 1, "a deployed service's server gets the new table")

	rec = httpCall(t, f.ctx, f.svc, http.MethodPut, path, map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m = httpJSON(t, rec)["maintenance"].(map[string]any)
	require.Equal(t, false, m["enabled"])
	require.Equal(t, "Upgrading", m["title"], "the stored text survives switching off")
	require.Equal(t, []string{"service.maintenance.on", "service.maintenance.off"}, f.auditActions())
}

func TestHTTPSetMaintenanceRejectsBadInput(t *testing.T) {
	f := newFixture(t)
	sv := f.deployedWeb("web", "app.example.com")
	path := "/services/" + sv.ID.String() + "/maintenance"

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	rec := httpCall(t, f.ctx, f.svc, http.MethodPut, path, map[string]any{"enabled": true, "eta": past})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "validation_failed", httpJSON(t, rec)["code"])

	long := make([]byte, 81)
	for i := range long {
		long[i] = 'x'
	}
	rec = httpCall(t, f.ctx, f.svc, http.MethodPut, path, map[string]any{"enabled": true, "title": string(long)})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "the length bound is enforced at the edge")

	rec = httpCall(t, f.ctx, f.svc, http.MethodPut, "/services/"+uuid.NewString()+"/maintenance", map[string]any{"enabled": true})
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	require.Empty(t, f.syncs())
	require.Empty(t, f.auditActions())
}

func TestHTTPSetMaintenanceNeedsDeployScope(t *testing.T) {
	f := newFixture(t)
	sv := f.deployedWeb("web", "app.example.com")
	path := "/services/" + sv.ID.String() + "/maintenance"

	rec := httpCall(t, f.reader, f.svc, http.MethodPut, path, map[string]any{"enabled": true})
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	rec = httpCall(t, context.Background(), f.svc, http.MethodPut, path, map[string]any{"enabled": true})
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	require.Empty(t, f.auditActions())
}

func TestHTTPPreviewServesTheHTMLPage(t *testing.T) {
	f := newFixture(t)
	sv := f.deployedWeb("web", "app.example.com")
	_, err := f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: true, Title: "Stored title", Message: "<i>x</i>"})
	require.NoError(t, err)
	path := "/services/" + sv.ID.String() + "/maintenance/preview"

	// A read-only principal may preview.
	rec := httpCall(t, f.reader, f.svc, http.MethodGet, path, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	require.NotEmpty(t, rec.Header().Get("Content-Security-Policy"), "the page ships with the proxy's security headers")
	require.Contains(t, rec.Body.String(), "Stored title")
	require.Contains(t, rec.Body.String(), "&lt;i&gt;x&lt;/i&gt;")

	// A draft replaces the stored text; an unparsable ETA is refused.
	q := url.Values{"draft": {"true"}, "title": {"Draft only"}, "eta": {time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}}
	rec = httpCall(t, f.ctx, f.svc, http.MethodGet, path+"?"+q.Encode(), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Draft only")
	require.NotContains(t, rec.Body.String(), "Stored title")

	q.Set("eta", "tomorrow-ish")
	rec = httpCall(t, f.ctx, f.svc, http.MethodGet, path+"?"+q.Encode(), nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "validation_failed", httpJSON(t, rec)["code"])

	// Without draft=true the query text and ETA are ignored entirely.
	rec = httpCall(t, f.ctx, f.svc, http.MethodGet, path+"?title=Ignored&eta=garbage", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Stored title")

	rec = httpCall(t, f.ctx, f.svc, http.MethodGet, "/services/"+uuid.NewString()+"/maintenance/preview", nil)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	rec = httpCall(t, context.Background(), f.svc, http.MethodGet, path, nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	_ = policy.ScopeProjectsRead
}

func TestMaintenanceRepeatWithPassedETAIsStillANoOp(t *testing.T) {
	f := newFixture(t)
	sv := f.deployedWeb("web", "app.example.com")
	eta := time.Now().Add(2 * time.Second).UTC().Truncate(time.Second)
	_, err := f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: true, Title: "t", ETA: &eta})
	require.NoError(t, err)
	require.Len(t, f.syncs(), 1)

	// A different ETA in the past is refused, and leaves the live state alone.
	old := time.Now().Add(-time.Minute)
	_, err = f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: true, Title: "t", ETA: &old})
	requireAPIError(t, err, 422, "validation_failed")
	require.Len(t, f.syncs(), 1)
	require.Equal(t, []string{"service.maintenance.on"}, f.auditActions())
}
