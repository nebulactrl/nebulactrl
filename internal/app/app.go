// Package app assembles the control plane: database pool, shared services and
// the HTTP handler. cmd/server runs it; integration tests build it in-process.
//
// To add a domain module (servers, agents, services, deployments, …) edit
// wire.go only.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/alexedwards/scs/v2"
	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/localserver"
	mcpserver "github.com/nebulactrl/nebulactrl/internal/mcp"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/statusroute"
	"github.com/nebulactrl/nebulactrl/internal/systemroute"
)

// Core is the set of shared building blocks handed to every module in wire.go.
// Pool is nil when building only for OpenAPI generation; modules must not touch
// it at construction time.
type Core struct {
	Config   *config.Config
	Logger   *slog.Logger
	Pool     *pgxpool.Pool
	Bus      *events.Bus
	Jobs     *jobs.Manager
	Box      *secretbox.Box
	Auth     *auth.Service
	Sessions *scs.SessionManager
}

// App is the assembled control plane.
type App struct {
	Core
	Handler http.Handler
	API     huma.API
	// MCP is the MCP server, attached to Handler and API.
	MCP *mcpserver.Handler
	// Status is the public status-page listener's handler (NEBULACTRL_STATUS_LISTEN):
	// it serves published status pages by Host and nothing else. Nil when the
	// module is not wired.
	Status http.Handler

	sessionStore *pgxstore.PostgresStore
	// starts are the modules' start hooks (Modules.Start).
	starts []func(ctx context.Context) error
}

// Build assembles the app. pool may be nil (OpenAPI generation); in that case
// no job client, session store or key material is required.
func Build(cfg *config.Config, pool *pgxpool.Pool, logger *slog.Logger) (*App, error) {
	if logger == nil {
		logger = slog.Default()
	}
	a := &App{}
	a.Core = Core{
		Config: cfg, Logger: logger, Pool: pool,
		Bus:  events.NewBus(),
		Jobs: jobs.New(pool, logger),
		Auth: auth.NewService(pool, policy.ScopesForRole),
	}
	if cfg != nil && cfg.Installer.DashboardUpstream != "" {
		systemroute.Configure(cfg) // the dashboard's system route (installer layout)
	}
	statusroute.Configure(cfg) // the status listener's upstream for published pages
	if len(cfg.SecretKey) > 0 {
		box, err := secretbox.NewRing(cfg.SecretKey, cfg.PreviousSecretKeys...)
		if err != nil {
			return nil, err
		}
		a.Box = box
	}
	a.Auth.Configure(a.Box, cfg.PublicURL)
	wireClientIP(pool) // the rate limiters key on the visitor, not on Traefik or Cloudflare's edge
	a.Sessions = newSessionManager(cfg)
	if pool != nil {
		a.sessionStore = pgxstore.New(pool)
		a.Sessions.Store = a.sessionStore
	}

	mods := wire(&a.Core)
	a.Handler, a.API = api.New(cfg, api.Deps{
		Logger:   logger,
		Pool:     pool,
		Auth:     a.Auth,
		Sessions: a.Sessions,
		Bus:      a.Bus,
		Register: mods.Register,
		MountAPI: mods.MountAPI,
		Mount:    mods.Mount,
		Fallback: mods.Fallback,
	})
	if err := mods.MCP.Attach(a.Handler, a.API); err != nil {
		return nil, fmt.Errorf("attach mcp: %w", err)
	}
	a.MCP = mods.MCP
	a.Status = mods.Status
	a.starts = mods.Start
	return a, nil
}

// Close releases background resources owned by the app (not the pool).
func (a *App) Close() {
	if a.sessionStore != nil {
		a.sessionStore.StopCleanup()
	}
}

func newSessionManager(cfg *config.Config) *scs.SessionManager {
	sm := scs.New()
	sm.Lifetime = 30 * 24 * time.Hour
	sm.IdleTimeout = 7 * 24 * time.Hour
	sm.Cookie.Name = api.SessionCookieName
	sm.Cookie.HttpOnly = true
	sm.Cookie.SameSite = http.SameSiteLaxMode
	sm.Cookie.Secure = cfg.SecureCookies()
	sm.Cookie.Path = "/"
	sm.Cookie.Persist = true
	return sm
}

// Start runs the modules' start hooks (stored settings that shape the routes
// pushed to agents are loaded first), then launches background workers (River,
// and the installer's local-server enrollment when NEBULACTRL_LOCAL_ENROLL_TOKEN
// is set). Call after db.Migrate and Jobs.Migrate.
func (a *App) Start(ctx context.Context) error {
	if a.Pool != nil {
		for _, start := range a.starts {
			if err := start(ctx); err != nil {
				return err
			}
		}
	}
	if err := a.Jobs.Start(ctx); err != nil {
		return err
	}
	if a.Pool != nil && a.Config.Installer.LocalEnrollToken != "" {
		go localserver.New(a.Pool, a.Bus, a.Config, a.Logger).Run(ctx)
	}
	return nil
}
