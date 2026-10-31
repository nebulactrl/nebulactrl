package scaling

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

type setScalingInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		Replicas    *int              `json:"replicas,omitempty" doc:"Desired replica count, 1-10. Not allowed while autoscaling is enabled."`
		Autoscaling *AutoscalingPatch `json:"autoscaling,omitempty" doc:"Omitted fields keep their current value"`
	}
}

type setProcessScalingInput struct {
	ID      uuid.UUID `path:"serviceId"`
	Process string    `path:"process" doc:"The process name; the default process' own name is the same as PUT /services/{serviceId}/scaling"`
	Body    struct {
		Replicas    *int              `json:"replicas,omitempty" doc:"Desired replica count, 1-10. Not allowed while autoscaling is enabled."`
		Autoscaling *AutoscalingPatch `json:"autoscaling,omitempty" doc:"Omitted fields keep their current value"`
	}
}

type serviceOutput struct {
	ETag string `header:"ETag"`
	Body services.ServiceView
}

// Register attaches the scaling endpoints.
func Register(h huma.API, s *Service) {
	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "setServiceScaling", Method: http.MethodPut, Path: "/services/{serviceId}/scaling", Tags: []string{"services"},
		Summary: "Set a service's replica count and autoscaling",
		Description: "Runs the service on N replicas on its server (1-10) and/or turns CPU autoscaling on. Only stateless services qualify: " +
			"no volumes, an HTTP port and kind other than database (422 `replicas_require_stateless`). A manual `replicas` value while " +
			"autoscaling is enabled returns 409 `autoscaling_enabled`. The change applies immediately, without a deployment and without " +
			"rebuilding: a deployed service is scaled through the agent, an undeployed one only stores the setting. " +
			"It is never a staged change.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}), policy.ScopeDeploymentsWrite), func(ctx context.Context, in *setScalingInput) (*serviceOutput, error) {
		v, err := s.Set(ctx, in.ID, SetInput{Replicas: in.Body.Replicas, Autoscaling: in.Body.Autoscaling})
		if err != nil {
			return nil, err
		}
		return &serviceOutput{ETag: services.ETag(v.Generation), Body: v}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "setProcessScaling", Method: http.MethodPut, Path: "/services/{serviceId}/processes/{process}/scaling", Tags: []string{"services"},
		Summary: "Set the replica count and autoscaling of one process",
		Description: "Like setServiceScaling, for one process of a service that runs several. `process` is a named process or the default " +
			"process' name (the same as PUT /services/{serviceId}/scaling); an unknown name returns 404 `unknown_process`. A process " +
			"scales when the service has no volumes or config files and the process is not a singleton (422 `replicas_require_stateless`); " +
			"a process needs no HTTP port. The change applies immediately, without a deployment: a process of the running release is scaled " +
			"through the agent, one that is staged but not deployed yet only stores the setting. It is never a staged change.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}), policy.ScopeDeploymentsWrite), func(ctx context.Context, in *setProcessScalingInput) (*serviceOutput, error) {
		v, err := s.SetProcess(ctx, in.ID, in.Process, SetInput{Replicas: in.Body.Replicas, Autoscaling: in.Body.Autoscaling})
		if err != nil {
			return nil, err
		}
		return &serviceOutput{ETag: services.ETag(v.Generation), Body: v}, nil
	})
}
