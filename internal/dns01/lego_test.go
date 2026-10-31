package dns01

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const legoCanary = "pdns-canary-key-24680"

func serverWithFacts(caps ...string) db.Server {
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: caps})
	return db.Server{ID: uuid.New(), Name: "edge", Facts: facts}
}

func TestRequireDNS01ProviderNeedsLegoCapabilityForNewProviders(t *testing.T) {
	old := serverWithFacts(agentprotocol.CapDNS01)
	current := serverWithFacts(agentprotocol.CapDNS01, agentprotocol.CapDNS01Lego)
	never := db.Server{Name: "new"} // never connected: assumed current

	// The original four render on an agent that predates the catalog.
	for _, p := range []string{"cloudflare", "route53", "hetzner", "digitalocean"} {
		require.NoError(t, RequireDNS01Provider(old, p), p)
	}
	for _, p := range []string{"ovh", "gandiv5", "pdns"} {
		err := RequireDNS01Provider(old, p)
		var he *apierr.Error
		require.ErrorAs(t, err, &he, p)
		require.Contains(t, err.Error(), "too old", p)
		require.NoError(t, RequireDNS01Provider(current, p), p)
		require.NoError(t, RequireDNS01Provider(never, p), p)
	}
	// No DNS-01 at all is refused whatever the provider.
	require.Error(t, RequireDNS01Provider(serverWithFacts(), "cloudflare"))
}

func TestNormalizeAndCheckValues(t *testing.T) {
	pdns, _ := agentprotocol.DNSProvider("pdns")
	require.NoError(t, checkValues(pdns, map[string]string{"PDNS_API_KEY": "k"}))
	require.Error(t, checkValues(pdns, nil), "a generic credential needs a variable")
	require.Error(t, checkValues(pdns, map[string]string{"TRAEFIK_PROVIDERS_DOCKER": "true"}))
	require.Error(t, checkValues(pdns, map[string]string{"PDNS_API_KEY": "a\nb"}))

	ovh, _ := agentprotocol.DNSProvider("ovh")
	require.Error(t, checkValues(ovh, map[string]string{"OVH_ENDPOINT": "ovh-eu"}))
	require.NoError(t, checkValues(ovh, map[string]string{"OVH_ENDPOINT": "ovh-eu", "OVH_CLIENT_ID": "i", "OVH_CLIENT_SECRET": "s"}))

	got := normalizeValues(map[string]string{"A": "  v  ", "B": "   ", "GCE_SERVICE_ACCOUNT": "{\n \"a\": 1\n}"})
	require.Equal(t, map[string]string{"A": "v", "GCE_SERVICE_ACCOUNT": `{"a":1}`}, got, "blank variables are dropped")
}

func TestAnyLegoProviderEndToEnd(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := t.Context()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	email := "ops@example.com"
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "Edge", AcmeEmail: &email})
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	svc := NewService(pool, nil, box)
	admin := principal(ws.ID, policy.AllScopes)
	dev := principal(ws.ID, []string{policy.ScopeProxyWrite, policy.ScopeServersRead})

	// The catalogs are readable by anyone who can read servers.
	curated, err := svc.Providers(dev)
	require.NoError(t, err)
	require.Len(t, curated, 19)
	rest, err := svc.LegoProviders(dev)
	require.NoError(t, err)
	require.Len(t, rest, len(agentprotocol.LegoProviderCodes())-19)
	_, err = svc.LegoProviders(principal(ws.ID, nil))
	require.Error(t, err)

	// Unknown, excluded and malformed codes are refused.
	for _, p := range []string{"nope", "exec", "manual", "", "../etc", "PDNS"} {
		_, err = svc.CreateCredential(admin, CredentialInput{Name: "x", Provider: p, Values: map[string]string{"PDNS_API_KEY": "k"}})
		require.Error(t, err, p)
	}
	// Only the provider's own variables.
	_, err = svc.CreateCredential(admin, CredentialInput{Name: "x", Provider: "pdns", Values: map[string]string{"TRAEFIK_API_INSECURE": "true"}})
	require.ErrorContains(t, err, "is not a PowerDNS credential variable")

	cred, err := svc.CreateCredential(admin, CredentialInput{Name: "pdns-main", Provider: "pdns", Values: map[string]string{
		"PDNS_API_KEY": legoCanary, "PDNS_API_URL": "https://pdns.example.com:8081",
	}})
	require.NoError(t, err)
	require.Equal(t, "pdns", cred.Provider)
	require.Equal(t, "PowerDNS", cred.ProviderName)

	// Only the variables in use are listed; the secret never leaves, the URL is shown to an administrator.
	raw, _ := json.Marshal(cred)
	require.NotContains(t, string(raw), legoCanary)
	require.Len(t, cred.Fields, 2)
	byEnv := map[string]DNSCredentialField{}
	for _, f := range cred.Fields {
		byEnv[f.Env] = f
	}
	require.True(t, byEnv["PDNS_API_KEY"].Secret)
	require.Nil(t, byEnv["PDNS_API_KEY"].Value)
	require.False(t, byEnv["PDNS_API_URL"].Secret)
	require.Equal(t, "https://pdns.example.com:8081", *byEnv["PDNS_API_URL"].Value)
	list, err := svc.ListCredentials(dev, nil)
	require.NoError(t, err)
	raw, _ = json.Marshal(list)
	require.NotContains(t, string(raw), legoCanary)
	require.NotContains(t, string(raw), "pdns.example.com", "non-secret values go to administrators only")

	// A credential of another provider cannot back this resolver.
	cf, err := svc.CreateCredential(admin, CredentialInput{Name: "cf", Provider: "cloudflare", Values: map[string]string{"CF_DNS_API_TOKEN": "t"}})
	require.NoError(t, err)
	_, err = svc.SetResolver(dev, srv.ID, "pdns", ResolverInput{CredentialID: cf.ID})
	require.ErrorContains(t, err, "the credential is for cloudflare")

	// Resolver and wildcard of a generic provider stage like any other.
	st, err := svc.SetResolver(dev, srv.ID, "pdns", ResolverInput{CredentialID: cred.ID})
	require.NoError(t, err)
	require.True(t, st.Staged)
	require.Equal(t, "pdns", st.Resolvers[0].Provider)
	require.Equal(t, "PowerDNS", st.Resolvers[0].ProviderName)
	_, err = svc.CreateWildcard(dev, srv.ID, WildcardInput{Domain: "example.com", Provider: "pdns", IncludeApex: true})
	require.NoError(t, err)
	_, err = svc.SetResolver(dev, srv.ID, "nope", ResolverInput{CredentialID: cred.ID})
	require.Error(t, err)

	desired, err := Desired(bg, q, srv.ID)
	require.NoError(t, err)
	var pc agentprotocol.ProxyConfig
	desired.Apply(&pc)
	payload, err := json.Marshal(agentprotocol.ProxyConfigurePayload{Proxy: pc})
	require.NoError(t, err)
	require.NotContains(t, string(payload), legoCanary, "credentials never enter a persisted payload")
	require.Contains(t, string(payload), "PDNS_API_KEY", "the names do")
	require.NoError(t, agentprotocol.ValidateDNS01(pc, email))
	secrets, err := Secrets(bg, q, box, srv.ID, &pc)
	require.NoError(t, err)
	require.Equal(t, legoCanary, secrets[agentprotocol.DNSSecretKey(cred.ID.String(), "PDNS_API_KEY")])

	// Rotating a variable that must not be removed is refused; clearing an optional one works.
	_, err = svc.UpdateCredential(admin, cred.ID, CredentialPatch{Values: map[string]string{"PDNS_TTL": "60"}})
	require.NoError(t, err)
	_, err = svc.UpdateCredential(admin, cred.ID, CredentialPatch{Clear: []string{"PDNS_TTL"}})
	require.NoError(t, err)
	_, err = svc.UpdateCredential(admin, cred.ID, CredentialPatch{Values: map[string]string{"HTTPS_PROXY": "http://evil"}})
	require.Error(t, err)
}

func TestGoogleServiceAccountKeyIsStoredOnOneLine(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := t.Context()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{4}, 32))
	require.NoError(t, err)
	svc := NewService(pool, nil, box)
	admin := principal(ws.ID, policy.AllScopes)

	key := "{\n  \"type\": \"service_account\",\n  \"project_id\": \"p\",\n  \"private_key\": \"-----BEGIN PRIVATE KEY-----\\nabc\\n-----END PRIVATE KEY-----\\n\"\n}\n"
	cred, err := svc.CreateCredential(admin, CredentialInput{Name: "gcp", Provider: "gcloud", Values: map[string]string{"GCE_SERVICE_ACCOUNT": key}})
	require.NoError(t, err)
	row, err := q.GetDNSCredential(bg, db.GetDNSCredentialParams{ID: cred.ID, WorkspaceID: ws.ID})
	require.NoError(t, err)
	values, err := openValues(box, row.ID, row.SecretsEnc)
	require.NoError(t, err)
	require.NotContains(t, values["GCE_SERVICE_ACCOUNT"], "\n")
	require.NoError(t, agentprotocol.ValidateDNSValue(values["GCE_SERVICE_ACCOUNT"]))
	var parsed map[string]string
	require.NoError(t, json.Unmarshal([]byte(values["GCE_SERVICE_ACCOUNT"]), &parsed), "still the same JSON")
	require.Equal(t, "service_account", parsed["type"])
}

// The proxy container has one environment: Route 53 and Lightsail both read
// AWS_ACCESS_KEY_ID, so only one of them can be a resolver of a server.
func TestResolversCannotShareAVariable(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := t.Context()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	email := "ops@example.com"
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "Edge", AcmeEmail: &email})
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	svc := NewService(pool, nil, box)
	admin := principal(ws.ID, policy.AllScopes)

	r53, err := svc.CreateCredential(admin, CredentialInput{Name: "r53", Provider: "route53", Values: map[string]string{
		"AWS_ACCESS_KEY_ID": "AKIA", "AWS_SECRET_ACCESS_KEY": "s", "AWS_REGION": "us-east-1",
	}})
	require.NoError(t, err)
	ls, err := svc.CreateCredential(admin, CredentialInput{Name: "ls", Provider: "lightsail", Values: map[string]string{
		"AWS_ACCESS_KEY_ID": "AKIB", "AWS_SECRET_ACCESS_KEY": "t",
	}})
	require.NoError(t, err)

	_, err = svc.SetResolver(admin, srv.ID, "route53", ResolverInput{CredentialID: r53.ID})
	require.NoError(t, err)
	_, err = svc.SetResolver(admin, srv.ID, "lightsail", ResolverInput{CredentialID: ls.ID})
	require.ErrorContains(t, err, "the route53 resolver already sets AWS_ACCESS_KEY_ID")

	// Replacing the route53 resolver itself is not a clash with itself.
	_, err = svc.SetResolver(admin, srv.ID, "route53", ResolverInput{CredentialID: r53.ID, DelaySeconds: 5})
	require.NoError(t, err)

	// The agent-side validation says the same for a hand-built configuration.
	cfg := agentprotocol.ProxyConfig{DNSResolvers: []agentprotocol.DNSResolver{
		{ID: "a", Provider: "route53", EnvNames: []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION"}},
		{ID: "b", Provider: "lightsail", EnvNames: []string{"AWS_ACCESS_KEY_ID"}},
	}}
	require.ErrorContains(t, agentprotocol.ValidateDNS01(cfg, "ops@example.com"), "both set AWS_ACCESS_KEY_ID")
}

func TestAliasIsRefusedWithTheCanonicalCode(t *testing.T) {
	err := providerError("rfc2136")
	require.ErrorContains(t, err, `use the lego provider code "dnsupdate"`)
	require.ErrorContains(t, providerError("nope"), "lego DNS provider code")
}
