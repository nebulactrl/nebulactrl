package proxyroutes

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// lookupHost resolves an upstream name when a route is saved; replaced in tests.
var lookupHost = func(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

// upstreamLookupBudget bounds the lookup of one upstream name at save time.
const upstreamLookupBudget = 3 * time.Second

// checkUpstreamName is the save-time half of the upstream policy for a DNS
// name (CheckUpstream only judges addresses and a few well-known names):
//
//   - a name starting with "nc-" is a NebulaCtrl-managed container; an upstream
//     address is operator-supplied, a managed target is chosen by service id;
//   - a name that resolves here is judged by its addresses. Loopback, link-local
//     and metadata addresses follow the server's policy; a private address
//     (RFC 1918, unique-local, CGNAT) is refused unless the caller is an instance
//     administrator.
//
// A name that does not resolve from the control plane is accepted: LAN and
// tailnet names only resolve on the server. The agent resolves every name again
// on each apply, judges the addresses against the policy and pins them, so a
// name that answers differently later never reaches a denied address.
func checkUpstreamName(ctx context.Context, address string, pol agentprotocol.UpstreamPolicy) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil // CheckUpstream already rejected the shape
	}
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if _, err := netip.ParseAddr(strings.Trim(lower, "[]")); err == nil {
		return nil
	}
	if strings.HasPrefix(lower, agentprotocol.ContainerPrefix) {
		return apierr.Unprocessable("upstream_denied", "upstream names starting with nc- are NebulaCtrl-managed containers: pick the service as the target instead of typing its name").
			WithDetails(map[string]any{"address": address})
	}
	if lower == agentprotocol.HostGatewayName {
		return nil
	}
	lctx, cancel := context.WithTimeout(ctx, upstreamLookupBudget)
	defer cancel()
	names, err := lookupHost(lctx, host)
	if err != nil {
		return nil
	}
	var addrs []netip.Addr
	for _, s := range names {
		if a, err := netip.ParseAddr(s); err == nil {
			addrs = append(addrs, a)
		}
	}
	allowPrivate := policy.Require(ctx, policy.ScopeAdmin, nil) == nil
	if err := agentprotocol.CheckResolved(host, port, addrs, pol, allowPrivate); err != nil {
		return apierr.Unprocessable("upstream_denied", err.Error()).WithDetails(map[string]any{"address": address})
	}
	return nil
}
