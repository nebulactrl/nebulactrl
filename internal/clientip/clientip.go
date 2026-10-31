// Package clientip answers one question for every rate limiter that guards a
// public endpoint: which address is the person on the other end of this
// request?
//
// The control plane sits behind the control-plane host's Traefik, so the TCP
// peer it sees is the proxy, never the visitor. Traefik names the address it
// saw in X-Forwarded-For (appended last), and when that address belongs to
// Cloudflare the visitor is the address Cloudflare saw: CF-Connecting-IP, which
// Cloudflare sets itself and the visitor cannot choose.
//
// Headers are read only when the peer is the local proxy: a peer on a
// loopback, private or link-local address (the edge network the proxy shares
// with the control plane). Any other peer reached the control plane directly,
// could have written every header, and is keyed on its own TCP address.
// CF-Connecting-IP is read only when the last X-Forwarded-For entry, the
// address the proxy itself saw connect, is in Cloudflare's ranges.
package clientip

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
)

// cloudflare reports whether an address belongs to Cloudflare's edge. It is set
// by the app from the instance's current range list; until then (tests, a bare
// handler) no address counts as Cloudflare.
var cloudflare atomic.Pointer[func(netip.Addr) bool]

// SetCloudflareRanges installs the Cloudflare range check. A nil f removes it.
func SetCloudflareRanges(f func(netip.Addr) bool) {
	if f == nil {
		cloudflare.Store(nil)
		return
	}
	cloudflare.Store(&f)
}

func isCloudflare(a netip.Addr) bool {
	if f := cloudflare.Load(); f != nil {
		return (*f)(a)
	}
	return false
}

// FromRequest returns the visitor's address for r: invalid only when
// r.RemoteAddr is not an address at all.
func FromRequest(r *http.Request) netip.Addr {
	return Resolve(r.RemoteAddr, r.Header)
}

// String is FromRequest as the string a limiter keys on ("" when unknown).
func String(r *http.Request) string {
	if ip := FromRequest(r); ip.IsValid() {
		return ip.String()
	}
	return ""
}

// Resolve returns the visitor's address given the TCP peer ("host:port" or a
// bare address) and the request headers. See the package comment for what is
// trusted.
func Resolve(remoteAddr string, h http.Header) netip.Addr {
	peer := parse(remoteAddr)
	if !peer.IsValid() || !MayForward(peer) {
		return peer
	}
	entries := forwardedFor(h)
	if len(entries) == 0 {
		// No X-Forwarded-For: the proxy did not describe the connection.
		// X-Real-IP is the same claim in another header.
		if a := parse(h.Get("X-Real-IP")); a.IsValid() {
			return a
		}
		return peer
	}
	// The last entry is the address the proxy saw connect; it is the only one
	// that cannot have been written by the visitor. A malformed last entry is
	// not skipped in favour of an earlier, visitor-controlled one.
	last := parse(entries[len(entries)-1])
	if !last.IsValid() {
		return peer
	}
	if isCloudflare(last) {
		if a := parse(h.Get("CF-Connecting-IP")); a.IsValid() {
			return a
		}
		if len(entries) > 1 {
			// Cloudflare appends the address that connected to it.
			if a := parse(entries[len(entries)-2]); a.IsValid() {
				return a
			}
		}
	}
	return last
}

// MayForward reports whether a TCP peer is the local proxy, whose forwarding
// headers are believed: loopback, private or link-local.
func MayForward(a netip.Addr) bool {
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast()
}

func forwardedFor(h http.Header) []string {
	var out []string
	for _, line := range h.Values("X-Forwarded-For") {
		for _, e := range strings.Split(line, ",") {
			if e = strings.TrimSpace(e); e != "" {
				out = append(out, e)
			}
		}
	}
	return out
}

// parse reads "host:port", "[v6]:port", "v6" or "v4": an IPv4-mapped IPv6
// address comes back unmapped and a zone is dropped. Invalid input gives the
// zero Addr.
func parse(s string) netip.Addr {
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
