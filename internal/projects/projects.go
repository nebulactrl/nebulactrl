// Package projects implements project CRUD. Creating a project also creates
// its `production` environment.
package projects

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/buildplacement"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
)

// Project is the API view of a project.
type Project struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description"`
	// System marks the team's "NebulaCtrl platform" project, which holds the
	// platform services. It is hidden from the list and cannot be renamed or deleted.
	System    bool      `json:"system"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	// BuildLocation overrides the team's default build location for this
	// project's services: inherit, target or build_server. BuildServerID pins a
	// build server and RegistryID the registry; null inherits.
	BuildLocation string     `json:"buildLocation" enum:"inherit,target,build_server"`
	BuildServerID *uuid.UUID `json:"buildServerId"`
	RegistryID    *uuid.UUID `json:"registryId"`
}

func toProject(p db.Project) Project {
	return Project{ID: p.ID, Name: p.Name, Slug: p.Slug, Description: p.Description, System: p.System, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		BuildLocation: p.BuildLocation, BuildServerID: p.BuildServerID, RegistryID: p.RegistryID}
}

// errSystemProject refuses changes to the platform project: its services are managed
// from Team settings → Private network (Headscale).
var errSystemProject = apierr.Conflict("system_project", "This project holds NebulaCtrl's Headscale service and is managed from Team settings → Private network (Headscale).")

// Service implements the project use cases.
type Service struct {
	pool    *pgxpool.Pool
	q       *db.Queries
	bus     *events.Bus
	cascade CascadeDeps
}

// NewService returns a Service (pool may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus) *Service {
	return &Service{pool: pool, q: db.New(pool), bus: bus}
}

// IsUniqueViolation reports whether err is a Postgres unique-constraint error.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// List returns the projects visible to the caller, newest first.
func (s *Service) List(ctx context.Context, cursor string, limit int) ([]Project, string, error) {
	if err := policy.Require(ctx, policy.ScopeProjectsRead, nil); err != nil {
		return nil, "", err
	}
	p, _ := auth.PrincipalFrom(ctx)
	params := db.ListProjectsParams{WorkspaceID: p.WorkspaceID, Limit: int32(limit + 1), ProjectIds: p.ProjectIDs}
	if t, id, ok, err := DecodeCursor(cursor); err != nil {
		return nil, "", err
	} else if ok {
		params.CursorCreatedAt, params.CursorID = &t, &id
	}
	rows, err := s.q.ListProjects(ctx, params)
	if err != nil {
		return nil, "", fmt.Errorf("list projects: %w", err)
	}
	var next string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = EncodeCursor(last.CreatedAt, last.ID)
	}
	out := make([]Project, 0, len(rows))
	for _, r := range rows {
		out = append(out, toProject(r))
	}
	return out, next, nil
}

// Get returns one project.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Project, error) {
	row, err := s.load(ctx, s.q, policy.ScopeProjectsRead, id)
	if err != nil {
		return Project{}, err
	}
	return toProject(row), nil
}

// Create makes a project and its production environment in one transaction.
func (s *Service) Create(ctx context.Context, name, description string) (Project, error) {
	if err := policy.Require(ctx, policy.ScopeProjectsWrite, nil); err != nil {
		return Project{}, err
	}
	p, _ := auth.PrincipalFrom(ctx)
	if p.Restricted() {
		// A project-restricted token could never see what it creates.
		return Project{}, apierr.Forbidden()
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, apierr.Unprocessable("validation_failed", "name is required")
	}
	base := Slugify(name, "project")
	var row db.Project
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		taken, err := q.ListProjectSlugsLike(ctx, db.ListProjectSlugsLikeParams{WorkspaceID: p.WorkspaceID, Base: base})
		if err != nil {
			return err
		}
		row, err = q.CreateProject(ctx, db.CreateProjectParams{
			WorkspaceID: p.WorkspaceID, Name: name, Slug: UniqueSlug(base, taken), Description: description,
		})
		if err != nil {
			return err
		}
		env, err := q.CreateEnvironment(ctx, db.CreateEnvironmentParams{ProjectID: row.ID, Name: "production", Slug: "production", IsProduction: true})
		if err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{
			Action: "project.create", ResourceType: "project", ResourceID: row.ID.String(), ProjectID: &row.ID,
			Metadata: map[string]any{"name": row.Name, "slug": row.Slug},
		}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "environment.create", ResourceType: "environment", ResourceID: env.ID.String(), ProjectID: &row.ID,
			Metadata: map[string]any{"name": env.Name, "slug": env.Slug},
		})
	})
	if IsUniqueViolation(err) {
		return Project{}, apierr.Conflict("slug_conflict", "a project with that name was just created; retry")
	}
	if err != nil {
		return Project{}, fmt.Errorf("create project: %w", err)
	}
	s.publish(events.TypeProjectCreated, row.WorkspaceID, row.ID)
	return toProject(row), nil
}

// UpdateInput holds the optional fields of a project PATCH.
type UpdateInput struct {
	Name        *string
	Description *string

	// BuildLocation replaces the project's build placement as a unit: with it,
	// BuildServerID and RegistryID are set to the given values (nil clears them).
	// Without it both ids must be nil and the placement is unchanged.
	BuildLocation *string
	BuildServerID *uuid.UUID
	RegistryID    *uuid.UUID
}

// Update renames a project and/or changes its description. The slug is stable.
func (s *Service) Update(ctx context.Context, id uuid.UUID, in UpdateInput) (Project, error) {
	cur, err := s.load(ctx, s.q, policy.ScopeProjectsWrite, id)
	if err != nil {
		return Project{}, err
	}
	if cur.System {
		return Project{}, errSystemProject
	}
	name, desc := cur.Name, cur.Description
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
		if name == "" {
			return Project{}, apierr.Unprocessable("validation_failed", "name must not be empty")
		}
	}
	if in.Description != nil {
		desc = *in.Description
	}
	if in.BuildLocation == nil && (in.BuildServerID != nil || in.RegistryID != nil) {
		return Project{}, apierr.Unprocessable("validation_failed", "buildServerId and registryId are set together with buildLocation")
	}
	if in.BuildLocation != nil {
		if !buildplacement.ValidLocation(*in.BuildLocation, true) {
			return Project{}, apierr.Unprocessable("validation_failed", "buildLocation must be inherit, target or build_server")
		}
		if err := buildplacement.ValidateTargets(ctx, s.q, cur.WorkspaceID, in.BuildServerID, in.RegistryID); err != nil {
			return Project{}, err
		}
	}
	var row db.Project
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		q := db.New(tx)
		row, err = q.UpdateProject(ctx, db.UpdateProjectParams{ID: id, WorkspaceID: cur.WorkspaceID, Name: name, Description: desc})
		if err != nil {
			return err
		}
		meta := map[string]any{"name": name}
		if in.BuildLocation != nil {
			row, err = q.SetProjectBuildPlacement(ctx, db.SetProjectBuildPlacementParams{
				ID: id, WorkspaceID: cur.WorkspaceID, BuildLocation: *in.BuildLocation, BuildServerID: in.BuildServerID, RegistryID: in.RegistryID,
			})
			if err != nil {
				return err
			}
			meta["buildLocation"] = *in.BuildLocation
			if in.BuildServerID != nil {
				meta["buildServerId"] = in.BuildServerID.String()
			}
			if in.RegistryID != nil {
				meta["registryId"] = in.RegistryID.String()
			}
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "project.update", ResourceType: "project", ResourceID: id.String(), ProjectID: &id,
			Metadata: meta,
		})
	})
	if err != nil {
		return Project{}, fmt.Errorf("update project: %w", err)
	}
	s.publish(events.TypeProjectUpdated, cur.WorkspaceID, id)
	return toProject(row), nil
}

// DeleteInput selects what a project delete removes.
type DeleteInput struct {
	// Cascade deletes the project with everything in it. Without it only an
	// empty project can be deleted (409 project_not_empty).
	Cascade bool
	// Confirm is the project's name; it is required with Cascade.
	Confirm string
}

// Delete removes a project. Without in.Cascade the project must be empty and
// its environments go with it. With it, the project goes with everything in it
// (see DeleteCascade) once in.Confirm equals the project's name, and MCP
// principals need an approval when an environment is production.
func (s *Service) Delete(ctx context.Context, id uuid.UUID, in DeleteInput) error {
	cur, err := s.load(ctx, s.q, policy.ScopeProjectsWrite, id)
	if err != nil {
		return err
	}
	if cur.System {
		return errSystemProject
	}
	if in.Cascade {
		if in.Confirm != cur.Name {
			return apierr.Unprocessable("confirm_mismatch", "confirm must equal the project name").
				WithDetails(map[string]any{"name": cur.Name})
		}
		if err := s.cascadePreflight(ctx, cur.ID); err != nil {
			return err
		}
		if err := s.guardCascade(ctx, cur); err != nil {
			return err
		}
		// The approval covers the services, previews and domains it lists.
		_, err := s.deleteCascade(prodguard.Covered(ctx), cur)
		return err
	}
	if err := s.guardDelete(ctx, cur); err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		n, err := q.CountProjectLiveServices(ctx, id)
		if err != nil {
			return err
		}
		if n > 0 {
			return apierr.Conflict("project_not_empty", "delete the project's services first")
		}
		if _, err := q.DeleteProject(ctx, db.DeleteProjectParams{ID: id, WorkspaceID: cur.WorkspaceID}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "project.delete", ResourceType: "project", ResourceID: id.String(), ProjectID: &id,
			Metadata: map[string]any{"name": cur.Name, "slug": cur.Slug},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("delete project: %w", err)
	}
	s.publish(events.TypeProjectDeleted, cur.WorkspaceID, id)
	return nil
}

// load authorizes scope and returns the project, scoped to the caller's
// workspace and project restriction. Missing and forbidden projects are
// indistinguishable (404).
func (s *Service) load(ctx context.Context, q *db.Queries, scope string, id uuid.UUID) (db.Project, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.Project{}, err
	}
	if err := policy.Require(ctx, scope, &id); err != nil {
		return db.Project{}, err
	}
	row, err := q.GetProject(ctx, db.GetProjectParams{ID: id, WorkspaceID: policy.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Project{}, apierr.NotFound("")
	}
	if err != nil {
		return db.Project{}, fmt.Errorf("get project: %w", err)
	}
	if row.System && policy.Require(ctx, policy.ScopeAdmin, nil) != nil {
		return db.Project{}, apierr.NotFound("") // the platform project is for administrators
	}
	return row, nil
}

func (s *Service) publish(typ string, workspaceID, id uuid.UUID) {
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: typ, ProjectID: id.String(), ResourceID: id.String(), WorkspaceID: workspaceID.String()})
	}
}
