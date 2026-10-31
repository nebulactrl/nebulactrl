package deployments

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// setBuildOptionsCap gives the target (and the build server) the facts of an
// agent that advertises CapBuildOptions, or takes it away.
func (f *buildFixture) setBuildOptionsCap(target, builder bool) {
	f.t.Helper()
	bg := context.Background()
	caps := func(has bool, base ...string) []string {
		if has {
			base = append(base, agentprotocol.CapBuildOptions)
		}
		return base
	}
	tf, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Arch: "amd64", Capabilities: caps(target, agentprotocol.CapProxyV2, agentprotocol.CapCancelDeploy)})
	_, err := f.pool.Exec(bg, "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, tf)
	require.NoError(f.t, err)
	bf, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Arch: "amd64", Capabilities: caps(builder, agentprotocol.CapBuildPush, agentprotocol.CapCancelDeploy)})
	_, err = f.pool.Exec(bg, "UPDATE servers SET facts = $2 WHERE id = $1", f.builder.ID, bf)
	require.NoError(f.t, err)
}

func (f *buildFixture) deployNoCache(sv uuid.UUID, sha string) (DeploymentView, error) {
	f.t.Helper()
	return f.deps.DeployWithOptions(f.ctx, sv, DeployOptions{CommitSHA: sha, NoCache: true})
}

func (f *buildFixture) noCacheColumn(depID uuid.UUID) bool {
	f.t.Helper()
	var v bool
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT no_cache FROM deployments WHERE id = $1", depID).Scan(&v))
	return v
}

func TestNoCacheDeploymentRebuildsInsteadOfReusingAndIsRecorded(t *testing.T) {
	f := newBuildFixture(t)
	f.setBuildOptionsCap(true, true)
	sv := f.gitService("api")
	bg := context.Background()
	f.buildAndDeploy(sv, sha1, dig1)
	require.Len(t, f.commands(agentprotocol.KindBuildImage), 1)

	// Same commit, same settings: a normal deploy reuses the pushed image, a no-cache deploy builds again.
	v, err := f.deployNoCache(sv.ID, sha1)
	require.NoError(t, err)
	require.True(t, v.NoCache, "the view says so")
	require.True(t, f.noCacheColumn(v.ID), "and so does the deployment row")
	require.NoError(t, f.deps.runDeployRef(bg, v.ID, sha1, ""))
	require.Len(t, f.commands(agentprotocol.KindBuildImage), 2, "no reuse of the image already built for this commit")
	require.Empty(t, f.source(v.ID).RegistryRef, "nothing was reused")
	var bp agentprotocol.BuildImagePayload
	require.NoError(t, json.Unmarshal(f.buildCmd(v.ID).Payload, &bp))
	require.True(t, bp.Git.NoCache)
	evs := f.events(v.ID, PlacementStep)
	require.Contains(t, evs[0], "Deploying without build cache")
	// The persisted payload names the flag and no secret.
	require.NotContains(t, string(f.buildCmd(v.ID).Payload), "secret")

	// After the build the target pulls the image: no git source, so nothing builds again.
	f.finishBuild(v.ID, agentprotocol.OutcomeSucceeded, dig2)
	require.NoError(t, f.deps.runDeployOpts(bg, v.ID, "", "", true))
	var dp agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.deployCmd(v.ID).Payload, &dp))
	require.Nil(t, dp.Git)
	require.NotNil(t, dp.Image)
	f.finishDeploy(v.ID, "registry.example.com/shop-api@"+dig2)

	// The flag belonged to that deployment: the next one is a normal one and the
	// no-cache build's image is a regular reusable image for the commit.
	v2, err := f.deps.DeployWithOptions(f.ctx, sv.ID, DeployOptions{CommitSHA: sha1})
	require.NoError(t, err)
	require.False(t, v2.NoCache)
	require.False(t, f.noCacheColumn(v2.ID))
	require.NoError(t, f.deps.runDeployRef(bg, v2.ID, sha1, ""))
	require.Len(t, f.commands(agentprotocol.KindBuildImage), 2, "reused")
	var audit string
	require.NoError(t, f.pool.QueryRow(bg, `SELECT metadata::text FROM audit_events WHERE resource_id = $1 AND action = 'deployment.create'`, v.ID.String()).Scan(&audit))
	require.Contains(t, audit, `"noCache": true`)
}

func TestNoCacheDeploymentBuildingOnTheTargetPassesTheFlagToTheDeployPayload(t *testing.T) {
	f := newBuildFixture(t)
	f.setBuildOptionsCap(true, true)
	sv := f.gitService("api")
	bg := context.Background()
	_, err := f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.builder.ID)
	require.NoError(t, err)

	v, err := f.deployNoCache(sv.ID, sha1)
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeployRef(bg, v.ID, sha1, ""))
	var dp agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.last(agentprotocol.KindDeploy).Payload, &dp))
	require.NotNil(t, dp.Git)
	require.True(t, dp.Git.NoCache)
	f.finishDeploy(v.ID, dig1)

	v2, err := f.deps.DeployWithOptions(f.ctx, sv.ID, DeployOptions{CommitSHA: sha2})
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeployRef(bg, v2.ID, sha2, ""))
	var dp2 agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.last(agentprotocol.KindDeploy).Payload, &dp2))
	require.False(t, dp2.Git.NoCache)
}

func TestNoCacheNeedsAGitServiceAndACurrentAgent(t *testing.T) {
	f := newBuildFixture(t)
	bg := context.Background()
	sv := f.gitService("api")

	// An agent without the capability would build with cache and say nothing: refused, nothing created.
	f.setBuildOptionsCap(false, true)
	_, err := f.deployNoCache(sv.ID, sha1)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status)
	require.Equal(t, "agent_too_old", ae.Code)
	var n int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM deployments WHERE service_id = $1", sv.ID).Scan(&n))
	require.Zero(t, n)
	_, err = f.deps.DeployStagedWith(f.ctx, f.env.ID, []uuid.UUID{sv.ID}, "", true)
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "agent_too_old", ae.Code)

	// A normal deploy to the same server is fine.
	_, err = f.deps.DeployWithOptions(f.ctx, sv.ID, DeployOptions{CommitSHA: sha1})
	require.NoError(t, err)

	// An image service has nothing to build.
	img := f.web("img")
	_, err = f.deps.DeployWithOptions(f.ctx, img.ID, DeployOptions{NoCache: true})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "validation_failed", ae.Code, "an image service has nothing to build")
	require.Contains(t, ae.Message, "git")
}

func TestNoCacheOnAnOutdatedBuildServerFailsThePreparation(t *testing.T) {
	f := newBuildFixture(t)
	f.setBuildOptionsCap(true, false)
	sv := f.gitService("api")
	v, err := f.deployNoCache(sv.ID, sha1)
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeployRef(context.Background(), v.ID, sha1, ""))
	require.Equal(t, "failed", f.status(v.ID))
	require.Empty(t, f.commands(agentprotocol.KindBuildImage))
	var msg string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT status_message FROM deployments WHERE id = $1", v.ID).Scan(&msg))
	require.Contains(t, msg, "too old")
}

func TestDeployStagedWithNoCacheMarksGitServicesOnly(t *testing.T) {
	f := newBuildFixture(t)
	f.setBuildOptionsCap(true, true)
	sv := f.gitService("api")
	img := f.web("img")
	res, err := f.deps.DeployStagedWith(f.ctx, f.env.ID, nil, "", true)
	require.NoError(t, err)
	byService := map[uuid.UUID]DeploymentView{}
	for _, d := range res.Items {
		byService[d.ServiceID] = d
	}
	require.True(t, byService[sv.ID].NoCache)
	require.NotNil(t, byService[img.ID].ServiceID)
	require.False(t, byService[img.ID].NoCache, "an image service has nothing to build")
}

func TestSubmodulesReachThePayloadAndRequireAnAgentThatChecksThemOut(t *testing.T) {
	f := newBuildFixture(t)
	bg := context.Background()
	f.setBuildOptionsCap(true, true)
	sv := f.gitService("api")
	_, err := f.pool.Exec(bg, "UPDATE services SET config = jsonb_set(config, '{submodules}', '\"on\"') WHERE id = $1", sv.ID)
	require.NoError(t, err)

	// On a build server the payload carries the mode.
	v, err := f.deps.DeployWithOptions(f.ctx, sv.ID, DeployOptions{CommitSHA: sha1})
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeployRef(bg, v.ID, sha1, ""))
	var bp agentprotocol.BuildImagePayload
	require.NoError(t, json.Unmarshal(f.buildCmd(v.ID).Payload, &bp))
	require.Equal(t, "on", bp.Git.Submodules)
	f.finishBuild(v.ID, agentprotocol.OutcomeSucceeded, dig1)
	require.NoError(t, f.deps.runDeployOpts(bg, v.ID, "", "", true))
	f.finishDeploy(v.ID, "registry.example.com/shop-api@"+dig1)

	// A different mode is a different build: the image is not reused.
	_, err = f.pool.Exec(bg, "UPDATE services SET config = jsonb_set(config, '{submodules}', '\"off\"') WHERE id = $1", sv.ID)
	require.NoError(t, err)
	v2, err := f.deps.DeployWithOptions(f.ctx, sv.ID, DeployOptions{CommitSHA: sha1})
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeployRef(bg, v2.ID, sha1, ""))
	require.Len(t, f.commands(agentprotocol.KindBuildImage), 2)

	// "on" is blocked on an agent that would silently skip submodules; auto is not.
	f.setBuildOptionsCap(false, false)
	_, err = f.pool.Exec(bg, "UPDATE services SET config = jsonb_set(config, '{submodules}', '\"on\"') WHERE id = $1", sv.ID)
	require.NoError(t, err)
	plan, err := f.deps.Plan(f.ctx, sv.ID)
	require.NoError(t, err)
	var codes []string
	for _, b := range plan.Blocking {
		codes = append(codes, b.Code)
	}
	require.Contains(t, codes, "agent_too_old")
	_, err = f.pool.Exec(bg, "UPDATE services SET config = config - 'submodules' WHERE id = $1", sv.ID)
	require.NoError(t, err)
	plan, err = f.deps.Plan(f.ctx, sv.ID)
	require.NoError(t, err)
	for _, b := range plan.Blocking {
		require.NotEqual(t, "agent_too_old", b.Code, b.Message)
	}
}
