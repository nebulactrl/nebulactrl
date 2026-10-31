package domains

import (
	"context"
	"fmt"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// validateDNS01 checks the DNS-01 side of a domain rule: a named resolver must
// exist on the service's server (and the agent must support it), and a wildcard
// hostname that is served over TLS needs either its own resolver or a wildcard
// certificate of the server that covers it (HTTP-01 cannot validate a wildcard).
// A hostname in a zone of the team's connected Cloudflare account needs neither:
// its certificate is obtained with DNS-01 through Cloudflare automatically
// (dns01.ApplyAuto), so naming the cloudflare resolver is accepted without one
// on the server and a wildcard hostname is accepted without a resolver.
func (s *Service) validateDNS01(ctx context.Context, q *db.Queries, srv *db.Server, sp spec) error {
	bad := func(code, format string, a ...any) error {
		return apierr.Unprocessable(code, fmt.Sprintf(format, a...))
	}
	auto := false
	if srv != nil && sp.TLSMode == agentprotocol.TLSAuto {
		var err error
		if auto, err = dns01.InConnectedZone(ctx, q, *srv, sp.Hostname); err != nil {
			return err
		}
	}
	if sp.DNSResolver != "" && !(auto && sp.DNSResolver == dns01.AutoProvider) {
		prov, ok := agentprotocol.DNSProvider(sp.DNSResolver)
		if !ok {
			return bad("invalid_dns_resolver", "dnsResolver must be empty or a lego DNS provider code, for example cloudflare, route53 or ovh")
		}
		if sp.TLSMode != agentprotocol.TLSAuto {
			return bad("invalid_tls_mode", "dnsResolver applies to tlsMode auto only")
		}
		if srv == nil {
			return bad("no_server", "assign the service to a server before choosing a DNS-01 resolver")
		}
		if err := dns01.RequireDNS01Provider(*srv, sp.DNSResolver); err != nil {
			return err
		}
		ok, err := dns01.HasResolver(ctx, q, srv.ID, sp.DNSResolver)
		if err != nil {
			return err
		}
		if !ok {
			return bad("resolver_not_configured", "%s has no %s DNS-01 resolver; add one under Servers, Proxy first", srv.Name, prov.Name)
		}
	}
	if agentprotocol.IsWildcardHost(sp.Hostname) && sp.TLSMode == agentprotocol.TLSAuto && sp.DNSResolver == "" && !auto {
		if srv == nil {
			return bad("no_server", "assign the service to a server before adding a wildcard domain")
		}
		covered, err := dns01.WildcardCovered(ctx, q, srv.ID, sp.Hostname, false)
		if err != nil {
			return err
		}
		if !covered {
			return bad("wildcard_needs_dns01", "a wildcard domain needs a DNS-01 resolver (set dnsResolver) or a wildcard certificate of the server that covers it; HTTP-01 cannot validate a wildcard")
		}
	}
	return nil
}
