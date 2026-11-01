package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// RecordView is a DNS record of a hostname as the API shows it.
type RecordView struct {
	Type    string `json:"type" enum:"A,AAAA,CNAME"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	Owned   bool   `json:"owned" doc:"Created by NebulaCtrl (carries its ownership marker)"`
}

// DNSStatus is the live DNS state of a domain or status page hostname: what is
// in the zone, what NebulaCtrl wants there, and what is wrong.
type DNSStatus struct {
	Configured bool   `json:"configured" doc:"The team has a Cloudflare connection"`
	Status     string `json:"status" enum:",managed,present,conflict,wrong_target,missing,not_in_zone" doc:"conflict: a record NebulaCtrl did not create differs or blocks; it stays as it is until it is replaced"`
	Message    string `json:"message"`
	Error      string `json:"error" doc:"The last failed sync, empty when the last sync worked"`
	ZoneID     string `json:"zoneId"`
	ZoneName   string `json:"zoneName"`
	Manage     bool   `json:"manage" doc:"Read-only. NebulaCtrl keeps this hostname's records in sync; always true for a hostname in a zone of the connected account"`
	Proxied    bool   `json:"proxied" doc:"The proxy setting of the hostname (orange cloud)"`
	// EffectiveProxied differs from Proxied for tunnel records, which are always proxied.
	EffectiveProxied bool `json:"effectiveProxied"`
	Tunnel           bool `json:"tunnel" doc:"The server is behind a Cloudflare Tunnel: the record is a proxied CNAME to it (never for a status page)"`
	// NeedsAdopt is set when records NebulaCtrl did not create differ from the wanted ones or block them (status conflict); replacing them is an explicit action (fix with adopt).
	NeedsAdopt bool        `json:"needsAdopt"`
	Desired    *RecordView `json:"desired" doc:"The first record NebulaCtrl wants; null when nothing can be created"`
	// DesiredRecords is every record NebulaCtrl wants: a status page on a server with an IPv4 and an IPv6 address wants an A and an AAAA record.
	DesiredRecords []RecordView `json:"desiredRecords"`
	Records        []RecordView `json:"records" doc:"A, AAAA and CNAME records of the hostname"`
	SSLMode        string       `json:"sslMode" enum:",off,flexible,full,strict" doc:"The zone's SSL/TLS mode; empty when unknown"`
	Warnings       []string     `json:"warnings"`
	CheckedAt      *time.Time   `json:"checkedAt"`
	// OriginLock is the origin lock of the hostname: layers, state and the toggle.
	OriginLock OriginLockView `json:"originLock"`
}

// runOpts steer one evaluation of a name.
type runOpts struct {
	apply bool // change the zone to match (otherwise classify only)
	adopt bool // replace the records NebulaCtrl did not create with its own
}

type runResult struct {
	n          *dnsName
	conn       *db.CloudflareConnection
	zone       Zone
	inZone     bool
	want       []Target
	tunnel     bool
	records    []Existing
	plan       Plan
	applied    bool
	status     string
	message    string
	privateIPs bool
}

// domainWorkspace returns the team of a domain (uuid.Nil when its service is gone).
func (s *Service) domainWorkspace(ctx context.Context, q *db.Queries, dom db.Domain) (uuid.UUID, error) {
	sc, err := services.LoadScopeInternal(ctx, q, dom.ServiceID)
	if err != nil {
		if ae, ok := apierr.As(err); ok && ae.Status == http.StatusNotFound {
			return uuid.Nil, nil
		}
		return uuid.Nil, err
	}
	return sc.WorkspaceID, nil
}

// owns reports whether NebulaCtrl owns a record: its comment carries the
// marker and the record is tracked, or the marker names a domain or status page
// of this team.
func (s *Service) owns(ctx context.Context, ws uuid.UUID, tracked map[string]bool, r Record) bool {
	id, ok := ParseMarker(r.Comment)
	if !ok {
		return false
	}
	if tracked[r.ID] {
		return true
	}
	if d, err := s.q.GetDomainByID(ctx, id); err == nil {
		w, err := s.domainWorkspace(ctx, s.q, d)
		return err == nil && w == ws
	}
	if p, err := s.q.GetStatusPageByID(ctx, id); err == nil {
		return p.WorkspaceID == ws
	}
	return false
}

// notFoundOf is the 404 of an owner that no longer exists.
func notFoundOf(kind OwnerKind) error {
	if kind == OwnerStatusPage {
		return apierr.NotFound("status page")
	}
	return apierr.NotFound("domain")
}

// run evaluates one name and, with o.apply, changes the zone to match. It
// returns (nil, nil) when the owner (or its hostname) no longer exists.
func (s *Service) run(ctx context.Context, kind OwnerKind, id uuid.UUID, o runOpts) (*runResult, error) {
	ow := s.ownerOf(kind)
	n, err := ow.load(ctx, id)
	if err != nil || n == nil {
		return nil, err
	}
	ws := n.Workspace
	res := &runResult{n: n}
	conn, ok, err := s.hasConnection(ctx, s.q, ws)
	if err != nil {
		return nil, err
	}
	if !ok {
		return res, nil
	}
	res.conn = &conn
	zone, in := MatchZone(decodeZones(conn.Zones), n.Host)
	if !in {
		if n.ZoneID != "" || n.Status != StatusNotInZone {
			if err := ow.setMatch(ctx, s.q, n.ID, Zone{}); err != nil {
				return nil, err
			}
			if err := ow.setState(ctx, s.q, n.ID, StatusNotInZone, notInZoneMessage); err != nil {
				return nil, err
			}
		}
		res.status, res.message = StatusNotInZone, notInZoneMessage
		return res, nil
	}
	res.zone, res.inZone = zone, true
	if n.ZoneID != zone.ID || n.ZoneName != zone.Name {
		if n.ZoneID == "" {
			// The name's first zone: its records are managed from now on.
			err = ow.setFirstMatch(ctx, s.q, n.ID, zone, startsProxied(n.Host))
			n.Proxied = startsProxied(n.Host)
		} else {
			err = ow.setMatch(ctx, s.q, n.ID, zone)
		}
		if err != nil {
			return nil, err
		}
		n.ZoneID, n.ZoneName = zone.ID, zone.Name
	}
	unlock := s.hostLock(ws, n.Host)
	defer unlock()

	want, tunnel, err := ow.want(ctx, n)
	if err != nil {
		return nil, err
	}
	res.want, res.tunnel = want, tunnel
	c, err := s.clientOf(conn)
	if err != nil {
		return nil, err
	}
	recs, err := c.ListRecords(ctx, zone.ID, n.Host)
	if err != nil {
		return res, s.failName(ctx, res, err)
	}
	trackedRows, err := s.q.ListCloudflareRecordsForHost(ctx, db.ListCloudflareRecordsForHostParams{WorkspaceID: ws, ZoneID: zone.ID, Hostname: n.Host})
	if err != nil {
		return nil, fmt.Errorf("list tracked records: %w", err)
	}
	tracked := make(map[string]bool, len(trackedRows))
	for _, r := range trackedRows {
		tracked[r.RecordID] = true
	}
	for _, r := range recs {
		res.records = append(res.records, Existing{Record: r, Owned: s.owns(ctx, ws, tracked, r)})
	}
	res.plan = BuildPlan(PlanInput{OwnerID: n.ID, Host: n.Host, Want: want, Records: res.records, Adopt: o.adopt})
	res.status, res.message = res.plan.Current, res.plan.Message
	for _, t := range want {
		if t.Proxied && t.IsPrivate() {
			res.privateIPs = true
		}
	}
	if o.apply && len(res.plan.Actions) > 0 {
		if err := s.execute(ctx, c, ow, n, zone, res.plan.Actions); err != nil {
			return res, s.failName(ctx, res, err)
		}
		res.applied = true
		res.status = res.plan.After
		res.message = fmt.Sprintf("%s points to %s through a record managed by NebulaCtrl.", n.Host, contentList(want))
	}
	if o.apply && res.status == StatusManaged && !res.tunnel && n.Proxied && n.OriginLock {
		// Authenticated Origin Pulls for the zone. A failure must not fail the
		// DNS sync: the name stays locked by IP and the next sync tries again.
		if _, _, err := s.ensureZoneAOP(ctx, c, ws, zone); err != nil {
			s.log.WarnContext(ctx, "authenticated origin pulls check failed", "owner_kind", string(n.Kind), "owner_id", n.ID.String(), "zone", zone.Name, "err", errorText(err))
		}
	}
	if err := s.storeState(ctx, ow, n, res); err != nil {
		return nil, fmt.Errorf("store dns state: %w", err)
	}
	return res, nil
}

// failName records a failed call on the owner and returns the error.
func (s *Service) failName(ctx context.Context, res *runResult, err error) error {
	msg := errorText(err)
	if e := s.ownerOf(res.n.Kind).setError(ctx, s.q, res.n.ID, msg); e != nil {
		s.log.WarnContext(ctx, "could not store dns error", "owner_kind", string(res.n.Kind), "owner_id", res.n.ID.String(), "err", e)
	}
	return err
}

// errorText is the short, token-free text of a failure shown on the domain.
func errorText(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Error()
	}
	var pe *apierr.Error
	if errors.As(err, &pe) {
		return pe.Message
	}
	return "Cloudflare could not be reached"
}

func recordMeta(a Action, host, zone string, rec Record) map[string]any {
	md := map[string]any{"hostname": host, "zone": zone, "recordId": rec.ID, "type": rec.Type, "content": rec.Content, "proxied": rec.Proxied}
	if a.Kind == ActionUpdate {
		md["from"] = map[string]any{"type": a.Record.Type, "content": a.Record.Content, "proxied": a.Record.Proxied}
		if a.Adopt {
			md["adopted"] = true
		}
	}
	if a.Kind == ActionDelete && a.Adopt {
		md["replaced"] = true
	}
	return md
}

// execute applies the actions in order. Each Cloudflare call is followed by the
// local bookkeeping and the audit entry in one transaction.
func (s *Service) execute(ctx context.Context, c *Client, ow owner, n *dnsName, zone Zone, acts []Action) error {
	for _, a := range acts {
		switch a.Kind {
		case ActionDelete:
			if err := c.DeleteRecord(ctx, zone.ID, a.Record.ID); err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
				if err := db.New(tx).DeleteCloudflareRecord(ctx, db.DeleteCloudflareRecordParams{ZoneID: zone.ID, RecordID: a.Record.ID}); err != nil {
					return err
				}
				return audit.Record(ctx, tx, audit.Entry{
					Action: "dns.record.delete", ResourceType: ow.resource(), ResourceID: n.ID.String(), ProjectID: n.Project, WorkspaceID: n.Workspace,
					Metadata: recordMeta(a, n.Host, zone.Name, a.Record),
				})
			})
			if err != nil {
				return fmt.Errorf("record delete: %w", err)
			}
		case ActionUpdate, ActionCreate:
			var rec Record
			var err error
			action := "dns.record.update"
			if a.Kind == ActionCreate {
				action = "dns.record.create"
				rec, err = c.CreateRecord(ctx, zone.ID, a.Input)
			} else {
				rec, err = c.UpdateRecord(ctx, zone.ID, a.Record.ID, a.Input)
			}
			if err != nil {
				return err
			}
			if rec.ID == "" {
				if a.Kind == ActionUpdate {
					rec.ID = a.Record.ID
				} else {
					return &APIError{Status: http.StatusBadGateway, Message: "Cloudflare did not return the record id"}
				}
			}
			stored := Record{ID: rec.ID, Type: a.Input.Type, Content: a.Input.Content, Proxied: a.Input.Proxied}
			err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
				if err := db.New(tx).UpsertCloudflareRecord(ctx, db.UpsertCloudflareRecordParams{
					ZoneID: zone.ID, RecordID: rec.ID, WorkspaceID: n.Workspace, OwnerKind: string(n.Kind), OwnerID: n.ID, Hostname: n.Host,
					Type: stored.Type, Content: stored.Content, Proxied: stored.Proxied,
				}); err != nil {
					return err
				}
				// The audit entry first: the origin lock's route table is numbered
				// last (db.Queries.NumberRouteTable).
				if err := audit.Record(ctx, tx, audit.Entry{
					Action: action, ResourceType: ow.resource(), ResourceID: n.ID.String(), ProjectID: n.Project, WorkspaceID: n.Workspace,
					Metadata: recordMeta(a, n.Host, zone.Name, stored),
				}); err != nil {
					return err
				}
				if stored.Proxied || (a.Kind == ActionUpdate && a.Record.Proxied) {
					// The record just became (or stopped being) proxied: the origin lock follows in the same transaction.
					return ow.push(ctx, tx, n)
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("record bookkeeping: %w", err)
			}
		}
	}
	return nil
}

// SyncDomain makes the zone match the domain (the River job's body). It does
// nothing for domains in no zone.
func (s *Service) SyncDomain(ctx context.Context, domainID uuid.UUID) error {
	_, err := s.run(ctx, OwnerDomain, domainID, runOpts{apply: true})
	return err
}

// SyncStatusPage makes the zone match the status page's hostname (the River
// job's body). It does nothing for pages with no hostname or in no zone.
func (s *Service) SyncStatusPage(ctx context.Context, pageID uuid.UUID) error {
	_, err := s.run(ctx, OwnerStatusPage, pageID, runOpts{apply: true})
	return err
}

// ---- API: status, settings, fix ----

// Operation names of the guarded DNS changes (the catalog's operation ids).
const (
	guardUpdateDomainDNS = "updateDomainDns"
	guardFixDomainDNS    = "fixDomainDns"
)

// loadDomain authorizes scope on a domain's service (404 for other teams).
func (s *Service) loadDomain(ctx context.Context, scope string, domainID uuid.UUID) (db.Domain, services.Scope, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.Domain{}, services.Scope{}, err
	}
	dom, err := s.q.GetDomainByID(ctx, domainID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Domain{}, services.Scope{}, apierr.NotFound("domain")
	}
	if err != nil {
		return db.Domain{}, services.Scope{}, fmt.Errorf("get domain: %w", err)
	}
	sc, err := services.LoadScope(ctx, s.q, scope, dom.ServiceID)
	if err != nil {
		return db.Domain{}, services.Scope{}, err
	}
	return dom, sc, nil
}

// Status classifies the domain's DNS against the zone right now (no changes).
func (s *Service) Status(ctx context.Context, domainID uuid.UUID) (DNSStatus, error) {
	if _, _, err := s.loadDomain(ctx, policy.ScopeProjectsRead, domainID); err != nil {
		return DNSStatus{}, err
	}
	return s.statusOf(ctx, OwnerDomain, domainID)
}

// statusOf classifies one name against the zone right now (no changes).
func (s *Service) statusOf(ctx context.Context, kind OwnerKind, id uuid.UUID) (DNSStatus, error) {
	res, err := s.run(ctx, kind, id, runOpts{})
	if err != nil && res == nil {
		return DNSStatus{}, mapAPIError(err)
	}
	if res == nil {
		return DNSStatus{}, notFoundOf(kind)
	}
	return s.statusView(ctx, res, err), nil
}

// SettingsInput changes the DNS settings of a domain or status page; nil fields stay as they are.
type SettingsInput struct {
	Proxied *bool
	// OriginLock opts the name in (true) or out (false) of the Cloudflare origin lock.
	OriginLock *bool
}

// SetSettings changes the proxy setting and the origin lock opt-in of a domain,
// then syncs. A failed sync is reported in the status (and retried by a job);
// the setting is kept.
func (s *Service) SetSettings(ctx context.Context, domainID uuid.UUID, in SettingsInput) (DNSStatus, error) {
	dom, sc, err := s.loadDomain(ctx, policy.ScopeProjectsWrite, domainID)
	if err != nil {
		return DNSStatus{}, err
	}
	if in.Proxied != nil || (in.OriginLock != nil && !*in.OriginLock) {
		args := []any{domainID, dom.Hostname}
		effects := []string{"The change reaches Cloudflare at once; turning the origin lock off exposes the origin past Cloudflare."}
		if in.Proxied != nil {
			args = append(args, "proxied", *in.Proxied)
			effects = append(effects, fmt.Sprintf("Proxied: %t.", *in.Proxied))
		}
		if in.OriginLock != nil {
			args = append(args, "originLock", *in.OriginLock)
			effects = append(effects, fmt.Sprintf("Origin lock: %t.", *in.OriginLock))
		}
		if err := sc.Guard(ctx, guardUpdateDomainDNS, fmt.Sprintf("Change the DNS proxy or origin lock of %s in production (%s / %s)", dom.Hostname, sc.ProjectName, sc.EnvName), false, effects, args...); err != nil {
			return DNSStatus{}, err
		}
	}
	return s.setSettings(ctx, domainName(dom, sc), in)
}

// setSettings stores the settings of a name and re-sends the route table that
// carries its lock, then syncs.
func (s *Service) setSettings(ctx context.Context, n *dnsName, in SettingsInput) (DNSStatus, error) {
	ow := s.ownerOf(n.Kind)
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := ow.saveSettings(ctx, db.New(tx), n.ID, in); err != nil {
			return err
		}
		md := map[string]any{"hostname": n.Host}
		if in.Proxied != nil {
			md["proxied"] = *in.Proxied
		}
		if in.OriginLock != nil {
			md["originLock"] = *in.OriginLock
		}
		if err := audit.Record(ctx, tx, audit.Entry{
			Action: ow.settingsAction(), ResourceType: ow.resource(), ResourceID: n.ID.String(), ProjectID: n.Project, WorkspaceID: n.Workspace, Metadata: md,
		}); err != nil {
			return err
		}
		if in.Proxied != nil || in.OriginLock != nil {
			// Lock changes reach the proxy before the DNS record changes, so a name
			// that stops being proxied is unlocked first.
			return ow.push(ctx, tx, n)
		}
		return nil
	})
	if err != nil {
		return DNSStatus{}, fmt.Errorf("update dns settings: %w", err)
	}
	res, err := s.run(ctx, n.Kind, n.ID, runOpts{apply: true})
	if res == nil {
		if err != nil {
			return DNSStatus{}, mapAPIError(err)
		}
		return DNSStatus{}, notFoundOf(n.Kind)
	}
	if err != nil {
		s.retryLater(ctx, n.Kind, n.ID)
	}
	return s.statusView(ctx, res, err), nil
}

// Fix makes the zone match the domain. Without adopt, a record NebulaCtrl did
// not create is never touched (409 adoption_required); with adopt, the explicit
// replace, those records are replaced with NebulaCtrl's own and every change is
// audited. The replace needs a team admin (not restricted to projects), like the
// status page one: the records belong to the whole zone, not to a project.
func (s *Service) Fix(ctx context.Context, domainID uuid.UUID, adopt bool) (DNSStatus, error) {
	dom, sc, err := s.loadDomain(ctx, policy.ScopeProjectsWrite, domainID)
	if err != nil {
		return DNSStatus{}, err
	}
	if adopt {
		if err := requireZoneAdmin(ctx); err != nil {
			return DNSStatus{}, err
		}
		if err := sc.Guard(ctx, guardFixDomainDNS, fmt.Sprintf("Replace DNS records for %s in production (%s / %s)", dom.Hostname, sc.ProjectName, sc.EnvName), false,
			[]string{"Records NebulaCtrl did not create are replaced at once; the public name points where NebulaCtrl says."}, domainID, dom.Hostname, adopt); err != nil {
			return DNSStatus{}, err
		}
	}
	return s.fix(ctx, domainName(dom, sc), adopt)
}

// requireZoneAdmin authorizes an action on records of the whole zone: the admin
// scope and a principal not restricted to some projects.
func requireZoneAdmin(ctx context.Context) error {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return err
	}
	if p, ok := auth.PrincipalFrom(ctx); !ok || p.Restricted() {
		return apierr.Forbidden()
	}
	return nil
}

func (s *Service) fix(ctx context.Context, n *dnsName, adopt bool) (DNSStatus, error) {
	res, err := s.run(ctx, n.Kind, n.ID, runOpts{apply: true, adopt: adopt})
	if res == nil {
		if err != nil {
			return DNSStatus{}, mapAPIError(err)
		}
		return DNSStatus{}, notFoundOf(n.Kind)
	}
	switch {
	case res.conn == nil:
		return DNSStatus{}, apierr.Unprocessable("not_configured", "connect Cloudflare in Team settings → Integrations first")
	case !res.inZone:
		return DNSStatus{}, apierr.Unprocessable("not_in_zone", notInZoneMessage)
	case err != nil:
		return DNSStatus{}, mapAPIError(err)
	case res.plan.NeedsAdopt && !adopt:
		return DNSStatus{}, apierr.Conflict("adoption_required", res.plan.Message)
	}
	return s.statusView(ctx, res, nil), nil
}

// statusView builds the API status of an evaluated name. runErr is the sync
// error of the evaluation, shown as the status' error.
func (s *Service) statusView(ctx context.Context, res *runResult, runErr error) DNSStatus {
	now := s.now()
	n := res.n
	v := DNSStatus{
		Configured: res.conn != nil, Status: res.status, Message: res.message,
		ZoneID: res.zone.ID, ZoneName: res.zone.Name, Manage: res.conn != nil && res.inZone, Proxied: n.Proxied,
		EffectiveProxied: n.Proxied || res.tunnel, Tunnel: res.tunnel,
		NeedsAdopt: res.plan.NeedsAdopt, DesiredRecords: []RecordView{}, Records: []RecordView{}, Warnings: []string{}, CheckedAt: &now,
	}
	if runErr != nil {
		v.Error = errorText(runErr)
	} else if res.applied {
		v.Error = ""
	} else {
		v.Error = n.Error
	}
	if res.conn == nil {
		v.Status, v.Message = "", ""
		v.OriginLock = s.originLockView(ctx, res, "")
		return v
	}
	if !res.inZone {
		v.OriginLock = s.originLockView(ctx, res, "")
		return v
	}
	for _, r := range res.records {
		v.Records = append(v.Records, RecordView{Type: r.Type, Content: r.Content, Proxied: r.Proxied, Owned: r.Owned})
	}
	if res.applied {
		// What was there is not what is there now: show the desired records as the result.
		v.Records = v.Records[:0]
	}
	for _, t := range res.want {
		v.DesiredRecords = append(v.DesiredRecords, RecordView{Type: t.Type, Content: t.Content, Proxied: t.Proxied, Owned: true})
	}
	if len(v.DesiredRecords) > 0 {
		first := v.DesiredRecords[0]
		v.Desired = &first
		if res.applied {
			v.Records = append(v.Records, v.DesiredRecords...)
		}
	}
	if !n.Proxied && !res.tunnel && !startsProxied(n.Host) {
		v.Warnings = append(v.Warnings, wildcardProxyNote)
	}
	if res.zone.Paused {
		v.Warnings = append(v.Warnings, "The zone is paused in Cloudflare: records resolve, but traffic is not proxied.")
	}
	if res.privateIPs {
		v.Warnings = append(v.Warnings, "The server's IP is a private address; Cloudflare cannot proxy traffic to it. Use a public IP or a tunnel.")
	}
	if v.EffectiveProxied && res.conn != nil {
		if c, err := s.clientOf(*res.conn); err == nil {
			if mode, err := c.ZoneSSLMode(ctx, res.zone.ID); err == nil {
				v.SSLMode = mode
			}
		}
		v.Warnings = append(v.Warnings, sslWarnings(v.SSLMode)...)
	}
	v.OriginLock = s.originLockView(ctx, res, v.SSLMode)
	return v
}

// sslWarnings explains the SSL/TLS modes that break a proxied record.
func sslWarnings(mode string) []string {
	switch strings.ToLower(mode) {
	case "flexible":
		return []string{"The zone's SSL/TLS mode is Flexible: Cloudflare talks plain HTTP to your server, which redirects to HTTPS, so visitors get a redirect loop. Set the mode to Full (strict) in Cloudflare → SSL/TLS, or switch this domain to DNS only."}
	case "off":
		return []string{"The zone's SSL/TLS mode is Off: Cloudflare serves and requests plain HTTP only, which loops against NebulaCtrl's HTTPS redirect. Set the mode to Full (strict) in Cloudflare → SSL/TLS, or switch this domain to DNS only."}
	}
	return nil
}

// ---- collector ----

// deleteTracked removes a record NebulaCtrl created from Cloudflare and forgets
// it. A record that lost its marker is left in the zone (it is no longer ours).
func (s *Service) deleteTracked(ctx context.Context, c *Client, r db.CloudflareRecord) error {
	forget := func(deleted bool) error {
		return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			if err := db.New(tx).DeleteCloudflareRecord(ctx, db.DeleteCloudflareRecordParams{ZoneID: r.ZoneID, RecordID: r.RecordID}); err != nil {
				return err
			}
			if !deleted {
				return nil
			}
			return audit.Record(ctx, tx, audit.Entry{
				Action: "dns.record.delete", ResourceType: r.OwnerKind, ResourceID: r.OwnerID.String(), WorkspaceID: r.WorkspaceID,
				Metadata: map[string]any{"hostname": r.Hostname, "zone": r.ZoneID, "recordId": r.RecordID, "type": r.Type, "content": r.Content, "proxied": r.Proxied},
			})
		})
	}
	rec, err := c.GetRecord(ctx, r.ZoneID, r.RecordID)
	if errors.Is(err, ErrNotFound) {
		return forget(false)
	}
	if err != nil {
		return err
	}
	if _, ok := ParseMarker(rec.Comment); !ok {
		return forget(false)
	}
	if err := c.DeleteRecord(ctx, r.ZoneID, r.RecordID); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return forget(true)
}

// Collect deletes the records of hostnames no domain or status page of the team uses any more.
func (s *Service) Collect(ctx context.Context, ws uuid.UUID) error {
	conn, ok, err := s.hasConnection(ctx, s.q, ws)
	if err != nil || !ok {
		return err
	}
	orphans, err := s.q.ListOrphanCloudflareRecords(ctx, ws)
	if err != nil {
		return fmt.Errorf("list orphan records: %w", err)
	}
	if len(orphans) == 0 {
		return nil
	}
	c, err := s.clientOf(conn)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range orphans {
		func() {
			unlock := s.hostLock(ws, r.Hostname)
			defer unlock()
			if err := s.deleteTracked(ctx, c, r); err != nil {
				errs = append(errs, err)
			}
		}()
	}
	return errors.Join(errs...)
}
