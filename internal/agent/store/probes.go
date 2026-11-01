package store

import (
	"encoding/binary"
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var bucketProbesPending = []byte("probes_pending")

// probeKey orders pending probe results by time (nanoseconds), then service.
func probeKey(r agentprotocol.ProbeResult) []byte {
	k := make([]byte, 8, 8+len(r.ServiceID))
	binary.BigEndian.PutUint64(k, uint64(r.At.UnixNano()))
	return append(k, r.ServiceID...)
}

// probeKeyTime is the probe time encoded in a pending key.
func probeKeyTime(k []byte) time.Time {
	if len(k) < 8 {
		return time.Time{}
	}
	return time.Unix(0, int64(binary.BigEndian.Uint64(k[:8]))).UTC()
}

// PutProbes stores probe results for delivery. A result of the same service
// and instant replaces the earlier one.
func (s *Store) PutProbes(results []agentprotocol.ProbeResult) error {
	if len(results) == 0 {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bucketProbesPending)
		for _, r := range results {
			data, err := json.Marshal(r)
			if err != nil {
				return err
			}
			if err := pb.Put(probeKey(r), data); err != nil {
				return err
			}
		}
		return nil
	})
}

// PendingProbes returns up to limit unsent results, oldest first, with their keys.
func (s *Store) PendingProbes(limit int) (keys [][]byte, results []agentprotocol.ProbeResult, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketProbesPending).Cursor()
		for k, v := c.First(); k != nil && len(keys) < limit; k, v = c.Next() {
			var r agentprotocol.ProbeResult
			if json.Unmarshal(v, &r) != nil {
				continue // unreadable entries are pruned with their age
			}
			keys = append(keys, append([]byte(nil), k...))
			results = append(results, r)
		}
		return nil
	})
	return keys, results, err
}

// DeleteProbes forgets delivered results.
func (s *Store) DeleteProbes(keys [][]byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bucketProbesPending)
		for _, k := range keys {
			if err := pb.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// PruneProbes drops unsent results older than cutoff (the retention bound),
// unreadable entries included, and reports how many it dropped.
func (s *Store) PruneProbes(cutoff time.Time) (int, error) {
	n := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bucketProbesPending)
		var drop [][]byte
		_ = pb.ForEach(func(k, _ []byte) error {
			if probeKeyTime(k).Before(cutoff) {
				drop = append(drop, append([]byte(nil), k...))
			}
			return nil
		})
		for _, k := range drop {
			if err := pb.Delete(k); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
