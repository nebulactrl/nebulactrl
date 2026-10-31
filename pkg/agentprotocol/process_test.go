package agentprotocol_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const testDeployment = "0123abcd-4567-89ab-cdef-0123456789ab"

func TestProcessContainerNameKeepsTheLegacyNameForTheDefaultProcess(t *testing.T) {
	require.Equal(t, "nc-shop-0123abcd-0", agentprotocol.ProcessContainerName("shop", testDeployment, "web", true, 0))
	require.Equal(t,
		agentprotocol.ContainerName("shop", testDeployment, 2),
		agentprotocol.ProcessContainerName("shop", testDeployment, "web", true, 2),
		"the default process is named exactly like a single-process replica")
	// The process name of the default process never enters its container name.
	require.Equal(t,
		agentprotocol.ProcessContainerName("shop", testDeployment, "web", true, 1),
		agentprotocol.ProcessContainerName("shop", testDeployment, "anything", true, 1))
}

func TestProcessContainerNameOfANamedProcess(t *testing.T) {
	require.Equal(t, "nc-shop-0123abcd-worker-0", agentprotocol.ProcessContainerName("shop", testDeployment, "worker", false, 0))
	require.Equal(t, "nc-shop-0123abcd-queue-high-3", agentprotocol.ProcessContainerName("shop", testDeployment, "queue-high", false, 3))
}

func TestProcessContainerNamesCountsFromZeroAndClampsToOne(t *testing.T) {
	require.Equal(t,
		[]string{"nc-shop-0123abcd-worker-0", "nc-shop-0123abcd-worker-1", "nc-shop-0123abcd-worker-2"},
		agentprotocol.ProcessContainerNames("shop", testDeployment, "worker", false, 3))
	require.Equal(t, []string{"nc-shop-0123abcd-worker-0"}, agentprotocol.ProcessContainerNames("shop", testDeployment, "worker", false, 0))
	require.Equal(t, []string{"nc-shop-0123abcd-worker-0"}, agentprotocol.ProcessContainerNames("shop", testDeployment, "worker", false, -4))
	require.Equal(t,
		agentprotocol.ContainerNames("shop", testDeployment, 2),
		agentprotocol.ProcessContainerNames("shop", testDeployment, "web", true, 2))
}

func TestProcessContainerNamesAreUniqueAcrossProcessesAndDeployments(t *testing.T) {
	seen := map[string]string{}
	for _, dep := range []string{testDeployment, "ffff0000-4567-89ab-cdef-0123456789ab"} {
		for _, p := range []struct {
			name string
			def  bool
		}{{"web", true}, {"worker", false}, {"clock", false}} {
			for _, n := range agentprotocol.ProcessContainerNames("shop", dep, p.name, p.def, 3) {
				if prev, dup := seen[n]; dup {
					t.Fatalf("container name %q used by %s and %s/%s", n, prev, dep, p.name)
				}
				seen[n] = dep + "/" + p.name
			}
		}
	}
	require.Len(t, seen, 2*3*3)
}

func TestProcessRouteIDIsDeterministicAndPerProcess(t *testing.T) {
	svc := uuid.NewString()
	a := agentprotocol.ProcessRouteID(svc, "worker")
	require.Equal(t, a, agentprotocol.ProcessRouteID(svc, "worker"), "same inputs, same ID")
	_, err := uuid.Parse(a)
	require.NoError(t, err, "a route ID is a UUID")
	require.NotEqual(t, a, agentprotocol.ProcessRouteID(svc, "api"), "another process, another ID")
	require.NotEqual(t, a, agentprotocol.ProcessRouteID(uuid.NewString(), "worker"), "another service, another ID")
	require.NotEqual(t, a, svc, "never the service's own route ID, which belongs to the default process")
	// A fixed vector pins the derivation: changing it would orphan live routes.
	require.Equal(t,
		uuid.NewSHA1(uuid.MustParse("11111111-2222-3333-4444-555555555555"), []byte("process:worker")).String(),
		agentprotocol.ProcessRouteID("11111111-2222-3333-4444-555555555555", "worker"))
}

func TestProcessRouteIDWithoutAServiceUUIDUsesTheOIDNamespace(t *testing.T) {
	require.Equal(t,
		uuid.NewSHA1(uuid.NameSpaceOID, []byte("process:worker")).String(),
		agentprotocol.ProcessRouteID("not-a-uuid", "worker"))
}

func TestProcessAlias(t *testing.T) {
	require.Equal(t, "shop", agentprotocol.ProcessAlias("shop", "web", true))
	require.Equal(t, "worker.shop", agentprotocol.ProcessAlias("shop", "worker", false))
}

func TestProcessNamePattern(t *testing.T) {
	for _, ok := range []string{"a", "web", "worker-2", "0", "a-b", "queue-high-priority", "abcdefghijklmnopqrstuvwxyz01234"[:31]} {
		require.True(t, agentprotocol.ProcessNamePattern.MatchString(ok), ok)
	}
	for _, bad := range []string{"", "-a", "a-", "A", "a_b", "a.b", "a b", "abcdefghijklmnopqrstuvwxyz012345"[:32], "é"} {
		require.False(t, agentprotocol.ProcessNamePattern.MatchString(bad), bad)
	}
}

func TestProcessConstantsAndPatternMatchTheServicesPackage(t *testing.T) {
	require.Equal(t, services.ProcessNamePattern.String(), agentprotocol.ProcessNamePattern.String())
	require.Equal(t, services.MaxProcesses, agentprotocol.MaxProcesses)
	require.Equal(t, services.DefaultProcessName, agentprotocol.DefaultProcessName)
	require.Equal(t, "nebulactrl.process", agentprotocol.LabelProcess)
	require.Equal(t, "service.processes", agentprotocol.CapProcesses)
}

func TestMultiProcessFieldsAreOmittedWhenEmpty(t *testing.T) {
	// A legacy payload and legacy frames carry none of the new keys, so old
	// peers and stored payloads stay byte-identical.
	for name, v := range map[string]any{
		"deploy":   agentprotocol.DeployPayload{},
		"result":   agentprotocol.DeployResult{},
		"desired":  agentprotocol.DesiredService{},
		"observed": agentprotocol.ObservedService{},
		"replica":  agentprotocol.ObservedReplica{},
		"stats":    agentprotocol.ContainerStats{},
		"logs":     agentprotocol.LogsSubscribe{},
		"scale":    agentprotocol.ScaleServicePayload{},
		"exec":     agentprotocol.ExecOpen{},
		"fs":       agentprotocol.FSRequest{},
	} {
		raw, err := json.Marshal(v)
		require.NoError(t, err, name)
		var m map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &m), name)
		require.NotContains(t, m, "processes", name)
		require.NotContains(t, m, "process", name)
	}
}

func TestProcessSpecRoundTrip(t *testing.T) {
	in := agentprotocol.DeployPayload{
		Replicas: 2, Strategy: agentprotocol.StrategyRolling,
		Processes: []agentprotocol.ProcessSpec{
			{Name: "web", Default: true, Replicas: 2, Strategy: agentprotocol.StrategyRolling, Port: 3000, StopGraceSeconds: 30, Hosts: []string{"a.example.com"}},
			{Name: "clock", Replicas: 1, Strategy: agentprotocol.StrategyRecreate, StartCommand: []string{"app", "clock"}, StopGraceSeconds: 5, Singleton: true},
		},
	}
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	var out agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Equal(t, in.Processes, out.Processes)
	require.Contains(t, string(raw), `"processes":[`)
	require.NotContains(t, string(raw), `"default":false`)
}
