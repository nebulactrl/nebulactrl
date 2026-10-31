// Package statuspages implements public status pages: the pages with their
// components, the incidents and maintenance windows posted to them, and the
// read-only view model (Service.Public) the status listener renders.
//
// Writes belong to workspace owners and admins (the admin scope); reads need
// the projects:read scope. Every change is audited in its own transaction and
// announces itself on the event bus (status_page.updated), which refreshes the
// web UI and the public cache. A change of which hostname a page is served on
// re-sends the control-plane server's route table, where the status routes
// live next to the dashboard's system route.
package statuspages

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dnscheck"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// EventPageUpdated is published after any change to a status page, its
// components, or an incident or maintenance window posted to it. ResourceID is
// the page (empty when the change touched no page): consumers that cache the
// public view invalidate everything on it.
const EventPageUpdated = "status_page.updated"

// Audit actions.
const (
	ActionPageCreate       = "status_page.create"
	ActionPageUpdate       = "status_page.update"
	ActionPageDelete       = "status_page.delete"
	ActionPageComponents   = "status_page.components"
	ActionIncidentCreate   = "status_incident.create"
	ActionIncidentUpdate   = "status_incident.update"
	ActionIncidentPost     = "status_incident.post"
	ActionIncidentDelete   = "status_incident.delete"
	ActionMaintenanceNew   = "maintenance_window.create"
	ActionMaintenanceEdit  = "maintenance_window.update"
	ActionMaintenanceStop  = "maintenance_window.cancel"
	ActionMaintenanceTrash = "maintenance_window.delete"

	resourcePage        = "status_page"
	resourceIncident    = "status_incident"
	resourceMaintenance = "maintenance_window"
)

// RoutePusher re-sends a server's route table inside tx (proxyserver.Service).
type RoutePusher interface {
	PushRoutes(ctx context.Context, tx pgx.Tx, serverID uuid.UUID) error
}

// DNS keeps the DNS records of a page's hostname (cloudflare.Service): the
// page's hostname is matched against the team's Cloudflare zones and its
// records follow the control-plane server. Both hooks run inside the
// transaction of the change and do nothing without a Cloudflare connection.
type DNS interface {
	// StatusPageHostChanged follows a page's hostname being set, changed or
	// removed; fresh says the page had no hostname before.
	StatusPageHostChanged(ctx context.Context, tx pgx.Tx, pageID uuid.UUID, fresh bool) error
	// StatusPageRemoved follows pages of the workspace being deleted.
	StatusPageRemoved(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error
}

// Resolver looks up the addresses of a hostname. dnscheck.Resolver (the
// default) asks the zone's authoritative nameservers, so a cached "no such
// name" on the host does not show; net.DefaultResolver satisfies it too.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Deps are the collaborators of a Service. Pool may be nil for OpenAPI
// generation only; Bus, Routes, DNS and Resolver are optional.
type Deps struct {
	Pool     *pgxpool.Pool
	Bus      *events.Bus
	Routes   RoutePusher
	DNS      DNS
	Resolver Resolver // default dnscheck.New()
	Logger   *slog.Logger
}

// Service implements the status page use cases.
type Service struct {
	pool     *pgxpool.Pool
	q        *db.Queries
	bus      *events.Bus
	routes   RoutePusher
	dns      DNS
	resolver Resolver
	log      *slog.Logger
	now      func() time.Time
	// started is when this Service was created: a server not heard from since
	// before then counts as silent from then on (see publicComponentState).
	started time.Time
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Resolver == nil {
		d.Resolver = dnscheck.New()
	}
	s := &Service{pool: d.Pool, bus: d.Bus, routes: d.Routes, dns: d.DNS, resolver: d.Resolver, log: d.Logger.With("component", "statuspages"), now: time.Now, started: time.Now()}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	return s
}

// reader authorizes a read (projects:read) and returns the caller's workspace.
func (s *Service) reader(ctx context.Context) (uuid.UUID, *auth.Principal, error) {
	if err := policy.Require(ctx, policy.ScopeProjectsRead, nil); err != nil {
		return uuid.Nil, nil, err
	}
	p, _ := auth.PrincipalFrom(ctx)
	return p.WorkspaceID, p, nil
}

// writer authorizes a write. Status pages are public statements about the whole
// workspace: the admin scope (owners and admins) is required, and a token
// restricted to some projects never holds enough of the workspace to make them.
func (s *Service) writer(ctx context.Context) (uuid.UUID, *auth.Principal, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return uuid.Nil, nil, err
	}
	p, _ := auth.PrincipalFrom(ctx)
	if p.Restricted() {
		return uuid.Nil, nil, apierr.Forbidden()
	}
	return p.WorkspaceID, p, nil
}

func userID(p *auth.Principal) *uuid.UUID {
	if p.UserID == uuid.Nil {
		return nil
	}
	id := p.UserID
	return &id
}

// publish announces a change of the pages. Without pages it still sends one
// event (no resource) so lists refresh. Every event names the team ws, which
// owns the pages, so a page that is gone (or has no id) still reaches only it.
func (s *Service) publish(ws uuid.UUID, pageIDs ...uuid.UUID) {
	if s.bus == nil {
		return
	}
	stamp := ws.String()
	if len(pageIDs) == 0 {
		s.bus.Publish(events.Event{Type: EventPageUpdated, WorkspaceID: stamp})
		return
	}
	seen := make(map[uuid.UUID]bool, len(pageIDs))
	for _, id := range pageIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		s.bus.Publish(events.Event{Type: EventPageUpdated, ResourceID: id.String(), WorkspaceID: stamp})
	}
}

// fail turns the error of a transaction into the API error: API errors pass,
// a unique violation of a page's hostname or slug becomes its 409, anything
// else is wrapped with op.
func fail(op string, err error) error {
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae
	}
	if c, ok := uniqueConstraint(err); ok {
		switch {
		case strings.Contains(c, "hostname"):
			return apierr.Conflict("hostname_conflict", "the hostname is not available")
		case strings.Contains(c, "slug"):
			return apierr.Conflict("slug_conflict", "another status page already uses this slug")
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}

// pushRoutes re-sends the control-plane server's route table inside tx. It does
// nothing without a control-plane server (a database-only setup) or pusher.
func (s *Service) pushRoutes(ctx context.Context, tx pgx.Tx) error {
	if s.routes == nil {
		return nil
	}
	srv, err := db.New(tx).GetControlPlaneServer(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load control-plane server: %w", err)
	}
	return s.routes.PushRoutes(ctx, tx, srv.ID)
}

// uniqueConstraint returns the violated constraint of a unique violation.
func uniqueConstraint(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName, true
	}
	return "", false
}

// cleanLine trims s and checks it is one line of printable text of min..max
// characters. field names it in the error (code invalid_<field>).
func cleanLine(field, s string, min, max int) (string, error) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	label := strings.ReplaceAll(field, "_", " ")
	switch {
	case n < min:
		return "", apierr.Unprocessable("invalid_"+field, label+" is required")
	case n > max:
		return "", apierr.Unprocessable("invalid_"+field, fmt.Sprintf("%s must be at most %d characters", label, max))
	case strings.ContainsFunc(s, unicode.IsControl):
		return "", apierr.Unprocessable("invalid_"+field, label+" must not contain control characters")
	}
	return s, nil
}

// cleanText is cleanLine for plain text with line breaks (CRLF becomes LF; tabs
// stay; other control characters are refused).
func cleanText(field, s string, min, max int) (string, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	n := utf8.RuneCountInString(s)
	label := strings.ReplaceAll(field, "_", " ")
	switch {
	case n < min:
		return "", apierr.Unprocessable("invalid_"+field, label+" is required")
	case n > max:
		return "", apierr.Unprocessable("invalid_"+field, fmt.Sprintf("%s must be at most %d characters", label, max))
	case strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }):
		return "", apierr.Unprocessable("invalid_"+field, label+" must not contain control characters")
	}
	return s, nil
}

// dedupe returns ids without repeats, in order.
func dedupe(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// workspaceServices requires every id to be a live service of the workspace
// and returns them by id.
func workspaceServices(ctx context.Context, q *db.Queries, ws uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]db.ListWorkspaceServicesByIDsRow, error) {
	out := make(map[uuid.UUID]db.ListWorkspaceServicesByIDsRow, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.ListWorkspaceServicesByIDs(ctx, db.ListWorkspaceServicesByIDsParams{Ids: ids, WorkspaceID: ws})
	if err != nil {
		return nil, fmt.Errorf("load services: %w", err)
	}
	for _, r := range rows {
		out[r.ID] = r
	}
	for _, id := range ids {
		if _, ok := out[id]; !ok {
			return nil, apierr.Unprocessable("unknown_service", "service "+id.String()+" does not exist in this workspace")
		}
	}
	return out, nil
}

// workspacePages requires every id to be a status page of the workspace.
func workspacePages(ctx context.Context, q *db.Queries, ws uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	found, err := q.ListWorkspaceStatusPageIDs(ctx, db.ListWorkspaceStatusPageIDsParams{Ids: ids, WorkspaceID: ws})
	if err != nil {
		return fmt.Errorf("load status pages: %w", err)
	}
	have := make(map[uuid.UUID]bool, len(found))
	for _, id := range found {
		have[id] = true
	}
	for _, id := range ids {
		if !have[id] {
			return apierr.Unprocessable("unknown_page", "status page "+id.String()+" does not exist in this workspace")
		}
	}
	return nil
}
