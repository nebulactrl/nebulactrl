package canvas_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/canvas"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

type rig struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	bus     *events.Bus
	svc     *canvas.Service
	ws      db.Workspace
	project db.Project
	env     db.Environment
	owner   context.Context
}

func principal(ws uuid.UUID, scopes []string, projects []uuid.UUID) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: ws, Role: auth.RoleOwner, Label: "Ada", Scopes: scopes, ProjectIDs: projects,
	})
}

func newRig(t *testing.T) *rig {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	bus := events.NewBus()
	return &rig{
		t: t, pool: pool, q: q, bus: bus, svc: canvas.NewService(pool, bus), ws: ws, project: prj, env: env,
		owner: principal(ws.ID, policy.AllScopes, nil),
	}
}

func (r *rig) newEnv(slug string) db.Environment {
	r.t.Helper()
	env, err := r.q.CreateEnvironment(context.Background(), db.CreateEnvironmentParams{ProjectID: r.project.ID, Name: slug, Slug: slug})
	require.NoError(r.t, err)
	return env
}

func (r *rig) rawLayout(envID uuid.UUID, layout string) {
	r.t.Helper()
	_, err := r.pool.Exec(context.Background(), `INSERT INTO canvas_layouts (environment_id, layout, version) VALUES ($1, $2::jsonb, 1)`, envID, layout)
	require.NoError(r.t, err)
}

func requireAPIError(t *testing.T, err error, status int, code string) *apierr.Error {
	t.Helper()
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, status, ae.Status)
	require.Equal(t, code, ae.Code)
	return ae
}

func TestGetBeforeAnythingWasSavedReturnsTheEmptyVersionZeroLayout(t *testing.T) {
	r := newRig(t)
	c, err := r.svc.Get(r.owner, r.project.ID, r.env.ID)
	require.NoError(t, err)
	require.Equal(t, int64(0), c.Version)
	require.NotNil(t, c.Layout.Nodes)
	require.Empty(t, c.Layout.Nodes)
	require.NotNil(t, c.Layout.Groups)
	require.Empty(t, c.Layout.Groups)
	require.Equal(t, canvas.Viewport{Zoom: 1}, c.Layout.Viewport)
}

func TestPutStoresTheLayoutAndAdvancesTheVersion(t *testing.T) {
	r := newRig(t)
	a, b := uuid.NewString(), uuid.NewString()
	layout := canvas.Layout{
		Nodes:    map[string]canvas.Pos{a: {X: 10, Y: 20}, b: {X: -5.5, Y: 7}},
		Groups:   []canvas.Group{{ID: "g1", Name: "Backend", X: 1, Y: 2, W: 300, H: 200, Collapsed: true, ServiceIDs: []string{a}}},
		Viewport: canvas.Viewport{X: 3, Y: 4, Zoom: 1.5},
	}
	saved, err := r.svc.Put(r.owner, r.project.ID, r.env.ID, 0, layout)
	require.NoError(t, err)
	require.Equal(t, int64(1), saved.Version)
	require.Equal(t, layout, saved.Layout)

	got, err := r.svc.Get(r.owner, r.project.ID, r.env.ID)
	require.NoError(t, err)
	require.Equal(t, saved, got)

	layout.Nodes[a] = canvas.Pos{X: 99, Y: 98}
	saved, err = r.svc.Put(r.owner, r.project.ID, r.env.ID, 1, layout)
	require.NoError(t, err)
	require.Equal(t, int64(2), saved.Version)
	got, err = r.svc.Get(r.owner, r.project.ID, r.env.ID)
	require.NoError(t, err)
	require.Equal(t, canvas.Pos{X: 99, Y: 98}, got.Layout.Nodes[a])
}

func TestPutFillsInDefaultsForAMinimalLayout(t *testing.T) {
	r := newRig(t)
	saved, err := r.svc.Put(r.owner, r.project.ID, r.env.ID, 0, canvas.Layout{Groups: []canvas.Group{{ID: "g"}}})
	require.NoError(t, err)
	require.NotNil(t, saved.Layout.Nodes)
	require.Equal(t, 1.0, saved.Layout.Viewport.Zoom, "a zero zoom means the default zoom")
	require.Equal(t, []string{}, saved.Layout.Groups[0].ServiceIDs, "a group without services is an empty list, not null")
}

func TestPutWithAStaleVersionIsRefusedAndReportsTheCurrentOne(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.Put(r.owner, r.project.ID, r.env.ID, 0, canvas.Layout{})
	require.NoError(t, err)
	_, err = r.svc.Put(r.owner, r.project.ID, r.env.ID, 1, canvas.Layout{})
	require.NoError(t, err)

	for name, expected := range map[string]int64{"first save again": 0, "old version": 1, "future version": 7} {
		node := uuid.NewString()
		_, err := r.svc.Put(r.owner, r.project.ID, r.env.ID, expected, canvas.Layout{Nodes: map[string]canvas.Pos{node: {X: 1}}})
		ae := requireAPIError(t, err, http.StatusPreconditionFailed, "layout_conflict")
		require.EqualValues(t, 2, ae.Details["currentVersion"], name)
	}
	got, err := r.svc.Get(r.owner, r.project.ID, r.env.ID)
	require.NoError(t, err)
	require.Empty(t, got.Layout.Nodes, "a refused save changed nothing")
	require.Equal(t, int64(2), got.Version)
}

func TestPutRejectsInvalidLayouts(t *testing.T) {
	r := newRig(t)
	tooManyNodes := map[string]canvas.Pos{}
	for range 2001 {
		tooManyNodes[uuid.NewString()] = canvas.Pos{}
	}
	tooManyGroups := make([]canvas.Group, 201)
	for i := range tooManyGroups {
		tooManyGroups[i] = canvas.Group{ID: fmt.Sprint("g", i)}
	}
	manyIDs := make([]string, 2001)
	for i := range manyIDs {
		manyIDs[i] = uuid.NewString()
	}
	longName := string(bytes.Repeat([]byte("n"), 201))

	for name, tc := range map[string]struct {
		layout canvas.Layout
		code   string
	}{
		"too many nodes":         {canvas.Layout{Nodes: tooManyNodes}, "layout_too_large"},
		"too many groups":        {canvas.Layout{Groups: tooManyGroups}, "layout_too_large"},
		"node key not a service": {canvas.Layout{Nodes: map[string]canvas.Pos{"web": {}}}, "invalid_layout"},
		"duplicate group ids":    {canvas.Layout{Groups: []canvas.Group{{ID: "g"}, {ID: "g"}}}, "invalid_layout"},
		"group name too long":    {canvas.Layout{Groups: []canvas.Group{{ID: "g", Name: longName}}}, "invalid_layout"},
		"group too large":        {canvas.Layout{Groups: []canvas.Group{{ID: "g", ServiceIDs: manyIDs}}}, "invalid_layout"},
		"negative zoom":          {canvas.Layout{Viewport: canvas.Viewport{Zoom: -1}}, "invalid_layout"},
	} {
		_, err := r.svc.Put(r.owner, r.project.ID, r.env.ID, 0, tc.layout)
		requireAPIError(t, err, http.StatusUnprocessableEntity, tc.code)
		got, gerr := r.svc.Get(r.owner, r.project.ID, r.env.ID)
		require.NoError(t, gerr, name)
		require.Zero(t, got.Version, "%s: nothing was stored", name)
	}

	// The limits themselves are fine.
	okNodes := map[string]canvas.Pos{}
	for range 2000 {
		okNodes[uuid.NewString()] = canvas.Pos{}
	}
	_, err := r.svc.Put(r.owner, r.project.ID, r.env.ID, 0, canvas.Layout{Nodes: okNodes, Groups: tooManyGroups[:200], Viewport: canvas.Viewport{Zoom: 0.1}})
	require.NoError(t, err)
}

func TestAccessIsScopedToTheCallersWorkspaceProjectsAndScopes(t *testing.T) {
	r := newRig(t)
	bg := context.Background()
	other, err := r.q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: r.ws.ID, Name: "Other", Slug: "other"})
	require.NoError(t, err)
	foreignWS, err := r.q.CreateWorkspace(bg, "foreign")
	require.NoError(t, err)

	readOnly := principal(r.ws.ID, []string{policy.ScopeProjectsRead}, nil)
	_, err = r.svc.Get(readOnly, r.project.ID, r.env.ID)
	require.NoError(t, err)
	_, err = r.svc.Put(readOnly, r.project.ID, r.env.ID, 0, canvas.Layout{})
	requireAPIError(t, err, http.StatusForbidden, "forbidden")

	_, err = r.svc.Get(context.Background(), r.project.ID, r.env.ID)
	requireAPIError(t, err, http.StatusUnauthorized, "unauthorized")
	_, err = r.svc.Put(context.Background(), r.project.ID, r.env.ID, 0, canvas.Layout{})
	requireAPIError(t, err, http.StatusUnauthorized, "unauthorized")

	_, err = r.svc.Get(r.owner, other.ID, r.env.ID)
	requireAPIError(t, err, http.StatusNotFound, "not_found")
	_, err = r.svc.Put(r.owner, other.ID, r.env.ID, 0, canvas.Layout{})
	requireAPIError(t, err, http.StatusNotFound, "not_found")
	_, err = r.svc.Get(r.owner, r.project.ID, uuid.New())
	requireAPIError(t, err, http.StatusNotFound, "not_found")

	_, err = r.svc.Get(principal(foreignWS.ID, policy.AllScopes, nil), r.project.ID, r.env.ID)
	requireAPIError(t, err, http.StatusNotFound, "not_found")

	restricted := principal(r.ws.ID, policy.AllScopes, []uuid.UUID{other.ID})
	_, err = r.svc.Get(restricted, r.project.ID, r.env.ID)
	requireAPIError(t, err, http.StatusNotFound, "not_found")
}

func TestPutAnnouncesTheChangeButARefusedSaveDoesNot(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	feed := r.bus.Subscribe(ctx)

	_, err := r.svc.Put(r.owner, r.project.ID, r.env.ID, 5, canvas.Layout{})
	requireAPIError(t, err, http.StatusPreconditionFailed, "layout_conflict")
	_, err = r.svc.Put(r.owner, r.project.ID, r.env.ID, 0, canvas.Layout{})
	require.NoError(t, err)

	select {
	case ev := <-feed:
		require.Equal(t, events.Event{
			Type: "canvas.updated", ProjectID: r.project.ID.String(), EnvironmentID: r.env.ID.String(), ResourceID: r.env.ID.String(),
		}, ev, "the first event is the successful save's")
	case <-time.After(5 * time.Second):
		t.Fatal("no canvas.updated event")
	}
}

func TestGetAndPutFailOnAStoredLayoutThatCannotBeRead(t *testing.T) {
	r := newRig(t)
	r.rawLayout(r.env.ID, `"not a layout"`)

	_, err := r.svc.Get(r.owner, r.project.ID, r.env.ID)
	require.ErrorContains(t, err, "decode layout")

	// An empty stored document is a valid empty layout.
	empty := r.newEnv("empty")
	r.rawLayout(empty.ID, `{}`)
	c, err := r.svc.Get(r.owner, r.project.ID, empty.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), c.Version)
	require.Equal(t, 1.0, c.Layout.Viewport.Zoom)
	require.Empty(t, c.Layout.Nodes)
}

// ---- HTTP ----

type httpRig struct {
	*rig
	router chi.Router
}

func newHTTPRig(t *testing.T, scopes ...string) *httpRig {
	t.Helper()
	r := newRig(t)
	if len(scopes) == 0 {
		scopes = policy.AllScopes
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), &auth.Principal{
				Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: r.ws.ID, Role: auth.RoleOwner, Scopes: scopes,
			})))
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	canvas.Register(humachi.New(router, cfg), r.svc)
	return &httpRig{rig: r, router: router}
}

func (h *httpRig) do(method, path string, body any, headers ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(h.t, err)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

// layoutBody is a complete PUT body: the schema requires nodes, groups and viewport.
func layoutBody(nodes map[string]any) map[string]any {
	if nodes == nil {
		nodes = map[string]any{}
	}
	return map[string]any{"layout": map[string]any{"nodes": nodes, "groups": []any{}, "viewport": map[string]any{"x": 0, "y": 0, "zoom": 1}}}
}

func (h *httpRig) path() string {
	return fmt.Sprintf("/projects/%s/environments/%s/canvas", h.project.ID, h.env.ID)
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m
}

func TestHTTPGetAndPutUseTheVersionAsETag(t *testing.T) {
	h := newHTTPRig(t)
	node := uuid.NewString()

	rec := h.do(http.MethodGet, h.path(), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, `"0"`, rec.Header().Get("ETag"))
	require.Equal(t, float64(0), decodeBody(t, rec)["version"])

	rec = h.do(http.MethodPut, h.path(), layoutBody(map[string]any{node: map[string]any{"x": 4, "y": 5}}), "If-Match", `"0"`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, `"1"`, rec.Header().Get("ETag"))

	rec = h.do(http.MethodGet, h.path(), nil)
	require.Equal(t, `"1"`, rec.Header().Get("ETag"))
	nodes := decodeBody(t, rec)["layout"].(map[string]any)["nodes"].(map[string]any)
	require.Equal(t, map[string]any{"x": float64(4), "y": float64(5)}, nodes[node])

	// A weak validator is accepted too.
	rec = h.do(http.MethodPut, h.path(), layoutBody(nil), "If-Match", `W/"1"`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, `"2"`, rec.Header().Get("ETag"))
}

func TestHTTPPutPreconditions(t *testing.T) {
	h := newHTTPRig(t)
	body := layoutBody(nil)

	rec := h.do(http.MethodPut, h.path(), body)
	require.Equal(t, http.StatusPreconditionRequired, rec.Code)
	require.Equal(t, "if_match_required", decodeBody(t, rec)["code"])

	rec = h.do(http.MethodPut, h.path(), body, "If-Match", "soon")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_if_match", decodeBody(t, rec)["code"])

	rec = h.do(http.MethodPut, h.path(), body, "If-Match", `"3"`)
	require.Equal(t, http.StatusPreconditionFailed, rec.Code)
	m := decodeBody(t, rec)
	require.Equal(t, "layout_conflict", m["code"])
	require.Equal(t, map[string]any{"currentVersion": float64(0)}, m["details"])

	rec = h.do(http.MethodPut, h.path(), layoutBody(map[string]any{"web": map[string]any{"x": 0, "y": 0}}), "If-Match", `"0"`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "invalid_layout", decodeBody(t, rec)["code"])
}

func TestHTTPEnforcesScopesAndEnvironmentOwnership(t *testing.T) {
	h := newHTTPRig(t, policy.ScopeProjectsRead)
	require.Equal(t, http.StatusOK, h.do(http.MethodGet, h.path(), nil).Code)
	require.Equal(t, http.StatusForbidden, h.do(http.MethodPut, h.path(), layoutBody(nil), "If-Match", `"0"`).Code)

	missing := fmt.Sprintf("/projects/%s/environments/%s/canvas", h.project.ID, uuid.New())
	require.Equal(t, http.StatusNotFound, h.do(http.MethodGet, missing, nil).Code)
}
