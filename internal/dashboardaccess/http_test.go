package dashboardaccess

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
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/systemroute"
	"github.com/nebulactrl/nebulactrl/internal/systemroute/systemroutetest"
)

// httpCall drives one request through the registered endpoints as the principal
// carried by ctx (a ctx without one is an anonymous caller). remote and
// forwardedFor describe how the request reached the control plane.
func httpCall(t *testing.T, ctx context.Context, svc *Service, method, remote, forwardedFor string, body any) *httptest.ResponseRecorder {
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
	req := httptest.NewRequest(method, "/instance/dashboard-access", rd)
	req.RemoteAddr = remote
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
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

const routePeer = "10.212.0.3:40000" // the host proxy's address on the control plane

func TestHTTPGetReportsTheCallerAsTheAllowListSeesThem(t *testing.T) {
	e := newEnv(t, config.Installer{})

	// Through the host proxy: the forwarded client counts.
	rec := httpCall(t, e.ctx, e.svc, http.MethodGet, routePeer, "203.0.113.9", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m := httpJSON(t, rec)
	require.Equal(t, "203.0.113.9", m["clientIp"])
	require.Equal(t, true, m["viaDashboardRoute"])
	require.Equal(t, true, m["clientIpAllowed"])
	require.Equal(t, host, m["host"])
	require.Equal(t, "default", m["source"])
	require.Equal(t, []any{}, m["allowCidrs"])

	// Reached directly, the TCP peer is the client and the route's list does not apply.
	rec = httpCall(t, e.ctx, e.svc, http.MethodGet, "198.51.100.20:5555", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m = httpJSON(t, rec)
	require.Equal(t, "198.51.100.20", m["clientIp"])
	require.Equal(t, false, m["viaDashboardRoute"])

	// A forged X-Forwarded-For from a direct peer is not believed.
	rec = httpCall(t, e.ctx, e.svc, http.MethodGet, "198.51.100.20:5555", "100.64.0.1", nil)
	require.Equal(t, "198.51.100.20", httpJSON(t, rec)["clientIp"])
}

func TestHTTPUpdateStoresTheAllowListAndGuardsAgainstLockout(t *testing.T) {
	e := newEnv(t, config.Installer{})
	body := map[string]any{"allowCidrs": []string{"100.64.0.0/10"}}

	// The caller (203.0.113.9 through the route) would be locked out.
	rec := httpCall(t, e.ctx, e.svc, http.MethodPut, routePeer, "203.0.113.9", body)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "dashboard_lockout", httpJSON(t, rec)["code"])
	require.Zero(t, e.push.count(), "a refused update pushes nothing")

	// Confirming the lockout saves it and re-sends the routes.
	body["confirmLockout"] = true
	rec = httpCall(t, e.ctx, e.svc, http.MethodPut, routePeer, "203.0.113.9", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m := httpJSON(t, rec)
	require.Equal(t, []any{"100.64.0.0/10"}, m["allowCidrs"])
	require.Equal(t, false, m["clientIpAllowed"])
	require.Equal(t, "admin", m["source"])
	require.Equal(t, 1, e.push.count())
	require.Equal(t, []string{"100.64.0.0/10"}, e.push.pushes[0].AllowCIDRs)

	// A caller inside the list needs no confirmation; an empty list lifts the restriction.
	rec = httpCall(t, e.ctx, e.svc, http.MethodPut, routePeer, "100.64.0.9", map[string]any{"allowCidrs": []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, true, httpJSON(t, rec)["clientIpAllowed"])
	rec = httpCall(t, e.ctx, e.svc, http.MethodPut, routePeer, "100.64.0.9", map[string]any{"allowCidrs": []string{}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []any{}, httpJSON(t, rec)["allowCidrs"])
}

func TestHTTPUpdateRejectsBadInput(t *testing.T) {
	e := newEnv(t, config.Installer{})
	rec := httpCall(t, e.ctx, e.svc, http.MethodPut, "198.51.100.20:5555", "", map[string]any{"allowCidrs": []string{"not-a-cidr"}})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

	// The control-plane server has no Cloudflare resolver.
	rec = httpCall(t, e.ctx, e.svc, http.MethodPut, "198.51.100.20:5555", "", map[string]any{"allowCidrs": []string{}, "dnsResolver": "cloudflare"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "resolver_not_configured", httpJSON(t, rec)["code"])

	tooMany := make([]string, 201)
	for i := range tooMany {
		tooMany[i] = "10.0.0.1"
	}
	rec = httpCall(t, e.ctx, e.svc, http.MethodPut, "198.51.100.20:5555", "", map[string]any{"allowCidrs": tooMany})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "the list is capped at 200 entries")
	require.Zero(t, e.push.count())
}

func TestHTTPNeedsTheInstanceAdmin(t *testing.T) {
	e := newEnv(t, config.Installer{})
	member, err := e.q.CreateUser(context.Background(), db.CreateUserParams{WorkspaceID: e.ws, Email: "m@example.com", Name: "M", PasswordHash: "x"})
	require.NoError(t, err)
	owner := principalFor(member.ID, e.ws)
	scoped := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, UserID: e.admin, WorkspaceID: e.ws, Scopes: []string{policy.ScopeServersRead}})

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		body := any(nil)
		if method == http.MethodPut {
			body = map[string]any{"allowCidrs": []string{}}
		}
		require.Equal(t, http.StatusForbidden, httpCall(t, owner, e.svc, method, "198.51.100.20:5555", "", body).Code, method+" by a team owner")
		require.Equal(t, http.StatusForbidden, httpCall(t, scoped, e.svc, method, "198.51.100.20:5555", "", body).Code, method+" by a token without admin scope")
		require.Equal(t, http.StatusUnauthorized, httpCall(t, context.Background(), e.svc, method, "198.51.100.20:5555", "", body).Code, method+" anonymous")
	}
	require.Zero(t, e.push.count())
}

func TestHTTPUpdateOfAnInstanceWithoutADashboardRoute(t *testing.T) {
	e := newEnv(t, config.Installer{})
	systemroutetest.Set(systemroute.Config{})
	rec := httpCall(t, e.ctx, e.svc, http.MethodGet, "198.51.100.20:5555", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, false, httpJSON(t, rec)["enabled"])
	rec = httpCall(t, e.ctx, e.svc, http.MethodPut, "198.51.100.20:5555", "", map[string]any{"allowCidrs": []string{"100.64.0.0/10"}})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "dashboard_route_disabled", httpJSON(t, rec)["code"])
}

func TestSeederRunWalksTheStepsWithGrowingBackoff(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, seedEnv)
	require.NoError(t, e.svc.Load(ctx))
	fp := &fakeProxy{e: e}
	s := e.seeder(t, fp, seedEnv)
	var waits []time.Duration
	s.sleep = func(_ context.Context, d time.Duration) bool {
		waits = append(waits, d)
		return true
	}

	s.Run(ctx) // returns once the resolver is live
	require.Equal(t, []time.Duration{seedInitialBackoff, 2 * seedInitialBackoff}, waits, "credential, deploy, switch: two waits in between")
	require.Equal(t, 1, fp.deploys)
	cur, _ := systemroute.Current()
	require.Equal(t, "cloudflare", cur.DNSResolver)
}

func TestSeederRunBacksOffUpToTheCapAndStopsWhenCancelled(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, seedEnv)
	require.NoError(t, e.svc.Load(ctx))
	_, err := e.pool.Exec(ctx, `UPDATE servers SET status='pending' WHERE id=$1`, e.srv.ID)
	require.NoError(t, err)
	s := e.seeder(t, &fakeProxy{e: e}, seedEnv)
	var waits []time.Duration
	s.sleep = func(_ context.Context, d time.Duration) bool {
		waits = append(waits, d)
		return len(waits) < 9 // the ninth wait is interrupted
	}
	s.Run(ctx)
	require.Equal(t, []time.Duration{
		5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second,
		seedMaxBackoff, seedMaxBackoff, seedMaxBackoff,
	}, waits)
}

func TestSeederRunLogsStepFailuresWithoutTheToken(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, seedEnv)
	require.NoError(t, e.svc.Load(ctx))
	_, err := e.pool.Exec(ctx, `UPDATE servers SET acme_email=NULL WHERE id=$1`, e.srv.ID)
	require.NoError(t, err)
	s := e.seeder(t, &fakeProxy{e: e}, seedEnv)
	s.sleep = func(context.Context, time.Duration) bool { return false }
	s.Run(ctx)
	require.Contains(t, e.logs.String(), "dashboard DNS-01 certificate setup")
	require.Contains(t, e.logs.String(), "NEBULACTRL_ACME_EMAIL")
	require.NotContains(t, e.logs.String(), tokenCanary)
}

func TestSeederRunDoesNothingWithoutInstallerSettings(t *testing.T) {
	e := newEnv(t, config.Installer{})
	fp := &fakeProxy{e: e}
	s := e.seeder(t, fp, config.Installer{})
	require.False(t, s.Enabled())
	s.sleep = func(context.Context, time.Duration) bool { t.Fatal("must not wait"); return false }
	s.Run(context.Background())
	require.Zero(t, fp.deploys)
	creds, err := e.dns.ListCredentials(principalFor(e.admin, e.ws), nil)
	require.NoError(t, err)
	require.Empty(t, creds)

	// A resolver without its token is not enough either.
	half := config.Installer{DashboardDNSResolver: "cloudflare"}
	require.False(t, e.seeder(t, fp, half).Enabled())
}

func TestSleepWaitsForTheTimerOrTheContext(t *testing.T) {
	require.True(t, sleep(context.Background(), time.Nanosecond))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, sleep(cancelled, time.Hour))
}
