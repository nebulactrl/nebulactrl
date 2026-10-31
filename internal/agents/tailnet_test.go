package agents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestHeartbeatPersistsTailnetStatusAndServerViewExposesIt(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-tailnet")
	// The agent reported a managed node earlier and the control plane applied it.
	_, err := h.pool.Exec(h.ctx, `UPDATE servers SET tailnet_mode='managed', tailnet_applied_mode='managed', tailnet_hostname='nebula-edge',
		proxy_generation=3, proxy_applied_generation=2 WHERE id=$1`, e.serverID)
	require.NoError(t, err)

	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	a.write(agentprotocol.TypeHeartbeat, agentprotocol.Heartbeat{
		DockerVersion: "29.0.0", Proxy: agentprotocol.ProxyHealth{Status: agentprotocol.ProxyOK},
		Tailnet: &agentprotocol.TailnetStatus{
			Mode: agentprotocol.TailnetManaged, State: agentprotocol.TailnetStateOnline, FQDN: "nebula-edge.tail1234.ts.net",
			IPs: []string{"100.64.0.7"}, DeviceID: "nABC123CNTRL",
		},
	})
	require.Eventually(t, func() bool {
		s, err := h.svc.Get(h.ctx, e.serverID)
		return err == nil && s.Proxy.Tailnet.State == "online"
	}, 5*time.Second, 20*time.Millisecond)

	s, err := h.svc.Get(h.ctx, e.serverID)
	require.NoError(t, err)
	require.Equal(t, "nebula-edge.tail1234.ts.net", s.Proxy.Tailnet.FQDN)
	require.Equal(t, []string{"100.64.0.7"}, s.Proxy.Tailnet.IPs)
	require.Equal(t, "managed", s.Proxy.Tailnet.Mode)
	require.EqualValues(t, 3, s.Proxy.Generation)
	require.EqualValues(t, 2, s.Proxy.AppliedGeneration)
	require.False(t, s.Proxy.V2, "no capabilities were advertised")

	var device *string
	require.NoError(t, h.pool.QueryRow(h.ctx, `SELECT tailnet_device_id FROM servers WHERE id=$1`, e.serverID).Scan(&device))
	require.NotNil(t, device)
	require.Equal(t, "nABC123CNTRL", *device)

	// A heartbeat without a tailnet clears the stored status.
	a.write(agentprotocol.TypeHeartbeat, agentprotocol.Heartbeat{DockerVersion: "29.0.0", Proxy: agentprotocol.ProxyHealth{Status: agentprotocol.ProxyOK}})
	require.Eventually(t, func() bool {
		s, err := h.svc.Get(h.ctx, e.serverID)
		return err == nil && s.Proxy.Tailnet.State == ""
	}, 5*time.Second, 20*time.Millisecond)
}
