package composeimport

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type planInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	Body          ComposePlanRequest
}

type importInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	Body          ComposeImportRequest
}

// Register attaches the Compose import endpoints.
func Register(h huma.API, svc *Service) {
	errs := func(extra ...int) []int {
		return append([]int{http.StatusUnauthorized, http.StatusForbidden}, extra...)
	}
	nf, uc := http.StatusNotFound, http.StatusUnprocessableEntity

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "planComposeImport", Method: http.MethodPost, Path: "/environments/{environmentId}/compose-imports/plan", Tags: []string{"compose"},
		Summary: "Review a Docker Compose import",
		Description: "Reads a docker-compose.yml (pasted, or from a git repository) and returns, for every Compose service, how it would be imported: " +
			"source, ports and domains, variables (secrets are flagged and never returned), volumes, files, the rewritten references, " +
			"what is not supported, and whether an official database image can become a managed database. `choices` change the defaults " +
			"(include a service, switch to a managed database, publish a port, ...); send the same body again to see the effect. " +
			"Nothing is written. Reading from a git repository needs `projects:write`.",
		Errors: errs(nf, uc),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *planInput) (*struct{ Body ComposePlan }, error) {
		p, err := svc.Plan(ctx, in.EnvironmentID, in.Body)
		if err != nil {
			return nil, err
		}
		return &struct{ Body ComposePlan }{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "importCompose", Method: http.MethodPost, Path: "/environments/{environmentId}/compose-imports", Tags: []string{"compose"},
		Summary: "Import a Docker Compose project", DefaultStatus: http.StatusCreated,
		Description: "Creates the services of a Compose project as staged changes: services, volumes, config files, variables, domains, " +
			"published ports, links (deploy order) and a canvas group. The request must match a plan without blockers. " +
			"With `deploy: true` the new services are deployed in dependency order (needs `deployments:write`); otherwise review them and " +
			"deploy from the staged bar.",
		Errors: errs(nf, http.StatusConflict, uc),
	}), policy.ScopeProjectsWrite), func(ctx context.Context, in *importInput) (*struct{ Body ComposeImportResult }, error) {
		r, err := svc.Import(ctx, in.EnvironmentID, in.Body)
		if err != nil {
			return nil, err
		}
		return &struct{ Body ComposeImportResult }{Body: r}, nil
	})
}
