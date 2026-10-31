package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// A hostname in Cloudflare DNS belongs to an owner. Two kinds of owner have one:
// a service domain (its records point to the server of the service, or to its
// tunnel) and a status page (its records point to the control-plane server). Both
// share one DNS sync, one record plan (BuildPlan), one set of settings (manage,
// proxied, origin lock opt-in) and one origin lock; what differs is behind the
// owner interface: where the settings live, which records are wanted, how the
// lock reaches the route table and what is audited.

// OwnerKind says what kind of object a DNS name belongs to.
type OwnerKind string

// Owner kinds. The value is stored on tracked records and used as the audit resource type.
const (
	OwnerDomain     OwnerKind = "domain"
	OwnerStatusPage OwnerKind = "status_page"
)

// dnsName is a hostname NebulaCtrl keeps in DNS for one owner, with the
// owner's DNS settings and the last evaluated state.
type dnsName struct {
	Kind      OwnerKind
	ID        uuid.UUID
	Workspace uuid.UUID
	Project   *uuid.UUID // audit scope of the owner, nil when it has none
	Host      string
	// RuleKind is the kind of the route rule that serves the name (serve or
	// redirect): a redirect cannot be origin-locked.
	RuleKind string
	// Served is whether a route for the name exists right now: always for a
	// domain, only while a status page is published.
	Served bool

	ZoneID, ZoneName string
	Proxied          bool // orange cloud
	OriginLock       bool // opted in to the Cloudflare origin lock (applies while proxied)
	Status           string
	Message          string
	Error            string

	scope *services.Scope // domains: the service the name serves
}

// startsProxied reports whether a name that first lands in a zone starts
// proxied (orange cloud). Wildcard names start DNS only: Cloudflare proxies a
// wildcard record on Enterprise plans only. It is a default, never a rule: the
// proxy setting stays the person's choice, and a name that already exists keeps
// the setting it has.
func startsProxied(host string) bool {
	return !strings.HasPrefix(host, "*.")
}

// wildcardProxyNote is the one-line reason a wildcard name starts DNS only.
const wildcardProxyNote = "Wildcard names start DNS only: Cloudflare proxies a wildcard record on Enterprise plans only."

// lockRow identifies the zone (and team) of a name eligible for the origin lock.
type lockRow struct {
	Workspace uuid.UUID
	ZoneID    string
}

// owner is what the DNS sync needs of the kind of object that owns a name.
type owner interface {
	kind() OwnerKind
	// audit says how the owner's changes are recorded: the resource type and
	// the action of a settings change.
	resource() string
	settingsAction() string
	// load returns the owner's name, nil when it (or its hostname) is gone.
	load(ctx context.Context, id uuid.UUID) (*dnsName, error)
	// want returns the records the name should have, and whether they are the
	// server's tunnel CNAME. Empty: nothing can be created.
	want(ctx context.Context, n *dnsName) (targets []Target, tunnel bool, err error)
	// setMatch stores the zone of the name; a zero zone means none. The
	// settings of the name stay as they are.
	setMatch(ctx context.Context, q *db.Queries, id uuid.UUID, z Zone) error
	// setFirstMatch stores the first zone of the name: its records are managed
	// from now on and it starts with the given proxy setting.
	setFirstMatch(ctx context.Context, q *db.Queries, id uuid.UUID, z Zone, proxied bool) error
	setState(ctx context.Context, q *db.Queries, id uuid.UUID, status, message string) error
	setError(ctx context.Context, q *db.Queries, id uuid.UUID, msg string) error
	saveSettings(ctx context.Context, q *db.Queries, id uuid.UUID, in SettingsInput) error
	// lockRow reports whether the name is eligible for the origin lock right
	// now (opted in, proxied, with a proxied record of ours).
	lockRow(ctx context.Context, n *dnsName) (lockRow, bool, error)
	// push re-sends the route table that carries the name's lock inside tx.
	push(ctx context.Context, tx pgx.Tx, n *dnsName) error
	// failure is the notification of a sync that gave up.
	failure(n *dnsName, err error) notifications.Event
}

// ownerOf returns the owner implementation of a kind.
func (s *Service) ownerOf(k OwnerKind) owner {
	if k == OwnerStatusPage {
		return pageOwner{s}
	}
	return domainOwner{s}
}

// ---- service domains ----

type domainOwner struct{ s *Service }

func (domainOwner) kind() OwnerKind        { return OwnerDomain }
func (domainOwner) resource() string       { return string(OwnerDomain) }
func (domainOwner) settingsAction() string { return "domain.dns.update" }

func domainName(d db.Domain, sc services.Scope) *dnsName {
	project := sc.ProjectID
	return &dnsName{
		Kind: OwnerDomain, ID: d.ID, Workspace: sc.WorkspaceID, Project: &project, Host: d.Hostname, RuleKind: d.Kind, Served: true,
		ZoneID: d.CfZoneID, ZoneName: d.CfZoneName, Proxied: d.CfProxied, OriginLock: d.CfOriginLock,
		Status: d.CfStatus, Message: d.CfMessage, Error: d.CfError, scope: &sc,
	}
}

func (o domainOwner) load(ctx context.Context, id uuid.UUID) (*dnsName, error) {
	dom, err := o.s.q.GetDomainByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get domain: %w", err)
	}
	sc, err := services.LoadScopeInternal(ctx, o.s.q, dom.ServiceID)
	if err != nil {
		if ae, ok := apierr.As(err); ok && ae.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return domainName(dom, sc), nil
}

// targetFor is the record a service on srv should be reachable through.
func (s *Service) targetFor(ctx context.Context, srv *db.Server, proxied bool) (want []Target, tunnel bool, err error) {
	if srv == nil {
		return nil, false, nil
	}
	if t := s.tunnelTargets(); t != nil {
		cname, ok, err := t.TunnelTarget(ctx, srv.ID)
		if err != nil {
			return nil, false, fmt.Errorf("tunnel target: %w", err)
		}
		if ok && cname != "" {
			return []Target{TargetForCNAME(cname)}, true, nil
		}
	}
	if srv.PublicIp != nil {
		if t, ok := TargetForIP(*srv.PublicIp, proxied); ok {
			return []Target{t}, false, nil
		}
	}
	return nil, false, nil
}

func (o domainOwner) want(ctx context.Context, n *dnsName) ([]Target, bool, error) {
	var srv *db.Server
	if n.scope != nil && n.scope.Service.ServerID != nil {
		row, err := o.s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *n.scope.Service.ServerID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, false, fmt.Errorf("load server: %w", err)
		}
		if err == nil {
			srv = &row
		}
	}
	return o.s.targetFor(ctx, srv, n.Proxied)
}

func (domainOwner) setFirstMatch(ctx context.Context, q *db.Queries, id uuid.UUID, z Zone, proxied bool) error {
	return q.SetDomainCloudflareZone(ctx, db.SetDomainCloudflareZoneParams{ID: id, ZoneID: z.ID, ZoneName: z.Name, Proxied: proxied})
}

func (domainOwner) setMatch(ctx context.Context, q *db.Queries, id uuid.UUID, z Zone) error {
	return q.SetDomainCloudflareMatch(ctx, db.SetDomainCloudflareMatchParams{ID: id, ZoneID: z.ID, ZoneName: z.Name})
}

func (domainOwner) setState(ctx context.Context, q *db.Queries, id uuid.UUID, status, message string) error {
	return q.SetDomainCloudflareState(ctx, db.SetDomainCloudflareStateParams{ID: id, Status: status, Message: message})
}

func (domainOwner) setError(ctx context.Context, q *db.Queries, id uuid.UUID, msg string) error {
	return q.SetDomainCloudflareError(ctx, db.SetDomainCloudflareErrorParams{ID: id, Error: msg})
}

func (domainOwner) saveSettings(ctx context.Context, q *db.Queries, id uuid.UUID, in SettingsInput) error {
	if _, err := q.SetDomainCloudflareSettings(ctx, db.SetDomainCloudflareSettingsParams{ID: id, Proxied: in.Proxied}); err != nil {
		return err
	}
	if in.OriginLock != nil {
		if _, err := q.SetDomainOriginLock(ctx, db.SetDomainOriginLockParams{ID: id, Enabled: *in.OriginLock}); err != nil {
			return err
		}
	}
	return nil
}

func (o domainOwner) lockRow(ctx context.Context, n *dnsName) (lockRow, bool, error) {
	rows, err := o.s.q.ListOriginLockDomains(ctx, []uuid.UUID{n.ID})
	if err != nil || len(rows) == 0 {
		return lockRow{}, false, err
	}
	return lockRow{Workspace: rows[0].WorkspaceID, ZoneID: rows[0].ZoneID}, true, nil
}

func (o domainOwner) push(ctx context.Context, tx pgx.Tx, n *dnsName) error {
	return o.s.pushDomainInTx(ctx, tx, n.ID)
}

func (domainOwner) failure(n *dnsName, err error) notifications.Event {
	ev := notifications.Event{
		Type: notifications.TypeDNSSyncFailed, Severity: notifications.SeverityError, WorkspaceID: n.Workspace,
		Title:  "DNS sync failed for " + n.Host,
		Body:   "NebulaCtrl could not update the Cloudflare DNS record of " + n.Host + ": " + errorText(err),
		Fields: map[string]string{"Hostname": n.Host},
	}
	if n.scope != nil {
		projectID, serviceID := n.scope.ProjectID, n.scope.Service.ID
		ev.ProjectID, ev.ServiceID, ev.ServerID = &projectID, &serviceID, n.scope.Service.ServerID
	}
	return ev
}

// ---- status pages ----

type pageOwner struct{ s *Service }

func (pageOwner) kind() OwnerKind        { return OwnerStatusPage }
func (pageOwner) resource() string       { return string(OwnerStatusPage) }
func (pageOwner) settingsAction() string { return "status_page.dns.update" }

func pageName(p db.StatusPage) *dnsName {
	host := ""
	if p.Hostname != nil {
		host = *p.Hostname
	}
	return &dnsName{
		Kind: OwnerStatusPage, ID: p.ID, Workspace: p.WorkspaceID, Host: host, RuleKind: agentprotocol.DomainServe, Served: p.Published,
		ZoneID: p.CfZoneID, ZoneName: p.CfZoneName, Proxied: p.CfProxied, OriginLock: p.CfOriginLock,
		Status: p.CfStatus, Message: p.CfMessage, Error: p.CfError,
	}
}

// load returns a page that has a hostname: without one there is nothing to keep in DNS.
func (o pageOwner) load(ctx context.Context, id uuid.UUID) (*dnsName, error) {
	p, err := o.s.q.GetStatusPageByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get status page: %w", err)
	}
	if p.Hostname == nil || *p.Hostname == "" {
		return nil, nil
	}
	return pageName(p), nil
}

// want points a page at the public addresses of the control-plane server: an
// A record per IPv4 address, an AAAA record per IPv6 address. A tunnel is never
// used: the tunnel's ingress carries service hostnames only.
func (o pageOwner) want(ctx context.Context, n *dnsName) ([]Target, bool, error) {
	srv, err := o.s.q.GetControlPlaneServer(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load control-plane server: %w", err)
	}
	if srv.PublicIp == nil {
		return nil, false, nil
	}
	return TargetsForIPs(*srv.PublicIp, n.Proxied), false, nil
}

func (pageOwner) setFirstMatch(ctx context.Context, q *db.Queries, id uuid.UUID, z Zone, proxied bool) error {
	return q.SetStatusPageCloudflareZone(ctx, db.SetStatusPageCloudflareZoneParams{ID: id, ZoneID: z.ID, ZoneName: z.Name, Proxied: proxied})
}

func (pageOwner) setMatch(ctx context.Context, q *db.Queries, id uuid.UUID, z Zone) error {
	return q.SetStatusPageCloudflareMatch(ctx, db.SetStatusPageCloudflareMatchParams{ID: id, ZoneID: z.ID, ZoneName: z.Name})
}

func (pageOwner) setState(ctx context.Context, q *db.Queries, id uuid.UUID, status, message string) error {
	return q.SetStatusPageCloudflareState(ctx, db.SetStatusPageCloudflareStateParams{ID: id, Status: status, Message: message})
}

func (pageOwner) setError(ctx context.Context, q *db.Queries, id uuid.UUID, msg string) error {
	return q.SetStatusPageCloudflareError(ctx, db.SetStatusPageCloudflareErrorParams{ID: id, Error: msg})
}

func (pageOwner) saveSettings(ctx context.Context, q *db.Queries, id uuid.UUID, in SettingsInput) error {
	if _, err := q.SetStatusPageCloudflareSettings(ctx, db.SetStatusPageCloudflareSettingsParams{ID: id, Proxied: in.Proxied}); err != nil {
		return err
	}
	if in.OriginLock != nil {
		if _, err := q.SetStatusPageOriginLock(ctx, db.SetStatusPageOriginLockParams{ID: id, Enabled: *in.OriginLock}); err != nil {
			return err
		}
	}
	return nil
}

func (o pageOwner) lockRow(ctx context.Context, n *dnsName) (lockRow, bool, error) {
	rows, err := o.s.q.ListOriginLockStatusPages(ctx, []string{n.Host})
	if err != nil {
		return lockRow{}, false, err
	}
	for _, r := range rows {
		if r.PageID == n.ID {
			return lockRow{Workspace: r.WorkspaceID, ZoneID: r.ZoneID}, true, nil
		}
	}
	return lockRow{}, false, nil
}

func (o pageOwner) push(ctx context.Context, tx pgx.Tx, _ *dnsName) error {
	return o.s.pushControlPlaneInTx(ctx, tx)
}

func (pageOwner) failure(n *dnsName, err error) notifications.Event {
	return notifications.Event{
		Type: notifications.TypeDNSSyncFailed, Severity: notifications.SeverityError, WorkspaceID: n.Workspace,
		Title:  "DNS sync failed for " + n.Host,
		Body:   "NebulaCtrl could not update the Cloudflare DNS record of the status page " + n.Host + ": " + errorText(err),
		Fields: map[string]string{"Hostname": n.Host},
		URL:    "/status-pages",
	}
}
