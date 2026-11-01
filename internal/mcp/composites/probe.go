package composites

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/netguard"
)

// Probing is how verify_service and setup_domain check "is it actually
// serving?". It is SSRF-hardened because the hostname is caller-influenced
// (anyone with projects:write can add a domain whose DNS points anywhere):
//
//   - only hostnames of the service itself are probed (the caller passes the
//     list it read from the service's domains; nothing user-typed becomes a
//     URL host);
//   - the host is resolved here and the connection is made to the resolved IP
//     (no second resolution, so no DNS rebinding); an IP is dialed only when it
//     is a public address or one of the service's server addresses (the
//     server's public IP) - loopback, private, link-local, multicast and
//     unspecified addresses are refused;
//   - redirects are never followed to another host or port: a same-host
//     redirect on the default port of its scheme is followed (http→https,
//     trailing slash) up to 3 hops, anything else stops and is reported as
//     `redirectedTo`;
//   - the whole probe has a timeout, the body is never read, and TLS
//     certificates are verified.

const (
	probeTimeout   = 8 * time.Second
	probeMaxHops   = 3
	probeDialLimit = 4 // resolved addresses tried per probe
)

// ProbeResult is the outcome of probing one hostname.
type ProbeResult struct {
	Hostname  string `json:"hostname"`
	URL       string `json:"url"`
	OK        bool   `json:"ok" jsonschema:"true when the host answered with a status below 500"`
	Status    int    `json:"status,omitempty"`
	LatencyMs int64  `json:"latencyMs,omitempty"`
	// RedirectedTo is the target of a redirect that was not followed because it
	// leaves the probed host.
	RedirectedTo string `json:"redirectedTo,omitempty"`
	// Error says why no response came back (DNS, refused address, TLS, timeout).
	Error string `json:"error,omitempty"`
}

// ProbeTarget is one hostname to probe.
type ProbeTarget struct {
	Hostname string
	// HTTP probes over plain HTTP (domains with TLS mode none).
	HTTP bool
	// Path is the request path; "/" when empty. It must be an absolute path.
	Path string
}

// probe checks the targets concurrently-safe one by one (callers fan out).
// serverIPs are the addresses of the servers that host the service: a
// hostname resolving to one of them is allowed even when it is a private
// address (homelab servers).
func (c *composites) probe(ctx context.Context, t ProbeTarget, serverIPs []netip.Addr) ProbeResult {
	res := ProbeResult{Hostname: t.Hostname}
	host := strings.ToLower(strings.TrimSpace(t.Hostname))
	if !validProbeHost(host) {
		res.Error = "not a valid hostname"
		return res
	}
	p := t.Path
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\r\n\\ ") {
		res.Error = "path must be an absolute path"
		return res
	}
	scheme := "https"
	if t.HTTP {
		scheme = "http"
	}
	u := url.URL{Scheme: scheme, Host: host, Path: p}
	res.URL = u.String()

	client := c.probeClient(host, serverIPs)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		res.Error = "invalid probe request"
		return res
	}
	req.Header.Set("User-Agent", "NebulaCtrl-verify/1")
	req.Header.Set("Accept", "*/*")
	start := time.Now()
	resp, err := client.Do(req)
	res.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = probeError(err)
		return res
	}
	defer resp.Body.Close()
	res.Status = resp.StatusCode
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if loc, err := resp.Location(); err == nil && !probeRedirectAllowed(host, loc) {
			res.RedirectedTo = loc.Scheme + "://" + loc.Host
		}
	}
	res.OK = resp.StatusCode < 500
	return res
}

// probeClient returns a client that probes host only. A client in Deps.Probe
// keeps its transport (tests, custom egress) but still gets the timeout and
// the redirect policy.
func (c *composites) probeClient(host string, serverIPs []netip.Addr) *http.Client {
	var cl http.Client
	if c.Probe != nil {
		cl = *c.Probe
	} else {
		cl.Transport = &http.Transport{
			DialContext:           safeDialer(host, serverIPs),
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			DisableKeepAlives:     true,
			ResponseHeaderTimeout: probeTimeout,
			TLSHandshakeTimeout:   probeTimeout,
		}
	}
	cl.Timeout = probeTimeout
	cl.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > probeMaxHops || !probeRedirectAllowed(host, req.URL) {
			return http.ErrUseLastResponse
		}
		return nil
	}
	return &cl
}

// probeRedirectAllowed reports whether a redirect stays on the probed host and
// on the default port of its scheme (so http→https and a trailing slash
// work). Another port is another service on the same address and is only
// reported.
func probeRedirectAllowed(host string, u *url.URL) bool {
	if !strings.EqualFold(u.Hostname(), host) {
		return false
	}
	var def string
	switch strings.ToLower(u.Scheme) {
	case "http":
		def = "80"
	case "https":
		def = "443"
	default:
		return false
	}
	port := u.Port()
	return port == "" || port == def
}

// safeDialer resolves the name itself and dials only an allowed address.
func safeDialer(host string, serverIPs []netip.Addr) func(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	d.Timeout = probeTimeout
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		h, port, err := net.SplitHostPort(addr)
		if err != nil || !strings.EqualFold(h, host) {
			// The transport only ever dials the probed host; anything else
			// (a redirect that slipped through, a proxy) is refused.
			return nil, errors.New("refused: not the probed hostname")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", h)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("dns: %s does not resolve", h)
		}
		var lastErr error = errors.New("refused: resolves to a non-public address")
		tried := 0
		for _, ip := range ips {
			ip = ip.Unmap()
			if !probeAddrAllowed(ip, serverIPs) {
				continue
			}
			if tried++; tried > probeDialLimit {
				break
			}
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

// probeAddrAllowed reports whether ip may be dialed: a server of the service,
// or a public unicast address.
func probeAddrAllowed(ip netip.Addr, serverIPs []netip.Addr) bool {
	for _, s := range serverIPs {
		if s.Unmap() == ip {
			return true
		}
	}
	switch {
	case !ip.IsValid(), ip.IsUnspecified(), ip.IsLoopback(), ip.IsPrivate(), ip.IsLinkLocalUnicast(),
		ip.IsLinkLocalMulticast(), ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return false
	}
	// Carrier-grade NAT (100.64.0.0/10), the IETF protocol and benchmarking
	// ranges, reserved space, unique-local and NAT64 are not public either.
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return false
		}
	}
	// 6to4 and Teredo addresses route to the IPv4 address they embed, so one
	// that embeds a non-public IPv4 address reaches an internal host.
	for _, v4 := range embeddedIPv4(ip) {
		if netguard.Blocked(v4, netguard.Allow{}) {
			return false
		}
	}
	return ip.IsGlobalUnicast()
}

// embeddedIPv4 returns the IPv4 addresses a 6to4 (2002::/16) or Teredo
// (2001::/32) address carries: the 6to4 gateway, or the Teredo server and
// the (bit-inverted) client.
func embeddedIPv4(ip netip.Addr) []netip.Addr {
	if !ip.Is6() || ip.Is4In6() {
		return nil
	}
	b := ip.As16()
	switch {
	case b[0] == 0x20 && b[1] == 0x02:
		return []netip.Addr{netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]})}
	case b[0] == 0x20 && b[1] == 0x01 && b[2] == 0 && b[3] == 0:
		return []netip.Addr{
			netip.AddrFrom4([4]byte{b[4], b[5], b[6], b[7]}),
			netip.AddrFrom4([4]byte{^b[12], ^b[13], ^b[14], ^b[15]}),
		}
	}
	return nil
}

var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("64:ff9b::/96"),
}

// serverAddrs parses the public IPs of servers into addresses.
func serverAddrs(ips ...string) []netip.Addr {
	var out []netip.Addr
	for _, s := range ips {
		if a, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil {
			out = append(out, a.Unmap())
		}
	}
	return out
}

// validProbeHost accepts an RFC 1123 hostname (no port, no IP literal, no
// userinfo, no scheme).
func validProbeHost(h string) bool {
	if h == "" || len(h) > 253 || strings.HasSuffix(h, ".") {
		return false
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// probeError turns a transport error into a short reason without leaking
// internals.
func probeError(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "timed out"
	case strings.Contains(err.Error(), "refused: "):
		return "refused: the hostname resolves to an address that is not public or the service's server"
	case strings.Contains(err.Error(), "dns:"):
		return "the hostname does not resolve"
	case strings.Contains(err.Error(), "x509") || strings.Contains(err.Error(), "tls:"):
		return "TLS problem: the certificate is not valid yet or not trusted"
	case strings.Contains(err.Error(), "connection refused"):
		return "connection refused"
	}
	return "request failed"
}
