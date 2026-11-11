package adoption

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type planInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body AdoptionPlanRequest
}

type applyInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body AdoptionApplyRequest
}

// Register attaches the server adoption endpoints.
func Register(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusServiceUnavailable, http.StatusGatewayTimeout}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "planServerAdoption", Method: http.MethodPost, Path: "/servers/{serverId}/adoption/plan", Tags: []string{"servers"},
		Summary: "Review the workloads of a server",
		Description: "Asks the server's agent for the NebulaCtrl workloads already running on the host (containers labelled by an earlier NebulaCtrl, their volumes and proxy routes) and returns how each project, " +
			"environment, service, volume, variable and domain would be rebuilt around them, reusing the IDs found on the containers so nothing is recreated or restarted. Variables are listed by key; values are never returned. " +
			"`choices` change the defaults; send the same body again to see the effect. Nothing is written. 409 `nothing_to_adopt` when no workloads of an earlier NebulaCtrl are on the host. " +
			"Needs servers:write, projects:write and an unrestricted token.",
		Errors: append(errs, http.StatusConflict),
	}, policy.ScopeServersWrite, policy.ScopeProjectsWrite), func(ctx context.Context, in *planInput) (*struct{ Body AdoptionPlan }, error) {
		p, err := svc.Plan(ctx, in.ID, in.Body)
		if err != nil {
			return nil, err
		}
		return &struct{ Body AdoptionPlan }{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "adoptServer", Method: http.MethodPost, Path: "/servers/{serverId}/adoption", Tags: []string{"servers"},
		Summary: "Adopt the workloads of a server", DefaultStatus: http.StatusCreated,
		Description: "Rebuilds the services of a plan around their running containers with the IDs found on them: no container is recreated or restarted and no deployment runs. " +
			"Services chosen as `leave` keep running unmanaged and are never removed; `remove` services are removed once the review ends. The request must match a plan without blockers; " +
			"the plan is recomputed from the host. Ends the hold on the server. Needs servers:write, projects:write and deployments:write and an unrestricted token.",
		Errors: append(errs, http.StatusConflict),
	}), policy.ScopeServersWrite, policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *applyInput) (*struct{ Body AdoptionResult }, error) {
		r, err := svc.Apply(ctx, in.ID, in.Body)
		if err != nil {
			return nil, err
		}
		return &struct{ Body AdoptionResult }{Body: r}, nil
	})
}
