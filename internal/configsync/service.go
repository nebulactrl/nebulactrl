package configsync

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/integrations/cloudflare"
	"github.com/nebulactrl/nebulactrl/internal/networks"
	"github.com/nebulactrl/nebulactrl/internal/proxyroutes"
	"github.com/nebulactrl/nebulactrl/internal/scaling"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/tasks"
	"github.com/nebulactrl/nebulactrl/internal/uptime"
	"github.com/nebulactrl/nebulactrl/internal/variables"
)

// The immediate-kind use cases apply runs after the staged transaction commits.
// They are interfaces so tests can make a step fail.

// Scaler sets replicas and autoscaling of a service and of its named processes (scaling.Service).
type Scaler interface {
	Set(ctx context.Context, id uuid.UUID, in scaling.SetInput) (services.ServiceView, error)
	SetProcess(ctx context.Context, id uuid.UUID, process string, in scaling.SetInput) (services.ServiceView, error)
}

// TaskStore manages cron tasks (tasks.Service).
type TaskStore interface {
	CreateTask(ctx context.Context, serviceID uuid.UUID, in tasks.TaskInput) (tasks.TaskView, error)
	UpdateTask(ctx context.Context, id uuid.UUID, in tasks.TaskPatch) (tasks.TaskView, error)
	DeleteTask(ctx context.Context, id uuid.UUID) error
}

// BackupStore manages backup schedules (backups.Service).
type BackupStore interface {
	CreateSchedule(ctx context.Context, serviceID uuid.UUID, in backups.ScheduleInput) (backups.ScheduleView, error)
	UpdateSchedule(ctx context.Context, id uuid.UUID, p backups.SchedulePatch) (backups.ScheduleView, error)
	DeleteSchedule(ctx context.Context, id uuid.UUID) error
}

// MonitorStore replaces a service's uptime monitor (uptime.Service).
type MonitorStore interface {
	UpdateMonitor(ctx context.Context, serviceID uuid.UUID, v uptime.MonitorView) (uptime.MonitorView, error)
}

// GitLinker links a service to a repository (gitproviders.Service).
type GitLinker interface {
	Link(ctx context.Context, serviceID uuid.UUID, in gitproviders.LinkInput) (gitproviders.GitLinkView, error)
}

// DNSSettings changes the Cloudflare settings of a domain (cloudflare.Service).
type DNSSettings interface {
	SetSettings(ctx context.Context, domainID uuid.UUID, in cloudflare.SettingsInput) (cloudflare.DNSStatus, error)
}

// Deployer deploys the staged changes of chosen services (deployments.Service).
type Deployer interface {
	DeployStaged(ctx context.Context, envID uuid.UUID, serviceIDs []uuid.UUID, message string) (deployments.StagedResult, error)
}

// Deps are the collaborators of the config service. Pool may be nil for OpenAPI generation.
type Deps struct {
	Pool *pgxpool.Pool
	Bus  *events.Bus
	// PublicURL is the instance's public origin; export writes it into the #:schema header.
	PublicURL string
	// CloneURL reads the clone URL of a repository on a git connection (gitproviders.Service.CloneURLFor);
	// the plan uses it to fill the git URL of a service linked through a connection. Nil: not available.
	CloneURL func(ctx context.Context, connectionID uuid.UUID, repo string) (cloneURL, branch string, err error)
	Box      *secretbox.Box
	Logger   *slog.Logger

	// Staged changes run inside the apply transaction through these.
	Services  *services.Service
	Variables *variables.Service
	Domains   *domains.Service
	Databases *databases.Service
	Routes    *proxyroutes.Service
	Networks  *networks.Service

	// Immediate changes run after it commits.
	Scaling    Scaler
	Tasks      TaskStore
	Backups    BackupStore
	Uptime     MonitorStore
	Git        GitLinker
	Cloudflare DNSSettings
	Deploy     Deployer
}

// Service implements validate, plan, apply and export of nebula.toml.
type Service struct {
	Deps
	q       *db.Queries
	binding Binding
}

// NewService returns a Service (Deps.Pool may be nil for OpenAPI generation).
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	s := &Service{Deps: d}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	return s
}

// SetBinding installs the environment binding (wiring only; call before serving).
func (s *Service) SetBinding(b Binding) { s.binding = b }
