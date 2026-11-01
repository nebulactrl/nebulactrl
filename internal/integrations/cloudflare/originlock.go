package cloudflare

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	_ "embed" // the pinned origin-pull CA
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/statusroute"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Cloudflare origin lock. A domain is locked when it is proxied (orange cloud),
// not served through a Cloudflare Tunnel and has not opted out. So is a
// published status page: its route on the control-plane server (statusroute) is
// locked when the page's record is proxied and the page has not opted out. The
// lock has three layers, rendered by the agent per domain (pkg/agentprotocol/cforigin.go):
//
//  1. IP lock: only Cloudflare's published ranges may connect (403 otherwise).
//  2. Authenticated Origin Pulls: the TLS handshake must present Cloudflare's
//     client certificate. Required only while the zone's AOP setting is on.
//  3. Real client IP: forwarded headers are trusted from Cloudflare's ranges.

// OriginPullCASource is where Cloudflare publishes the CA that signs the client
// certificate of (global) Authenticated Origin Pulls:
// https://developers.cloudflare.com/ssl/origin-configuration/authenticated-origin-pull/set-up/global/
const OriginPullCASource = "https://developers.cloudflare.com/ssl/static/authenticated_origin_pull_ca.pem"

// OriginPullCAFingerprint is the SHA-256 fingerprint of the pinned certificate
// (DER), the value `openssl x509 -fingerprint -sha256` prints. The PEM file's own
// SHA-256 is c14fed0ce5210db0719fea11d1f10b33750dc17d609aeaf47c75e9eff0d7b843.
// Subject: CN=origin-pull.cloudflare.net, OU=Origin Pull, O=CloudFlare, Inc.;
// valid until 2029-11-01. Downloaded 2026-10-27 from OriginPullCASource.
//
// To refresh it, download the file again, replace data/authenticated_origin_pull_ca.pem,
// run `openssl x509 -in <file> -noout -fingerprint -sha256`, and update this
// constant; TestOriginPullCAIsPinned fails when the file and the constant differ
// or the certificate is within 180 days of expiry.
const OriginPullCAFingerprint = "9a1ac2b4be15f9f27eee20a734cba4e9898f61001b3bd7c84b69b56a3e25a2b9"

//go:embed data/authenticated_origin_pull_ca.pem
var originPullCAPEM string

func init() {
	// A broken build must not ship an unpinned CA: fail at start-up, not per request.
	if err := checkOriginPullCA(originPullCAPEM); err != nil {
		panic("cloudflare: " + err.Error())
	}
}

// OriginPullCA returns the pinned Cloudflare origin-pull CA certificate (PEM).
func OriginPullCA() string { return originPullCAPEM }

func checkOriginPullCA(p string) error {
	block, _ := pem.Decode([]byte(p))
	if block == nil || block.Type != "CERTIFICATE" {
		return errors.New("the origin-pull CA is not a PEM certificate")
	}
	sum := sha256.Sum256(block.Bytes)
	if hex.EncodeToString(sum[:]) != OriginPullCAFingerprint {
		return errors.New("the origin-pull CA does not match its pinned fingerprint")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return fmt.Errorf("the origin-pull CA does not parse: %w", err)
	}
	return nil
}

// ---- policy ----

// Why a domain is not origin-locked (OriginLockView.Reason).
const (
	LockReasonNone       = ""
	LockReasonOptedOut   = "opted_out"
	LockReasonTunnel     = "tunnel"
	LockReasonNotProxied = "not_proxied"
	LockReasonNotInZone  = "not_in_zone"
	LockReasonPending    = "pending"
	LockReasonWildcard   = "wildcard"
	LockReasonRedirect   = "redirect"
)

// LockReasonNotPublished: a status page is locked once it is published (it has no route before).
const LockReasonNotPublished = "not_published"

// LockPolicy decides whether a domain is origin-locked, and says why not.
// Proxied is the proxy setting of the domain's DNS (orange cloud).
func LockPolicy(proxied, tunnel, optIn bool) (locked bool, reason string) {
	switch {
	case tunnel:
		// A tunnel has no public origin: cloudflared connects from inside.
		return false, LockReasonTunnel
	case !proxied:
		return false, LockReasonNotProxied
	case !optIn:
		return false, LockReasonOptedOut
	}
	return true, LockReasonNone
}

// lockableRule reports whether a rendered rule can carry the lock at all. The
// agent rejects the lock on tunnel domains, redirect rules and wildcard hosts
// (Traefik cannot choose a TLS option for a HostRegexp router).
func lockableRule(r agentprotocol.DomainRule, tunnel bool) (bool, string) {
	switch {
	case tunnel || r.Tunnel:
		return false, LockReasonTunnel
	case r.Kind == agentprotocol.DomainRedirect:
		return false, LockReasonRedirect
	case strings.Contains(r.Host, "*"):
		return false, LockReasonWildcard
	}
	return true, ""
}

// ruleLock is the lock state computed for the rules of one table.
type ruleLock struct {
	locked map[string]bool // host -> locked
	cert   map[string]bool // host -> requires the client certificate
}

// resolveLocks computes, per hostname, whether it is locked and whether it
// requires the client certificate. A hostname is locked only when every rule of
// it qualifies: the agent requires the rules of one host to agree, and an
// operator's opt-out wins. tunnelled marks domain ids served through a tunnel.
// A rule is either a service domain's or a status page's (its id derives from
// the page id, see statusroute.DomainID).
func resolveLocks(ctx context.Context, q *db.Queries, rules []*agentprotocol.DomainRule, tunnelled map[string]bool) (ruleLock, error) {
	out := ruleLock{locked: map[string]bool{}, cert: map[string]bool{}}
	var ids []uuid.UUID
	var hosts []string
	poisoned := map[string]bool{}
	for _, r := range rules {
		if ok, _ := lockableRule(*r, tunnelled[r.ID]); !ok {
			poisoned[r.Host] = true
			continue
		}
		if id, err := uuid.Parse(r.ID); err == nil {
			ids = append(ids, id)
			hosts = append(hosts, strings.ToLower(r.Host))
		} else {
			poisoned[r.Host] = true
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.ListOriginLockDomains(ctx, ids)
	if err != nil {
		return out, fmt.Errorf("list origin lock domains: %w", err)
	}
	aop := make(map[string]string, len(rows)) // eligible rule id -> the zone's AOP state
	for _, r := range rows {
		aop[r.DomainID.String()] = r.AopState
	}
	pages, err := q.ListOriginLockStatusPages(ctx, hosts)
	if err != nil {
		return out, fmt.Errorf("list origin lock status pages: %w", err)
	}
	for _, p := range pages {
		aop[statusroute.DomainID(p.PageID)] = p.AopState
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if poisoned[r.Host] {
			continue
		}
		state, eligible := aop[r.ID]
		if !seen[r.Host] {
			seen[r.Host] = true
			out.locked[r.Host], out.cert[r.Host] = eligible, eligible && state == AOPOn
			continue
		}
		out.locked[r.Host] = out.locked[r.Host] && eligible
		out.cert[r.Host] = out.cert[r.Host] && eligible && state == AOPOn
	}
	for h := range out.cert {
		out.cert[h] = out.cert[h] && out.locked[h]
	}
	return out, nil
}

// ApplyOriginLock marks the domains of an http route table that are locked to
// Cloudflare (DomainRule.OriginLocked / RequireClientCert) and gives every route
// with a locked domain its shared data (Route.OriginLock: Cloudflare's current
// ranges and, where a domain requires the certificate, the pinned CA). Tunnel
// flags need not be set: serverID's tunnel is looked up here (a domain served
// through the server's Cloudflare Tunnel is never locked). Routes are modified
// in place.
func ApplyOriginLock(ctx context.Context, q *db.Queries, serverID uuid.UUID, routes []agentprotocol.Route) error {
	var rules []*agentprotocol.DomainRule
	for i := range routes {
		if routes[i].IsL4() {
			continue
		}
		for j := range routes[i].Domains {
			rules = append(rules, &routes[i].Domains[j])
		}
	}
	if len(rules) == 0 {
		return nil
	}
	tunnelled, err := tunnelledRules(ctx, q, serverID, rules)
	if err != nil {
		return err
	}
	lock, err := resolveLocks(ctx, q, rules, tunnelled)
	if err != nil {
		return err
	}
	var list *IPList
	for i := range routes {
		if routes[i].IsL4() {
			continue
		}
		anyLocked, needCA := false, false
		for j := range routes[i].Domains {
			d := &routes[i].Domains[j]
			d.OriginLocked = lock.locked[d.Host]
			d.RequireClientCert = lock.cert[d.Host]
			anyLocked = anyLocked || d.OriginLocked
			needCA = needCA || d.RequireClientCert
		}
		if !anyLocked {
			routes[i].OriginLock = nil
			continue
		}
		if list == nil {
			l, err := CurrentIPList(ctx, q)
			if err != nil {
				return err
			}
			list = &l
		}
		routes[i].OriginLock = &agentprotocol.OriginLock{CloudflareCIDRs: list.CIDRs()}
		if needCA {
			routes[i].OriginLock.ClientCAPEM = OriginPullCA()
		}
	}
	return nil
}

// tunnelledRules returns the ids of the rules whose domain is served through
// serverID's tunnel (or already flagged Tunnel).
func tunnelledRules(ctx context.Context, q *db.Queries, serverID uuid.UUID, rules []*agentprotocol.DomainRule) (map[string]bool, error) {
	out := map[string]bool{}
	var ids []uuid.UUID
	for _, r := range rules {
		if r.Tunnel {
			out[r.ID] = true
		}
		if id, err := uuid.Parse(r.ID); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	tun, err := q.ListTunnelledDomainIDs(ctx, db.ListTunnelledDomainIDsParams{DomainIds: ids, ServerID: serverID})
	if err != nil {
		return nil, fmt.Errorf("list tunnelled domains: %w", err)
	}
	for _, id := range tun {
		out[id.String()] = true
	}
	return out, nil
}

// ApplyOriginLockToRules is ApplyOriginLock for the rules of one service in a
// deploy payload (the agent builds that service's own route from them). It
// returns the route data to carry next to them, nil when no rule is locked.
// serverID is the server the service runs on (tunnelled domains are skipped).
func ApplyOriginLockToRules(ctx context.Context, q *db.Queries, serverID uuid.UUID, rules []agentprotocol.DomainRule) (*agentprotocol.OriginLock, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	ptrs := make([]*agentprotocol.DomainRule, len(rules))
	for i := range rules {
		ptrs[i] = &rules[i]
	}
	tunnelled, err := tunnelledRules(ctx, q, serverID, ptrs)
	if err != nil {
		return nil, err
	}
	lock, err := resolveLocks(ctx, q, ptrs, tunnelled)
	if err != nil {
		return nil, err
	}
	anyLocked, needCA := false, false
	for i := range rules {
		rules[i].OriginLocked = lock.locked[rules[i].Host]
		rules[i].RequireClientCert = lock.cert[rules[i].Host]
		anyLocked = anyLocked || rules[i].OriginLocked
		needCA = needCA || rules[i].RequireClientCert
	}
	if !anyLocked {
		return nil, nil
	}
	list, err := CurrentIPList(ctx, q)
	if err != nil {
		return nil, err
	}
	ol := &agentprotocol.OriginLock{CloudflareCIDRs: list.CIDRs()}
	if needCA {
		ol.ClientCAPEM = OriginPullCA()
	}
	return ol, nil
}

// ---- Authenticated Origin Pulls (zone setting tls_client_auth) ----

// AOP states.
const (
	AOPOn          = "on"
	AOPOff         = "off"
	AOPUnavailable = "unavailable"
)

// Why AOP is unavailable for a zone.
const (
	AOPReasonPermission = "permission"
	AOPReasonSSLMode    = "ssl_mode"
	AOPReasonError      = "error"
)

// aopRecheck is how long a zone's "on" state is trusted before Cloudflare is asked again.
const aopRecheck = 5 * time.Minute

// AOPPermissionHint names the token permission that enables AOP. Authenticated
// Origin Pulls with Cloudflare's certificate is the `tls_client_auth` zone
// setting; changing a zone setting needs "Zone Settings Write"
// (https://developers.cloudflare.com/api/resources/zones/subresources/settings/methods/edit/).
const AOPPermissionHint = "Zone → Zone Settings → Edit"

// ZoneAOP reports whether global Authenticated Origin Pulls is on for the zone
// (GET /zones/{zone_id}/settings/tls_client_auth; needs Zone Settings Read).
func (c *Client) ZoneAOP(ctx context.Context, zoneID string) (bool, error) {
	var s struct {
		Value string `json:"value"`
	}
	if err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/settings/tls_client_auth", nil, &s); err != nil {
		return false, err
	}
	return strings.EqualFold(s.Value, "on"), nil
}

// SetZoneAOP turns global Authenticated Origin Pulls on or off for the zone
// (PATCH /zones/{zone_id}/settings/tls_client_auth {"value":"on"}: "Edit zone
// setting", https://developers.cloudflare.com/ssl/origin-configuration/authenticated-origin-pull/set-up/global/;
// needs Zone Settings Write).
func (c *Client) SetZoneAOP(ctx context.Context, zoneID string, on bool) error {
	v := "off"
	if on {
		v = "on"
	}
	return c.do(ctx, http.MethodPatch, "/zones/"+url.PathEscape(zoneID)+"/settings/tls_client_auth", map[string]string{"value": v}, nil)
}

// AOPState is what is known about a zone's Authenticated Origin Pulls.
type AOPState struct {
	State      string // on | unavailable
	ReasonCode string
	Reason     string
	// Enabled is set when this check turned the zone setting on (it was off).
	Enabled bool
}

func sslModeBreaksAOP(mode string) bool {
	switch strings.ToLower(mode) {
	case "off", "flexible":
		return true
	}
	return false
}

func sslModeReason(mode string) string {
	return "The zone's SSL/TLS mode is " + strings.ToUpper(mode[:1]) + strings.ToLower(mode[1:]) +
		": Cloudflare does not connect to the origin over HTTPS, so Authenticated Origin Pulls cannot apply. " +
		"Set the mode to Full (strict) in Cloudflare → SSL/TLS. The domain stays locked by IP only."
}

func permissionReason() string {
	return "The Cloudflare token cannot change zone settings. Grant it " + AOPPermissionHint +
		" to turn on Authenticated Origin Pulls. The domain stays locked by IP only."
}

// ensureZoneAOP makes sure Authenticated Origin Pulls is on for the zone, or
// records why it cannot be: a token without the permission, or an SSL mode
// (Flexible, Off) that never connects over HTTPS. The state is stored per zone;
// the returned changed is true when it differs from the stored one. A transient
// failure returns an error and leaves the stored state alone.
func (s *Service) ensureZoneAOP(ctx context.Context, c *Client, ws uuid.UUID, zone Zone) (state AOPState, changed bool, err error) {
	old, oerr := s.q.GetZoneAOP(ctx, db.GetZoneAOPParams{WorkspaceID: ws, ZoneID: zone.ID})
	known := oerr == nil
	if oerr != nil && !errors.Is(oerr, pgx.ErrNoRows) {
		return AOPState{}, false, fmt.Errorf("get zone aop: %w", oerr)
	}
	if known && old.State == AOPOn && s.now().Sub(old.CheckedAt) < aopRecheck {
		return AOPState{State: AOPOn}, false, nil
	}
	state, err = s.probeZoneAOP(ctx, c, zone)
	if err != nil {
		return AOPState{}, false, err
	}
	changed = !known || old.State != state.State
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.New(tx).UpsertZoneAOP(ctx, db.UpsertZoneAOPParams{
			WorkspaceID: ws, ZoneID: zone.ID, State: state.State, ReasonCode: state.ReasonCode, Reason: state.Reason,
		}); err != nil {
			return err
		}
		if state.Enabled {
			// A change NebulaCtrl made in the customer's Cloudflare account.
			if err := audit.Record(ctx, tx, audit.Entry{
				Action: "cloudflare.zone.aop.enable", ResourceType: "cloudflare_zone", ResourceID: zone.ID, WorkspaceID: ws,
				Metadata: map[string]any{"zone": zone.Name, "zoneId": zone.ID, "setting": "tls_client_auth"},
			}); err != nil {
				return err
			}
		}
		if !changed {
			return nil
		}
		return s.pushZoneInTx(ctx, tx, zone.ID)
	})
	if err != nil {
		return AOPState{}, false, fmt.Errorf("store zone aop: %w", err)
	}
	return state, changed, nil
}

func (s *Service) probeZoneAOP(ctx context.Context, c *Client, zone Zone) (AOPState, error) {
	mode, err := c.ZoneSSLMode(ctx, zone.ID)
	switch {
	case err == nil && sslModeBreaksAOP(mode):
		return AOPState{State: AOPUnavailable, ReasonCode: AOPReasonSSLMode, Reason: sslModeReason(mode)}, nil
	case err != nil && errors.Is(err, ErrAuth):
		return AOPState{State: AOPUnavailable, ReasonCode: AOPReasonPermission, Reason: permissionReason()}, nil
	case err != nil:
		return AOPState{}, err
	}
	on, err := c.ZoneAOP(ctx, zone.ID)
	if err != nil {
		if errors.Is(err, ErrAuth) {
			return AOPState{State: AOPUnavailable, ReasonCode: AOPReasonPermission, Reason: permissionReason()}, nil
		}
		return AOPState{}, err
	}
	if !on {
		if err := c.SetZoneAOP(ctx, zone.ID, true); err != nil {
			if errors.Is(err, ErrAuth) {
				return AOPState{State: AOPUnavailable, ReasonCode: AOPReasonPermission, Reason: permissionReason()}, nil
			}
			return AOPState{}, err
		}
		return AOPState{State: AOPOn, Enabled: true}, nil
	}
	return AOPState{State: AOPOn}, nil
}

// ---- route push ----

// RoutePusher queues a sync_routes command with a server's current route table
// inside tx (proxyserver.Service.PushRoutes).
type RoutePusher interface {
	PushRoutes(ctx context.Context, tx pgx.Tx, serverID uuid.UUID) error
}

// pushZoneInTx pushes the route table of every server that serves an
// origin-lockable domain or status page of the zone (all zones when zoneID is
// empty).
func (s *Service) pushZoneInTx(ctx context.Context, tx pgx.Tx, zoneID string) error {
	p := s.routePusher()
	if p == nil {
		return nil
	}
	q := db.New(tx)
	ids, err := q.ListOriginLockServerIDs(ctx, zoneID)
	if err != nil {
		return fmt.Errorf("list origin lock servers: %w", err)
	}
	cp, err := q.ListStatusOriginLockServerIDs(ctx, zoneID)
	if err != nil {
		return fmt.Errorf("list status origin lock servers: %w", err)
	}
	var domainServers []uuid.UUID
	for _, id := range ids {
		if id != nil {
			domainServers = append(domainServers, *id)
		}
	}
	// One table per server, in ascending server id: the counters' locks are held
	// to the end of the transaction (db.Queries.NumberRouteTable).
	for _, id := range db.SortedServerIDs(domainServers, cp) {
		if err := p.PushRoutes(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

// pushDomainInTx pushes the route table of the server that serves the domain.
func (s *Service) pushDomainInTx(ctx context.Context, tx pgx.Tx, domainID uuid.UUID) error {
	p := s.routePusher()
	if p == nil {
		return nil
	}
	sid, err := db.New(tx).GetDomainRouteServerID(ctx, domainID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && sid == nil) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get domain server: %w", err)
	}
	return p.PushRoutes(ctx, tx, *sid)
}

// pushControlPlaneInTx pushes the route table of the control-plane server,
// which carries the status routes. It does nothing without one.
func (s *Service) pushControlPlaneInTx(ctx context.Context, tx pgx.Tx) error {
	p := s.routePusher()
	if p == nil {
		return nil
	}
	srv, err := db.New(tx).GetControlPlaneServer(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get control-plane server: %w", err)
	}
	return p.PushRoutes(ctx, tx, srv.ID)
}

// ---- API view ----

// OriginLockView is the origin-lock state of a domain or status page.
type OriginLockView struct {
	// Enabled: the hostname is locked to Cloudflare right now.
	Enabled bool `json:"enabled"`
	// OptIn is the owner's setting; false is the operator's opt-out.
	OptIn bool `json:"optIn" doc:"The setting of the domain or page. False is an opt-out: anyone who finds the server's IP can bypass Cloudflare"`
	// Reason says why the lock is not enabled: opted_out, tunnel, not_proxied, not_in_zone, pending (waiting for the proxied record), not_published (a status page that is not served yet), wildcard or redirect.
	Reason string `json:"reason" enum:",opted_out,tunnel,not_proxied,not_in_zone,pending,not_published,wildcard,redirect"`
	// IPLock is layer 1: only Cloudflare's ranges may connect.
	IPLock bool `json:"ipLock"`
	// AOP is layer 2: on (the origin requires Cloudflare's client certificate), off (not applied) or unavailable.
	AOP           string `json:"aop" enum:"on,off,unavailable"`
	AOPReason     string `json:"aopReason" doc:"Why Authenticated Origin Pulls is unavailable; empty otherwise"`
	AOPReasonCode string `json:"aopReasonCode" enum:",permission,ssl_mode,error"`
	// RealClientIP is layer 3: CF-Connecting-IP / X-Forwarded-For is trusted from Cloudflare's ranges, so IP rules and analytics see the visitor.
	RealClientIP bool `json:"realClientIp"`
	// PermissionHint names the token permission AOP needs.
	PermissionHint string `json:"permissionHint"`
	// IPList says which list the lock uses.
	IPListSource string `json:"ipListSource" enum:"builtin,api,text"`
}

// originLockView builds the lock state of an evaluated name. sslMode is the
// zone's live SSL mode when it is known.
func (s *Service) originLockView(ctx context.Context, res *runResult, sslMode string) OriginLockView {
	n := res.n
	v := OriginLockView{OptIn: n.OriginLock, AOP: AOPOff, PermissionHint: AOPPermissionHint}
	if list, err := s.ipView(ctx); err == nil {
		v.IPListSource = list.Source
	} else {
		v.IPListSource = IPSourceBuiltin
	}
	if res.conn == nil || !res.inZone {
		v.Reason = LockReasonNotInZone
		return v
	}
	if ok, why := lockableRule(agentprotocol.DomainRule{Host: n.Host, Kind: n.RuleKind}, res.tunnel); !ok {
		v.Reason = why
		return v
	}
	if locked, why := LockPolicy(n.Proxied, res.tunnel, n.OriginLock); !locked {
		v.Reason = why
		return v
	}
	if !n.Served {
		v.Reason = LockReasonNotPublished
		return v
	}
	row, ok, err := s.ownerOf(n.Kind).lockRow(ctx, n)
	if err != nil || !ok {
		v.Reason = LockReasonPending
		return v
	}
	v.Enabled, v.IPLock, v.RealClientIP = true, true, true
	aop, err := s.q.GetZoneAOP(ctx, db.GetZoneAOPParams{WorkspaceID: row.Workspace, ZoneID: row.ZoneID})
	switch {
	case err != nil: // not checked yet: the next sync turns it on
	case aop.State == AOPOn:
		v.AOP = AOPOn
	default:
		v.AOP, v.AOPReason, v.AOPReasonCode = AOPUnavailable, aop.Reason, aop.ReasonCode
	}
	// The live SSL mode beats a stored verdict: fixing the mode shows at once.
	switch {
	case sslMode != "" && sslModeBreaksAOP(sslMode):
		v.AOP, v.AOPReasonCode, v.AOPReason = AOPUnavailable, AOPReasonSSLMode, sslModeReason(sslMode)
	case sslMode != "" && v.AOP == AOPUnavailable && v.AOPReasonCode == AOPReasonSSLMode:
		v.AOP, v.AOPReason, v.AOPReasonCode = AOPOff, "", ""
	}
	return v
}
