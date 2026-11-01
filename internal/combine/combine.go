// Package combine merges several git services of one environment into one
// service with several processes: the services keep one repository, one build,
// one set of variables and one release, and each former service becomes a
// process of the target.
//
// A combine is a saga whose every step is recorded in service_combines.state,
// so it can be resumed after a crash and each step runs more than once without
// harm:
//
//   - staged: in one transaction the target gains the sources' commands,
//     ports, health checks, scale settings and variables as staged changes.
//     The sources are untouched and keep serving.
//   - deploying: one deployment of the target builds the image once and rolls
//     every process (the normal multi-process deploy, all or nothing).
//   - cutover: once the target's release runs every process, one transaction
//     moves the sources' domains and scheduled tasks to the target, sends the
//     server one route table (each hostname flips from the old container to the
//     new process atomically) and deletes the sources.
//   - failed: the deployment failed; nothing was deleted, no domain moved, the
//     sources still serve. The user retries or aborts.
//
// Nothing is deleted before the new release serves, and a failure before the
// cutover leaves the sources exactly as they were.
package combine

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// guardOp is the production guard digest of a combine.
const guardOp = "combine_services"

// ServiceOps is the part of services.Service a combine uses.
type ServiceOps interface {
	UpdateTx(ctx context.Context, tx pgx.Tx, sc services.Scope, expected int64, in services.UpdateInput) (db.Service, func(context.Context), error)
	Stop(ctx context.Context, id uuid.UUID) (services.ActionResult, error)
	Restart(ctx context.Context, id uuid.UUID) (services.ActionResult, error)
	DeleteInternalTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, opts services.DeleteOptions) (func(context.Context), error)
}

// DomainOps is the part of domains.Service a combine uses.
type DomainOps interface {
	MoveToService(ctx context.Context, tx pgx.Tx, domainIDs []uuid.UUID, targetServiceID uuid.UUID, process string) error
}

// Deployer is the part of deployments.Service a combine uses.
type Deployer interface {
	DeployWithOptions(ctx context.Context, serviceID uuid.UUID, o deployments.DeployOptions) (deployments.DeploymentView, error)
	// DiscardFields takes staged changes (pending-change fields) off a service.
	DiscardFields(ctx context.Context, serviceID uuid.UUID, fields []string) error
}

// Deps are the collaborators of a Service. Pool, Box, Services, Domains,
// Deployments and Dispatcher may be nil for OpenAPI generation only.
type Deps struct {
	Pool        *pgxpool.Pool
	Bus         *events.Bus
	Box         *secretbox.Box
	Jobs        *jobs.Manager
	Services    ServiceOps
	Domains     DomainOps
	Deployments Deployer
	Dispatcher  agents.Dispatcher
	Logger      *slog.Logger
}

// Service implements combine.
type Service struct {
	Deps
	q *db.Queries
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	var q *db.Queries
	if d.Pool != nil {
		q = db.New(d.Pool)
	}
	return &Service{Deps: d, q: q}
}

// systemCtx carries the control plane's principal for the combine's project,
// so the saga can stop, restart and deploy services in a job. The request that
// started the combine carried the production approval.
func systemCtx(ctx context.Context, workspaceID, projectID uuid.UUID) context.Context {
	p := auth.System(workspaceID, projectID, "Combine services",
		policy.ScopeProjectsRead, policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite)
	return auth.WithPrincipal(ctx, p)
}

// requireWrite checks the two scopes every mutating combine call needs.
func requireWrite(ctx context.Context, projectID *uuid.UUID) error {
	if err := policy.Require(ctx, policy.ScopeProjectsWrite, projectID); err != nil {
		return err
	}
	return policy.Require(ctx, policy.ScopeDeploymentsWrite, projectID)
}

// errNoRows reports whether err is "no row": a compare-and-swap that lost.
func errNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
