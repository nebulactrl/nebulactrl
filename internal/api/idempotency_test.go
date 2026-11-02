package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

type createOut struct {
	Location string `header:"X-Resource"`
	Body     struct {
		N    int32  `json:"n"`
		Name string `json:"name"`
	}
}

type createIn struct {
	Body struct {
		Name string `json:"name"`
	}
}

// idemRig is a router with the idempotency middleware over a real Postgres
// schema and three operations: two idempotent POSTs and one that is not.
type idemRig struct {
	router  http.Handler
	calls   atomic.Int32 // executions of the handlers
	failing atomic.Bool  // make the next executions answer 409
}

func newIdemRig(t *testing.T) (*idemRig, func(path, key string, principal uuid.UUID, body string) *httptest.ResponseRecorder) {
	t.Helper()
	pool := testutil.NewPool(t)
	rig := &idemRig{}

	// X-User names the caller; without it the request is anonymous.
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if id, err := uuid.Parse(req.Header.Get("X-User")); err == nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), &auth.Principal{Type: auth.TypeUser, UserID: id}))
			}
			next.ServeHTTP(w, req)
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	h := humachi.New(r, cfg)
	h.UseMiddleware(idempotencyMiddleware(pool))

	handler := func(ctx context.Context, in *createIn) (*createOut, error) {
		n := rig.calls.Add(1)
		if rig.failing.Load() {
			return nil, apierr.Conflict("busy", "try again")
		}
		out := &createOut{Location: "/things/" + in.Body.Name}
		out.Body.N, out.Body.Name = n, in.Body.Name
		return out, nil
	}
	huma.Register(h, WithIdempotency(huma.Operation{OperationID: "createA", Method: http.MethodPost, Path: "/a", DefaultStatus: http.StatusCreated}), handler)
	huma.Register(h, WithIdempotency(huma.Operation{OperationID: "createB", Method: http.MethodPost, Path: "/b", DefaultStatus: http.StatusCreated}), handler)
	huma.Register(h, huma.Operation{OperationID: "plain", Method: http.MethodPost, Path: "/plain", DefaultStatus: http.StatusCreated}, handler)
	rig.router = r

	return rig, func(path, key string, principal uuid.UUID, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set(IdempotencyHeader, key)
		}
		if principal != uuid.Nil {
			req.Header.Set("X-User", principal.String())
		}
		rec := httptest.NewRecorder()
		rig.router.ServeHTTP(rec, req)
		return rec
	}
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m
}

func TestIdempotentRetryReplaysTheFirstResponse(t *testing.T) {
	rig, post := newIdemRig(t)
	user := uuid.New()

	first := post("/a", "key-1", user, `{"name":"alpha"}`)
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	require.Empty(t, first.Header().Get("Idempotent-Replayed"))
	require.Equal(t, "/things/alpha", first.Header().Get("X-Resource"))
	require.EqualValues(t, 1, decodeBody(t, first)["n"])

	// The retry gets the same status, headers and bytes, and the handler does not run again.
	again := post("/a", "key-1", user, `{"name":"alpha"}`)
	require.Equal(t, http.StatusCreated, again.Code)
	require.Equal(t, "true", again.Header().Get("Idempotent-Replayed"))
	require.Equal(t, "/things/alpha", again.Header().Get("X-Resource"), "response headers are replayed")
	require.Contains(t, again.Header().Get("Content-Type"), "application/json")
	require.Equal(t, first.Body.String(), again.Body.String(), "a replay is byte-identical")
	require.EqualValues(t, 1, rig.calls.Load())

	// A body that differs is still the same request as far as the key goes.
	third := post("/a", "key-1", user, `{"name":"other"}`)
	require.Equal(t, "true", third.Header().Get("Idempotent-Replayed"))
	require.Equal(t, first.Body.String(), third.Body.String())
	require.EqualValues(t, 1, rig.calls.Load())
}

func TestIdempotencyKeysAreScopedToThePrincipal(t *testing.T) {
	rig, post := newIdemRig(t)
	alice, bob := uuid.New(), uuid.New()

	a := post("/a", "shared-key", alice, `{"name":"a"}`)
	b := post("/a", "shared-key", bob, `{"name":"b"}`)
	require.Equal(t, http.StatusCreated, b.Code, b.Body.String())
	require.Empty(t, b.Header().Get("Idempotent-Replayed"), "bob never sees alice's stored response")
	require.EqualValues(t, 2, rig.calls.Load())
	require.NotEqual(t, a.Body.String(), b.Body.String())
	require.Equal(t, "true", post("/a", "shared-key", bob, `{"name":"b"}`).Header().Get("Idempotent-Replayed"))
	require.EqualValues(t, 2, rig.calls.Load())
}

func TestDifferentKeysAndMissingKeysRunTheHandlerEveryTime(t *testing.T) {
	rig, post := newIdemRig(t)
	user := uuid.New()

	for _, key := range []string{"k1", "k2", "", ""} {
		rec := post("/a", key, user, `{"name":"x"}`)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		require.Empty(t, rec.Header().Get("Idempotent-Replayed"))
	}
	require.EqualValues(t, 4, rig.calls.Load())

	// An operation that does not opt in ignores the header.
	post("/plain", "k1", user, `{"name":"x"}`)
	rec := post("/plain", "k1", user, `{"name":"x"}`)
	require.Empty(t, rec.Header().Get("Idempotent-Replayed"))
	require.EqualValues(t, 6, rig.calls.Load())
}

func TestAKeyReusedForAnotherRequestIsRefused(t *testing.T) {
	rig, post := newIdemRig(t)
	user := uuid.New()
	require.Equal(t, http.StatusCreated, post("/a", "key-1", user, `{"name":"x"}`).Code)

	rec := post("/b", "key-1", user, `{"name":"x"}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "idempotency_key_reused", decodeBody(t, rec)["code"])
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.EqualValues(t, 1, rig.calls.Load(), "the second operation did not run")
}

func TestOnlySuccessfulResponsesAreStored(t *testing.T) {
	rig, post := newIdemRig(t)
	user := uuid.New()

	rig.failing.Store(true)
	fail := post("/a", "key-1", user, `{"name":"x"}`)
	require.Equal(t, http.StatusConflict, fail.Code, fail.Body.String())
	require.Empty(t, fail.Header().Get("Idempotent-Replayed"))

	// The client retries with the same key once the cause is gone: the handler runs again.
	rig.failing.Store(false)
	ok := post("/a", "key-1", user, `{"name":"x"}`)
	require.Equal(t, http.StatusCreated, ok.Code, ok.Body.String())
	require.Empty(t, ok.Header().Get("Idempotent-Replayed"))
	require.EqualValues(t, 2, rig.calls.Load())

	// A validation error is not stored either.
	bad := post("/a", "key-2", user, `{"name":`)
	require.GreaterOrEqual(t, bad.Code, 400)
	good := post("/a", "key-2", user, `{"name":"y"}`)
	require.Equal(t, http.StatusCreated, good.Code, good.Body.String())
	require.Empty(t, good.Header().Get("Idempotent-Replayed"))

	require.Equal(t, "true", post("/a", "key-1", user, `{"name":"x"}`).Header().Get("Idempotent-Replayed"), "the success was stored")
}

func TestOverlongKeysAreRejectedBeforeAnythingRuns(t *testing.T) {
	rig, post := newIdemRig(t)
	user := uuid.New()

	rec := post("/a", strings.Repeat("k", 256), user, `{"name":"x"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, "invalid_idempotency_key", decodeBody(t, rec)["code"])
	require.Zero(t, rig.calls.Load())

	require.Equal(t, http.StatusCreated, post("/a", strings.Repeat("k", 255), user, `{"name":"x"}`).Code, "255 characters is the limit")
}

func TestAnonymousCallsPassThroughToTheHandler(t *testing.T) {
	rig, post := newIdemRig(t)
	// With no principal the middleware stores nothing; the handler decides (here it would answer 401).
	for range 2 {
		rec := post("/a", "key-1", uuid.Nil, `{"name":"x"}`)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		require.Empty(t, rec.Header().Get("Idempotent-Replayed"))
	}
	require.EqualValues(t, 2, rig.calls.Load())
}

func TestConcurrentRetriesRunTheHandlerOnce(t *testing.T) {
	rig, post := newIdemRig(t)
	user := uuid.New()
	const callers = 8

	recs := make([]*httptest.ResponseRecorder, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs[i] = post("/a", "race", user, `{"name":"x"}`)
		}()
	}
	wg.Wait()

	require.EqualValues(t, 1, rig.calls.Load(), "the advisory lock serialises same-key requests")
	replayed := 0
	for _, rec := range recs {
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		require.EqualValues(t, 1, decodeBody(t, rec)["n"])
		if rec.Header().Get("Idempotent-Replayed") == "true" {
			replayed++
		}
	}
	require.Equal(t, callers-1, replayed)
}

func TestRecordingContextCapturesWhatItForwards(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(nil))
	cfg := huma.DefaultConfig("t", "1")
	r := chi.NewRouter()
	h := humachi.New(r, cfg)
	var captured *recordingContext
	h.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		captured = &recordingContext{baseCtx: ctx, headers: http.Header{}}
		next(captured)
	})
	type rowsOut struct {
		Rows []string `header:"X-Row"`
		Body string
	}
	huma.Register(h, huma.Operation{OperationID: "x", Method: http.MethodPost, Path: "/x"}, func(context.Context, *struct{}) (*rowsOut, error) {
		return &rowsOut{Rows: []string{"a", "b"}, Body: "hello"}, nil
	})
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, captured)
	require.Equal(t, http.StatusOK, captured.status)
	require.Equal(t, []string{"a", "b"}, rec.Header().Values("X-Row"), "what the recorder saw is still sent")
	require.Contains(t, captured.body.String(), "hello")
	require.Contains(t, captured.headers.Get("Content-Type"), "json")
}
