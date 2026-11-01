package agentprotocol

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The curated forms and the lego catalog must agree: a variable the form sends
// that lego does not read would silently do nothing. The names in
// wantCuratedEnv are copied from lego v5.5.2 (Traefik v3.7.16 go.mod):
// providers/dns/<code>/<code>.toml [Configuration], which the docs at
// https://go-acme.github.io/lego/dns/<code>/ render.
var wantCuratedEnv = map[string][]string{
	DNSCloudflare:   {"CF_DNS_API_TOKEN", "CF_ZONE_API_TOKEN"},
	DNSRoute53:      {"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION", "AWS_HOSTED_ZONE_ID"},
	DNSHetzner:      {"HETZNER_API_TOKEN"},
	DNSDigitalOcean: {"DO_AUTH_TOKEN"},
	DNSGandi:        {"GANDIV5_PERSONAL_ACCESS_TOKEN"},
	DNSOVH:          {"OVH_ENDPOINT", "OVH_APPLICATION_KEY", "OVH_APPLICATION_SECRET", "OVH_CONSUMER_KEY", "OVH_CLIENT_ID", "OVH_CLIENT_SECRET", "OVH_ACCESS_TOKEN"},
	DNSPorkbun:      {"PORKBUN_API_KEY", "PORKBUN_SECRET_API_KEY"},
	DNSNamecheap:    {"NAMECHEAP_API_USER", "NAMECHEAP_API_KEY"},
	DNSGoogle:       {"GCE_SERVICE_ACCOUNT", "GCE_PROJECT", "GCE_ZONE_ID"},
	DNSAzure:        {"AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET", "AZURE_TENANT_ID", "AZURE_SUBSCRIPTION_ID", "AZURE_RESOURCE_GROUP"},
	DNSDeSEC:        {"DESEC_TOKEN"},
	DNSLinode:       {"LINODE_TOKEN"},
	DNSVultr:        {"VULTR_API_KEY"},
	DNSScaleway:     {"SCW_SECRET_KEY", "SCW_ACCESS_KEY", "SCW_PROJECT_ID"},
	DNSBunny:        {"BUNNY_API_KEY"},
	DNSDNSimple:     {"DNSIMPLE_OAUTH_TOKEN"},
	DNSIONOS:        {"IONOS_API_KEY"},
	DNSGoDaddy:      {"GODADDY_API_KEY", "GODADDY_API_SECRET"},
	DNSNetcup:       {"NETCUP_CUSTOMER_NUMBER", "NETCUP_API_KEY", "NETCUP_API_PASSWORD"},
}

func TestCuratedEnvNamesMatchLego(t *testing.T) {
	require.Len(t, DNSProviders, len(wantCuratedEnv), "every curated provider has an expectation")
	for _, spec := range DNSProviders {
		want, ok := wantCuratedEnv[spec.Code]
		require.True(t, ok, spec.Code)
		var got []string
		for _, f := range spec.Fields {
			got = append(got, f.Env)
		}
		require.Equal(t, want, got, spec.Code)
	}
}

func TestCuratedFieldsExistInLegoCatalog(t *testing.T) {
	for _, spec := range DNSProviders {
		p, ok := legoLookup(spec.Code)
		require.True(t, ok, "%s is a lego provider code", spec.Code)
		catalog := LegoEnvNames(spec.Code)
		for _, f := range spec.Fields {
			require.Contains(t, catalog, f.Env, "%s: lego %s does not read %s", spec.Code, LegoVersion, f.Env)
		}
		for _, set := range spec.OneOf {
			for _, n := range set {
				require.Contains(t, catalog, n, spec.Code)
			}
		}
		require.Equal(t, LegoDocsURL(spec.Code), spec.Docs, spec.Code)
		require.NotEmpty(t, p.Name)
		require.False(t, spec.Generic, spec.Code)
		require.NotEmpty(t, spec.Fields, spec.Code)
		// Every curated provider is satisfiable by its required variables.
		var need []string
		for _, f := range spec.Fields {
			if f.Required {
				need = append(need, f.Env)
			}
		}
		for _, set := range spec.OneOf {
			if len(need) == 0 {
				need = set
			}
		}
		require.NoError(t, CheckDNSVariables(spec, need), spec.Code)
	}
}

func TestCuratedCodesAreUniqueAndLowercase(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range DNSProviders {
		require.False(t, seen[spec.Code], spec.Code)
		seen[spec.Code] = true
		require.True(t, ValidDNSProviderCode(spec.Code), spec.Code)
	}
}

func TestLegoCatalogShape(t *testing.T) {
	codes := LegoProviderCodes()
	require.Greater(t, len(codes), 150, "lego v5 ships well over 150 DNS providers")
	require.True(t, slices.IsSorted(codes))
	require.NotContains(t, codes, "exec", "exec runs a program: never a credential")
	require.NotContains(t, codes, "manual")
	for _, p := range legoCatalog {
		require.True(t, ValidDNSProviderCode(p.Code), p.Code)
		require.NotEmpty(t, p.Name, p.Code)
		for _, e := range p.Env {
			require.Regexp(t, `^[A-Z][A-Z0-9_]{1,63}$`, e.Name, p.Code)
			require.False(t, strings.HasSuffix(e.Name, "_FILE") || strings.HasSuffix(e.Name, "_PATH"),
				"%s: %s reads a file; the proxy container has nothing to read", p.Code, e.Name)
			require.False(t, strings.HasPrefix(e.Name, "TRAEFIK_"), "%s: %s would configure Traefik itself", p.Code, e.Name)
		}
	}
	require.Equal(t, "v5.5.2", LegoVersion)
}

func TestGenericSpecForAnyLegoProvider(t *testing.T) {
	spec, ok := DNSProvider("pdns")
	require.True(t, ok)
	require.True(t, spec.Generic)
	require.Equal(t, "https://go-acme.github.io/lego/dns/pdns/", spec.Docs)
	require.Equal(t, LegoEnvNames("pdns"), func() []string {
		var out []string
		for _, f := range spec.Fields {
			out = append(out, f.Env)
		}
		return out
	}())

	_, ok = DNSProvider("not-a-provider")
	require.False(t, ok)
	_, ok = DNSProvider("")
	require.False(t, ok)
	_, ok = DNSProvider("exec")
	require.False(t, ok, "exec is excluded")

	// Curated providers keep their form even though lego lists them too.
	cf, _ := DNSProvider("cloudflare")
	require.False(t, cf.Generic)

	for _, p := range LegoProviders() {
		require.True(t, p.Generic, p.Code)
		require.NotContains(t, DNSProviderCodes(), p.Code, "LegoProviders lists only providers without a curated form")
	}
	require.Len(t, LegoProviders(), len(LegoProviderCodes())-len(DNSProviders))
}

func TestGenericResolverAllowsOnlyCatalogVariables(t *testing.T) {
	names := LegoCredentialNames("pdns")
	require.NotEmpty(t, names)
	ok := DNSResolver{ID: "c1", Provider: "pdns", EnvNames: names[:1], Env: map[string]string{names[0]: "value"}}
	require.NoError(t, ValidateDNSResolver(ok))

	for _, evil := range []string{"TRAEFIK_API_INSECURE", "HTTPS_PROXY", "LD_PRELOAD", "PATH", "LEGO_CA_CERTIFICATES", "PDNS_API_KEY_FILE"} {
		bad := ok
		bad.EnvNames = []string{evil}
		bad.Env = map[string]string{evil: "x"}
		require.ErrorContains(t, ValidateDNSResolver(bad), "is not a", evil)
	}

	bad := ok
	bad.EnvNames, bad.Env = nil, nil
	require.ErrorContains(t, ValidateDNSResolver(bad), "at least one variable")

	bad = ok
	bad.Provider = "exec"
	require.ErrorContains(t, ValidateDNSResolver(bad), "unknown DNS provider")
}

func TestOneOfAuthenticationMethods(t *testing.T) {
	ovh, _ := DNSProvider(DNSOVH)
	require.Error(t, CheckDNSVariables(ovh, []string{"OVH_ENDPOINT"}))
	require.Error(t, CheckDNSVariables(ovh, []string{"OVH_APPLICATION_KEY", "OVH_APPLICATION_SECRET"}), "the consumer key is missing")
	require.NoError(t, CheckDNSVariables(ovh, []string{"OVH_APPLICATION_KEY", "OVH_APPLICATION_SECRET", "OVH_CONSUMER_KEY"}))
	require.NoError(t, CheckDNSVariables(ovh, []string{"OVH_CLIENT_ID", "OVH_CLIENT_SECRET", "OVH_ENDPOINT"}))
	require.NoError(t, CheckDNSVariables(ovh, []string{"OVH_ACCESS_TOKEN"}))
	err := CheckDNSVariables(ovh, nil)
	require.ErrorContains(t, err, "OVH_APPLICATION_KEY + OVH_APPLICATION_SECRET + OVH_CONSUMER_KEY or OVH_CLIENT_ID + OVH_CLIENT_SECRET or OVH_ACCESS_TOKEN")
}

func TestSecretHeuristic(t *testing.T) {
	for name, secret := range map[string]bool{
		"PDNS_API_KEY":            true,
		"AZURE_CLIENT_SECRET":     true,
		"GANDIV5_API_KEY":         true,
		"SOMETHING_PASSWORD":      true,
		"UNKNOWN_THING":           true, // unknown names are hidden, not shown
		"AWS_ACCESS_KEY_ID":       false,
		"AZURE_TENANT_ID":         false,
		"OVH_ENDPOINT":            false,
		"NETCUP_CUSTOMER_NUMBER":  false,
		"GCE_PROJECT":             false,
		"OVH_TTL":                 false,
		"OVH_PROPAGATION_TIMEOUT": false,
		"OVH_POLLING_INTERVAL":    false,
	} {
		require.Equal(t, secret, secretEnv(name), name)
	}
}

func TestNormalizeDNSValue(t *testing.T) {
	pretty := "{\n  \"type\": \"service_account\",\n  \"private_key\": \"-----BEGIN PRIVATE KEY-----\\nabc\\n-----END PRIVATE KEY-----\\n\",\n  \"x\": \"a<b\"\n}\n"
	got := NormalizeDNSValue("GCE_SERVICE_ACCOUNT", pretty)
	require.NotContains(t, got, "\n")
	require.NoError(t, ValidateDNSValue(got))
	require.Contains(t, got, `"type":"service_account"`)
	require.Contains(t, got, `a<b`)
	require.Equal(t, "tok", NormalizeDNSValue("CF_DNS_API_TOKEN", "  tok \n"))
	require.Equal(t, "{ not json", NormalizeDNSValue("GCE_SERVICE_ACCOUNT", "{ not json"))
}

func TestDNSProviderNeedsLego(t *testing.T) {
	for _, c := range []string{DNSCloudflare, DNSRoute53, DNSHetzner, DNSDigitalOcean} {
		require.False(t, DNSProviderNeedsLego(c), c)
	}
	for _, c := range []string{DNSOVH, DNSGandi, "pdns", "acmedns"} {
		require.True(t, DNSProviderNeedsLego(c), c)
	}
}

func TestLegoAliasesResolveToTheCanonicalCode(t *testing.T) {
	require.Equal(t, "dnsupdate", LegoCanonicalCode("rfc2136"))
	require.Equal(t, "linode", LegoCanonicalCode("linodev4"))
	require.Equal(t, "acmedns", LegoCanonicalCode("acme-dns"))
	require.Empty(t, LegoCanonicalCode("dnsupdate"))
	require.Empty(t, LegoCanonicalCode("cloudflare"))
	// A credential names the canonical code: an alias is not a provider here.
	_, ok := DNSProvider("rfc2136")
	require.False(t, ok)
	spec, ok := DNSProvider("dnsupdate")
	require.True(t, ok)
	require.Equal(t, []string{"rfc2136"}, spec.Aliases)
	// Every alias is a catalog alias, never a code of its own.
	for _, p := range legoCatalog {
		for _, a := range p.Aliases {
			_, ok := legoLookup(a)
			require.False(t, ok, "%s is an alias of %s", a, p.Code)
		}
	}
}
