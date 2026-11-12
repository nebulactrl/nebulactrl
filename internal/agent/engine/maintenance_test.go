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
	got := inheritOperational(payload, live)
	require.Equal(t, m, got[0].Maintenance, "maintenance that began after dispatch is kept")
	require.Nil(t, got[1].Maintenance, "maintenance that ended after dispatch stays ended")
	require.Nil(t, got[2].Maintenance)
	require.Nil(t, got[3].Maintenance, "tcp/udp routes never carry it")
	require.Equal(t, "a.example.com", got[0].Hosts[0])
	require.NotNil(t, payload[1].Maintenance, "the input is not modified")
}

func TestInheritFirewallFollowsTheLiveTableAndKeepsANewRoutesOwn(t *testing.T) {
	block := func(id string) *agentprotocol.Firewall {
		return &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{{ID: id, Name: id, Action: agentprotocol.FirewallBlock,
			Conditions: []agentprotocol.FirewallCondition{{Field: agentprotocol.FirewallFieldPath, Op: agentprotocol.FirewallOpPrefix, Values: []string{"/admin"}}}}}}
	}
	live := []agentprotocol.Route{
		{ServiceID: "changed", RouteExt: agentprotocol.RouteExt{Firewall: block("live")}},
		{ServiceID: "removed"},
	}
	// A deploy payload dispatched before the last firewall changes, plus the
	// first deploy of a service whose route is not live yet.
	payload := []agentprotocol.Route{
		{ServiceID: "changed", RouteExt: agentprotocol.RouteExt{Firewall: block("stale")}},
		{ServiceID: "removed", RouteExt: agentprotocol.RouteExt{Firewall: block("stale")}},
		{ServiceID: "first", RouteExt: agentprotocol.RouteExt{Firewall: block("own")}},
	}
	got := inheritOperational(payload, live)
	require.Equal(t, "live", got[0].Firewall.Rules[0].ID, "a firewall changed after dispatch is kept")
	require.Nil(t, got[1].Firewall, "a firewall removed after dispatch stays removed")
	require.Equal(t, "own", got[2].Firewall.Rules[0].ID, "a first deploy is protected by the firewall it carries")
}
