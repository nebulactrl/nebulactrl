package agentprotocol

// Cloudflare origin lock (contract between the control plane and the agent).
//
// A domain behind Cloudflare's proxy (orange cloud) can be locked so that the
// origin only answers Cloudflare. Three layers, all rendered by the agent into
// the Traefik dynamic config of the locked domain:
//
//  1. IP lock: an ipAllowList on the direct peer address with Cloudflare's
//     published ranges (OriginLock.CloudflareCIDRs). Everything else gets 403.
//  2. Authenticated Origin Pulls: the domain's TLS option requires a client
//     certificate signed by OriginLock.ClientCAPEM (DomainRule.RequireClientCert).
//  3. Real client IP: the entrypoints trust X-Forwarded-For only from the
//     Cloudflare ranges, and the user's IP allow/deny and rate limit rules of a
//     locked domain evaluate the visitor address (see internal/agent/proxy).
//
// The route carries the shared data (the ranges and the CA) and each
// DomainRule says whether it is locked and whether it requires the client
// certificate, because one service can have a proxied domain next to a DNS-only
// one. There is no server-level field: the agent derives the entrypoints'
// forwardedHeaders.trustedIPs from the union of OriginLock.CloudflareCIDRs of
// the routes that have a locked domain, so a refresh of Cloudflare's IP list is
// one route push (sync_routes or reconcile) that changes both the lock and the
// trust set atomically.

// OriginLock is the Cloudflare data a route's locked domains share. It is nil
// when no domain of the route is locked; a locked domain (DomainRule.OriginLocked)
// on a route without it is rejected by the agent.
type OriginLock struct {
	// CloudflareCIDRs are Cloudflare's IPv4 and IPv6 ranges (CIDR notation).
	CloudflareCIDRs []string `json:"cloudflareCidrs"`
	// ClientCAPEM is the PEM of the CA that signs Cloudflare's origin-pull
	// client certificate. Needed by domains with DomainRule.RequireClientCert.
	ClientCAPEM string `json:"clientCaPem,omitempty"`
}
