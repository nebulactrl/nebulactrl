package proxy

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/pages"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func dnsResolver(provider string, env map[string]string, mut func(*agentprotocol.DNSResolver)) agentprotocol.DNSResolver {
	spec, _ := agentprotocol.DNSProvider(provider)
	r := agentprotocol.DNSResolver{ID: "11111111-aaaa-4aaa-8aaa-111111111111", Provider: provider, Env: env}
	for _, f := range spec.Fields {
		if _, ok := env[f.Env]; ok {
			r.EnvNames = append(r.EnvNames, f.Env)
		}
	}
	if mut != nil {
		mut(&r)
	}
	return r
}

// goldenConfigNoFile builds the dynamic config without comparing it to a file.
func goldenConfigNoFile(t *testing.T, routes []agentprotocol.Route, opts Options) []byte {
	t.Helper()
	opts.Generation = 1
	got, err := BuildConfig(routes, opts)
	require.NoError(t, err)
	return got
}

// dnsCases is one resolver per curated provider, plus generic lego providers,
// each with the variables its form sends. The goldens pin the static
// configuration the proxy container runs with: the resolver flags
// (static_dns01_<code>.golden.txt) and its environment
// (static_dns01_<code>.env.golden.txt). Variable names are lego v5.5.2's
// (agentprotocol.LegoVersion, Traefik v3.7.16).
func dnsCases() map[string]agentprotocol.DNSResolver {
	return map[string]agentprotocol.DNSResolver{
		"cloudflare":   dnsResolver("cloudflare", map[string]string{"CF_DNS_API_TOKEN": "cf-secret-token"}, nil),
		"route53":      dnsResolver("route53", map[string]string{"AWS_ACCESS_KEY_ID": "AKIA", "AWS_SECRET_ACCESS_KEY": "aws-secret", "AWS_REGION": "us-east-1"}, func(r *agentprotocol.DNSResolver) { r.DelaySeconds = 30 }),
		"hetzner":      dnsResolver("hetzner", map[string]string{"HETZNER_API_TOKEN": "hz-secret-token"}, nil),
		"digitalocean": dnsResolver("digitalocean", map[string]string{"DO_AUTH_TOKEN": "do-secret-token"}, func(r *agentprotocol.DNSResolver) { r.CAServer = agentprotocol.LetsEncryptStagingCA }),
		"gandiv5":      dnsResolver("gandiv5", map[string]string{"GANDIV5_PERSONAL_ACCESS_TOKEN": "gandi-secret-pat"}, nil),
		"ovh": dnsResolver("ovh", map[string]string{
			"OVH_ENDPOINT": "ovh-eu", "OVH_APPLICATION_KEY": "ovh-app-key", "OVH_APPLICATION_SECRET": "ovh-app-secret", "OVH_CONSUMER_KEY": "ovh-consumer-key",
		}, nil),
		"porkbun":   dnsResolver("porkbun", map[string]string{"PORKBUN_API_KEY": "pk1_public", "PORKBUN_SECRET_API_KEY": "sk1_secret"}, nil),
		"namecheap": dnsResolver("namecheap", map[string]string{"NAMECHEAP_API_USER": "nc-user", "NAMECHEAP_API_KEY": "nc-secret-key"}, func(r *agentprotocol.DNSResolver) { r.DelaySeconds = 120 }),
		"gcloud":    dnsResolver("gcloud", map[string]string{"GCE_SERVICE_ACCOUNT": `{"type":"service_account","project_id":"p","private_key":"-----BEGIN PRIVATE KEY-----\nk\n-----END PRIVATE KEY-----\n"}`, "GCE_PROJECT": "acme-dns"}, nil),
		"azuredns": dnsResolver("azuredns", map[string]string{
			"AZURE_CLIENT_ID": "az-client", "AZURE_CLIENT_SECRET": "az-secret", "AZURE_TENANT_ID": "az-tenant", "AZURE_SUBSCRIPTION_ID": "az-sub", "AZURE_RESOURCE_GROUP": "dns-rg",
		}, nil),
		"desec":    dnsResolver("desec", map[string]string{"DESEC_TOKEN": "desec-secret-token"}, nil),
		"linode":   dnsResolver("linode", map[string]string{"LINODE_TOKEN": "linode-secret-token"}, nil),
		"vultr":    dnsResolver("vultr", map[string]string{"VULTR_API_KEY": "vultr-secret-key"}, nil),
		"scaleway": dnsResolver("scaleway", map[string]string{"SCW_SECRET_KEY": "scw-secret-key", "SCW_ACCESS_KEY": "SCWXXXXXXXXXXXXXXXXX", "SCW_PROJECT_ID": "scw-project"}, nil),
		"bunny":    dnsResolver("bunny", map[string]string{"BUNNY_API_KEY": "bunny-secret-key"}, nil),
		"dnsimple": dnsResolver("dnsimple", map[string]string{"DNSIMPLE_OAUTH_TOKEN": "dnsimple-secret-token"}, nil),
		"ionos":    dnsResolver("ionos", map[string]string{"IONOS_API_KEY": "prefix.ionos-secret"}, nil),
		"godaddy":  dnsResolver("godaddy", map[string]string{"GODADDY_API_KEY": "gd-key", "GODADDY_API_SECRET": "gd-secret"}, nil),
		"netcup":   dnsResolver("netcup", map[string]string{"NETCUP_CUSTOMER_NUMBER": "12345", "NETCUP_API_KEY": "nc-api-key", "NETCUP_API_PASSWORD": "nc-api-password"}, nil),
		// Generic: any other lego provider, variables from lego's catalog.
		"pdns":    dnsResolver("pdns", map[string]string{"PDNS_API_KEY": "pdns-secret-key", "PDNS_API_URL": "https://pdns.example.com:8081", "PDNS_TTL": "60"}, nil),
		"acmedns": dnsResolver("acmedns", map[string]string{"ACME_DNS_API_BASE": "https://auth.acme-dns.io", "ACME_DNS_STORAGE_BASE_URL": "https://storage.acme-dns.example.com"}, nil),
	}
}

func TestStaticFlagsDNS01Golden(t *testing.T) {
	for provider, r := range dnsCases() {
		t.Run(provider, func(t *testing.T) {
			require.NoError(t, agentprotocol.ValidateDNSResolver(r), "the case is a configuration the control plane accepts")
			got := StaticFlags(StaticOptions{ACMEEmail: "ops@example.com", PublicHTTPSPort: 8443, TrafficAnalytics: true, DNSResolvers: []agentprotocol.DNSResolver{r}})
			flags := strings.Join(got, "\n")
			golden(t, "static_dns01_"+provider+".golden.txt", []byte(flags))
			// The resolver is named after the lego code and uses its own storage file.
			require.Contains(t, flags, "--certificatesresolvers.nc-dns-"+provider+".acme.dnschallenge.provider="+provider)
			require.Contains(t, flags, "--certificatesresolvers.nc-dns-"+provider+".acme.storage=/data/acme-"+provider+".json")
			// Credentials are the container environment, never a flag.
			for _, v := range r.Env {
				require.NotContains(t, flags, v)
			}
			// The environment: sorted KEY=VALUE of the catalog's variables only.
			env := dnsEnv([]agentprotocol.DNSResolver{r})
			golden(t, "static_dns01_"+provider+".env.golden.txt", []byte(strings.Join(env, "\n")))
			require.True(t, sort.StringsAreSorted(env))
			for _, kv := range env {
				name, _, _ := strings.Cut(kv, "=")
				require.Contains(t, agentprotocol.LegoEnvNames(provider), name, "lego %s reads %s", agentprotocol.LegoVersion, name)
			}
		})
	}
}

// The catalog the variable names come from is the one of the lego release the
// pinned Traefik vendors: bumping TraefikImage without regenerating
// pkg/agentprotocol/dns_lego_catalog.go (go run ./tools/dnscatalog -traefik <tag>)
// would let the forms drift from what the proxy reads.
func TestCatalogMatchesPinnedTraefik(t *testing.T) {
	tag, _, _ := strings.Cut(strings.TrimPrefix(TraefikImage, "traefik:"), "@")
	require.Equal(t, tag, agentprotocol.LegoCatalogTraefik,
		"regenerate the lego catalog: go run ./tools/dnscatalog -traefik %s -o pkg/agentprotocol/dns_lego_catalog.go", tag)
}

func TestDNSEnvOnlyAllowedNamesSortedAndFingerprinted(t *testing.T) {
	r := dnsResolver("route53", map[string]string{"AWS_SECRET_ACCESS_KEY": "s3cr3t-value", "AWS_ACCESS_KEY_ID": "AKIA", "AWS_REGION": "eu-west-1"}, nil)
	r.Env["LD_PRELOAD"] = "/evil.so" // not a route53 variable
	require.Equal(t, []string{"AWS_ACCESS_KEY_ID=AKIA", "AWS_REGION=eu-west-1", "AWS_SECRET_ACCESS_KEY=s3cr3t-value"}, dnsEnv([]agentprotocol.DNSResolver{r}))

	a := dnsFingerprint(dnsEnv([]agentprotocol.DNSResolver{r}))
	r.Env["AWS_SECRET_ACCESS_KEY"] = "rotated-value"
	b := dnsFingerprint(dnsEnv([]agentprotocol.DNSResolver{r}))
	require.NotEqual(t, a, b, "rotating a credential changes the spec fingerprint")
	require.NotContains(t, a, "s3cr3t")
}

func TestResolveDNSSecrets(t *testing.T) {
	r := agentprotocol.DNSResolver{ID: "cred-1", Provider: agentprotocol.DNSCloudflare, EnvNames: []string{"CF_DNS_API_TOKEN"}}
	cfg := agentprotocol.ProxyConfig{DNSResolvers: []agentprotocol.DNSResolver{r}}

	_, _, err := ResolveSecrets(nil, &cfg, map[string]string{})
	require.ErrorContains(t, err, "CF_DNS_API_TOKEN is missing")
	require.NotContains(t, err.Error(), "secret")

	_, got, err := ResolveSecrets(nil, &cfg, map[string]string{agentprotocol.DNSSecretKey("cred-1", "CF_DNS_API_TOKEN"): "tok-123"})
	require.NoError(t, err)
	require.Equal(t, "tok-123", got.DNSResolvers[0].Env["CF_DNS_API_TOKEN"])
	require.Empty(t, cfg.DNSResolvers[0].Env, "the input is not modified")
}

func TestSpecEnvAndRecreateOnRotation(t *testing.T) {
	m := &Manager{cfg: Config{HTTPPort: 80, HTTPSPort: 443, AdminPort: 9000, AdminURL: "http://127.0.0.1:9000"}, adminSecret: "s"}
	in := specInputs{ACMEEmail: "ops@example.com", Traffic: true, DNS: []agentprotocol.DNSResolver{dnsResolver("hetzner", map[string]string{"HETZNER_API_TOKEN": "one-token"}, nil)}}
	a := m.specFor(in)
	require.Equal(t, []string{"HETZNER_API_TOKEN=one-token"}, a.Env)
	for k, v := range a.Labels {
		require.NotContains(t, k+v, "one-token", "credentials never appear in labels")
	}
	in.DNS = []agentprotocol.DNSResolver{dnsResolver("hetzner", map[string]string{"HETZNER_API_TOKEN": "two-token"}, nil)}
	b := m.specFor(in)
	require.NotEqual(t, a.Labels[LabelSpec], b.Labels[LabelSpec], "a rotated token recreates the container")
	require.Empty(t, m.specFor(specInputs{ACMEEmail: "ops@example.com"}).Env)
}

// The containers that stay on the edge network keep it as their default
// gateway (Engine 28), so joining environment networks never re-binds their
// published ports.
func TestEdgeContainersKeepTheEdgeGateway(t *testing.T) {
	m := &Manager{cfg: Config{HTTPPort: 80, HTTPSPort: 443, AdminPort: 9000, AdminURL: "http://127.0.0.1:9000"}, adminSecret: "s"}
	for name, spec := range map[string]docker.ContainerSpec{
		"traefik": m.specFor(specInputs{}),
		"pages":   m.pagesSpec(&pages.Bundle{}),
	} {
		require.Len(t, spec.Networks, 1, name)
		require.Equal(t, agentprotocol.EdgeNetwork, spec.Networks[0].Network, name)
		require.Equal(t, agentprotocol.EdgeGwPriority, spec.Networks[0].GwPriority, name)
	}
}

func TestDNS01DynamicGolden(t *testing.T) {
	route := ruleRoute(rule(domA, "*.preview.example.org", func(d *agentprotocol.DomainRule) { d.DNSResolver = agentprotocol.DNSCloudflare }))
	opts := Options{
		ACMEEmail:    "ops@example.com",
		DNSResolvers: []agentprotocol.DNSResolver{dnsResolver("cloudflare", map[string]string{"CF_DNS_API_TOKEN": "tok"}, nil)},
		Wildcards: []agentprotocol.WildcardCert{{
			ID: "dddddddd-4444-4444-8444-444444444444", Provider: agentprotocol.DNSCloudflare, Domain: "example.com", IncludeApex: true, Default: true,
		}},
	}
	raw := goldenConfig(t, "dns01_wildcard", []agentprotocol.Route{route}, opts)
	require.NotContains(t, string(raw), "tok\"", "no credential in the dynamic config")
}

func TestWildcardCoverageAndGeneratedSwitch(t *testing.T) {
	res := []agentprotocol.DNSResolver{dnsResolver("cloudflare", map[string]string{"CF_DNS_API_TOKEN": "tok"}, nil)}
	wc := agentprotocol.WildcardCert{ID: "dddddddd-4444-4444-8444-444444444444", Provider: agentprotocol.DNSCloudflare, Domain: "example.com"}
	route := ruleRoute(
		rule(domA, "app.example.com", nil),
		rule(domB, "svc-prod-proj.example.com", func(d *agentprotocol.DomainRule) { d.Generated = true }),
	)
	tlsOf := func(p parsed, id string) any { return p.HTTP.Routers["nc-"+svcApp+"-d"+id[:8]+"-https"]["tls"] }

	p := parse(t, goldenConfigNoFile(t, []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com", DNSResolvers: res, Wildcards: []agentprotocol.WildcardCert{wc}}))
	require.Equal(t, map[string]any{}, tlsOf(p, domA), "a host the wildcard covers uses the store")
	require.Equal(t, map[string]any{"certResolver": "le"}, tlsOf(p, domB), "generated domains keep HTTP-01 unless the wildcard opts in")

	wc.CoverGenerated = true
	p = parse(t, goldenConfigNoFile(t, []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com", DNSResolvers: res, Wildcards: []agentprotocol.WildcardCert{wc}}))
	require.Equal(t, map[string]any{}, tlsOf(p, domB))

	// Without the resolver the wildcard is ignored: no anchor, back to HTTP-01.
	p = parse(t, goldenConfigNoFile(t, []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com", Wildcards: []agentprotocol.WildcardCert{wc}}))
	require.Equal(t, map[string]any{"certResolver": "le"}, tlsOf(p, domA))
	require.NotContains(t, p.HTTP.Routers, "nc-cert-dddddddd")
}

func TestWildcardHostRuleAndTLS(t *testing.T) {
	require.Equal(t, "HostRegexp(`^[^.]+\\.example\\.com$`)", hostRule([]string{"*.example.com"}))
	require.Equal(t, "Host(`a.example.com`) || HostRegexp(`^[^.]+\\.b\\.org$`)", hostRule([]string{"a.example.com", "*.b.org"}))

	// A wildcard host nothing covers never asks HTTP-01 (it cannot validate it).
	route := ruleRoute(rule(domA, "*.nowhere.example.com", nil))
	p := parse(t, goldenConfigNoFile(t, []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com"}))
	require.Equal(t, map[string]any{}, p.HTTP.Routers["nc-"+svcApp+"-daaaaaaaa-https"]["tls"])
}

func TestValidateDNS01NeedsResolvedValues(t *testing.T) {
	r := agentprotocol.DNSResolver{ID: "c", Provider: agentprotocol.DNSHetzner, EnvNames: []string{"HETZNER_API_TOKEN"}}
	require.ErrorContains(t, validateDNS01(agentprotocol.ProxyConfig{DNSResolvers: []agentprotocol.DNSResolver{r}}, "a@b.c"), "was not delivered with the command")
	r.Env = map[string]string{"HETZNER_API_TOKEN": "x"}
	require.NoError(t, validateDNS01(agentprotocol.ProxyConfig{DNSResolvers: []agentprotocol.DNSResolver{r}}, "a@b.c"))
}

func TestParseACMELogError(t *testing.T) {
	line := `{"level":"error","error":"cloudflare: bad token","providerName":"nc-dns-cloudflare.acme","message":"Unable to obtain ACME certificate for domains"}`
	name, msg, ok := parseACMELogError(line)
	require.True(t, ok)
	require.Equal(t, "nc-dns-cloudflare", name)
	require.Contains(t, msg, "bad token")
	_, _, ok = parseACMELogError(`{"level":"info","providerName":"nc-dns-cloudflare.acme"}`)
	require.False(t, ok)
	_, _, ok = parseACMELogError("not json")
	require.False(t, ok)
}

func TestACMEConfigPersistsOnlyNames(t *testing.T) {
	// The control plane's payload (what agent_commands stores) carries names only.
	r := agentprotocol.DNSResolver{ID: "c", Provider: agentprotocol.DNSCloudflare, EnvNames: []string{"CF_DNS_API_TOKEN"}}
	raw, err := json.Marshal(agentprotocol.ProxyConfig{DNSResolvers: []agentprotocol.DNSResolver{r}})
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"env"`)
}
