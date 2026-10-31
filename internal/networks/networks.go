// Package networks owns team shared networks: named Docker networks on one
// server that services of any project of the team attach to explicitly, with
// the alias <service-slug>.<env-slug>.<project-slug>. Attachments are staged
// (they advance the service generation) and frozen into the release snapshot
// when the service is deployed. Nothing is ever shared across teams.
package networks

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Audit actions and the event type of the networks domain.
const (
	ActionCreate = "network.create"
	ActionDelete = "network.delete"
	ActionAttach = "network.attach"
	ActionDetach = "network.detach"

	// EventUpdated invalidates the network lists in the web app.
	EventUpdated = "network.updated"
)

// Service implements the shared-network use cases.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
}

// NewService returns a Service (arguments may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus) *Service {
	s := &Service{pool: pool, bus: bus}
	if pool != nil {
		s.q = db.New(pool)
	}
	return s
}

// AttachedService is a service attached to a shared network.
type AttachedService struct {
	ServiceID     uuid.UUID `json:"serviceId"`
	Name          string    `json:"name"`
	Slug          string    `json:"slug"`
	Kind          string    `json:"kind"`
	ProjectID     uuid.UUID `json:"projectId"`
	ProjectName   string    `json:"projectName"`
	EnvironmentID uuid.UUID `json:"environmentId"`
	EnvName       string    `json:"envName"`
	EnvSlug       string    `json:"envSlug"`
	// Alias is the service's hostname on the network.
	Alias string `json:"alias"`
}

// NetworkView is the API representation of a shared network.
type NetworkView struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	ServerID   uuid.UUID `json:"serverId"`
	ServerName string    `json:"serverName"`
	// DockerName is the network's name on the server (nebulactrl-shared-<id8>).
	DockerName string            `json:"dockerName"`
	Services   []AttachedService `json:"services" doc:"Services attached (staged or deployed); services of projects the token cannot see are left out"`
	CreatedAt  time.Time         `json:"createdAt"`
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,62}$`)

// ValidName checks a network name (1-63 characters: letters, digits, space, '.', '_' and '-').
func ValidName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !nameRe.MatchString(name) {
		return "", apierr.Unprocessable("invalid_network_name", "network names are 1-63 characters: letters, digits, spaces, '.', '_' and '-'")
	}
	return name, nil
}

func toView(n db.SharedNetwork, serverName string, attached []AttachedService) NetworkView {
	if attached == nil {
		attached = []AttachedService{}
	}
	return NetworkView{
		ID: n.ID, Name: n.Name, ServerID: n.ServerID, ServerName: serverName,
		DockerName: agentprotocol.SharedNetworkName(n.ID.String()), Services: attached, CreatedAt: n.CreatedAt,
	}
}

func (s *Service) publish(workspaceID, networkID uuid.UUID) {
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: EventUpdated, ResourceID: networkID.String(), WorkspaceID: workspaceID.String()})
	}
}

// requireTeamAdmin authorizes servers:write without a project restriction:
// shared networks belong to the team, not to a project.
func requireTeamAdmin(ctx context.Context, scope string) error {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return err
	}
	if p, ok := auth.PrincipalFrom(ctx); ok && p.Restricted() {
		return apierr.Forbidden()
	}
	return nil
}

// load returns a network of the caller's team (404 for another team's).
func (s *Service) load(ctx context.Context, q *db.Queries, id uuid.UUID) (db.SharedNetwork, error) {
	n, err := q.GetSharedNetwork(ctx, db.GetSharedNetworkParams{ID: id, WorkspaceID: policy.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.SharedNetwork{}, apierr.NotFound("network")
	}
	if err != nil {
		return db.SharedNetwork{}, fmt.Errorf("get network: %w", err)
	}
	return n, nil
}

// views renders networks with their servers and attached services.
func (s *Service) views(ctx context.Context, q *db.Queries, rows []db.SharedNetwork, networkID *uuid.UUID) ([]NetworkView, error) {
	if len(rows) == 0 {
		return []NetworkView{}, nil
	}
	serverIDs := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		serverIDs = append(serverIDs, r.ServerID)
	}
	servers, err := q.LoadServerRows(ctx, serverIDs)
	if err != nil {
		return nil, fmt.Errorf("load servers: %w", err)
	}
	serverName := make(map[uuid.UUID]string, len(servers))
	for _, sv := range servers {
		serverName[sv.ID] = sv.Name
	}
	atts, err := q.ListNetworkAttachments(ctx, db.ListNetworkAttachmentsParams{WorkspaceID: policy.WorkspaceID(ctx), NetworkID: networkID})
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	p, _ := auth.PrincipalFrom(ctx)
	byNet := map[uuid.UUID][]AttachedService{}
	for _, a := range atts {
		if p != nil && !p.CanSeeProject(a.ProjectID) {
			continue
		}
		byNet[a.NetworkID] = append(byNet[a.NetworkID], AttachedService{
			ServiceID: a.ServiceID, Name: a.ServiceName, Slug: a.ServiceSlug, Kind: a.ServiceKind,
			ProjectID: a.ProjectID, ProjectName: a.ProjectName, EnvironmentID: a.EnvironmentID, EnvName: a.EnvName, EnvSlug: a.EnvSlug,
			Alias: a.Alias,
		})
	}
	out := make([]NetworkView, len(rows))
	for i, r := range rows {
		out[i] = toView(r, serverName[r.ServerID], byNet[r.ID])
	}
	return out, nil
}

// List returns the team's shared networks, optionally only those on one server.
func (s *Service) List(ctx context.Context, serverID *uuid.UUID) ([]NetworkView, error) {
	if err := policy.Require(ctx, policy.ScopeServersRead, nil); err != nil {
		return nil, err
	}
	rows, err := s.q.ListSharedNetworks(ctx, db.ListSharedNetworksParams{WorkspaceID: policy.WorkspaceID(ctx), ServerID: serverID})
	if err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
	}
	return s.views(ctx, s.q, rows, nil)
}

// Get returns one shared network.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (NetworkView, error) {
	if err := policy.Require(ctx, policy.ScopeServersRead, nil); err != nil {
		return NetworkView{}, err
	}
	n, err := s.load(ctx, s.q, id)
	if err != nil {
		return NetworkView{}, err
	}
	vs, err := s.views(ctx, s.q, []db.SharedNetwork{n}, &n.ID)
	if err != nil {
		return NetworkView{}, err
	}
	return vs[0], nil
}

// Create adds a shared network on a server of the team (servers:write).
func (s *Service) Create(ctx context.Context, name string, serverID uuid.UUID) (NetworkView, error) {
	if err := requireTeamAdmin(ctx, policy.ScopeServersWrite); err != nil {
		return NetworkView{}, err
	}
	name, err := ValidName(name)
	if err != nil {
		return NetworkView{}, err
	}
	ws := policy.WorkspaceID(ctx)
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID, WorkspaceID: &ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return NetworkView{}, apierr.Unprocessable("server_not_found", "the selected server does not exist")
	}
	if err != nil {
		return NetworkView{}, fmt.Errorf("load server: %w", err)
	}
	if srv.Status == "revoked" {
		return NetworkView{}, apierr.Unprocessable("server_unavailable", fmt.Sprintf("server %q is revoked", srv.Name))
	}
	var createdBy *uuid.UUID
	if p, ok := auth.PrincipalFrom(ctx); ok && p.UserID != uuid.Nil {
		createdBy = &p.UserID
	}
	var row db.SharedNetwork
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).InsertSharedNetwork(ctx, db.InsertSharedNetworkParams{WorkspaceID: ws, ServerID: serverID, Name: name, CreatedBy: createdBy})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionCreate, ResourceType: "network", ResourceID: row.ID.String(),
			Metadata: map[string]any{"name": name, "server": srv.Name, "serverId": serverID.String()},
		})
	})
	if services.IsUniqueViolation(err) {
		return NetworkView{}, apierr.Conflict("network_name_taken", "a shared network with that name already exists")
	}
	if err != nil {
		return NetworkView{}, fmt.Errorf("create network: %w", err)
	}
	s.publish(row.WorkspaceID, row.ID)
	return toView(row, srv.Name, nil), nil
}

// Delete removes a shared network that no service is attached to
// (409 network_in_use otherwise). The agent drops the empty Docker network on
// its next reconcile.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	if err := requireTeamAdmin(ctx, policy.ScopeServersWrite); err != nil {
		return err
	}
	n, err := s.load(ctx, s.q, id)
	if err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.PurgeDeletedNetworkAttachments(ctx, id); err != nil {
			return err
		}
		used, err := q.CountNetworkAttachments(ctx, id)
		if err != nil {
			return err
		}
		if used > 0 {
			return apierr.Conflict("network_in_use", fmt.Sprintf("%d service(s) are attached to this network; detach them first", used)).
				WithDetails(map[string]any{"attached": used})
		}
		if _, err := q.DeleteSharedNetwork(ctx, db.DeleteSharedNetworkParams{ID: id, WorkspaceID: n.WorkspaceID}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionDelete, ResourceType: "network", ResourceID: id.String(),
			Metadata: map[string]any{"name": n.Name, "serverId": n.ServerID.String()},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("delete network: %w", err)
	}
	s.publish(n.WorkspaceID, id)
	return nil
}

// Attach stages the attachment of a service to a shared network (deployments:write).
// 422 network_wrong_server when the service runs on another server than the
// network, 422 egress_blocked while the service blocks outbound internet.
func (s *Service) Attach(ctx context.Context, serviceID, networkID uuid.UUID) (services.NetworkRef, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, serviceID)
	if err != nil {
		return services.NetworkRef{}, err
	}
	var ref services.NetworkRef
	var after func(context.Context)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		ref, after, err = s.AttachTx(ctx, tx, sc, networkID)
		return err
	})
	if err != nil {
		return services.NetworkRef{}, err
	}
	after(ctx)
	return ref, nil
}

// AttachTx is the transaction-bound core of Attach. sc is the service's scope,
// loaded by the caller on db.New(tx); deployments:write on the service's
// project is required here as in Attach. Every query uses tx. Attaching an
// already attached service is a no-op (no generation bump, no audit entry). The
// returned function (never nil) publishes the service and network events and
// MUST be run only after the transaction committed.
func (s *Service) AttachTx(ctx context.Context, tx pgx.Tx, sc services.Scope, networkID uuid.UUID) (services.NetworkRef, func(context.Context), error) {
	noop := func(context.Context) {}
	if err := policy.Require(ctx, policy.ScopeDeploymentsWrite, &sc.ProjectID); err != nil {
		return services.NetworkRef{}, noop, err
	}
	if err := services.RequireContainer(sc, "shared networks"); err != nil {
		return services.NetworkRef{}, noop, err
	}
	q := db.New(tx)
	n, err := s.load(ctx, q, networkID)
	if err != nil {
		return services.NetworkRef{}, noop, err
	}
	serviceID := sc.Service.ID
	fail := func(err error) (services.NetworkRef, func(context.Context), error) {
		if _, ok := apierr.As(err); ok {
			return services.NetworkRef{}, noop, err
		}
		return services.NetworkRef{}, noop, fmt.Errorf("attach network: %w", err)
	}
	svc, err := q.LockServiceRow(ctx, serviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(apierr.NotFound("service"))
	}
	if err != nil {
		return fail(fmt.Errorf("lock service: %w", err))
	}
	if svc.ServerID == nil {
		return fail(apierr.Unprocessable("service_no_server", "assign the service to the network's server first"))
	}
	if *svc.ServerID != n.ServerID {
		return fail(services.WrongServer(svc.Name, n.Name))
	}
	if services.DecodeConfig(svc.Config).BlockEgress {
		return fail(services.NetworkConflict())
	}
	added, err := q.AttachServiceNetwork(ctx, db.AttachServiceNetworkParams{ServiceID: serviceID, NetworkID: networkID})
	if err != nil {
		return fail(fmt.Errorf("attach: %w", err))
	}
	ref := services.NetworkRef{ID: n.ID, Name: n.Name, ServerID: n.ServerID, Alias: aliasOf(sc)}
	after := func(context.Context) {
		services.PublishService(s.bus, services.EventServiceUpdated, sc)
		s.publish(sc.WorkspaceID, networkID)
	}
	if added == 0 {
		return ref, after, nil // already attached
	}
	if _, err := q.BumpServiceGeneration(ctx, serviceID); err != nil {
		return fail(err)
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Action: ActionAttach, ResourceType: "network", ResourceID: networkID.String(), ProjectID: &sc.ProjectID,
		Metadata: map[string]any{"service": sc.Service.Slug, "network": n.Name, "alias": ref.Alias},
	}); err != nil {
		return fail(err)
	}
	return ref, after, nil
}

// Detach stages the removal of an attachment (deployments:write).
func (s *Service) Detach(ctx context.Context, serviceID, networkID uuid.UUID) error {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, serviceID)
	if err != nil {
		return err
	}
	var after func(context.Context)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		after, err = s.DetachTx(ctx, tx, sc, networkID)
		return err
	})
	if err != nil {
		return err
	}
	after(ctx)
	return nil
}

// DetachTx is the transaction-bound core of Detach. sc is the service's scope,
// loaded by the caller on db.New(tx); deployments:write on the service's
// project is required here as in Detach. 404 network_attachment when the
// service is not attached. The returned function (never nil) publishes the
// service and network events and MUST be run only after the transaction
// committed.
func (s *Service) DetachTx(ctx context.Context, tx pgx.Tx, sc services.Scope, networkID uuid.UUID) (func(context.Context), error) {
	noop := func(context.Context) {}
	if err := policy.Require(ctx, policy.ScopeDeploymentsWrite, &sc.ProjectID); err != nil {
		return noop, err
	}
	q := db.New(tx)
	n, err := s.load(ctx, q, networkID)
	if err != nil {
		return noop, err
	}
	fail := func(err error) (func(context.Context), error) {
		if _, ok := apierr.As(err); ok {
			return noop, err
		}
		return noop, fmt.Errorf("detach network: %w", err)
	}
	removed, err := q.DetachServiceNetwork(ctx, db.DetachServiceNetworkParams{ServiceID: sc.Service.ID, NetworkID: networkID})
	if err != nil {
		return fail(err)
	}
	if removed == 0 {
		return fail(apierr.NotFound("network attachment"))
	}
	if _, err := q.BumpServiceGeneration(ctx, sc.Service.ID); err != nil {
		return fail(err)
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Action: ActionDetach, ResourceType: "network", ResourceID: networkID.String(), ProjectID: &sc.ProjectID,
		Metadata: map[string]any{"service": sc.Service.Slug, "network": n.Name},
	}); err != nil {
		return fail(err)
	}
	return func(context.Context) {
		services.PublishService(s.bus, services.EventServiceUpdated, sc)
		s.publish(sc.WorkspaceID, networkID)
	}, nil
}

// ResolveByName finds the workspace's shared network called name (matched
// case-insensitively, as the unique index does), optionally restricted to one
// server. 422 unknown_network when none matches, 422 ambiguous_reference when
// several do (the same name on several servers and no serverID filter).
func (s *Service) ResolveByName(ctx context.Context, q *db.Queries, workspaceID uuid.UUID, name string, serverID *uuid.UUID) (db.SharedNetwork, error) {
	name = strings.TrimSpace(name)
	rows, err := q.ListSharedNetworks(ctx, db.ListSharedNetworksParams{WorkspaceID: workspaceID, ServerID: serverID})
	if err != nil {
		return db.SharedNetwork{}, fmt.Errorf("list networks: %w", err)
	}
	var match []db.SharedNetwork
	for _, r := range rows {
		if strings.EqualFold(r.Name, name) {
			match = append(match, r)
		}
	}
	switch len(match) {
	case 0:
		return db.SharedNetwork{}, apierr.Unprocessable("unknown_network", fmt.Sprintf("no shared network named %q", name))
	case 1:
		return match[0], nil
	}
	return db.SharedNetwork{}, apierr.Unprocessable("ambiguous_reference", fmt.Sprintf("%d shared networks are named %q: pick the one on the service's server", len(match), name))
}

// aliasOf is the service's hostname on a shared network.
func aliasOf(sc services.Scope) string {
	return sc.Service.Slug + "." + sc.EnvSlug + "." + sc.ProjectSlug
}
