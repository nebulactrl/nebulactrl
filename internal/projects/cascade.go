package projects

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// CascadeDeps are the delete paths a project delete reuses. They live in
// packages that import this one, so they are plugged in at wiring time
// (Service.SetCascade) instead of imported.
type CascadeDeps struct {
	// DeleteService deletes one service the way the service delete dialog does
	// with "remove volumes" ticked: the service is soft-deleted, its domains go
	// (and the DNS hook schedules the removal of the records NebulaCtrl manages
	// for them) and the removal of its container and volumes is queued on its
	// server. slug is the service's slug, the confirmation that call asks for.
	DeleteService func(ctx context.Context, id uuid.UUID, slug string) error
	// TeardownPreview removes a pull-request preview with its services,
	// volumes, domains and environment, and returns once it is gone.
	TeardownPreview func(ctx context.Context, id uuid.UUID) error
	// DetachStatusPages removes, inside tx, the status page components that
	// show the project's services and returns how many went; the function it
	// returns announces the change and runs after the commit.
	DetachStatusPages func(ctx context.Context, tx pgx.Tx, projectID uuid.UUID) (int, func(), error)
}

// SetCascade plugs in the delete paths a project delete with cascade reuses
// (wiring only; call before serving).
func (s *Service) SetCascade(d CascadeDeps) { s.cascade = d }

// DeletionPlan is what deleting a project with everything in it removes.
type DeletionPlan struct {
	Project      PlanProject       `json:"project"`
	Environments []PlanEnvironment `json:"environments" doc:"The project's own environments. Pull-request previews are counted in previews."`
	// Counts cover the whole project, previews included.
	Services             int `json:"services" doc:"Services other than databases."`
	Databases            int `json:"databases"`
	Volumes              int `json:"volumes"`
	Domains              int `json:"domains"`
	Previews             int `json:"previews"`
	StatusPageComponents int `json:"statusPageComponents" doc:"Components of status pages that show the project's services. They are removed from their pages; the pages stay."`
	ManagedDNSRecords    int `json:"managedDnsRecords" doc:"DNS records NebulaCtrl manages for the project's domains. They are removed from the DNS zone."`
	// BackupDestinationsKept says that backups already written to a backup
	// destination stay there; only the project's backup history rows go.
	BackupDestinationsKept bool `json:"backupDestinationsKept"`
}

// PlanProject identifies the project of a DeletionPlan.
type PlanProject struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Slug string    `json:"slug"`
}

// PlanEnvironment is one environment of a DeletionPlan.
type PlanEnvironment struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	Production bool      `json:"production"`
}

// Plan reports what DeleteCascade would remove. It deletes nothing.
func (s *Service) Plan(ctx context.Context, id uuid.UUID) (DeletionPlan, error) {
	cur, err := s.load(ctx, s.q, policy.ScopeProjectsRead, id)
	if err != nil {
		return DeletionPlan{}, err
	}
	return s.plan(ctx, s.q, cur)
}

func (s *Service) plan(ctx context.Context, q *db.Queries, p db.Project) (DeletionPlan, error) {
	envs, err := q.ListEnvironments(ctx, p.ID)
	if err != nil {
		return DeletionPlan{}, fmt.Errorf("list environments: %w", err)
	}
	c, err := q.GetProjectDeletionCounts(ctx, p.ID)
	if err != nil {
		return DeletionPlan{}, fmt.Errorf("count project contents: %w", err)
	}
	out := DeletionPlan{
		Project:      PlanProject{ID: p.ID, Name: p.Name, Slug: p.Slug},
		Environments: make([]PlanEnvironment, 0, len(envs)),
		Services:     int(c.Services), Databases: int(c.Databases), Volumes: int(c.Volumes), Domains: int(c.Domains),
		Previews: int(c.Previews), StatusPageComponents: int(c.StatusPageComponents), ManagedDNSRecords: int(c.ManagedDnsRecords),
		BackupDestinationsKept: true,
	}
	for _, e := range envs {
		if e.Kind != "preview" {
			out.Environments = append(out.Environments, PlanEnvironment{ID: e.ID, Name: e.Name, Production: e.IsProduction})
		}
	}
	return out, nil
}

// DeleteCascade deletes a project with everything in it: its previews, every
// service of every environment (volumes included; the agents are told to remove
// containers and volumes), the status page components that show those services,
// the DNS records NebulaCtrl manages for their domains, the shared variables,
// then the environments and the project. Backups already written to backup
// destinations stay there.
//
// It checks the caller's projects:write scope and refuses the platform project.
// It asks no production approval and no confirmation: Delete does both for the
// API. A caller that is guarded for production changes is asked for an approval
// by every service it deletes there, unless the context is prodguard.Covered.
//
// Each service delete is its own transaction (the services' delete path is
// reused as it is), the rest is one: a delete that fails in between leaves the
// project with fewer services and can be repeated. It returns the counts the
// project held before the delete.
func (s *Service) DeleteCascade(ctx context.Context, id uuid.UUID) (DeletionPlan, error) {
	cur, err := s.load(ctx, s.q, policy.ScopeProjectsWrite, id)
	if err != nil {
		return DeletionPlan{}, err
	}
	if cur.System {
		return DeletionPlan{}, errSystemProject
	}
	return s.deleteCascade(ctx, cur)
}

func (s *Service) deleteCascade(ctx context.Context, cur db.Project) (DeletionPlan, error) {
	if s.cascade.DeleteService == nil || s.cascade.TeardownPreview == nil || s.cascade.DetachStatusPages == nil {
		return DeletionPlan{}, errors.New("project cascade delete is not configured")
	}
	if err := s.cascadePreflight(ctx, cur.ID); err != nil {
		return DeletionPlan{}, err
	}
	plan, err := s.plan(ctx, s.q, cur)
	if err != nil {
		return DeletionPlan{}, err
	}
	envs, err := s.q.ListEnvironments(ctx, cur.ID)
	if err != nil {
		return DeletionPlan{}, fmt.Errorf("list environments: %w", err)
	}

	// Previews first: they can ask to wait for a running deployment, and the
	// project's own environments are what they were cloned from.
	previews, err := s.q.ListProjectPreviews(ctx, cur.ID)
	if err != nil {
		return DeletionPlan{}, fmt.Errorf("list previews: %w", err)
	}
	for _, pv := range previews {
		if err := s.cascade.TeardownPreview(ctx, pv.ID); err != nil {
			return DeletionPlan{}, cascadeErr("delete preview", err)
		}
	}
	svcs, err := s.q.ListProjectStandardServices(ctx, cur.ID)
	if err != nil {
		return DeletionPlan{}, fmt.Errorf("list services: %w", err)
	}
	for _, sv := range svcs {
		if err := s.cascade.DeleteService(ctx, sv.ID, sv.Slug); err != nil {
			if ae, ok := apierr.As(err); ok && ae.Status == 404 {
				continue // gone meanwhile
			}
			return DeletionPlan{}, cascadeErr("delete service "+sv.Slug, err)
		}
	}

	var announce func()
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if n, err := q.CountProjectLiveServices(ctx, cur.ID); err != nil {
			return err
		} else if n > 0 {
			return apierr.Conflict("project_not_empty", "a service was added while the project was being deleted; delete the project again")
		}
		_, announceStatus, err := s.cascade.DetachStatusPages(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		announce = announceStatus
		if _, err := q.DeleteProjectSharedVariables(ctx, cur.ID); err != nil {
			return err
		}
		for _, e := range envs {
			if e.Kind == "preview" {
				continue
			}
			if _, err := q.DeleteEnvironment(ctx, e.ID); err != nil {
				return err
			}
		}
		if _, err := q.DeleteProject(ctx, db.DeleteProjectParams{ID: cur.ID, WorkspaceID: cur.WorkspaceID}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "project.deleted", ResourceType: "project", ResourceID: cur.ID.String(), ProjectID: &cur.ID,
			Metadata: map[string]any{
				"name": cur.Name, "slug": cur.Slug, "cascade": true,
				"environments": len(plan.Environments), "services": plan.Services, "databases": plan.Databases,
				"volumes": plan.Volumes, "domains": plan.Domains, "previews": plan.Previews,
				"statusPageComponents": plan.StatusPageComponents, "managedDnsRecords": plan.ManagedDNSRecords,
			},
		})
	})
	if err != nil {
		return DeletionPlan{}, cascadeErr("delete project", err)
	}
	announce()
	for _, e := range envs {
		if e.Kind != "preview" && s.bus != nil {
			s.bus.Publish(events.Event{Type: events.TypeEnvironmentDeleted, ProjectID: cur.ID.String(), EnvironmentID: e.ID.String(), ResourceID: e.ID.String(), WorkspaceID: cur.WorkspaceID.String()})
		}
	}
	s.publish(events.TypeProjectDeleted, cur.WorkspaceID, cur.ID)
	return plan, nil
}

// cascadePreflight refuses, before anything is deleted, what the service
// delete would refuse halfway: a running deployment, and read replicas, which
// have their own removal (Remove replica) that drops the replication slot.
func (s *Service) cascadePreflight(ctx context.Context, projectID uuid.UUID) error {
	if n, err := s.q.CountProjectActiveDeployments(ctx, projectID); err != nil {
		return fmt.Errorf("count deployments: %w", err)
	} else if n > 0 {
		return apierr.Conflict("deployment_in_progress", "a deployment is in progress; wait for it to finish or cancel it")
	}
	if n, err := s.q.CountProjectDatabaseReplicas(ctx, projectID); err != nil {
		return fmt.Errorf("count replicas: %w", err)
	} else if n > 0 {
		return apierr.Conflict("project_has_replicas", "remove the read replicas of the project's databases first")
	}
	return nil
}

// cascadeErr passes API errors through and wraps the rest.
func cascadeErr(op string, err error) error {
	if _, ok := apierr.As(err); ok {
		return err
	}
	return fmt.Errorf("%s: %w", op, err)
}
