package fleet

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/servers"
)

type serverIDInput struct {
	ID uuid.UUID `path:"serverId"`
}

type planInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		TargetServerID *uuid.UUID `json:"targetServerId,omitempty" doc:"Send every service to this server; omit to pick the online server with the most free memory per service"`
	} `required:"false"`
}

type planOutput struct{ Body DrainPlan }

type moveBody struct {
	ServiceID      uuid.UUID `json:"serviceId"`
	TargetServerID uuid.UUID `json:"targetServerId"`
}

type startInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		Moves           []moveBody `json:"moves" doc:"Where every service on the server goes; each service must appear exactly once"`
		ConfirmStateful bool       `json:"confirmStateful,omitempty" doc:"Required when a moved service has volumes or is a database: their data is not copied"`
	}
}

type drainOutput struct{ Body DrainView }

type getDrainOutput struct {
	Body struct {
		Drain *DrainView `json:"drain" doc:"The running drain, else the newest finished one; null when the server was never drained"`
	}
}

type undrainOutput struct{ Body servers.Server }

type overviewOutput struct{ Body Overview }

// Register attaches the fleet and drain endpoints.
func Register(h huma.API, svc *Service) {
	tags := []string{"fleet"}
	common := []int{http.StatusUnauthorized, http.StatusForbidden}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getFleet", Method: http.MethodGet, Path: "/fleet", Tags: tags,
		Summary:     "Capacity and placement of every server",
		Description: "Per server: resource usage, the memory promised through service limits (committed), whether that exceeds the host, and the services placed on it (limited to the caller's projects).",
		Errors:      common,
	}, policy.ScopeServersRead), func(ctx context.Context, _ *struct{}) (*overviewOutput, error) {
		o, err := svc.Overview(ctx)
		if err != nil {
			return nil, err
		}
		return &overviewOutput{Body: o}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "planServerDrain", Method: http.MethodPost, Path: "/servers/{serverId}/drain/plan", Tags: tags,
		Summary: "Plan evacuating a server",
		Description: "Dry run: proposes a target for every service on the server, flags stateful services (volumes are not copied), " +
			"and checks that the targets have the memory. Requires a token that is not restricted to projects.",
		Errors: append([]int{http.StatusNotFound, http.StatusUnprocessableEntity}, common...),
	}, policy.ScopeServersRead, policy.ScopeProjectsRead), func(ctx context.Context, in *planInput) (*planOutput, error) {
		p, err := svc.Plan(ctx, in.ID, in.Body.TargetServerID)
		if err != nil {
			return nil, err
		}
		return &planOutput{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "startServerDrain", Method: http.MethodPost, Path: "/servers/{serverId}/drain", Tags: tags,
		Summary: "Drain a server", DefaultStatus: http.StatusAccepted,
		Description: "Marks the server as draining (nothing new is placed on it) and moves its services one at a time: " +
			"each move changes the placement and deploys the service to the target; the old container is removed once the new one is live. " +
			"The first failed move stops the drain and leaves the remaining services untouched. Scopes: servers:write, deployments:write, projects:write.",
		Errors: append([]int{http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity}, common...),
	}), policy.ScopeServersWrite, policy.ScopeDeploymentsWrite, policy.ScopeProjectsWrite), func(ctx context.Context, in *startInput) (*drainOutput, error) {
		moves := make([]MoveInput, len(in.Body.Moves))
		for i, m := range in.Body.Moves {
			moves[i] = MoveInput{ServiceID: m.ServiceID, TargetServerID: m.TargetServerID}
		}
		v, err := svc.Start(ctx, in.ID, StartInput{Moves: moves, ConfirmStateful: in.Body.ConfirmStateful})
		if err != nil {
			return nil, err
		}
		return &drainOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServerDrain", Method: http.MethodGet, Path: "/servers/{serverId}/drain", Tags: tags,
		Summary: "Progress of a server's drain", Errors: append([]int{http.StatusNotFound}, common...),
	}, policy.ScopeServersRead, policy.ScopeProjectsRead), func(ctx context.Context, in *serverIDInput) (*getDrainOutput, error) {
		v, err := svc.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &getDrainOutput{}
		out.Body.Drain = v
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "cancelServerDrain", Method: http.MethodPost, Path: "/servers/{serverId}/drain/cancel", Tags: tags,
		Summary: "Stop a drain after the move in flight",
		Description: "The drain ends after the current move. The server returns to `none` when nothing was moved yet, " +
			"otherwise it becomes `failed` with \"Canceled after N of M moves\".",
		Errors: append([]int{http.StatusNotFound, http.StatusConflict}, common...),
	}, policy.ScopeServersWrite, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *serverIDInput) (*drainOutput, error) {
		v, err := svc.Cancel(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &drainOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "undrainServer", Method: http.MethodPost, Path: "/servers/{serverId}/undrain", Tags: tags,
		Summary:     "Make a drained server schedulable again",
		Description: "Clears the drain state of a drained or failed-drain server. Services that were moved stay where they are.",
		Errors:      append([]int{http.StatusNotFound, http.StatusConflict}, common...),
	}, policy.ScopeServersWrite), func(ctx context.Context, in *serverIDInput) (*undrainOutput, error) {
		s, err := svc.Undrain(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &undrainOutput{Body: s}, nil
	})
}
