package agentprotocol

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProxyConfigurePayloadCarriesPorts(t *testing.T) {
	in := ProxyConfigurePayload{Proxy: ProxyConfig{UpstreamPolicy: DefaultUpstreamPolicy(), HTTPPort: 8080, HTTPSPort: 8443}}
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"httpPort":8080`)
	require.Contains(t, string(raw), `"httpsPort":8443`)
	var out ProxyConfigurePayload
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Equal(t, 8080, out.Proxy.HTTPPort)
	require.Equal(t, 8443, out.Proxy.HTTPSPort)

	// 0 means "keep the agent's own port" and is not sent at all, so an older
	// agent sees the same payload as before.
	raw, err = json.Marshal(ProxyConfigurePayload{Proxy: ProxyConfig{UpstreamPolicy: DefaultUpstreamPolicy()}})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "httpPort")
	require.NotContains(t, string(raw), "httpsPort")
}

func TestHeartbeatCarriesProxyPorts(t *testing.T) {
	hb := Heartbeat{Proxy: ProxyHealth{Status: ProxyOK, Ports: &ProxyPorts{HTTP: 8080, HTTPS: 8443, Source: ProxyPortsServer, FlagHTTP: 80, FlagHTTPS: 443, BusyOther: []int{443, 80}}}}
	hb.Proxy.Ports.Normalize()
	raw, err := json.Marshal(hb)
	require.NoError(t, err)
	var out Heartbeat
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Equal(t, hb.Proxy, out.Proxy)
	require.Equal(t, []int{80, 443}, out.Proxy.Ports.BusyOther)

	// An old agent's heartbeat has no ports.
	var old Heartbeat
	require.NoError(t, json.Unmarshal([]byte(`{"proxy":{"status":"ok"}}`), &old))
	require.Nil(t, old.Proxy.Ports)
}

func TestValidateProxyPorts(t *testing.T) {
	require.NoError(t, ValidateProxyPorts(80, 443, ProxyAdminPort))
	require.NoError(t, ValidateProxyPorts(8080, 8443, 0))
	require.ErrorContains(t, ValidateProxyPorts(0, 443, ProxyAdminPort), "not between")
	require.ErrorContains(t, ValidateProxyPorts(80, 65536, ProxyAdminPort), "not between")
	require.ErrorContains(t, ValidateProxyPorts(8080, 8080, ProxyAdminPort), "must differ")
	require.ErrorContains(t, ValidateProxyPorts(ProxyAdminPort, 443, ProxyAdminPort), "admin port")
	require.ErrorContains(t, ValidateProxyPorts(80, ProxyAdminPort, ProxyAdminPort), "admin port")

	require.NoError(t, ValidateProxyConfigPorts(ProxyConfig{}))
	require.NoError(t, ValidateProxyConfigPorts(ProxyConfig{HTTPPort: 8080}))
	require.ErrorContains(t, ValidateProxyConfigPorts(ProxyConfig{HTTPPort: 8080, HTTPSPort: 8080}), "must differ")
	require.ErrorContains(t, ValidateProxyConfigPorts(ProxyConfig{HTTPSPort: -1}), "not between")
}
