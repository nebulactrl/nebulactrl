package deployments

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/notifications/notificationstest"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestStepPredeployMapsToReleasing(t *testing.T) {
	st, ok := stepStatus(agentprotocol.StepPredeploy)
	require.True(t, ok)
	require.Equal(t, Releasing, st)
	// Image sources skip building: preparing → releasing is a legal move.
	require.Equal(t, []Status{Releasing}, Path(Preparing, Releasing))
}

// deployWeb deploys the service end to end against the fake agent and returns
// the deployment and the deploy payload that was enqueued.
func (f *routeFixture) deployWeb(id uuid.UUID) (uuid.UUID, agentprotocol.DeployPayload) {
	f.t.Helper()
	v, err := f.deps.Deploy(f.ctx, id, "", "")
	require.NoError(f.t, err)
	require.NoError(f.t, f.deps.runDeploy(context.Background(), v.ID))
	var p agentprotocol.DeployPayload
	require.NoError(f.t, json.Unmarshal(f.lastCommand(agentprotocol.KindDeploy).Payload, &p))
	data, _ := json.Marshal(agentprotocol.DeployResult{Outcome: agentprotocol.OutcomeSucceeded, ImageDigest: "traefik/whoami@sha256:abc"})
	require.NoError(f.t, f.deps.HandleResult(context.Background(), f.server.ID, uuid.New(), &v.ID, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: data}))
	return v.ID, p
}

// The pre-deploy command rides the deploy payload of a new release. Rollbacks
// re-run an existing release and must not run migrations again.
func TestPreDeployCommandRidesNewReleasesOnly(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("api")

	_, p0 := f.deployWeb(web.ID)
	require.Nil(t, p0.PreDeploy, "no command configured: no pre-deploy step")

	cfg := `{"image":"traefik/whoami:latest","port":80,"preDeployCommand":["rake","db:migrate"],"preDeployTimeoutSeconds":90}`
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET config = $2::jsonb, generation = generation + 1 WHERE id = $1", web.ID, cfg)
	require.NoError(t, err)
	withCmd, p1 := f.deployWeb(web.ID)
	require.NotNil(t, p1.PreDeploy)
	require.Equal(t, []string{"rake", "db:migrate"}, p1.PreDeploy.Command)
	require.Equal(t, 90, p1.PreDeploy.TimeoutSeconds)

	_, err = f.pool.Exec(context.Background(), "UPDATE services SET generation = generation + 1 WHERE id = $1", web.ID)
	require.NoError(t, err)
	f.deployWeb(web.ID) // a later release

	// Rolling back to the release that has the command re-runs it as it was,
	// without the pre-deploy step.
	_, err = f.deps.Rollback(f.ctx, withCmd)
	require.NoError(t, err)
	var rb uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT id FROM deployments WHERE service_id = $1 AND trigger = 'rollback'`, web.ID).Scan(&rb))
	require.NoError(t, f.deps.runDeploy(context.Background(), rb))
	var p2 agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.lastCommand(agentprotocol.KindDeploy).Payload, &p2))
	require.Nil(t, p2.PreDeploy, "rollbacks never run the pre-deploy command")
}

func setConfig(t *testing.T, f *routeFixture, id uuid.UUID, cfg string) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET config = $2::jsonb, generation = generation + 1 WHERE id = $1", id, cfg)
	require.NoError(t, err)
}

// The post-deploy command rides the deploy payload of a new release only:
// rollbacks re-run an existing release and must not run it again.
func TestPostDeployCommandRidesNewReleasesOnly(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("api")

	_, p0 := f.deployWeb(web.ID)
	require.Nil(t, p0.PostDeploy, "no command configured: no post-deploy step")

	setConfig(t, f, web.ID, `{"image":"traefik/whoami:latest","port":80,"postDeployCommand":["rake","cache:warm"],"postDeployTimeoutSeconds":45}`)
	withCmd, p1 := f.deployWeb(web.ID)
	require.NotNil(t, p1.PostDeploy)
	require.Equal(t, []string{"rake", "cache:warm"}, p1.PostDeploy.Command)
	require.Equal(t, 45, p1.PostDeploy.TimeoutSeconds)
	require.Nil(t, p1.PreDeploy)

	_, err := f.pool.Exec(context.Background(), "UPDATE services SET generation = generation + 1 WHERE id = $1", web.ID)
	require.NoError(t, err)
	f.deployWeb(web.ID) // a later release

	_, err = f.deps.Rollback(f.ctx, withCmd)
	require.NoError(t, err)
	var rb uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT id FROM deployments WHERE service_id = $1 AND trigger = 'rollback'`, web.ID).Scan(&rb))
	require.NoError(t, f.deps.runDeploy(context.Background(), rb))
	var p2 agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.lastCommand(agentprotocol.KindDeploy).Payload, &p2))
	require.Nil(t, p2.PostDeploy, "rollbacks never run the post-deploy command")
}

// A failed post-deploy command is warn-only: the agent reports a warning step
// and a succeeded result. The control plane records the warning in the
// timeline, emits one notification, and the deployment still succeeds.
func TestPostDeployWarningRecordsEventNotifiesAndStillSucceeds(t *testing.T) {
	f := newRouteFixture(t)
	rec := &notificationstest.Recorder{}
	f.deps.Notifier = rec
	web := f.web("api")
	setConfig(t, f, web.ID, `{"image":"traefik/whoami:latest","port":80,"postDeployCommand":["rake","cache:warm"]}`)

	v, err := f.deps.Deploy(f.ctx, web.ID, "", "")
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeploy(context.Background(), v.ID))
	bg := context.Background()

	warning := "Post-deploy command failed with exit code 3. The new release is live; the deployment was not rolled back."
	for _, ev := range []agentprotocol.EventFrame{
		{DeploymentID: v.ID.String(), Step: agentprotocol.StepVerify, Status: agentprotocol.StepSucceeded},
		{DeploymentID: v.ID.String(), Step: agentprotocol.StepPostdeploy, Status: agentprotocol.StepStarted, Message: "Running post-deploy command: rake cache:warm"},
		{DeploymentID: v.ID.String(), Step: agentprotocol.StepPostdeploy, Status: agentprotocol.StepWarning, Message: warning},
	} {
		require.NoError(t, f.deps.HandleEvent(bg, f.server.ID, ev))
	}
	require.Len(t, rec.OfType(notifications.TypeTaskFailed), 1, "the warning is announced as it arrives")
	require.Empty(t, rec.OfType(notifications.TypeDeploySucceeded), "the deployment is not finished yet")

	data, _ := json.Marshal(agentprotocol.DeployResult{
		Outcome: agentprotocol.OutcomeSucceeded, ImageDigest: "traefik/whoami@sha256:abc",
		Message: "Deployed nc-api-x. Warning: post-deploy command failed with exit code 3",
	})
	require.NoError(t, f.deps.HandleResult(bg, f.server.ID, uuid.New(), &v.ID, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: data}))

	var status string
	require.NoError(t, f.pool.QueryRow(bg, `SELECT status FROM deployments WHERE id = $1`, v.ID).Scan(&status))
	require.Equal(t, string(Succeeded), status, "a post-deploy failure never fails the deployment")

	var n int
	require.NoError(t, f.pool.QueryRow(bg, `SELECT count(*) FROM deployment_events WHERE deployment_id = $1 AND step = 'postdeploy' AND status = 'warning'`, v.ID).Scan(&n))
	require.Equal(t, 1, n, "the warning is in the timeline")

	warns := rec.OfType(notifications.TypeTaskFailed)
	require.Len(t, warns, 1)
	require.Equal(t, notifications.SeverityWarning, warns[0].Severity)
	require.Equal(t, "Post-deploy command failed on api", warns[0].Title)
	require.Contains(t, warns[0].Body, "exit code 3")
	require.Equal(t, web.ID, *warns[0].ServiceID)
	require.Len(t, rec.OfType(notifications.TypeDeploySucceeded), 1, "the deployment is still announced as succeeded")
	require.Empty(t, rec.OfType(notifications.TypeDeployFailed))
}
