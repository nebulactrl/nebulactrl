package configsync_test

// Apply of [services.<s>.autoban]: an immediate step through firewall.Service.PutAutoBan, the table
// is owned as one unit (a key left out is its default), checked against the server at plan time, and
// a prune switches auto-ban off. These need the database like the other apply tests.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/configsync"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const applyAutoBanTable = `
[services.web.autoban]
enabled = true
threshold = 5
window_seconds = 120
allow = ["203.0.113.7", "198.51.100.0/24", "203.0.113.7/32"]
`

// autobanServer lets the fixture's agent run auto-ban (its facts list the capability); analytics
// turns on the traffic analytics it needs.
func (f *applyFixture) autobanServer(analytics bool) {
	f.t.Helper()
	facts, err := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapProxyV2, agentprotocol.CapFirewall, agentprotocol.CapAutoBan}})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(context.Background(),
		"UPDATE servers SET facts = $2, traffic_analytics_enabled = $3 WHERE id = $1", f.server.ID, facts, analytics)
	require.NoError(f.t, err)
}

// policy is the auto-ban policy stored on a service (the default, off, without one).
func (f *applyFixture) autobanPolicy(id uuid.UUID) agentprotocol.AutoBan {
	f.t.Helper()
	return domains.StoredAutoBan(f.row(id).Autoban)
}

func TestApplyAutoBan(t *testing.T) {
	f := newApplyFixture(t)
	f.autobanServer(true)
	file := applyWebFile(3000, applyAutoBanTable)

	pl := f.plan("staging", file)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.Contains(t, applyChangeFields(applyPlanService(t, pl, "web")), "autoban", "the service is new: its fields come with the policy")
	require.ElementsMatch(t, []string{"projects:write", "proxy:write"}, pl.RequiredScopes)
	res := f.mustApply(file, applyWithPlanID(pl.PlanID))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "autoban:web"), applyJSON(res.Steps))

	web := f.svcRow(f.staging, "web")
	require.Equal(t, agentprotocol.AutoBan{
		Enabled: true, Threshold: 5, WindowSeconds: 120, DurationSeconds: 3600, Allow: []string{"203.0.113.7/32", "198.51.100.0/24"},
	}, f.autobanPolicy(web.ID), "a key the file leaves out is its default; the allow list is canonical")
	require.Equal(t, applyFP(projectconfig.AutoBanValueOf(f.autobanPolicy(web.ID))), f.owned(web.ID)["autoban"])

	// The same file is what is live.
	require.Equal(t, configsync.StatusNoop, f.mustApply(file).Status)

	// An edit of one key changes the unit.
	v2 := strings.Replace(file, "threshold = 5", "threshold = 8", 1)
	pl = f.plan("staging", v2)
	require.Equal(t, []string{"autoban"}, applyChangeFields(applyPlanService(t, pl, "web")))
	f.mustApply(v2, applyWithPlanID(pl.PlanID))
	require.Equal(t, 8, f.autobanPolicy(web.ID).Threshold)

	// Another person switches it off in the dashboard: drift, and the file would switch it back on.
	_, err := f.pool.Exec(context.Background(), `UPDATE services SET autoban = '{"enabled":false,"threshold":8,"windowSeconds":120,"durationSeconds":3600}'::jsonb WHERE id = $1`, web.ID)
	require.NoError(t, err)
	pl = f.plan("staging", v2)
	ps := applyPlanService(t, pl, "web")
	require.Equal(t, []string{"autoban"}, applyChangeFields(ps))
	require.Equal(t, configsync.StateDrift, ps.State)
	f.mustApply(v2, applyWithPlanID(pl.PlanID))
	require.True(t, f.autobanPolicy(web.ID).Enabled)
}

func TestApplyAutoBanOfAnExistingServiceAndOffTable(t *testing.T) {
	f := newApplyFixture(t)
	f.autobanServer(false) // analytics off: only an enabled policy needs it
	web := f.uiService(f.staging, "web", services.SourceImage, services.Config{Image: "nginx:1.27", Port: 3000})

	// An off table with the defaults is what a service without a policy has: nothing to do.
	off := applyWebFile(3000, "\n[services.web.autoban]\nenabled = false\n")
	pl := f.plan("staging", off)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.Empty(t, applyChangeFields(applyPlanService(t, pl, "web")))

	// An off table with its own limits is a change, and analytics is not needed to store it.
	tuned := applyWebFile(3000, "\n[services.web.autoban]\nenabled = false\nthreshold = 4\n")
	pl = f.plan("staging", tuned)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	f.mustApply(tuned, applyWithPlanID(pl.PlanID))
	require.Equal(t, agentprotocol.AutoBan{Threshold: 4, WindowSeconds: 600, DurationSeconds: 3600, Allow: []string{}}, f.autobanPolicy(web.ID))
}

func TestApplyRemovingTheTableReleasesItAndPruneSwitchesAutoBanOff(t *testing.T) {
	f := newApplyFixture(t)
	f.autobanServer(true)
	f.mustApply(applyWebFile(3000, applyAutoBanTable))
	web := f.svcRow(f.staging, "web")
	gone := applyWebFile(3000, "")

	pl := f.plan("staging", gone)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.Empty(t, applyRemovedFields(pl))
	require.Equal(t, []string{"services.web.autoban"}, applyPlanService(t, pl, "web").Released)
	f.mustApply(gone)
	require.True(t, f.autobanPolicy(web.ID).Enabled, "released: the policy stays live")
	require.NotContains(t, f.owned(web.ID), "autoban")

	// Own it again, then prune.
	f.mustApply(applyWebFile(3000, applyAutoBanTable))
	pl = f.plan("staging", gone, applyWithPrune())
	require.Equal(t, map[string][]string{"web": {"autoban"}}, applyRemovedFields(pl))
	res := f.mustApply(gone, applyWithPrune(), applyWithPlanID(pl.PlanID))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "autoban:web:reset"), applyJSON(res.Steps))
	require.Equal(t, agentprotocol.AutoBan{Threshold: 10, WindowSeconds: 600, DurationSeconds: 3600, Allow: []string{}}, f.autobanPolicy(web.ID))
	require.Nil(t, f.row(web.ID).Autoban, "back to no policy")
	require.NotContains(t, f.owned(web.ID), "autoban")
	require.Empty(t, applyRemovedFields(f.plan("staging", gone, applyWithPrune())))
}

func TestApplyAutoBanNeedsProxyWrite(t *testing.T) {
	f := newApplyFixture(t)
	f.autobanServer(true)
	file := applyWebFile(3000, applyAutoBanTable)
	before := f.state()

	_, err := f.apply(f.token(policy.ScopeProjectsWrite), "staging", file)
	ae := applyRequireError(t, err, 403, configsync.CodeInsufficientScope)
	require.Equal(t, []string{"proxy:write"}, ae.Details["scopes"])
	require.Equal(t, before, f.state())

	res, err := f.apply(f.token(policy.ScopeProjectsWrite, policy.ScopeProxyWrite), "staging", file)
	require.NoError(t, err)
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "autoban:web"), applyJSON(res.Steps))
}

func TestApplyAutoBanThatTheServerCannotRunBlocksThePlanAndWritesNothing(t *testing.T) {
	f := newApplyFixture(t)
	file := applyWebFile(3000, applyAutoBanTable)

	// The fixture's agent predates auto-ban.
	pl := f.plan("staging", file)
	require.False(t, pl.Valid)
	require.Contains(t, applyJSON(pl.Diagnostics), "agent_too_old")
	before := f.state()
	_, err := f.apply(f.admin, "staging", file)
	require.Error(t, err)
	require.Equal(t, before, f.state(), "nothing is created")

	// A current agent with traffic analytics off.
	f.autobanServer(false)
	pl = f.plan("staging", file)
	require.False(t, pl.Valid)
	require.Contains(t, applyJSON(pl.Diagnostics), "analytics_required")
	require.Contains(t, applyJSON(pl.Diagnostics), "services.web.autoban")

	f.autobanServer(true)
	pl = f.plan("staging", file)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	f.mustApply(file)
	require.True(t, f.autobanPolicy(f.svcRow(f.staging, "web").ID).Enabled)
}
