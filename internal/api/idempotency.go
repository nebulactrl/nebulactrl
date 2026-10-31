package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

const maxIdempotencyKeyLen = 255

// storedResponse is the jsonb persisted in idempotency_keys.body. The body is
// kept as a string so a replay is byte-identical to the original.
type storedResponse struct {
	ContentType string              `json:"contentType"`
	Headers     map[string][]string `json:"headers,omitempty"`
	Body        string              `json:"body"`
}

type baseCtx = huma.Context

// recordingContext wraps a huma.Context to capture the response.
type recordingContext struct {
	baseCtx
	status  int
	headers http.Header
	body    bytes.Buffer
}

func (c *recordingContext) SetStatus(code int) {
	c.status = code
	c.baseCtx.SetStatus(code)
}

func (c *recordingContext) SetHeader(name, value string) {
	c.headers.Set(name, value)
	c.baseCtx.SetHeader(name, value)
}

func (c *recordingContext) AppendHeader(name, value string) {
	c.headers.Add(name, value)
	c.baseCtx.AppendHeader(name, value)
}

func (c *recordingContext) BodyWriter() io.Writer {
	return io.MultiWriter(c.baseCtx.BodyWriter(), &c.body)
}

// idempotencyMiddleware replays stored responses for operations marked with
// WithIdempotency. Only 2xx responses are stored. Concurrent requests with the
// same key serialise on a Postgres advisory lock, so the second one replays the
// first one's response.
func idempotencyMiddleware(pool *pgxpool.Pool) func(huma.Context, func(huma.Context)) {
	return func(hctx huma.Context, next func(huma.Context)) {
		op := hctx.Operation()
		key := hctx.Header(IdempotencyHeader)
		if !IsIdempotent(op) || key == "" {
			next(hctx)
			return
		}
		p, ok := auth.PrincipalFrom(hctx.Context())
		if !ok {
			next(hctx) // the handler answers 401
			return
		}
		if len(key) > maxIdempotencyKeyLen {
			writeProblem(hctx, apierr.BadRequest("invalid_idempotency_key", "Idempotency-Key must be at most 255 characters"))
			return
		}
		ctx := hctx.Context()
		principal := p.ID().String()

		conn, err := pool.Acquire(ctx)
		if err != nil {
			writeProblem(hctx, apierr.Internal())
			return
		}
		defer conn.Release()
		lockKey := principal + ":" + key
		if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(hashtextextended($1, 0))", lockKey); err != nil {
			writeProblem(hctx, apierr.Internal())
			return
		}
		defer func() {
			// Use a fresh context: the request may already be cancelled, and the
			// lock must not leak into the pool.
			if _, err := conn.Exec(contextWithoutCancel(ctx), "SELECT pg_advisory_unlock(hashtextextended($1, 0))", lockKey); err != nil {
				conn.Conn().Close(contextWithoutCancel(ctx)) //nolint:errcheck // dropping the connection releases the lock
			}
		}()

		q := db.New(conn)
		rec, err := q.GetIdempotencyKey(ctx, db.GetIdempotencyKeyParams{PrincipalID: principal, Key: key})
		switch {
		case err == nil:
			if rec.Method != hctx.Method() || rec.Path != op.Path {
				writeProblem(hctx, apierr.Unprocessable("idempotency_key_reused", "Idempotency-Key was already used for a different request"))
				return
			}
			replay(hctx, rec)
			return
		case !errors.Is(err, pgx.ErrNoRows):
			writeProblem(hctx, apierr.Internal())
			return
		}

		rc := &recordingContext{baseCtx: hctx, headers: http.Header{}}
		next(rc)
		if rc.status < 200 || rc.status > 299 {
			return
		}
		stored := storedResponse{ContentType: rc.headers.Get("Content-Type"), Body: rc.body.String()}
		rc.headers.Del("Content-Length")
		rc.headers.Del("Set-Cookie")
		if len(rc.headers) > 0 {
			stored.Headers = rc.headers
		}
		raw, err := json.Marshal(stored)
		if err != nil {
			return
		}
		_ = q.InsertIdempotencyKey(contextWithoutCancel(ctx), db.InsertIdempotencyKeyParams{
			PrincipalID: principal,
			Key:         key,
			Method:      hctx.Method(),
			Path:        op.Path,
			Status:      int32(rc.status),
			Body:        raw,
		})
	}
}

func replay(hctx huma.Context, rec db.IdempotencyKey) {
	var s storedResponse
	if len(rec.Body) > 0 {
		_ = json.Unmarshal(rec.Body, &s)
	}
	for name, vals := range s.Headers {
		for _, v := range vals {
			hctx.AppendHeader(name, v)
		}
	}
	hctx.SetHeader("Idempotent-Replayed", "true")
	hctx.SetStatus(int(rec.Status))
	if s.Body != "" {
		_, _ = io.WriteString(hctx.BodyWriter(), s.Body)
	}
}

func writeProblem(hctx huma.Context, e *apierr.Error) {
	hctx.SetHeader("Content-Type", "application/problem+json")
	hctx.SetStatus(e.Status)
	_ = json.NewEncoder(hctx.BodyWriter()).Encode(e)
}
