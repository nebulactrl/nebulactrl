package tunnel

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type serverInput struct {
	ID uuid.UUID `path:"serverId"`
}

type disableInput struct {
	ID    uuid.UUID `path:"serverId"`
	Force bool      `query:"force" doc:"Drop the tunnel record without asking Cloudflare (it may linger in the Cloudflare dashboard). Use it when the Cloudflare connection is gone or Cloudflare keeps refusing the delete."`
}

type viewOutput struct{ Body TunnelView }

// Register attaches the server tunnel endpoints.
func Register(h huma.API, svc *Service) {
	tags := []string{"servers"}
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServerTunnel", Method: http.MethodGet, Path: "/servers/{serverId}/tunnel", Tags: tags,
		Summary:     "Cloudflare Tunnel state of a server",
		Description: "Whether the server has a Cloudflare Tunnel, the connector's observed state and the ingress sync. A server without a tunnel answers `state: off` and says whether one can be enabled.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, policy.ScopeServersRead), func(ctx context.Context, in *serverInput) (*viewOutput, error) {
		v, err := svc.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "enableServerTunnel", Method: http.MethodPost, Path: "/servers/{serverId}/tunnel", Tags: tags,
		Summary:       "Enable a Cloudflare Tunnel on a server",
		DefaultStatus: http.StatusAccepted,
		Description: "Creates a remotely-managed tunnel with the team's Cloudflare connection, pushes its ingress rules (every hostname of the server's services to the proxy, then a 404 catch-all) and tells the agent to run the cloudflared connector. The connector token is fetched when the command is sent and is never stored. Once the connector connects, the server's domains in connected zones point at the tunnel and ports 80 and 443 may stay closed. " +
			"Needs the `servers:write` and `proxy:write` scopes. The Cloudflare token needs the permission Account, Cloudflare Tunnel, Edit (422 `cloudflare_tunnel_forbidden`).",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusBadGateway},
	}), policy.ScopeServersWrite, policy.ScopeProxyWrite), func(ctx context.Context, in *serverInput) (*viewOutput, error) {
		v, err := svc.Enable(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "disableServerTunnel", Method: http.MethodDelete, Path: "/servers/{serverId}/tunnel", Tags: tags,
		Summary:       "Disable the Cloudflare Tunnel of a server",
		DefaultStatus: http.StatusAccepted,
		Description:   "Points the server's domains back at its public IP, removes the connector and deletes the tunnel at Cloudflare shortly afterwards. Disabling a tunnel that is already being removed retries the delete. Ports 80 and 443 must be reachable again before the DNS change propagates.",
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, policy.ScopeServersWrite, policy.ScopeProxyWrite), func(ctx context.Context, in *disableInput) (*viewOutput, error) {
		v, err := svc.Disable(ctx, in.ID, in.Force)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "resyncServerTunnel", Method: http.MethodPost, Path: "/servers/{serverId}/tunnel/sync", Tags: tags,
		Summary:       "Re-sync the Cloudflare Tunnel of a server",
		DefaultStatus: http.StatusAccepted,
		Description:   "Re-pushes the ingress rules and re-sends the connector command (creating the container again when it is gone). Use it when the connector needs attention.",
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusBadGateway},
	}), policy.ScopeServersWrite, policy.ScopeProxyWrite), func(ctx context.Context, in *serverInput) (*viewOutput, error) {
		v, err := svc.Resync(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})
}
