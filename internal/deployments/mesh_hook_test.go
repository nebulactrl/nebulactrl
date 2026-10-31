package deployments

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeMeshHook is a scripted MeshHook.
type fakeMeshHook struct {
	plan       services.MeshPlan
	prepareErr error
	waiting    string
	waitErr    error
	alias      string
	prepared   []string // triggers seen by Prepare
}

func (h *fakeMeshHook) PlanCheck(context.Context, *db.Queries, services.Scope) (services.MeshPlan, error) {
	return h.plan, nil
}

func (h *fakeMeshHook) Prepare(_ context.Context, _ uuid.UUID, trigger string) error {
	h.prepared = append(h.prepared, trigger)
	return h.prepareErr
}

func (h *fakeMeshHook) WaitReady(context.Context, uuid.UUID, time.Time) (string, error) {
	return h.waiting, h.waitErr
}

func (h *fakeMeshHook) Alias(context.Context, *db.Queries, uuid.UUID) (string, error) {
	return h.alias, nil
}

func (f *routeFixture) meshDeploy(h *fakeMeshHook, slug string) (uuid.UUID, error) {
	f.t.Helper()
	f.deps.SetMesh(h)
	web := f.web(slug)
	v, err := f.deps.Deploy(f.ctx, web.ID, "", "")
	require.NoError(f.t, err)
	return v.ID, f.deps.runDeploy(context.Background(), v.ID, "")
}

func (f *routeFixture) status(id uuid.UUID) string {
	f.t.Helper()
	var s string
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT status FROM deployments WHERE id = $1", id).Scan(&s))
	return s
}

func TestPlanCarriesTheMeshBlockAndItsBlockers(t *testing.T) {
	f := newRouteFixture(t)
	h := &fakeMeshHook{plan: services.MeshPlan{
		CrossServer: true, DevicesAdded: 2,
		Links:    []services.MeshPlanLink{{Purpose: "service", State: "new", Providers: []string{"postgres"}}},
		Blockers: []services.MeshBlocker{{Code: apierr.CodeMeshServerRequired, Message: "enable the mesh on agent2 first"}},
	}}
	f.deps.SetMesh(h)
	web := f.web("api")
	plan, err := f.deps.Plan(f.ctx, web.ID)
	require.NoError(t, err)
	require.True(t, plan.Mesh.CrossServer)
	require.Equal(t, 2, plan.Mesh.DevicesAdded)
	require.Len(t, plan.Mesh.Links, 1)
	codes := []string{}
	for _, b := range plan.Blocking {
		codes = append(codes, b.Code)
	}
	require.Contains(t, codes, apierr.CodeMeshServerRequired)
	require.Contains(t, plan.Warnings, "Deploying adds 2 tailnet devices (one mesh link per pair of servers): tagged devices may be billed by Tailscale.")

	// Without a hook the plan has no mesh block content and nothing blocks.
	f2 := newRouteFixture(t)
	plan, err = f2.deps.Plan(f2.ctx, f2.web("api").ID)
	require.NoError(t, err)
	require.False(t, plan.Mesh.CrossServer)
	require.Empty(t, plan.Mesh.Blockers)
}

func TestDeployWaitsForTheMeshLinksThenDispatchesWithTheAlias(t *testing.T) {
	f := newRouteFixture(t)
	h := &fakeMeshHook{waiting: "Waiting for the mesh link agent1 -> agent2 to come online.", alias: "nc-svc-abcd1234"}
	id, err := f.meshDeploy(h, "api")
	var wait *buildWaitError
	require.ErrorAs(t, err, &wait)
	require.Equal(t, h.waiting, wait.message)
	require.Equal(t, "preparing", f.status(id), "the deployment waits in preparing")
	require.Empty(t, f.disp.cmds, "nothing is dispatched while a link is not ready")
	require.Equal(t, []string{TriggerManual}, h.prepared)

	h.waiting = ""
	require.NoError(t, f.deps.runDeploy(context.Background(), id, ""))
	var p agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.lastCommand(agentprotocol.KindDeploy).Payload, &p))
	require.Equal(t, "nc-svc-abcd1234", p.MeshAlias)
}

func TestDeployFailsBeforeAnythingIsCreatedWhenTheMeshSaysSo(t *testing.T) {
	f := newRouteFixture(t)
	h := &fakeMeshHook{waitErr: apierr.Conflict(apierr.CodeMeshLinkNotReady, "Not deployed: mesh link not ready. No changes were made.")}
	id, err := f.meshDeploy(h, "api")
	require.NoError(t, err, "a user-facing failure is recorded on the deployment, not retried")
	require.Equal(t, "failed", f.status(id))
	var msg string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT status_message FROM deployments WHERE id = $1", id).Scan(&msg))
	require.Equal(t, "Not deployed: mesh link not ready. No changes were made.", msg)
	require.Empty(t, f.disp.cmds)

	f2 := newRouteFixture(t)
	h2 := &fakeMeshHook{prepareErr: apierr.Unprocessable(apierr.CodeMeshServerRequired, "Not deployed: enable the mesh on agent2 first. No changes were made.")}
	id, err = f2.meshDeploy(h2, "api")
	require.NoError(t, err)
	require.Equal(t, "failed", f2.status(id))
	require.Empty(t, f2.disp.cmds)
}

func TestDeployNeverProceedsWithAPendingMeshRoute(t *testing.T) {
	f := newRouteFixture(t)
	bg := context.Background()
	srv2, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.server.WorkspaceID, Name: "agent2"})
	require.NoError(t, err)
	pg := f.web("pg")
	_, err = f.pool.Exec(bg, "UPDATE services SET server_id = $2 WHERE id = $1", pg.ID, srv2.ID)
	require.NoError(t, err)
	api := f.web("api")
	enc, err := f.deps.Box.SealString(secretbox.ServiceVariablesValue, secretbox.RowID(api.ID.String(), "DB_PORT"), "${{pg.PORT}}")
	require.NoError(t, err)
	_, err = f.q.UpsertServiceVariable(bg, db.UpsertServiceVariableParams{ServiceID: api.ID, Key: "DB_PORT", ValueEnc: enc})
	require.NoError(t, err)

	f.deps.SetMesh(&fakeMeshHook{})
	plan, err := f.deps.Plan(f.ctx, api.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Blocking, "the route is created by the deployment: a plan only warns")
	require.Contains(t, plan.Warnings, "pg runs on another server: api reaches it over the mesh, the route is created by this deployment (mesh_route_pending).")

	v, err := f.deps.Deploy(f.ctx, api.ID, "", "")
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeploy(bg, v.ID, ""))
	require.Equal(t, "failed", f.status(v.ID))
	var msg string
	require.NoError(t, f.pool.QueryRow(bg, "SELECT status_message FROM deployments WHERE id = $1", v.ID).Scan(&msg))
	require.Equal(t, "Not deployed: pg runs on another server and has no mesh route yet (mesh_route_pending). No changes were made.", msg)
	require.Empty(t, f.disp.cmds)
}
