package domains

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The platform service (Headscale, internal/platform) is reachable from the
// tailnet, private networks and the team's own servers, never from the public
// internet. The rule is applied where the route table is built, to every domain
// of the platform service, so editing a domain cannot lift it. Object storage, the
// registry and central logs are instance services with no public route at all.

// PlatformInternalCIDRs are the networks a platform service accepts: RFC 1918,
// the Tailscale CGNAT range and ULA prefix, IPv6 ULA and loopback. The proxy
// takes its own Docker bridge ranges out of any allow list that judges the TCP
// peer (internal/agent/proxy/edge.go), so the private ranges cannot be reached
// through the port proxy or a tunnel connector.
var PlatformInternalCIDRs = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"100.64.0.0/10", "fd7a:115c:a1e0::/48",
	"fc00::/7",
	"127.0.0.0/8", "::1/128",
}

// PlatformAllowList is the allow list of a platform service: the internal
// networks plus the public address of every server (agents on other servers
// reach the service through its hostname from their public IPs). Addresses that
// do not parse, are unspecified or already fall inside an internal network are
// dropped. The list is bounded by MaxAccessCIDRs; the internal networks come
// first, then the server addresses in address order.
func PlatformAllowList(serverIPs []string) []string {
	internal := make([]netip.Prefix, len(PlatformInternalCIDRs))
	for i, c := range PlatformInternalCIDRs {
		internal[i] = netip.MustParsePrefix(c)
	}
	var addrs []netip.Addr
	for _, s := range serverIPs {
		a, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil || a.IsUnspecified() {
			continue
		}
		a = a.Unmap()
		if slices.ContainsFunc(internal, func(p netip.Prefix) bool { return p.Contains(a) }) {
			continue
		}
		addrs = append(addrs, a)
	}
	slices.SortFunc(addrs, netip.Addr.Compare)
	addrs = slices.Compact(addrs)

	out := slices.Clone(PlatformInternalCIDRs)
	for _, a := range addrs {
		if len(out) >= agentprotocol.MaxAccessCIDRs {
			break
		}
		out = append(out, a.String())
	}
	return out
}

// RestrictPlatformAccess returns the access rules a platform domain is served
// with. The result always allows only the platform list: a user's allow list is
// intersected with it (so it can narrow, never widen) and an allow list that
// shares nothing with it is ignored. The user's deny list is kept unless it
// would leave nobody. The client-IP depth is zeroed unless ipFromProxy is set
// (Cloudflare origin lock, where the visitor is the last X-Forwarded-For entry):
// otherwise a visitor could forge an X-Forwarded-For entry that passes the list.
func RestrictPlatformAccess(user *agentprotocol.Access, platform []string, ipFromProxy bool) *agentprotocol.Access {
	out := &agentprotocol.Access{AllowCIDRs: slices.Clone(platform)}
	if user != nil {
		out.DenyCIDRs = slices.Clone(user.DenyCIDRs)
		out.RejectStatus = user.RejectStatus
		if ipFromProxy {
			out.TrustedProxyDepth = user.TrustedProxyDepth
		}
		if len(user.AllowCIDRs) > 0 {
			if both := intersectCIDRs(user.AllowCIDRs, platform); len(both) > 0 {
				out.AllowCIDRs = both
			}
		}
	}
	if len(out.DenyCIDRs) > 0 {
		if _, err := agentprotocol.EffectiveAllow(out.AllowCIDRs, out.DenyCIDRs); err != nil {
			out.DenyCIDRs = nil
		}
	}
	return out
}

// intersectCIDRs returns the networks both lists admit: for every pair that
// overlaps, the narrower one (prefixes are aligned, so overlapping ones nest).
// Entries that do not parse are ignored.
func intersectCIDRs(a, b []string) []string {
	pa, pb := parseEntries(a), parseEntries(b)
	var out []string
	add := func(e cidrEntry) {
		if !slices.Contains(out, e.text) {
			out = append(out, e.text)
		}
	}
	for _, x := range pa {
		for _, y := range pb {
			switch {
			case x.prefix.Bits() <= y.prefix.Bits() && x.prefix.Contains(y.prefix.Addr()):
				add(y)
			case y.prefix.Bits() <= x.prefix.Bits() && y.prefix.Contains(x.prefix.Addr()):
				add(x)
			}
		}
	}
	return out
}

// cidrEntry is one access-list entry: its network and how it is written back (a
// CIDR in masked form, a single address without a length).
type cidrEntry struct {
	prefix netip.Prefix
	text   string
}

func parseEntries(list []string) []cidrEntry {
	out := make([]cidrEntry, 0, len(list))
	for _, s := range list {
		s = strings.TrimSpace(s)
		if p, err := netip.ParsePrefix(s); err == nil {
			p = p.Masked()
			out = append(out, cidrEntry{prefix: p, text: p.String()})
			continue
		}
		if a, err := netip.ParseAddr(s); err == nil {
			a = a.Unmap()
			out = append(out, cidrEntry{prefix: netip.PrefixFrom(a, a.BitLen()), text: a.String()})
		}
	}
	return out
}

// withPlatformAccess restricts the domains of platform services on serverID
// (see RestrictPlatformAccess). It runs last when the table is built, after the
// tunnel and origin-lock flags are known.
func withPlatformAccess(ctx context.Context, q *db.Queries, serverID uuid.UUID, routes []agentprotocol.Route) ([]agentprotocol.Route, error) {
	if len(routes) == 0 {
		return routes, nil
	}
	srv, err := q.GetServerByID(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return routes, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load server for platform access: %w", err)
	}
	platform, err := platformServiceIDs(ctx, q, srv.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if len(platform) == 0 {
		return routes, nil
	}
	var allow []string
	for i := range routes {
		id, err := uuid.Parse(routes[i].ServiceID)
		if err != nil || !platform[id] {
			continue
		}
		if allow == nil {
			if allow, err = platformAllowList(ctx, q, srv.WorkspaceID); err != nil {
				return nil, err
			}
		}
		restrictRules(routes[i].Domains, allow)
	}
	return routes, nil
}

// ApplyPlatformAccessToRules restricts the domain rules of a deploy payload
// when serviceID is a platform service (the agent builds the deployed
// service's route from the payload until the next route push).
func ApplyPlatformAccessToRules(ctx context.Context, q *db.Queries, serverID, serviceID uuid.UUID, rules []agentprotocol.DomainRule) error {
	if len(rules) == 0 {
		return nil
	}
	srv, err := q.GetServerByID(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load server for platform access: %w", err)
	}
	platform, err := platformServiceIDs(ctx, q, srv.WorkspaceID)
	if err != nil || !platform[serviceID] {
		return err
	}
	allow, err := platformAllowList(ctx, q, srv.WorkspaceID)
	if err != nil {
		return err
	}
	restrictRules(rules, allow)
	return nil
}

func restrictRules(rules []agentprotocol.DomainRule, allow []string) {
	for j := range rules {
		d := &rules[j]
		// Only an origin lock reads the visitor from X-Forwarded-For. A tunnelled
		// domain does not: cloudflared's address is what the proxy sees (see
		// withTunnelAccess).
		d.Options.Access = RestrictPlatformAccess(d.Options.Access, allow, d.OriginLocked)
	}
}

// tunnelDeniedCIDR is an allow list entry no address falls into (0.0.0.0/32): a
// rule made of it admits nobody.
const tunnelDeniedCIDR = "0.0.0.0/32"

// withTunnelAccess makes an IP allow list on a tunnelled domain admit nobody.
//
// A domain served through a Cloudflare Tunnel is reached by the cloudflared
// container on the edge network: the address the proxy sees is the connector's,
// a private Docker address, never the visitor's. An allow list that holds a
// private range (an "internal only" admin panel, every platform service) would
// admit the whole internet, and one that holds none would admit nobody. Reading
// the visitor from a header would mean trusting the connector's network, which
// the other containers on it share. So the rule cannot be enforced, and it fails
// closed: the domain answers 403 to everyone until the rule is removed (the
// save path refuses new ones) or the domain is no longer served by the tunnel.
// A platform service domain always carries its allow list, so it is never
// reachable through a tunnel. A deny-only rule is left as it is: it admits
// every connector address, so it hides nothing.
func withTunnelAccess(routes []agentprotocol.Route) []agentprotocol.Route {
	for i := range routes {
		for j := range routes[i].Domains {
			d := &routes[i].Domains[j]
			if !d.Tunnel || d.Options.Access == nil || len(d.Options.Access.AllowCIDRs) == 0 {
				continue
			}
			d.Options.Access = &agentprotocol.Access{
				AllowCIDRs: []string{tunnelDeniedCIDR}, RejectStatus: d.Options.Access.RejectStatus,
			}
		}
	}
	return routes
}

// platformServiceIDs is the set of service ids that belong to a platform service
// of the workspace.
func platformServiceIDs(ctx context.Context, q *db.Queries, ws uuid.UUID) (map[uuid.UUID]bool, error) {
	rows, err := q.ListPlatformServices(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list platform services: %w", err)
	}
	out := map[uuid.UUID]bool{}
	for _, r := range rows {
		for _, id := range r.ServiceIds {
			out[id] = true
		}
	}
	return out, nil
}

// platformAllowList is PlatformAllowList for the servers of the workspace that
// are not revoked.
func platformAllowList(ctx context.Context, q *db.Queries, ws uuid.UUID) ([]string, error) {
	servers, err := q.ListServersForFleet(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list servers for platform access: %w", err)
	}
	var ips []string
	for _, s := range servers {
		if s.Status != "revoked" && s.PublicIp != nil {
			ips = append(ips, *s.PublicIp)
		}
	}
	return PlatformAllowList(ips), nil
}

// PlatformServerIDs returns the servers that run a live service of a platform
// service of the workspace: the servers whose route tables carry platform
// routes.
func PlatformServerIDs(ctx context.Context, q *db.Queries, ws uuid.UUID) ([]uuid.UUID, error) {
	set, err := platformServiceIDs(ctx, q, ws)
	if err != nil || len(set) == 0 {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	rows, err := q.ListServicesIncludingDeleted(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list platform services' services: %w", err)
	}
	var out []uuid.UUID
	for _, r := range rows {
		if r.DeletedAt == nil && r.ServerID != nil {
			out = append(out, *r.ServerID)
		}
	}
	// Ascending, without duplicates: a transaction numbers the tables of several
	// servers in this order (db.Queries.NumberRouteTable).
	return db.SortedServerIDs(out), nil
}

// RoutePusher re-sends a server's route table inside tx (proxyserver.Service).
type RoutePusher interface {
	PushRoutes(ctx context.Context, tx pgx.Tx, serverID uuid.UUID) error
}

// PlatformRouteSync keeps platform routes in step with the workspace's servers:
// their allow list names every server's public address, so a server that is
// added, revoked or changes address re-sends the route tables of the servers
// that run platform services. It implements servers.RoutesHook.
type PlatformRouteSync struct {
	Routes RoutePusher
}

// ServersChanged queues the route table of every server that runs a platform
// service inside tx.
func (p PlatformRouteSync) ServersChanged(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	ids, err := PlatformServerIDs(ctx, db.New(tx), ws)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := p.Routes.PushRoutes(ctx, tx, id); err != nil {
			return fmt.Errorf("push routes of server %s: %w", id, err)
		}
	}
	return nil
}
