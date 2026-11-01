package proxyroutes

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

type routeOutput struct {
	ETag string `header:"ETag"`
	Body services.ServiceView
}

type createRouteInput struct {
	EnvID uuid.UUID `path:"environmentId"`
	Body  struct {
		Name     string        `json:"name" minLength:"1" maxLength:"100"`
		ServerID uuid.UUID     `json:"serverId" doc:"The server that publishes the route (required)"`
		Protocol string        `json:"protocol" enum:"http,tcp,udp"`
		Domains  []DomainInput `json:"domains,omitempty" doc:"Hostnames of an http route (staged; refine each with PATCH /domains/{id})"`
		RouteFields
	}
}

type routeIDInput struct {
	ID uuid.UUID `path:"routeId"`
}

type updateRouteInput struct {
	ID      uuid.UUID `path:"routeId"`
	IfMatch string    `header:"If-Match" doc:"The ETag (generation) from the last GET, e.g. \"3\". Required."`
	Body    struct {
		Name *string `json:"name,omitempty" minLength:"1" maxLength:"100"`
		RouteFields
	}
}

type serverIDInput struct {
	ID uuid.UUID `path:"serverId"`
}

type portsOutput struct {
	Body struct {
		Items []PortView `json:"items"`
	}
}

func viewOutput(v services.ServiceView) *routeOutput {
	return &routeOutput{ETag: services.ETag(v.Generation), Body: v}
}

// Register attaches the route-service endpoints.
func Register(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createRoute", Method: http.MethodPost, Path: "/environments/{environmentId}/routes", Tags: []string{"routes"},
		Summary: "Create a route service (staged until deployed)", DefaultStatus: http.StatusCreated,
		Description: "A route service has no container: it publishes an external upstream (http), or exposes a tcp/udp port of a server " +
			"(optionally for a managed service). Needs `projects:write` and `proxy:write`. The server is required. " +
			"422 codes: `invalid_config`, `invalid_listen_port`, `upstream_denied` (the server's upstream policy), `invalid_upstream`, `agent_too_old`. " +
			"409 `port_in_use` when another route holds the listen port, `domain_conflict` for a taken hostname.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}), policy.ScopeProjectsWrite, policy.ScopeProxyWrite), func(ctx context.Context, in *createRouteInput) (*routeOutput, error) {
		v, err := svc.Create(ctx, in.EnvID, CreateInput{
			Name: in.Body.Name, ServerID: in.Body.ServerID, Protocol: in.Body.Protocol, Domains: in.Body.Domains, RouteFields: in.Body.RouteFields,
		})
		if err != nil {
			return nil, err
		}
		return viewOutput(v), nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getRoute", Method: http.MethodGet, Path: "/routes/{routeId}", Tags: []string{"routes"},
		Summary: "Get a route service", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *routeIDInput) (*routeOutput, error) {
		v, err := svc.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return viewOutput(v), nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateRoute", Method: http.MethodPatch, Path: "/routes/{routeId}", Tags: []string{"routes"},
		Summary: "Update a route service (staged until deployed)",
		Description: "Requires `If-Match: \"<generation>\"`. The protocol and server cannot be changed. " +
			"Same 4xx codes as create; 412 `generation_conflict` for a stale generation.",
		Errors: append(errs, http.StatusConflict, http.StatusPreconditionFailed, http.StatusPreconditionRequired, http.StatusUnprocessableEntity),
	}, policy.ScopeProjectsWrite, policy.ScopeProxyWrite), func(ctx context.Context, in *updateRouteInput) (*routeOutput, error) {
		gen, ok, err := services.ParseETag(in.IfMatch)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, apierr.New(http.StatusPreconditionRequired, "if_match_required", "send If-Match with the generation from the last GET")
		}
		v, err := svc.Update(ctx, in.ID, gen, UpdateInput{Name: in.Body.Name, RouteFields: in.Body.RouteFields})
		if err != nil {
			return nil, err
		}
		return viewOutput(v), nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteRoute", Method: http.MethodDelete, Path: "/routes/{routeId}", Tags: []string{"routes"},
		Summary: "Delete a route service", DefaultStatus: http.StatusNoContent,
		Description: "Frees the listen port and removes the route from its server's table (a sync is queued atomically). " +
			"`DELETE /services/{id}` does the same.",
		Errors: append(errs, http.StatusConflict),
	}, policy.ScopeProjectsWrite, policy.ScopeProxyWrite), func(ctx context.Context, in *routeIDInput) (*struct{}, error) {
		return nil, svc.Delete(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listServerPorts", Method: http.MethodGet, Path: "/servers/{serverId}/ports", Tags: []string{"routes"},
		Summary:     "List the tcp/udp ports route services hold on a server",
		Description: "Includes staged (not yet deployed) routes. Holder names of projects the caller cannot see are omitted.",
		Errors:      errs,
	}, policy.ScopeServersRead), func(ctx context.Context, in *serverIDInput) (*portsOutput, error) {
		items, err := svc.ServerPorts(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &portsOutput{}
		out.Body.Items = items
		return out, nil
	})
}
