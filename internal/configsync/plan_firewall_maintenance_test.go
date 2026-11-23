package configsync

// Database-backed plan tests of [services.<s>.firewall] and [services.<s>.maintenance]: both are
// immediate fields owned as one unit, the firewall is checked against the server at plan time, and
// a prune removes them. They follow the pattern of the uptime and task plan tests.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const planFirewallTable = `
[[services.web.firewall.rules]]
name = "Allow the office"
action = "allow"

[[services.web.firewall.rules.conditions]]
field = "ip"
op = "in"
values = ["203.0.113.0/24"]

[[services.web.firewall.rules]]
name = "Block WordPress probes"
action = "block"

[[services.web.firewall.rules.conditions]]
field = "path"
op = "contains"
values = ["wp-login.php", "/wp-admin"]
`

const planMaintenanceTable = `
[services.web.maintenance]
enabled = true
title = "Back soon"
message = "Upgrading the database."
eta = "2099-12-01T06:00:00Z"
`

// firewallServer lets the fixture's server run firewalls (its facts list the capability); geo turns on
// the geolocation country rules need.
func (f *planFixture) firewallServer(geo bool) {
	f.t.Helper()
	facts, err := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapProxyV2, agentprotocol.CapFirewall}})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(context.Background(),
		"UPDATE servers SET facts = $2, geo_enabled = $3, traffic_analytics_enabled = $3 WHERE id = $1", f.server.ID, facts, geo)
	require.NoError(f.t, err)
}

// setFirewall is a dashboard edit of a service's firewall (what firewall.Service.Put stores).
func (f *planFixture) setFirewall(id uuid.UUID, rules ...agentprotocol.FirewallRule) {
	f.t.Helper()
	var raw json.RawMessage
	if len(rules) > 0 {
		var err error
		raw, err = json.Marshal(agentprotocol.Firewall{Rules: rules})
		require.NoError(f.t, err)
	}
	_, err := f.q.SetServiceFirewall(context.Background(), db.SetServiceFirewallParams{ID: id, Firewall: raw})
	require.NoError(f.t, err)
}

// setMaintenance is a dashboard edit of a service's maintenance mode (what maintenance.Service.Set stores).
func (f *planFixture) setMaintenance(id uuid.UUID, on bool, title, message string, eta *time.Time) {
	f.t.Helper()
	now := time.Now()
	_, err := f.q.SetServiceMaintenance(context.Background(), db.SetServiceMaintenanceParams{
		ID: id, Enabled: on, Title: title, Message: message, Eta: eta, Since: &now,
	})
	require.NoError(f.t, err)
}

func planFirewallRule(id, name, action string, c agentprotocol.FirewallCondition) agentprotocol.FirewallRule {
	return agentprotocol.FirewallRule{ID: id, Name: name, Action: action, Conditions: []agentprotocol.FirewallCondition{c}}
}

var (
	planOfficeRule = planFirewallRule("office1", "Allow the office", agentprotocol.FirewallAllow,
		agentprotocol.FirewallCondition{Field: agentprotocol.FirewallFieldIP, Op: agentprotocol.FirewallOpIn, Values: []string{"203.0.113.0/24"}})
	planWordpressRule = planFirewallRule("wp1", "Block WordPress probes", agentprotocol.FirewallBlock,
		agentprotocol.FirewallCondition{Field: agentprotocol.FirewallFieldPath, Op: agentprotocol.FirewallOpContains, Values: []string{"wp-login.php", "/wp-admin"}})
)

func TestPlanDB_FirewallAndMaintenanceArePlannedAsImmediateChanges(t *testing.T) {
	f := newPlanFixture(t)
	f.firewallServer(false)
	f.image(f.staging, "web", 8080)

	p := f.plan(f.staging, planWebHead("8080")+planFirewallTable+planMaintenanceTable)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps := planSvc(t, p, "web")
	require.Equal(t, ActionUpdate, ps.Action)
	require.Equal(t, []string{"firewall", "maintenance"}, planSorted(planFieldsOf(ps.Changes)))
	for _, c := range ps.Changes {
		require.Equal(t, AppliesImmediate, c.Applies, c.Field)
	}
	fw := planChangeOf(t, ps, "firewall")
	require.Equal(t, "services.web.firewall", fw.Path)
	require.Equal(t, `allow "Allow the office"; block "Block WordPress probes"`, fw.To)
	require.Equal(t, "no rules", fw.From)
	mt := planChangeOf(t, ps, "maintenance")
	require.Equal(t, "services.web.maintenance", mt.Path)
	require.Equal(t, `on: "Back soon" until 2099-12-01T06:00:00Z`, mt.To)
	require.Equal(t, "off", mt.From)
	require.True(t, p.HasImmediateChanges())
	require.Empty(t, p.Summary.RequiresDeploy, "operational settings need no deploy")
	require.Equal(t, []string{"projects:write", "deployments:write", "proxy:write"}, p.RequiredScopes)
}

func TestPlanDB_FirewallNeedsProxyWriteAndMaintenanceDeploymentsWrite(t *testing.T) {
	f := newPlanFixture(t)
	f.firewallServer(false)
	f.image(f.staging, "web", 8080)
	for name, c := range map[string]struct {
		file string
		want []string
	}{
		"firewall":    {planWebHead("8080") + planFirewallTable, []string{"projects:write", "proxy:write"}},
		"maintenance": {planWebHead("8080") + planMaintenanceTable, []string{"projects:write", "deployments:write"}},
	} {
		t.Run(name, func(t *testing.T) {
			p := f.plan(f.staging, c.file)
			require.True(t, p.Valid, "%v", p.Diagnostics)
			require.Equal(t, c.want, p.RequiredScopes)
		})
	}
}

// A file without ids is the live rules when their order, names and conditions are: it must not
// look changed on every plan, and an edit in the dashboard is drift, not a difference of ids.
func TestPlanDB_FirewallWithoutIDsKeepsTheLiveIDsByPosition(t *testing.T) {
	f := newPlanFixture(t)
	f.firewallServer(false)
	web := f.image(f.staging, "web", 8080)
	f.setFirewall(web.ID, planOfficeRule, planWordpressRule)
	file := planWebHead("8080") + planFirewallTable
	f.markApplied(f.staging, file)

	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps := planSvc(t, p, "web")
	require.Equal(t, ActionUnchanged, ps.Action)
	require.Equal(t, StateInSync, ps.State)
	require.Empty(t, ps.Changes)

	// Another person turns the second rule off in the dashboard: drift, and the file would turn it back on.
	off := planWordpressRule
	off.Disabled = true
	f.setFirewall(web.ID, planOfficeRule, off)
	ps = planSvc(t, f.plan(f.staging, file), "web")
	require.Equal(t, StateDrift, ps.State)
	require.Equal(t, []string{"firewall"}, planFieldsOf(ps.Changes))
	require.Equal(t, ReasonDrift, ps.Changes[0].Reason)
	require.Contains(t, ps.Changes[0].From, "(disabled)")

	// A third rule in the dashboard that the file does not list is drift too: the list is one unit.
	extra := planFirewallRule("tail1", "Block scanners", agentprotocol.FirewallBlock,
		agentprotocol.FirewallCondition{Field: agentprotocol.FirewallFieldUserAgent, Op: agentprotocol.FirewallOpContains, Values: []string{"sqlmap"}})
	f.setFirewall(web.ID, planOfficeRule, planWordpressRule, extra)
	ps = planSvc(t, f.plan(f.staging, file), "web")
	require.Equal(t, []string{"firewall"}, planFieldsOf(ps.Changes))
	require.Equal(t, StateDrift, ps.State)
	require.Contains(t, ps.Changes[0].From, "Block scanners")
}

func TestPlanDB_FirewallIsCheckedAgainstTheServerAtPlanTime(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	file := planWebHead("8080") + planFirewallTable
	countryRule := func(extra string) string {
		return planWebHead("8080") + "\n[[services.web.firewall.rules]]\nname = \"Block countries\"\naction = \"block\"\n" + extra + `
[[services.web.firewall.rules.conditions]]
field = "country"
op = "in"
values = ["CN", "RU"]
`
	}
	country := countryRule("")

	// The fixture's agent predates firewalls: the plan is blocked instead of the apply failing.
	p := f.plan(f.staging, file)
	require.False(t, p.Valid)
	require.Equal(t, []string{"services.web.firewall"}, planDiagPaths(p.Diagnostics, "agent_too_old"), "%v", p.Diagnostics)
	require.Contains(t, planSvc(t, p, "web").Blockers, "agent_too_old")
	require.Equal(t, ActionBlocked, planSvc(t, p, "web").Action)

	// A firewall the apply leaves alone is not written, so it is not checked.
	f.setFirewall(web.ID, planOfficeRule, planWordpressRule)
	p = f.plan(f.staging, file)
	require.Empty(t, planDiagPaths(p.Diagnostics, "agent_too_old"), "%v", p.Diagnostics)
	f.setFirewall(web.ID)

	// With the capability, a country rule needs geolocation on the server.
	f.firewallServer(false)
	require.True(t, f.plan(f.staging, file).Valid)
	p = f.plan(f.staging, country)
	require.False(t, p.Valid)
	require.Equal(t, []string{"services.web.firewall"}, planDiagPaths(p.Diagnostics, "geo_required"), "%v", p.Diagnostics)
	f.firewallServer(true)
	p = f.plan(f.staging, country)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Equal(t, []string{"firewall"}, planFieldsOf(planSvc(t, p, "web").Changes))

	// A disabled country rule matches nobody, so it needs no geolocation.
	f.firewallServer(false)
	require.True(t, f.plan(f.staging, countryRule("disabled = true\n")).Valid)
}

func TestPlanDB_MaintenanceRefusesAnETAThatHasPassed(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	past := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	file := planWebHead("8080") + "\n[services.web.maintenance]\nenabled = true\ntitle = \"Back soon\"\neta = \"" + past.Format(time.RFC3339) + "\"\n"

	p := f.plan(f.staging, file)
	require.False(t, p.Valid)
	require.Equal(t, []string{"services.web.maintenance.eta"}, planDiagPaths(p.Diagnostics, "invalid_value"), "%v", p.Diagnostics)

	// The window that is live already (its time has passed since): nothing changes, so nothing is refused.
	f.setMaintenance(web.ID, true, "Back soon", "", &past)
	p = f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Empty(t, planSvc(t, p, "web").Changes)
}

func TestPlanDB_MaintenanceOffIgnoresTheStoredText(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	eta := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	f.setMaintenance(web.ID, false, "Earlier window", "Text kept from last time.", &eta)

	file := planWebHead("8080") + "\n[services.web.maintenance]\nenabled = false\n"
	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Empty(t, planSvc(t, p, "web").Changes, "off is off, whatever text is stored")

	p = f.plan(f.staging, planWebHead("8080")+"\n[services.web.maintenance]\nenabled = true\n")
	require.Equal(t, []string{"maintenance"}, planFieldsOf(planSvc(t, p, "web").Changes))
}

func TestPlanDB_FirewallAndMaintenanceOfAnHTTPRoute(t *testing.T) {
	f := newPlanFixture(t)
	f.firewallServer(false)
	f.route(f.staging)
	file := planHeadServer + `
[services.printer.route]
protocol = "http"
upstreams = [{ address = "192.168.1.20:631" }]

[[services.printer.firewall.rules]]
name = "Only GET and POST"
action = "block"

[[services.printer.firewall.rules.conditions]]
field = "method"
op = "in"
values = ["GET", "POST"]
not = true

[services.printer.maintenance]
enabled = true
`
	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Equal(t, []string{"firewall", "maintenance"}, planSorted(planFieldsOf(planSvc(t, p, "printer").Changes)))
}

func TestPlanDB_PruneRemovesTheFirewallAndSwitchesMaintenanceOff(t *testing.T) {
	f := newPlanFixture(t)
	f.firewallServer(false)
	web := f.image(f.staging, "web", 8080)
	f.setFirewall(web.ID, planOfficeRule, planWordpressRule)
	eta := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	f.setMaintenance(web.ID, true, "Back soon", "Upgrading the database.", &eta)
	both := planWebHead("8080") + planFirewallTable + "\n[services.web.maintenance]\nenabled = true\ntitle = \"Back soon\"\nmessage = \"Upgrading the database.\"\neta = \"" + eta.Format(time.RFC3339) + "\"\n"
	f.markApplied(f.staging, both)
	require.Empty(t, planSvc(t, f.plan(f.staging, both), "web").Changes)

	gone := planWebHead("8080")

	// Without prune the tables are only released: nothing changes, and the plan says so.
	p := f.plan(f.staging, gone)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps := planSvc(t, p, "web")
	require.Empty(t, ps.Changes)
	require.ElementsMatch(t, []string{"services.web.firewall", "services.web.maintenance"}, ps.Released)

	// With prune both are removals, applied at once.
	req := f.planReq(f.staging, gone)
	req.Prune = true
	p = f.planWith(req)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps = planSvc(t, p, "web")
	require.Equal(t, []string{"firewall", "maintenance"}, planSorted(planFieldsOf(ps.Changes)))
	for _, c := range ps.Changes {
		require.Equal(t, OpRemove, c.Op, c.Field)
		require.Equal(t, AppliesImmediate, c.Applies, c.Field)
	}
	require.Equal(t, 2, p.Summary.Pruned)
	require.Empty(t, p.Summary.RequiresDeploy, "removing operational settings needs no deploy")
}
