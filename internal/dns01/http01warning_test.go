package dns01

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestHTTP01WarningText(t *testing.T) {
	require.Equal(t,
		"This server's proxy listens on :8080, not :80: Let's Encrypt HTTP-01 cannot validate app.example.com. Use a DNS-01 resolver (Servers, Proxy, DNS-01) for this domain.",
		HTTP01Warning(8080, "app.example.com"))
	require.Empty(t, HTTP01Warning(80, "app.example.com"))
	require.Empty(t, HTTP01Warning(0, "app.example.com"), "an unknown port warns about nothing")
}

func TestServerHTTPPortPrefersWhatTheAgentReports(t *testing.T) {
	applied := int32(9080)
	require.Equal(t, 8080, ServerHTTPPort(8080, &applied))
	require.Equal(t, 9080, ServerHTTPPort(0, &applied))
	require.Equal(t, 80, ServerHTTPPort(0, nil))
}

// TestHTTP01WarningMatrix covers the domains of a server on :8080 and on :80.
func TestHTTP01WarningMatrix(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	proj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: proj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	email := "ops@example.com"
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "alpha", AcmeEmail: &email})
	require.NoError(t, err)
	svcRow, err := q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: env.ID, Name: "web", Slug: "web", Kind: "app", SourceType: "image",
		Config: json.RawMessage(`{"image":"nginx:1","port":80}`), ServerID: &srv.ID,
	})
	require.NoError(t, err)

	add := func(host, tls, resolver string) db.Domain {
		t.Helper()
		d, err := q.InsertDomain(bg, db.InsertDomainParams{ServiceID: svcRow.ID, Hostname: host, TargetPort: 80, TlsMode: tls, DnsResolver: resolver})
		require.NoError(t, err)
		return d
	}
	auto := add("auto.example.com", "", "")
	custom := add("custom.example.com", "custom", "")
	plain := add("plain.example.com", "none", "")
	dnsd := add("dns.example.com", "", "cloudflare")
	wild := add("app.wild.example.com", "", "")
	uploaded := add("up.example.com", "", "")
	all := []db.Domain{auto, custom, plain, dnsd, wild, uploaded}

	_, err = q.InsertWildcardCert(bg, db.InsertWildcardCertParams{WorkspaceID: ws.ID, ServerID: srv.ID, Domain: "wild.example.com", Provider: "cloudflare"})
	require.NoError(t, err)
	_, err = q.InsertCertificate(bg, db.InsertCertificateParams{
		WorkspaceID: ws.ID, ServerID: srv.ID, Name: "up", Domains: []string{"up.example.com"}, CertPem: "pem", KeyEnc: []byte("k"),
		Fingerprint: "fp", Issuer: "x", NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), ID: uuid.New(),
	})
	require.NoError(t, err)

	warned := func() map[string]string {
		t.Helper()
		certs, err := DomainCerts(bg, q, all)
		require.NoError(t, err)
		out := map[string]string{}
		for _, d := range all {
			if w := certs[d.ID].Warning; w != "" {
				out[d.Hostname] = w
			}
		}
		return out
	}
	report := func(port int) {
		t.Helper()
		raw, _ := json.Marshal(agentprotocol.ProxyPorts{HTTP: port, HTTPS: port + 363, Source: agentprotocol.ProxyPortsFlag, FlagHTTP: port, FlagHTTPS: port + 363})
		_, err := q.RecordServerProxyPorts(bg, db.RecordServerProxyPortsParams{ID: srv.ID, Ports: raw})
		require.NoError(t, err)
	}

	// No report yet and nothing applied: the default :80.
	require.Empty(t, warned())

	// :8080 - only the plain HTTP-01 domain depends on a certificate it cannot get.
	report(8080)
	got := warned()
	require.Len(t, got, 1, "tls custom/none, a DNS-01 resolver, a wildcard and an uploaded certificate are all fine: %v", got)
	require.Equal(t, HTTP01Warning(8080, "auto.example.com"), got["auto.example.com"])

	// Back on :80: nothing to warn about.
	report(80)
	require.Empty(t, warned())

	// The applied server setting counts while the agent has not reported ports.
	_, err = q.RecordServerProxyPorts(bg, db.RecordServerProxyPortsParams{ID: srv.ID, Ports: []byte(`{"http":0,"https":0}`)})
	require.NoError(t, err)
	_, err = pool.Exec(bg, `UPDATE servers SET proxy_applied_http_port = 8081 WHERE id = $1`, srv.ID)
	require.NoError(t, err)
	require.Equal(t, HTTP01Warning(8081, "auto.example.com"), warned()["auto.example.com"])

	// Without an ACME contact no certificate is requested at all.
	_, err = pool.Exec(bg, `UPDATE servers SET acme_email = NULL WHERE id = $1`, srv.ID)
	require.NoError(t, err)
	require.Empty(t, warned())
	_, err = pool.Exec(bg, `UPDATE servers SET acme_email = 'ops@example.com' WHERE id = $1`, srv.ID)
	require.NoError(t, err)

	// A domain served through the server's Cloudflare Tunnel never uses HTTP-01
	// (Cloudflare terminates the public TLS).
	_, err = pool.Exec(bg, `UPDATE domains SET cf_zone_id = 'z1' WHERE id = $1`, auto.ID)
	require.NoError(t, err)
	_, err = pool.Exec(bg, `INSERT INTO server_tunnels (server_id, workspace_id, account_id, tunnel_id, tunnel_name, state, activated_at)
		VALUES ($1, $2, 'acc', 't1', 'tunnel', 'online', now())`, srv.ID, ws.ID)
	require.NoError(t, err)
	require.Empty(t, warned())
	_, err = pool.Exec(bg, `DELETE FROM server_tunnels WHERE server_id = $1`, srv.ID)
	require.NoError(t, err)
	_, err = pool.Exec(bg, `UPDATE domains SET cf_zone_id = '' WHERE id = $1`, auto.ID)
	require.NoError(t, err)

	// The server proxy plan asks for the domains of the server with the HTTP port a deploy would apply.
	row, err := q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID})
	require.NoError(t, err)
	plan8080, err := HTTP01Warnings(bg, q, row, 8080)
	require.NoError(t, err)
	require.Equal(t, []string{HTTP01Warning(8080, "auto.example.com")}, plan8080)
	plan80, err := HTTP01Warnings(bg, q, row, 80)
	require.NoError(t, err)
	require.Empty(t, plan80)
}
