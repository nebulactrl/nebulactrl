package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// DNS-01 resolvers. Each resolver is static Traefik configuration
// (a certificatesresolvers.* group), so adding, removing or rotating one
// recreates the container. The provider credentials are environment variables
// of that container: lego reads them from the process environment.

// sortedResolvers returns resolvers ordered by provider (deterministic flags,
// env and spec hash).
func sortedResolvers(in []agentprotocol.DNSResolver) []agentprotocol.DNSResolver {
	out := append([]agentprotocol.DNSResolver(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// dnsFlags renders the static flags of the DNS-01 resolvers: one ACME resolver
// per provider with its own storage file (Traefik cannot share one), the dns
// challenge provider code, an optional propagation delay and an optional CA.
func dnsFlags(email string, resolvers []agentprotocol.DNSResolver) []string {
	var f []string
	for _, r := range sortedResolvers(resolvers) {
		n := "--certificatesresolvers." + r.Name() + ".acme."
		f = append(f,
			n+"email="+email,
			n+"storage="+agentprotocol.DNSResolverStorage(r.Provider),
			n+"dnschallenge=true",
			n+"dnschallenge.provider="+r.Provider,
		)
		if r.DelaySeconds > 0 {
			f = append(f, n+"dnschallenge.propagation.delaybeforechecks="+strconv.Itoa(r.DelaySeconds)+"s")
		}
		if r.CAServer != "" {
			f = append(f, n+"caserver="+r.CAServer)
		}
	}
	return f
}

// dnsEnv renders the credential variables of the resolvers as KEY=VALUE pairs
// of the Traefik container, sorted by name. Names outside a provider's
// catalog are dropped (ValidateDNSResolver rejects them earlier; this keeps the
// container environment bounded even for a hand-built config).
func dnsEnv(resolvers []agentprotocol.DNSResolver) []string {
	var env []string
	for _, r := range sortedResolvers(resolvers) {
		spec, ok := agentprotocol.DNSProvider(r.Provider)
		if !ok {
			continue
		}
		for _, field := range spec.Fields {
			if v, ok := r.Env[field.Env]; ok && v != "" {
				env = append(env, field.Env+"="+v)
			}
		}
	}
	sort.Strings(env)
	return env
}

// dnsFingerprint is what the spec hash records of the credentials: a digest of
// the environment, so rotating a token recreates the container without the
// value appearing in a label.
func dnsFingerprint(env []string) string {
	h := sha256.New()
	for _, e := range env {
		h.Write([]byte(e))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// validateDNS01 checks the DNS-01 part of the configuration the next Apply
// will run with, including that every credential variable was resolved from
// the command's secrets.
func validateDNS01(cfg agentprotocol.ProxyConfig, acmeEmail string) error {
	if err := agentprotocol.ValidateDNS01(cfg, acmeEmail); err != nil {
		return err
	}
	for _, r := range cfg.DNSResolvers {
		for _, n := range r.EnvNames {
			if r.Env[n] == "" {
				return fmt.Errorf("resolver %s: the value of %s was not delivered with the command", r.Provider, n)
			}
		}
	}
	return nil
}

// resolveDNSSecrets fills DNSResolver.Env from secrets. A variable named in
// EnvNames whose secret is absent is an error unless the resolver already
// carries it (a configuration the agent resolved earlier and kept). Errors name
// variables and credential IDs, never values.
func resolveDNSSecrets(rc *agentprotocol.ProxyConfig, secrets map[string]string) error {
	for i := range rc.DNSResolvers {
		r := &rc.DNSResolvers[i]
		env := make(map[string]string, len(r.EnvNames))
		for _, n := range r.EnvNames {
			v, ok := secrets[agentprotocol.DNSSecretKey(r.ID, n)]
			if !ok {
				if old := r.Env[n]; old != "" {
					env[n] = old
					continue
				}
				return fmt.Errorf("DNS credential %s: %s is missing: it was not delivered with the command", r.ID, n)
			}
			if err := agentprotocol.ValidateDNSValue(v); err != nil {
				return fmt.Errorf("DNS credential %s: %s: %v", r.ID, n, err)
			}
			env[n] = v
		}
		r.Env = env
	}
	return nil
}

func cloneDNS(in []agentprotocol.DNSResolver) []agentprotocol.DNSResolver {
	if in == nil {
		return nil
	}
	out := make([]agentprotocol.DNSResolver, len(in))
	for i, r := range in {
		out[i] = r
		out[i].EnvNames = append([]string(nil), r.EnvNames...)
		if r.Env != nil {
			out[i].Env = make(map[string]string, len(r.Env))
			for k, v := range r.Env {
				out[i].Env[k] = v
			}
		}
	}
	return out
}

// resolverSet returns the set of provider codes with a resolver.
func resolverSet(in []agentprotocol.DNSResolver) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, r := range in {
		out[r.Provider] = true
	}
	return out
}

// wildcardSNI is the SNI used to ask the proxy for a wildcard's
// certificate.
func wildcardSNI(w agentprotocol.WildcardCert) string { return "nc-probe." + w.Domain }

// trimErr bounds a message shown in the UI.
func trimErr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}
