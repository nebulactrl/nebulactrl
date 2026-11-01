package environments

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type listEnvironmentsInput struct {
	ProjectID uuid.UUID `path:"projectId"`
}

type listEnvironmentsOutput struct{ Body api.Page[Environment] }

type createEnvironmentInput struct {
	ProjectID uuid.UUID `path:"projectId"`
	Body      struct {
		Name         string `json:"name" minLength:"1" maxLength:"100"`
		IsProduction bool   `json:"isProduction,omitempty"`
	}
}

type environmentOutput struct{ Body Environment }

type updateEnvironmentInput struct {
	ID   uuid.UUID `path:"environmentId"`
	Body struct {
		Name         *string `json:"name,omitempty" minLength:"1" maxLength:"100"`
		IsProduction *bool   `json:"isProduction,omitempty"`
	}
}

type environmentIDInput struct {
	ID uuid.UUID `path:"environmentId"`
}

// Register attaches the environment endpoints.
func Register(h huma.API, svc *Service) {
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listEnvironments", Method: http.MethodGet, Path: "/projects/{projectId}/environments", Tags: []string{"environments"},
		Summary: "List a project's environments", Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *listEnvironmentsInput) (*listEnvironmentsOutput, error) {
		items, err := svc.List(ctx, in.ProjectID)
		if err != nil {
			return nil, err
		}
		return &listEnvironmentsOutput{Body: api.Page[Environment]{Items: items}}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createEnvironment", Method: http.MethodPost, Path: "/projects/{projectId}/environments", Tags: []string{"environments"},
		Summary: "Create an environment", DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}), policy.ScopeProjectsWrite), func(ctx context.Context, in *createEnvironmentInput) (*environmentOutput, error) {
		e, err := svc.Create(ctx, in.ProjectID, CreateInput{Name: in.Body.Name, IsProduction: in.Body.IsProduction})
		if err != nil {
			return nil, err
		}
		return &environmentOutput{Body: e}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateEnvironment", Method: http.MethodPatch, Path: "/environments/{environmentId}", Tags: []string{"environments"},
		Summary: "Update an environment", Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *updateEnvironmentInput) (*environmentOutput, error) {
		e, err := svc.Update(ctx, in.ID, UpdateInput{Name: in.Body.Name, IsProduction: in.Body.IsProduction})
		if err != nil {
			return nil, err
		}
		return &environmentOutput{Body: e}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteEnvironment", Method: http.MethodDelete, Path: "/environments/{environmentId}", Tags: []string{"environments"},
		Summary: "Delete an empty environment", DefaultStatus: http.StatusNoContent,
		Description: "Fails with 409 `environment_not_empty` while the environment still has services.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *environmentIDInput) (*struct{}, error) {
		return nil, svc.Delete(ctx, in.ID)
	})
}
