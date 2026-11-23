package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"

	bolt "go.etcd.io/bbolt"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var (
	bucketAutobanBans   = []byte("autoban_bans")
	bucketAutobanOutbox = []byte("autoban_outbox")
)

// BanKey is the key of an active ban in the autoban_bans bucket: the route id
// and the ban prefix.
func BanKey(routeID, prefix string) []byte {
	return []byte(routeID + "|" + prefix)
}

// BanChange is one atomic change of the ban state: bans to write, bans to
// forget, and the events that tell the control plane about it. Applying them
// in one transaction means a restart never has a ban without its event or an
// event without its ban.
type BanChange struct {
	Put    []agentprotocol.Ban
	Delete []agentprotocol.Ban
	Events []agentprotocol.BanEvent
}

// outboxKey orders events by time (nanoseconds), then a ban before its end
// (so equal clocks never deliver an end first), then event id.
func outboxKey(e agentprotocol.BanEvent) []byte {
	k := make([]byte, 9, 9+len(e.EventID))
	binary.BigEndian.PutUint64(k, uint64(e.At.UnixNano()))
	if e.Kind == agentprotocol.BanEventEnd {
		k[8] = 1
	}
	return append(k, e.EventID...)
}

// ApplyBans stores a BanChange in one transaction. An event with an id that is
// already queued replaces it.
func (s *Store) ApplyBans(c BanChange) error {
	if len(c.Put) == 0 && len(c.Delete) == 0 && len(c.Events) == 0 {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bans := tx.Bucket(bucketAutobanBans)
		for _, b := range c.Delete {
			if err := bans.Delete(BanKey(b.RouteID, b.Prefix)); err != nil {
				return err
			}
		}
		for _, b := range c.Put {
			data, err := json.Marshal(b)
			if err != nil {
				return err
			}
			if err := bans.Put(BanKey(b.RouteID, b.Prefix), data); err != nil {
				return err
			}
		}
		ob := tx.Bucket(bucketAutobanOutbox)
		for _, e := range c.Events {
			data, err := json.Marshal(e)
			if err != nil {
				return err
			}
			if err := ob.Put(outboxKey(e), data); err != nil {
				return err
			}
		}
		return nil
	})
}

// LoadBans returns every stored ban. Unreadable entries are skipped.
func (s *Store) LoadBans() ([]agentprotocol.Ban, error) {
	var out []agentprotocol.Ban
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketAutobanBans).ForEach(func(_, v []byte) error {
			var b agentprotocol.Ban
			if json.Unmarshal(v, &b) == nil {
				out = append(out, b)
			}
			return nil
		})
	})
	return out, err
}

// PendingBanEvents returns up to limit unacknowledged events, oldest first,
// with their keys. A limit of zero or less returns all of them.
func (s *Store) PendingBanEvents(limit int) (keys [][]byte, events []agentprotocol.BanEvent, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketAutobanOutbox).Cursor()
		for k, v := c.First(); k != nil && (limit <= 0 || len(keys) < limit); k, v = c.Next() {
			var e agentprotocol.BanEvent
			if json.Unmarshal(v, &e) != nil {
				continue
			}
			keys = append(keys, bytes.Clone(k))
			events = append(events, e)
		}
		return nil
	})
	return keys, events, err
}

// DeleteBanEvents forgets acknowledged events.
func (s *Store) DeleteBanEvents(keys [][]byte) error {
	if len(keys) == 0 {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		ob := tx.Bucket(bucketAutobanOutbox)
		for _, k := range keys {
			if err := ob.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// TrimBanEvents drops the oldest events beyond keep and returns how many it
// dropped.
func (s *Store) TrimBanEvents(keep int) (int, error) {
	dropped := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		ob := tx.Bucket(bucketAutobanOutbox)
		excess := ob.Stats().KeyN - keep
		if excess <= 0 {
			return nil
		}
		c := ob.Cursor()
		var drop [][]byte
		for k, _ := c.First(); k != nil && len(drop) < excess; k, _ = c.Next() {
			drop = append(drop, bytes.Clone(k))
		}
		for _, k := range drop {
			if err := ob.Delete(k); err != nil {
				return err
			}
			dropped++
		}
		return nil
	})
	return dropped, err
}
