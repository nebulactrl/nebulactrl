package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/build"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// railpackBuilder is a fake Builder that reports how it built and can plan.
type railpackBuilder struct {
	*fakeBuilder
	info    agentprotocol.BuildInfo
	plan    agentprotocol.BuildPlanResult
	planErr error
	planReq build.Request
}

func (b *railpackBuilder) Build(ctx context.Context, req build.Request, logf func(string)) (string, error) {
	id, err := b.fakeBuilder.Build(ctx, req, logf)
	if err == nil && req.Info != nil {
		*req.Info = b.info
	}
	return id, err
}

func (b *railpackBuilder) Plan(_ context.Context, req build.Request, _ func(string)) (agentprotocol.BuildPlanResult, error) {
	b.planReq = req
	return b.plan, b.planErr
}

func gitPayload() agentprotocol.DeployPayload {
	p := basePayload()
	p.Image = nil
	p.Git = &agentprotocol.GitSource{
		URL: "https://example.com/r.git", CommitSHA: "0123456789abcdef0123456789abcdef01234567",
		DockerfilePath: "Dockerfile", BuildContext: ".", BuildType: "railpack",
	}
	return p
}

func TestGitDeployPassesBuildTypeAndReportsBuildInfo(t *testing.T) {
	r := newRig(t, nil)
	rb := &railpackBuilder{fakeBuilder: r.bld, info: agentprotocol.BuildInfo{
		Method: "railpack", RailpackVersion: "0.43.0", Providers: []string{"node"}, StartCommand: "npm run start", Builder: "docker",
	}}
	r.eng.cfg.Builder = rb

	res, dr := r.deploy(gitPayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Len(t, r.bld.calls, 1)
	require.Equal(t, "railpack", r.bld.calls[0].BuildType)
	require.NotNil(t, dr.Build)
	require.Equal(t, "railpack", dr.Build.Method)
	require.Equal(t, []string{"node"}, dr.Build.Providers)
	var sawLabel bool
	for _, e := range r.snd.events {
		if e.Step == agentprotocol.StepBuild && e.Status == agentprotocol.StepSucceeded {
			sawLabel = sawLabel || strings.Contains(e.Message, "Railpack 0.43.0 (node)")
		}
	}
	require.True(t, sawLabel, "the build event names the method: %+v", r.snd.events)

	// A build that reports no method (a fake, or a reused image) carries no build info.
	r2 := newRig(t, nil)
	_, dr = r2.deploy(gitPayload(), nil)
	require.Nil(t, dr.Build)
}

func planCommand(t *testing.T, p agentprotocol.BuildPlanPayload, secrets map[string]string) agentprotocol.Command {
	t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return agentprotocol.Command{ID: "plan-1", Kind: agentprotocol.KindBuildPlan, Payload: raw, Secrets: secrets}
}

func basePlan() agentprotocol.BuildPlanPayload {
	return agentprotocol.BuildPlanPayload{
		ServiceID: svcID, ServiceSlug: "web", GitURL: "https://example.com/r.git",
		CommitSHA: "0123456789abcdef0123456789abcdef01234567", BuildType: "auto",
		DockerfilePath: "Dockerfile", BuildContext: ".",
	}
}

func TestBuildPlanCommandPlansWithVarsAndMasksSecrets(t *testing.T) {
	r := newRig(t, nil)
	rb := &railpackBuilder{fakeBuilder: r.bld, plan: agentprotocol.BuildPlanResult{
		Outcome: agentprotocol.OutcomeFailed, Requested: "auto",
		Message: "railpack could not plan this source: token ghp_supersecrettoken leaked",
	}}
	r.eng.cfg.Builder = rb

	p := basePlan()
	p.HasToken = true
	p.BuildArgKeys = []string{"NODE_ENV"}
	p.BuildSecretKeys = []string{"API_KEY"}
	res := r.eng.HandleCommand(context.Background(), planCommand(t, p, map[string]string{
		agentprotocol.SecretGitToken:                 "ghp_supersecrettoken",
		agentprotocol.SecretBuildPrefix + "NODE_ENV": "production",
		agentprotocol.SecretBuildPrefix + "API_KEY":  "sekret-canary-12345",
	}))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)

	require.Equal(t, "plan-1", rb.planReq.DeploymentID)
	require.Equal(t, "auto", rb.planReq.BuildType)
	require.Equal(t, "ghp_supersecrettoken", rb.planReq.Token)
	require.Equal(t, map[string]string{"NODE_ENV": "production"}, rb.planReq.Vars.Args)
	require.Equal(t, map[string]string{"API_KEY": "sekret-canary-12345"}, rb.planReq.Vars.Secrets)

	var out agentprotocol.BuildPlanResult
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.Equal(t, agentprotocol.OutcomeFailed, out.Outcome, "a source problem is part of the result, the command succeeds")
	require.NotContains(t, out.Message, "ghp_supersecrettoken")
	require.Contains(t, out.Message, redactMark)
	require.Empty(t, r.bld.calls, "a plan never builds")
	require.Empty(t, r.dk.created, "a plan creates no managed container")
}

func TestBuildPlanCommandErrors(t *testing.T) {
	r := newRig(t, nil)
	r.eng.cfg.Builder = &railpackBuilder{fakeBuilder: r.bld, planErr: errors.New("git clone: token ghp_supersecrettoken rejected")}
	p := basePlan()
	p.HasToken = true
	res := r.eng.HandleCommand(context.Background(), planCommand(t, p, map[string]string{agentprotocol.SecretGitToken: "ghp_supersecrettoken"}))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.NotContains(t, res.Error, "ghp_supersecrettoken")

	bad := basePlan()
	bad.BuildContext = "../outside"
	res = r.eng.HandleCommand(context.Background(), planCommand(t, bad, nil))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "buildContext")

	bad = basePlan()
	bad.BuildType = "nixpacks"
	res = r.eng.HandleCommand(context.Background(), planCommand(t, bad, nil))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "build type")

	// A builder that cannot plan.
	r2 := newRig(t, nil)
	res = r2.eng.HandleCommand(context.Background(), planCommand(t, basePlan(), nil))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
}

func TestShellEntrypointDetection(t *testing.T) {
	for _, ep := range [][]string{{"/bin/bash", "-c"}, {"/bin/sh", "-c"}, {"sh", "-c"}, {"/usr/bin/dash", "-c"}} {
		require.True(t, isShellEntrypoint(ep), "%v", ep)
	}
	for _, ep := range [][]string{nil, {"/app"}, {"/bin/bash"}, {"/bin/bash", "-lc"}, {"python", "-c"}, {"/bin/sh", "-c", "x"}} {
		require.False(t, isShellEntrypoint(ep), "%v", ep)
	}
}

func TestStartCommandIsJoinedForShellEntrypointImages(t *testing.T) {
	r := newRig(t, nil)
	r.dk.images["sha256:railpack"] = docker.ImageInfo{ID: "sha256:railpack", Entrypoint: []string{"/bin/bash", "-c"}}
	r.dk.images["sha256:plain"] = docker.ImageInfo{ID: "sha256:plain", Entrypoint: []string{"/entrypoint.sh"}}

	d := &deploy{e: r.eng, p: agentprotocol.DeployPayload{StartCommand: []string{"node", "server.js", "--name", "my app", "it's"}}, imageID: "sha256:railpack"}
	require.Equal(t, []string{`node server.js --name 'my app' 'it'\''s'`}, d.startCommand())

	// A single element is already a script; an empty command keeps the image default.
	d = &deploy{e: r.eng, p: agentprotocol.DeployPayload{StartCommand: []string{"npm run start"}}, imageID: "sha256:railpack"}
	require.Equal(t, []string{"npm run start"}, d.startCommand())
	d = &deploy{e: r.eng, p: agentprotocol.DeployPayload{}, imageID: "sha256:railpack"}
	require.Empty(t, d.startCommand())

	// Ordinary images get the argv untouched.
	d = &deploy{e: r.eng, p: agentprotocol.DeployPayload{StartCommand: []string{"node", "server.js"}}, imageID: "sha256:plain"}
	require.Equal(t, []string{"node", "server.js"}, d.startCommand())
}
