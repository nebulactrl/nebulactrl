package services

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// RouteHook lets the proxy owner (internal/proxyroutes) react to the deletion
// of a route service inside Delete's transaction: it pushes the remaining route
// table to the server the route was deployed on.
type RouteHook interface {
	// RouteDeleted is called after the route service row was soft-deleted and
	// its domains and listen port removed.
	RouteDeleted(ctx context.Context, tx pgx.Tx, sc Scope) error
}

// SetRouteHook installs the hook (wire time only).
func (s *Service) SetRouteHook(h RouteHook) { s.routeHook = h }

// RequireContainer returns 422 `not_applicable` for route services: they have
// no container, so restart/stop, logs, metrics, volumes and scaling do not apply.
func RequireContainer(sc Scope, what string) error {
	if sc.Service.Kind == KindRoute {
		return apierr.Unprocessable("not_applicable", what+" does not apply to a route service: it has no container")
	}
	return nil
}

// applyRouteStatus derives the runtime status of a route service, which has no
// container and therefore never reports an observation: published means the
// deployed route table includes it ("running"), not deployed yet is left empty,
// and an offline server marks it stale.
func applyRouteStatus(v *ServiceView, r db.Service) {
	if r.CurrentDeploymentID == nil {
		return
	}
	v.Observed = &ObservedView{
		State: "running", Health: "healthy", DeploymentID: r.CurrentDeploymentID.String(),
		Replicas: []ObservedReplicaView{},
	}
	v.Stale = v.Server != nil && v.Server.Status != "online"
}
