package configsync_test

// Apply of [services.<s>.firewall] and [services.<s>.maintenance]: both are immediate steps that run
// through firewall.Service.Put and maintenance.Service.Set after the staged part committed, the rule
// list and the maintenance table are each owned as one unit, and a prune removes them (empties the
// firewall, switches maintenance off). These need the database like the other apply tests.

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/configsync"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const applyFirewallTables = `
[[services.web.firewall.rules]]
id = "office1"
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

const applyMaintenanceTable = `
[services.web.maintenance]
enabled = true
title = "Back soon"
message = "Upgrading the database."
eta = "2099-12-01T06:00:00Z"
`

var applyRuleID = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// firewallServer lets the fixture's agent run firewalls (its facts list the capability); geo turns on the
// geolocation country rules need.
func (f *applyFixture) firewallServer(geo bool) {
	f.t.Helper()
	facts, err := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapProxyV2, agentprotocol.CapFirewall}})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(context.Background(),
		"UPDATE servers SET facts = $2, geo_enabled = $3, traffic_analytics_enabled = $3 WHERE id = $1", f.server.ID, facts, geo)
	require.NoError(f.t, err)
}

// rules are the firewall rules stored on a service.
func (f *applyFixture) rules(id uuid.UUID) []agentprotocol.FirewallRule {
	f.t.Helper()
	if fw := domains.DecodeFirewall(f.row(id).Firewall); fw != nil {
		return fw.Rules
	}
	return nil
}

func applyRuleNames(rules []agentprotocol.FirewallRule) []string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = r.Name
	}
	return out
}

func TestApplyFirewallAndMaintenance(t *testing.T) {
	f := newApplyFixture(t)
	f.firewallServer(false)
	file := applyWebFile(3000, applyFirewallTables+applyMaintenanceTable)

	pl := f.plan("staging", file)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	res := f.mustApply(file, applyWithPlanID(pl.PlanID))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "firewall:web"), applyJSON(res.Steps))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "maintenance:web"), applyJSON(res.Steps))

	web := f.svcRow(f.staging, "web")
	rules := f.rules(web.ID)
	require.Equal(t, []string{"Allow the office", "Block WordPress probes"}, applyRuleNames(rules))
	require.Equal(t, "office1", rules[0].ID, "an id the file writes is kept")
	require.Regexp(t, applyRuleID, rules[1].ID, "the platform makes one for a rule without an id")
	require.Equal(t, agentprotocol.FirewallAllow, rules[0].Action)
	require.Equal(t, []string{"203.0.113.0/24"}, rules[0].Conditions[0].Values)
	require.Equal(t, agentprotocol.FirewallFieldPath, rules[1].Conditions[0].Field)
	require.Equal(t, []string{"wp-login.php", "/wp-admin"}, rules[1].Conditions[0].Values)

	row := f.row(web.ID)
	require.True(t, row.MaintenanceEnabled)
	require.Equal(t, "Back soon", row.MaintenanceTitle)
	require.Equal(t, "Upgrading the database.", row.MaintenanceMessage)
	require.NotNil(t, row.MaintenanceEta)
	require.True(t, row.MaintenanceEta.Equal(time.Date(2099, 12, 1, 6, 0, 0, 0, time.UTC)))

	owned := f.owned(web.ID)
	require.Contains(t, owned, "firewall")
	require.Contains(t, owned, "maintenance")
	require.Equal(t, applyFP(projectconfig.FirewallValueOf(rules)), owned["firewall"], "ownership records the rules as stored, with the ids the platform made")

	// The file without ids is what is live: nothing to do, and the ids stay.
	require.Equal(t, configsync.StatusNoop, f.mustApply(file).Status)
	require.Equal(t, rules, f.rules(web.ID))

	// An edit of a rule and of the page text changes the unit; the rules keep the ids of their positions.
	v2 := strings.NewReplacer(`name = "Block WordPress probes"`, `name = "Block WordPress and PHP probes"`, "Upgrading the database.", "Upgrading the database. Back by noon.").Replace(file)
	pl = f.plan("staging", v2)
	ps := applyPlanService(t, pl, "web")
	require.Equal(t, []string{"firewall", "maintenance"}, applyChangeFields(ps))
	require.Equal(t, configsync.StateFileChanged, ps.State, "the live rules are what the last apply wrote, so only the file moved")
	f.mustApply(v2, applyWithPlanID(pl.PlanID))
	after := f.rules(web.ID)
	require.Equal(t, []string{"Allow the office", "Block WordPress and PHP probes"}, applyRuleNames(after))
	require.Equal(t, rules[0].ID, after[0].ID)
	require.Equal(t, rules[1].ID, after[1].ID)
	require.Equal(t, "Upgrading the database. Back by noon.", f.row(web.ID).MaintenanceMessage)
	require.Equal(t, configsync.StatusNoop, f.mustApply(v2).Status)

	// Rules removed in the dashboard are drift: the file puts the list back.
	_, err := f.pool.Exec(context.Background(), `UPDATE services SET firewall = '{"rules":[]}'::jsonb WHERE id = $1`, web.ID)
	require.NoError(t, err)
	require.Empty(t, f.rules(web.ID))
	pl = f.plan("staging", v2)
	require.Equal(t, []string{"firewall"}, applyChangeFields(applyPlanService(t, pl, "web")))
	f.mustApply(v2, applyWithPlanID(pl.PlanID))
	require.Equal(t, []string{"Allow the office", "Block WordPress and PHP probes"}, applyRuleNames(f.rules(web.ID)))
}

func TestApplyFirewallAndMaintenanceOfAnExistingService(t *testing.T) {
	f := newApplyFixture(t)
	f.firewallServer(false)
	web := f.uiService(f.staging, "web", services.SourceImage, services.Config{Image: "nginx:1.27", Port: 3000})

	res := f.mustApply(applyWebFile(3000, applyFirewallTables+applyMaintenanceTable))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "firewall:web"), applyJSON(res.Steps))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "maintenance:web"), applyJSON(res.Steps))
	require.Len(t, f.rules(web.ID), 2)
	require.True(t, f.row(web.ID).MaintenanceEnabled)
}

func TestApplyEmptyFirewallTableRemovesTheRulesAndOffKeepsTheText(t *testing.T) {
	f := newApplyFixture(t)
	f.firewallServer(false)
	f.mustApply(applyWebFile(3000, applyFirewallTables+applyMaintenanceTable))
	web := f.svcRow(f.staging, "web")
	require.Len(t, f.rules(web.ID), 2)

	v2 := applyWebFile(3000, "\n[services.web.firewall]\n\n[services.web.maintenance]\nenabled = false\n")
	pl := f.plan("staging", v2)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.Equal(t, []string{"firewall", "maintenance"}, applyChangeFields(applyPlanService(t, pl, "web")))
	f.mustApply(v2, applyWithPlanID(pl.PlanID))

	require.Empty(t, f.rules(web.ID), "an empty table owns the firewall with no rules")
	row := f.row(web.ID)
	require.False(t, row.MaintenanceEnabled)
	require.Equal(t, "Back soon", row.MaintenanceTitle, "switching off keeps the stored text for next time")
	require.Contains(t, f.owned(web.ID), "firewall")
	require.Contains(t, f.owned(web.ID), "maintenance")
	require.Equal(t, configsync.StatusNoop, f.mustApply(v2).Status, "off is off, whatever text is stored")
}

func TestApplyRemovingTheTablesReleasesThemAndPruneRemovesThem(t *testing.T) {
	f := newApplyFixture(t)
	f.firewallServer(false)
	f.mustApply(applyWebFile(3000, applyFirewallTables+applyMaintenanceTable))
	web := f.svcRow(f.staging, "web")
	gone := applyWebFile(3000, "")

	// Without prune the file only lets go: both stay live, and the plan says so.
	pl := f.plan("staging", gone)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.Empty(t, applyRemovedFields(pl))
	require.ElementsMatch(t, []string{"services.web.firewall", "services.web.maintenance"}, applyPlanService(t, pl, "web").Released)

	// With prune the firewall is emptied and maintenance is switched off, at once.
	pl = f.plan("staging", gone, applyWithPrune())
	require.Equal(t, map[string][]string{"web": {"firewall", "maintenance"}}, applyRemovedFields(pl))
	res := f.mustApply(gone, applyWithPrune(), applyWithPlanID(pl.PlanID))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "firewall:web:reset"), applyJSON(res.Steps))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "maintenance:web:off"), applyJSON(res.Steps))
	require.Empty(t, f.rules(web.ID))
	require.False(t, f.row(web.ID).MaintenanceEnabled)
	require.NotContains(t, f.owned(web.ID), "firewall")
	require.NotContains(t, f.owned(web.ID), "maintenance")

	// Ownership is gone, so a prune no longer touches anything.
	require.Empty(t, applyRemovedFields(f.plan("staging", gone, applyWithPrune())))
}

func TestApplyReleasedTablesStayLive(t *testing.T) {
	f := newApplyFixture(t)
	f.firewallServer(false)
	f.mustApply(applyWebFile(3000, applyFirewallTables+applyMaintenanceTable))
	web := f.svcRow(f.staging, "web")

	f.mustApply(applyWebFile(3000, ""))
	require.Len(t, f.rules(web.ID), 2)
	require.True(t, f.row(web.ID).MaintenanceEnabled)
	require.NotContains(t, f.owned(web.ID), "firewall")
	require.NotContains(t, f.owned(web.ID), "maintenance")
}

func TestApplyFirewallAndMaintenanceNeedTheirScopes(t *testing.T) {
	f := newApplyFixture(t)
	f.firewallServer(false)
	file := applyWebFile(3000, applyFirewallTables+applyMaintenanceTable)
	pl := f.plan("staging", file)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.ElementsMatch(t, []string{"projects:write", "deployments:write", "proxy:write"}, pl.RequiredScopes)
	before := f.state()

	_, err := f.apply(f.token(policy.ScopeProjectsWrite), "staging", file)
	ae := applyRequireError(t, err, 403, configsync.CodeInsufficientScope)
	require.Equal(t, []string{"deployments:write", "proxy:write"}, ae.Details["scopes"])
	require.Equal(t, before, f.state())

	_, err = f.apply(f.token(policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite), "staging", file)
	ae = applyRequireError(t, err, 403, configsync.CodeInsufficientScope)
	require.Equal(t, []string{"proxy:write"}, ae.Details["scopes"], "the firewall needs proxy:write")
	require.Equal(t, before, f.state())

	_, err = f.apply(f.token(policy.ScopeProjectsWrite, policy.ScopeProxyWrite), "staging", file)
	ae = applyRequireError(t, err, 403, configsync.CodeInsufficientScope)
	require.Equal(t, []string{"deployments:write"}, ae.Details["scopes"], "maintenance needs deployments:write")

	res, err := f.apply(f.token(policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite, policy.ScopeProxyWrite), "staging", file)
	require.NoError(t, err)
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "firewall:web"), applyJSON(res.Steps))
}

func TestApplyFirewallThatTheServerCannotRunBlocksThePlanAndWritesNothing(t *testing.T) {
	f := newApplyFixture(t)
	file := applyWebFile(3000, applyFirewallTables)

	// The fixture's agent predates firewalls.
	pl := f.plan("staging", file)
	require.False(t, pl.Valid)
	require.Contains(t, applyJSON(pl.Diagnostics), "agent_too_old")
	before := f.state()
	_, err := f.apply(f.admin, "staging", file)
	require.Error(t, err)
	require.Equal(t, before, f.state(), "nothing is created")
	require.False(t, f.hasSvc(f.staging, "web"))

	// Country rules need geolocation on the server.
	f.firewallServer(false)
	country := applyWebFile(3000, "\n[[services.web.firewall.rules]]\nname = \"Block countries\"\naction = \"block\"\n\n[[services.web.firewall.rules.conditions]]\nfield = \"country\"\nop = \"in\"\nvalues = [\"CN\"]\n")
	pl = f.plan("staging", country)
	require.False(t, pl.Valid)
	require.Contains(t, applyJSON(pl.Diagnostics), "geo_required")

	f.firewallServer(true)
	pl = f.plan("staging", country)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	f.mustApply(country)
	rules := f.rules(f.svcRow(f.staging, "web").ID)
	require.Equal(t, []string{"Block countries"}, applyRuleNames(rules))
	require.Equal(t, []string{"CN"}, rules[0].Conditions[0].Values)
}
