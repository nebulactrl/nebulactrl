// Package clone copies a service or a whole environment and moves a service to
// another environment.
//
// A clone is staged like every new service: nothing runs until it is deployed.
// It carries the configuration (Docker options included), the variables, the
// volume definitions, the repository link and the links of the source. It never
// carries running state:
//
//   - secrets are copied sealed: each value is opened and sealed again for its
//     new row inside the transaction, and no value appears in a result, an
//     audit entry or a log;
//   - variables that are generated (secret(32), uuid(), ...) are generated
//     again, so a staging copy never shares a secret with production;
//   - managed databases are created new and empty (fresh credentials, empty
//     data volume): restore a dump into them (docs/operations/database-backups.md);
//   - other volumes are new and empty: the volume definition is copied, the
//     data is not;
//   - hostnames are globally unique, so domains are dropped (the default) or
//     suffixed (app.example.com becomes app-staging.example.com); a taken
//     hostname is reported and fails the clone;
//   - schedules (backups, cron tasks), deploy hooks, shared-network
//     attachments, tailnet settings and replica groups are not copied.
package clone

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Domain modes of a clone.
const (
	// DomainsDrop clones no domain: the user attaches new ones (the default).
	DomainsDrop = "drop"
	// DomainsSuffix clones each public domain with a suffix on its first label.
	DomainsSuffix = "suffix"
)

// Deps are the collaborators of a Service. Pool, Box and Services may be nil
// for OpenAPI generation only.
type Deps struct {
	Pool     *pgxpool.Pool
	Bus      *events.Bus
	Box      *secretbox.Box
	Services *services.Service
	// Databases creates the new, empty databases of a clone.
	Databases *databases.Service
	// Jobs schedules the DNS check of cloned domains (nil: no check is scheduled).
	Jobs *jobs.Manager
	// Git links cloned git services to the repository of their source (nil: no link).
	Git GitLinker
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

// GitLinker links a service to the repository another service is linked to.
type GitLinker interface {
	// CopyServiceGitTx links to the same connection and repository as from, with
	// auto-deploy off. It is a no-op when from has no link.
	CopyServiceGitTx(ctx context.Context, tx pgx.Tx, from, to uuid.UUID) error
}

// Service implements clone and move.
type Service struct {
	Deps
	q *db.Queries
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{Deps: d, q: db.New(d.Pool)}
}

// CloneEnvironmentRef names an environment in a result.
type CloneEnvironmentRef struct {
	ID           uuid.UUID `json:"id"`
	ProjectID    uuid.UUID `json:"projectId"`
	Name         string    `json:"name"`
	Slug         string    `json:"slug"`
	IsProduction bool      `json:"isProduction"`
}

func envRef(e db.Environment) CloneEnvironmentRef {
	return CloneEnvironmentRef{ID: e.ID, ProjectID: e.ProjectID, Name: e.Name, Slug: e.Slug, IsProduction: e.IsProduction}
}

// ClonedService is one service a clone created (or would create).
type ClonedService struct {
	SourceID uuid.UUID `json:"sourceId"`
	// ID is the new service (nil UUID in a dry run).
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Slug string    `json:"slug"`
	Kind string    `json:"kind"`
	// Fresh is true for a managed database: new credentials and an empty volume.
	Fresh bool `json:"fresh"`
}

// CloneSkipped is a service a clone left out, with the reason.
type CloneSkipped struct {
	Slug   string `json:"slug"`
	Reason string `json:"reason"`
}

// CloneDomainOutcome says what happened to one domain of a source service.
type CloneDomainOutcome struct {
	// Service is the slug of the cloned service.
	Service  string `json:"service"`
	Hostname string `json:"hostname"`
	// To is the hostname the clone got (empty when dropped or skipped).
	To     string `json:"to,omitempty"`
	Action string `json:"action" enum:"dropped,suffixed,generated,conflict,skipped"`
	Reason string `json:"reason,omitempty"`
}

// Domain actions.
const (
	ActionDropped   = "dropped"
	ActionSuffixed  = "suffixed"
	ActionGenerated = "generated"
	ActionConflict  = "conflict"
	ActionSkipped   = "skipped"
)

// ReferenceFinding is a variable reference that does not resolve (or resolves to a
// different service) after a clone or move.
type ReferenceFinding struct {
	// Service is the slug of the service holding the variable.
	Service string `json:"service"`
	Key     string `json:"key"`
	// Ref is the reference as written without the braces, for example postgres.DATABASE_URL.
	Ref     string `json:"ref"`
	Status  string `json:"status" enum:"unresolved,rebound"`
	Message string `json:"message"`
}

// Reference statuses.
const (
	RefUnresolved = "unresolved"
	RefRebound    = "rebound"
)

// CloneResult is the outcome of a clone (or its dry run).
type CloneResult struct {
	DryRun bool `json:"dryRun"`
	// Environment is the new environment (environment clones only).
	Environment *CloneEnvironmentRef `json:"environment,omitempty"`
	// Service is the new service (service clones only; absent in a dry run).
	Service         *services.ServiceView `json:"service,omitempty"`
	Services        []ClonedService       `json:"services"`
	Skipped         []CloneSkipped        `json:"skipped"`
	Domains         []CloneDomainOutcome  `json:"domains"`
	References      []ReferenceFinding    `json:"references"`
	Links           int                   `json:"links"`
	SharedVariables int                   `json:"sharedVariables"`
	Warnings        []string              `json:"warnings"`
}

func newResult(dry bool) CloneResult {
	return CloneResult{DryRun: dry, Services: []ClonedService{}, Skipped: []CloneSkipped{}, Domains: []CloneDomainOutcome{}, References: []ReferenceFinding{}, Warnings: []string{}}
}

// errDryRun rolls a transaction back after a dry run completed.
var errDryRun = errors.New("dry run")

// runTx runs fn in a transaction that is committed, or rolled back for a dry run.
func (s *Service) runTx(ctx context.Context, dry bool, fn func(tx pgx.Tx) error) error {
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if dry {
			return errDryRun
		}
		return nil
	})
	if dry && errors.Is(err, errDryRun) {
		return nil
	}
	return err
}

// guardTarget rejects environments a clone or move must not write into.
func (s *Service) guardTarget(ctx context.Context, q *db.Queries, env services.EnvScope) error {
	if env.Env.Kind == "preview" {
		return apierr.Unprocessable("preview_environment", "pull-request preview environments are managed by their pull request; choose a regular environment")
	}
	proj, err := q.GetProject(ctx, db.GetProjectParams{ID: env.Env.ProjectID, WorkspaceID: policy.WorkspaceID(ctx)})
	if err != nil {
		return err
	}
	if proj.System {
		return apierr.Unprocessable("not_applicable", "services of the platform project cannot be cloned or moved")
	}
	return nil
}

// cloneable reports why a service cannot be cloned ("" when it can).
func cloneable(src db.Service) string {
	switch src.Kind {
	case services.KindRoute:
		return "route services expose an external upstream and are not cloned: add the route in the target environment"
	case services.KindStack:
		return "Compose stacks are not cloned: import the compose file in the target environment"
	}
	if d := services.DecodeConfig(src.Config).Database; d != nil && d.Replica != nil {
		return "a read replica is not cloned: add a replica to the cloned primary instead"
	}
	return ""
}

func trimName(name string) string { return strings.TrimSpace(name) }
