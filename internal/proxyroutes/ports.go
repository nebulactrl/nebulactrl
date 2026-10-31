// Package proxyroutes implements route services (kind=route): nodes without a
// container that publish an external upstream through the proxy or expose a
// TCP/UDP port of the server, plus the per-server listen-port registry
// (route_ports) the exported helpers in this file maintain.
package proxyroutes

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// PortInUseError reports that a tcp/udp listen port of a server is already held
// by another route service. It surfaces as 409 `port_in_use`.
type PortInUseError struct {
	Protocol string
	Port     int
	ServerID uuid.UUID
	// HeldBy is the name of the holder ("" when the holder is not visible or
	// the conflict was detected by the unique index only).
	HeldBy string
}

func (e *PortInUseError) Error() string {
	msg := fmt.Sprintf("%s port %d is already in use on this server", e.Protocol, e.Port)
	if e.HeldBy != "" {
		msg += " by " + e.HeldBy
	}
	return msg
}

// Unwrap lets errors.As(err, **apierr.Error) find the HTTP representation.
func (e *PortInUseError) Unwrap() error {
	ae := apierr.Conflict("port_in_use", e.Error())
	return ae.WithDetails(map[string]any{"protocol": e.Protocol, "port": e.Port, "serverId": e.ServerID.String(), "heldBy": e.HeldBy})
}

// ValidateListenPort checks a tcp/udp host port (1024-65535, not reserved by the proxy).
func ValidateListenPort(port int) error {
	if err := agentprotocol.ValidateListenPort(port); err != nil {
		return apierr.Unprocessable("invalid_listen_port", err.Error())
	}
	return nil
}

// PortInUse reports whether a route service already holds protocol/port on the server.
func PortInUse(ctx context.Context, q *db.Queries, serverID uuid.UUID, protocol string, port int) (bool, error) {
	_, err := q.GetRoutePortHolder(ctx, db.GetRoutePortHolderParams{ServerID: serverID, Protocol: protocol, ListenPort: int32(port)})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check route port: %w", err)
	}
	return true, nil
}

// PortRouteInput describes a TCP/UDP exposure of a managed service's port.
type PortRouteInput struct {
	EnvironmentID   uuid.UUID
	TargetServiceID uuid.UUID
	Name            string
	Protocol        string // tcp | udp
	ListenPort      int    // host port on the target's server
	TargetPort      int    // container port of the target service
	OpenToInternet  bool
	AllowCIDRs      []string
	DenyCIDRs       []string
	PublicDatabase  bool // marks the route as a database's public port (needs an allow-list)
}

// AddPortRouteTx stages a route service that exposes TargetPort of a managed
// service on ListenPort of its server, inside the caller's transaction. The
// caller owns events after commit. It requires proxy:write, audits
// (`route.create`), registers the listen port and returns *PortInUseError on a
// conflict. The route is staged: it takes effect when the service is deployed.
func AddPortRouteTx(ctx context.Context, tx pgx.Tx, in PortRouteInput) (uuid.UUID, error) {
	q := db.New(tx)
	env, err := services.LoadEnvironment(ctx, q, policy.ScopeProxyWrite, in.EnvironmentID)
	if err != nil {
		return uuid.Nil, err
	}
	if in.Protocol != agentprotocol.ProtoTCP && in.Protocol != agentprotocol.ProtoUDP {
		return uuid.Nil, apierr.Unprocessable("invalid_config", "protocol must be tcp or udp")
	}
	if err := ValidateListenPort(in.ListenPort); err != nil {
		return uuid.Nil, err
	}
	target, err := loadTarget(ctx, q, env, in.TargetServiceID)
	if err != nil {
		return uuid.Nil, err
	}
	if target.Service.ServerID == nil {
		return uuid.Nil, apierr.Unprocessable("no_server", "assign "+target.Service.Name+" to a server before exposing a port")
	}
	srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *target.Service.ServerID})
	if err != nil {
		return uuid.Nil, fmt.Errorf("load server: %w", err)
	}
	if err := domains.RequireProxyV2(&srv); err != nil {
		return uuid.Nil, err
	}
	cfg := services.RouteConfig{
		Protocol: in.Protocol, ListenPort: in.ListenPort, OpenToInternet: in.OpenToInternet, AllowCIDRs: in.AllowCIDRs,
		DenyCIDRs: in.DenyCIDRs, PublicDatabase: in.PublicDatabase,
		Upstreams: []services.RouteUpstream{{ServiceID: &in.TargetServiceID, Port: in.TargetPort}},
	}
	if err := validateRouteConfig(cfg); err != nil {
		return uuid.Nil, err
	}
	name := in.Name
	if name == "" {
		name = fmt.Sprintf("%s %s :%d", target.Service.Name, in.Protocol, in.ListenPort)
	}
	row, err := insertRouteService(ctx, tx, env, name, "", cfg, srv.ID)
	if err != nil {
		return uuid.Nil, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Action: "route.create", ResourceType: "service", ResourceID: row.ID.String(), ProjectID: &env.Env.ProjectID,
		Metadata: map[string]any{
			"service": row.Slug, "protocol": in.Protocol, "listenPort": in.ListenPort, "target": target.Service.Slug,
			"targetPort": in.TargetPort, "openToInternet": in.OpenToInternet, "server": srv.Name,
		},
	}); err != nil {
		return uuid.Nil, err
	}
	return row.ID, nil
}

// loadTarget loads a managed service of env that a route may point at.
func loadTarget(ctx context.Context, q *db.Queries, env services.EnvScope, id uuid.UUID) (services.Scope, error) {
	sc, err := services.LoadScope(ctx, q, policy.ScopeProjectsRead, id)
	if err != nil {
		return services.Scope{}, err
	}
	if sc.EnvironmentID != env.Env.ID {
		return services.Scope{}, apierr.NotFound("service")
	}
	if sc.Service.Kind == services.KindRoute {
		return services.Scope{}, apierr.Unprocessable("invalid_upstream", "a route cannot point at another route service")
	}
	return sc, nil
}

// registerPort records the listen port of a tcp/udp route service. A unique
// violation (race, or a stale holder) becomes *PortInUseError.
func registerPort(ctx context.Context, q *db.Queries, serviceID, serverID uuid.UUID, cfg services.RouteConfig) error {
	if cfg.Protocol != agentprotocol.ProtoTCP && cfg.Protocol != agentprotocol.ProtoUDP {
		return nil
	}
	holder, err := q.GetRoutePortHolder(ctx, db.GetRoutePortHolderParams{ServerID: serverID, Protocol: cfg.Protocol, ListenPort: int32(cfg.ListenPort)})
	switch {
	case err == nil && holder.ServiceID != serviceID:
		return &PortInUseError{Protocol: cfg.Protocol, Port: cfg.ListenPort, ServerID: serverID, HeldBy: holder.ServiceName}
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("check route port: %w", err)
	}
	if err := q.DeleteRoutePort(ctx, serviceID); err != nil {
		return fmt.Errorf("clear route port: %w", err)
	}
	err = q.InsertRoutePort(ctx, db.InsertRoutePortParams{ServiceID: serviceID, ServerID: serverID, Protocol: cfg.Protocol, ListenPort: int32(cfg.ListenPort)})
	if services.IsUniqueViolation(err) {
		return &PortInUseError{Protocol: cfg.Protocol, Port: cfg.ListenPort, ServerID: serverID}
	}
	if err != nil {
		return fmt.Errorf("register route port: %w", err)
	}
	return nil
}

// PortRouteUpdate changes the listen port and the client rules of a tcp route
// created by AddPortRouteTx.
type PortRouteUpdate struct {
	ListenPort     int
	AllowCIDRs     []string
	DenyCIDRs      []string
	OpenToInternet bool
}

// UpdatePortRouteTx stages new listen-port and access rules on a tcp route
// service, inside the caller's transaction (the caller owns events after
// commit). It requires proxy:write, audits (`route.update`) and returns the
// updated route's scope and the config before the change. Nothing changes on
// the server until the route is deployed.
func UpdatePortRouteTx(ctx context.Context, tx pgx.Tx, routeID uuid.UUID, in PortRouteUpdate) (updated services.Scope, before services.RouteConfig, err error) {
	q := db.New(tx)
	sc, err := services.LoadScope(ctx, q, policy.ScopeProjectsWrite, routeID)
	if err != nil {
		return services.Scope{}, services.RouteConfig{}, err
	}
	if err := policy.Require(ctx, policy.ScopeProxyWrite, &sc.ProjectID); err != nil {
		return services.Scope{}, services.RouteConfig{}, err
	}
	old, err := routeOf(sc.Service)
	if err != nil {
		return services.Scope{}, services.RouteConfig{}, err
	}
	if sc.Service.ServerID == nil {
		return services.Scope{}, services.RouteConfig{}, apierr.Unprocessable("no_server", "the route has no server")
	}
	srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *sc.Service.ServerID})
	if err != nil {
		return services.Scope{}, services.RouteConfig{}, fmt.Errorf("load server: %w", err)
	}
	if err := domains.RequireProxyV2(&srv); err != nil {
		return services.Scope{}, services.RouteConfig{}, err
	}
	next := old
	next.ListenPort, next.AllowCIDRs, next.DenyCIDRs, next.OpenToInternet = in.ListenPort, in.AllowCIDRs, in.DenyCIDRs, in.OpenToInternet
	if err := ValidateListenPort(next.ListenPort); err != nil {
		return services.Scope{}, services.RouteConfig{}, err
	}
	if err := validateRouteConfig(next); err != nil {
		return services.Scope{}, services.RouteConfig{}, err
	}
	raw, err := routeServiceConfig(next)
	if err != nil {
		return services.Scope{}, services.RouteConfig{}, err
	}
	row, err := q.UpdateServiceCAS(ctx, db.UpdateServiceCASParams{
		ID: routeID, ExpectedGeneration: sc.Service.Generation, Name: sc.Service.Name, Config: raw, ServerID: sc.Service.ServerID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return services.Scope{}, services.RouteConfig{}, apierr.PreconditionFailed("generation_conflict", "the route changed since you loaded it")
	}
	if err != nil {
		return services.Scope{}, services.RouteConfig{}, err
	}
	if err := registerPort(ctx, q, routeID, srv.ID, next); err != nil {
		return services.Scope{}, services.RouteConfig{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Action: "route.update", ResourceType: "service", ResourceID: routeID.String(), ProjectID: &sc.ProjectID,
		Metadata: map[string]any{
			"service": sc.Service.Slug, "listenPort": next.ListenPort, "allowCidrs": len(next.AllowCIDRs), "denyCidrs": len(next.DenyCIDRs),
			"openToInternet": next.OpenToInternet, "generation": row.Generation,
		},
	}); err != nil {
		return services.Scope{}, services.RouteConfig{}, err
	}
	sc.Service = row
	return sc, old, nil
}
