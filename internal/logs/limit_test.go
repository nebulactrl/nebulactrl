package logs

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

func TestReadLimiterCapsPerKeyAndForgetsIdleKeys(t *testing.T) {
	l := newReadLimiter(maxConcurrentReadsPerPrincipal)
	a, b := uuid.New(), uuid.New()
	var releases []func()
	for range maxConcurrentReadsPerPrincipal {
		release, ok := l.acquire(a)
		require.True(t, ok)
		releases = append(releases, release)
	}
	_, ok := l.acquire(a)
	require.False(t, ok, "the fifth read of one principal is refused")
	release, ok := l.acquire(b)
	require.True(t, ok, "another principal has its own allowance")
	release()

	releases[0]()
	releases[0]() // a second release of the same slot is a no-op
	again, ok := l.acquire(a)
	require.True(t, ok, "a finished read frees a slot")
	_, ok = l.acquire(a)
	require.False(t, ok, "releasing twice did not free two slots")

	again()
	for _, r := range releases[1:] {
		r()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	require.Empty(t, l.inFlight, "a key whose count dropped to zero is removed")
}

// The four log read routes answer 429 with a Retry-After of one second once the
// calling principal has its reads in flight; the limiter runs before the
// service, so no database is involved.
func TestLogReadRoutesAnswer429WhenThePrincipalIsAtTheCap(t *testing.T) {
	l := newReadLimiter(maxConcurrentReadsPerPrincipal)
	r := chi.NewRouter()
	var cur *auth.Principal
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if cur != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), cur))
			}
			next.ServeHTTP(w, req)
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	register(humachi.New(r, cfg), nil, l)

	tokenID := uuid.New()
	p := &auth.Principal{Type: auth.TypeToken, TokenID: &tokenID, UserID: uuid.New(), WorkspaceID: uuid.New(), Scopes: policy.AllScopes}
	var releases []func()
	for range maxConcurrentReadsPerPrincipal {
		release, ok := l.acquire(p.ID())
		require.True(t, ok)
		releases = append(releases, release)
	}
	cur = p
	for _, path := range []string{
		"/services/" + uuid.NewString() + "/logs/history",
		"/environments/" + uuid.NewString() + "/logs/history",
		"/services/" + uuid.NewString() + "/logs/facets",
		"/environments/" + uuid.NewString() + "/logs/facets",
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusTooManyRequests, rec.Code, path)
		require.Equal(t, "1", rec.Header().Get("Retry-After"), path)
		require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"), path)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
		require.Equal(t, "rate_limited", body["code"], path)
		require.Equal(t, float64(http.StatusTooManyRequests), body["status"], path)
	}
	for _, release := range releases {
		release()
	}
}

// A held request keeps its slot until the handler returns, and a request
// without a principal is left to the service.
func TestReadLimiterMiddlewareHoldsTheSlotForTheRequest(t *testing.T) {
	l := newReadLimiter(1)
	r := chi.NewRouter()
	var cur *auth.Principal
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if cur != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), cur))
			}
			next.ServeHTTP(w, req)
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	h := humachi.New(r, cfg)
	entered, finish := make(chan struct{}), make(chan struct{})
	huma.Register(h, huma.Operation{OperationID: "slow", Method: http.MethodGet, Path: "/slow", Middlewares: huma.Middlewares{l.middleware}},
		func(context.Context, *struct{}) (*struct{}, error) {
			entered <- struct{}{}
			<-finish
			return &struct{}{}, nil
		})

	cur = &auth.Principal{Type: auth.TypeUser, UserID: uuid.New()}
	done := make(chan int)
	go func() {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slow", nil))
		done <- rec.Code
	}()
	<-entered
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slow", nil))
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	close(finish)
	require.Equal(t, http.StatusNoContent, <-done)

	go func() { <-entered }()
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slow", nil))
	require.Equal(t, http.StatusNoContent, rec.Code, "the finished request gave its slot back")
	l.mu.Lock()
	require.Empty(t, l.inFlight)
	l.mu.Unlock()
}
