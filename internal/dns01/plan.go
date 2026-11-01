package dns01

import (
	"context"
	"fmt"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// PlanChange is one staged difference of the DNS-01 configuration.
type PlanChange struct{ Field, From, To string }

// Plan is what "Deploy proxy" would change about DNS-01.
type Plan struct {
	Diff []PlanChange
	// Restart is true when the Traefik container is recreated: a resolver is
	// static configuration, and the credentials are its environment.
	Restart  bool
	Reasons  []string
	Warnings []string
}

// PlanFor compares the desired DNS-01 configuration of srv with the applied one.
func PlanFor(ctx context.Context, q *db.Queries, srv db.Server) (Plan, error) {
	want, err := loadDesired(ctx, q, srv)
	if err != nil {
		return Plan{}, err
	}
	applied := AppliedSnapshot(srv)
	var p Plan
	restart := func(reason string) {
		p.Restart = true
		for _, r := range p.Reasons {
			if r == reason {
				return
			}
		}
		p.Reasons = append(p.Reasons, reason)
	}
	const reasonStatic = "DNS-01 resolvers are part of Traefik's static configuration and their credentials are its environment, so the proxy container is recreated (about 1 second of dropped connections)."

	have := map[string]agentprotocol.DNSResolver{}
	for _, r := range applied.Resolvers {
		have[r.Provider] = r
	}
	seen := map[string]bool{}
	for _, r := range want.resolvers {
		seen[r.Provider] = true
		spec := want.resolverSpec(r)
		field := "dns01.resolver." + r.Provider
		a, ok := have[r.Provider]
		switch {
		case !ok:
			p.Diff = append(p.Diff, PlanChange{Field: field, From: "-", To: "credential " + r.CredentialName})
			restart(reasonStatic)
			if r.Auto {
				p.Warnings = append(p.Warnings, "This server serves names in zones of your connected Cloudflare account, so the proxy gets a Cloudflare DNS-01 resolver with the connection's token. Their certificates are requested through the Cloudflare API from now on instead of HTTP-01; until this is applied they stay on HTTP-01, and a certificate that is already issued stays in use.")
			}
		default:
			if a.ID != spec.ID {
				p.Diff = append(p.Diff, PlanChange{Field: field + ".credential", From: a.ID, To: r.CredentialName})
				restart(reasonStatic)
			} else if a.Rev != spec.Rev {
				p.Diff = append(p.Diff, PlanChange{Field: field + ".credential", From: "previous values", To: "rotated values of " + r.CredentialName})
				restart(reasonStatic)
			}
			if a.DelaySeconds != spec.DelaySeconds {
				p.Diff = append(p.Diff, PlanChange{Field: field + ".delaySeconds", From: fmt.Sprint(a.DelaySeconds), To: fmt.Sprint(spec.DelaySeconds)})
				restart(reasonStatic)
			}
			if a.CAServer != spec.CAServer {
				p.Diff = append(p.Diff, PlanChange{Field: field + ".staging", From: onOff(a.CAServer != ""), To: onOff(spec.CAServer != "")})
				restart(reasonStatic)
			}
		}
		if r.Staging {
			p.Warnings = append(p.Warnings, "The "+r.Provider+" resolver uses the Let's Encrypt staging CA: browsers do not trust its certificates. Use it to test, then switch it off.")
		}
	}
	for _, a := range applied.Resolvers {
		if !seen[a.Provider] {
			p.Diff = append(p.Diff, PlanChange{Field: "dns01.resolver." + a.Provider, From: "configured", To: "removed"})
			restart(reasonStatic)
		}
	}

	haveW := map[string]agentprotocol.WildcardCert{}
	for _, w := range applied.Wildcards {
		haveW[w.ID] = w
	}
	base, hasBase := baseDomainOf(srv)
	liveW := map[string]bool{}
	for _, w := range want.wildcards {
		spec := want.wildcardSpec(w)
		liveW[spec.ID] = true
		field := "dns01.wildcard." + w.Domain
		if a, ok := haveW[spec.ID]; !ok {
			p.Diff = append(p.Diff, PlanChange{Field: field, From: "-", To: "*." + w.Domain + " via " + w.Provider})
			p.Warnings = append(p.Warnings, fmt.Sprintf("Requesting *.%s needs %s to accept TXT records at _acme-challenge.%s. The certificate shows up here once Let's Encrypt issued it.", w.Domain, w.Provider, w.Domain))
		} else if a != spec {
			p.Diff = append(p.Diff, PlanChange{Field: field, From: describeWildcard(a), To: describeWildcard(spec)})
		}
		if w.CoverGenerated && hasBase && base != w.Domain {
			p.Warnings = append(p.Warnings, fmt.Sprintf("*.%s covers generated domains only when it matches the server's base domain (%s).", w.Domain, base))
		}
		if w.CoverGenerated && !hasBase {
			p.Warnings = append(p.Warnings, "*."+w.Domain+" is set to cover generated domains, but the server has no base domain: its generated domains live under sslip.io.")
		}
	}
	for _, a := range applied.Wildcards {
		if !liveW[a.ID] {
			p.Diff = append(p.Diff, PlanChange{Field: "dns01.wildcard." + a.Domain, From: "*." + a.Domain, To: "removed"})
		}
	}
	if (len(want.resolvers) > 0 || len(want.wildcards) > 0) && derefStr(srv.AcmeEmail) == "" {
		p.Warnings = append(p.Warnings, "The server has no ACME email: the proxy refuses DNS-01 configuration until one is set.")
	}
	return p, nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func describeWildcard(w agentprotocol.WildcardCert) string {
	parts := []string{strings.Join(w.Names(), ", ")}
	if w.CoverGenerated {
		parts = append(parts, "covers generated domains")
	}
	if w.Default {
		parts = append(parts, "default certificate")
	}
	return strings.Join(parts, "; ")
}
