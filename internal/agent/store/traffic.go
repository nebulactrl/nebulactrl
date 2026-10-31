package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var (
	bucketTrafficPending = []byte("traffic_pending")
	bucketTrafficMeta    = []byte("traffic_meta")
	bucketTrafficSalt    = []byte("traffic_salt")

	keyFlushedThrough = []byte("flushed_through")
)

// trafficKey orders pending buckets by minute, then host and service.
func trafficKey(b agentprotocol.TrafficBucket) []byte {
	k := make([]byte, 8, 8+len(b.Host)+1+len(b.ServiceID))
	binary.BigEndian.PutUint64(k, uint64(b.Bucket.Unix()))
	k = append(k, b.Host...)
	k = append(k, 0)
	return append(k, b.ServiceID...)
}

func keyTime(k []byte) time.Time {
	if len(k) < 8 {
		return time.Time{}
	}
	return time.Unix(int64(binary.BigEndian.Uint64(k[:8])), 0).UTC()
}

// PutTraffic stores completed minutes for delivery and records flushedThrough:
// the start of the latest minute that is now durable. Both happen in one
// transaction, so a restart never loses or re-reads a minute.
func (s *Store) PutTraffic(buckets []agentprotocol.TrafficBucket, flushedThrough time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bucketTrafficPending)
		for _, b := range buckets {
			data, err := json.Marshal(b)
			if err != nil {
				return err
			}
			if err := pb.Put(trafficKey(b), data); err != nil {
				return err
			}
		}
		if flushedThrough.IsZero() {
			return nil
		}
		v := make([]byte, 8)
		binary.BigEndian.PutUint64(v, uint64(flushedThrough.Unix()))
		return tx.Bucket(bucketTrafficMeta).Put(keyFlushedThrough, v)
	})
}

// TrafficFlushedThrough returns the start of the latest minute already stored.
func (s *Store) TrafficFlushedThrough() (time.Time, bool) {
	var t time.Time
	var ok bool
	_ = s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketTrafficMeta).Get(keyFlushedThrough)
		if len(v) == 8 {
			t, ok = time.Unix(int64(binary.BigEndian.Uint64(v)), 0).UTC(), true
		}
		return nil
	})
	return t, ok
}

// PendingTraffic returns up to limit unsent buckets, oldest first, with their keys.
func (s *Store) PendingTraffic(limit int) (keys [][]byte, buckets []agentprotocol.TrafficBucket, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketTrafficPending).Cursor()
		for k, v := c.First(); k != nil && len(keys) < limit; k, v = c.Next() {
			var b agentprotocol.TrafficBucket
			if json.Unmarshal(v, &b) != nil {
				continue // unreadable entries are pruned with their age
			}
			keys = append(keys, append([]byte(nil), k...))
			buckets = append(buckets, b)
		}
		return nil
	})
	return keys, buckets, err
}

// DeleteTraffic forgets delivered buckets.
func (s *Store) DeleteTraffic(keys [][]byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bucketTrafficPending)
		for _, k := range keys {
			if err := pb.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// PruneTraffic drops unsent buckets older than cutoff (the 24 h bound).
func (s *Store) PruneTraffic(cutoff time.Time) (int, error) {
	n := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bucketTrafficPending)
		var drop [][]byte
		_ = pb.ForEach(func(k, _ []byte) error {
			if keyTime(k).Before(cutoff) {
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

// TrafficSalt returns the visitor-hash salt for a UTC day (YYYY-MM-DD),
// creating it with gen on first use. Salts of days before the previous one are
// deleted, so yesterday's salt survives only for late lines.
func (s *Store) TrafficSalt(day string, gen func() ([]byte, error)) ([]byte, error) {
	var out []byte
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketTrafficSalt)
		if v := b.Get([]byte(day)); v != nil {
			out = append([]byte(nil), v...)
			return nil
		}
		salt, err := gen()
		if err != nil {
			return err
		}
		if err := b.Put([]byte(day), salt); err != nil {
			return err
		}
		if t, perr := time.Parse("2006-01-02", day); perr == nil {
			keep := []byte(t.AddDate(0, 0, -1).Format("2006-01-02"))
			var drop [][]byte
			_ = b.ForEach(func(k, _ []byte) error {
				if bytes.Compare(k, keep) < 0 {
					drop = append(drop, append([]byte(nil), k...))
				}
				return nil
			})
			for _, k := range drop {
				_ = b.Delete(k)
			}
		}
		out = salt
		return nil
	})
	return out, err
}
