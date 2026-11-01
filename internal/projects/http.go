package projects

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type listProjectsInput struct {
	api.PageQuery
}

type listProjectsOutput struct{ Body api.Page[Project] }

type createProjectInput struct {
	Body struct {
		Name        string `json:"name" minLength:"1" maxLength:"100"`
		Description string `json:"description,omitempty" maxLength:"500"`
	}
}

type projectOutput struct{ Body Project }

type updateProjectInput struct {
	ID   uuid.UUID `path:"projectId"`
	Body struct {
		Name        *string `json:"name,omitempty" minLength:"1" maxLength:"100"`
		Description *string `json:"description,omitempty" maxLength:"500"`
		// The build placement is replaced as a unit: omitted ids are cleared.
		BuildLocation *string    `json:"buildLocation,omitempty" enum:"inherit,target,build_server" doc:"Where this project's git services are built: inherit (team default), target, or build_server. Sets buildServerId and registryId with it (omitted = cleared)."`
		BuildServerID *uuid.UUID `json:"buildServerId,omitempty" doc:"Pin the build server; omitted lets the control plane choose a sticky one. Only with buildLocation."`
		RegistryID    *uuid.UUID `json:"registryId,omitempty" doc:"Registry the builds are pushed to; omitted inherits the team default. Only with buildLocation."`
	}
}

type projectIDInput struct {
	ID uuid.UUID `path:"projectId"`
}

type deleteProjectInput struct {
	ID      uuid.UUID `path:"projectId"`
	Cascade bool      `query:"cascade" default:"false" doc:"Delete the project with everything in it. Irreversible."`
	Confirm string    `query:"confirm" doc:"The project's name, exactly. Required with cascade."`
}

type deletionPlanOutput struct{ Body DeletionPlan }

// Register attaches the project endpoints.
func Register(h huma.API, svc *Service) {
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listProjects", Method: http.MethodGet, Path: "/projects", Tags: []string{"projects"},
		Summary: "List projects", Errors: []int{http.StatusUnauthorized, http.StatusForbidden},
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *listProjectsInput) (*listProjectsOutput, error) {
		items, next, err := svc.List(ctx, in.Cursor, in.Size())
		if err != nil {
			return nil, err
		}
		return &listProjectsOutput{Body: api.Page[Project]{Items: items, NextCursor: next}}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createProject", Method: http.MethodPost, Path: "/projects", Tags: []string{"projects"},
		Summary: "Create a project (with its production environment)", DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
	}), policy.ScopeProjectsWrite), func(ctx context.Context, in *createProjectInput) (*projectOutput, error) {
		p, err := svc.Create(ctx, in.Body.Name, in.Body.Description)
		if err != nil {
			return nil, err
		}
		return &projectOutput{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getProject", Method: http.MethodGet, Path: "/projects/{projectId}", Tags: []string{"projects"},
		Summary: "Get a project", Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *projectIDInput) (*projectOutput, error) {
		p, err := svc.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &projectOutput{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateProject", Method: http.MethodPatch, Path: "/projects/{projectId}", Tags: []string{"projects"},
		Summary: "Update a project", Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *updateProjectInput) (*projectOutput, error) {
		p, err := svc.Update(ctx, in.ID, UpdateInput{
			Name: in.Body.Name, Description: in.Body.Description,
			BuildLocation: in.Body.BuildLocation, BuildServerID: in.Body.BuildServerID, RegistryID: in.Body.RegistryID,
		})
		if err != nil {
			return nil, err
		}
		return &projectOutput{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getProjectDeletionPlan", Method: http.MethodGet, Path: "/projects/{projectId}/deletion-plan", Tags: []string{"projects"},
		Summary: "Show what deleting a project removes",
		Description: "Counts what `DELETE /projects/{projectId}?cascade=true` removes: the environments, services, databases, volumes, domains, " +
			"previews, status page components and DNS records NebulaCtrl manages. It deletes nothing. Backups already written to backup destinations stay.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *projectIDInput) (*deletionPlanOutput, error) {
		plan, err := svc.Plan(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &deletionPlanOutput{Body: plan}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteProject", Method: http.MethodDelete, Path: "/projects/{projectId}", Tags: []string{"projects"},
		Summary: "Delete a project",
		Description: "Without parameters the project must be empty: it fails with 409 `project_not_empty` while a service is left. " +
			"With `cascade=true` the project goes with everything in it, irreversibly: its previews, every service of every environment with its volumes " +
			"(the agents remove the containers and volumes), the status page components that show those services, the DNS records NebulaCtrl manages " +
			"for their domains, the shared variables, the environments, then the project. `confirm` must equal the project's name (422 `confirm_mismatch`). " +
			"Backups already written to backup destinations stay. See `GET /projects/{projectId}/deletion-plan` for the counts. " +
			"MCP principals need an approval when the project has a production environment. A deployment in progress is refused with 409 `deployment_in_progress`, " +
			"and read replicas of a database with 409 `project_has_replicas`.",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *deleteProjectInput) (*struct{}, error) {
		return nil, svc.Delete(ctx, in.ID, DeleteInput{Cascade: in.Cascade, Confirm: in.Confirm})
	})
}
