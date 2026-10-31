package proxy

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var errNoKey = errors.New("no private key")

// ResolveSecrets fills the agent-side resolved fields from the command's
// secrets: DomainRule.AuthUsers from Secrets[AuthRef] ("user:bcrypt" lines,
// validated) for every domain with basic auth, and CertRef.KeyPEM from
// Secrets[KeyRef]. It returns copies; its inputs are not modified. A domain or
// certificate whose secret is absent is an error unless the input already
// carries the resolved value (a table the agent resolved earlier and kept): the
// route table must never be applied with an unprotected domain or a certificate
// without its key. Errors name keys and IDs, never values.
func ResolveSecrets(routes []agentprotocol.Route, cfg *agentprotocol.ProxyConfig, secrets map[string]string) ([]agentprotocol.Route, *agentprotocol.ProxyConfig, error) {
	out := cloneRoutes(routes)
	for i := range out {
		for j := range out[i].Domains {
			d := &out[i].Domains[j]
			if !d.Options.BasicAuth {
				continue
			}
			raw, ok := secrets[d.AuthRef]
			if d.AuthRef == "" || !ok {
				if len(d.AuthUsers) > 0 {
					continue
				}
				return nil, nil, fmt.Errorf("basic-auth users for domain %s (%s) are missing from the command's secrets", d.Host, d.ID)
			}
			var users []string
			for _, l := range strings.Split(raw, "\n") {
				if l = strings.TrimSpace(l); l != "" {
					users = append(users, l)
				}
			}
			if len(users) == 0 {
				return nil, nil, fmt.Errorf("basic-auth users for domain %s (%s) are empty", d.Host, d.ID)
			}
			if err := agentprotocol.ValidBasicAuthLines(users); err != nil {
				return nil, nil, fmt.Errorf("basic-auth users for domain %s (%s): %w", d.Host, d.ID, err)
			}
			d.AuthUsers = users
		}
	}
	if cfg == nil {
		return out, nil, nil
	}
	rc := cloneProxyConfig(*cfg)
	for i := range rc.Certificates {
		c := &rc.Certificates[i]
		if key, ok := secrets[c.KeyRef]; ok && c.KeyRef != "" {
			c.KeyPEM = key
		}
		if c.KeyPEM == "" {
			return nil, nil, fmt.Errorf("private key of certificate %s (%s) is missing from the command's secrets", c.Name, c.ID)
		}
	}
	if err := resolveDNSSecrets(&rc, secrets); err != nil {
		return nil, nil, err
	}
	return out, &rc, nil
}

// CertStatuses validates every certificate of cfg and returns the ones that can
// be served plus one status per certificate. A pair that does not parse is
// excluded (Loaded=false) so it cannot break the whole config.
func CertStatuses(certs []agentprotocol.CertRef) (usable []agentprotocol.CertRef, status []agentprotocol.CertStatus) {
	for _, c := range certs {
		if err := certUsable(c); err != nil {
			status = append(status, agentprotocol.CertStatus{ID: c.ID, Error: "certificate not loaded: " + err.Error()})
			continue
		}
		usable = append(usable, c)
		status = append(status, agentprotocol.CertStatus{ID: c.ID, Loaded: true})
	}
	return usable, status
}

func cloneProxyConfig(c agentprotocol.ProxyConfig) agentprotocol.ProxyConfig {
	out := c
	out.Certificates = append([]agentprotocol.CertRef(nil), c.Certificates...)
	for i := range out.Certificates {
		out.Certificates[i].Domains = append([]string(nil), c.Certificates[i].Domains...)
	}
	if c.Tailnet != nil {
		t := *c.Tailnet
		out.Tailnet = &t
	}
	out.WildcardCerts = append([]agentprotocol.WildcardCert(nil), c.WildcardCerts...)
	out.DNSResolvers = cloneDNS(c.DNSResolvers)
	return out
}
