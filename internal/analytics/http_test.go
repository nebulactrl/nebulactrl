package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// httpGet drives one GET through the registered endpoints as the principal of ctx
// (a ctx without one is an anonymous caller).
func httpGet(t *testing.T, ctx context.Context, svc *Service, path string) *httptest.ResponseRecorder {
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
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func reportBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
	return m
}

func requestsOf(m map[string]any) float64 { return m["summary"].(map[string]any)["requests"].(float64) }

func TestHTTPServiceAnalytics(t *testing.T) {
	f := newFixture(t)
	f.seedTraffic()
	base := "/services/" + f.service.String() + "/analytics"

	// The default window is 24 hours with the minute tier.
	m := reportBody(t, httpGet(t, f.ctx, f.svc, base))
	require.EqualValues(t, 65, requestsOf(m), "app host plus the www domain of the service")
	require.EqualValues(t, 300, m["range"].(map[string]any)["stepSeconds"], "24h reports in five minute steps")
	require.Equal(t, "pages", m["topPathsKind"], "page views rank by default")
	require.NotContains(t, m, "comparison")

	// compare=prev adds the previous window; pages=false ranks every request.
	m = reportBody(t, httpGet(t, f.ctx, f.svc, base+"?range=1h&compare=prev&pages=false"))
	require.Equal(t, "requests", m["topPathsKind"])
	require.Contains(t, m, "comparison")
	require.EqualValues(t, 0, m["comparison"].(map[string]any)["requests"])

	// The host filter is case-insensitive.
	m = reportBody(t, httpGet(t, f.ctx, f.svc, base+"?range=1h&host=App.Example.com"))
	require.EqualValues(t, 60, requestsOf(m))

	// Unknown ranges and compare values are refused at the edge.
	require.Equal(t, http.StatusUnprocessableEntity, httpGet(t, f.ctx, f.svc, base+"?range=2y").Code)
	require.Equal(t, http.StatusUnprocessableEntity, httpGet(t, f.ctx, f.svc, base+"?compare=next").Code)
	require.Equal(t, http.StatusUnprocessableEntity, httpGet(t, f.ctx, f.svc, "/services/not-a-uuid/analytics").Code)
}

func TestHTTPServiceAnalyticsPermissions(t *testing.T) {
	f := newFixture(t)
	f.seedTraffic()
	base := "/services/" + f.service.String() + "/analytics?range=1h"

	require.Equal(t, http.StatusUnauthorized, httpGet(t, context.Background(), f.svc, base).Code)
	serversOnly := f.principal([]string{policy.ScopeServersRead}, nil)
	require.Equal(t, http.StatusForbidden, httpGet(t, serversOnly, f.svc, base).Code)
	// A token restricted to another project sees the service as missing.
	other := f.principal(policy.AllScopes, []uuid.UUID{f.otherPrj})
	require.Equal(t, http.StatusNotFound, httpGet(t, other, f.svc, base).Code)
	require.Equal(t, http.StatusNotFound, httpGet(t, f.ctx, f.svc, "/services/"+uuid.NewString()+"/analytics").Code)
	own := f.principal([]string{policy.ScopeProjectsRead}, []uuid.UUID{f.project})
	require.EqualValues(t, 65, requestsOf(reportBody(t, httpGet(t, own, f.svc, base))))
}

func TestHTTPServerAnalytics(t *testing.T) {
	f := newFixture(t)
	f.seedTraffic()
	base := "/servers/" + f.server.String() + "/analytics?range=1h"

	m := reportBody(t, httpGet(t, f.ctx, f.svc, base))
	require.EqualValues(t, 75, requestsOf(m))
	require.EqualValues(t, 10, m["unrouted"].(map[string]any)["requests"], "traffic that matched no service")

	projectsOnly := f.principal([]string{policy.ScopeProjectsRead}, nil)
	require.Equal(t, http.StatusForbidden, httpGet(t, projectsOnly, f.svc, base).Code)
	require.Equal(t, http.StatusUnauthorized, httpGet(t, context.Background(), f.svc, base).Code)
	require.Equal(t, http.StatusNotFound, httpGet(t, f.ctx, f.svc, "/servers/"+uuid.NewString()+"/analytics").Code)
}

func TestHTTPTrafficAnalyticsFilters(t *testing.T) {
	f := newFixture(t)
	f.seedTraffic()
	base := "/analytics/traffic?range=1h"

	require.EqualValues(t, 75, requestsOf(reportBody(t, httpGet(t, f.ctx, f.svc, base))))
	require.EqualValues(t, 75, requestsOf(reportBody(t, httpGet(t, f.ctx, f.svc, base+"&serverId="+f.server.String()))))
	require.EqualValues(t, 65, requestsOf(reportBody(t, httpGet(t, f.ctx, f.svc, base+"&serviceId="+f.service.String()))))
	require.EqualValues(t, 65, requestsOf(reportBody(t, httpGet(t, f.ctx, f.svc, base+"&environmentId="+f.env.String()))))
	require.EqualValues(t, 0, requestsOf(reportBody(t, httpGet(t, f.ctx, f.svc, base+"&serverId="+f.server.String()+"&host=nobody.example.org"))))

	// Unknown ids are 404, malformed ones 422.
	require.Equal(t, http.StatusNotFound, httpGet(t, f.ctx, f.svc, base+"&serverId="+uuid.NewString()).Code)
	require.Equal(t, http.StatusNotFound, httpGet(t, f.ctx, f.svc, base+"&serviceId="+uuid.NewString()).Code)
	require.Equal(t, http.StatusNotFound, httpGet(t, f.ctx, f.svc, base+"&environmentId="+uuid.NewString()).Code)
	require.Equal(t, http.StatusUnprocessableEntity, httpGet(t, f.ctx, f.svc, base+"&serverId=nope").Code)
	require.Equal(t, http.StatusUnprocessableEntity, httpGet(t, f.ctx, f.svc, base+"&serviceId=nope").Code)
	require.Equal(t, http.StatusUnprocessableEntity, httpGet(t, f.ctx, f.svc, base+"&environmentId=nope").Code)

	// Without a filter the report needs servers:read; a service or environment
	// filter is judged by the project and needs projects:read instead.
	projectsOnly := f.principal([]string{policy.ScopeProjectsRead}, nil)
	require.Equal(t, http.StatusForbidden, httpGet(t, projectsOnly, f.svc, base).Code)
	require.EqualValues(t, 65, requestsOf(reportBody(t, httpGet(t, projectsOnly, f.svc, base+"&serviceId="+f.service.String()))))
	require.EqualValues(t, 65, requestsOf(reportBody(t, httpGet(t, projectsOnly, f.svc, base+"&environmentId="+f.env.String()))))
	require.Equal(t, http.StatusUnauthorized, httpGet(t, context.Background(), f.svc, base).Code)

	// A project-restricted token only counts its own services and never sees unrouted traffic.
	own := f.principal(policy.AllScopes, []uuid.UUID{f.project})
	m := reportBody(t, httpGet(t, own, f.svc, base))
	require.EqualValues(t, 65, requestsOf(m))
	require.NotContains(t, m, "unrouted")
	other := f.principal(policy.AllScopes, []uuid.UUID{f.otherPrj})
	require.EqualValues(t, 0, requestsOf(reportBody(t, httpGet(t, other, f.svc, base))))
	require.Equal(t, http.StatusNotFound, httpGet(t, other, f.svc, base+"&serviceId="+f.service.String()).Code)
}
