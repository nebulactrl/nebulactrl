package meshlinks

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type environmentInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
}

type serviceInput struct {
	ServiceID uuid.UUID `path:"serviceId"`
}

type environmentMeshOutput struct{ Body EnvironmentMeshView }

type serviceMeshOutput struct{ Body ServiceMeshView }

type reconcileInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	Body          struct {
		Mode              string `json:"mode" enum:"add,repair" doc:"add creates the links the environment needs (devices need acceptMeshChanges); repair only removes grants without a source"`
		AcceptMeshChanges bool   `json:"acceptMeshChanges,omitempty" doc:"Confirm the tailnet devices mode add creates; the 409 mesh_confirmation_required response states how many"`
	}
}

type reconcileOutput struct{ Body ReconcileView }

// Register attaches the cross-server service link endpoints.
func Register(h huma.API, svc *Service) {
	tags := []string{"mesh"}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getEnvironmentMesh", Method: http.MethodGet, Path: "/environments/{environmentId}/mesh", Tags: tags,
		Summary: "Mesh links, grants and routes of an environment",
		Description: "Needs `projects:read`. Links are the tailnet link nodes of the environment (service and replication purpose); " +
			"grants say which service reaches which provider and why (`reference`, `edge`, `replica`); routes list, per consumer, the providers on other servers with the mesh port the consumer dials.",
		Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *environmentInput) (*environmentMeshOutput, error) {
		v, err := svc.EnvironmentMesh(ctx, in.EnvironmentID)
		if err != nil {
			return nil, err
		}
		return &environmentMeshOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServiceMesh", Method: http.MethodGet, Path: "/services/{serviceId}/mesh", Tags: tags,
		Summary: "Mesh exposures, routes and consumers of a service",
		Description: "Needs `projects:read`. `exposures` are the service's ports on its server's mesh node, `routes` the providers on other servers it reaches " +
			"(a consumer dials `<provider slug>:<mesh port>`), `consumers` the services on other servers that reach it.",
		Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceInput) (*serviceMeshOutput, error) {
		v, err := svc.ServiceMesh(ctx, in.ServiceID)
		if err != nil {
			return nil, err
		}
		return &serviceMeshOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "reconcileEnvironmentMesh", Method: http.MethodPost, Path: "/environments/{environmentId}/mesh/reconcile", Tags: tags,
		Summary: "Reconcile the mesh links of an environment now",
		Description: "Needs `servers:write`. Mode `add` creates every link, exposure and grant the environment's references and canvas edges need; " +
			"it adds tailnet devices, so it needs `acceptMeshChanges` (409 `mesh_confirmation_required` states the count; 422 with the blocker code, for example `mesh_server_required`, when the mesh cannot serve a need). " +
			"Mode `repair` is what runs every 5 minutes: it only removes grants whose source is gone (10 minutes grace; an edge's grant at once) and never adds a device.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeServersWrite), func(ctx context.Context, in *reconcileInput) (*reconcileOutput, error) {
		v, err := svc.ReconcileNow(ctx, in.EnvironmentID, Mode(in.Body.Mode), in.Body.AcceptMeshChanges)
		if err != nil {
			return nil, err
		}
		return &reconcileOutput{Body: v}, nil
	})
}
