package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var (
	// upstream hosts are container names, IPs or DNS names.
	upstreamHostPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._:-]*[A-Za-z0-9])?$`)
	// healthPathPattern: an absolute URL path (plus optional query) without
	// spaces or control characters.
	healthPathPattern = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()*+,;=:@%/?-]*$`)
	// domainIDPattern: domain IDs (UUIDs) name Traefik objects, so they must be
	// plain identifiers.
	domainIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{7,}$`)
	// maintenanceIDPattern: service IDs name the pages server's per-service
	// paths and files, so they must be plain identifiers.
	maintenanceIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
)

// idShort is the 8-character domain ID prefix used in router names.
func idShort(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// ValidateRoutes rejects route tables the agent cannot express safely: every
// value ends up inside a Traefik rule, URL or regex. Upstreams of external
// routes and of every tcp/udp route are checked against policy; names are not
// resolved here (see PinUpstreams).
func ValidateRoutes(routes []agentprotocol.Route, policy agentprotocol.UpstreamPolicy) error {
	httpIDs := map[string]bool{}
	l4Ports := map[L4Port]string{}
	l4IDs := map[string]bool{} // protocol:service id (router/service names)
	for i, r := range routes {
		if r.ServiceID == "" || !upstreamHostPattern.MatchString(r.ServiceID) {
			return fmt.Errorf("route %d: invalid service id %q", i, r.ServiceID)
		}
		switch r.Kind {
		case "", agentprotocol.RouteKindService, agentprotocol.RouteKindExternal:
		default:
			return fmt.Errorf("route %d (service %s): invalid kind %q", i, r.ServiceID, r.Kind)
		}
		if len(r.Upstreams) == 0 {
			return fmt.Errorf("route %d (service %s): no upstreams", i, r.ServiceID)
		}
		for _, up := range r.Upstreams {
			host, port, err := net.SplitHostPort(up)
			if err != nil || host == "" || port == "" {
				return fmt.Errorf("route %d (service %s): upstream %q must be host:port", i, r.ServiceID, up)
			}
			if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("route %d (service %s): invalid upstream port %q", i, r.ServiceID, port)
			}
			if !upstreamHostPattern.MatchString(host) {
				return fmt.Errorf("route %d (service %s): invalid upstream host %q", i, r.ServiceID, host)
			}
			if r.Kind == agentprotocol.RouteKindExternal || r.IsL4() {
				if err := agentprotocol.CheckUpstream(up, policy); err != nil {
					return fmt.Errorf("route %d (service %s): %w", i, r.ServiceID, err)
				}
			}
		}

		switch r.Protocol {
		case "", agentprotocol.ProtoHTTP:
			if err := validateHTTPRoute(i, r); err != nil {
				return err
			}
			if httpIDs[r.ServiceID] {
				return fmt.Errorf("route %d: service %s has more than one http route", i, r.ServiceID)
			}
			httpIDs[r.ServiceID] = true
		case agentprotocol.ProtoTCP, agentprotocol.ProtoUDP:
			if err := validateL4Route(i, r); err != nil {
				return err
			}
			p := L4Port{Proto: r.Protocol, Port: r.ListenPort}
			if other, dup := l4Ports[p]; dup {
				return fmt.Errorf("route %d (service %s): %s port %d is already used by service %s", i, r.ServiceID, r.Protocol, r.ListenPort, other)
			}
			l4Ports[p] = r.ServiceID
			id := r.Protocol + ":" + r.ServiceID
			if l4IDs[id] {
				return fmt.Errorf("route %d: service %s has more than one %s route", i, r.ServiceID, r.Protocol)
			}
			l4IDs[id] = true
		default:
			return fmt.Errorf("route %d (service %s): invalid protocol %q", i, r.ServiceID, r.Protocol)
		}
	}
	return validateOriginLocks(routes)
}

func validateHTTPRoute(i int, r agentprotocol.Route) error {
	if len(r.Hosts) == 0 {
		return fmt.Errorf("route %d (service %s): no hosts", i, r.ServiceID)
	}
	if r.ListenPort != 0 || len(r.AllowCIDRs) > 0 || len(r.DenyCIDRs) > 0 || r.ProxyProtocol != 0 {
		return fmt.Errorf("route %d (service %s): listenPort, allowCidrs, denyCidrs and proxyProtocol only apply to tcp/udp routes", i, r.ServiceID)
	}
	hosts := make(map[string]bool, len(r.Hosts))
	for _, h := range r.Hosts {
		if !agentprotocol.ValidRouteHost(h) {
			return fmt.Errorf("route %d (service %s): invalid host %q", i, r.ServiceID, h)
		}
		hosts[h] = true
	}
	if r.HealthPath != "" && !healthPathPattern.MatchString(r.HealthPath) {
		return fmt.Errorf("route %d (service %s): invalid health path %q", i, r.ServiceID, r.HealthPath)
	}
	if r.Maintenance != nil {
		if !maintenanceIDPattern.MatchString(r.ServiceID) {
			return fmt.Errorf("route %d (service %s): maintenance needs a plain service id", i, r.ServiceID)
		}
		if err := agentprotocol.ValidateMaintenance(*r.Maintenance); err != nil {
			return fmt.Errorf("route %d (service %s): maintenance: %w", i, r.ServiceID, err)
		}
	}
	switch r.Scheme {
	case "", "http", "https":
	default:
		return fmt.Errorf("route %d (service %s): invalid scheme %q", i, r.ServiceID, r.Scheme)
	}
	if t := r.Transport; t != nil && t.ServerName != "" && !agentprotocol.ValidHostname(t.ServerName) {
		return fmt.Errorf("route %d (service %s): invalid transport server name %q", i, r.ServiceID, t.ServerName)
	}
	ids := map[string]bool{}
	for _, d := range r.Domains {
		if !domainIDPattern.MatchString(d.ID) {
			return fmt.Errorf("route %d (service %s): invalid domain id %q", i, r.ServiceID, d.ID)
		}
		if ids[idShort(d.ID)] {
			return fmt.Errorf("route %d (service %s): domain id prefix %q is not unique", i, r.ServiceID, idShort(d.ID))
		}
		ids[idShort(d.ID)] = true
		if err := d.Validate(); err != nil {
			return fmt.Errorf("route %d (service %s): domain %s: %w", i, r.ServiceID, d.Host, err)
		}
		if !hosts[d.Host] {
			return fmt.Errorf("route %d (service %s): domain %s is not listed in hosts", i, r.ServiceID, d.Host)
		}
		if d.Options.BasicAuth {
			// A basic-auth rule without resolved users must never render: it
			// would publish the service unprotected.
			if len(d.AuthUsers) == 0 {
				return fmt.Errorf("route %d (service %s): domain %s requires basic auth but has no users", i, r.ServiceID, d.Host)
			}
			if err := agentprotocol.ValidBasicAuthLines(d.AuthUsers); err != nil {
				return fmt.Errorf("route %d (service %s): domain %s: %w", i, r.ServiceID, d.Host, err)
			}
		}
	}
	return nil
}

func validateL4Route(i int, r agentprotocol.Route) error {
	if r.Maintenance != nil {
		return fmt.Errorf("route %d (service %s): maintenance only applies to http routes", i, r.ServiceID)
	}
	if len(r.Hosts) > 0 || len(r.Domains) > 0 {
		return fmt.Errorf("route %d (service %s): %s routes have no hosts or domains", i, r.ServiceID, r.Protocol)
	}
	if err := agentprotocol.ValidateListenPort(r.ListenPort); err != nil {
		return fmt.Errorf("route %d (service %s): %w", i, r.ServiceID, err)
	}
	if err := agentprotocol.ValidateAccessLists(r.AllowCIDRs, r.DenyCIDRs, false); err != nil {
		return fmt.Errorf("route %d (service %s): %w", i, r.ServiceID, err)
	}
	switch r.ProxyProtocol {
	case 0, 1, 2:
	default:
		return fmt.Errorf("route %d (service %s): proxyProtocol must be 0, 1 or 2", i, r.ServiceID)
	}
	if r.Protocol == agentprotocol.ProtoUDP {
		if len(r.AllowCIDRs) > 0 || len(r.DenyCIDRs) > 0 {
			return fmt.Errorf("route %d (service %s): udp routes cannot enforce allowCidrs or denyCidrs (Traefik has no UDP middleware)", i, r.ServiceID)
		}
		if r.ProxyProtocol != 0 {
			return fmt.Errorf("route %d (service %s): udp routes do not support the PROXY protocol", i, r.ServiceID)
		}
	}
	return nil
}

// lookupHost resolves a name for PinUpstreams; replaced in tests.
var lookupHost = func(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

// resolveBudget bounds the DNS lookups of one apply.
const resolveBudget = 4 * time.Second

// maxPinned caps the addresses one upstream name is pinned to.
const maxPinned = 8

// managedContainerName matches the name of a replica container as
// agentprotocol.ContainerName builds it. Upstreams of mixed route services and
// tcp routes of managed services use these names; no DNS name matches (no dots).
var managedContainerName = regexp.MustCompile(`^nc-[A-Za-z0-9][A-Za-z0-9-]*-[0-9a-f]{8}-[0-9]+$`)

// PinUpstreams is the second half of the upstream policy. It resolves the
// hostname of every external and tcp/udp upstream, rejects a name that points
// at an address the policy denies (a public name resolving to 127.0.0.1 or the
// metadata address is the classic bypass) and returns a copy of the table whose
// such upstreams are the resolved IP literals. Traefik then never resolves
// those names itself, so a name that answers differently later (DNS rebinding,
// TTL 0) cannot reach a denied address: the next apply and every re-push
// resolve and judge again.
//
// It fails closed. A name that does not resolve on the agent is rejected,
// whatever its spelling, and the prefix "nc-" is no exemption: only a name of
// exactly the shape the control plane generates for a managed container
// (nc-<slug>-<8 hex>-<replica>, no dots) is left to Docker's DNS, and the
// control plane refuses such a name typed as an upstream address.
// Further exceptions: the host gateway name (judged by CheckUpstream, resolved
// by Docker inside the proxy container), System routes (the control plane's own
// containers) and, only when magicDNS is set, a single-label name or a *.ts.net
// name, which only the proxy container's tailnet resolver knows.
//
// A route whose Host header or TLS server name would become the address after
// pinning (passHost=false, or an https upstream without transport.serverName)
// keeps the name: it is rendered as the Host header override and the TLS server
// name. Such a route can therefore use one hostname only.
func PinUpstreams(ctx context.Context, routes []agentprotocol.Route, policy agentprotocol.UpstreamPolicy, magicDNS bool) ([]agentprotocol.Route, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveBudget)
	defer cancel()
	type answer struct {
		addrs []netip.Addr
		err   error
	}
	cache := map[string]answer{}
	resolve := func(host string) ([]netip.Addr, error) {
		if a, ok := cache[host]; ok {
			return a.addrs, a.err
		}
		addrs, err := lookupHost(ctx, host)
		var out []netip.Addr
		for _, s := range addrs {
			if a, perr := netip.ParseAddr(s); perr == nil {
				out = append(out, a.Unmap())
			}
		}
		if err == nil && len(out) == 0 {
			err = errors.New("no addresses")
		}
		if ctx.Err() != nil {
			err = fmt.Errorf("lookup did not finish in %s", resolveBudget)
		}
		cache[host] = answer{out, err}
		return out, err
	}

	out := make([]agentprotocol.Route, len(routes))
	copy(out, routes)
	for i, r := range routes {
		if (r.Kind != agentprotocol.RouteKindExternal && !r.IsL4()) || r.System {
			continue
		}
		type hostPort struct{ host, port string }
		var ups []string
		var named []hostPort
		seen := map[string]bool{}
		for _, up := range r.Upstreams {
			host, port, err := net.SplitHostPort(up)
			if err != nil {
				return nil, fmt.Errorf("service %s: upstream %q must be host:port", r.ServiceID, up)
			}
			if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
				ups = append(ups, up) // literal addresses were checked by ValidateRoutes
				continue
			}
			if strings.EqualFold(strings.TrimSuffix(host, "."), agentprotocol.HostGatewayName) || managedContainerName.MatchString(host) {
				ups = append(ups, up)
				continue
			}
			addrs, err := resolve(host)
			if err != nil {
				if magicDNS && tailnetOnlyName(host) {
					ups = append(ups, up)
					continue
				}
				return nil, fmt.Errorf("service %s: upstream name %s does not resolve on this server (%v); use an address or a name its DNS knows", r.ServiceID, host, err)
			}
			if err := agentprotocol.CheckResolved(host, port, addrs, policy, true); err != nil {
				return nil, fmt.Errorf("service %s: %w", r.ServiceID, err)
			}
			sorted := slices.SortedFunc(slices.Values(addrs), func(a, b netip.Addr) int { return a.Compare(b) })
			sorted = slices.Compact(sorted)
			if len(sorted) > maxPinned {
				sorted = sorted[:maxPinned]
			}
			for _, a := range sorted {
				ups = append(ups, net.JoinHostPort(a.String(), port))
			}
			if !seen[host] {
				seen[host] = true
				named = append(named, hostPort{host, port})
			}
		}
		nr := r
		nr.Upstreams = ups
		if len(named) > 0 && !r.IsL4() {
			noPass := r.PassHost != nil && !*r.PassHost
			tlsName := r.Scheme == "https" && (r.Transport == nil || r.Transport.ServerName == "")
			if noPass || tlsName {
				hosts := map[string]bool{}
				for _, up := range r.Upstreams {
					h, _, _ := net.SplitHostPort(up)
					hosts[strings.ToLower(h)] = true
				}
				if len(hosts) > 1 {
					return nil, fmt.Errorf("service %s: upstreams on different hostnames cannot be pinned with passHost=false or an https upstream; use one hostname or set transport.serverName", r.ServiceID)
				}
				if noPass {
					nr.HostOverride = net.JoinHostPort(named[0].host, named[0].port)
				}
				if tlsName {
					t := agentprotocol.UpstreamTransport{}
					if r.Transport != nil {
						t = *r.Transport
					}
					t.ServerName = named[0].host
					nr.Transport = &t
				}
			}
		}
		out[i] = nr
	}
	return out, nil
}

// tailnetOnlyName reports whether host can only be a tailnet device name: a
// single label or a MagicDNS (*.ts.net) name.
func tailnetOnlyName(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return !strings.Contains(h, ".") || strings.HasSuffix(h, ".ts.net")
}

// VetProbeHost returns the addresses an agent-side probe of an upstream may
// dial. It is PinUpstreams for one host: a literal address, the host gateway
// name and a managed container name are returned as they are (the table already
// judged them, or Docker resolves them), any other name is resolved and judged
// against policy, and only the vetted addresses are returned, so a probe can
// neither reach an address the proxy would refuse nor scan the host's loopback
// through a name that points there. A system route is left alone. The error is
// generic on purpose: it is shown to the team and must not reveal what the name
// resolves to.
func VetProbeHost(ctx context.Context, r agentprotocol.Route, host string, policy agentprotocol.UpstreamPolicy) ([]string, error) {
	if r.System || managedContainerName.MatchString(host) || strings.EqualFold(strings.TrimSuffix(host, "."), agentprotocol.HostGatewayName) {
		return []string{host}, nil
	}
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return []string{host}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, resolveBudget)
	defer cancel()
	names, err := lookupHost(ctx, host)
	var addrs []netip.Addr
	for _, s := range names {
		if a, perr := netip.ParseAddr(s); perr == nil {
			addrs = append(addrs, a.Unmap())
		}
	}
	if err != nil || len(addrs) == 0 {
		return nil, errors.New("upstream name does not resolve on this server")
	}
	// The port does not matter for the address policy (only the proxy admin
	// port does, and the probe never targets a proxy name).
	if err := agentprotocol.CheckResolved(host, "80", addrs, policy, true); err != nil {
		return nil, errors.New("upstream name resolves to an address the server's upstream policy denies")
	}
	out := make([]string, 0, len(addrs))
	for _, a := range slices.Compact(slices.SortedFunc(slices.Values(addrs), func(a, b netip.Addr) int { return a.Compare(b) })) {
		out = append(out, a.String())
	}
	return out, nil
}
