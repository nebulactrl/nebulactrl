package composites

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestServiceStateCompletedOneShot(t *testing.T) {
	dep := uuid.New()
	view := func(oneShot bool, state, health string) services.ServiceView {
		return services.ServiceView{
			CurrentDeploymentID: &dep,
			Config:              services.Config{OneShot: oneShot},
			Observed:            &services.ObservedView{State: state, Health: health},
		}
	}
	require.Equal(t, stateRunning, serviceState(view(true, agentprotocol.StateExited, agentprotocol.HealthHealthy)))
	require.Equal(t, stateUnhealthy, serviceState(view(true, agentprotocol.StateExited, agentprotocol.HealthUnhealthy)))
	require.Equal(t, stateUnhealthy, serviceState(view(false, agentprotocol.StateExited, agentprotocol.HealthHealthy)))
}

func TestServiceStateOfACronService(t *testing.T) {
	dep := uuid.New()
	view := func(lastRun string) services.ServiceView {
		v := services.ServiceView{
			CurrentDeploymentID: &dep,
			Config:              services.Config{OneShot: true, Cron: &services.CronSpec{Schedule: "* * * * *"}},
			Cron:                &services.CronView{Schedule: "* * * * *"},
		}
		if lastRun != "" {
			v.Cron.LastRun = &services.CronRunView{Status: lastRun}
		}
		return v
	}
	// No container between runs, so nothing is observed: that is not "unknown".
	require.Equal(t, stateRunning, serviceState(view("")))
	require.Equal(t, stateRunning, serviceState(view("succeeded")))
	require.Equal(t, stateUnhealthy, serviceState(view("failed")))
	require.Equal(t, stateUnhealthy, serviceState(view("timed_out")))
	require.Equal(t, diagHealthy, diagServiceVerdict(view("succeeded")))
	require.Equal(t, diagDegraded, diagServiceVerdict(view("failed")))

	paused := view("succeeded")
	paused.DesiredState = services.DesiredStopped
	require.Equal(t, stateStopped, serviceState(paused))
}
