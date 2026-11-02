package composeimport_test

import (
	"bytes"
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

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/composeimport"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

func (f *importFixture) post(ctx context.Context, path string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	p, _ := auth.PrincipalFrom(ctx)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	cfg := huma.DefaultConfig("t", "0")
	cfg.CreateHooks = nil
	composeimport.Register(humachi.New(r, cfg), f.svc)
	raw, err := json.Marshal(body)
	require.NoError(f.t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestComposeRoutesPlanThenImport(t *testing.T) {
	f := newImportFixture(t)
	src := readFixture(t, "kv")
	base := "/environments/" + f.env.ID.String() + "/compose-imports"

	w := f.post(f.ctx, base+"/plan", composeimport.ComposePlanRequest{Source: src, ServerID: &f.srv.ID})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var plan struct {
		Installable bool  `json:"installable"`
		Services    []any `json:"services"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &plan))
	require.True(t, plan.Installable)
	require.Len(t, plan.Services, 2)
	require.Empty(t, f.services(), "a plan writes nothing")

	// A reader may plan but not import.
	read := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, WorkspaceID: policy.WorkspaceID(f.ctx), Scopes: []string{policy.ScopeProjectsRead}})
	require.Equal(t, http.StatusOK, f.post(read, base+"/plan", composeimport.ComposePlanRequest{Source: src, ServerID: &f.srv.ID}).Code)
	require.Equal(t, http.StatusForbidden, f.post(read, base, composeimport.ComposeImportRequest{Source: src, ServerID: f.srv.ID}).Code)
	require.Empty(t, f.services())

	w = f.post(f.ctx, base, composeimport.ComposeImportRequest{Source: src, ServerID: f.srv.ID})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var res struct {
		GroupID  string `json:"groupId"`
		Services []any  `json:"services"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	require.NotEmpty(t, res.GroupID)
	require.Len(t, res.Services, 2)
	require.Len(t, f.services(), 2)

	// Unknown environment.
	w = f.post(f.ctx, "/environments/"+uuid.NewString()+"/compose-imports/plan", composeimport.ComposePlanRequest{Source: src, ServerID: &f.srv.ID})
	require.Equal(t, http.StatusNotFound, w.Code)
}
