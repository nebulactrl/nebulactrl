package tokens

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type listTokensOutput struct {
	Body struct {
		Items []Token `json:"items"`
	}
}

// CreatedToken is the create response: the token plus its one-time secret.
type CreatedToken struct {
	Token
	Secret string `json:"token" doc:"The raw API token. Shown once; store it now."`
}

type createTokenInput struct {
	Body struct {
		Name                           string      `json:"name" minLength:"1" maxLength:"100"`
		Kind                           string      `json:"kind,omitempty" enum:"mcp,rest" default:"rest" doc:"mcp: for an AI agent; works only on /mcp and is refused on /api/v1. rest: for the REST API, the CLI and CI; works only on /api/v1 and is refused on /mcp."`
		Scopes                         []string    `json:"scopes" minItems:"1" doc:"projects:read, logs:read, projects:write, deployments:write, deployments:rollback, servers:read, servers:write, proxy:write, admin"`
		ProjectIDs                     []uuid.UUID `json:"projectIds,omitempty" doc:"Restrict the token to these projects; omit for all projects"`
		ExpiresAt                      *time.Time  `json:"expiresAt,omitempty"`
		AllowProductionWithoutApproval bool        `json:"allowProductionWithoutApproval,omitempty" doc:"Calls with this token skip the human approval production changes otherwise need. Only the instance admin or a team Owner or Admin signed in on the dashboard can set it; a token cannot. Audited. Off by default."`
	}
}

type createTokenOutput struct{ Body CreatedToken }

type revokeTokenInput struct {
	ID uuid.UUID `path:"id"`
}

// Register attaches the token endpoints.
func Register(h huma.API, svc *Service) {
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listTokens", Method: http.MethodGet, Path: "/tokens", Tags: []string{"tokens"},
		Summary: "List API tokens", Errors: []int{http.StatusUnauthorized, http.StatusForbidden},
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*listTokensOutput, error) {
		items, err := svc.List(ctx)
		if err != nil {
			return nil, err
		}
		out := &listTokensOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "createToken", Method: http.MethodPost, Path: "/tokens", Tags: []string{"tokens"},
		Summary: "Create an API token", DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity},
	}, policy.ScopeAdmin), func(ctx context.Context, in *createTokenInput) (*createTokenOutput, error) {
		t, secret, err := svc.Create(ctx, CreateInput{
			Name: in.Body.Name, Scopes: in.Body.Scopes, ProjectIDs: in.Body.ProjectIDs, ExpiresAt: in.Body.ExpiresAt,
			Kind: in.Body.Kind, AllowProductionWithoutApproval: in.Body.AllowProductionWithoutApproval,
		})
		if err != nil {
			return nil, err
		}
		return &createTokenOutput{Body: CreatedToken{Token: t, Secret: secret}}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "revokeToken", Method: http.MethodDelete, Path: "/tokens/{id}", Tags: []string{"tokens"},
		Summary: "Revoke an API token", DefaultStatus: http.StatusNoContent,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, policy.ScopeAdmin), func(ctx context.Context, in *revokeTokenInput) (*struct{}, error) {
		return nil, svc.Revoke(ctx, in.ID)
	})
}
