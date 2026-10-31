package mcp

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/mcp/catalog"
)

func TestGateAppliesOnlyToTheCallsAnOperationGuards(t *testing.T) {
	lock, ok := catalog.CentralGuard("lockBackup")
	require.True(t, ok)
	require.True(t, gateApplies(lock, map[string]any{"locked": false}), "unlocking a backup needs approval")
	require.False(t, gateApplies(lock, map[string]any{"locked": true}), "locking one is protective")

	adopt, _ := catalog.CentralGuard("fixDomainDns")
	require.True(t, gateApplies(adopt, map[string]any{"adopt": true}))
	require.False(t, gateApplies(adopt, map[string]any{"adopt": false}), "creating missing records replaces nothing")
	require.False(t, gateApplies(adopt, map[string]any{}))

	dns, _ := catalog.CentralGuard("updateDomainDns")
	require.True(t, gateApplies(dns, map[string]any{"originLock": false}))
	require.True(t, gateApplies(dns, map[string]any{"proxied": true}), "any proxy change reaches production DNS")
	require.False(t, gateApplies(dns, map[string]any{"originLock": true}))

	task, _ := catalog.CentralGuard("createServiceTask")
	require.True(t, gateApplies(task, map[string]any{}), "no condition: every call is guarded")
}

func TestGateDigestBindsEveryArgument(t *testing.T) {
	id := uuid.New()
	base := map[string]any{"name": "x", "schedule": "* * * * *", "command": []any{"id"}}
	d := gateDigest("createServiceTask", id, base)
	require.Equal(t, d, gateDigest("createServiceTask", id, base), "the same call gives the same digest")

	changed := map[string]any{"name": "x", "schedule": "* * * * *", "command": []any{"env"}}
	require.NotEqual(t, d, gateDigest("createServiceTask", id, changed), "another command asks again")
	require.NotEqual(t, d, gateDigest("updateServiceTask", id, base), "another operation asks again")
	require.NotEqual(t, d, gateDigest("createServiceTask", uuid.New(), base), "another target asks again")

	withKey := map[string]any{"name": "x", "schedule": "* * * * *", "command": []any{"id"}, "Idempotency-Key": "k1"}
	require.Equal(t, d, gateDigest("createServiceTask", id, withKey), "the idempotency key is not part of what was approved")
}

func TestGateScopeStatesWhatWillHappen(t *testing.T) {
	c, _ := catalog.CentralGuard("createServiceTask")
	sid, eid, pid := uuid.New(), uuid.New(), uuid.New()
	scope := gateScope(c, []gateRow{{serviceID: sid.String(), serviceName: "api", envID: eid, envName: "production", projectID: pid, projectName: "Shop"}},
		map[string]any{"command": []any{"sh", "-c", "env"}, "schedule": "* * * * *", "ignored": "x"}, uuid.New())
	require.True(t, scope.IsProduction)
	require.Equal(t, "Schedule a command to run inside api in production", scope.Summary)
	require.Equal(t, eid, scope.EnvironmentID)
	require.Equal(t, pid, scope.ProjectID)
	require.Contains(t, scope.Effects, `schedule: "* * * * *"`)
	require.Contains(t, scope.Effects, `command: ["sh","-c","env"]`)
	for _, e := range scope.Effects {
		require.NotContains(t, e, "ignored", "only the operation's own arguments are shown")
	}
	require.Len(t, scope.Targets, 1)
	require.Equal(t, "service", scope.Targets[0].Kind)

	srv, _ := catalog.CentralGuard("revokeServer")
	scope = gateScope(srv, []gateRow{
		{serviceID: sid.String(), serviceName: "api", envID: eid, envName: "production", projectID: pid, projectName: "Shop", serverNm: "edge-1"},
		{serviceID: uuid.NewString(), serviceName: "worker", envID: eid, envName: "production", projectID: pid, projectName: "Shop", serverNm: "edge-1"},
	}, map[string]any{}, uuid.New())
	require.Contains(t, scope.Summary, "edge-1")
	require.True(t, scope.Irreversible)
	require.Len(t, scope.Targets, 2)
}
