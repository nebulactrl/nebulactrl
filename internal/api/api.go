// Package api builds the HTTP surface: chi router, huma operations under
// /api/v1, authentication middleware, CSRF protection, idempotency, and the
// core endpoints (setup, auth, me, events). Domain packages expose
// `Register(huma.API, *Service)` and are attached through Deps.Register.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/clientip"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/events"
)

// BasePath is where the huma API is mounted.
const BasePath = "/api/v1"

// SessionCookieName is the browser session cookie.
const SessionCookieName = "nebulactrl_session"

// Version is reported in the OpenAPI info block.
const Version = "0.1.0"

// Deps are the collaborators of the HTTP layer. For OpenAPI generation every
// service-like field may be nil: operations are registered but never invoked.
type Deps struct {
	Logger   *slog.Logger
	Pool     *pgxpool.Pool // nil disables idempotency and /readyz DB checks
	Auth     *auth.Service
	Sessions *scs.SessionManager
	Bus      *events.Bus

	// Register attaches domain operations to the huma API (called after the
	// core operations, in order).
	Register []func(huma.API)
	// MountAPI attaches raw chi routes under /api/v1 behind the same
	// middleware as huma operations (CSRF, session, auth): SSE endpoints etc.
	MountAPI []func(chi.Router)
	// Mount attaches raw routes to the root router, outside /api/v1 and
	// without session/CSRF handling: /agent/v1/*, /mcp, /install-agent.sh,
	// /download/cli/*, /cli.sh.
	Mount []func(chi.Router)
	// Fallback serves every unmatched non-API path (the embedded SPA).
	Fallback http.Handler
}

// New builds the root handler and the huma API.
func New(cfg *config.Config, deps Deps) (http.Handler, huma.API) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	root := chi.NewRouter()
	root.Use(middleware.RequestID, requestLogger(logger), middleware.Recoverer)
	root.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONStatus(w, http.StatusOK, `{"status":"ok"}`)
	})
	var ready interface{ Ping(context.Context) error }
	if deps.Pool != nil {
		ready = deps.Pool
	}
	root.Get("/readyz", readyz(ready))
	for _, m := range deps.Mount {
		m(root)
	}

	var humaAPI huma.API
	root.Route(BasePath, func(r chi.Router) {
		r.Use(clientIPMiddleware, crossOriginProtection(cfg))
		if deps.Sessions != nil {
			r.Use(deps.Sessions.LoadAndSave)
		}
		r.Use(authMiddleware(deps.Auth, deps.Sessions))
		r.Use(twoFactorGate(cfg, deps.Auth, deps.Sessions))
		r.NotFound(func(w http.ResponseWriter, _ *http.Request) { apierr.Write(w, apierr.NotFound("route")) })
		r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
			apierr.Write(w, apierr.New(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed"))
		})

		humaAPI = humachi.New(r, humaConfig())
		if deps.Pool != nil {
			humaAPI.UseMiddleware(idempotencyMiddleware(deps.Pool))
		}
		for _, m := range deps.MountAPI {
			m(r)
		}

		c := &core{
			auth: deps.Auth, sessions: deps.Sessions, bus: deps.Bus, logins: newIPLimiter(10, time.Minute), scope: newEventScope(deps.Auth),
			accountLogins: newIPLimiter(10, 10*time.Minute),
			setupToken:    cfg.Installer.SetupToken,
		}
		c.register(humaAPI)
		for _, reg := range deps.Register {
			reg(humaAPI)
		}
	})
	if deps.Fallback != nil {
		root.NotFound(deps.Fallback.ServeHTTP)
	}
	return root, humaAPI
}

func humaConfig() huma.Config {
	c := huma.DefaultConfig("NebulaCtrl API", Version)
	// No $schema fields or Link headers: error and success bodies stay minimal.
	c.CreateHooks = nil
	c.Components.Schemas = NewSchemaRegistry()
	c.Info.Description = "Control-plane API for NebulaCtrl, a self-hosted Docker-native PaaS. " +
		"Authenticate with the `nebulactrl_session` cookie (browser) or `Authorization: Bearer nct_…` (API tokens). " +
		"Errors are RFC 9457 problem+json with an additional `code` field."
	c.Servers = []*huma.Server{{URL: BasePath}}
	c.Tags = Tags
	c.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		SchemeBearer: {Type: "http", Scheme: "bearer", Description: "API token (`nct_…`)"},
		SchemeCookie: {Type: "apiKey", In: "cookie", Name: SessionCookieName, Description: "Browser session"},
	}
	c.Security = []map[string][]string{{SchemeBearer: {}}, {SchemeCookie: {}}}
	return c
}

// clientIPMiddleware records the visitor's address for the rate limiters. The
// TCP peer of a domain install is the host's Traefik, so the address comes from
// the shared trusted-client-IP rules (internal/clientip), not RemoteAddr.
func clientIPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey, clientip.String(r))))
	})
}

// crossOriginProtection rejects cross-origin unsafe browser requests. Requests
// carrying a bearer token are exempt: a cross-site page cannot attach one
// without a CORS preflight, which this server never approves.
func crossOriginProtection(cfg *config.Config) func(http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	if o := cfg.PublicOrigin(); o != "" {
		_ = cop.AddTrustedOrigin(o)
	}
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		apierr.Write(w, apierr.New(http.StatusForbidden, "cross_origin_forbidden", "cross-origin request rejected"))
	}))
	return func(next http.Handler) http.Handler {
		protected := cop.Handler(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				next.ServeHTTP(w, r)
				return
			}
			protected.ServeHTTP(w, r)
		})
	}
}

func authMiddleware(svc *auth.Service, sessions *scs.SessionManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if svc == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if h := r.Header.Get("Authorization"); h != "" {
				raw, ok := strings.CutPrefix(h, "Bearer ")
				if !ok {
					apierr.Write(w, apierr.Unauthorized())
					return
				}
				p, err := svc.AuthenticateBearer(ctx, strings.TrimSpace(raw))
				if err != nil {
					if ae, ok := apierr.As(err); ok {
						apierr.Write(w, ae)
					} else {
						slog.ErrorContext(ctx, "authenticate bearer", "error", err)
						apierr.Write(w, apierr.Internal())
					}
					return
				}
				if _, inProc := inProcessFrom(ctx); p.TokenKind == auth.TokenKindMCP && !inProc {
					// An MCP token is MCP-only: the production guard and the
					// deny list live behind /mcp and must not be sidestepped.
					apierr.Write(w, auth.ErrMCPTokenOnREST)
					return
				}
				next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(ctx, markInProcess(ctx, p))))
				return
			}
			if sessions != nil {
				if id := sessionUserID(ctx, sessions); id != nil {
					u, err := svc.UserByID(ctx, *id)
					if err == nil {
						stored := sessionTeamID(ctx, sessions)
						p, perr := svc.PrincipalForSession(ctx, u, stored)
						if perr == nil {
							// Keep the session on the team actually in force (the stored
							// team may have been left, deleted or never set).
							if p.WorkspaceID != uuid.Nil && (stored == nil || *stored != p.WorkspaceID) {
								SetSessionTeam(ctx, sessions, p.WorkspaceID)
							}
							p.SessionMFA = sessions.GetBool(ctx, auth.SessionMFAKey)
							next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(ctx, p)))
							return
						}
						slog.ErrorContext(ctx, "resolve session team", "error", perr)
					}
					// Unknown user (deleted) or transient error: treat as anonymous.
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func readyz(pool interface {
	Ping(context.Context) error
}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pool != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := pool.Ping(ctx); err != nil {
				writeJSONStatus(w, http.StatusServiceUnavailable, `{"status":"unavailable","reason":"database"}`)
				return
			}
		}
		writeJSONStatus(w, http.StatusOK, `{"status":"ready"}`)
	}
}

func writeJSONStatus(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			level := slog.LevelInfo
			if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
				level = slog.LevelDebug
			}
			logger.Log(r.Context(), level, "http request",
				"method", r.Method, "path", r.URL.Path, "status", status,
				"bytes", ww.BytesWritten(), "duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()))
		})
	}
}
