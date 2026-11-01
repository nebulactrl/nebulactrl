package agents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The stats of a container carry the process it belongs to; the sample stores it
// next to the replica index (which counts within the process). A container of a
// single-process service carries none and stores the empty process.
func TestStatsStoreTheProcessOfEachContainer(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	svcID, _ := h.seedDeployment(e.serverID)
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)

	a.write(agentprotocol.TypeStats, agentprotocol.Stats{Containers: []agentprotocol.ContainerStats{
		{ServiceID: svcID.String(), Replica: 0, Process: "web", CPUPercent: 10, MemBytes: 100, MemLimit: 1000},
		{ServiceID: svcID.String(), Replica: 0, Process: "worker", CPUPercent: 20, MemBytes: 200, MemLimit: 1000},
		{ServiceID: svcID.String(), Replica: 1, Process: "worker", CPUPercent: 30, MemBytes: 300, MemLimit: 1000},
		{ServiceID: svcID.String(), Replica: 0, CPUPercent: 40, MemBytes: 400, MemLimit: 1000},
	}})

	type sample struct {
		process string
		replica int
		cpu     float64
	}
	var got []sample
	require.Eventually(t, func() bool {
		rows, err := h.pool.Query(h.ctx, "SELECT process, replica, cpu_percent FROM metric_samples WHERE service_id = $1 ORDER BY cpu_percent", svcID)
		require.NoError(t, err)
		defer rows.Close()
		got = nil
		for rows.Next() {
			var s sample
			require.NoError(t, rows.Scan(&s.process, &s.replica, &s.cpu))
			got = append(got, s)
		}
		return len(got) == 4
	}, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, []sample{{"web", 0, 10}, {"worker", 0, 20}, {"worker", 1, 30}, {"", 0, 40}}, got)
}
