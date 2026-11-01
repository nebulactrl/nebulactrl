package patching

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var (
	opensslSec = agentprotocol.PatchPackage{Name: "openssl", From: "1", To: "2", Security: true}
	vim        = agentprotocol.PatchPackage{Name: "vim", From: "1", To: "2"}
	dockerPkg  = agentprotocol.PatchPackage{Name: "docker-ce", From: "26", To: "27"}
	kernelPkg  = agentprotocol.PatchPackage{Name: "linux-image-6.1.0-21-amd64", To: "6.1.0-22", Security: true}
)

func TestSupportsPatching(t *testing.T) {
	require.True(t, SupportsPatching(nil), "no facts yet: assume a current agent")
	old, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapHostHardening}})
	require.False(t, SupportsPatching(old))
	cur, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapHostPatching}})
	require.True(t, SupportsPatching(cur))
	require.True(t, Handles(agentprotocol.KindHostPatching))
	require.False(t, Handles(agentprotocol.KindHostHardening))
}

func TestCheckStoresStateAndSummary(t *testing.T) {
	f := newFixture(t)
	require.Zero(t, f.disp.count())
	run := f.check(state(3, 1, false, "boot-1", opensslSec, vim))
	require.Equal(t, StatusSucceeded, run.Status)
	require.NotNil(t, run.UpdatesAfter)
	require.EqualValues(t, 3, *run.UpdatesAfter)
	p := f.disp.last(t)
	require.Equal(t, agentprotocol.PatchingActionCheck, p.Action)
	require.Equal(t, agentprotocol.PatchingTriggerManual, p.Trigger)

	v, err := f.svc.Get(f.viewer, f.server.ID)
	require.NoError(t, err)
	require.True(t, v.Known)
	require.True(t, v.Supported)
	require.Equal(t, 3, v.Updates)
	require.Equal(t, 1, v.Security)
	require.False(t, v.RebootRequired)
	require.Equal(t, "apt", v.Manager)
	require.Len(t, v.Packages, 2)
	require.Empty(t, v.Busy)
	require.Len(t, v.Runs, 1)
	require.False(t, v.Stale)

	sums, err := f.q.ListServerPatchSummaries(bg, []uuid.UUID{f.server.ID})
	require.NoError(t, err)
	require.Len(t, sums, 1)
	require.EqualValues(t, 3, sums[0].Updates)
	require.EqualValues(t, 1, sums[0].SecurityUpdates)
	require.Empty(t, sums[0].Busy)
	require.NotContains(t, f.auditActions(), ActionRun, "checks are not audited when they finish")
}

func TestCheckPermissionsAndStates(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Check(f.member, f.server.ID)
	require.Equal(t, "forbidden", apiCode(t, err), "a developer cannot start a check")
	_, err = f.svc.Check(f.viewer, f.server.ID)
	require.Error(t, err)
	_, err = f.svc.Get(f.viewer, uuid.New())
	require.Equal(t, "not_found", apiCode(t, err))

	// Another workspace's server is a 404, not a 403.
	ws2, err := f.q.CreateWorkspace(bg, "w2")
	require.NoError(t, err)
	u, err := f.q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws2.ID, Email: "z@example.com", Name: "Z", PasswordHash: "x"})
	require.NoError(t, err)
	_, err = f.svc.Get(principal(ws2.ID, u.ID, auth.RoleOwner, policy.AllScopes), f.server.ID)
	require.Equal(t, "not_found", apiCode(t, err))

	r, err := f.svc.Check(f.owner, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPending, r.Status)
	_, err = f.svc.Check(f.owner, f.server.ID)
	require.Equal(t, "patching_running", apiCode(t, err), "one run at a time")
	v, err := f.svc.Get(f.owner, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, "check", v.Busy)

	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	f.pool.Exec(bg, "UPDATE server_patch_runs SET status = 'failed'")
	_, err = f.svc.Check(f.owner, f.server.ID)
	require.Equal(t, "server_offline", apiCode(t, err))
}

func TestOldAgentIsRefused(t *testing.T) {
	f := newFixture(t)
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapHostHardening}})
	_, err := f.pool.Exec(bg, "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, facts)
	require.NoError(t, err)
	_, err = f.svc.Check(f.owner, f.server.ID)
	require.Equal(t, "agent_too_old", apiCode(t, err))
}

func TestSweepChecksOncePerInterval(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 1, f.disp.count())
	require.Equal(t, agentprotocol.PatchingTriggerScheduled, f.disp.last(t).Trigger)
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 1, f.disp.count(), "a run in flight is not duplicated")

	// Answered and recent: still nothing for six hours, even when the check failed.
	_, err := f.pool.Exec(bg, "UPDATE server_patch_runs SET status = 'failed', created_at = now() - interval '5 hours'")
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 1, f.disp.count(), "checked less than 6 hours ago")

	_, err = f.pool.Exec(bg, "UPDATE server_patch_runs SET created_at = now() - interval '7 hours'")
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 2, f.disp.count(), "due again after 6 hours")

	// An offline server is skipped.
	_, err = f.pool.Exec(bg, "UPDATE server_patch_runs SET status = 'failed', created_at = now() - interval '7 hours'; UPDATE servers SET status = 'offline'")
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 2, f.disp.count())
}

func TestRebootRequiredNotifiesOncePerStreak(t *testing.T) {
	f := newFixture(t)
	f.check(state(0, 0, false, "b1"))
	require.Empty(t, f.emit.rebootRequired())

	f.check(state(0, 0, true, "b1"))
	evs := f.emit.rebootRequired()
	require.Len(t, evs, 1)
	require.Equal(t, notifications.SeverityWarning, evs[0].Severity)
	require.Contains(t, evs[0].Title, "Agent 1")
	require.NotNil(t, evs[0].ServerID)

	f.check(state(0, 0, true, "b1"))
	require.Len(t, f.emit.rebootRequired(), 1, "still required: not announced again")

	f.check(state(0, 0, false, "b2"))
	f.check(state(0, 0, true, "b2"))
	require.Len(t, f.emit.rebootRequired(), 2, "a new streak is announced")

	sums, err := f.q.ListServerPatchSummaries(bg, []uuid.UUID{f.server.ID})
	require.NoError(t, err)
	require.True(t, sums[0].RebootRequired)
}

func TestFailedAndUndeliveredChecks(t *testing.T) {
	f := newFixture(t)
	r, err := f.svc.Check(f.owner, f.server.ID)
	require.NoError(t, err)
	cid := *f.run(r.ID).CommandID
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, cid, agentprotocol.ResultFrame{
		CommandID: cid.String(), Outcome: agentprotocol.OutcomeFailed, Error: "docker unreachable",
	}))
	run := f.run(r.ID)
	require.Equal(t, StatusFailed, run.Status)
	require.Equal(t, "docker unreachable", run.Error)

	r2, err := f.svc.Check(f.owner, f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.HandleUndelivered(bg, *f.run(r2.ID).CommandID, "Not delivered: server offline. No changes were made."))
	require.Equal(t, StatusFailed, f.run(r2.ID).Status)
	require.Contains(t, f.run(r2.ID).Error, "Not delivered")

	// A result for a command of another domain is ignored.
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, uuid.New(), agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded}))
}

func TestApplyNeedsInstanceAdminConfirmationAndACheck(t *testing.T) {
	f := newFixture(t)
	in := ApplyInput{Confirm: "Agent 1"}

	_, err := f.svc.Apply(f.other, f.server.ID, in)
	require.Equal(t, "forbidden", apiCode(t, err), "an Owner who is not the instance admin cannot upgrade the host")
	_, err = f.svc.Apply(f.owner, f.server.ID, ApplyInput{Confirm: "agent 1"})
	require.Equal(t, "confirmation_mismatch", apiCode(t, err))
	_, err = f.svc.Apply(f.owner, f.server.ID, in)
	require.Equal(t, "no_check_yet", apiCode(t, err), "no plan without a check")

	f.check(state(0, 0, false, "b1"))
	_, err = f.svc.Apply(f.owner, f.server.ID, in)
	require.Equal(t, "no_updates", apiCode(t, err))

	f.check(state(1, 0, false, "b1", vim))
	_, err = f.svc.Apply(f.owner, f.server.ID, ApplyInput{Confirm: "Agent 1", SecurityOnly: true})
	require.Equal(t, "no_updates", apiCode(t, err), "nothing in the security set")
	require.Empty(t, f.auditActions(), "refused requests leave no audit row and no command")
	before := f.disp.count()

	r, err := f.svc.Apply(f.owner, f.server.ID, in)
	require.NoError(t, err)
	require.Equal(t, StatusPending, r.Status)
	require.Equal(t, before+1, f.disp.count())
	p := f.disp.last(t)
	require.Equal(t, agentprotocol.PatchingActionApply, p.Action)
	require.Equal(t, r.ID.String(), p.RunID)
	require.False(t, p.SecurityOnly)
	require.Equal(t, []string{ActionApply}, f.auditActions())
}

func TestApplyFlowSuccessAndFailure(t *testing.T) {
	f := newFixture(t)
	f.check(state(2, 1, false, "b1", opensslSec, vim))
	r, err := f.svc.Apply(f.owner, f.server.ID, ApplyInput{Confirm: "Agent 1", SecurityOnly: true})
	require.NoError(t, err)
	require.True(t, f.disp.last(t).SecurityOnly)
	cid := *f.run(r.ID).CommandID

	f.answer(cid, agentprotocol.PatchingResult{
		Action: agentprotocol.PatchingActionApply, State: state(1, 0, true, "b1", vim), Log: "Setting up openssl\nDone",
	})
	run, err := f.svc.GetRun(f.viewer, f.server.ID, r.ID)
	require.NoError(t, err)
	require.Equal(t, StatusSucceeded, run.Status)
	require.Equal(t, "Setting up openssl\nDone", run.Log, "the log is kept and readable")
	require.NotNil(t, run.UpdatesBefore)
	require.Equal(t, 2, *run.UpdatesBefore)
	require.NotNil(t, run.UpdatesAfter)
	require.Equal(t, 1, *run.UpdatesAfter)

	v, err := f.svc.Get(f.owner, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, 1, v.Updates, "the state is the one after the upgrade")
	require.True(t, v.RebootRequired)
	require.Len(t, f.emit.rebootRequired(), 1, "the upgrade started needing a reboot")
	require.Equal(t, []string{ActionApply, ActionRun}, f.auditActions())

	// The list view leaves logs out.
	for _, rr := range v.Runs {
		require.Empty(t, rr.Log)
	}

	// A duplicate result changes nothing.
	f.answer(cid, agentprotocol.PatchingResult{Action: agentprotocol.PatchingActionApply, State: state(9, 9, false, "b1"), Log: "other"})
	again, err := f.svc.GetRun(f.owner, f.server.ID, r.ID)
	require.NoError(t, err)
	require.Equal(t, "Setting up openssl\nDone", again.Log)
	require.Equal(t, []string{ActionApply, ActionRun}, f.auditActions())

	// A failing package manager fails the run but keeps the log and the new state.
	r2, err := f.svc.Apply(f.owner, f.server.ID, ApplyInput{Confirm: "Agent 1"})
	require.NoError(t, err)
	f.answer(*f.run(r2.ID).CommandID, agentprotocol.PatchingResult{
		Action: agentprotocol.PatchingActionApply, State: state(1, 0, true, "b1", vim), ExitCode: 100, Log: "E: dpkg error", Message: "The package manager exited with status 100.",
	})
	failed := f.run(r2.ID)
	require.Equal(t, StatusFailed, failed.Status)
	require.Equal(t, "E: dpkg error", failed.Log)
	require.Contains(t, failed.Error, "status 100")
	require.NotNil(t, failed.ExitCode)
	require.EqualValues(t, 100, *failed.ExitCode)
}

func TestApplyOnProductionServerNeedsAcknowledgement(t *testing.T) {
	f := newFixture(t)
	f.addProductionService("api")
	f.check(state(1, 1, false, "b1", opensslSec))

	_, err := f.svc.Apply(f.owner, f.server.ID, ApplyInput{Confirm: "Agent 1"})
	require.Equal(t, "production_ack_required", apiCode(t, err))
	_, err = f.svc.Reboot(f.owner, f.server.ID, RebootInput{Confirm: "Agent 1"})
	require.Equal(t, "production_ack_required", apiCode(t, err))
	require.Empty(t, f.auditActions())

	r, err := f.svc.Apply(f.owner, f.server.ID, ApplyInput{Confirm: "Agent 1", AcknowledgeProduction: true})
	require.NoError(t, err)
	var meta string
	require.NoError(t, f.pool.QueryRow(bg, "SELECT metadata::text FROM audit_events WHERE action = $1", ActionApply).Scan(&meta))
	require.Contains(t, meta, "Shop/production/api", "the audit row names the production services affected")
	require.Equal(t, StatusPending, r.Status)

	plan, err := f.svc.Plan(f.viewer, f.server.ID, false)
	require.NoError(t, err)
	require.True(t, plan.RequiresAcknowledgement)
	require.Len(t, plan.ProductionServices, 1)
	require.Equal(t, "api", plan.ProductionServices[0].Name)
	require.Equal(t, 1, plan.ServicesOnServer)
}

func TestProductionGuardFailsClosedForGuardedPrincipals(t *testing.T) {
	f := newFixture(t)
	f.addProductionService("api")
	f.check(state(1, 1, false, "b1", opensslSec))
	var first uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg, "SELECT id FROM users ORDER BY created_at, id LIMIT 1").Scan(&first))
	mcp := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeMCP, UserID: first, WorkspaceID: f.ws, Role: auth.RoleOwner, Label: "agent", Scopes: policy.AllScopes})
	_, err := f.svc.Apply(mcp, f.server.ID, ApplyInput{Confirm: "Agent 1", AcknowledgeProduction: true})
	require.Equal(t, "approval_unavailable", apiCode(t, err), "an MCP client needs the production approval; without a guard the call is refused")
	require.Empty(t, f.auditActions())
}

func TestControlPlaneServerNeedsAcknowledgement(t *testing.T) {
	f := newFixture(t)
	_, err := f.pool.Exec(bg, "UPDATE servers SET is_control_plane = true WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.svc.Reboot(f.owner, f.server.ID, RebootInput{Confirm: "Agent 1"})
	require.Equal(t, "production_ack_required", apiCode(t, err))
	plan, err := f.svc.Plan(f.owner, f.server.ID, false)
	require.NoError(t, err)
	require.True(t, plan.ControlPlane)
	require.True(t, plan.RequiresAcknowledgement)
}

func TestPlanRisksAndSecurityOnly(t *testing.T) {
	f := newFixture(t)
	plan, err := f.svc.Plan(f.owner, f.server.ID, false)
	require.NoError(t, err)
	require.False(t, plan.Ready)
	require.Contains(t, plan.NotReadyReason, "No check")
	require.Equal(t, "Agent 1", plan.Confirm)

	f.check(state(4, 2, false, "b1", opensslSec, kernelPkg, dockerPkg, vim))
	plan, err = f.svc.Plan(f.owner, f.server.ID, false)
	require.NoError(t, err)
	require.True(t, plan.Ready)
	require.Equal(t, 4, plan.Updates)
	require.Len(t, plan.Packages, 4)
	require.True(t, plan.RestartsDocker)
	require.True(t, plan.RebootLikely)
	risks := map[string]bool{}
	for _, r := range plan.Risks {
		risks[r.Package] = r.Reboot
	}
	require.Equal(t, map[string]bool{"linux-image-6.1.0-21-amd64": true, "docker-ce": false}, risks)
	require.False(t, plan.RequiresAcknowledgement)

	sec, err := f.svc.Plan(f.owner, f.server.ID, true)
	require.NoError(t, err)
	require.Equal(t, 2, sec.Updates)
	require.Len(t, sec.Packages, 2)
	require.False(t, sec.RestartsDocker, "docker is not a security update here")

	// Packages carry their warning in the view too.
	v, err := f.svc.Get(f.owner, f.server.ID)
	require.NoError(t, err)
	var warned int
	for _, p := range v.Packages {
		if p.Risk != "" {
			warned++
		}
	}
	require.Equal(t, 2, warned)
}

func (f *fixture) reboot() db.ServerPatchRun {
	f.t.Helper()
	r, err := f.svc.Reboot(f.owner, f.server.ID, RebootInput{Confirm: "Agent 1"})
	require.NoError(f.t, err)
	require.Equal(f.t, agentprotocol.PatchingActionReboot, f.disp.last(f.t).Action)
	f.answer(*f.run(r.ID).CommandID, agentprotocol.PatchingResult{
		Action: agentprotocol.PatchingActionReboot, State: agentprotocol.PatchState{BootID: "boot-1"}, Message: "Reboot scheduled (systemd).",
	})
	return f.run(r.ID)
}

func TestRebootWaitsForTheAgentOnANewBoot(t *testing.T) {
	f := newFixture(t)
	f.check(state(0, 0, true, "boot-1"))
	require.Len(t, f.emit.rebootRequired(), 1)

	_, err := f.svc.Reboot(f.owner, f.server.ID, RebootInput{Confirm: "nope"})
	require.Equal(t, "confirmation_mismatch", apiCode(t, err))
	_, err = f.svc.Reboot(f.other, f.server.ID, RebootInput{Confirm: "Agent 1"})
	require.Equal(t, "forbidden", apiCode(t, err))

	run := f.reboot()
	require.Equal(t, StatusRebooting, run.Status, "scheduled, not done")
	require.Equal(t, "boot-1", run.BootIDBefore)
	require.NotNil(t, run.RebootingSince)
	require.Contains(t, f.auditActions(), ActionReboot)
	v, err := f.svc.Get(f.owner, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, "reboot", v.Busy)
	_, err = f.svc.Check(f.owner, f.server.ID)
	require.Equal(t, "patching_running", apiCode(t, err), "nothing else runs while the server reboots")

	// Right after scheduling the agent has not had time to go down: no probe yet.
	n := f.disp.count()
	require.NoError(t, f.svc.WatchReboots(bg))
	require.Equal(t, n, f.disp.count())

	// Later, with the server online, the watcher asks for a check to read the boot id.
	_, err = f.pool.Exec(bg, "UPDATE server_patch_runs SET rebooting_since = now() - interval '1 minute' WHERE id = $1", run.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.WatchReboots(bg))
	require.Equal(t, n+1, f.disp.count())
	probe := f.disp.last(t)
	require.Equal(t, agentprotocol.PatchingActionCheck, probe.Action)
	require.Equal(t, run.ID.String(), probe.RunID)
	require.NoError(t, f.svc.WatchReboots(bg))
	require.Equal(t, n+1, f.disp.count(), "one probe at a time")

	// The probe answered by the old boot (the host had not gone down yet): still waiting.
	verify := *f.run(run.ID).VerifyCommandID
	f.answer(verify, agentprotocol.PatchingResult{Action: agentprotocol.PatchingActionCheck, State: state(0, 0, true, "boot-1")})
	require.Equal(t, StatusRebooting, f.run(run.ID).Status)

	// The server really went away and came back: a new boot id finishes the run.
	_, err = f.pool.Exec(bg, "UPDATE server_patch_runs SET verify_sent_at = now() - interval '1 minute' WHERE id = $1", run.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.WatchReboots(bg))
	require.Equal(t, n+2, f.disp.count())
	verify = *f.run(run.ID).VerifyCommandID
	f.answer(verify, agentprotocol.PatchingResult{Action: agentprotocol.PatchingActionCheck, State: state(0, 0, false, "boot-2")})
	done := f.run(run.ID)
	require.Equal(t, StatusSucceeded, done.Status)
	require.Contains(t, done.Message, "rebooted")
	require.NotNil(t, done.FinishedAt)

	got, err := f.svc.Get(f.owner, f.server.ID)
	require.NoError(t, err)
	require.False(t, got.RebootRequired, "the flag is gone after the reboot")
	require.Empty(t, got.Busy)
	require.Equal(t, []string{ActionReboot, ActionRun}, f.auditActions())
}

func TestRebootFailsAfterTheDeadlineOrWhenRevoked(t *testing.T) {
	f := newFixture(t)
	run := f.reboot()
	_, err := f.pool.Exec(bg, "UPDATE server_patch_runs SET rebooting_since = now() - interval '21 minutes' WHERE id = $1", run.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.WatchReboots(bg))
	late := f.run(run.ID)
	require.Equal(t, StatusFailed, late.Status)
	require.Contains(t, late.Error, "did not come back")

	run2 := f.reboot()
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'revoked' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.WatchReboots(bg))
	require.Equal(t, StatusFailed, f.run(run2.ID).Status)
}

func TestRebootNotDeliveredFails(t *testing.T) {
	f := newFixture(t)
	r, err := f.svc.Reboot(f.owner, f.server.ID, RebootInput{Confirm: "Agent 1"})
	require.NoError(t, err)
	require.NoError(t, f.svc.HandleUndelivered(bg, *f.run(r.ID).CommandID, "Not delivered: server offline. No changes were made."))
	require.Equal(t, StatusFailed, f.run(r.ID).Status)
}

func TestLostVerificationIsRetriedNotFatal(t *testing.T) {
	f := newFixture(t)
	run := f.reboot()
	_, err := f.pool.Exec(bg, "UPDATE server_patch_runs SET rebooting_since = now() - interval '1 minute' WHERE id = $1", run.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.WatchReboots(bg))
	verify := *f.run(run.ID).VerifyCommandID
	require.NoError(t, f.svc.HandleUndelivered(bg, verify, "expired"))
	require.Equal(t, StatusRebooting, f.run(run.ID).Status)
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, verify, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "docker restarting"}))
	require.Equal(t, StatusRebooting, f.run(run.ID).Status)
}

func TestRunsArePruned(t *testing.T) {
	f := newFixture(t)
	for range HistoryKept + 3 {
		f.check(state(0, 0, false, "b"))
	}
	runs, err := f.q.ListServerPatchRuns(bg, db.ListServerPatchRunsParams{ServerID: f.server.ID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, runs, HistoryKept)
}
