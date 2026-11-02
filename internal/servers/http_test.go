package servers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/servers"
)

// httpFx is the remove fixture with a request helper.
type httpFx struct {
	*removeFixture
	t *testing.T
}

func newHTTPFx(t *testing.T) *httpFx { return &httpFx{removeFixture: newRemoveFixture(t), t: t} }

// nobody is a caller of the team without any server scope.
func (f *httpFx) nobody() context.Context {
	p, _ := auth.PrincipalFrom(f.admin)
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: p.UserID, WorkspaceID: p.WorkspaceID, Scopes: []string{policy.ScopeProjectsRead},
	})
}

func testBox(t *testing.T) *secretbox.Box {
	t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte("k"), secretbox.KeySize))
	require.NoError(t, err)
	return box
}

// do runs one request against the server endpoints as ctx's principal.
func (f *httpFx) do(ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
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
	servers.Register(humachi.New(r, cfg), f.svc)

	var rd *bytes.Reader
	if body == nil {
		rd = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		require.NoError(f.t, err)
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

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v), rec.Body.String())
	return v
}

func codeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	return decodeBody[struct {
		Code string `json:"code"`
	}](t, rec).Code
}

// readOnly is a caller who may only read servers.
func (f *httpFx) readOnly() context.Context {
	p, _ := auth.PrincipalFrom(f.admin)
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: p.UserID, WorkspaceID: p.WorkspaceID, Scopes: []string{policy.ScopeServersRead},
	})
}

func TestEnrollmentTokenAndServerListOverHTTP(t *testing.T) {
	f := newHTTPFx(t)

	rec := f.do(f.admin, http.MethodPost, "/servers/enrollment-tokens", map[string]any{
		"name": "worker-1", "publicIp": "203.0.113.9", "baseDomain": "Apps.Example.com", "acmeEmail": "ops@example.com",
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	tok := decodeBody[servers.EnrollmentToken](t, rec)
	require.Equal(t, "pending", tok.Server.Status)
	require.Equal(t, "worker-1", tok.Server.Name)
	require.NotNil(t, tok.Server.PublicIP)
	require.Equal(t, "203.0.113.9", *tok.Server.PublicIP)
	require.NotNil(t, tok.Server.BaseDomain)
	require.Equal(t, "apps.example.com", *tok.Server.BaseDomain, "base domains are lower-cased")
	require.Contains(t, tok.InstallCommand, tok.Token)
	require.Contains(t, tok.InstallCommand, "--insecure-dev", "the dev control plane is plain http")

	for name, body := range map[string]map[string]any{
		"empty name":  {"name": ""},
		"bad ip":      {"name": "x", "publicIp": "not-an-ip"},
		"bad domain":  {"name": "x", "baseDomain": "Bad_Domain!"},
		"bad email":   {"name": "x", "acmeEmail": "nope"},
		"unspecified": {"name": "x", "publicIp": "0.0.0.0"},
	} {
		rec = f.do(f.admin, http.MethodPost, "/servers/enrollment-tokens", body)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, name+": "+rec.Body.String())
	}
	require.Equal(t, "invalid_public_ip", codeOf(t, f.do(f.admin, http.MethodPost, "/servers/enrollment-tokens", map[string]any{"name": "x", "publicIp": "999.1.1.1"})))
	require.Equal(t, "invalid_base_domain", codeOf(t, f.do(f.admin, http.MethodPost, "/servers/enrollment-tokens", map[string]any{"name": "x", "baseDomain": "no spaces"})))
	require.Equal(t, "invalid_acme_email", codeOf(t, f.do(f.admin, http.MethodPost, "/servers/enrollment-tokens", map[string]any{"name": "x", "acmeEmail": "Ops <ops@example.com>"})))
	require.Equal(t, http.StatusForbidden, f.do(f.readOnly(), http.MethodPost, "/servers/enrollment-tokens", map[string]any{"name": "x"}).Code)

	// Newest first, paged by cursor.
	rec = f.do(f.admin, http.MethodGet, "/servers?limit=1", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	first := decodeBody[api.Page[servers.Server]](t, rec)
	require.Len(t, first.Items, 1)
	require.Equal(t, tok.Server.ID, first.Items[0].ID, "the newest server comes first")
	require.NotEmpty(t, first.NextCursor)
	rec = f.do(f.admin, http.MethodGet, "/servers?limit=1&cursor="+url.QueryEscape(first.NextCursor), nil)
	second := decodeBody[api.Page[servers.Server]](t, rec)
	require.Len(t, second.Items, 1)
	require.Equal(t, f.server.ID, second.Items[0].ID)
	require.Empty(t, second.NextCursor)
	require.Equal(t, http.StatusBadRequest, f.do(f.admin, http.MethodGet, "/servers?cursor=garbage", nil).Code)
	require.Equal(t, http.StatusForbidden, f.do(f.nobody(), http.MethodGet, "/servers", nil).Code, "listing needs servers:read")
}

func TestReissueAndGetServerOverHTTP(t *testing.T) {
	f := newHTTPFx(t)
	rec := f.do(f.admin, http.MethodPost, "/servers/enrollment-tokens", map[string]any{"name": "worker"})
	require.Equal(t, http.StatusCreated, rec.Code)
	tok := decodeBody[servers.EnrollmentToken](t, rec)
	id := tok.Server.ID.String()

	rec = f.do(f.admin, http.MethodPost, "/servers/"+id+"/enrollment-token", nil)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	again := decodeBody[servers.EnrollmentToken](t, rec)
	require.NotEqual(t, tok.Token, again.Token)

	rec = f.do(f.readOnly(), http.MethodGet, "/servers/"+id, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "worker", decodeBody[servers.Server](t, rec).Name)
	require.Equal(t, http.StatusForbidden, f.do(f.readOnly(), http.MethodPost, "/servers/"+id+"/enrollment-token", nil).Code)

	f.goOnline(t, tok.Server.ID, true)
	rec = f.do(f.admin, http.MethodPost, "/servers/"+id+"/enrollment-token", nil)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "server_not_pending", codeOf(t, rec))

	missing := "/servers/" + uuid.NewString()
	require.Equal(t, http.StatusNotFound, f.do(f.admin, http.MethodGet, missing, nil).Code)
	require.Equal(t, http.StatusNotFound, f.do(f.admin, http.MethodPost, missing+"/enrollment-token", nil).Code)
	require.Equal(t, http.StatusNotFound, f.do(f.admin, http.MethodPost, missing+"/revoke", nil).Code)
	require.Equal(t, http.StatusNotFound, f.do(f.admin, http.MethodPatch, missing, map[string]any{"name": "x"}).Code)
}

func TestUpdateServerOverHTTP(t *testing.T) {
	f := newHTTPFx(t)
	path := "/servers/" + f.server.ID.String()

	rec := f.do(f.admin, http.MethodPatch, path, map[string]any{
		"name": "  edge-2 ", "publicIp": "2001:db8::1", "baseDomain": "*.Edge.example.com", "acmeEmail": "ops@example.com",
		"metricsRetentionDays": 14, "diskAlertThresholdPercent": 90, "trafficAnalyticsEnabled": false,
		"trafficHourlyRetentionDays": 3, "trafficDailyRetentionDays": 30, "acceptWorkloads": false,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	s := decodeBody[servers.Server](t, rec)
	require.Equal(t, "edge-2", s.Name, "the name is trimmed")
	require.Equal(t, "2001:db8::1", *s.PublicIP)
	require.Equal(t, "edge.example.com", *s.BaseDomain, "a wildcard prefix is stripped")
	require.Equal(t, "ops@example.com", *s.ACMEEmail)
	require.Equal(t, 14, s.MetricsRetentionDays)
	require.Equal(t, 90, s.DiskAlertThresholdPercent)
	require.False(t, s.TrafficAnalyticsEnabled)
	require.Equal(t, 3, s.TrafficHourlyRetentionDays)
	require.Equal(t, 30, s.TrafficDailyRetentionDays)
	require.False(t, s.AcceptWorkloads)

	// Empty strings clear the optional host fields.
	rec = f.do(f.admin, http.MethodPatch, path, map[string]any{"publicIp": "", "baseDomain": "", "acmeEmail": ""})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	s = decodeBody[servers.Server](t, rec)
	require.Nil(t, s.PublicIP)
	require.Nil(t, s.BaseDomain)
	require.Nil(t, s.ACMEEmail)
	require.Equal(t, "edge-2", s.Name, "fields that were not sent stay")

	// The build role.
	rec = f.do(f.admin, http.MethodPatch, path, map[string]any{"buildEnabled": true, "buildMaxConcurrent": 4, "buildAllowEmulation": true, "buildAllowPreviews": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	s = decodeBody[servers.Server](t, rec)
	require.True(t, s.BuildEnabled)
	require.Equal(t, 4, s.BuildMaxConcurrent)
	require.True(t, s.BuildAllowEmulation)
	require.True(t, s.BuildAllowPreviews)
	require.Len(t, f.audit(t, "server.build_role"), 1, "a role change is audited on its own")

	for name, body := range map[string]map[string]any{
		"name too long":         {"name": string(bytes.Repeat([]byte("x"), 101))},
		"retention too low":     {"metricsRetentionDays": 0},
		"retention too high":    {"metricsRetentionDays": 91},
		"disk threshold low":    {"diskAlertThresholdPercent": 49},
		"hourly traffic low":    {"trafficHourlyRetentionDays": 1},
		"daily traffic low":     {"trafficDailyRetentionDays": 6},
		"build concurrency 0":   {"buildMaxConcurrent": 0},
		"build concurrency 17":  {"buildMaxConcurrent": 17},
		"geo source unknown":    {"geoSource": "ip2location"},
		"geo level unknown":     {"geoLevel": "street"},
		"geo refresh too large": {"geoRefreshDays": 91},
	} {
		rec = f.do(f.admin, http.MethodPatch, path, body)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, name)
	}
	require.Equal(t, http.StatusForbidden, f.do(f.readOnly(), http.MethodPatch, path, map[string]any{"name": "x"}).Code)
	require.Equal(t, "validation_failed", codeOf(t, f.do(f.admin, http.MethodPatch, path, map[string]any{"name": "   "})))
}

func TestRevokeServerOverHTTP(t *testing.T) {
	f := newHTTPFx(t)
	f.goOnline(t, f.server.ID, true)
	path := "/servers/" + f.server.ID.String() + "/revoke"

	require.Equal(t, http.StatusForbidden, f.do(f.readOnly(), http.MethodPost, path, nil).Code)
	rec := f.do(f.admin, http.MethodPost, path, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "revoked", decodeBody[servers.Server](t, rec).Status)

	rec = f.do(f.admin, http.MethodPost, path, nil)
	require.Equal(t, http.StatusOK, rec.Code, "revoking twice is harmless")
	require.Equal(t, "revoked", decodeBody[servers.Server](t, rec).Status)
}

func TestRemoveServerOverHTTPAnswers202ForCleanAnd200ForForget(t *testing.T) {
	f := newHTTPFx(t)
	f.goOnline(t, f.server.ID, true)
	path := "/servers/" + f.server.ID.String()

	rec := f.do(f.admin, http.MethodDelete, path+"?confirm=wrong", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.True(t, f.exists(t, f.server.ID))
	require.Equal(t, http.StatusUnprocessableEntity, f.do(f.admin, http.MethodDelete, path+"?confirm=edge&mode=nuke", nil).Code, "mode is an enum")
	require.Equal(t, http.StatusForbidden, f.do(f.readOnly(), http.MethodDelete, path+"?confirm=edge", nil).Code)

	// Clean (the default) hands the cleanup to the queue and says so.
	rec = f.do(f.admin, http.MethodDelete, path+"?confirm=edge", nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	res := decodeBody[servers.ServerRemoval](t, rec)
	require.False(t, res.Removed)
	require.Len(t, f.queued, 1)
	require.True(t, f.exists(t, f.server.ID), "the server stays until the cleanup finished")

	// Forget deletes the row at once and leaves the host alone.
	other := newHTTPFx(t)
	other.goOnline(t, other.server.ID, true)
	rec = other.do(other.admin, http.MethodDelete, "/servers/"+other.server.ID.String()+"?confirm=edge&mode=forget", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	res = decodeBody[servers.ServerRemoval](t, rec)
	require.True(t, res.Removed)
	require.False(t, other.exists(t, other.server.ID))
	require.Empty(t, other.agent.sent())
}

func TestUnrestrictedOverHTTP(t *testing.T) {
	f := newHTTPFx(t)
	path := "/servers/" + f.server.ID.String() + "/unrestricted"

	rec := f.do(f.readOnly(), http.MethodGet, path, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.False(t, decodeBody[servers.UnrestrictedSettings](t, rec).Unrestricted)

	rec = f.do(f.admin, http.MethodPut, path, map[string]any{"unrestricted": true, "confirm": "not-the-name"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	rec = f.do(f.admin, http.MethodPut, path, map[string]any{"unrestricted": true, "confirm": "edge"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, decodeBody[servers.UnrestrictedSettings](t, rec).Unrestricted)
	require.True(t, decodeBody[servers.UnrestrictedSettings](t, f.do(f.readOnly(), http.MethodGet, path, nil)).Unrestricted)

	require.Equal(t, http.StatusForbidden, f.do(f.readOnly(), http.MethodPut, path, map[string]any{"unrestricted": false}).Code)
	rec = f.do(f.admin, http.MethodPut, path, map[string]any{"unrestricted": false})
	require.Equal(t, http.StatusOK, rec.Code, "turning it off needs no confirmation")
	require.False(t, decodeBody[servers.UnrestrictedSettings](t, rec).Unrestricted)
	require.Equal(t, http.StatusNotFound, f.do(f.admin, http.MethodGet, "/servers/"+uuid.NewString()+"/unrestricted", nil).Code)
}

func TestServerMetricsOverHTTP(t *testing.T) {
	f := newHTTPFx(t)
	path := "/servers/" + f.server.ID.String() + "/metrics"

	rec := f.do(f.readOnly(), http.MethodGet, path, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	res := decodeBody[struct {
		Points        []servers.MetricPoint `json:"points"`
		StepSeconds   int                   `json:"stepSeconds"`
		RetentionDays int                   `json:"retentionDays"`
	}](t, rec)
	require.Empty(t, res.Points, "a server that never reported has an empty series")
	require.Positive(t, res.StepSeconds)
	require.Equal(t, 7, res.RetentionDays)

	require.Equal(t, http.StatusUnprocessableEntity, f.do(f.admin, http.MethodGet, path+"?range=2y", nil).Code, "the range is an enum")
	require.Equal(t, http.StatusNotFound, f.do(f.admin, http.MethodGet, "/servers/"+uuid.NewString()+"/metrics", nil).Code)
}

func TestMaxMindCredentialsOverHTTP(t *testing.T) {
	f := newHTTPFx(t)
	f.svc.SetBox(testBox(t))

	rec := f.do(f.readOnly(), http.MethodGet, "/geo/maxmind", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.False(t, decodeBody[servers.GeoCredentialsStatus](t, rec).Configured)

	rec = f.do(f.admin, http.MethodPut, "/geo/maxmind", map[string]any{"accountId": "123456", "licenseKey": "sekret-license-key-0123456789"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	st := decodeBody[servers.GeoCredentialsStatus](t, rec)
	require.True(t, st.Configured)
	require.Equal(t, "123456", st.AccountID)
	require.NotContains(t, rec.Body.String(), "sekret-license-key", "the key is write-only")

	require.Equal(t, http.StatusUnprocessableEntity, f.do(f.admin, http.MethodPut, "/geo/maxmind", map[string]any{"accountId": "123456", "licenseKey": "short"}).Code)
	rec = f.do(f.admin, http.MethodPut, "/geo/maxmind", map[string]any{"accountId": "abc", "licenseKey": "sekret-license-key-0123456789"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "invalid_maxmind_account", codeOf(t, rec))
	require.Equal(t, http.StatusForbidden, f.do(f.readOnly(), http.MethodPut, "/geo/maxmind", map[string]any{"accountId": "123456", "licenseKey": "sekret-license-key-0123456789"}).Code)

	// A server on the MaxMind source keeps the account.
	rec = f.do(f.admin, http.MethodPatch, "/servers/"+f.server.ID.String(), map[string]any{"geoSource": "maxmind"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = f.do(f.admin, http.MethodDelete, "/geo/maxmind", nil)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "maxmind_in_use", codeOf(t, rec))
	f.do(f.admin, http.MethodPatch, "/servers/"+f.server.ID.String(), map[string]any{"geoSource": "dbip"})
	rec = f.do(f.admin, http.MethodDelete, "/geo/maxmind", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.False(t, decodeBody[servers.GeoCredentialsStatus](t, rec).Configured)
}
