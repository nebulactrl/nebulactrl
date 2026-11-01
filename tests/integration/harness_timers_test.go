//go:build integration

package integration

import (
	"fmt"
	"os"

	"github.com/nebulactrl/nebulactrl/internal/config"
)

// fastTimers are the product timers the suite shortens, by environment
// variable (docs/operations/configuration.md, "Advanced timers"). Production
// defaults stay as they are; every value here only shortens a wait a test would
// otherwise sit out in real time, and stays longer than the interval at which the
// condition is re-checked (the mesh grace is re-checked every 5 s by the test,
// the replica poll runs every 30 s). The same variables in the environment of
// `tests/run.sh` win over this table, e.g. NEBULACTRL_MESH_LINK_GRACE=10m for the
// real timing.
//
//	NEBULACTRL_MESH_LINK_GRACE       10m -> 30s  (TestMeshCrossServer: grant of a removed reference)
//	NEBULACTRL_REPLICA_BROKEN_AFTER   2m -> 60s  (TestMeshCrossServer: a replica that cannot reconnect)
//	NEBULACTRL_UPTIME_INTERVAL       30s -> 2s   (TestUptime*: outage and recovery in seconds; the harness agent probes at the same period)
var fastTimers = map[string]string{
	"NEBULACTRL_MESH_LINK_GRACE":      "30s",
	"NEBULACTRL_REPLICA_BROKEN_AFTER": "60s",
	"NEBULACTRL_UPTIME_INTERVAL":      "2s",
}

// harnessTimers returns the timer overrides of every control plane the harness
// starts: the environment first (set a variable to its production value to test
// the real timing), the short value from fastTimers otherwise.
func harnessTimers() config.Timers {
	t, err := config.LoadTimers(func(k string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return fastTimers[k]
	})
	if err != nil {
		panic(fmt.Sprintf("integration: timer overrides: %v", err))
	}
	return t
}
