package agentprotocol

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderEnvNamesMatchLego(t *testing.T) {
	// The exact variable names go-acme/lego v5 (Traefik v3.7) reads.
	want := map[string][]string{
		DNSCloudflare:   {"CF_DNS_API_TOKEN", "CF_ZONE_API_TOKEN"},
		DNSRoute53:      {"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION", "AWS_HOSTED_ZONE_ID"},
		DNSHetzner:      {"HETZNER_API_TOKEN"},
		DNSDigitalOcean: {"DO_AUTH_TOKEN"},
	}
	for code, envs := range want {
		spec, ok := DNSProvider(code)
		require.True(t, ok, code)
		var got []string
		for _, f := range spec.Fields {
			got = append(got, f.Env)
		}
		require.Equal(t, envs, got, code)
	}
}

func TestValidateDNSResolver(t *testing.T) {
	ok := DNSResolver{ID: "c1", Provider: DNSCloudflare, EnvNames: []string{"CF_DNS_API_TOKEN"}, Env: map[string]string{"CF_DNS_API_TOKEN": "tok"}}
	require.NoError(t, ValidateDNSResolver(ok))

	bad := ok
	bad.EnvNames = []string{"PATH"}
	require.ErrorContains(t, ValidateDNSResolver(bad), "not a Cloudflare credential variable")

	bad = ok
	bad.EnvNames = nil
	bad.Env = nil
	require.ErrorContains(t, ValidateDNSResolver(bad), "CF_DNS_API_TOKEN) is required")

	bad = ok
	bad.Env = map[string]string{"CF_DNS_API_TOKEN": "a\nb"}
	require.Error(t, ValidateDNSResolver(bad))

	bad = ok
	bad.CAServer = "http://insecure.example"
	require.Error(t, ValidateDNSResolver(bad))
	bad.CAServer = LetsEncryptStagingCA
	require.NoError(t, ValidateDNSResolver(bad))

	bad = ok
	bad.Provider = "nope"
	require.Error(t, ValidateDNSResolver(bad))
}

func TestValidateDNS01(t *testing.T) {
	r := DNSResolver{ID: "c1", Provider: DNSHetzner, EnvNames: []string{"HETZNER_API_TOKEN"}}
	w := WildcardCert{ID: "w1", Provider: DNSHetzner, Domain: "example.com", IncludeApex: true}
	require.NoError(t, ValidateDNS01(ProxyConfig{}, ""), "nothing configured needs no email")
	require.ErrorContains(t, ValidateDNS01(ProxyConfig{DNSResolvers: []DNSResolver{r}}, ""), "ACME email")
	require.NoError(t, ValidateDNS01(ProxyConfig{DNSResolvers: []DNSResolver{r}, WildcardCerts: []WildcardCert{w}}, "a@b.c"))
	require.ErrorContains(t, ValidateDNS01(ProxyConfig{WildcardCerts: []WildcardCert{w}}, "a@b.c"), "no resolver")
	require.ErrorContains(t, ValidateDNS01(ProxyConfig{DNSResolvers: []DNSResolver{r, r}}, "a@b.c"), "more than one resolver")
	w2 := w
	w2.ID, w2.Domain, w.Default, w2.Default = "w2", "example.org", true, true
	require.ErrorContains(t, ValidateDNS01(ProxyConfig{DNSResolvers: []DNSResolver{r}, WildcardCerts: []WildcardCert{w, w2}}, "a@b.c"), "default")
}

func TestWildcardHosts(t *testing.T) {
	require.True(t, IsWildcardHost("*.example.com"))
	require.False(t, IsWildcardHost("*.com"))
	require.False(t, IsWildcardHost("*.*.example.com"))
	require.False(t, IsWildcardHost("a.*.example.com"))
	require.Equal(t, "nc-probe.example.com", ProbeHost("*.example.com"))
	require.Equal(t, "app.example.com", ProbeHost("app.example.com"))

	w := WildcardCert{Domain: "example.com", IncludeApex: true}
	require.True(t, WildcardCovers(w, "app.example.com"))
	require.True(t, WildcardCovers(w, "example.com"))
	require.True(t, WildcardCovers(w, "*.example.com"))
	require.False(t, WildcardCovers(w, "a.b.example.com"), "one label only")
	w.IncludeApex = false
	require.False(t, WildcardCovers(w, "example.com"))
	require.Equal(t, []string{"*.example.com"}, w.Names())
}
