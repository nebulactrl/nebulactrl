package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func probe(svc string, at time.Time, outcome string) agentprotocol.ProbeResult {
	return agentprotocol.ProbeResult{ServiceID: svc, At: at, Outcome: outcome, Replicas: 1}
}

func TestProbesAreReturnedOldestFirstAndSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	require.NoError(t, err)
	base := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	require.NoError(t, st.PutProbes([]agentprotocol.ProbeResult{
		probe("b", base.Add(30*time.Second), agentprotocol.ProbeUp),
		probe("b", base, agentprotocol.ProbeUp),
		probe("a", base.Add(30*time.Second), agentprotocol.ProbeDown), // same instant as b: service breaks the tie
		probe("c", base.Add(time.Nanosecond), agentprotocol.ProbePaused),
	}))
	require.NoError(t, st.Close())

	st, err = Open(dir)
	require.NoError(t, err)
	defer st.Close()
	keys, got, err := st.PendingProbes(10)
	require.NoError(t, err)
	require.Len(t, keys, 4)
	var order []string
	for _, r := range got {
		order = append(order, r.ServiceID+"@"+r.At.Format("15:04:05.000000000"))
	}
	require.Equal(t, []string{"b@12:00:00.000000000", "c@12:00:00.000000001", "a@12:00:30.000000000", "b@12:00:30.000000000"}, order)
}

func TestPendingProbesHonoursTheLimitAndDeleteForgets(t *testing.T) {
	st, err := Open(t.TempDir())
	require.NoError(t, err)
	defer st.Close()
	base := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	var all []agentprotocol.ProbeResult
	for i := range 5 {
		all = append(all, probe("a", base.Add(time.Duration(i)*time.Second), agentprotocol.ProbeUp))
	}
	require.NoError(t, st.PutProbes(all))

	keys, first, err := st.PendingProbes(3)
	require.NoError(t, err)
	require.Equal(t, all[:3], first)
	require.NoError(t, st.DeleteProbes(keys))

	_, rest, err := st.PendingProbes(10)
	require.NoError(t, err)
	require.Equal(t, all[3:], rest, "only the delivered page is gone")
}

func TestPutProbesOfTheSameServiceAndInstantReplaces(t *testing.T) {
	st, err := Open(t.TempDir())
	require.NoError(t, err)
	defer st.Close()
	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	require.NoError(t, st.PutProbes([]agentprotocol.ProbeResult{probe("a", at, agentprotocol.ProbeUp)}))
	require.NoError(t, st.PutProbes([]agentprotocol.ProbeResult{probe("a", at, agentprotocol.ProbeDown)}))
	require.NoError(t, st.PutProbes(nil))

	_, got, err := st.PendingProbes(10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, agentprotocol.ProbeDown, got[0].Outcome)
}

func TestPruneProbesDropsOlderThanTheCutoffAndUnreadableEntries(t *testing.T) {
	st, err := Open(t.TempDir())
	require.NoError(t, err)
	defer st.Close()
	cutoff := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	require.NoError(t, st.PutProbes([]agentprotocol.ProbeResult{
		probe("a", cutoff.Add(-time.Nanosecond), agentprotocol.ProbeUp),
		probe("a", cutoff, agentprotocol.ProbeUp), // exactly at the cutoff stays
		probe("a", cutoff.Add(time.Hour), agentprotocol.ProbeUp),
	}))
	// A corrupt entry from long ago is pruned with its age, not delivered.
	require.NoError(t, st.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketProbesPending).Put(probeKey(probe("z", cutoff.Add(-time.Hour), "")), []byte("{not json"))
	}))

	_, got, err := st.PendingProbes(10)
	require.NoError(t, err)
	require.Len(t, got, 3, "the corrupt entry is skipped")

	n, err := st.PruneProbes(cutoff)
	require.NoError(t, err)
	require.Equal(t, 2, n, "the stale result and the corrupt entry")

	_, got, err = st.PendingProbes(10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, []time.Time{cutoff, cutoff.Add(time.Hour)}, []time.Time{got[0].At, got[1].At})
}
