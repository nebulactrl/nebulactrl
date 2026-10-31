// Package mcp exposes NebulaCtrl to AI agents over the Model Context Protocol
// (Streamable HTTP, mounted at /mcp). The endpoint answers only while it is
// switched on: by NEBULACTRL_MCP_ENABLED=true, or by the instance admin from
// the web UI when the variable is not set (see Switch).
//
// The tool surface has three layers, all filtered by the caller's scopes:
//
//   - composites (internal/mcp/composites): one call for a whole workflow;
//   - curated primitives: named tools generated from REST operations by the
//     catalog (internal/mcp/catalog), plus a few hand-written native tools;
//   - the long tail: every other allowed operation through find_operation and
//     call_operation.
//
// Generated tools call the root http.Handler in-process with the caller's own
// bearer token, so authentication, scopes, project restriction, idempotency,
// audit and the production guard are exactly the REST ones; the request is
// marked (by a context value no network request can set) so audit rows record
// actor type `mcp`. The caller authenticates with an API token as
// `Authorization: Bearer nct_…`; sessions and query-string tokens are never
// accepted.
package mcp

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/time/rate"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/configsync"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/environments"
	"github.com/nebulactrl/nebulactrl/internal/mcp/catalog"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/tasks"
	"github.com/nebulactrl/nebulactrl/internal/templates"
	"github.com/nebulactrl/nebulactrl/internal/variables"
	"github.com/nebulactrl/nebulactrl/internal/version"
)

// Path is where the MCP endpoint is mounted.
const Path = "/mcp"

// RequestsPerMinute is the per-token request budget.
const RequestsPerMinute = 60

// ToolsetsParam is the URL query parameter narrowing the listed toolsets:
// /mcp?toolsets=core,config.
const ToolsetsParam = "toolsets"

// Deps are the collaborators of the MCP server. Services are the same
// instances the REST handlers use. Pool and the services may be nil when
// building only for OpenAPI generation.
type Deps struct {
	Config       *config.Config
	Logger       *slog.Logger
	Pool         *pgxpool.Pool
	Auth         *auth.Service
	Projects     *projects.Service
	Environments *environments.Service
	Servers      *servers.Service
	Services     *services.Service
	Deployments  *deployments.Service
	Variables    *variables.Service
	Domains      *domains.Service
	Databases    *databases.Service
	Audit        *audit.Service
	// Templates backs list_templates, plan_template and install_template; nil
	// leaves those tools unregistered.
	Templates *templates.Service
	// Backups backs list_backup_destinations, list_backups, create_backup and
	// restore_backup; nil leaves those tools unregistered.
	Backups *backups.Service
	// Tasks backs run_command and get_task_run; nil leaves those tools
	// unregistered.
	Tasks *tasks.Service
	// ConfigSync backs sync_config; nil leaves the tool answering
	// `unavailable`.
	ConfigSync *configsync.Service
	// Switch decides whether /mcp answers. Nil builds one from Config alone
	// (the environment decides; tests and OpenAPI generation).
	Switch *Switch
}

// Handler serves /mcp.
type Handler struct {
	deps     Deps
	log      *slog.Logger
	limiter  *tokenLimiter
	http     *mcp.StreamableHTTPHandler
	resolver *mcpkit.Resolver
	disp     dispatcher
	reg      atomic.Pointer[registry]
}

// New builds the handler. It never touches the database. Until Attach is
// called only the composite and native tools exist.
func New(d Deps) *Handler {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Switch == nil {
		d.Switch = NewSwitch(d.Config, nil, nil)
	}
	if d.Config == nil {
		d.Config = &config.Config{}
	}
	h := &Handler{
		deps:    d,
		log:     d.Logger.With("component", "mcp"),
		limiter: newTokenLimiter(RequestsPerMinute, time.Minute),
	}
	if d.Projects != nil && d.Environments != nil && d.Services != nil {
		h.resolver = mcpkit.NewResolver(d.Projects, d.Environments, d.Services, d.Pool)
	}
	// Stateless: every request builds its own server bound to that request's
	// principal, so a session cannot outlive or be reused by another token.
	h.http = mcp.NewStreamableHTTPHandler(h.serverFor, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		Logger:       h.log,
	})
	if reg, err := h.buildRegistry(nil, nil); err == nil {
		h.reg.Store(reg)
	} else {
		h.log.Error("build mcp registry", "error", err)
		h.reg.Store(&registry{byOp: map[string]*opEntry{}, byName: map[string]*regTool{}})
	}
	return h
}

// Attach connects the handler to the assembled API: root is the router the
// generated tools dispatch to in-process, api describes its operations. It
// builds the registry from the catalog and must be called once, after the API
// is built and before requests are served.
func (h *Handler) Attach(root http.Handler, api huma.API) error {
	return h.attach(root, api, catalog.Manifest)
}

func (h *Handler) attach(root http.Handler, api huma.API, manifest map[string]catalog.Entry) error {
	ops, err := buildOps(api)
	if err != nil {
		return err
	}
	reg, err := h.buildRegistry(ops, manifest)
	if err != nil {
		return err
	}
	h.disp.attach(root)
	h.reg.Store(reg)
	return nil
}

// Mount registers /mcp and /mcp/* on the root router. When MCP is disabled
// the paths answer 404 (also when an SPA fallback is installed).
func (h *Handler) Mount(r chi.Router) {
	r.Handle(Path, h)
	r.Handle(Path+"/*", h)
}

type serverKey struct{}

// serverFor returns the server built for this request in ServeHTTP (the SDK
// asks twice per request; both calls get the same instance).
func (h *Handler) serverFor(r *http.Request) *mcp.Server {
	s, _ := r.Context().Value(serverKey{}).(*mcp.Server)
	return s
}

// ServeHTTP authenticates the bearer token, applies the rate limit and hands
// the request to the SDK.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.deps.Switch.Enabled() || h.deps.Auth == nil {
		apierr.Write(w, apierr.NotFound("route"))
		return
	}
	raw, ok := bearerToken(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="nebulactrl-mcp"`)
		apierr.Write(w, apierr.Unauthorized())
		return
	}
	p, err := h.deps.Auth.AuthenticateBearer(r.Context(), raw)
	if err != nil {
		if ae, ok := apierr.As(err); ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nebulactrl-mcp", error="invalid_token"`)
			apierr.Write(w, ae)
		} else {
			h.log.ErrorContext(r.Context(), "authenticate bearer", "error", err)
			apierr.Write(w, apierr.Internal())
		}
		return
	}
	if p.TokenKind != auth.TokenKindMCP {
		// REST and CLI tokens never reach the agent surface: an MCP token is
		// the one a person created for an agent, with the approval flow on.
		w.Header().Set("WWW-Authenticate", `Bearer realm="nebulactrl-mcp", error="insufficient_scope"`)
		apierr.Write(w, auth.ErrRESTTokenOnMCP)
		return
	}
	if !h.limiter.allow(p.ID()) {
		w.Header().Set("Retry-After", "1")
		apierr.Write(w, apierr.TooManyRequests("MCP rate limit exceeded (60 requests per minute per token)"))
		return
	}
	toolsets, err := parseToolsets(r.URL.Query()[ToolsetsParam])
	if err != nil {
		apierr.Write(w, apierr.BadRequest("invalid_toolsets", err.Error()))
		return
	}
	mp := *p
	mp.Type = auth.TypeMCP
	e := &env{h: h, p: &mp, token: raw, remote: r.RemoteAddr, ua: r.UserAgent(), toolsets: toolsets, listed: toolsets}
	if toolsets == nil {
		// No explicit selection: list the lean default; everything allowed
		// stays reachable through call_operation.
		e.listed = DefaultToolsets()
	}
	h.http.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), serverKey{}, h.newServer(e))))
}

// parseToolsets reads `?toolsets=core,config` (repeatable). nil means every
// toolset.
func parseToolsets(values []string) (map[string]bool, error) {
	var out map[string]bool
	for _, v := range values {
		for _, name := range strings.Split(v, ",") {
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" {
				continue
			}
			valid := false
			for _, ts := range mcpkit.Toolsets {
				if ts == name {
					valid = true
				}
			}
			if !valid {
				return nil, &toolsetError{name}
			}
			if out == nil {
				out = map[string]bool{}
			}
			out[name] = true
		}
	}
	return out, nil
}

type toolsetError struct{ name string }

func (e *toolsetError) Error() string {
	return "unknown toolset " + `"` + e.name + `"` + "; valid toolsets: " + strings.Join(mcpkit.Toolsets, ", ")
}

// newServer builds the MCP server for one authenticated request: the tools the
// principal's scopes allow (within the selected toolsets), the prompts whose
// tools are all listed, and the resources.
func (h *Handler) newServer(e *env) *mcp.Server {
	s := mcp.NewServer(
		&mcp.Implementation{Name: "nebulactrl", Title: "NebulaCtrl", Version: version.Current()},
		&mcp.ServerOptions{
			Instructions: instructions,
			Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
		},
	)
	reg := h.reg.Load()
	visible := map[string]bool{}
	for _, rt := range reg.visible(e.p, e.listed) {
		rt := rt
		visible[rt.name] = true
		s.AddTool(rt.mcpTool(), func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return e.handle(ctx, req, rt), nil
		})
	}
	e.addPrompts(s, visible)
	e.addResources(s, reg, visible)
	return s
}

func bearerToken(r *http.Request) (string, bool) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	raw = strings.TrimSpace(raw)
	return raw, ok && raw != ""
}

// tokenLimiter is a per-credential token bucket.
type tokenLimiter struct {
	mu      sync.Mutex
	perMin  int
	window  time.Duration
	entries map[uuid.UUID]*limiterEntry
	now     func() time.Time
}

type limiterEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

func newTokenLimiter(perWindow int, window time.Duration) *tokenLimiter {
	return &tokenLimiter{perMin: perWindow, window: window, entries: map[uuid.UUID]*limiterEntry{}, now: time.Now}
}

func (l *tokenLimiter) allow(id uuid.UUID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.entries) > 1024 {
		for k, e := range l.entries {
			if now.Sub(e.seen) > 2*l.window {
				delete(l.entries, k)
			}
		}
	}
	e, ok := l.entries[id]
	if !ok {
		e = &limiterEntry{lim: rate.NewLimiter(rate.Every(l.window/time.Duration(l.perMin)), l.perMin)}
		l.entries[id] = e
	}
	e.seen = now
	return e.lim.AllowN(now, 1)
}

// DefaultToolsets are the toolsets listed when the URL names none: the
// composites and meta tools (core) plus the read tools (observe). config,
// data and servers are opt-in via ?toolsets=; their operations remain
// reachable through call_operation.
func DefaultToolsets() map[string]bool {
	return map[string]bool{mcpkit.ToolsetCore: true, mcpkit.ToolsetObserve: true}
}
