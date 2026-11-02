package tunnel

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
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// do runs one request against the tunnel endpoints as ctx's principal.
func (f *fixture) do(ctx context.Context, method, serverID, suffix string) *httptest.ResponseRecorder {
	f.t.Helper()
	p, ok := auth.PrincipalFrom(ctx)
	require.True(f.t, ok)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	Register(humachi.New(r, cfg), f.svc)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, "/servers/"+serverID+"/tunnel"+suffix, nil))
	return rec
}

func decodeView(t *testing.T, rec *httptest.ResponseRecorder) TunnelView {
	t.Helper()
	var v TunnelView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v), rec.Body.String())
	return v
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e), rec.Body.String())
	return e.Code
}

func TestTunnelLifecycleOverHTTP(t *testing.T) {
	f := newFixture(t)
	f.deployed("web", "app.example.com", true)
	id := f.server.ID.String()

	// A server without a tunnel says so and whether one can be enabled.
	rec := f.do(f.viewer, http.MethodGet, id, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	v := decodeView(t, rec)
	require.False(t, v.Enabled)
	require.Equal(t, StateOff, v.State)
	require.True(t, v.CloudflareConnected)
	require.True(t, v.AgentSupported)

	// Enabling answers 202 with the provisioning state and queues the connector.
	rec = f.do(f.admin, http.MethodPost, id, "")
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	v = decodeView(t, rec)
	require.True(t, v.Enabled)
	require.Equal(t, "provisioning", v.State)
	require.Equal(t, tunnelID, v.TunnelID)
	require.Equal(t, tunnelID+".cfargotunnel.com", v.CNAME)
	require.Equal(t, 1, v.IngressHosts)
	require.Len(t, f.disp.of(agentprotocol.KindSyncTunnel), 1)

	rec = f.do(f.viewer, http.MethodGet, id, "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, tunnelID, decodeView(t, rec).TunnelID)

	// Enabling twice is a conflict and creates nothing more.
	rec = f.do(f.admin, http.MethodPost, id, "")
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "tunnel_exists", errorCode(t, rec))
	require.Equal(t, 1, f.api.count("POST /accounts/"+account+"/cfd_tunnel"), "no second tunnel at Cloudflare")

	// Re-sync pushes the ingress rules again and re-sends the connector command.
	puts := f.api.count("PUT ")
	rec = f.do(f.admin, http.MethodPost, id, "/sync")
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Equal(t, puts+1, f.api.count("PUT "), "a re-sync always pushes")
	require.Len(t, f.disp.of(agentprotocol.KindSyncTunnel), 2)

	// Disable answers 202 with the removing state; the delete is queued.
	rec = f.do(f.admin, http.MethodDelete, id, "")
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	v = decodeView(t, rec)
	require.Equal(t, "removing", v.State)
	require.False(t, v.Active)
	require.Len(t, f.deleteJobArgs(), 1)
	require.Len(t, f.disp.of(agentprotocol.KindRemoveTunnel), 1)

	// A removing tunnel cannot be enabled or re-synced.
	rec = f.do(f.admin, http.MethodPost, id, "")
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "tunnel_removing", errorCode(t, rec))
	rec = f.do(f.admin, http.MethodPost, id, "/sync")
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "tunnel_removing", errorCode(t, rec))
}

func TestDisableForceOverHTTPDropsTheRecord(t *testing.T) {
	f := newFixture(t)
	f.enable()
	id := f.server.ID.String()

	rec := f.do(f.admin, http.MethodDelete, id, "?force=true")
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.False(t, decodeView(t, rec).Enabled, "a forced disable forgets the tunnel at once")
	require.False(t, f.api.has("DELETE /accounts/"+account+"/cfd_tunnel/"+tunnelID), "Cloudflare is not asked")
	_, err := f.q.GetServerTunnel(bg, f.server.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
}

func TestTunnelEndpointsEnforceScopesAndTenancy(t *testing.T) {
	f := newFixture(t)
	id := f.server.ID.String()

	for _, c := range []struct{ method, suffix string }{
		{http.MethodPost, ""}, {http.MethodDelete, ""}, {http.MethodPost, "/sync"},
	} {
		rec := f.do(f.viewer, c.method, id, c.suffix)
		require.Equal(t, http.StatusForbidden, rec.Code, c.method+c.suffix)
	}
	require.Empty(t, f.api.calls, "a forbidden caller never reaches Cloudflare")

	// Another team's server is not found, and a missing tunnel is not found.
	other := f.other.ID.String()
	for _, c := range []struct{ method, suffix string }{
		{http.MethodGet, ""}, {http.MethodPost, ""}, {http.MethodDelete, ""}, {http.MethodPost, "/sync"},
	} {
		rec := f.do(f.admin, c.method, other, c.suffix)
		require.Equal(t, http.StatusNotFound, rec.Code, c.method+c.suffix)
		require.Equal(t, "not_found", errorCode(t, rec))
	}
	rec := f.do(f.admin, http.MethodGet, uuid.NewString(), "")
	require.Equal(t, http.StatusNotFound, rec.Code)
	rec = f.do(f.admin, http.MethodDelete, id, "")
	require.Equal(t, http.StatusNotFound, rec.Code, "nothing to disable")
	rec = f.do(f.admin, http.MethodPost, id, "/sync")
	require.Equal(t, http.StatusNotFound, rec.Code, "nothing to re-sync")
	require.Equal(t, http.StatusUnprocessableEntity, f.do(f.admin, http.MethodGet, "not-a-uuid", "").Code, "the id is validated")
}

func TestEnableOverHTTPMapsCloudflareFailures(t *testing.T) {
	f := newFixture(t)
	id := f.server.ID.String()

	f.cf.notConfigured = true
	rec := f.do(f.admin, http.MethodPost, id, "")
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "cloudflare_not_connected", errorCode(t, rec))
	f.cf.notConfigured = false

	f.api.mu.Lock()
	f.api.deny = true
	f.api.mu.Unlock()
	rec = f.do(f.admin, http.MethodPost, id, "")
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "cloudflare_tunnel_forbidden", errorCode(t, rec))
	f.api.mu.Lock()
	f.api.deny = false
	f.api.failPut = true
	f.api.mu.Unlock()

	rec = f.do(f.admin, http.MethodPost, id, "")
	require.Equal(t, http.StatusBadGateway, rec.Code, "a Cloudflare refusal is a 502")
	require.Equal(t, "cloudflare_error", errorCode(t, rec))
	require.Contains(t, rec.Body.String(), "ingress invalid")
}

func TestResyncRefusals(t *testing.T) {
	f := newFixture(t)

	_, err := f.svc.Resync(f.admin, f.server.ID)
	require.Equal(t, "not_found", apiCode(t, err), "no tunnel yet")

	f.enable()
	_, err = f.svc.Resync(f.viewer, f.server.ID)
	require.Equal(t, "forbidden", apiCode(t, err))

	// Cloudflare refuses the ingress push: the error is mapped and the failure recorded.
	f.api.mu.Lock()
	f.api.failPut = true
	f.api.mu.Unlock()
	before := len(f.disp.of(agentprotocol.KindSyncTunnel))
	_, err = f.svc.Resync(f.admin, f.server.ID)
	require.Equal(t, "cloudflare_error", apiCode(t, err))
	require.Contains(t, f.row().IngressError, "ingress invalid")
	require.Len(t, f.disp.of(agentprotocol.KindSyncTunnel), before, "the connector command is not re-sent after a failed push")
	f.api.mu.Lock()
	f.api.failPut = false
	f.api.mu.Unlock()

	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'revoked' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.svc.Resync(f.admin, f.server.ID)
	require.Equal(t, "server_revoked", apiCode(t, err))
}
