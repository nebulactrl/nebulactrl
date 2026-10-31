package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const dnsTokenCanary = "cf-token-canary-0123456789abcdef"

// A deploy command carries the server's applied proxy configuration, and with a
// DNS-01 resolver in it the send-time secrets must include the resolver's
// credential: it is looked up for the command's server (a credential is only
// readable for a server of its team). Regression: the lookup ran with no server
// and every deploy to a server with a resolver failed with "Not delivered: could
// not resolve the command's secrets: load dns credential ...: no rows".
func TestDeployToAServerWithADNSResolverResolvesItsCredential(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.startApp()
	e := h.newVarEnv("dnssecrets")
	svc := e.service("api", "app", nil)

	r := h.do("POST", "/dns/credentials", "", map[string]any{
		"name": "cf", "provider": "cloudflare", "serverId": e.srvID,
		"values": map[string]string{"CF_DNS_API_TOKEN": dnsTokenCanary},
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	require.NotContains(t, string(r.Raw), dnsTokenCanary)
	credID := r.JSON()["id"].(string)

	r = h.do("POST", "/services/"+svc+"/deployments", "", map[string]any{})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	cmdID, payload := h.deployCommand(e.srvID)

	// The applied proxy configuration of the server, as BuildConfig attaches it.
	var p map[string]any
	require.NoError(t, json.Unmarshal(payload, &p))
	p["proxy"] = map[string]any{"dnsResolvers": []map[string]any{{
		"id": credID, "provider": "cloudflare", "rev": 1, "envNames": []string{"CF_DNS_API_TOKEN"},
	}}}
	withProxy, err := json.Marshal(p)
	require.NoError(t, err)

	secrets, err := h.extRig().deps.SecretsForCommand(context.Background(), cmdID, agentprotocol.KindDeploy, withProxy)
	require.NoError(t, err)
	require.Equal(t, dnsTokenCanary, secrets[agentprotocol.DNSSecretKey(credID, "CF_DNS_API_TOKEN")])
	require.NotContains(t, string(withProxy), dnsTokenCanary, "the stored payload carries variable names only")
}
