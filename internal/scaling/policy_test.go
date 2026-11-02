package scaling_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/internal/scaling"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// --- production approval ---------------------------------------------------------

func (f *fixture) principal() *auth.Principal {
	p, ok := auth.PrincipalFrom(f.ctx)
	require.True(f.t, ok)
	return p
}

// restToken is a REST token as authentication builds it: guarded unless exempt.
func (f *fixture) restToken() context.Context {
	tok := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: f.principal().UserID, TokenID: &tok, WorkspaceID: f.ws.ID, Role: auth.RoleOwner,
		Label: "ci", Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
}

func (f *fixture) installGuard() *prodguard.Guard {
	f.t.Helper()
	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: f.box})
	g.Install()
	f.t.Cleanup(prodguardtest.Uninstall)
	return g
}

func (f *fixture) approve(g *prodguard.Guard, err error) {
	f.t.Helper()
	ar, ok := prodguard.AsRequired(err)
	require.True(f.t, ok, "want approval_required, got %v", err)
	person := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: f.principal().UserID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})
	_, aerr := g.Approve(person, ar.ApprovalID)
	require.NoError(f.t, aerr)
}

func TestScalingAProductionServiceNeedsApprovalForRESTTokens(t *testing.T) {
	f := newFixture(t) // the fixture environment is production
	g := f.installGuard()
	sv := f.web("web")
	f.deploy(sv)
	rest := f.restToken()

	_, err := f.scal.Set(rest, sv.ID, scaling.SetInput{Replicas: new(3)})
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Summary, "Change the scaling of web in production")
	require.Len(t, ar.Plan.Effects, 1)
	require.Contains(t, ar.Plan.Effects[0], "from 1 to 3")
	require.EqualValues(t, 1, f.reload(sv.ID).Replicas, "nothing changes before the approval")
	require.Empty(t, f.scaleCommands())

	f.approve(g, err)
	v, err := f.scal.Set(rest, sv.ID, scaling.SetInput{Replicas: new(3)})
	require.NoError(t, err)
	require.Equal(t, 3, v.Scaling.Replicas)
	require.Len(t, f.scaleCommands(), 1)

	// The approval was spent: the same change in the other direction asks again,
	// and an autoscaling change names itself in the effects.
	_, err = f.scal.Set(rest, sv.ID, scaling.SetInput{Autoscaling: &scaling.AutoscalingPatch{Enabled: new(true), Max: new(5)}})
	ar, ok = prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Len(t, ar.Plan.Effects, 1, "3 replicas already fit the range, only autoscaling changes")
	require.Contains(t, ar.Plan.Effects[0], "Changes autoscaling (enabled: true, 1–5 replicas")

	// A person in the browser is never asked.
	_, err = f.scal.Set(f.ctx, sv.ID, scaling.SetInput{Replicas: new(2)})
	require.NoError(t, err)
	// A request that changes nothing is not guarded either.
	_, err = f.scal.Set(rest, sv.ID, scaling.SetInput{Replicas: new(2)})
	require.NoError(t, err)
}

func TestScalingAProcessOfAProductionServiceNeedsApprovalForRESTTokens(t *testing.T) {
	f := newFixture(t)
	g := f.installGuard()
	sv := f.multi("shop")
	f.deployMulti(sv)
	f.setFacts(agentprotocol.CapProcesses)
	rest := f.restToken()

	_, err := f.scal.SetProcess(rest, sv.ID, "worker", scaling.SetInput{Replicas: new(4)})
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Summary, "Change the scaling of process worker of shop")
	require.Contains(t, ar.Plan.Effects[0], "process worker")
	_, stored := f.processRow(sv.ID, "worker")
	require.False(t, stored, "nothing is written before the approval")

	f.approve(g, err)
	_, err = f.scal.SetProcess(rest, sv.ID, "worker", scaling.SetInput{Replicas: new(4)})
	require.NoError(t, err)
	row, _ := f.processRow(sv.ID, "worker")
	require.EqualValues(t, 4, row.Replicas)

	// Autoscaling of one process is a guarded change as well.
	_, err = f.scal.SetProcess(rest, sv.ID, "worker", scaling.SetInput{Autoscaling: &scaling.AutoscalingPatch{Enabled: new(true)}})
	ar, ok = prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Effects[len(ar.Plan.Effects)-1], "Changes autoscaling of process worker")
}

func TestScalingOutsideProductionIsNotGuarded(t *testing.T) {
	f := newFixture(t)
	f.installGuard()
	_, err := f.pool.Exec(context.Background(), "UPDATE environments SET is_production = false WHERE id = $1", f.env.ID)
	require.NoError(t, err)
	sv := f.web("web")
	_, err = f.scal.Set(f.restToken(), sv.ID, scaling.SetInput{Replicas: new(2)})
	require.NoError(t, err)
}

// --- autoscaler decisions beyond the CPU windows ---------------------------------

func (f *fixture) lastAutoscaleReason(id uuid.UUID) string {
	f.t.Helper()
	var md []byte
	require.NoError(f.t, f.pool.QueryRow(context.Background(),
		"SELECT metadata FROM audit_events WHERE action = 'service.autoscale' AND resource_id = $1 ORDER BY id DESC LIMIT 1", id.String()).Scan(&md))
	var meta map[string]any
	require.NoError(f.t, json.Unmarshal(md, &meta))
	return meta["reason"].(string)
}

func TestAutoscalerBringsAServiceBelowItsMinimumUpWithoutLoad(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	f.deploy(sv)
	f.enableAutoscaling(sv.ID, services.Autoscaling{Enabled: true, Min: 2, Max: 4, TargetCPUPercent: 70})

	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	require.EqualValues(t, 2, f.reload(sv.ID).Replicas)
	require.Equal(t, "Below the autoscaling minimum of 2", f.lastAutoscaleReason(sv.ID))
	cmds := f.scaleCommands()
	require.Len(t, cmds, 1)
	require.Equal(t, 2, decodeScale(t, cmds[0].Payload).Replicas)

	// At the minimum with no load, nothing more happens.
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	require.EqualValues(t, 2, f.reload(sv.ID).Replicas)
	require.Len(t, f.scaleCommands(), 1)
}

func TestAutoscalerBringsAServiceAboveItsMaximumBackToIt(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	f.deploy(sv)
	f.setReplicas(sv.ID, 5)
	f.enableAutoscaling(sv.ID, services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})

	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	require.EqualValues(t, 3, f.reload(sv.ID).Replicas, "never ends above the maximum")
	require.Equal(t, "Above the autoscaling maximum of 3", f.lastAutoscaleReason(sv.ID))
	require.Equal(t, 3, decodeScale(t, f.scaleCommands()[0].Payload).Replicas)
}

func TestAutoscalerLeavesStoppedServicesAndRunningDeploymentsAlone(t *testing.T) {
	f := newFixture(t)
	stopped := f.web("stopped")
	f.deploy(stopped)
	busy := f.web("busy")
	f.deploy(busy)
	for _, sv := range []db.Service{stopped, busy} {
		f.enableAutoscaling(sv.ID, services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})
		f.load(sv.ID, 1, 95, 3*time.Minute)
	}
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET desired_state = $2 WHERE id = $1", stopped.ID, services.DesiredStopped)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), `INSERT INTO deployments (service_id, server_id, trigger, actor_type, status, strategy, generation)
		VALUES ($1, $2, 'manual', 'user', 'building', 'rolling', 1)`, busy.ID, f.server.ID)
	require.NoError(t, err)

	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	require.EqualValues(t, 1, f.reload(stopped.ID).Replicas, "a stopped service is not scaled out")
	require.EqualValues(t, 1, f.reload(busy.ID).Replicas, "no scaling while a deployment runs")
	require.Empty(t, f.scaleCommands())
}

func TestAutoscalerIgnoresServicesWithoutADeploymentOrAutoscaling(t *testing.T) {
	f := newFixture(t)
	undeployed := f.web("undeployed")
	f.enableAutoscaling(undeployed.ID, services.Autoscaling{Enabled: true, Min: 2, Max: 4, TargetCPUPercent: 70})
	manual := f.web("manual")
	f.deploy(manual)
	f.load(manual.ID, 1, 99, 3*time.Minute)

	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	require.EqualValues(t, 1, f.reload(undeployed.ID).Replicas)
	require.EqualValues(t, 1, f.reload(manual.ID).Replicas, "autoscaling is off: load changes nothing")
	require.Empty(t, f.scaleCommands())
}

// --- command results -------------------------------------------------------------

func (f *fixture) insertCommand(kind string, payload []byte) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	_, err := f.q.InsertAgentCommand(context.Background(), db.InsertAgentCommandParams{
		ID: id, ServerID: f.server.ID, Kind: kind, Payload: payload,
		IdempotencyKey: kind + ":" + id.String(), ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(f.t, err)
	return id
}

func scalePayloadOf(t *testing.T, serviceID, process string) []byte {
	t.Helper()
	raw, err := json.Marshal(agentprotocol.ScaleServicePayload{ServiceID: serviceID, Process: process, Replicas: 2})
	require.NoError(t, err)
	return raw
}

func TestHandleResultRecordsOnlyFailedScaleCommands(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	sv := f.web("web")
	failed := agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "no free port"}
	blocked := func() *string { return f.reload(sv.ID).ScaleBlockedReason }

	// A success, an unknown command, another kind of command and unreadable payloads change nothing.
	scale := f.insertCommand(agentprotocol.KindScaleService, scalePayloadOf(t, sv.ID.String(), ""))
	require.NoError(t, f.scal.HandleResult(ctx, scale, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded}))
	require.NoError(t, f.scal.HandleResult(ctx, uuid.New(), failed))
	other := f.insertCommand(agentprotocol.KindDeploy, scalePayloadOf(t, sv.ID.String(), ""))
	require.NoError(t, f.scal.HandleResult(ctx, other, failed))
	garbled := f.insertCommand(agentprotocol.KindScaleService, []byte(`"not a payload"`))
	require.NoError(t, f.scal.HandleResult(ctx, garbled, failed), "an unreadable payload must not be redelivered forever")
	noService := f.insertCommand(agentprotocol.KindScaleService, scalePayloadOf(t, "not-a-uuid", ""))
	require.NoError(t, f.scal.HandleResult(ctx, noService, failed))
	gone := f.insertCommand(agentprotocol.KindScaleService, scalePayloadOf(t, uuid.NewString(), ""))
	require.NoError(t, f.scal.HandleResult(ctx, gone, failed), "a deleted service has nothing to block")
	require.Nil(t, blocked())

	// A failure stores its error; without one the outcome stands in for it.
	require.NoError(t, f.scal.HandleResult(ctx, scale, failed))
	require.NotNil(t, blocked())
	require.Equal(t, "Scale failed: no free port", *blocked())
	require.NoError(t, f.scal.HandleResult(ctx, scale, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed}))
	require.Equal(t, "Scale failed: failed", *blocked())

	// A manual scale clears it.
	_, err := f.scal.Set(f.ctx, sv.ID, scaling.SetInput{Replicas: new(2)})
	require.NoError(t, err)
	require.Nil(t, blocked())
}

func TestScaleFailureOfTheDefaultProcessOfAMultiProcessServiceBlocksTheService(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	sv := f.multi("shop")

	// The default process' name and "" are the same process; an unknown one left the configuration.
	require.NoError(t, f.scal.HandleResult(ctx, f.insertCommand(agentprotocol.KindScaleService, scalePayloadOf(t, sv.ID.String(), "web")),
		agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "no memory"}))
	require.NotNil(t, f.reload(sv.ID).ScaleBlockedReason)
	require.Equal(t, "Scale failed: no memory", *f.reload(sv.ID).ScaleBlockedReason)
	_, stored := f.processRow(sv.ID, "web")
	require.False(t, stored)

	require.NoError(t, f.scal.HandleResult(ctx, f.insertCommand(agentprotocol.KindScaleService, scalePayloadOf(t, sv.ID.String(), "removed")),
		agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "x"}))
	_, stored = f.processRow(sv.ID, "removed")
	require.False(t, stored, "a process that is no longer configured gets no row")
}

func TestRecordScaleFailureOfADeletedServiceIsANoOp(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.scal.RecordScaleFailure(context.Background(), uuid.New(), "whatever"))
}

// --- scaling a service whose deployment cannot be scaled ---------------------------

func TestScalingADeployedServiceWithoutItsDeployCommandIsRefused(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	f.deploy(sv)
	_, err := f.pool.Exec(context.Background(), "DELETE FROM agent_commands WHERE kind = $1", agentprotocol.KindDeploy)
	require.NoError(t, err)

	_, err = f.scal.Set(f.ctx, sv.ID, scaling.SetInput{Replicas: new(2)})
	requireAPIError(t, err, 409, "not_deployed")
	require.EqualValues(t, 1, f.reload(sv.ID).Replicas, "the count is not stored when the agent cannot be told")
	require.Empty(t, f.scaleCommands())
}

func TestScalingWithoutAPrincipalLabelIsAttributedToTheSystem(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	system := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeSystem, WorkspaceID: f.ws.ID, Scopes: []string{policy.ScopeDeploymentsWrite, policy.ScopeProjectsRead},
	})
	v, err := f.scal.Set(system, sv.ID, scaling.SetInput{Replicas: new(2)})
	require.NoError(t, err)
	require.Equal(t, "system", v.Scaling.LastScaleEvent.Actor)
}
