package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// raw serves one request and returns the recorder (the body unparsed).
func (e *env) raw(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.apiOf(ctx).ServeHTTP(w, req)
	return w
}

func bodyOf(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m), w.Body.String())
	return m
}

func TestIntegrationRoutesConnectTestAndDisconnect(t *testing.T) {
	e := newEnv(t)

	w := e.raw(e.dev, http.MethodGet, "/integrations/cloudflare", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, false, bodyOf(t, w)["configured"])
	w = e.raw(e.dev, http.MethodGet, "/integrations/cloudflare/zones", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Empty(t, bodyOf(t, w)["items"], "no connection, no zones")

	// Only an administrator connects; a rejected token is a 422.
	w = e.raw(e.dev, http.MethodPut, "/integrations/cloudflare", `{"token":"`+secretToken+`"}`)
	require.Equal(t, http.StatusForbidden, w.Code)
	w = e.raw(e.admin, http.MethodPut, "/integrations/cloudflare", `{"token":"wrong-token-value"}`)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.Equal(t, "invalid_token", bodyOf(t, w)["code"])
	require.NotContains(t, w.Body.String(), "wrong-token-value")

	w = e.raw(e.admin, http.MethodPut, "/integrations/cloudflare", `{"token":"`+secretToken+`"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	v := bodyOf(t, w)
	require.Equal(t, true, v["configured"])
	require.EqualValues(t, 1, v["zoneCount"])
	require.NotContains(t, w.Body.String(), secretToken, "the token is write-only")

	w = e.raw(e.dev, http.MethodGet, "/integrations/cloudflare", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, true, bodyOf(t, w)["configured"])
	require.NotContains(t, w.Body.String(), secretToken)
	w = e.raw(e.dev, http.MethodGet, "/integrations/cloudflare/zones", "")
	require.Equal(t, http.StatusOK, w.Code)
	items := bodyOf(t, w)["items"].([]any)
	require.Len(t, items, 1)
	require.Contains(t, w.Body.String(), "example.com")

	// Re-verifying refreshes the zone list: a zone added since shows up.
	e.fake.addZone("z2", "example.org")
	w = e.raw(e.dev, http.MethodPost, "/integrations/cloudflare/test", "")
	require.Equal(t, http.StatusForbidden, w.Code)
	w = e.raw(e.admin, http.MethodPost, "/integrations/cloudflare/test", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.EqualValues(t, 2, bodyOf(t, w)["zoneCount"])

	// Disconnecting is for administrators and a second time is a 404.
	w = e.raw(e.dev, http.MethodDelete, "/integrations/cloudflare", "")
	require.Equal(t, http.StatusForbidden, w.Code)
	w = e.raw(e.admin, http.MethodDelete, "/integrations/cloudflare", "")
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	w = e.raw(e.dev, http.MethodGet, "/integrations/cloudflare", "")
	require.Equal(t, false, bodyOf(t, w)["configured"])
	w = e.raw(e.admin, http.MethodDelete, "/integrations/cloudflare", "")
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestDomainDNSRoutes(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	e.connect(t)
	d := e.domain(t, "app.example.com", "")
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	path := "/domains/" + d.ID.String() + "/dns"

	w := e.raw(e.dev, http.MethodGet, path, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	st := bodyOf(t, w)
	require.Equal(t, StatusManaged, st["status"])
	require.Equal(t, "z1", st["zoneId"])
	require.Equal(t, true, st["manage"])
	require.Equal(t, false, st["needsAdopt"])

	// The proxy toggle changes the record in Cloudflare.
	before := e.fake.records("app.example.com")
	require.Len(t, before, 1)
	proxied := before[0].Proxied
	w = e.raw(e.dev, http.MethodPut, path, `{"proxied":`+map[bool]string{true: "false", false: "true"}[proxied]+`}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, !proxied, bodyOf(t, w)["proxied"])
	require.Equal(t, !proxied, e.fake.records("app.example.com")[0].Proxied)

	// A record somebody else owns is replaced only on request, by an administrator.
	w = e.raw(e.dev, http.MethodPost, path+"/fix", `{"adopt":true}`)
	require.Equal(t, http.StatusForbidden, w.Code, "adopt is for administrators")
	w = e.raw(e.dev, http.MethodPost, path+"/fix", `{}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, StatusManaged, bodyOf(t, w)["status"])

	w = e.raw(e.other, http.MethodGet, path, "")
	require.Equal(t, http.StatusNotFound, w.Code, "another team's domain is not visible")
	w = e.raw(e.dev, http.MethodGet, "/domains/00000000-0000-0000-0000-000000000000/dns", "")
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestStatusPageDNSRoutes(t *testing.T) {
	e := newEnv(t)
	e.controlPlane(t, ipA)
	e.connect(t)
	id := e.page(t, "status.example.com", false)
	path := "/status-pages/" + id.String() + "/cloudflare"

	w := e.raw(e.dev, http.MethodGet, path, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	st := bodyOf(t, w)
	require.Equal(t, true, st["configured"])
	require.Equal(t, "z1", st["zoneId"])
	require.Len(t, st["desiredRecords"], 1, "the control plane's address is wanted")

	// Changing and fixing are administrator actions.
	w = e.raw(e.dev, http.MethodPut, path, `{"proxied":true}`)
	require.Equal(t, http.StatusForbidden, w.Code)
	w = e.raw(e.dev, http.MethodPost, path+"/fix", `{}`)
	require.Equal(t, http.StatusForbidden, w.Code)
	w = e.raw(e.admin, http.MethodPut, path, `{"proxied":true}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, true, bodyOf(t, w)["proxied"])
	require.True(t, e.pageRow(t, id).CfProxied)
	w = e.raw(e.admin, http.MethodPost, path+"/fix", `{"adopt":false}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, StatusManaged, bodyOf(t, w)["status"])
	require.Len(t, e.recordsByType("status.example.com"), 1)

	w = e.raw(e.admin, http.MethodGet, "/status-pages/00000000-0000-0000-0000-000000000000/cloudflare", "")
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestCloudflareIPRoutes(t *testing.T) {
	e := newEnv(t)
	ips := e.useIPs(t)

	w := e.raw(e.dev, http.MethodGet, "/cloudflare/ips", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, IPSourceBuiltin, bodyOf(t, w)["source"])

	w = e.raw(e.dev, http.MethodPost, "/cloudflare/ips/refresh", "")
	require.Equal(t, http.StatusForbidden, w.Code)
	w = e.raw(e.admin, http.MethodPost, "/cloudflare/ips/refresh", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, IPSourceAPI, bodyOf(t, w)["source"])

	ips.failAll()
	w = e.raw(e.admin, http.MethodPost, "/cloudflare/ips/refresh", "")
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Equal(t, "ip_refresh_failed", bodyOf(t, w)["code"])
	w = e.raw(e.dev, http.MethodGet, "/cloudflare/ips", "")
	require.Equal(t, IPSourceAPI, bodyOf(t, w)["source"], "the last good list stays in use")
}
