package prodguard

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type approvalInput struct {
	ID uuid.UUID `path:"approvalId"`
}

type approvalOutput struct{ Body ApprovalView }

// Register attaches the approval endpoints. They are for signed-in people only:
// a token or MCP principal gets 403, so an agent can never approve itself.
func Register(h huma.API, g *Guard) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getApproval", Method: http.MethodGet, Path: "/approvals/{approvalId}", Tags: []string{"approvals"},
		Summary: "Get a production approval request",
		Description: "The plan an AI client's production change needs a person to approve: what will happen, who asked " +
			"(token and MCP client), when it expires and whether you can decide. Signed-in members who can read the " +
			"project only; tokens are refused.",
		Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *approvalInput) (*approvalOutput, error) {
		v, err := g.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &approvalOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "approveApproval", Method: http.MethodPost, Path: "/approvals/{approvalId}/approve", Tags: []string{"approvals"},
		Summary: "Approve a production change",
		Description: "Lets the waiting call proceed once, with exactly the plan shown. Owner, Admin or Developer with deploy " +
			"rights on the project, signed in. The approval is valid for ten minutes from the request and void when the " +
			"service changes in the meantime.",
		Errors: append(errs, http.StatusConflict),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *approvalInput) (*approvalOutput, error) {
		v, err := g.Approve(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &approvalOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "declineApproval", Method: http.MethodPost, Path: "/approvals/{approvalId}/decline", Tags: []string{"approvals"},
		Summary:     "Decline a production change",
		Description: "Refuses the waiting call. The same call stays refused for one minute. Same permissions as approving.",
		Errors:      append(errs, http.StatusConflict),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *approvalInput) (*approvalOutput, error) {
		v, err := g.Decline(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &approvalOutput{Body: v}, nil
	})
}
