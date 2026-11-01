package registries

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type idInput struct {
	ID uuid.UUID `path:"registryId"`
}

type viewOutput struct{ Body RegistryView }

type listOutput struct {
	Body struct {
		Items []RegistryView `json:"items"`
	}
}

type createInput struct {
	Body struct {
		Name         string `json:"name" minLength:"1" maxLength:"80" doc:"Unique name in the team."`
		Host         string `json:"host" minLength:"1" maxLength:"253" doc:"registry.example.com or registry.example.com:5000, without a scheme or path. ghcr.io, docker.io, registry.gitlab.com, a Harbor or self-hosted host."`
		Namespace    string `json:"namespace,omitempty" maxLength:"200" doc:"The path between the host and the repository name: a GitHub user or organization, a Docker Hub namespace, a GitLab group/project. Images are pushed to <host>/<namespace>/<project>-<service>."`
		PushUsername string `json:"pushUsername" minLength:"1" maxLength:"4096" doc:"Write-only credential used to push images (basic auth). A registry without authentication takes any pair."`
		PushPassword string `json:"pushPassword" minLength:"1" maxLength:"4096" doc:"Write-only: a token or password, never returned."`
		PullUsername string `json:"pullUsername,omitempty" maxLength:"4096" doc:"Optional read-only credential the target servers pull with. Without it they use the push credential."`
		PullPassword string `json:"pullPassword,omitempty" maxLength:"4096" doc:"Write-only, never returned."`
	}
}

type patchInput struct {
	ID   uuid.UUID `path:"registryId"`
	Body struct {
		Name            *string `json:"name,omitempty" minLength:"1" maxLength:"80"`
		Host            *string `json:"host,omitempty" maxLength:"253"`
		Namespace       *string `json:"namespace,omitempty" maxLength:"200"`
		PushUsername    *string `json:"pushUsername,omitempty" maxLength:"4096"`
		PushPassword    *string `json:"pushPassword,omitempty" maxLength:"4096" doc:"Omit to keep the stored password, except when the host changes: the stored credentials would go to the new host, so send it again (422 secret_required)."`
		PullUsername    *string `json:"pullUsername,omitempty" maxLength:"4096"`
		PullPassword    *string `json:"pullPassword,omitempty" maxLength:"4096" doc:"Omit to keep the stored password, except when the host changes and a read-only credential exists: send it again (or clearPull)."`
		ClearPull       bool    `json:"clearPull,omitempty" doc:"Remove the read-only credential."`
		ManageRetention *bool   `json:"manageRetention,omitempty" doc:"Let NebulaCtrl delete images that fall out of the rollback window. External registries only; the built-in registry always does."`
	}
}

type checkInput struct {
	ID       uuid.UUID `path:"registryId"`
	ServerID uuid.UUID `query:"serverId" doc:"The server whose agent runs the check. Builds and pulls reach the registry from servers, so this is the test that matters."`
}

type checkOutput struct{ Body RegistryCheck }

type imagesInput struct {
	ID     uuid.UUID `path:"registryId"`
	State  string    `query:"state" enum:"live,expired,deleted" doc:"Only images in this state."`
	Limit  int       `query:"limit" minimum:"0" maximum:"200" doc:"Page size (default 50)."`
	Cursor string    `query:"cursor"`
}

type imagesOutput struct{ Body RegistryImagePage }

// Register attaches the registry endpoints.
func Register(h huma.API, svc *Service) {
	tags := []string{"registries"}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listRegistries", Method: http.MethodGet, Path: "/registries", Tags: tags,
		Summary: "List registries", Errors: errs,
		Description: "Bring-your-own (external) registries and the team's built-in registry slice. Credentials are write-only and never returned.",
	}, policy.ScopeProjectsRead), func(ctx context.Context, _ *struct{}) (*listOutput, error) {
		items, err := svc.List(ctx)
		if err != nil {
			return nil, err
		}
		out := &listOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createRegistry", Method: http.MethodPost, Path: "/registries", Tags: tags,
		Summary: "Add an external registry", DefaultStatus: http.StatusCreated,
		Description: "Adds a registry that takes basic auth (GHCR, Docker Hub, GitLab, Harbor, self-hosted). " +
			"Credentials are sealed and write-only. ECR, GAR and ACR tokens that expire are not supported. " +
			"The built-in registry is created by NebulaCtrl for every team. Requires the admin scope.",
		Errors: append(errs[:2:2], http.StatusConflict, http.StatusUnprocessableEntity),
	}), policy.ScopeAdmin), func(ctx context.Context, in *createInput) (*viewOutput, error) {
		v, err := svc.Create(ctx, CreateInput(in.Body))
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getRegistry", Method: http.MethodGet, Path: "/registries/{registryId}", Tags: tags,
		Summary: "Get a registry", Errors: append(errs[:2:2], http.StatusNotFound),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *idInput) (*viewOutput, error) {
		v, err := svc.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateRegistry", Method: http.MethodPatch, Path: "/registries/{registryId}", Tags: tags,
		Summary: "Update a registry", Errors: append(errs[:2:2], http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity),
		Description: "Omitted fields stay as they are; an omitted password keeps the stored one. The built-in registry is read-only (409 `builtin_registry_readonly`).",
	}, policy.ScopeAdmin), func(ctx context.Context, in *patchInput) (*viewOutput, error) {
		v, err := svc.Update(ctx, in.ID, UpdateInput(in.Body))
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteRegistry", Method: http.MethodDelete, Path: "/registries/{registryId}", Tags: tags,
		Summary: "Delete an external registry", DefaultStatus: http.StatusNoContent,
		Description: "409 `registry_in_use` while a team default, a project or a service override uses it. " +
			"The built-in registry cannot be removed (409 `builtin_registry`). Images already pushed stay in the registry.",
		Errors: append(errs[:2:2], http.StatusNotFound, http.StatusConflict),
	}, policy.ScopeAdmin), func(ctx context.Context, in *idInput) (*struct{}, error) {
		return nil, svc.Delete(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "checkRegistry", Method: http.MethodPost, Path: "/registries/{registryId}/check", Tags: tags,
		Summary: "Test a registry from a server",
		Description: "The agent on the chosen server contacts the registry with the stored credentials and reports whether push and pull are accepted. " +
			"Waits up to 25 seconds; if the agent has not answered by then the response has `pending: true` and the result appears in the registry's `lastCheck`.",
		Errors: append(errs[:2:2], http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *checkInput) (*checkOutput, error) {
		res, err := svc.Check(ctx, in.ID, in.ServerID)
		if err != nil {
			return nil, err
		}
		return &checkOutput{Body: res}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listRegistryImages", Method: http.MethodGet, Path: "/registries/{registryId}/images", Tags: tags,
		Summary:     "List the images pushed to a registry",
		Description: "Newest first. `live` images are inside the rollback window, `expired` ones are outside it (the built-in registry deletes them at the next sweep), `deleted` ones are gone.",
		Errors:      append(errs[:2:2], http.StatusNotFound, http.StatusBadRequest, http.StatusUnprocessableEntity),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *imagesInput) (*imagesOutput, error) {
		p, err := svc.ListImages(ctx, in.ID, in.State, in.Cursor, in.Limit)
		if err != nil {
			return nil, err
		}
		return &imagesOutput{Body: p}, nil
	})
}
