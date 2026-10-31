package proxy

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Cloudflare origin lock, agent side (contract: pkg/agentprotocol/cforigin.go).
//
// A locked domain (DomainRule.OriginLocked) gets, on every router that serves it
// (https and http; the probe router is verification only and is not locked):
//
//  1. the CF peer lock: an ipAllowList of Cloudflare's ranges, FIRST in the
//     middleware chain. It has no ipStrategy, so it checks the TCP peer
//     (RemoteAddrStrategy: "ipStrategy absent = request.RemoteAddr", see
//     pkg/ip/strategy.go and pkg/middlewares/ipallowlist in Traefik v3.7.16 and
//     https://doc.traefik.io/traefik/reference/routing-configuration/http/middlewares/ipallowlist/).
//     Any other peer gets Traefik's plain "Forbidden" 403 body, nothing internal.
//  2. Authenticated Origin Pulls (DomainRule.RequireClientCert): the https router
//     references a TLS option that requires a client certificate verified
//     against the pinned Cloudflare origin-pull CA (RequireAndVerifyClientCert).
//     The CA PEM is carried inline in clientAuth.caFiles: Traefik's FileOrContent
//     type takes the content itself (the agent already ships uploaded certificates
//     the same way), so no file has to be written into, or kept readable by, the
//     container. The option is named by a hash of the CA, so a CA refresh is a
//     plain dynamic-config change that reaches new handshakes without a recreate.
//  3. the real visitor address: see originLockRealIPDepth.
//
// Middleware order of a locked domain: lock, then the user's rules (ipAllowList,
// rateLimit, basicAuth, ...), then the rest. The errors middleware (pages), when
// enabled, is outermost as everywhere else: it only rewrites 502-504 responses.
//
// ACME HTTP-01 is not behind the lock. Traefik answers
// /.well-known/acme-challenge/<token> on the web entrypoint from its internal
// acme-http router, which has the highest possible priority and no middlewares,
// so no user router, redirect or lock ever sees the validation request,
// whichever address it comes from. Let's Encrypt reaches a proxied domain through
// Cloudflare, so the validation request arrives from Cloudflare's ranges anyway
// (it would pass the lock too). What Cloudflare does with the request before it
// reaches the origin is configuration of the zone: with "Always Use HTTPS" or an
// SSL mode that contacts the origin on :443, the HTTP-01 request never arrives
// on :80. A locked domain whose certificate is issued over DNS-01 does not depend
// on that: for a name in a zone of the team's connected Cloudflare account whose
// record a DNS sync wrote, the control plane names the cloudflare DNS-01
// resolver on its rule (DomainRule.DNSResolver, internal/dns01 ApplyAuto), and
// DNS-01 never touches :80. Elsewhere a locked domain whose certificate cannot be
// validated that way needs a DNS-01 resolver set by hand or a Cloudflare
// configuration rule that lets the challenge path through; the lock does not
// change that.
// The client-certificate requirement is HTTPS only (a TLS option), so it can
// never affect the challenge on :80.

// originLockRealIPDepth is the X-Forwarded-For position, counted from the
// right, of the visitor address on a locked domain: the user's IP allow/deny
// rules and the rate limit evaluate ipStrategy.depth 1.
//
// Why X-Forwarded-For depth 1, and not CF-Connecting-IP:
// names: Traefik's ipStrategy can read exactly two sources, the TCP peer and
// X-Forwarded-For (ip.RemoteAddrStrategy, ip.DepthStrategy, ip.PoolStrategy in
// Traefik v3.7.16 pkg/ip/strategy.go); there is no option that names another
// header. What Cloudflare puts in X-Forwarded-For is documented: it appends the
// address that connected to it to whatever the visitor sent
// ("X-Forwarded-For: <visitor-supplied>, <connecting IP>",
// https://developers.cloudflare.com/fundamentals/reference/http-headers/), and
// that connecting IP is the CF-Connecting-IP value. Counting from the right
// therefore picks the address Cloudflare saw, and everything left of it is
// attacker-controlled and never read. Depth 1 is safe only because
//
//   - the entrypoints trust X-Forwarded-For solely from Cloudflare's ranges
//     (forwardedHeaders.trustedIPs, see TrustedProxyCIDRs): for any other peer
//     Traefik deletes the header before any middleware runs
//     (forwardedheaders.XForwarded.ServeHTTP, "if !insecure && !isTrustedIP:
//     DeleteXForwardedHeaders"; https://doc.traefik.io/traefik/reference/install-configuration/entrypoints/), and
//   - the lock runs first, so a peer outside the ranges is answered 403 before
//     the user's rules look at a header at all.
//
// A user-chosen TrustedProxyDepth is ignored on a locked domain: a larger depth
// would read an entry the visitor can forge. excludedIPs (PoolStrategy) is not
// used either: it skips Cloudflare ranges, so a visitor behind Cloudflare
// itself (a Worker) would make the scan fall through to a forged entry.
// A request that reaches the user's rule without an X-Forwarded-For (only
// possible from a trusted peer that did not send one) has no visitor address;
// Traefik then rejects it (empty address, fails closed).
const originLockRealIPDepth = 1

const (
	lockMiddlewarePrefix = "nc-cflock-"
	aopOptionPrefix      = "nc-aop-"
	// aopClientAuthType makes Traefik refuse a handshake without a client
	// certificate chaining to the CA (never VerifyClientCertIfGiven).
	aopClientAuthType = "RequireAndVerifyClientCert"
)

// dynTLSOption is a named entry of the dynamic config's tls.options.
type dynTLSOption struct {
	ClientAuth *dynClientAuth `json:"clientAuth,omitempty"`
}

type dynClientAuth struct {
	// CAFiles carries inline PEM content (types.FileOrContent).
	CAFiles        []string `json:"caFiles"`
	ClientAuthType string   `json:"clientAuthType"`
}

// TrustedProxyCIDRs is the set the entrypoints trust X-Forwarded-For from
// (forwardedHeaders.trustedIPs): the union of the Cloudflare ranges of the
// routes that have a locked domain, sorted, masked and de-duplicated so the
// answer is a stable fingerprint. Empty when nothing is locked: the entrypoints
// then keep Traefik's default of trusting nobody, and a server without a
// locked domain never recreates its proxy for a Cloudflare list change.
func TrustedProxyCIDRs(routes []agentprotocol.Route) []string {
	var all []string
	for _, r := range routes {
		if r.IsL4() || r.OriginLock == nil || !hasLockedDomain(r) {
			continue
		}
		all = append(all, r.OriginLock.CloudflareCIDRs...)
	}
	return normalizeCIDRs(all)
}

func hasLockedDomain(r agentprotocol.Route) bool {
	for _, d := range r.Domains {
		if d.OriginLocked {
			return true
		}
	}
	return false
}

// normalizeCIDRs returns the valid prefixes of in, masked to their network
// address, sorted and without duplicates. Invalid entries are dropped here;
// ValidateRoutes rejects them before anything is rendered.
func normalizeCIDRs(in []string) []string {
	seen := make(map[string]bool, len(in))
	var out []string
	for _, c := range in {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			continue
		}
		s := p.Masked().String()
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// withLock puts the lock middleware (when set) in front of mws.
func withLock(lock string, mws ...string) []string {
	if lock == "" {
		return mws
	}
	return append([]string{lock}, mws...)
}

// lockFor registers the lock middleware of a locked domain and returns its
// name, or "" for an unlocked domain. The name is derived from the CIDR set, so
// every router that uses the same ranges shares one middleware. A locked domain
// whose route has no valid range fails closed (nothing matches the allow list);
// ValidateRoutes rejects that table first.
func (b *builder) lockFor(t target, d agentprotocol.DomainRule) string {
	if !d.OriginLocked {
		return ""
	}
	var cidrs []string
	if t.origin != nil {
		cidrs = normalizeCIDRs(t.origin.CloudflareCIDRs)
	}
	if len(cidrs) == 0 {
		cidrs = []string{neverMatch}
	}
	name := lockMiddlewarePrefix + specHash(cidrs...)[:8]
	if _, ok := b.cfg.HTTP.Middlewares[name]; !ok {
		// No ipStrategy: the peer address is checked.
		b.addMiddleware(name, &dynMiddleware{IPAllowList: &dynIPAllowList{SourceRange: cidrs}})
	}
	return name
}

// aopFor adds the client-certificate requirement to the TLS block of a locked
// domain's https router when the domain asks for it.
func (b *builder) aopFor(t target, d agentprotocol.DomainRule, tls *dynTLS) *dynTLS {
	if !d.OriginLocked || !d.RequireClientCert || t.origin == nil || strings.TrimSpace(t.origin.ClientCAPEM) == "" {
		return tls
	}
	pem := strings.TrimSpace(t.origin.ClientCAPEM) + "\n"
	sum := sha256.Sum256([]byte(pem))
	name := aopOptionPrefix + hex.EncodeToString(sum[:])[:8]
	if b.tlsOptions == nil {
		b.tlsOptions = map[string]*dynTLSOption{}
	}
	b.tlsOptions[name] = &dynTLSOption{ClientAuth: &dynClientAuth{CAFiles: []string{pem}, ClientAuthType: aopClientAuthType}}
	tls.Options = name
	return tls
}

func (b *builder) attachTLSOptions() {
	if len(b.tlsOptions) == 0 {
		return
	}
	if b.cfg.TLS == nil {
		b.cfg.TLS = &dynTLSConfig{}
	}
	b.cfg.TLS.Options = b.tlsOptions
}

// validateOriginLocks rejects the route tables whose origin lock cannot be
// rendered the way it was asked for. Every case is one where the alternative to
// an error would be a domain that looks locked and is not: the table is
// refused whole and the previous one keeps serving.
func validateOriginLocks(routes []agentprotocol.Route) error {
	type lockState struct{ locked, cert bool }
	hostState := map[string]lockState{}
	for i, r := range routes {
		if r.OriginLock != nil && r.IsL4() {
			return fmt.Errorf("route %d (service %s): originLock only applies to http routes", i, r.ServiceID)
		}
		for _, d := range r.Domains {
			if !d.OriginLocked {
				if d.RequireClientCert {
					return fmt.Errorf("route %d (service %s): domain %s requires a client certificate but is not origin-locked", i, r.ServiceID, d.Host)
				}
			} else if err := validateLockedDomain(i, r, d); err != nil {
				return err
			}
			st := lockState{locked: d.OriginLocked, cert: d.RequireClientCert}
			if prev, ok := hostState[d.Host]; ok && prev != st {
				// One host has one TLS option (chosen by SNI) and one IP policy.
				return fmt.Errorf("route %d (service %s): domain rules of %s disagree on originLocked/requireClientCert", i, r.ServiceID, d.Host)
			}
			hostState[d.Host] = st
		}
	}
	return nil
}

func validateLockedDomain(i int, r agentprotocol.Route, d agentprotocol.DomainRule) error {
	where := fmt.Sprintf("route %d (service %s): domain %s", i, r.ServiceID, d.Host)
	if r.OriginLock == nil {
		return fmt.Errorf("%s is origin-locked but the route has no originLock", where)
	}
	if d.Tunnel {
		return fmt.Errorf("%s is origin-locked but is served over a tunnel (no public origin to lock)", where)
	}
	if d.Kind == agentprotocol.DomainRedirect {
		return fmt.Errorf("%s is origin-locked but is a redirect domain", where)
	}
	if len(r.OriginLock.CloudflareCIDRs) == 0 {
		return fmt.Errorf("%s is origin-locked but originLock has no Cloudflare ranges", where)
	}
	for _, c := range r.OriginLock.CloudflareCIDRs {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			return fmt.Errorf("%s: invalid Cloudflare range %q", where, c)
		}
		if p.Bits() == 0 {
			// Trusting or allowing the whole internet is the opposite of a lock.
			return fmt.Errorf("%s: Cloudflare range %q matches every address", where, c)
		}
	}
	if !d.RequireClientCert {
		return nil
	}
	if d.TLSMode == agentprotocol.TLSNone {
		return fmt.Errorf("%s requires a client certificate but has TLS disabled", where)
	}
	if agentprotocol.IsWildcardHost(d.Host) {
		// Traefik derives a router's TLS option from its Host() names; a
		// HostRegexp rule has none, so the requirement would silently not apply.
		return fmt.Errorf("%s requires a client certificate but is a wildcard host (Traefik cannot select a TLS option for it)", where)
	}
	if !x509.NewCertPool().AppendCertsFromPEM([]byte(r.OriginLock.ClientCAPEM)) {
		return fmt.Errorf("%s requires a client certificate but originLock.clientCaPem holds no certificate", where)
	}
	return nil
}
