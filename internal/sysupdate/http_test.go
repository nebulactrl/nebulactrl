package sysupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/releasesign"
	"github.com/nebulactrl/nebulactrl/internal/version"
)

// httpCall drives one request through the registered endpoints as the principal
// carried by ctx (a ctx without one is an anonymous caller).
func (f *fixture) httpCall(ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
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
	Register(humachi.New(r, cfg), f.svc)

	rd := bytes.NewReader(nil)
	if body != nil {
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

func httpJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m
}

func TestHTTPGetUpdateIsForEveryAuthenticatedUser(t *testing.T) {
	f := newFixture(t)
	f.check()

	rec := f.httpCall(f.member, http.MethodGet, "/system/update", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m := httpJSON(t, rec)
	require.Equal(t, "0.1.0", m["current"])
	require.Equal(t, "0.1.2", m["latest"])
	require.Equal(t, true, m["available"])
	require.Equal(t, false, m["canUpdate"])
	require.Equal(t, "Only the instance admin can update.", m["cannotUpdateReason"])
	require.Empty(t, m["manualCommand"], "the mirror's address is for the admin only")

	rec = f.httpCall(f.admin, http.MethodGet, "/system/update", nil)
	m = httpJSON(t, rec)
	require.Equal(t, true, m["canUpdate"])
	require.Contains(t, m["manualCommand"], "--update-to 0.1.2")

	require.Equal(t, http.StatusUnauthorized, f.httpCall(context.Background(), http.MethodGet, "/system/update", nil).Code)
}

func TestHTTPStartUpdate(t *testing.T) {
	f := newFixture(t)

	// Nothing is offered before a check.
	rec := f.httpCall(f.admin, http.MethodPost, "/system/update", map[string]any{"version": "0.1.2"})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, CodeUpToDate, httpJSON(t, rec)["code"])

	f.check()
	rec = f.httpCall(f.admin, http.MethodPost, "/system/update", map[string]any{"version": "0.1.1"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, CodeNotOffered, httpJSON(t, rec)["code"])
	rec = f.httpCall(f.admin, http.MethodPost, "/system/update", map[string]any{"version": ""})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "the version is required")
	rec = f.httpCall(f.admin, http.MethodPost, "/system/update", map[string]any{"version": strings.Repeat("9", 65)})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "the version is bounded")
	require.Zero(t, f.disp.count())

	// Members and anonymous callers cannot start it.
	rec = f.httpCall(f.member, http.MethodPost, "/system/update", map[string]any{"version": "0.1.2"})
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	rec = f.httpCall(context.Background(), http.MethodPost, "/system/update", map[string]any{"version": "0.1.2"})
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	require.Zero(t, f.disp.count())

	rec = f.httpCall(f.admin, http.MethodPost, "/system/update", map[string]any{"version": "0.1.2"})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	updating := httpJSON(t, rec)["updating"].(map[string]any)
	require.Equal(t, "running", updating["state"])
	require.Equal(t, "0.1.2", updating["targetVersion"])
	require.Equal(t, 1, f.disp.count())

	rec = f.httpCall(f.admin, http.MethodPost, "/system/update", map[string]any{"version": "0.1.2"})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, CodeInProgress, httpJSON(t, rec)["code"])
	require.Equal(t, 1, f.disp.count())
}

func TestHTTPCheckNow(t *testing.T) {
	f := newFixture(t)
	now := f.clock(time.Now())

	rec := f.httpCall(f.admin, http.MethodPost, "/system/update/check", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m := httpJSON(t, rec)
	require.Equal(t, "0.1.2", m["latest"])
	require.Equal(t, true, m["available"])
	require.NotNil(t, m["checkedAt"])

	// A second check inside 30 seconds is turned away with the wait.
	*now = now.Add(10 * time.Second)
	rec = f.httpCall(f.admin, http.MethodPost, "/system/update/check", nil)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	require.Equal(t, "20", rec.Header().Get("Retry-After"))
	require.Equal(t, 1, f.feed.requests())

	// A feed that fails is a 502 and the last release stays.
	f.feed.mu.Lock()
	f.feed.status = http.StatusServiceUnavailable
	f.feed.mu.Unlock()
	*now = now.Add(manualCheckEvery)
	rec = f.httpCall(f.admin, http.MethodPost, "/system/update/check", nil)
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	require.Equal(t, CodeCheckFailed, httpJSON(t, rec)["code"])
	require.Equal(t, "0.1.2", httpJSON(t, f.httpCall(f.member, http.MethodGet, "/system/update", nil))["latest"])

	// Only the instance admin may ask, and checks may be off.
	require.Equal(t, http.StatusForbidden, f.httpCall(f.member, http.MethodPost, "/system/update/check", nil).Code)
	require.Equal(t, http.StatusUnauthorized, f.httpCall(context.Background(), http.MethodPost, "/system/update/check", nil).Code)
	off := newFixtureWith(t, false)
	rec = off.httpCall(off.admin, http.MethodPost, "/system/update/check", nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, CodeCheckDisabled, httpJSON(t, rec)["code"])
	require.Zero(t, off.feed.requests())
}

func TestNewServiceFillsInTheOfficialDefaults(t *testing.T) {
	s := NewService(Deps{})
	require.Nil(t, s.q, "no pool, no queries (OpenAPI generation)")
	require.Equal(t, config.DefaultUpdateFeedURL, s.cfg.FeedURL)
	require.Equal(t, releasesign.PublicKey, s.pubKey)
	require.Equal(t, version.Current(), s.ver())
	require.False(t, s.CheckEnabled(), "checks are opt-in through the config")
}

func TestInstallerURLDropsFeedCredentialsAndFallsBackToTheOfficialInstaller(t *testing.T) {
	s := NewService(Deps{Config: config.Update{FeedURL: "https://user:token@mirror.example/releases/latest.json?k=v#x"}})
	require.Equal(t, "https://mirror.example/install.sh", s.installerURL())
	require.NotContains(t, s.manualCommand("", false), "token")
	require.Equal(t, "curl -fsSL https://mirror.example/install.sh | sudo bash -s -- --update-to 1.2.3", s.manualCommand("1.2.3", true))
	require.NotContains(t, s.manualCommand("1.2.3", false), "--update-to", "no release is offered: the plain installer")

	// A feed URL that does not end in the feed path gives no derivable installer.
	odd := NewService(Deps{Config: config.Update{FeedURL: "https://mirror.example/feed.json"}})
	require.Equal(t, defaultInstaller, odd.installerURL())
}

func TestCheckJobReadsTheFeed(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, (&checkWorker{svc: f.svc}).Work(context.Background(), nil))
	require.Equal(t, 1, f.feed.requests())
	require.Equal(t, "0.1.2", f.get(f.admin).Latest)

	// With checks off the job does nothing.
	off := newFixtureWith(t, false)
	require.NoError(t, (&checkWorker{svc: off.svc}).Work(context.Background(), nil))
	require.Zero(t, off.feed.requests())
}

func TestRegisterJobsChecksOnStartOnlyWhenEnabled(t *testing.T) {
	f := newFixture(t)
	jm := jobs.New(f.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	RegisterJobs(jm, f.svc)
	require.NoError(t, jm.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = jm.Stop(ctx)
	})
	require.Eventually(t, func() bool { return f.feed.requests() == 1 && f.get(f.admin).Latest == "0.1.2" }, 20*time.Second, 50*time.Millisecond)

	// Disabled: the worker exists (a manual insert works) but nothing runs by itself.
	off := newFixtureWith(t, false)
	offJM := jobs.New(off.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	RegisterJobs(offJM, off.svc)
	require.NoError(t, offJM.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = offJM.Stop(ctx)
	})
	require.NoError(t, offJM.Insert(context.Background(), CheckArgs{}, nil))
	require.Eventually(t, func() bool {
		var n int
		err := off.pool.QueryRow(context.Background(), "SELECT count(*) FROM river_job WHERE kind = 'update_check' AND state = 'completed'").Scan(&n)
		return err == nil && n == 1
	}, 20*time.Second, 50*time.Millisecond)
	var total int
	require.NoError(t, off.pool.QueryRow(context.Background(), "SELECT count(*) FROM river_job WHERE kind = 'update_check'").Scan(&total))
	require.Equal(t, 1, total, "no periodic run was scheduled")
	require.Zero(t, off.feed.requests())
}
