package databases

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/proxyroutes"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// A public database port is a TCP route service (kind=route, tcp, marked
// publicDatabase) that publishes the database's port on its server through a
// static proxy entrypoint. It always has an IP allow-list, and a list that
// admits every address (0.0.0.0/0, ::/0) needs the database's slug typed as a
// confirmation. TLS is never terminated by the proxy (the route is a raw TCP
// pass-through), so a database that enforces TLS itself keeps doing so
// end-to-end; the managed engines do not enable TLS on their own.

const publicPortTLSNote = "The proxy forwards raw TCP and never terminates TLS. The managed database does not enable TLS by itself, " +
	"so traffic on this port is not encrypted: restrict it to trusted networks or tunnel it through a VPN."

// PublicPortView is the state of a database's public port.
type PublicPortView struct {
	Enabled       bool       `json:"enabled"`
	RouteID       *uuid.UUID `json:"routeId,omitempty" doc:"The route service that holds the port"`
	ListenPort    int        `json:"listenPort,omitempty"`
	SuggestedPort int        `json:"suggestedPort" doc:"A free port to start from when enabling"`
	AllowCIDRs    []string   `json:"allowCidrs"`
	DenyCIDRs     []string   `json:"denyCidrs"`
	OpenToAll     bool       `json:"openToAll" doc:"The allow-list admits every address (confirmed with the database slug)"`
	Staged        bool       `json:"staged" doc:"The route has changes that are not deployed yet"`
	Deployed      bool       `json:"deployed" doc:"The proxy publishes the port"`
	NewEntrypoint bool       `json:"newEntrypoint" doc:"The proxy does not listen on this port yet: applying it recreates the proxy container (about a second of dropped connections)"`
	ServerID      *uuid.UUID `json:"serverId,omitempty"`
	ServerName    string     `json:"serverName,omitempty"`
	Host          string     `json:"host,omitempty" doc:"The address clients connect to (the server's public IP); empty when the server has none"`
	TLS           string     `json:"tls" enum:"passthrough" doc:"The proxy never terminates TLS on this port"`
	Warnings      []string   `json:"warnings"`
}

// PublicPortInput enables or changes a database's public port.
type PublicPortInput struct {
	ListenPort  int
	AllowCIDRs  []string
	DenyCIDRs   []string
	ConfirmOpen string // the database slug, required when AllowCIDRs admits every address
}

// publicRoute finds the route service that publishes the database's public port.
func (s *Service) publicRoute(ctx context.Context, q *db.Queries, sc services.Scope) (*db.Service, services.RouteConfig, error) {
	rows, err := q.ListAllEnvironmentServices(ctx, sc.EnvironmentID)
	if err != nil {
		return nil, services.RouteConfig{}, fmt.Errorf("list services: %w", err)
	}
	for _, r := range rows {
		if r.Kind != services.KindRoute {
			continue
		}
		rc := services.DecodeConfig(r.Config).Route
		if rc != nil && rc.PublicDatabase && len(rc.Upstreams) == 1 && rc.Upstreams[0].ServiceID != nil && *rc.Upstreams[0].ServiceID == sc.Service.ID {
			r := r
			return &r, *rc, nil
		}
	}
	return nil, services.RouteConfig{}, nil
}

func (s *Service) publicPortView(ctx context.Context, q *db.Queries, sc services.Scope, spec engines.Spec) (PublicPortView, error) {
	v := PublicPortView{AllowCIDRs: []string{}, DenyCIDRs: []string{}, TLS: "passthrough", Warnings: []string{}}
	route, rc, err := s.publicRoute(ctx, q, sc)
	if err != nil {
		return v, err
	}
	serverID := sc.Service.ServerID
	if route != nil {
		serverID = route.ServerID
	}
	var srv *db.Server
	if serverID != nil {
		row, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *serverID})
		if err != nil && !isNoRows(err) {
			return v, fmt.Errorf("load server: %w", err)
		}
		if err == nil {
			srv = &row
			v.ServerID, v.ServerName = &row.ID, row.Name
			if row.PublicIp != nil {
				v.Host = *row.PublicIp
			}
		}
	}
	if srv != nil {
		taken, err := q.ListServerRoutePorts(ctx, srv.ID)
		if err != nil {
			return v, fmt.Errorf("list route ports: %w", err)
		}
		v.SuggestedPort = suggestPort(spec.Port(), func(p int) bool {
			return slices.ContainsFunc(taken, func(t db.ListServerRoutePortsRow) bool {
				return t.Protocol == agentprotocol.ProtoTCP && int(t.ListenPort) == p
			})
		})
	} else {
		v.SuggestedPort = spec.Port()
	}
	if route == nil {
		return v, nil
	}
	v.Enabled, v.RouteID = true, &route.ID
	v.ListenPort = rc.ListenPort
	v.AllowCIDRs, v.DenyCIDRs = nonNil(rc.AllowCIDRs), nonNil(rc.DenyCIDRs)
	v.OpenToAll = agentprotocol.AnyOpenCIDR(rc.AllowCIDRs)
	v.Staged = route.Generation > route.AppliedGeneration
	v.Deployed = route.CurrentDeploymentID != nil
	if srv != nil {
		applied, err := q.ListServerAppliedRoutePorts(ctx, &srv.ID)
		if err != nil {
			return v, fmt.Errorf("list applied ports: %w", err)
		}
		v.NewEntrypoint = !slices.ContainsFunc(applied, func(p db.ListServerAppliedRoutePortsRow) bool {
			return p.Protocol == agentprotocol.ProtoTCP && int(p.ListenPort) == rc.ListenPort
		})
	}
	v.Warnings = append(v.Warnings, publicPortTLSNote)
	if v.OpenToAll {
		v.Warnings = append(v.Warnings, "Every address on the internet can reach this database's login. Keep the password strong and prefer an allow-list of known networks.")
	}
	if v.Host == "" {
		v.Warnings = append(v.Warnings, "The server has no public IP set, so the connection string cannot be shown. Add it in the server's settings.")
	}
	return v, nil
}

// Operation names of the guarded public port changes (the catalog's operation ids).
const (
	guardSetPublicPort     = "setDatabasePublicPort"
	guardDisablePublicPort = "disableDatabasePublicPort"
)

func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return slices.Clone(in)
}

// suggestPort picks the engine's own port when free, else the same number in
// the 10000s and 20000s, else the first free port above 30000.
func suggestPort(enginePort int, taken func(int) bool) int {
	for _, p := range []int{enginePort, 10000 + enginePort%10000, 20000 + enginePort%10000} {
		if p >= 1024 && p <= 65535 && agentprotocol.ValidateListenPort(p) == nil && !taken(p) {
			return p
		}
	}
	for p := 30000; p < 65535; p++ {
		if !taken(p) {
			return p
		}
	}
	return enginePort
}

// PublicPort returns the state of a managed database's public port.
func (s *Service) PublicPort(ctx context.Context, serviceID uuid.UUID) (PublicPortView, error) {
	sc, spec, _, err := s.managed(ctx, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return PublicPortView{}, err
	}
	return s.publicPortView(ctx, s.q, sc, spec)
}

// SetPublicPort enables the public port of a managed database or changes its
// port and rules. It stages a TCP route service (deploy it to apply). The
// allow-list is mandatory; an allow-list that admits every address needs the
// database slug as ConfirmOpen. Requires projects:write and proxy:write.
func (s *Service) SetPublicPort(ctx context.Context, serviceID uuid.UUID, in PublicPortInput) (PublicPortView, error) {
	sc, spec, _, err := s.managed(ctx, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return PublicPortView{}, err
	}
	if err := policy.Require(ctx, policy.ScopeProxyWrite, &sc.ProjectID); err != nil {
		return PublicPortView{}, err
	}
	if sc.Service.ServerID == nil {
		return PublicPortView{}, apierr.Unprocessable("no_server", "assign "+sc.Service.Name+" to a server before publishing its port")
	}
	if len(in.AllowCIDRs) == 0 {
		return PublicPortView{}, apierr.Unprocessable("allowlist_required",
			"a public database port needs an allow-list of client networks (CIDRs or addresses)")
	}
	if err := agentprotocol.ValidateAccessLists(in.AllowCIDRs, in.DenyCIDRs, true); err != nil {
		return PublicPortView{}, apierr.Unprocessable("invalid_config", err.Error())
	}
	open := agentprotocol.AnyOpenCIDR(in.AllowCIDRs)
	if open && in.ConfirmOpen != sc.Service.Slug {
		return PublicPortView{}, apierr.Unprocessable("open_confirmation_required",
			"allowing every address (0.0.0.0/0 or ::/0) needs confirmOpen set to the database slug "+sc.Service.Slug)
	}
	if err := sc.Guard(ctx, guardSetPublicPort, fmt.Sprintf("Open the public database port of %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), false,
		[]string{
			"The database is reachable from the internet on the listen port, from the allowed addresses.",
			fmt.Sprintf("Listen port: %d, allow: %s, deny: %s.", in.ListenPort, strings.Join(in.AllowCIDRs, ", "), strings.Join(in.DenyCIDRs, ", ")),
		}, in.ListenPort, strings.Join(in.AllowCIDRs, ","), strings.Join(in.DenyCIDRs, ","), in.ConfirmOpen); err != nil {
		return PublicPortView{}, err
	}

	route, _, err := s.publicRoute(ctx, s.q, sc)
	if err != nil {
		return PublicPortView{}, err
	}
	var routeID uuid.UUID
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if route == nil {
			id, err := proxyroutes.AddPortRouteTx(ctx, tx, proxyroutes.PortRouteInput{
				EnvironmentID: sc.EnvironmentID, TargetServiceID: sc.Service.ID, Name: sc.Service.Name + " public port",
				Protocol: agentprotocol.ProtoTCP, ListenPort: in.ListenPort, TargetPort: spec.Port(),
				OpenToInternet: open, AllowCIDRs: in.AllowCIDRs, DenyCIDRs: in.DenyCIDRs, PublicDatabase: true,
			})
			routeID = id
			return err
		}
		routeID = route.ID
		_, _, err := proxyroutes.UpdatePortRouteTx(ctx, tx, route.ID, proxyroutes.PortRouteUpdate{
			ListenPort: in.ListenPort, AllowCIDRs: in.AllowCIDRs, DenyCIDRs: in.DenyCIDRs, OpenToInternet: open,
		})
		return err
	})
	if err != nil {
		var pe *proxyroutes.PortInUseError
		if errors.As(err, &pe) {
			return PublicPortView{}, err
		}
		if _, ok := apierr.As(err); ok {
			return PublicPortView{}, err
		}
		return PublicPortView{}, fmt.Errorf("set public port: %w", err)
	}
	if rsc, err := services.LoadScopeInternal(ctx, s.q, routeID); err == nil {
		services.PublishService(s.bus, services.EventServiceUpdated, rsc)
	}
	s.publish(sc)
	return s.publicPortView(ctx, s.q, sc, spec)
}

// DisablePublicPort deletes the route service that publishes the database's
// port. The port closes on the server at once when the route was deployed (the
// delete queues the remaining route table).
func (s *Service) DisablePublicPort(ctx context.Context, serviceID uuid.UUID) (PublicPortView, error) {
	sc, spec, _, err := s.managed(ctx, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return PublicPortView{}, err
	}
	if err := policy.Require(ctx, policy.ScopeProxyWrite, &sc.ProjectID); err != nil {
		return PublicPortView{}, err
	}
	route, _, err := s.publicRoute(ctx, s.q, sc)
	if err != nil {
		return PublicPortView{}, err
	}
	if route == nil {
		return PublicPortView{}, apierr.NotFound("public_port")
	}
	if err := sc.Guard(ctx, guardDisablePublicPort, fmt.Sprintf("Close the public database port of %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), false,
		[]string{"Clients that connect through the public port lose access."}, route.ID); err != nil {
		return PublicPortView{}, err
	}
	// The approval covers removing the port's route service.
	ctx = prodguard.Covered(ctx)
	if err := s.svcs.Delete(ctx, route.ID, services.DeleteInput{Confirm: route.Slug}); err != nil {
		return PublicPortView{}, err
	}
	s.publish(sc)
	return s.publicPortView(ctx, s.q, sc, spec)
}

// PublicConnection is an external connection string, revealed in plain text.
type PublicConnection struct {
	Key        string    `json:"key"`
	Value      string    `json:"value"`
	Host       string    `json:"host"`
	Port       int       `json:"port"`
	RevealedAt time.Time `json:"revealedAt"`
	ExpiresAt  time.Time `json:"expiresAt" doc:"The value is meant to be shown for this long only; clients must not persist it"`
}

// RevealPublicConnection returns one derived export (DATABASE_URL, REDIS_URL, ...)
// with the server's public address and the public port instead of the private
// host. It needs deployments:write, is audited as `secret.reveal` in the same
// transaction and is never cached.
func (s *Service) RevealPublicConnection(ctx context.Context, serviceID uuid.UUID, key string) (PublicConnection, error) {
	sc, spec, _, err := s.managed(ctx, policy.ScopeDeploymentsWrite, serviceID)
	if err != nil {
		return PublicConnection{}, err
	}
	route, rc, err := s.publicRoute(ctx, s.q, sc)
	if err != nil {
		return PublicConnection{}, err
	}
	if route == nil {
		return PublicConnection{}, apierr.Conflict("public_port_disabled", "the database has no public port")
	}
	if route.ServerID == nil {
		return PublicConnection{}, apierr.Unprocessable("no_server", "the public port has no server")
	}
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *route.ServerID})
	if err != nil {
		return PublicConnection{}, fmt.Errorf("load server: %w", err)
	}
	if srv.PublicIp == nil || *srv.PublicIp == "" {
		return PublicConnection{}, apierr.Unprocessable("no_public_address",
			"server "+srv.Name+" has no public IP: set it in the server's settings so the connection string can name it")
	}
	host := *srv.PublicIp
	vars, err := s.resolved(ctx, s.q, sc)
	if err != nil {
		return PublicConnection{}, err
	}
	var value string
	found := false
	for _, e := range engines.Exports(spec, host, rc.ListenPort, func(k string) (string, bool) { v, ok := vars[k]; return v, ok }) {
		// An empty key means the engine's connection URL (the first *_URL export).
		if e.Key == key || (key == "" && strings.HasSuffix(e.Key, "_URL")) {
			key, value, found = e.Key, e.Value, true
			break
		}
	}
	if !found {
		return PublicConnection{}, apierr.NotFound("export")
	}
	now := time.Now().UTC()
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Keys only: the value never reaches the audit log.
		return audit.Record(ctx, tx, audit.Entry{
			Action: "secret.reveal", ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "key": key, "public": true, "host": host, "port": rc.ListenPort},
		})
	})
	if err != nil {
		return PublicConnection{}, fmt.Errorf("audit reveal: %w", err)
	}
	return PublicConnection{Key: key, Value: value, Host: host, Port: rc.ListenPort, RevealedAt: now, ExpiresAt: now.Add(RevealTTL)}, nil
}
