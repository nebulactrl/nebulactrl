package agentprotocol

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// DNS-01 certificate resolvers and wildcard certificates. Shared by the control
// plane (validation, builders) and the
// agent (Traefik static and dynamic configuration).
//
// A server may run one ACME resolver per DNS provider. Provider credentials
// are environment variables of the Traefik container (lego reads them from the
// process environment), so the namespace is process-wide: at most one
// credential per provider per server. Values never appear in a payload: the
// control plane lists the variable NAMES (DNSResolver.EnvNames) and attaches
// the values to the outgoing frame as Secrets[SecretDNSPrefix+<credentialId>+":"+<NAME>].
// The agent resolves them into DNSResolver.Env, which is the only place a
// value lives next to the rest of the proxy configuration (persisted in the
// agent's 0600 bbolt file, like uploaded certificate keys).

// CapDNS01 is advertised in Facts.Capabilities by agents that render DNS-01
// resolvers. The control plane refuses to stage resolvers or wildcard
// certificates (422 agent_too_old) for servers without it.
const CapDNS01 = "dns01"

// CapDNS01Lego is advertised by agents that carry the lego provider catalog
// (dns_lego_catalog.go). Resolvers of any provider other than the original
// four (Cloudflare, Route 53, Hetzner, DigitalOcean) need it: an older agent
// would reject the unknown provider when it renders the proxy configuration.
const CapDNS01Lego = "dns01-lego"

// SecretDNSPrefix + <credential id> + ":" + <ENV NAME> → the variable's value.
const SecretDNSPrefix = "dns01:"

// DNSSecretKey is the Command.Secrets key of one credential variable.
func DNSSecretKey(credentialID, env string) string {
	return SecretDNSPrefix + credentialID + ":" + env
}

// DNS provider codes of the original four: the lego provider code, which is
// also the value of --certificatesresolvers.<name>.acme.dnschallenge.provider.
// The other curated codes are in dns01_providers.go; any other lego provider
// is reachable through the generic form (dns_lego.go).
const (
	DNSCloudflare   = "cloudflare"
	DNSRoute53      = "route53"
	DNSHetzner      = "hetzner"
	DNSDigitalOcean = "digitalocean"
)

// DNSField is one environment variable of a provider's credentials.
type DNSField struct {
	// Env is the exact variable name lego reads (go-acme/lego v5, the library
	// Traefik v3.7 vendors; https://go-acme.github.io/lego/dns/<code>/).
	Env      string `json:"env"`
	Label    string `json:"label"`
	Help     string `json:"help,omitempty"`
	Secret   bool   `json:"secret" doc:"Secret values are write-only: never returned by any read"`
	Required bool   `json:"required"`
}

// DNSProviderSpec describes a supported DNS provider.
type DNSProviderSpec struct {
	Code string `json:"code" doc:"The lego provider code"`
	Name string `json:"name"`
	Help string `json:"help,omitempty"`
	Docs string `json:"docs" doc:"The lego documentation page of the provider"`
	// Generic marks a provider without a curated form: any lego provider.
	// Its fields are every variable the lego catalog names for the code and
	// none is required on its own.
	Generic bool `json:"generic" doc:"A lego provider without a curated form: variables are free-form, validated against lego's catalog"`
	// Aliases are other names lego accepts for the code; a credential names the code.
	Aliases []string   `json:"aliases,omitempty" doc:"Other names lego accepts for the provider; a credential uses the code"`
	Fields  []DNSField `json:"fields"`
	// OneOf lists alternative authentication methods as sets of variables:
	// the credential must have every variable of at least one set.
	OneOf [][]string `json:"oneOf,omitempty" doc:"Alternative authentication methods; set every variable of at least one"`
}

// DNSProvider looks a provider up by code: a curated provider, else any lego
// provider of the catalog (a generic spec).
func DNSProvider(code string) (DNSProviderSpec, bool) {
	for _, p := range DNSProviders {
		if p.Code == code {
			return p, true
		}
	}
	if p, ok := legoLookup(code); ok {
		return legoSpec(p), true
	}
	return DNSProviderSpec{}, false
}

// DNSProviderCodes lists the curated codes in catalog order.
func DNSProviderCodes() []string {
	out := make([]string, len(DNSProviders))
	for i, p := range DNSProviders {
		out[i] = p.Code
	}
	return out
}

// DNSProviderNeedsLego reports whether resolving a provider requires an agent
// that advertises CapDNS01Lego: every provider but the original four.
func DNSProviderNeedsLego(code string) bool {
	switch code {
	case DNSCloudflare, DNSRoute53, DNSHetzner, DNSDigitalOcean:
		return false
	}
	return true
}

// DNSResolverName is the Traefik certificate resolver of a provider on a server.
func DNSResolverName(provider string) string { return "nc-dns-" + provider }

// DNSResolverStorage is the ACME storage file of a resolver on the ACME volume.
// Every resolver needs its own file: Traefik does not allow sharing one.
func DNSResolverStorage(provider string) string { return "/data/acme-" + provider + ".json" }

// Limits of a credential value.
const (
	MaxDNSValueLen      = 2048
	MaxDNSDelaySeconds  = 600
	maxCAServerURLBytes = 200
)

// DNSResolver is one DNS-01 certificate resolver of a server.
type DNSResolver struct {
	// ID is the credential the resolver uses; it names the Secrets keys.
	ID       string `json:"id"`
	Provider string `json:"provider"`
	// DelaySeconds delays the propagation check (acme.dnschallenge.propagation.delaybeforechecks).
	DelaySeconds int `json:"delaySeconds,omitempty"`
	// CAServer overrides the ACME directory, e.g. Let's Encrypt staging. "" = Let's Encrypt production.
	CAServer string `json:"caServer,omitempty"`
	// EnvNames are the variables the command's Secrets carry for this resolver.
	EnvNames []string `json:"envNames"`
	// Rev is the credential's revision (control-plane bookkeeping: a rotated
	// credential has a new Rev, which the plan shows as a change).
	Rev int `json:"rev,omitempty"`
	// Env is filled by the AGENT from Secrets (DNSSecretKey); the control plane
	// never sets it and it must not appear in a persisted payload.
	Env map[string]string `json:"env,omitempty"`
}

// Name is the Traefik resolver name.
func (r DNSResolver) Name() string { return DNSResolverName(r.Provider) }

// LetsEncryptStagingCA is the staging ACME directory (untrusted certificates,
// generous rate limits).
const LetsEncryptStagingCA = "https://acme-staging-v02.api.letsencrypt.org/directory"

// WildcardCert is a certificate for *.<Domain> (and the apex when IncludeApex)
// obtained through a server's resolver of Provider.
type WildcardCert struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	// Domain is the base domain without the wildcard label, e.g. example.com.
	Domain      string `json:"domain"`
	IncludeApex bool   `json:"includeApex"`
	// CoverGenerated lets generated domains (<service>-<env>-<project>.<Domain>)
	// use this certificate instead of requesting one per host.
	CoverGenerated bool `json:"coverGenerated,omitempty"`
	// Default makes it the proxy's default certificate
	// (tls.stores.default.defaultGeneratedCert): served to clients whose SNI
	// matches no router, instead of Traefik's self-signed one.
	Default bool `json:"default,omitempty"`
}

// Names are the certificate's subject names: the wildcard first.
func (w WildcardCert) Names() []string {
	if w.IncludeApex {
		return []string{"*." + w.Domain, w.Domain}
	}
	return []string{"*." + w.Domain}
}

// ACME certificate observation kinds and statuses.
const (
	AcmeKindWildcard = "wildcard"
	AcmeKindDomain   = "domain"

	AcmeStatusPending = "pending"
	AcmeStatusActive  = "active"
	AcmeStatusError   = "error"

	// AcmeIssuedHTTP01 and AcmeIssuedDNS01 say which ACME storage file holds the
	// certificate a host is served with: the default resolver's (HTTP-01) or the
	// DNS-01 resolver's. A name that moved to DNS-01 keeps the certificate the
	// HTTP-01 resolver issued earlier, and Traefik renews it there.
	AcmeIssuedHTTP01 = "http-01"
	AcmeIssuedDNS01  = "dns-01"
)

// AcmeCertStatus is the agent's observation of the certificate the proxy
// serves for a wildcard certificate (ID = WildcardCert.ID) or a domain served
// through a DNS-01 resolver (ID = DomainRule.ID). A domain whose host names a
// DNS-01 resolver the proxy does not run yet is reported with an empty Status
// and only IssuedVia (the certificate it still holds from HTTP-01, if any).
type AcmeCertStatus struct {
	Kind     string     `json:"kind"`
	ID       string     `json:"id"`
	Status   string     `json:"status"`
	Message  string     `json:"message,omitempty"`
	NotAfter *time.Time `json:"notAfter,omitempty"`
	Issuer   string     `json:"issuer,omitempty"`
	// IssuedVia is the ACME storage that holds the host's certificate, read from
	// the storage files' domain names (AcmeIssuedHTTP01, AcmeIssuedDNS01); empty
	// when no storage has one (or the files could not be read).
	IssuedVia string `json:"issuedVia,omitempty"`
}

var (
	dnsEnvNameRe  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
	wildcardBaseR = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)
)

// ValidWildcardBase reports whether d is a valid base domain of a wildcard
// certificate: lowercase, at least two labels, a non-numeric last label, at
// most 253 characters.
func ValidWildcardBase(d string) bool {
	return len(d) <= 253 && wildcardBaseR.MatchString(d)
}

// IsWildcardHost reports whether h is a wildcard hostname ("*." + a valid base).
func IsWildcardHost(h string) bool {
	base, ok := strings.CutPrefix(h, "*.")
	return ok && ValidWildcardBase(base)
}

// ValidRouteHost reports whether h may appear in a route's Host rule: a plain
// hostname or a wildcard hostname.
func ValidRouteHost(h string) bool {
	return ValidHostname(h) || IsWildcardHost(h)
}

// ProbeHost maps a route host to a concrete name that routes to it: a wildcard
// host gets the label "nc-probe", every other host is returned unchanged.
func ProbeHost(h string) string {
	if rest, ok := strings.CutPrefix(h, "*."); ok {
		return "nc-probe." + rest
	}
	return h
}

// WildcardCovers reports whether the wildcard certificate's names cover host: a
// single label under Domain, or the apex when IncludeApex.
func WildcardCovers(w WildcardCert, host string) bool {
	host = strings.ToLower(host)
	if rest, ok := strings.CutPrefix(host, "*."); ok {
		return rest == w.Domain
	}
	if host == w.Domain {
		return w.IncludeApex
	}
	label, parent, ok := strings.Cut(host, ".")
	return ok && label != "" && parent == w.Domain
}

// ValidateDNSResolver checks one resolver: a known provider, only the
// provider's variables, bounded values (when Env is present), a sane delay and
// CA server.
func ValidateDNSResolver(r DNSResolver) error {
	spec, ok := DNSProvider(r.Provider)
	if !ok {
		return fmt.Errorf("unknown DNS provider %q (supported: %s)", r.Provider, strings.Join(DNSProviderCodes(), ", "))
	}
	if r.ID == "" || strings.ContainsAny(r.ID, ": \n") {
		return fmt.Errorf("resolver %s: invalid credential id", r.Provider)
	}
	if r.DelaySeconds < 0 || r.DelaySeconds > MaxDNSDelaySeconds {
		return fmt.Errorf("resolver %s: delaySeconds must be between 0 and %d", r.Provider, MaxDNSDelaySeconds)
	}
	if r.CAServer != "" {
		if err := ValidateCAServer(r.CAServer); err != nil {
			return fmt.Errorf("resolver %s: %w", r.Provider, err)
		}
	}
	allowed := map[string]DNSField{}
	for _, f := range spec.Fields {
		allowed[f.Env] = f
	}
	seen := map[string]bool{}
	for _, n := range r.EnvNames {
		if !dnsEnvNameRe.MatchString(n) {
			return fmt.Errorf("resolver %s: invalid variable name %q", r.Provider, n)
		}
		if _, ok := allowed[n]; !ok {
			return fmt.Errorf("resolver %s: %s is not a %s credential variable", r.Provider, n, spec.Name)
		}
		if seen[n] {
			return fmt.Errorf("resolver %s: duplicate variable %s", r.Provider, n)
		}
		seen[n] = true
	}
	if err := CheckDNSVariables(spec, r.EnvNames); err != nil {
		return fmt.Errorf("resolver %s: %w", r.Provider, err)
	}
	for n, v := range r.Env {
		if !seen[n] {
			return fmt.Errorf("resolver %s: value for %s was not announced in envNames", r.Provider, n)
		}
		if err := ValidateDNSValue(v); err != nil {
			return fmt.Errorf("resolver %s: %s: %w", r.Provider, n, err)
		}
	}
	return nil
}

// ValidateDNSValue bounds one credential value: non-empty, printable, one line.
func ValidateDNSValue(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("the value is empty")
	}
	if len(v) > MaxDNSValueLen {
		return fmt.Errorf("the value is longer than %d bytes", MaxDNSValueLen)
	}
	if strings.ContainsAny(v, "\r\n\x00") {
		return fmt.Errorf("the value must be a single line")
	}
	return nil
}

// ValidateCAServer checks an ACME directory URL: https, a host, no credentials.
func ValidateCAServer(raw string) error {
	if len(raw) > maxCAServerURLBytes {
		return fmt.Errorf("caServer is too long")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || strings.ContainsAny(raw, " \n\r") {
		return fmt.Errorf("caServer must be an https URL without credentials")
	}
	return nil
}

// ValidateWildcardCert checks one wildcard certificate.
func ValidateWildcardCert(w WildcardCert) error {
	if w.ID == "" {
		return fmt.Errorf("wildcard certificate without id")
	}
	if _, ok := DNSProvider(w.Provider); !ok {
		return fmt.Errorf("wildcard %s: unknown DNS provider %q", w.Domain, w.Provider)
	}
	if !ValidWildcardBase(w.Domain) {
		return fmt.Errorf("wildcard %q: invalid base domain", w.Domain)
	}
	return nil
}

// ValidateDNS01 checks the DNS-01 part of a proxy configuration: unique
// resolvers per provider, wildcard certificates that name a configured
// resolver, one default, and (when anything is configured) an ACME email.
func ValidateDNS01(cfg ProxyConfig, acmeEmail string) error {
	if len(cfg.DNSResolvers) == 0 && len(cfg.WildcardCerts) == 0 {
		return nil
	}
	if acmeEmail == "" {
		return fmt.Errorf("DNS-01 certificates need an ACME email on the server; set one first")
	}
	providers := map[string]bool{}
	envOwner := map[string]string{}
	for _, r := range cfg.DNSResolvers {
		if providers[r.Provider] {
			return fmt.Errorf("more than one resolver for %s", r.Provider)
		}
		providers[r.Provider] = true
		if err := ValidateDNSResolver(r); err != nil {
			return err
		}
		// The proxy container has one environment: two resolvers cannot both set a variable.
		for _, n := range r.EnvNames {
			if other, taken := envOwner[n]; taken {
				return fmt.Errorf("resolvers %s and %s both set %s; the proxy container has one environment, so use a different provider for one of them", other, r.Provider, n)
			}
			envOwner[n] = r.Provider
		}
	}
	defaults := 0
	domains := map[string]bool{}
	for _, w := range cfg.WildcardCerts {
		if err := ValidateWildcardCert(w); err != nil {
			return err
		}
		if !providers[w.Provider] {
			return fmt.Errorf("wildcard %s uses %s, which has no resolver on this server", w.Domain, w.Provider)
		}
		if domains[w.Domain] {
			return fmt.Errorf("wildcard %s is configured twice", w.Domain)
		}
		domains[w.Domain] = true
		if w.Default {
			defaults++
		}
	}
	if defaults > 1 {
		return fmt.Errorf("only one wildcard certificate can be the default certificate")
	}
	return nil
}

// HasDNSResolver reports whether cfg configures a resolver for provider.
func (c ProxyConfig) HasDNSResolver(provider string) bool {
	return slices.ContainsFunc(c.DNSResolvers, func(r DNSResolver) bool { return r.Provider == provider })
}
