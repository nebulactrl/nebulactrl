package dashboardaccess

import (
	"net"
	"net/netip"
	"strings"
)

// Peer is how a request reached the control plane: the TCP peer and the
// X-Forwarded-For header it carried.
type Peer struct {
	RemoteAddr   string
	ForwardedFor string
}

// RealClientIP returns the address the dashboard route's allow list sees for a
// request, and whether the request came through the host proxy at all.
//
// The control plane sits behind the control-plane host's Traefik, which is the
// edge: the route's ipAllowList checks Traefik's TCP peer, the browser's own
// address. What the control plane itself sees as RemoteAddr is the Traefik
// container on the private edge network. Traefik appends the address of the
// peer it saw to X-Forwarded-For, so the LAST entry is that address; the
// entries before it are whatever the client sent and are never read (the
// same reasoning as Cloudflare's depth-1 rule in internal/agent/proxy/cforigin.go,
// originLockRealIPDepth).
//
// The header is trusted only when the request came from a loopback, private
// or link-local peer: a public peer reaches the control plane directly, can
// write any header, and is not subject to the route's allow list either. A
// request without a trusted X-Forwarded-For did not pass through the route, so
// (ip is the TCP peer and) viaProxy is false: the allow list cannot lock that
// caller out.
func RealClientIP(remoteAddr, forwardedFor string) (ip netip.Addr, viaProxy bool) {
	peer := parseHost(remoteAddr)
	if !peer.IsValid() {
		return netip.Addr{}, false
	}
	if !peerMayForward(peer) {
		return peer, false
	}
	entries := strings.Split(forwardedFor, ",")
	for i := len(entries) - 1; i >= 0; i-- {
		e := strings.TrimSpace(entries[i])
		if e == "" {
			continue
		}
		// Only the last non-empty entry counts: a malformed one is not skipped
		// in favour of an earlier, client-controlled entry.
		if a := parseHost(e); a.IsValid() {
			return a, true
		}
		break
	}
	return peer, false
}

func peerMayForward(a netip.Addr) bool {
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast()
}

// parseHost parses "host:port", "[v6]:port", "v6", "v4" and returns the
// address with an IPv4-mapped IPv6 address unmapped and a zone dropped.
func parseHost(s string) netip.Addr {
	s = strings.TrimSpace(s)
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}
	}
	return a.WithZone("").Unmap()
}

// Allows reports whether ip is admitted by an allow list of CIDRs and
// addresses. An empty list admits everybody; an invalid ip is admitted by no
// non-empty list. Entries are not validated here (agentprotocol does that); an
// entry that does not parse matches nothing.
func Allows(allow []string, ip netip.Addr) bool {
	if len(allow) == 0 {
		return true
	}
	if !ip.IsValid() {
		return false
	}
	ip = ip.WithZone("").Unmap()
	for _, e := range allow {
		e = strings.TrimSpace(e)
		if p, err := netip.ParsePrefix(e); err == nil {
			if p.Contains(ip) {
				return true
			}
			continue
		}
		if a, err := netip.ParseAddr(e); err == nil && a.WithZone("").Unmap() == ip {
			return true
		}
	}
	return false
}
