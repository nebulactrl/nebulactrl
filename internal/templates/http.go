package templates

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// TemplateCatalog is the response of the catalog listing.
type TemplateCatalog struct {
	Items      []TemplateSummary  `json:"items"`
	Categories []TemplateCategory `json:"categories"`
}

type listCatalogInput struct {
	Q        string `query:"q" doc:"Search terms (name, summary, tags)"`
	Category string `query:"category" enum:"homelab,media,storage,games,devtools,ai,data,web,analytics,automation,productivity,security" doc:"Restrict to a category"`
	License  string `query:"license" doc:"Restrict to a licence class: permissive, copyleft, source-available or proprietary"`
}

type templateIDInput struct {
	TemplateID string `path:"templateId"`
}

type planInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	TemplateID    string    `path:"templateId"`
	Body          TemplatePlanRequest
}

type installInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	TemplateID    string    `path:"templateId"`
	Body          TemplateInstallRequest
}

type envIDInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
}

type instanceIDInput struct {
	ID uuid.UUID `path:"instanceId"`
}

type applyUpdateInput struct {
	ID   uuid.UUID `path:"instanceId"`
	Body TemplateUpdateRequest
}

type deleteInstanceInput struct {
	ID            uuid.UUID `path:"instanceId"`
	Confirm       string    `query:"confirm" doc:"Must equal the template id"`
	DeleteVolumes bool      `query:"deleteVolumes" doc:"Also remove the stack's Docker volumes (data is lost)"`
}

// List returns the catalog (optionally filtered) with per-category counts.
func (s *Service) List(ctx context.Context, f Filter) (TemplateCatalog, error) {
	if err := policy.Require(ctx, policy.ScopeProjectsRead, nil); err != nil {
		return TemplateCatalog{}, err
	}
	out := TemplateCatalog{Items: []TemplateSummary{}, Categories: []TemplateCategory{}}
	for _, t := range s.Catalog.Find(f) {
		out.Items = append(out.Items, summaryOf(t))
	}
	// Category counts follow the query and licence filter, not the category.
	counts := map[string]int{}
	for _, t := range s.Catalog.Find(Filter{Query: f.Query, LicenseClass: f.LicenseClass}) {
		counts[t.Category]++
	}
	for _, c := range Categories {
		out.Categories = append(out.Categories, TemplateCategory{ID: c, Label: categoryLabels[c], Count: counts[c]})
	}
	return out, nil
}

// Get returns one template.
func (s *Service) Get(ctx context.Context, id string) (*CatalogTemplate, error) {
	if err := policy.Require(ctx, policy.ScopeProjectsRead, nil); err != nil {
		return nil, err
	}
	return s.template(id)
}

// Register attaches the template endpoints.
func Register(h huma.API, svc *Service) {
	errs := func(extra ...int) []int {
		return append([]int{http.StatusUnauthorized, http.StatusForbidden}, extra...)
	}
	nf, uc := http.StatusNotFound, http.StatusUnprocessableEntity

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listCatalogTemplates", Method: http.MethodGet, Path: "/catalog/templates", Tags: []string{"templates"},
		Summary: "List the one-click template catalog",
		Description: "Built-in, reviewed templates. `q` matches every word in the name, summary, tags, licence and database engines (best match first); `category` and `license` filter. " +
			"Source-available templates carry `licenseClass: source-available`.",
		Errors: errs(),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *listCatalogInput) (*struct{ Body TemplateCatalog }, error) {
		l, err := svc.List(ctx, Filter{Query: in.Q, Category: in.Category, LicenseClass: in.License})
		if err != nil {
			return nil, err
		}
		return &struct{ Body TemplateCatalog }{Body: l}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getCatalogTemplate", Method: http.MethodGet, Path: "/catalog/templates/{templateId}", Tags: []string{"templates"},
		Summary: "Get a template", Errors: errs(nf),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *templateIDInput) (*struct{ Body *CatalogTemplate }, error) {
		t, err := svc.Get(ctx, in.TemplateID)
		if err != nil {
			return nil, err
		}
		return &struct{ Body *CatalogTemplate }{Body: t}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "planTemplate", Method: http.MethodPost, Path: "/environments/{environmentId}/templates/{templateId}/plan", Tags: []string{"templates"},
		Summary: "Preflight a template install",
		Description: "Returns the services that would be created, the secrets that would be generated (names only) and " +
			"checks against the chosen server (architecture, memory, proxy, domain, published ports). Nothing is written.",
		Errors: errs(nf, uc),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *planInput) (*struct{ Body TemplatePlan }, error) {
		p, err := svc.Plan(ctx, in.EnvironmentID, in.TemplateID, in.Body)
		if err != nil {
			return nil, err
		}
		return &struct{ Body TemplatePlan }{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "installTemplate", Method: http.MethodPost, Path: "/environments/{environmentId}/templates/{templateId}/instances", Tags: []string{"templates"},
		Summary: "Install a template", DefaultStatus: http.StatusCreated,
		Description: "Creates the template's services, volumes, files, variables (secrets generated once), domain, published ports, links " +
			"and a canvas group as staged changes. With `deploy: true` the new services are deployed in dependency order " +
			"(needs `deployments:write`); templates that publish TCP/UDP ports also need `proxy:write`.",
		Errors: errs(nf, http.StatusConflict, uc),
	}), policy.ScopeProjectsWrite), func(ctx context.Context, in *installInput) (*struct{ Body TemplateInstallResult }, error) {
		r, err := svc.Install(ctx, in.EnvironmentID, in.TemplateID, in.Body)
		if err != nil {
			return nil, err
		}
		return &struct{ Body TemplateInstallResult }{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listTemplateInstances", Method: http.MethodGet, Path: "/environments/{environmentId}/template-instances", Tags: []string{"templates"},
		Summary: "List the template stacks installed in an environment", Errors: errs(nf),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *envIDInput) (*struct{ Body []TemplateInstance }, error) {
		l, err := svc.ListInstances(ctx, in.EnvironmentID)
		if err != nil {
			return nil, err
		}
		return &struct{ Body []TemplateInstance }{Body: l}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getTemplateUpdate", Method: http.MethodGet, Path: "/template-instances/{instanceId}/update", Tags: []string{"templates"},
		Summary: "Diff an installed stack against the template's current revision",
		Description: "Lists what changed between the template as installed and the catalog's current revision: " +
			"`apply` changes the update would stage, `kept` ones that differ because you changed the service, " +
			"`manual` ones (new or removed services, database upgrades, published ports) that an update never does. " +
			"Nothing is written. `blocked` is set for stacks installed before updates existed.",
		Errors: errs(nf),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *instanceIDInput) (*struct{ Body TemplateUpdate }, error) {
		u, err := svc.GetUpdate(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &struct{ Body TemplateUpdate }{Body: u}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "applyTemplateUpdate", Method: http.MethodPost, Path: "/template-instances/{instanceId}/update", Tags: []string{"templates"},
		Summary: "Stage a template update",
		Description: "Stages the `apply` changes of the diff as ordinary edits of the stack's services (image, configuration, " +
			"variables, files, volumes); nothing is deployed, the staged bar and Deploy do that. Fields you changed are kept, " +
			"secrets are never overwritten, and databases, routes and services are never touched. `revision` must be the " +
			"revision you reviewed (409 `template_changed` otherwise). A retry after a failure is safe.",
		Errors: errs(nf, http.StatusConflict, uc),
	}), policy.ScopeProjectsWrite), func(ctx context.Context, in *applyUpdateInput) (*struct{ Body TemplateUpdateResult }, error) {
		r, err := svc.ApplyUpdate(ctx, in.ID, in.Body)
		if err != nil {
			return nil, err
		}
		return &struct{ Body TemplateUpdateResult }{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteTemplateInstance", Method: http.MethodDelete, Path: "/template-instances/{instanceId}", Tags: []string{"templates"},
		Summary: "Delete a template stack", DefaultStatus: http.StatusNoContent,
		Description: "Removes every service of the stack. Requires `confirm=<template id>`. Volumes are kept unless `deleteVolumes=true`.",
		Errors:      errs(nf, http.StatusConflict, uc),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *deleteInstanceInput) (*struct{}, error) {
		return nil, svc.DeleteInstance(ctx, in.ID, in.Confirm, in.DeleteVolumes)
	})
}
