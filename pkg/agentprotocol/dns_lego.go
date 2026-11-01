package agentprotocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// "Any lego provider". Traefik's ACME client is lego;
// every DNS provider it ships can be named in
// --certificatesresolvers.<name>.acme.dnschallenge.provider=<code> and reads
// its credentials from the process environment. The curated list (DNSProviders)
// has forms with help text for the common ones; every other lego provider is
// reachable through the generic form: a provider code plus environment
// variables, validated against the catalog generated from the pinned lego
// release (dns_lego_catalog.go, tools/dnscatalog). Only variables that catalog
// names for the code are ever put in the Traefik container's environment, so a
// credential cannot set TRAEFIK_* options, proxy variables or anything else.

type legoEnv struct {
	Name string
	Desc string
	// Optional marks a tuning variable (TTL, polling, timeouts, zone IDs) as
	// opposed to a credential.
	Optional bool
}

type legoProvider struct {
	Code string
	Name string
	URL  string
	// Aliases are other names lego's NewDNSChallengeProviderByName accepts for
	// the code (kept for older releases). A credential names the canonical code.
	Aliases []string
	Env     []legoEnv
}

// LegoDocsURL is the lego documentation page of a provider code.
func LegoDocsURL(code string) string { return "https://go-acme.github.io/lego/dns/" + code + "/" }

func legoLookup(code string) (legoProvider, bool) {
	// Sorted by code; the catalog is small enough for a scan, and lookups are
	// off the hot path.
	for _, p := range legoCatalog {
		if p.Code == code {
			return p, true
		}
	}
	return legoProvider{}, false
}

// plainSuffixes end the names of variables that are identifiers or settings,
// not secrets: shown to administrators, like an AWS access key ID.
var plainSuffixes = []string{
	"_ID", "_USER", "_USERNAME", "_EMAIL", "_REGION", "_ENDPOINT", "_URL", "_HOST", "_PORT", "_ZONE",
	"_PROJECT", "_TENANT", "_DOMAIN", "_NAME", "_TTL", "_TIMEOUT", "_INTERVAL", "_SANDBOX", "_DEBUG",
	"_CUSTOMER_NUMBER", "_ACCOUNT", "_ORG", "_ORGANIZATION", "_PROFILE", "_SUBSCRIPTION", "_ENVIRONMENT",
}

// secretEnv reports whether a variable of the generic form is write-only. A
// variable is secret unless its name says it is an identifier or a setting:
// a wrongly hidden value costs a retype, a wrongly shown one leaks.
func secretEnv(name string) bool {
	for _, s := range plainSuffixes {
		if strings.HasSuffix(name, s) {
			return false
		}
	}
	return true
}

// legoSpec turns a catalog entry into a provider spec for the generic form.
// Nothing is required: which variables a provider needs depends on the
// authentication method the person picks (the docs link says which), so the
// control plane only demands at least one.
func legoSpec(p legoProvider) DNSProviderSpec {
	spec := DNSProviderSpec{
		Code: p.Code, Name: p.Name, Docs: LegoDocsURL(p.Code), Generic: true, Aliases: p.Aliases,
		Help: "Set the variables of one authentication method; the docs page lists which. Secret values are write-only.",
	}
	for _, e := range p.Env {
		label := e.Desc
		if label == "" {
			label = e.Name
		}
		if e.Optional {
			label += " (optional)"
		}
		spec.Fields = append(spec.Fields, DNSField{Env: e.Name, Label: label, Secret: secretEnv(e.Name)})
	}
	return spec
}

// LegoProviders returns the generic specs of every lego provider that has no
// curated form, sorted by name, for the "any lego provider" form.
func LegoProviders() []DNSProviderSpec {
	curated := map[string]bool{}
	for _, p := range DNSProviders {
		curated[p.Code] = true
	}
	var out []DNSProviderSpec
	for _, p := range legoCatalog {
		if !curated[p.Code] {
			out = append(out, legoSpec(p))
		}
	}
	return out
}

// NormalizeDNSValue tidies one credential value before it is stored: it trims
// surrounding space, and puts a Google service account key (GCE_SERVICE_ACCOUNT)
// on one line, because a pasted key file spans many.
func NormalizeDNSValue(env, v string) string {
	v = strings.TrimSpace(v)
	if env == "GCE_SERVICE_ACCOUNT" && strings.HasPrefix(v, "{") {
		var compact bytes.Buffer
		if json.Compact(&compact, []byte(v)) == nil {
			return compact.String()
		}
	}
	return v
}

// CheckDNSVariables checks the set of variable names a credential carries
// against its provider: every required variable, one complete authentication
// method where the provider has several, and for a generic provider at least
// one variable. The error names variables, never values.
func CheckDNSVariables(spec DNSProviderSpec, names []string) error {
	have := make(map[string]bool, len(names))
	for _, n := range names {
		have[n] = true
	}
	for _, f := range spec.Fields {
		if f.Required && !have[f.Env] {
			return fmt.Errorf("%s (%s) is required", f.Label, f.Env)
		}
	}
	if len(spec.OneOf) > 0 {
		for _, set := range spec.OneOf {
			if slices.ContainsFunc(set, func(n string) bool { return !have[n] }) {
				continue
			}
			return nil
		}
		methods := make([]string, len(spec.OneOf))
		for i, set := range spec.OneOf {
			methods[i] = strings.Join(set, " + ")
		}
		return fmt.Errorf("set every variable of one authentication method: %s", strings.Join(methods, " or "))
	}
	if spec.Generic && len(names) == 0 {
		return fmt.Errorf("set at least one variable; %s lists them", spec.Docs)
	}
	return nil
}

// LegoCanonicalCode maps an alias lego accepts (rfc2136, linodev4, acme-dns, …)
// to the canonical code a credential uses; "" when code is not an alias.
func LegoCanonicalCode(code string) string {
	for _, p := range legoCatalog {
		if slices.Contains(p.Aliases, code) {
			return p.Code
		}
	}
	return ""
}
