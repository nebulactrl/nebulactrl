package combine

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
	Body          CombinePlanRequest
}

type planOutput struct{ Body CombinePlan }

type startInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	Body          CombineRequest
}

type combineInput struct {
	ID uuid.UUID `path:"combineId"`
}

type combineOutput struct{ Body CombineView }

// Register attaches the combine endpoints.
func Register(h huma.API, s *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "planCombineServices", Method: http.MethodPost, Path: "/environments/{environmentId}/services/combine/plan", Tags: []string{"services"},
		Summary: "Plan combining services into one",
		Description: "Reports what merging 2 to 10 git services of one environment into a single service with several processes would do, and whether it can start. " +
			"The target keeps running and gains one process per other service (their start command, port, health check, limits and replicas); they share its one build, image, variables and release. " +
			"`blocking` lists what prevents the start (`not_combinable`, `source_mismatch`, `server_mismatch`, `agent_too_old`, `source_has_volumes`, `already_has_processes`, `port_conflict`, `task_name_conflict`, `source_referenced`, ...); " +
			"`variables.conflicts` lists the variables the services set to different values (the start needs a choice for each; secrets are compared by value and never shown); " +
			"`domainMoves` and `taskMoves` are what moves to the target at the cutover. `fingerprint` binds a start to this plan. It changes nothing.",
		DefaultStatus: http.StatusOK, Errors: errs,
	}, policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *planInput) (*planOutput, error) {
		p, err := s.Plan(ctx, in.EnvironmentID, in.Body)
		if err != nil {
			return nil, err
		}
		return &planOutput{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "combineServices", Method: http.MethodPost, Path: "/environments/{environmentId}/services/combine", Tags: []string{"services"},
		Summary: "Combine services into one",
		Description: "Starts merging the services of a reviewed plan (202). The target is staged with one process per other service and variables chosen with `variableChoices`; the other services are not touched and keep serving. " +
			"A deployment of the target then builds once and rolls every process, all or nothing. Only when it succeeded do the domains and scheduled tasks of the other services move to the target, in one step with one route table, and the other services are deleted (their volumes are kept). " +
			"If the deployment fails nothing was deleted: the combine is `failed`, and can be retried or aborted. A stale `fingerprint` is 409 `plan_stale`; a missing variable choice 422 `variable_choice_required`. " +
			"Deleting services needs approval for guarded clients in production.",
		DefaultStatus: http.StatusAccepted, Errors: errs,
	}), policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *startInput) (*combineOutput, error) {
		v, err := s.Start(ctx, in.EnvironmentID, in.Body)
		if err != nil {
			return nil, err
		}
		return &combineOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getCombine", Method: http.MethodGet, Path: "/combines/{combineId}", Tags: []string{"services"},
		Summary:     "Get a combine",
		Description: "The state of a combine: `staged`, `deploying`, `cutover`, `done`, `failed` or `aborted`, its deployment and, when failed, why.",
		Errors:      errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *combineInput) (*combineOutput, error) {
		v, err := s.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &combineOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "abortCombine", Method: http.MethodPost, Path: "/combines/{combineId}/abort", Tags: []string{"services"},
		Summary: "Abort a combine",
		Description: "Takes a `staged` or `failed` combine back: stopped services start again, the changes it staged on the target are discarded and the scale settings it created are removed. " +
			"409 `invalid_state` while the deployment or the cutover runs, and once the services are merged.",
		Errors: errs,
	}), policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *combineInput) (*combineOutput, error) {
		v, err := s.Abort(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &combineOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "retryCombine", Method: http.MethodPost, Path: "/combines/{combineId}/retry", Tags: []string{"services"},
		Summary: "Retry a failed combine",
		Description: "Runs a `failed` combine again (202) with the configuration it staged: the deployment of the target, then the cutover. " +
			"When the target's release already runs every process, only the cutover runs. 409 `invalid_state` unless the combine is failed.",
		DefaultStatus: http.StatusAccepted, Errors: errs,
	}), policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *combineInput) (*combineOutput, error) {
		v, err := s.Retry(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &combineOutput{Body: v}, nil
	})
}
