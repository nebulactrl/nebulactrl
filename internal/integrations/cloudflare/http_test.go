package cloudflare

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
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
)

// apiOf serves the Cloudflare endpoints for the caller of ctx.
func (e *env) apiOf(ctx context.Context) http.Handler {
	p, _ := auth.PrincipalFrom(ctx)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	Register(humachi.New(r, huma.DefaultConfig("t", "0")), e.svc)
	return r
}

func (e *env) call(h http.Handler, method, path, body string) (int, map[string]any) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// DNS of a hostname in a connected zone is managed, full stop: the API has no
// switch for it, and a client that sends one is refused instead of obeyed.
func TestTheAPIHasNoSwitchToTurnManagementOff(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	e.connect(t)
	d := e.domain(t, "app.example.com", "")
	h := e.apiOf(e.dev)

	code, _ := e.call(h, http.MethodPut, "/domains/"+d.ID.String()+"/dns", `{"manage":false}`)
	require.Equal(t, http.StatusUnprocessableEntity, code, "an unknown field is refused")
	code, _ = e.call(h, http.MethodPut, "/domains/"+d.ID.String()+"/dns", `{"manageDns":false}`)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	code, _ = e.call(e.apiOf(e.admin), http.MethodPut, "/integrations/cloudflare", `{"autoManage":false}`)
	require.Equal(t, http.StatusUnprocessableEntity, code, "the team has no switch either")

	// Nothing changed: the domain is managed and its record is created.
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	code, st := e.call(h, http.MethodGet, "/domains/"+d.ID.String()+"/dns", "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, st["manage"], "manage is read-only and true in a connected zone")
	require.Equal(t, "managed", st["status"])
	require.Len(t, e.fake.records("z1", "app.example.com"), 1)

	// The proxy setting stays a choice.
	code, st = e.call(h, http.MethodPut, "/domains/"+d.ID.String()+"/dns", `{"proxied":false}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, false, st["proxied"])
	require.Equal(t, true, st["manage"])
}
