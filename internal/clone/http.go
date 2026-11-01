package clone

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type cloneServiceInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		EnvironmentID *uuid.UUID `json:"environmentId,omitempty" doc:"Environment of the clone (default: the source's environment)"`
		Name          string     `json:"name,omitempty" maxLength:"100" doc:"Name of the clone (default: the source's name plus ' copy' in the same environment)"`
		ServerID      *uuid.UUID `json:"serverId,omitempty" doc:"Server of the clone (default: the source's server)"`
		Domains       string     `json:"domains,omitempty" enum:"drop,suffix" doc:"drop (default) clones no domain; suffix clones each public domain with domainSuffix added to its first label"`
		DomainSuffix  string     `json:"domainSuffix,omitempty" maxLength:"40" doc:"Default: -copy in the same environment, -<environment slug> in another one"`
		DryRun        bool       `json:"dryRun,omitempty" doc:"Report what the clone would do and change nothing"`
	}
}

type cloneEnvironmentInput struct {
	ID   uuid.UUID `path:"environmentId"`
	Body struct {
		Name         string     `json:"name" minLength:"1" maxLength:"100"`
		ServerID     *uuid.UUID `json:"serverId,omitempty" doc:"Place every cloned service on this server (default: each service keeps its server)"`
		Domains      string     `json:"domains,omitempty" enum:"drop,suffix" doc:"drop (default) clones no domain; suffix clones each public domain with domainSuffix added to its first label"`
		DomainSuffix string     `json:"domainSuffix,omitempty" maxLength:"40" doc:"Default: -<new environment slug>"`
		DryRun       bool       `json:"dryRun,omitempty" doc:"Report what the clone would do and change nothing"`
	}
}

type moveServiceInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		EnvironmentID uuid.UUID `json:"environmentId" doc:"The environment to move the service to"`
		Force         bool      `json:"force,omitempty" doc:"Move even though references from or to the service break"`
		DryRun        bool      `json:"dryRun,omitempty" doc:"Report the plan (references, links, slug) and change nothing"`
	}
}

type cloneOutput struct{ Body CloneResult }

type moveOutput struct{ Body MoveResult }

// Register attaches the clone and move endpoints.
func Register(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity}

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "cloneService", Method: http.MethodPost, Path: "/services/{serviceId}/clone", Tags: []string{"services"},
		Summary: "Clone a service",
		Description: "Creates a staged copy of a service in the same or another environment of the team; nothing runs until it is deployed. " +
			"The clone gets the source's configuration (Docker options included), variables, volume definitions and, in the same environment, its outgoing links. " +
			"Secrets are copied sealed and generated values are generated again. Volumes are new and empty (the data is not copied). " +
			"A managed database is created new and empty with new credentials: restore a dump into it. " +
			"Domains are dropped (default) or cloned with a suffix; 409 `domain_conflict` when a suffixed hostname is taken. " +
			"Needs write access to the source and the target environment. Route services, Compose stacks and read replicas are not cloned (422 `not_applicable`). " +
			"`dryRun` returns the plan and changes nothing; the response lists variable references that do not resolve in the target environment.",
		DefaultStatus: http.StatusOK, Errors: errs,
	}), policy.ScopeProjectsWrite), func(ctx context.Context, in *cloneServiceInput) (*cloneOutput, error) {
		res, err := svc.CloneService(ctx, in.ID, ServiceInput{
			EnvironmentID: in.Body.EnvironmentID, Name: in.Body.Name, ServerID: in.Body.ServerID,
			Domains: in.Body.Domains, DomainSuffix: in.Body.DomainSuffix, DryRun: in.Body.DryRun,
		})
		if err != nil {
			return nil, err
		}
		return &cloneOutput{Body: res}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "cloneEnvironment", Method: http.MethodPost, Path: "/environments/{environmentId}/clone", Tags: []string{"environments"},
		Summary: "Clone an environment",
		Description: "Creates a new, non-production environment in the same project with a staged copy of every service of the source: configuration, variables, " +
			"the environment's shared variables, volume definitions, links, repository links and canvas layout. Slugs are kept, so references such as `${{postgres.DATABASE_URL}}` point at the cloned siblings. " +
			"Secrets are copied sealed and generated values are generated again. Databases are created new and empty: restore a dump into them. Volumes are new and empty. " +
			"Domains are dropped (default) or cloned with a suffix; 409 `domain_conflict` when a suffixed hostname is taken. " +
			"Route services and Compose stacks are skipped and listed. Nothing runs until the services are deployed. `dryRun` returns the plan and changes nothing.",
		DefaultStatus: http.StatusOK, Errors: errs,
	}), policy.ScopeProjectsWrite), func(ctx context.Context, in *cloneEnvironmentInput) (*cloneOutput, error) {
		res, err := svc.CloneEnvironment(ctx, in.ID, EnvironmentInput{
			Name: in.Body.Name, ServerID: in.Body.ServerID, Domains: in.Body.Domains, DomainSuffix: in.Body.DomainSuffix, DryRun: in.Body.DryRun,
		})
		if err != nil {
			return nil, err
		}
		return &cloneOutput{Body: res}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "moveService", Method: http.MethodPost, Path: "/services/{serviceId}/move", Tags: []string{"services"},
		Summary: "Move a service to another environment",
		Description: "Stages a move of the service to another environment (of any project of the team): it keeps running where it is until its next deployment, " +
			"which joins the new environment's network and resolves its variables there. Domains, volumes and data stay with the service. " +
			"References are checked first: variables of the service that point at services staying behind, and variables of other services that point at the moved one, " +
			"break. The move is refused with 409 `move_blocked` (details.plan) unless `force` is set; links on the canvas are removed, " +
			"and the services that referenced the moved one are marked as having staged changes. " +
			"A service in a replica group or with a cross-server link cannot be moved (409 `move_blocked`); route services and Compose stacks are not movable (422 `not_applicable`). " +
			"`dryRun` returns the plan and changes nothing. Moving a service out of or into a production environment needs approval for guarded clients.",
		Errors: errs,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *moveServiceInput) (*moveOutput, error) {
		res, err := svc.MoveService(ctx, in.ID, MoveInput{EnvironmentID: in.Body.EnvironmentID, Force: in.Body.Force, DryRun: in.Body.DryRun})
		if err != nil {
			return nil, err
		}
		return &moveOutput{Body: res}, nil
	})
}
