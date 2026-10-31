package agentprotocol

import (
	"fmt"
	"net"
	"net/netip"
)

// metadataAddrs are cloud metadata endpoints outside 169.254.0.0/16 (Alibaba
// Cloud, and the AWS IPv6 endpoint). They follow the link-local switch of the
// upstream policy.
var metadataAddrs = map[netip.Addr]bool{
	netip.MustParseAddr("100.100.100.200"): true,
	netip.MustParseAddr("fd00:ec2::254"):   true,
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// IsPrivateAddr reports whether ip is in a range that is not publicly routable
// and not already judged by CheckUpstream's loopback/link-local rules: RFC 1918,
// unique-local IPv6 and the CGNAT/tailnet range.
func IsPrivateAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsPrivate() || cgnat.Contains(ip)
}

// CheckResolved judges the addresses a hostname resolved to. Each address goes
// through CheckUpstream (unspecified, loopback, link-local, metadata and
// multicast addresses are denied unless the server's policy allows them), so a
// public-looking name cannot point a route at them. A name that resolves to a
// private address (RFC 1918, unique-local, CGNAT) is also refused unless
// allowPrivate is set: callers pass true for an instance administrator.
func CheckResolved(host, port string, addrs []netip.Addr, p UpstreamPolicy, allowPrivate bool) error {
	for _, a := range addrs {
		a = a.Unmap()
		if err := CheckUpstream(net.JoinHostPort(a.String(), port), p); err != nil {
			return fmt.Errorf("upstream name %s resolves to %s: %w", host, a, err)
		}
		if !allowPrivate && IsPrivateAddr(a) {
			return fmt.Errorf("upstream name %s resolves to the private address %s; only an instance administrator can point a name at a private address", host, a)
		}
	}
	return nil
}
