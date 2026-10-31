package coolify

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type planInput struct {
	Body CoolifyPlanRequest
}

type applyInput struct {
	Body CoolifyApplyRequest
}

// Register attaches the Coolify import endpoints.
func Register(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "planCoolifyImport", Method: http.MethodPost, Path: "/imports/coolify/plan", Tags: []string{"imports"},
		Summary: "Review an import from Coolify",
		Description: "Reads a Coolify v4 instance with the given API token (read and read:sensitive) and returns how every project, environment, application and database " +
			"would be imported: the service source and settings, domains, variables by key (secrets flagged, values never returned), references rewritten to other imported " +
			"resources, what is not supported, and the follow-up steps (data is never copied). `choices` change the defaults; send the same body again to see the effect. " +
			"Nothing is written and the Coolify token is used for this request only. Needs projects:write and an unrestricted token, because the server connects to the given address with the credential.",
		Errors: errs,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *planInput) (*struct{ Body CoolifyPlan }, error) {
		p, err := svc.Plan(ctx, in.Body)
		if err != nil {
			return nil, err
		}
		return &struct{ Body CoolifyPlan }{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "importCoolify", Method: http.MethodPost, Path: "/imports/coolify/apply", Tags: []string{"imports"},
		Summary: "Import from Coolify", DefaultStatus: http.StatusCreated,
		Description: "Creates the projects, environments, services and managed databases of a Coolify instance as staged changes: services, volumes, variables (secrets marked), domains and links. " +
			"The request must match a plan without blockers; the plan is recomputed from Coolify, so the response never depends on a stale review. Database and volume data is not copied. " +
			"With `deploy: true` the new services are deployed (needs deployments:write); otherwise review them and deploy from the staged bar.",
		Errors: append(errs, http.StatusConflict),
	}), policy.ScopeProjectsWrite), func(ctx context.Context, in *applyInput) (*struct{ Body CoolifyApplyResult }, error) {
		r, err := svc.Apply(ctx, in.Body)
		if err != nil {
			return nil, err
		}
		return &struct{ Body CoolifyApplyResult }{Body: r}, nil
	})
}
