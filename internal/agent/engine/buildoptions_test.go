package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestGitDeployPassesNoCacheAndSubmodulesToTheBuild(t *testing.T) {
	r := newRig(t, nil)
	p := gitPayload()
	p.Git.NoCache = true
	p.Git.Submodules = agentprotocol.SubmodulesOn
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Len(t, r.bld.calls, 1)
	require.True(t, r.bld.calls[0].NoCache)
	require.Equal(t, agentprotocol.SubmodulesOn, r.bld.calls[0].Submodules)

	// The flag belongs to one deployment: the next payload without it builds with cache.
	r2 := newRig(t, nil)
	_, dr = r2.deploy(gitPayload(), nil)
	require.NotEmpty(t, dr.ImageDigest)
	require.Len(t, r2.bld.calls, 1)
	require.False(t, r2.bld.calls[0].NoCache)
	require.Empty(t, r2.bld.calls[0].Submodules, "empty is auto")
}

func TestGitDeployRejectsAnUnknownSubmodulesMode(t *testing.T) {
	r := newRig(t, nil)
	p := gitPayload()
	p.Git.Submodules = "recursive"
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "submodules mode")
	require.Empty(t, r.bld.calls, "nothing is cloned for an invalid source")
}

func TestBuildPlanCommandPassesSubmodules(t *testing.T) {
	r := newRig(t, nil)
	rb := &railpackBuilder{fakeBuilder: r.bld, plan: agentprotocol.BuildPlanResult{Outcome: agentprotocol.OutcomeSucceeded}}
	r.eng.cfg.Builder = rb
	p := basePlan()
	p.Submodules = agentprotocol.SubmodulesOff
	res := r.eng.HandleCommand(context.Background(), planCommand(t, p, nil))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Equal(t, agentprotocol.SubmodulesOff, rb.planReq.Submodules)
	require.False(t, rb.planReq.NoCache, "a plan never builds")
}
