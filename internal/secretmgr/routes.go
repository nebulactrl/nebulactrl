package secretmgr

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type idInput struct {
	ID uuid.UUID `path:"id"`
}

type integrationOutput struct{ Body SecretManager }

type listOutput struct {
	Body struct {
		Items []SecretManager `json:"items"`
	}
}

// SecretManagerBody is the request form of an integration.
type SecretManagerBody struct {
	SecretManagerConfig
	Secret string       `json:"secret,omitempty" maxLength:"4096" doc:"Write-only: the Vault token (token auth), the AppRole secret id, the Infisical client secret or the Doppler token. Omit on update to keep the stored one."`
	Scope  string       `json:"scope,omitempty" enum:"team,selected" doc:"team (default on create): every project may reference the integration. selected: only the projects and environments in grants. Omit on update to keep the stored scope."`
	Grants []ScopeGrant `json:"grants,omitempty" maxItems:"200" doc:"With scope selected: the projects (or single environments) allowed to reference the integration. At least one."`
}

type createInput struct {
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"63" doc:"Lowercase letters, digits and dashes; it appears in references as vault:<name>/… or doppler:<name>/…"`
		Kind string `json:"kind" enum:"vault,infisical,doppler" doc:"vault covers HashiCorp Vault and OpenBao (KV v2); doppler is Doppler (service token, or a personal / service account token with a project and config)"`
		SecretManagerBody
	}
}

type updateInput struct {
	ID   uuid.UUID `path:"id"`
	Body SecretManagerBody
}

type testInput struct {
	ID   uuid.UUID `path:"id"`
	Body struct {
		Path string `json:"path,omitempty" maxLength:"512" doc:"Optional secret path to read (without the integration name); for Doppler the project and config, like backend/prd, or empty for the integration's defaults"`
		Key  string `json:"key,omitempty" maxLength:"255" doc:"Optional field or secret name to read"`
	}
}

type testOutput struct{ Body SecretManagerTestResult }

// Register attaches the secret manager endpoints.
func Register(h huma.API, svc *Service) {
	tags := []string{"secret-managers"}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden}
	errsNF := append(append([]int{}, errs...), http.StatusNotFound)
	errs422 := append(append([]int{}, errsNF...), http.StatusUnprocessableEntity)

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listSecretManagers", Method: http.MethodGet, Path: "/secret-managers", Tags: tags,
		Summary: "List the team's external secret manager integrations",
		Description: "Variables reference a secret as `${{vault:<name>/<path>#<key>}}` or, for Doppler, `${{doppler:<name>/<project>/<config>#<key>}}`. The value is fetched when a deploy sends its command and is never stored. " +
			"Credentials are never returned (`secretSet` only). `scope` and `grants` say which projects and environments may reference the integration.",
		Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, _ *struct{}) (*listOutput, error) {
		items, err := svc.List(ctx)
		if err != nil {
			return nil, err
		}
		out := &listOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "createSecretManager", Method: http.MethodPost, Path: "/secret-managers", Tags: tags,
		Summary: "Add a secret manager integration", DefaultStatus: http.StatusCreated,
		Description: "HashiCorp Vault / OpenBao: KV v2 with a token or AppRole. Infisical: a machine identity (universal auth). Doppler: a service token (bound to one config) or a personal / service account token. Nothing contacts the manager until the test or the first deploy that references it. Admin scope.",
		Errors:      append(errs422, http.StatusConflict),
	}, policy.ScopeAdmin), func(ctx context.Context, in *createInput) (*integrationOutput, error) {
		v, err := svc.Create(ctx, Input{Name: in.Body.Name, Kind: in.Body.Kind, Config: in.Body.SecretManagerConfig, Secret: in.Body.Secret, Scope: in.Body.Scope, Grants: in.Body.Grants})
		if err != nil {
			return nil, err
		}
		return &integrationOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateSecretManager", Method: http.MethodPut, Path: "/secret-managers/{id}", Tags: tags,
		Summary:     "Update a secret manager integration",
		Description: "The name and kind are fixed. Changing the address, auth method or identity requires the credential again. Omitting `scope` keeps the stored scope and grants. Admin scope.",
		Errors:      errs422,
	}, policy.ScopeAdmin), func(ctx context.Context, in *updateInput) (*integrationOutput, error) {
		v, err := svc.Update(ctx, in.ID, Input{Config: in.Body.SecretManagerConfig, Secret: in.Body.Secret, Scope: in.Body.Scope, Grants: in.Body.Grants})
		if err != nil {
			return nil, err
		}
		return &integrationOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteSecretManager", Method: http.MethodDelete, Path: "/secret-managers/{id}", Tags: tags,
		Summary: "Remove a secret manager integration", DefaultStatus: http.StatusNoContent,
		Description: "Refused with 409 `integration_in_use` while a variable references it.",
		Errors:      append(errsNF, http.StatusConflict),
	}, policy.ScopeAdmin), func(ctx context.Context, in *idInput) (*struct{}, error) {
		return nil, svc.Delete(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "testSecretManager", Method: http.MethodPost, Path: "/secret-managers/{id}/test", Tags: tags,
		Summary:     "Test a secret manager integration",
		Description: "Signs in and, when `path` and `key` are given, reads that secret. The value is discarded and never returned.",
		Errors:      errsNF,
	}, policy.ScopeAdmin), func(ctx context.Context, in *testInput) (*testOutput, error) {
		r, err := svc.Test(ctx, in.ID, TestInput{Path: in.Body.Path, Key: in.Body.Key})
		if err != nil {
			return nil, err
		}
		return &testOutput{Body: r}, nil
	})
}
