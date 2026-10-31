package app_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// certPair returns a self-signed certificate and its PKCS#8 key (PEM).
func certPair(t *testing.T, dns ...string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: dns[0]}, DNSNames: dns,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(60 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}))
}

func TestServerProxyCertificatesTailnetAndScopesOverHTTP(t *testing.T) {
	h := newHarness(t)
	h.setup()
	ctx := context.Background()

	r := h.do("POST", "/servers/enrollment-tokens", "", map[string]any{"name": "edge"})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	serverID := r.JSON()["server"].(map[string]any)["id"].(string)
	base := "/servers/" + serverID

	// The agent is too old until it advertises proxy.v2.
	certPEM, keyPEM := certPair(t, "app.example.com")
	upload := map[string]any{"name": "app", "certPem": certPEM, "keyPem": keyPEM}
	r = h.do("POST", base+"/certificates", "", upload)
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "agent_too_old", r.JSON()["code"])
	r = h.do("GET", base+"/proxy", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, true, r.JSON()["agentTooOld"])
	_, err := h.app.Pool.Exec(ctx, `UPDATE servers SET facts = '{"capabilities":["proxy.v2"],"hostTailscale":{"detected":false}}'::jsonb WHERE id = $1`, serverID)
	require.NoError(t, err)

	// Upload (Idempotency-Key replays byte-identically), never returning the key.
	r = h.do("POST", base+"/certificates", "", upload, "Idempotency-Key", "cert-1")
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	certID := r.JSON()["id"].(string)
	require.Equal(t, "add", r.JSON()["change"])
	require.NotContains(t, string(r.Raw), "PRIVATE KEY")
	replay := h.do("POST", base+"/certificates", "", upload, "Idempotency-Key", "cert-1")
	require.Equal(t, r.Raw, replay.Raw)

	r = h.do("GET", base+"/certificates", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Len(t, r.JSON()["items"], 1)
	require.NotContains(t, string(r.Raw), "PRIVATE KEY")

	// Server list and view expose the proxy summary.
	r = h.do("GET", "/servers", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	px := r.JSON()["items"].([]any)[0].(map[string]any)["proxy"].(map[string]any)
	require.Equal(t, true, px["v2"])
	require.EqualValues(t, 1, px["generation"])
	require.EqualValues(t, 0, px["appliedGeneration"])
	require.Equal(t, "off", px["tailnet"].(map[string]any)["mode"])

	// Scopes: a proxy:write token manages certs and the host gateway, but not the SSRF toggles or the integration.
	tok := h.createToken([]string{"proxy:write", "servers:read"}, nil)
	r = h.do("PATCH", base+"/proxy/settings", tok, map[string]any{"upstreamPolicy": map[string]any{"allowLoopback": true}})
	require.Equal(t, http.StatusForbidden, r.Status, string(r.Raw))
	r = h.do("PATCH", base+"/proxy/settings", tok, map[string]any{"upstreamPolicy": map[string]any{"allowHostGateway": false}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, false, r.JSON()["upstreamPolicy"].(map[string]any)["allowHostGateway"])
	r = h.do("GET", "/integrations/tailscale", tok, nil)
	require.Equal(t, http.StatusForbidden, r.Status, string(r.Raw))
	ro := h.createToken([]string{"servers:read"}, nil)
	r = h.do("POST", base+"/proxy/deploy", ro, map[string]any{})
	require.Equal(t, http.StatusForbidden, r.Status, string(r.Raw))
	r = h.do("GET", base+"/proxy", ro, nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))

	// Tailnet: managed needs the integration; the secret is write-only.
	r = h.do("PATCH", base+"/proxy/settings", "", map[string]any{"tailnet": map[string]any{"mode": "managed"}})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "tailnet_not_configured", r.JSON()["code"])
	r = h.do("GET", "/integrations/tailscale", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, false, r.JSON()["configured"])
	r = h.do("PUT", "/integrations/tailscale", "", map[string]any{"clientId": "kABC", "clientSecret": "tskey-client-HTTPSECRET"})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.NotContains(t, string(r.Raw), "HTTPSECRET")
	require.Equal(t, true, r.JSON()["secretSet"])
	r = h.do("PATCH", base+"/proxy/settings", "", map[string]any{"tailnet": map[string]any{"mode": "managed", "magicDns": true}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	tn := r.JSON()["tailnet"].(map[string]any)
	require.Equal(t, "managed", tn["mode"])
	require.Equal(t, "off", tn["appliedMode"])
	require.Equal(t, "nebula-edge", tn["hostname"])

	r = h.do("POST", base+"/proxy/plan", "", map[string]any{})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, true, r.JSON()["restartRequired"])
	require.NotEmpty(t, r.JSON()["diff"])

	// Deploy: 202, idempotent, the persisted payload carries no secrets.
	r = h.do("POST", base+"/proxy/deploy", "", map[string]any{}, "Idempotency-Key", "deploy-1")
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	cmdID := r.JSON()["commandId"].(string)
	replay = h.do("POST", base+"/proxy/deploy", "", map[string]any{}, "Idempotency-Key", "deploy-1")
	require.Equal(t, r.Raw, replay.Raw)
	r = h.do("POST", base+"/proxy/deploy", "", map[string]any{}, "Idempotency-Key", "deploy-2")
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "proxy_deploy_in_progress", r.JSON()["code"])
	var payload string
	require.NoError(t, h.app.Pool.QueryRow(ctx, `SELECT payload::text FROM agent_commands WHERE id = $1`, cmdID).Scan(&payload))
	require.Contains(t, payload, `"provision": true`)
	for _, banned := range []string{"PRIVATE KEY", "tskey", "HTTPSECRET"} {
		require.False(t, strings.Contains(payload, banned), banned)
	}

	r = h.do("GET", base+"/proxy/routes", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))

	// Another workspace cannot reach these (404 is covered by the service tests); deleting works.
	r = h.do("DELETE", "/certificates/"+certID, "", nil)
	require.Equal(t, http.StatusNoContent, r.Status, string(r.Raw))
	r = h.do("DELETE", "/certificates/"+certID, "", nil)
	require.Equal(t, http.StatusNotFound, r.Status, string(r.Raw))

	// The integration cannot be removed while a server uses a managed tailnet.
	r = h.do("DELETE", "/integrations/tailscale", "", nil)
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "integration_in_use", r.JSON()["code"])
}
