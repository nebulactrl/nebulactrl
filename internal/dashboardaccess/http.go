package dashboardaccess

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type peerKey struct{}

// peerMiddleware records how the request reached the control plane for the
// handler: the TCP peer and X-Forwarded-For. Neither is a documented API
// parameter, so they are read here instead of declared on the operations.
func peerMiddleware(ctx huma.Context, next func(huma.Context)) {
	p := Peer{RemoteAddr: ctx.RemoteAddr(), ForwardedFor: ctx.Header("X-Forwarded-For")}
	next(huma.WithValue(ctx, peerKey{}, p))
}

func peerOf(ctx context.Context) Peer {
	p, _ := ctx.Value(peerKey{}).(Peer)
	return p
}

type viewOutput struct{ Body DashboardAccessView }

type updateDashboardAccessInput struct {
	Body struct {
		AllowCIDRs     []string `json:"allowCidrs" maxItems:"200" doc:"CIDRs or single addresses (IPv4 or IPv6) allowed to reach the dashboard. Empty lifts the restriction."`
		DNSResolver    string   `json:"dnsResolver,omitempty" doc:"DNS-01 provider of the dashboard's certificate (a resolver the control-plane server already has, see resolverAvailable). Empty is automatic HTTP-01."`
		ConfirmLockout bool     `json:"confirmLockout,omitempty" doc:"Save even though allowCidrs excludes the caller's current address (otherwise 409 dashboard_lockout)."`
	}
}

// Register attaches the dashboard access endpoints. Both belong to the
// instance admin (the first account).
func Register(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden}
	tag := []string{"instance"}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getDashboardAccess", Method: http.MethodGet, Path: "/instance/dashboard-access", Tags: tag,
		Summary: "Get the dashboard access settings",
		Description: "Who may reach the dashboard's hostname (an IP allow list on its system route) and how its certificate is issued (automatic HTTP-01 or a DNS-01 resolver of the control-plane server). " +
			"`clientIp` is the caller's address as the allow list sees it. Instance admin only.",
		Errors:      errs,
		Middlewares: huma.Middlewares{peerMiddleware},
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*viewOutput, error) {
		v, err := svc.Get(ctx, peerOf(ctx))
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateDashboardAccess", Method: http.MethodPut, Path: "/instance/dashboard-access", Tags: tag,
		Summary: "Update the dashboard access settings",
		Description: "Replaces the allow list and the certificate method and re-sends the control-plane server's routes: an allow list applies immediately. " +
			"Agents on other servers and webhooks reach the control plane through the same hostname, so keep their addresses in the list. " +
			"409 `dashboard_lockout` when a non-empty list excludes the caller's current address, unless `confirmLockout` is true. " +
			"422 `resolver_not_configured` when the control-plane server has no resolver of that provider. " +
			"A resolver that is staged but not applied yet sets `proxyDeployRequired`: the dashboard keeps its HTTP-01 certificate until Deploy proxy. " +
			"409 `dashboard_route_disabled` when the dashboard has no system route. Instance admin only.",
		Errors:      append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
		Middlewares: huma.Middlewares{peerMiddleware},
	}, policy.ScopeAdmin), func(ctx context.Context, in *updateDashboardAccessInput) (*viewOutput, error) {
		v, err := svc.Update(ctx, peerOf(ctx), UpdateInput{
			AllowCIDRs: in.Body.AllowCIDRs, DNSResolver: in.Body.DNSResolver, ConfirmLockout: in.Body.ConfirmLockout,
		})
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})
}
