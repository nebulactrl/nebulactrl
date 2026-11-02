package scaling_test

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
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/scaling"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// httpCall drives one PUT request through the registered endpoints as the principal
// carried by ctx (a ctx without one is an anonymous caller).
func httpCall(t *testing.T, ctx context.Context, svc *scaling.Service, path string, body any) *httptest.ResponseRecorder {
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
	scaling.Register(humachi.New(r, cfg), svc)

	rd := bytes.NewReader(nil)
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(http.MethodPut, path, rd)
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

func scalingOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	return httpJSON(t, rec)["scaling"].(map[string]any)
}

func TestHTTPSetScalingScalesADeployedService(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	f.deploy(sv)

	rec := httpCall(t, f.ctx, f.scal, "/services/"+sv.ID.String()+"/scaling", map[string]any{"replicas": 3})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotEmpty(t, rec.Header().Get("ETag"))
	require.EqualValues(t, 3, scalingOf(t, rec)["replicas"])
	require.EqualValues(t, 3, f.reload(sv.ID).Replicas)
	cmds := f.scaleCommands()
	require.Len(t, cmds, 1)
	require.Equal(t, 3, decodeScale(t, cmds[0].Payload).Replicas)

	// Repeating the request is a no-op.
	rec = httpCall(t, f.ctx, f.scal, "/services/"+sv.ID.String()+"/scaling", map[string]any{"replicas": 3})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, f.scaleCommands(), 1)
}

func TestHTTPSetScalingAutoscalingPatch(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	path := "/services/" + sv.ID.String() + "/scaling"

	rec := httpCall(t, f.ctx, f.scal, path, map[string]any{"autoscaling": map[string]any{"enabled": true, "max": 5}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	as := scalingOf(t, rec)["autoscaling"].(map[string]any)
	require.Equal(t, true, as["enabled"])
	require.EqualValues(t, 5, as["max"])

	// A manual count while autoscaling is on conflicts.
	rec = httpCall(t, f.ctx, f.scal, path, map[string]any{"replicas": 2})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "autoscaling_enabled", httpJSON(t, rec)["code"])

	// Omitted autoscaling fields keep their value.
	rec = httpCall(t, f.ctx, f.scal, path, map[string]any{"autoscaling": map[string]any{"targetCpuPercent": 60}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	as = scalingOf(t, rec)["autoscaling"].(map[string]any)
	require.Equal(t, true, as["enabled"])
	require.EqualValues(t, 5, as["max"])
	require.EqualValues(t, 60, as["targetCpuPercent"])
}

func TestHTTPSetScalingErrors(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	path := "/services/" + sv.ID.String() + "/scaling"

	rec := httpCall(t, f.ctx, f.scal, path, map[string]any{})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "validation_failed", httpJSON(t, rec)["code"])
	rec = httpCall(t, f.ctx, f.scal, path, map[string]any{"replicas": 11})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	rec = httpCall(t, f.ctx, f.scal, path, map[string]any{"autoscaling": map[string]any{"min": 4, "max": 3}})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "validation_failed", httpJSON(t, rec)["code"])

	pg := f.service("pg", services.KindDatabase, services.Config{Image: "postgres:17", Port: 5432})
	rec = httpCall(t, f.ctx, f.scal, "/services/"+pg.ID.String()+"/scaling", map[string]any{"replicas": 2})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "replicas_require_stateless", httpJSON(t, rec)["code"])

	rec = httpCall(t, f.ctx, f.scal, "/services/"+uuid.NewString()+"/scaling", map[string]any{"replicas": 2})
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Empty(t, f.disp.cmds)
	require.EqualValues(t, 1, f.reload(sv.ID).Replicas)
}

func TestHTTPScalingNeedsTheDeployScope(t *testing.T) {
	f := newFixture(t)
	sv := f.multi("shop")
	reader := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, WorkspaceID: f.ws.ID, Label: "ci", Scopes: []string{policy.ScopeProjectsRead}})
	for _, path := range []string{
		"/services/" + sv.ID.String() + "/scaling",
		"/services/" + sv.ID.String() + "/processes/worker/scaling",
	} {
		rec := httpCall(t, reader, f.scal, path, map[string]any{"replicas": 2})
		require.Equal(t, http.StatusForbidden, rec.Code, path+": "+rec.Body.String())
		rec = httpCall(t, context.Background(), f.scal, path, map[string]any{"replicas": 2})
		require.Equal(t, http.StatusUnauthorized, rec.Code, path+": "+rec.Body.String())
	}
	require.Empty(t, f.disp.cmds)
	_, ok := f.processRow(sv.ID, "worker")
	require.False(t, ok)
}

func TestHTTPSetProcessScaling(t *testing.T) {
	f := newFixture(t)
	sv := f.multi("shop")
	f.deployMulti(sv)
	f.setFacts(agentprotocol.CapProcesses)
	base := "/services/" + sv.ID.String() + "/processes/"

	rec := httpCall(t, f.ctx, f.scal, base+"worker/scaling", map[string]any{"replicas": 4})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotEmpty(t, rec.Header().Get("ETag"))
	row, ok := f.processRow(sv.ID, "worker")
	require.True(t, ok)
	require.EqualValues(t, 4, row.Replicas)
	cmds := f.scaleCommands()
	require.Len(t, cmds, 1)
	require.Equal(t, "worker", decodeScale(t, cmds[0].Payload).Process)
	require.EqualValues(t, 1, f.reload(sv.ID).Replicas, "the default process is untouched")

	// The default process' own name is the plain service endpoint.
	rec = httpCall(t, f.ctx, f.scal, base+"web/scaling", map[string]any{"replicas": 2})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.EqualValues(t, 2, f.reload(sv.ID).Replicas)

	// A singleton cannot scale; an unknown process is a 404 naming the real ones.
	rec = httpCall(t, f.ctx, f.scal, base+"clock/scaling", map[string]any{"replicas": 2})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "replicas_require_stateless", httpJSON(t, rec)["code"])
	rec = httpCall(t, f.ctx, f.scal, base+"nope/scaling", map[string]any{"replicas": 2})
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	body := httpJSON(t, rec)
	require.Equal(t, "unknown_process", body["code"])
	require.ElementsMatch(t, []any{"web", "worker", "clock"}, body["details"].(map[string]any)["processes"])

	// Autoscaling of one process, then a manual count conflicts.
	rec = httpCall(t, f.ctx, f.scal, base+"worker/scaling", map[string]any{"autoscaling": map[string]any{"enabled": true, "max": 6}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = httpCall(t, f.ctx, f.scal, base+"worker/scaling", map[string]any{"replicas": 2})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "autoscaling_enabled", httpJSON(t, rec)["code"])
	rec = httpCall(t, f.ctx, f.scal, base+"worker/scaling", map[string]any{})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}

func TestScalingDoesNotApplyToRouteOrStackServices(t *testing.T) {
	f := newFixture(t)
	insert := func(slug, kind, source string, cfg services.Config) db.Service {
		raw, err := json.Marshal(cfg)
		require.NoError(t, err)
		sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
			ID: uuid.New(), EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: kind, SourceType: source, Config: raw,
		})
		require.NoError(t, err)
		return sv
	}
	route := insert("edge", services.KindRoute, services.SourceExternal, services.Config{Route: &services.RouteConfig{Protocol: agentprotocol.ProtoHTTP}})
	stack := insert("compose", services.KindStack, services.SourceCompose, services.Config{})
	for _, sv := range []db.Service{route, stack} {
		_, err := f.scal.Set(f.ctx, sv.ID, scaling.SetInput{Replicas: new(2)})
		requireAPIError(t, err, 422, "not_applicable")
		_, err = f.scal.SetProcess(f.ctx, sv.ID, "web", scaling.SetInput{Replicas: new(2)})
		requireAPIError(t, err, 422, "not_applicable")
	}
	require.Empty(t, f.disp.cmds)
}

func TestAutoscaleJobRunsThroughRiver(t *testing.T) {
	require.Equal(t, "autoscale", scaling.AutoscaleArgs{}.Kind())

	f := newFixture(t)
	jm := jobs.New(f.pool, nil)
	scaling.RegisterJobs(jm, f.scal)
	require.NoError(t, jm.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = jm.Stop(ctx)
	})

	hot := f.web("hot")
	f.deploy(hot)
	f.enableAutoscaling(hot.ID, services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})
	f.load(hot.ID, 1, 90, 3*time.Minute)
	idle := f.web("idle")
	f.deploy(idle)
	f.enableAutoscaling(idle.ID, services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})

	require.NoError(t, jm.Insert(context.Background(), scaling.AutoscaleArgs{}, nil))
	require.Eventually(t, func() bool { return f.reload(hot.ID).Replicas == 2 }, 20*time.Second, 50*time.Millisecond)
	require.EqualValues(t, 1, f.reload(idle.ID).Replicas, "a service without load stays put")
}
