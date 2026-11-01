// Package environments implements environment CRUD within a project.
package environments

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projects"
)

// Environment is the API view of an environment.
type Environment struct {
	ID           uuid.UUID `json:"id"`
	ProjectID    uuid.UUID `json:"projectId"`
	Name         string    `json:"name"`
	Slug         string    `json:"slug"`
	IsProduction bool      `json:"isProduction"`
	CreatedAt    time.Time `json:"createdAt"`
	// Kind is "standard" or "preview" (a pull-request environment).
	Kind string `json:"kind" enum:"standard,preview"`
	// ParentEnvironmentID is the base environment a preview was cloned from.
	ParentEnvironmentID *uuid.UUID `json:"parentEnvironmentId" nullable:"true"`
	// PRNumber and PRTitle describe the pull request of a preview.
	PRNumber *int   `json:"prNumber"`
	PRTitle  string `json:"prTitle"`
}

func toEnvironment(e db.Environment) Environment {
	var pr *int
	if e.PrNumber != nil {
		n := int(*e.PrNumber)
		pr = &n
	}
	return Environment{
		ID: e.ID, ProjectID: e.ProjectID, Name: e.Name, Slug: e.Slug, IsProduction: e.IsProduction, CreatedAt: e.CreatedAt,
		Kind: e.Kind, ParentEnvironmentID: e.ParentEnvironmentID, PRNumber: pr, PRTitle: e.PrTitle,
	}
}

// Service implements the environment use cases.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
}

// NewService returns a Service (pool may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus) *Service {
	return &Service{pool: pool, q: db.New(pool), bus: bus}
}

// List returns a project's environments, production first.
func (s *Service) List(ctx context.Context, projectID uuid.UUID) ([]Environment, error) {
	if err := s.authorizeProject(ctx, policy.ScopeProjectsRead, projectID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListEnvironments(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	out := make([]Environment, 0, len(rows))
	for _, r := range rows {
		out = append(out, toEnvironment(r))
	}
	return out, nil
}

// Get returns one environment.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Environment, error) {
	e, err := s.load(ctx, policy.ScopeProjectsRead, id)
	if err != nil {
		return Environment{}, err
	}
	return toEnvironment(e), nil
}

// CreateInput describes a new environment.
type CreateInput struct {
	Name         string
	IsProduction bool
}

// Create adds an environment to a project.
func (s *Service) Create(ctx context.Context, projectID uuid.UUID, in CreateInput) (Environment, error) {
	if err := s.authorizeProject(ctx, policy.ScopeProjectsWrite, projectID); err != nil {
		return Environment{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return Environment{}, apierr.Unprocessable("validation_failed", "name is required")
	}
	base := projects.Slugify(name, "environment")
	if err := projects.CheckEnvironmentSlug(base); err != nil {
		return Environment{}, err
	}
	var row db.Environment
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		taken, err := q.ListEnvironmentSlugsLike(ctx, db.ListEnvironmentSlugsLikeParams{ProjectID: projectID, Base: base})
		if err != nil {
			return err
		}
		row, err = q.CreateEnvironment(ctx, db.CreateEnvironmentParams{
			ProjectID: projectID, Name: name, Slug: projects.UniqueSlug(base, taken), IsProduction: in.IsProduction,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "environment.create", ResourceType: "environment", ResourceID: row.ID.String(), ProjectID: &projectID,
			Metadata: map[string]any{"name": row.Name, "slug": row.Slug},
		})
	})
	if projects.IsUniqueViolation(err) {
		return Environment{}, apierr.Conflict("slug_conflict", "an environment with that name was just created; retry")
	}
	if err != nil {
		return Environment{}, fmt.Errorf("create environment: %w", err)
	}
	s.publish(ctx, events.TypeEnvironmentCreated, row)
	return toEnvironment(row), nil
}

// UpdateInput holds the optional fields of an environment PATCH.
type UpdateInput struct {
	Name         *string
	IsProduction *bool
}

// Update renames an environment or flips its production flag. The slug is stable.
func (s *Service) Update(ctx context.Context, id uuid.UUID, in UpdateInput) (Environment, error) {
	cur, err := s.load(ctx, policy.ScopeProjectsWrite, id)
	if err != nil {
		return Environment{}, err
	}
	name, prod := cur.Name, cur.IsProduction
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
		if name == "" {
			return Environment{}, apierr.Unprocessable("validation_failed", "name must not be empty")
		}
	}
	if in.IsProduction != nil {
		prod = *in.IsProduction
	}
	if cur.IsProduction && !prod {
		if err := s.guard(ctx, cur, guardUnmarkProd, "Stop treating %s as a production environment (%s)", false,
			"Removes the production flag. Production guards (approvals for AI clients, warnings in the UI) no longer apply here."); err != nil {
			return Environment{}, err
		}
	}
	var row db.Environment
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		row, err = q.UpdateEnvironment(ctx, db.UpdateEnvironmentParams{ID: id, Name: name, IsProduction: prod})
		if err != nil {
			return err
		}
		if prod {
			// nebula.toml auto-apply is never on for production (internal/configsync).
			if err := q.ClearEnvironmentConfigAutoApply(ctx, id); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "environment.update", ResourceType: "environment", ResourceID: id.String(), ProjectID: &cur.ProjectID,
			Metadata: map[string]any{"name": name, "isProduction": prod},
		})
	})
	if err != nil {
		return Environment{}, fmt.Errorf("update environment: %w", err)
	}
	s.publish(ctx, events.TypeEnvironmentUpdated, row)
	return toEnvironment(row), nil
}

// Delete removes an environment that has no services.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	cur, err := s.load(ctx, policy.ScopeProjectsWrite, id)
	if err != nil {
		return err
	}
	if err := s.guard(ctx, cur, guardDelete, "Delete the production environment %s (%s)", true,
		"Removes the environment, its canvas layout and its settings. It must hold no services."); err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		n, err := q.CountEnvironmentLiveServices(ctx, id)
		if err != nil {
			return err
		}
		if n > 0 {
			return apierr.Conflict("environment_not_empty", "delete the environment's services first")
		}
		if p, err := q.CountEnvironmentPreviews(ctx, &id); err != nil {
			return err
		} else if p > 0 {
			return apierr.Conflict("environment_has_previews", "delete or close the environment's pull-request previews first")
		}
		if _, err := q.DeleteEnvironment(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "environment.delete", ResourceType: "environment", ResourceID: id.String(), ProjectID: &cur.ProjectID,
			Metadata: map[string]any{"name": cur.Name, "slug": cur.Slug},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("delete environment: %w", err)
	}
	s.publish(ctx, events.TypeEnvironmentDeleted, cur)
	return nil
}

// authorizeProject checks scope + project visibility and that the project
// exists in the caller's workspace.
func (s *Service) authorizeProject(ctx context.Context, scope string, projectID uuid.UUID) error {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return err
	}
	if err := policy.Require(ctx, scope, &projectID); err != nil {
		return err
	}
	if _, err := s.q.GetProject(ctx, db.GetProjectParams{ID: projectID, WorkspaceID: policy.WorkspaceID(ctx)}); errors.Is(err, pgx.ErrNoRows) {
		return apierr.NotFound("")
	} else if err != nil {
		return fmt.Errorf("get project: %w", err)
	}
	return nil
}

// load authorizes scope and returns the environment, joined through the
// caller's workspace and project restriction.
func (s *Service) load(ctx context.Context, scope string, id uuid.UUID) (db.Environment, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.Environment{}, err
	}
	e, err := s.q.GetEnvironmentScoped(ctx, db.GetEnvironmentScopedParams{ID: id, WorkspaceID: policy.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Environment{}, apierr.NotFound("")
	}
	if err != nil {
		return db.Environment{}, fmt.Errorf("get environment: %w", err)
	}
	if err := policy.Require(ctx, scope, &e.ProjectID); err != nil {
		return db.Environment{}, err
	}
	return e, nil
}

// publish announces an environment change to the caller's team (the row of a
// deletion is gone, so the feed could not look its team up).
func (s *Service) publish(ctx context.Context, typ string, e db.Environment) {
	if s.bus == nil {
		return
	}
	ev := events.Event{Type: typ, ProjectID: e.ProjectID.String(), EnvironmentID: e.ID.String(), ResourceID: e.ID.String()}
	if ws := policy.WorkspaceID(ctx); ws != uuid.Nil {
		ev.WorkspaceID = ws.String()
	}
	s.bus.Publish(ev)
}
