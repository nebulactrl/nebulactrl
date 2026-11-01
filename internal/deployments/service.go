package deployments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/proxyserver"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// LogFollower wakes log followers when new deployment lines arrive. The agent
// hub implements it; nil falls back to polling alone.
type LogFollower interface {
	FollowDeploymentLogs(ctx context.Context, deploymentID uuid.UUID) (<-chan agentprotocol.LogFrame, func())
}

// Deps are the collaborators of Service.
type Deps struct {
	Pool       *pgxpool.Pool
	Bus        *events.Bus
	Jobs       *jobs.Manager
	Box        *secretbox.Box
	Dispatcher agents.Dispatcher
	Follower   LogFollower
	Services   *services.Service
	Logger     *slog.Logger
	// Tailnet mints the auth key of a managed tailnet node at command send time
	// (nil: provisioning commands fail to send).
	Tailnet proxyserver.TailnetKeyMinter
	// Notifier is told how deployments end (nil: no notifications).
	Notifier notifications.Emitter
	// ExtSecrets fetches `${{vault:…}}` values at command send time (nil: none).
	ExtSecrets *services.ExtSecrets
}

// Service implements deployments, releases, planning and the agent callbacks
// (agents.SecretSource, the deployment half of agents.ResultHandler).
type Service struct {
	Deps
	q *db.Queries
	// gitAuth mints clone credentials for services linked to a git connection
	// (set after construction: the git providers service deploys through this one).
	gitAuth GitAuthSource
	// sshKeys supplies the SSH deploy keys and pinned host keys of services that
	// clone over SSH (set with SetSSHKeys).
	sshKeys SSHKeys
	// lsRemote replaces LsRemote when set (tests; see SetGitResolver).
	lsRemote func(ctx context.Context, url, branch, user, token string) (string, error)
	// ServiceTailnet is the per-service tailnet domain (nil: services have no
	// tailnet part). Set after construction with SetServiceTailnet.
	ServiceTailnet TailnetHook
	// Mesh is the cross-server service link domain (nil: services reach providers
	// on other servers at their container port only). Set with SetMesh.
	Mesh MeshHook
	// Databases is the managed-database replication domain (nil: no replicas).
	// Set with SetDatabaseHook.
	Databases DatabaseHook
	// dns re-syncs a service's DNS records after a successful deployment (the
	// service may have moved to another server). Set with SetDNS.
	dns DNSHook
	// resync pushes the server's desired state after a deployment changes
	// which services block outbound internet. Set with SetResync.
	resync Resyncer
	// registries supplies build registries and their credentials (set with
	// SetRegistries; nil: builds stay on the target).
	registries RegistryProvider
}

// NewService returns a Service (Pool, Box, Jobs and Dispatcher may be nil for
// OpenAPI generation).
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{Deps: d, q: db.New(d.Pool)}
}

// depCtx is a deployment with its service and project context.
type depCtx struct {
	Dep           db.Deployment
	EnvironmentID uuid.UUID
	ProjectID     uuid.UUID
	WorkspaceID   uuid.UUID
	ServiceSlug   string
	ServiceName   string
}

func toDepCtx(r db.GetDeploymentScopeRow) depCtx {
	return depCtx{
		Dep: r.Deployment, EnvironmentID: r.EnvironmentID, ProjectID: r.ProjectID, WorkspaceID: r.WorkspaceID,
		ServiceSlug: r.ServiceSlug, ServiceName: r.ServiceName,
	}
}

// loadDeployment authorizes scope and loads a deployment through its service,
// environment, project and workspace (404 outside the caller's reach).
func (s *Service) loadDeployment(ctx context.Context, scope string, id uuid.UUID) (depCtx, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return depCtx{}, err
	}
	ws := policy.WorkspaceID(ctx)
	row, err := s.q.GetDeploymentScope(ctx, db.GetDeploymentScopeParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return depCtx{}, apierr.NotFound("deployment")
	}
	if err != nil {
		return depCtx{}, fmt.Errorf("get deployment: %w", err)
	}
	if err := policy.Require(ctx, scope, &row.ProjectID); err != nil {
		return depCtx{}, err
	}
	return toDepCtx(row), nil
}

func (s *Service) loadDeploymentInternal(ctx context.Context, id uuid.UUID) (depCtx, error) {
	row, err := s.q.GetDeploymentScopeInternal(ctx, id)
	if err != nil {
		return depCtx{}, err
	}
	return toDepCtx(db.GetDeploymentScopeRow(row)), nil
}

// publish announces that a deployment changed.
func (s *Service) publish(d depCtx) {
	services.Publish(s.Bus, services.EventDeploymentUpdated, d.ProjectID, d.EnvironmentID, d.Dep.ID)
}

// publishService announces that the service view changed (status chips, staged
// markers) because of a deployment event.
func (s *Service) publishService(d depCtx) {
	services.Publish(s.Bus, services.EventServiceUpdated, d.ProjectID, d.EnvironmentID, d.Dep.ServiceID)
}
