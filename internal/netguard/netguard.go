// Package netguard keeps the control plane's outbound requests to
// tenant-influenced URLs (git remotes, git provider base URLs) away from the
// host's own network: loopback, private, link-local, carrier-grade NAT and
// cloud metadata addresses are refused at connect time, on the address that
// was actually resolved, so DNS rebinding and redirects cannot get around the
// check.
//
// Self-hosted forges on a private network are allowed by the instance admin
// through NEBULACTRL_GIT_ALLOW_PRIVATE (see AllowEnv). Link-local and metadata
// addresses stay refused whatever the list says.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"time"
)

// AllowEnv names the environment variable with the destinations the instance
// admin allows although they are private: a comma-separated list of host
// names (git.corp.example), IP addresses or CIDR ranges (10.0.0.0/24). Entries
// that are not valid are ignored. Link-local and metadata addresses cannot be
// allowed.
const AllowEnv = "NEBULACTRL_GIT_ALLOW_PRIVATE"

// ErrBlocked is returned for a destination that is not allowed.
var ErrBlocked = errors.New("refused: the address is private, loopback, link-local or a metadata service; an instance admin can allow a self-hosted forge with " + AllowEnv)

// Allow is the instance admin's list of private destinations that are allowed.
type Allow struct {
	hosts    map[string]bool
	prefixes []netip.Prefix
}

// AllowFromEnv reads AllowEnv. It is cheap, so it is read per connection and
// a change takes effect without a restart of the connection pool.
func AllowFromEnv() Allow { return ParseAllow(os.Getenv(AllowEnv)) }

// ParseAllow parses a comma-separated allow list.
func ParseAllow(list string) Allow {
	var a Allow
	for _, e := range strings.Split(list, ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if p, err := netip.ParsePrefix(e); err == nil {
			a.prefixes = append(a.prefixes, netip.PrefixFrom(p.Addr().Unmap(), p.Bits()).Masked())
			continue
		}
		if ip, err := netip.ParseAddr(e); err == nil {
			ip = ip.Unmap()
			a.prefixes = append(a.prefixes, netip.PrefixFrom(ip, ip.BitLen()))
			continue
		}
		if a.hosts == nil {
			a.hosts = map[string]bool{}
		}
		a.hosts[strings.TrimSuffix(e, ".")] = true
	}
	return a
}

func (a Allow) host(h string) bool {
	return a.hosts[strings.TrimSuffix(strings.ToLower(h), ".")]
}

func (a Allow) ip(ip netip.Addr) bool {
	for _, p := range a.prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

var (
	// neverAllowed are refused even when the admin lists them: link-local
	// (cloud metadata at 169.254.169.254 and fd00:ec2::254) and "this network".
	neverAllowed = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("fe80::/10"),
		netip.MustParsePrefix("fd00:ec2::/32"),
	}
	// nonPublic are refused unless the admin lists them.
	nonPublic = []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT (Tailscale, cloud internals)
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("fc00::/7"),
		netip.MustParsePrefix("64:ff9b::/96"), // NAT64 embeds an IPv4 address
		netip.MustParsePrefix("2002::/16"),    // 6to4 embeds an IPv4 address
	}
)

// Blocked reports whether ip must not be dialed (allow lists private ranges
// the admin permits).
func Blocked(ip netip.Addr, allow Allow) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	for _, p := range neverAllowed {
		if p.Contains(ip) {
			return true
		}
	}
	if allow.ip(ip) {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return true
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return true
		}
	}
	return !ip.IsGlobalUnicast()
}

// control refuses the resolved address right before the connection is made.
func control(allow Allow) func(network, address string, _ syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return ErrBlocked
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || Blocked(ip, allow) {
			return ErrBlocked
		}
		return nil
	}
}

// DialContext returns a dial function that refuses blocked destinations. A
// host name the admin allowed is dialed as is (the admin vouches for it).
func DialContext(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		allow := AllowFromEnv()
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, ErrBlocked
		}
		d := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
		if _, perr := netip.ParseAddr(host); perr != nil && allow.host(host) {
			return d.DialContext(ctx, network, addr)
		}
		d.Control = control(allow)
		return d.DialContext(ctx, network, addr)
	}
}

// CheckHost resolves host and fails with ErrBlocked when any of its addresses
// is blocked. It guards protocols whose dialer the control plane does not own
// (git://, ssh); the HTTP client checks at connect time instead.
func CheckHost(ctx context.Context, host string) error {
	allow := AllowFromEnv()
	host = strings.Trim(host, "[]")
	if host == "" {
		return ErrBlocked
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if Blocked(ip, allow) {
			return ErrBlocked
		}
		return nil
	}
	if allow.host(host) {
		return nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("dns: %s does not resolve", host)
	}
	for _, ip := range ips {
		if Blocked(ip, allow) {
			return ErrBlocked
		}
	}
	return nil
}

// maxRedirects bounds the redirects of a guarded client.
const maxRedirects = 5

// NewTransport returns an HTTP transport whose connections are guarded. It
// ignores proxy environment variables: a proxy would receive the destination
// as a name and could reach what the guard refuses.
func NewTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           DialContext(10 * time.Second),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// NewClient returns an HTTP client for tenant-influenced URLs: guarded
// connections, at most five redirects and no downgrade from https to http.
// Every redirect target is dialed through the guard, so a redirect to an
// internal address is refused.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: NewTransport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return errors.New("refused: redirect from https to http")
			}
			return nil
		},
	}
}
