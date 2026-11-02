package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func tb(host, svc string, at time.Time, requests int64) agentprotocol.TrafficBucket {
	return agentprotocol.TrafficBucket{Host: host, ServiceID: svc, Bucket: at, Requests: requests, S2xx: requests}
}

func TestTrafficBucketsComeBackOldestFirstWithHostAndServiceTiebreak(t *testing.T) {
	s, _ := openTemp(t)
	base := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	require.NoError(t, s.PutTraffic([]agentprotocol.TrafficBucket{
		tb("b.example", "s1", base.Add(time.Minute), 3),
		tb("b.example", "s1", base, 1),
		tb("a.example", "s2", base.Add(time.Minute), 4),
		tb("a.example", "s1", base.Add(time.Minute), 5),
	}, time.Time{}))

	keys, got, err := s.PendingTraffic(10)
	require.NoError(t, err)
	require.Len(t, keys, 4)
	var order []string
	for _, b := range got {
		order = append(order, b.Bucket.Format("15:04")+"/"+b.Host+"/"+b.ServiceID)
	}
	assert.Equal(t, []string{"12:00/b.example/s1", "12:01/a.example/s1", "12:01/a.example/s2", "12:01/b.example/s1"}, order)
	assert.Equal(t, int64(1), got[0].Requests, "the stored payload round-trips")
	assert.Equal(t, int64(1), got[0].S2xx)
}

func TestPutTrafficSameMinuteHostAndServiceReplaces(t *testing.T) {
	s, _ := openTemp(t)
	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	require.NoError(t, s.PutTraffic([]agentprotocol.TrafficBucket{tb("h", "s", at, 1)}, time.Time{}))
	require.NoError(t, s.PutTraffic([]agentprotocol.TrafficBucket{tb("h", "s", at, 9)}, time.Time{}))
	_, got, err := s.PendingTraffic(10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, int64(9), got[0].Requests)
}

func TestTrafficFlushedThroughIsRecordedWithTheBuckets(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)

	_, ok := s.TrafficFlushedThrough()
	assert.False(t, ok, "nothing flushed yet")

	through := time.Date(2026, 10, 29, 12, 5, 0, 0, time.UTC)
	require.NoError(t, s.PutTraffic([]agentprotocol.TrafficBucket{tb("h", "s", through, 1)}, through))
	// A later write without a cursor leaves the cursor where it was.
	require.NoError(t, s.PutTraffic(nil, time.Time{}))
	require.NoError(t, s.Close())

	s, err = Open(dir)
	require.NoError(t, err)
	defer s.Close()
	got, ok := s.TrafficFlushedThrough()
	require.True(t, ok)
	assert.True(t, through.Equal(got), "got %s", got)
	assert.Equal(t, time.UTC, got.Location())
}

func TestTrafficFlushedThroughIgnoresACorruptCursor(t *testing.T) {
	s, _ := openTemp(t)
	require.NoError(t, s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTrafficMeta).Put(keyFlushedThrough, []byte{1, 2, 3})
	}))
	_, ok := s.TrafficFlushedThrough()
	assert.False(t, ok)
}

func TestPendingTrafficHonoursLimitAndDeleteForgets(t *testing.T) {
	s, _ := openTemp(t)
	base := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	var in []agentprotocol.TrafficBucket
	for i := range 5 {
		in = append(in, tb("h", "s", base.Add(time.Duration(i)*time.Minute), int64(i)))
	}
	require.NoError(t, s.PutTraffic(in, time.Time{}))

	keys, got, err := s.PendingTraffic(2)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	assert.Equal(t, int64(0), got[0].Requests)
	assert.Equal(t, int64(1), got[1].Requests)

	require.NoError(t, s.DeleteTraffic(keys))
	_, got, err = s.PendingTraffic(10)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, int64(2), got[0].Requests, "delivered buckets are gone, the rest keeps its order")

	require.NoError(t, s.DeleteTraffic(keys), "deleting twice is harmless")
	require.NoError(t, s.DeleteTraffic(nil))

	keys, _, err = s.PendingTraffic(0)
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestPendingTrafficSkipsUnreadableEntries(t *testing.T) {
	s, _ := openTemp(t)
	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	require.NoError(t, s.PutTraffic([]agentprotocol.TrafficBucket{tb("h", "s", at.Add(time.Minute), 2)}, time.Time{}))
	require.NoError(t, s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTrafficPending).Put(trafficKey(tb("bad", "s", at, 0)), []byte("{nope"))
	}))
	keys, got, err := s.PendingTraffic(10)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "h", got[0].Host)
}

func TestPruneTrafficDropsOlderThanCutoffIncludingUnreadable(t *testing.T) {
	s, _ := openTemp(t)
	cutoff := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	require.NoError(t, s.PutTraffic([]agentprotocol.TrafficBucket{
		tb("h", "s", cutoff.Add(-time.Minute), 1),
		tb("h", "s", cutoff, 2), // exactly at the cutoff stays
		tb("h", "s", cutoff.Add(time.Hour), 3),
	}, time.Time{}))
	require.NoError(t, s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTrafficPending).Put(trafficKey(tb("bad", "", cutoff.Add(-time.Hour), 0)), []byte("garbage"))
	}))

	n, err := s.PruneTraffic(cutoff)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "the stale bucket and the corrupt entry")
	_, got, err := s.PendingTraffic(10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, int64(2), got[0].Requests)
	assert.Equal(t, int64(3), got[1].Requests)

	n, err = s.PruneTraffic(cutoff)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestKeyTimeOfAShortKeyIsZero(t *testing.T) {
	assert.True(t, keyTime([]byte{1, 2}).IsZero())
	assert.True(t, probeKeyTime([]byte{1, 2}).IsZero())
}

func TestTrafficSaltIsStablePerDayAndKeepsYesterdayOnly(t *testing.T) {
	s, _ := openTemp(t)
	calls := 0
	gen := func(v byte) func() ([]byte, error) {
		return func() ([]byte, error) { calls++; return []byte{v}, nil }
	}

	a, err := s.TrafficSalt("2026-10-27", gen(1))
	require.NoError(t, err)
	assert.Equal(t, []byte{1}, a)
	again, err := s.TrafficSalt("2026-10-27", gen(9))
	require.NoError(t, err)
	assert.Equal(t, []byte{1}, again, "the day's salt is generated once")
	assert.Equal(t, 1, calls)

	_, err = s.TrafficSalt("2026-10-28", gen(2))
	require.NoError(t, err)
	// Yesterday's salt survives for late lines.
	got, err := s.TrafficSalt("2026-10-27", gen(9))
	require.NoError(t, err)
	assert.Equal(t, []byte{1}, got)

	_, err = s.TrafficSalt("2026-10-29", gen(3))
	require.NoError(t, err)
	// 10-27 is now older than yesterday (10-28) and was deleted: asking again generates a new one.
	regen, err := s.TrafficSalt("2026-10-27", gen(7))
	require.NoError(t, err)
	assert.Equal(t, []byte{7}, regen)
	kept, err := s.TrafficSalt("2026-10-28", gen(8))
	require.NoError(t, err)
	assert.Equal(t, []byte{2}, kept)
}

func TestTrafficSaltGeneratorErrorStoresNothing(t *testing.T) {
	s, _ := openTemp(t)
	_, err := s.TrafficSalt("2026-10-29", func() ([]byte, error) { return nil, assert.AnError })
	require.ErrorIs(t, err, assert.AnError)
	got, err := s.TrafficSalt("2026-10-29", func() ([]byte, error) { return []byte{5}, nil })
	require.NoError(t, err)
	assert.Equal(t, []byte{5}, got)
}

func TestTrafficSaltWithUnparsableDayDoesNotPruneOthers(t *testing.T) {
	s, _ := openTemp(t)
	_, err := s.TrafficSalt("2026-10-20", func() ([]byte, error) { return []byte{1}, nil })
	require.NoError(t, err)
	_, err = s.TrafficSalt("not-a-day", func() ([]byte, error) { return []byte{2}, nil })
	require.NoError(t, err)
	got, err := s.TrafficSalt("2026-10-20", func() ([]byte, error) { return []byte{9}, nil })
	require.NoError(t, err)
	assert.Equal(t, []byte{1}, got)
}
