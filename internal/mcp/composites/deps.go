// Package composites implements the 16 workflow tools of the NebulaCtrl MCP
// server: one call that replaces four to six primitive calls (overview,
// diagnosis, log tailing, staged review/apply, rollback, recipes).
//
// Composites call the domain services in internal/* directly, so scopes,
// project restriction, audit, the staged-generation model and the production
// guard (internal/prodguard, enforced inside deployments.Deploy*/Rollback)
// apply exactly as for REST. Independent reads run concurrently with an
// errgroup that never cancels on error: a failing section lands in the
// result's `errors[]` while the rest is returned (partial failure).
//
// Every composite result is `{summary, <findings|evidence…>, errors[],
// nextTools[]}`. Log text is wrapped with mcpkit.WrapLines (untrusted, per-call
// nonce) and is already redacted by the services it comes from. The package is
// a leaf under internal/mcp: it never imports internal/mcp, which registers the
// tools returned by Tools.
package composites

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/environments"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/variables"
)

// LogSource streams runtime logs. *services.Service implements it. process
// narrows a service with processes to one of them ("" = every process).
type LogSource interface {
	SubscribeServiceProcessLogs(ctx context.Context, id uuid.UUID, tail int, follow bool, component, process string) (<-chan services.LogEntry, func(), error)
	SubscribeEnvironmentLogs(ctx context.Context, envID uuid.UUID, tail int, follow bool) (<-chan services.LogEntry, func(), error)
}

// Deps are the collaborators of the composites. They are the same service
// instances the REST handlers use.
type Deps struct {
	Pool         *pgxpool.Pool
	Logger       *slog.Logger
	Projects     *projects.Service
	Environments *environments.Service
	Servers      *servers.Service
	Services     *services.Service
	Deployments  *deployments.Service
	Variables    *variables.Service
	Domains      *domains.Service
	Databases    *databases.Service
	Audit        *audit.Service
	// ConfigSync backs sync_config (*configsync.Service). Nil makes the tool
	// answer `unavailable`.
	ConfigSync ConfigService
	// Logs defaults to Services.
	Logs LogSource
	// Resolver resolves selectors; it defaults to
	// mcpkit.NewResolver(Projects, Environments, Services, Pool).
	Resolver *mcpkit.Resolver
	// Probe is the HTTP client verify_service and setup_domain use. It
	// defaults to a client with a short timeout that never follows a redirect
	// off the probed host and never dials anything but the service's own
	// hostnames (see probe.go).
	Probe *http.Client
	// Now defaults to time.Now.
	Now func() time.Time
}

func (d *Deps) defaults() {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Logs == nil && d.Services != nil {
		d.Logs = d.Services
	}
	if d.Resolver == nil {
		d.Resolver = mcpkit.NewResolver(d.Projects, d.Environments, d.Services, d.Pool)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
}

// Tools returns the 16 composites in a deterministic order. Registration
// (scope filtering, hints, output schema, the 64 KB cap, audit) is
// internal/mcp's job.
func Tools(d Deps) []mcpkit.Tool {
	d.defaults()
	c := &composites{Deps: d}
	return []mcpkit.Tool{
		c.getOverview(),
		c.findIssues(),
		c.diagnoseService(),
		c.explainFailedDeployment(),
		c.diagnoseServer(),
		c.tailLogs(),
		c.searchResources(),
		c.reviewChanges(),
		c.stageChanges(),
		c.applyChanges(),
		c.rollbackService(),
		c.addDatabase(),
		c.setupDomain(),
		c.verifyService(),
		c.getRecentChanges(),
		c.syncConfigTool(),
	}
}

// composites binds the dependencies to the tool constructors.
type composites struct{ Deps }
