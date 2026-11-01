package deployhooks

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// ServicePath is the service path parameter of the hook endpoints.
type ServicePath struct {
	ID uuid.UUID `path:"serviceId"`
}

type viewOutput struct{ Body DeployHookView }

// DeployHookSetting is the body of PUT /services/{serviceId}/deploy-hook.
type DeployHookSetting struct {
	Enabled bool `json:"enabled" doc:"true turns the hook on (new secret, URL returned once); false turns it off and deletes the secret"`
}

type putInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body DeployHookSetting
}

// Register attaches the deploy hook management endpoints.
func Register(h huma.API, s *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getDeployHook", Method: http.MethodGet, Path: "/services/{serviceId}/deploy-hook", Tags: []string{"services", "deploy-hooks"},
		Summary: "Get a service's deploy hook",
		Description: "Whether the hook is on, when its secret was created, and what the last call did. The secret and the URL are never returned here; " +
			"`urlPattern` shows the URL with a placeholder.",
		Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *ServicePath) (*viewOutput, error) {
		v, err := s.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "setDeployHook", Method: http.MethodPut, Path: "/services/{serviceId}/deploy-hook", Tags: []string{"services", "deploy-hooks"},
		Summary: "Turn a service's deploy hook on or off",
		Description: "Turning the hook on creates a secret (at least 256 bits, stored only as a hash) and returns the complete `url` once; " +
			"`POST /hooks/deploy/<serviceId>/<secret>` then deploys the service like a manual deploy, with trigger `deploy_hook`. " +
			"Turning it off deletes the secret, so enabling again gives a new URL. Needs `deployments:write`.",
		Errors: append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *putInput) (*viewOutput, error) {
		v, err := s.SetEnabled(ctx, in.ID, in.Body.Enabled)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "rotateDeployHook", Method: http.MethodPost, Path: "/services/{serviceId}/deploy-hook/rotate", Tags: []string{"services", "deploy-hooks"},
		Summary: "Rotate a service's deploy hook secret",
		Description: "Replaces the secret of an enabled hook; the old URL stops working at once. The new `url` is returned once. " +
			"409 `deploy_hook_disabled` when the hook is off. Needs `deployments:write`.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *ServicePath) (*viewOutput, error) {
		v, err := s.Rotate(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})
}
