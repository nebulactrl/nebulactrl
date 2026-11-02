package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/alexedwards/scs/v2"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/config"
)

// --- client address -----------------------------------------------------------------

func TestClientIPMiddlewareRecordsTheVisitorForTheLimiters(t *testing.T) {
	var seen string
	h := clientIPMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = ClientIP(r.Context()) }))

	// Reached directly: the TCP peer is the visitor, whatever the headers claim.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.7:4711"
	req.Header.Set("X-Forwarded-For", "203.0.113.99")
	h.ServeHTTP(httptest.NewRecorder(), req)
	require.Equal(t, "198.51.100.7", seen)

	// Through the local proxy: the address it saw is the visitor.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.212.0.3:4711"
	req.Header.Set("X-Forwarded-For", "203.0.113.99")
	h.ServeHTTP(httptest.NewRecorder(), req)
	require.Equal(t, "203.0.113.99", seen)

	require.Empty(t, ClientIP(context.Background()), "outside a request there is no address")
}

// --- cross-origin protection --------------------------------------------------------

func TestCrossOriginProtection(t *testing.T) {
	cfg := &config.Config{PublicURL: "https://ctrl.example.com/"}
	hits := 0
	h := crossOriginProtection(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))
	do := func(method string, header map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://ctrl.example.com/api/v1/x", nil)
		for k, v := range header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Safe methods and same-origin writes pass.
	require.Equal(t, http.StatusNoContent, do(http.MethodGet, map[string]string{"Origin": "https://evil.example"}).Code)
	require.Equal(t, http.StatusNoContent, do(http.MethodPost, map[string]string{"Origin": "https://ctrl.example.com"}).Code)
	require.Equal(t, http.StatusNoContent, do(http.MethodPost, nil).Code, "no Origin and no Sec-Fetch-Site: not a browser")
	require.Equal(t, 3, hits)

	// A cross-site browser write is refused with the API's error shape.
	rec := do(http.MethodPost, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"})
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "cross_origin_forbidden")
	require.Equal(t, 3, hits, "the handler never ran")
	require.Equal(t, http.StatusForbidden, do(http.MethodDelete, map[string]string{"Origin": "https://evil.example"}).Code)

	// A bearer token cannot be attached by a cross-site page, so it is exempt.
	rec = do(http.MethodPost, map[string]string{"Origin": "https://evil.example", "Authorization": "Bearer nct_abc"})
	require.Equal(t, http.StatusNoContent, rec.Code)
	// A cookie session pretending to be one is not.
	rec = do(http.MethodPost, map[string]string{"Origin": "https://evil.example", "Authorization": "Basic abc"})
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestCrossOriginProtectionWithoutAPublicURLTrustsNoExtraOrigin(t *testing.T) {
	h := crossOriginProtection(&config.Config{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequest(http.MethodPost, "http://localhost/x", nil)
	req.Header.Set("Origin", "https://ctrl.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
}

// --- readiness and logging ----------------------------------------------------------

type pinger func(context.Context) error

func (p pinger) Ping(ctx context.Context) error { return p(ctx) }

func TestReadyzReportsTheDatabase(t *testing.T) {
	get := func(h http.HandlerFunc) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec
	}

	rec := get(readyz(pinger(func(context.Context) error { return nil })))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.JSONEq(t, `{"status":"ready"}`, rec.Body.String())

	rec = get(readyz(pinger(func(context.Context) error { return errors.New("connection refused") })))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.JSONEq(t, `{"status":"unavailable","reason":"database"}`, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "refused", "the cause stays in the log")

	// The ping is bounded, so a hung database cannot hang the probe.
	var deadline bool
	get(readyz(pinger(func(ctx context.Context) error { _, deadline = ctx.Deadline(); return nil })))
	require.True(t, deadline)

	require.Equal(t, http.StatusOK, get(readyz(nil)).Code, "no database configured: ready")
}

func TestRequestLoggerLogsEachRequestWithItsStatus(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	h := requestLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/teapot":
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("short and stout"))
		default:
			_, _ = w.Write([]byte("ok")) // no explicit status: 200
		}
	}))
	do := func(path string) {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	do("/teapot")
	require.Contains(t, buf.String(), "http request")
	require.Contains(t, buf.String(), "path=/teapot")
	require.Contains(t, buf.String(), "status=418")
	require.Contains(t, buf.String(), "bytes=15")
	buf.Reset()

	do("/things")
	require.Contains(t, buf.String(), "status=200", "an implicit 200 is logged as 200")
	buf.Reset()

	// Probes are logged at debug level only.
	do("/healthz")
	do("/readyz")
	require.Empty(t, buf.String())
}

// --- sessions -----------------------------------------------------------------------

func TestSessionHelpersReadAndWriteTheUserAndTeam(t *testing.T) {
	sm := scs.New()
	user, team := uuid.New(), uuid.New()
	var seenUser, seenTeam *uuid.UUID
	var startErr error

	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		startErr = StartUserSession(r.Context(), sm, user, team)
	})
	mux.HandleFunc("/no-team", func(w http.ResponseWriter, r *http.Request) {
		startErr = StartUserSession(r.Context(), sm, user, uuid.Nil)
	})
	mux.HandleFunc("/switch", func(w http.ResponseWriter, r *http.Request) { SetSessionTeam(r.Context(), sm, team) })
	mux.HandleFunc("/garbage", func(w http.ResponseWriter, r *http.Request) {
		sm.Put(r.Context(), sessionUserKey, "not-a-uuid")
	})
	mux.HandleFunc("/who", func(w http.ResponseWriter, r *http.Request) {
		seenUser, seenTeam = sessionUserID(r.Context(), sm), sessionTeamID(r.Context(), sm)
	})
	h := sm.LoadAndSave(mux)

	call := func(path string, cookies []*http.Cookie) []*http.Cookie {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Result().Cookies(); len(got) > 0 {
			return got
		}
		return cookies
	}

	// Nothing signed in yet.
	call("/who", nil)
	require.Nil(t, seenUser)
	require.Nil(t, seenTeam)

	// Login records both and rotates the token.
	cookies := call("/login", nil)
	require.NoError(t, startErr)
	call("/who", cookies)
	require.NotNil(t, seenUser)
	require.Equal(t, user, *seenUser)
	require.Equal(t, team, *seenTeam)
	rotated := call("/login", cookies)
	require.NotEqual(t, cookies[0].Value, rotated[0].Value, "signing in again renews the session token")

	// Signing in without a team clears the team of an earlier session.
	cookies = call("/no-team", cookies)
	require.NoError(t, startErr)
	call("/who", cookies)
	require.Equal(t, user, *seenUser)
	require.Nil(t, seenTeam)

	// Switching teams only changes the team.
	cookies = call("/switch", cookies)
	call("/who", cookies)
	require.Equal(t, user, *seenUser)
	require.Equal(t, team, *seenTeam)

	// A value that is not an id reads as signed out.
	cookies = call("/garbage", cookies)
	call("/who", cookies)
	require.Nil(t, seenUser)
}

// --- OpenAPI plumbing ---------------------------------------------------------------

type Plan struct{}

func TestSchemaNamerPrefixesTheNebulaTomlPackages(t *testing.T) {
	// A type of another package keeps huma's name.
	require.Equal(t, huma.DefaultSchemaNamer(reflect.TypeFor[strings.Builder](), ""), schemaNamer(reflect.TypeFor[strings.Builder](), ""))
	require.Equal(t, huma.DefaultSchemaNamer(reflect.TypeFor[*Plan](), ""), schemaNamer(reflect.TypeFor[*Plan](), ""), "a pointer is named after its element")
	// Unnamed types have no package to judge.
	require.Equal(t, huma.DefaultSchemaNamer(reflect.TypeFor[[]string](), ""), schemaNamer(reflect.TypeFor[[]string](), ""))

	reg := NewSchemaRegistry()
	s := reg.Schema(reflect.TypeFor[Plan](), true, "")
	require.NotNil(t, s)
	require.Equal(t, schemaPrefix+"Plan", s.Ref)
}

func TestHumaConfigDocumentsTheAuthSchemes(t *testing.T) {
	c := humaConfig()
	require.Equal(t, "NebulaCtrl API", c.Info.Title)
	require.Equal(t, Version, c.Info.Version)
	require.Nil(t, c.CreateHooks, "no $schema fields or Link headers")
	require.Equal(t, BasePath, c.Servers[0].URL)
	require.Equal(t, Tags, c.Tags)
	require.Equal(t, "bearer", c.Components.SecuritySchemes[SchemeBearer].Scheme)
	require.Equal(t, SessionCookieName, c.Components.SecuritySchemes[SchemeCookie].Name)
	require.Equal(t, []map[string][]string{{SchemeBearer: {}}, {SchemeCookie: {}}}, c.Security)
	require.Contains(t, c.Info.Description, "problem+json")
}

func TestOperationHelpers(t *testing.T) {
	require.False(t, IsIdempotent(nil))
	require.False(t, IsIdempotent(&huma.Operation{}))
	op := WithIdempotency(huma.Operation{})
	require.True(t, IsIdempotent(&op))
	require.Len(t, op.Parameters, 1)
	require.Equal(t, IdempotencyHeader, op.Parameters[0].Name)
	require.Equal(t, "header", op.Parameters[0].In)
	notBool := huma.Operation{Metadata: map[string]any{IdempotentKey: "yes"}}
	require.False(t, IsIdempotent(&notBool), "only a boolean true opts in")
}

// --- server-sent events -------------------------------------------------------------

func TestSSEWritesCommentsAndDataFrames(t *testing.T) {
	r := chi.NewRouter()
	h := humachi.New(r, huma.DefaultConfig("t", "1"))
	type streamOut struct{}
	var dataErr, commentErr, badErr error
	huma.Register(h, huma.Operation{OperationID: "stream", Method: http.MethodGet, Path: "/stream"}, func(context.Context, *struct{}) (*huma.StreamResponse, error) {
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			sse, err := NewSSE(hctx)
			if err != nil {
				t.Errorf("NewSSE: %v", err)
				return
			}
			dataErr = sse.Data(map[string]any{"type": "service.updated", "n": 1})
			commentErr = sse.Comment("heartbeat")
			badErr = sse.Data(make(chan int))
		}}, nil
	})
	_ = streamOut{}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stream", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	require.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	require.Equal(t, "no", rec.Header().Get("X-Accel-Buffering"))
	require.NoError(t, dataErr)
	require.NoError(t, commentErr)
	require.Error(t, badErr, "a value that cannot be encoded is an error, and nothing partial is sent")
	require.Equal(t, ": connected\n\ndata: {\"n\":1,\"type\":\"service.updated\"}\n\n: heartbeat\n\n", rec.Body.String())
}
