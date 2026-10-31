package networks

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

type listNetworksInput struct {
	ServerID string `query:"serverId" format:"uuid" doc:"Only the networks on this server"`
}

type listNetworksOutput struct {
	Body struct {
		Items []NetworkView `json:"items"`
	}
}

type networkOutput struct{ Body NetworkView }

type networkIDInput struct {
	ID uuid.UUID `path:"networkId"`
}

type createNetworkInput struct {
	Body struct {
		Name     string    `json:"name" minLength:"1" maxLength:"63" doc:"Unique per team; letters, digits, spaces, '.', '_' and '-'"`
		ServerID uuid.UUID `json:"serverId" doc:"The server the network lives on; only services on this server can attach"`
	}
}

type serviceNetworkInput struct {
	ServiceID uuid.UUID `path:"serviceId"`
	NetworkID uuid.UUID `path:"networkId"`
}

type attachmentOutput struct{ Body services.NetworkRef }

// Register attaches the shared-network endpoints.
func Register(h huma.API, svc *Service) {
	tags := []string{"networks"}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listNetworks", Method: http.MethodGet, Path: "/networks", Tags: tags,
		Summary:     "List the team's shared networks",
		Description: "Needs `servers:read`. Each network lives on one server and lists the services attached to it.",
		Errors:      errs[:2],
	}, policy.ScopeServersRead), func(ctx context.Context, in *listNetworksInput) (*listNetworksOutput, error) {
		var serverID *uuid.UUID
		if in.ServerID != "" {
			id, err := uuid.Parse(in.ServerID)
			if err != nil {
				return nil, apierr.Unprocessable("validation_failed", "serverId must be a UUID")
			}
			serverID = &id
		}
		items, err := svc.List(ctx, serverID)
		if err != nil {
			return nil, err
		}
		out := &listNetworksOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createNetwork", Method: http.MethodPost, Path: "/networks", Tags: tags,
		Summary: "Create a shared network", DefaultStatus: http.StatusCreated,
		Description: "Needs `servers:write`. A shared network spans one server: services of any project of the team that run on it attach explicitly " +
			"and are reachable by `<service>.<env>.<project>`. 409 `network_name_taken`, 422 `server_not_found`.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}), policy.ScopeServersWrite), func(ctx context.Context, in *createNetworkInput) (*networkOutput, error) {
		v, err := svc.Create(ctx, in.Body.Name, in.Body.ServerID)
		if err != nil {
			return nil, err
		}
		return &networkOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getNetwork", Method: http.MethodGet, Path: "/networks/{networkId}", Tags: tags,
		Summary: "Get a shared network", Errors: errs,
	}, policy.ScopeServersRead), func(ctx context.Context, in *networkIDInput) (*networkOutput, error) {
		v, err := svc.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &networkOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteNetwork", Method: http.MethodDelete, Path: "/networks/{networkId}", Tags: tags,
		Summary: "Delete a shared network", DefaultStatus: http.StatusNoContent,
		Description: "Needs `servers:write`. 409 `network_in_use` while a service is attached.",
		Errors:      append(errs, http.StatusConflict),
	}, policy.ScopeServersWrite), func(ctx context.Context, in *networkIDInput) (*struct{}, error) {
		return nil, svc.Delete(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "attachServiceNetwork", Method: http.MethodPut, Path: "/services/{serviceId}/networks/{networkId}", Tags: tags,
		Summary: "Attach a service to a shared network (staged)",
		Description: "Needs `deployments:write`. Advances the service generation; the next deployment joins the network. " +
			"422 `network_wrong_server` when the service is on another server than the network, 422 `egress_blocked` while the service blocks outbound internet.",
		Errors: append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *serviceNetworkInput) (*attachmentOutput, error) {
		ref, err := svc.Attach(ctx, in.ServiceID, in.NetworkID)
		if err != nil {
			return nil, err
		}
		return &attachmentOutput{Body: ref}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "detachServiceNetwork", Method: http.MethodDelete, Path: "/services/{serviceId}/networks/{networkId}", Tags: tags,
		Summary: "Detach a service from a shared network (staged)", DefaultStatus: http.StatusNoContent,
		Description: "Needs `deployments:write`. Advances the service generation; the next deployment leaves the network.",
		Errors:      errs,
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *serviceNetworkInput) (*struct{}, error) {
		return nil, svc.Detach(ctx, in.ServiceID, in.NetworkID)
	})
}
