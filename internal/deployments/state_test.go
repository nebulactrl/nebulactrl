package deployments

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestStateMachineRejectsBackwardsAndTerminalTransitions(t *testing.T) {
	var te *TransitionError
	require.ErrorAs(t, Transition(Succeeded, Building), &te)
	require.Equal(t, Succeeded, te.From)
	require.Equal(t, Building, te.To)

	require.Error(t, Transition(Failed, Queued))
	require.Error(t, Transition(Releasing, Building))
	require.Error(t, Transition(Queued, Releasing))
	require.NoError(t, Transition(Preparing, Releasing)) // image sources skip building
	require.NoError(t, Transition(Verifying, RolledBack))
}

func TestPathWalksIntermediateStatuses(t *testing.T) {
	require.Equal(t, []Status{Queued, Preparing, Releasing}, Path(Created, Releasing))
	require.Nil(t, Path(Succeeded, Building))
}

func TestStepStatusMapping(t *testing.T) {
	cases := map[string]Status{
		agentprotocol.StepBuild: Building, agentprotocol.StepCreate: Releasing, agentprotocol.StepStart: Releasing,
		agentprotocol.StepHealth: Verifying, agentprotocol.StepRoute: Verifying, agentprotocol.StepVerify: Verifying,
	}
	for step, want := range cases {
		got, ok := stepStatus(step)
		require.True(t, ok, step)
		require.Equal(t, want, got, step)
	}
	// Image pulls keep the deployment in `preparing`.
	_, ok := stepStatus(agentprotocol.StepPull)
	require.False(t, ok)
	require.Equal(t, Succeeded, outcomeStatus(agentprotocol.OutcomeSucceeded))
	require.Equal(t, RolledBack, outcomeStatus(agentprotocol.OutcomeRolledBack))
	require.Equal(t, RollbackFailed, outcomeStatus(agentprotocol.OutcomeRollbackFailed))
	require.Equal(t, Canceled, outcomeStatus(agentprotocol.OutcomeCanceled))
	require.Equal(t, Failed, outcomeStatus("weird"))
}

func TestPlannerPicksRecreateForVolumesAndPortlessServices(t *testing.T) {
	web := services.Config{Image: "app", Port: 80}.WithDefaults()

	s, reasons := ChooseStrategy(web, 0)
	require.Equal(t, agentprotocol.StrategyRolling, s)
	require.Empty(t, reasons)

	s, reasons = ChooseStrategy(web, 1)
	require.Equal(t, agentprotocol.StrategyRecreate, s)
	require.Equal(t, []string{ReasonHasVolumes}, reasons)

	worker := services.Config{Image: "worker"}.WithDefaults()
	s, reasons = ChooseStrategy(worker, 0)
	require.Equal(t, agentprotocol.StrategyRecreate, s)
	require.Equal(t, []string{ReasonNoHTTPPort}, reasons)

	s, reasons = ChooseStrategy(worker, 2)
	require.Equal(t, agentprotocol.StrategyRecreate, s)
	require.Equal(t, []string{ReasonHasVolumes, ReasonNoHTTPPort}, reasons)
}

func TestStatusRankNeverMovesBackwards(t *testing.T) {
	require.Less(t, statusRank(Preparing), statusRank(Building))
	require.Less(t, statusRank(Building), statusRank(Releasing))
	require.Less(t, statusRank(Releasing), statusRank(Verifying))
	require.Greater(t, statusRank(Succeeded), statusRank(Verifying))
}
