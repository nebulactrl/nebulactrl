package engine

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestInheritMaintenanceFollowsTheLiveTable(t *testing.T) {
	m := &agentprotocol.Maintenance{Title: "Upgrade"}
	live := []agentprotocol.Route{
		{ServiceID: "a", RouteExt: agentprotocol.RouteExt{Maintenance: m}},
		{ServiceID: "b"},
		{ServiceID: "l4", RouteExt: agentprotocol.RouteExt{Protocol: "tcp", Maintenance: m}},
	}
	// A deploy payload built before the toggle: a is off, b is on.
	payload := []agentprotocol.Route{
		{ServiceID: "a", Hosts: []string{"a.example.com"}},
		{ServiceID: "b", Hosts: []string{"b.example.com"}, RouteExt: agentprotocol.RouteExt{Maintenance: m}},
		{ServiceID: "new", Hosts: []string{"n.example.com"}},
		{ServiceID: "l4", RouteExt: agentprotocol.RouteExt{Protocol: "tcp"}},
	}
	got := inheritMaintenance(payload, live)
	require.Equal(t, m, got[0].Maintenance, "maintenance that began after dispatch is kept")
	require.Nil(t, got[1].Maintenance, "maintenance that ended after dispatch stays ended")
	require.Nil(t, got[2].Maintenance)
	require.Nil(t, got[3].Maintenance, "tcp/udp routes never carry it")
	require.Equal(t, "a.example.com", got[0].Hosts[0])
	require.NotNil(t, payload[1].Maintenance, "the input is not modified")
}
