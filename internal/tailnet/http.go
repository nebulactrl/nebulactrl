package tailnet

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type reprovisionInput struct {
	ID uuid.UUID `path:"serviceId"`
}

type reprovisionOutput struct{ Body ReprovisionResult }

// Register attaches the per-service tailnet endpoints. Enabling and configuring
// the tailnet is a normal service PATCH (config.tailnet); this adds the one
// action that is not a setting.
func Register(h huma.API, svc *Service) {
	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "reprovisionServiceTailnet", Method: http.MethodPost, Path: "/services/{serviceId}/tailnet/reprovision", Tags: []string{"services"},
		Summary:       "Re-provision the service's tailnet node",
		DefaultStatus: http.StatusAccepted,
		Description:   "Deletes the service's device from the tailnet and re-creates it with a fresh single-use key and a fresh state volume. Use it when the node needs attention (the device was removed in the admin console or its key was rejected). The device gets a new tailnet IP.",
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
	}), policy.ScopeDeploymentsWrite), func(ctx context.Context, in *reprovisionInput) (*reprovisionOutput, error) {
		r, err := svc.Reprovision(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &reprovisionOutput{Body: r}, nil
	})
}
