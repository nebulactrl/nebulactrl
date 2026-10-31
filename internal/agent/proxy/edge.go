package proxy

import (
	"context"
	"fmt"
	"net/netip"
	"sync"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Why the public ports are bound to IPv4 unless the edge network has IPv6, and
// why an allow list that judges the TCP peer is rendered exactly as written.
//
// An IP allow list that has no ipStrategy (a depth-0 rule, and every TCP rule)
// judges the TCP peer. Two hops can make the peer a Docker address instead of
// the real client:
//
//   - the userland port proxy: an IPv6 client of a port published on :: reaches
//     an IPv4-only container through docker-proxy, whose source is the edge
//     network's gateway, a private address that "internal only" allow lists
//     (Headscale, 172.16.0.0/12, 192.168.0.0/16) admit;
//   - cloudflared: a Cloudflare Tunnel's connector is a container on the edge
//     network, so every tunnelled request comes from a private address.
//
// Both are closed at their source, not by taking the Docker subnets out of the
// allow lists. Excluding the gateway and the bridge subnets also refused every
// legitimate client that arrives through the gateway: a client on the host
// itself (127.0.0.1 through the port publishing) and a LAN client behind the
// host's NAT are seen as the bridge gateway, and those are exactly the clients
// an "internal only" or database allow list is written for.
//
//   - The public ports are published on 0.0.0.0 (IPv4 only) unless the edge
//     network has an IPv6 subnet (specInputs.EdgeIPv6). With no IPv6 listener a
//     public IPv6 visitor cannot reach the proxy at all, so none can arrive as
//     the IPv4 gateway. On a dual-stack edge network docker routes IPv6
//     traffic to the container's IPv6 address and keeps the visitor's source.
//   - A tunnelled domain never carries an effective IP allow list: the save path
//     refuses one (ip_rules_on_tunnel) and the route table renders an existing
//     one as "admit nobody" (domains.withTunnelAccess), so the connector's
//     private address is never judged against a user's private ranges.
//
// A rule that reads the visitor from X-Forwarded-For (depth ≥ 1, origin lock)
// does not judge the peer at all.

// edgeState is the manager's view of the edge network, refreshed by every apply.
type edgeState struct {
	mu   sync.RWMutex
	ipv6 bool
}

// refreshEdge reads whether the edge network has an IPv6 subnet. A network that
// does not exist (the edge network before the proxy first starts) has none. An
// engine error fails the apply: the published ports must not be chosen on a
// guess.
func (m *Manager) refreshEdge(ctx context.Context) error {
	nets, err := m.cfg.Engine.NetworkList(ctx, agentprotocol.EdgeNetwork, nil)
	if err != nil {
		return fmt.Errorf("inspect network %s: %w", agentprotocol.EdgeNetwork, err)
	}
	ipv6 := false
	for _, n := range nets {
		if n.Name != agentprotocol.EdgeNetwork { // NetworkList matches by prefix
			continue
		}
		for _, s := range n.Subnets {
			if p, err := netip.ParsePrefix(s); err == nil && p.Addr().Is6() {
				ipv6 = true
			}
		}
	}
	m.edge.mu.Lock()
	m.edge.ipv6 = ipv6
	m.edge.mu.Unlock()
	return nil
}

// edgeIPv6 reports whether the edge network has an IPv6 subnet.
func (m *Manager) edgeIPv6() bool {
	m.edge.mu.RLock()
	defer m.edge.mu.RUnlock()
	return m.edge.ipv6
}
