// Package store is the agent's durable local state, backed by bbolt at
// <state-dir>/agent.db (mode 0600).
//
// Buckets:
//
//	identity  – the enrolled agent identity and Ed25519 private key
//	commands  – every command ID ever received, with its stored result once finished
//	results   – results the control plane has not yet acknowledged (result_ack)
//	routes    – the last route table applied to Traefik, plus proxy settings (ACME email, admin secret)
//	traffic_* – unsent traffic minutes, the access-log cursor and the daily visitor salt
//	probes_pending – unacknowledged uptime probe results
package store

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var (
	bucketIdentity = []byte("identity")
	bucketCommands = []byte("commands")
	bucketResults  = []byte("results")
	bucketRoutes   = []byte("routes")

	keyIdentity = []byte("self")
	keyRoutes   = []byte("table")
)

// FileName is the database file inside the state directory.
const FileName = "agent.db"

// Identity is the agent's enrolled identity.
type Identity struct {
	AgentID    string             `json:"agentId"`
	ServerID   string             `json:"serverId"`
	PrivateKey ed25519.PrivateKey `json:"privateKey"`
	// Revoked is set when the control plane closed the socket with 4401. A revoked
	// identity is replaced the next time the agent starts with an enrollment token.
	Revoked bool `json:"revoked,omitempty"`
}

// Store is the agent's bbolt database.
type Store struct {
	db *bolt.DB
}

// Open opens (creating if needed) <stateDir>/agent.db with mode 0600.
func Open(stateDir string) (*Store, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	return OpenFile(filepath.Join(stateDir, FileName))
}

// OpenFile opens the database at an explicit path.
func OpenFile(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketIdentity, bucketCommands, bucketResults, bucketRoutes, bucketTrafficPending, bucketTrafficMeta, bucketTrafficSalt, bucketProbesPending} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init buckets: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Identity returns the stored identity, or ok=false when the agent has not enrolled.
func (s *Store) Identity() (id Identity, ok bool, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketIdentity).Get(keyIdentity)
		if v == nil {
			return nil
		}
		ok = true
		return json.Unmarshal(v, &id)
	})
	return id, ok, err
}

// PutIdentity stores (replacing) the agent identity.
func (s *Store) PutIdentity(id Identity) error {
	data, err := json.Marshal(id)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketIdentity).Put(keyIdentity, data)
	})
}

// commandRecord is the value stored in the commands bucket.
type commandRecord struct {
	ReceivedAt time.Time                  `json:"receivedAt"`
	Result     *agentprotocol.ResultFrame `json:"result,omitempty"`
}

// SeenCommand reports whether a command ID was received before. stored is the
// recorded result when the command finished, nil when it was received but has
// no result yet.
func (s *Store) SeenCommand(id string) (stored *agentprotocol.ResultFrame, seen bool) {
	_ = s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketCommands).Get([]byte(id))
		if v == nil {
			return nil
		}
		seen = true
		var rec commandRecord
		if err := json.Unmarshal(v, &rec); err == nil {
			stored = rec.Result
		}
		return nil
	})
	return stored, seen
}

// MarkCommand records receipt of a command. It is a no-op for known IDs.
func (s *Store) MarkCommand(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketCommands)
		if b.Get([]byte(id)) != nil {
			return nil
		}
		data, err := json.Marshal(commandRecord{ReceivedAt: time.Now().UTC()})
		if err != nil {
			return err
		}
		return b.Put([]byte(id), data)
	})
}

// PutResult records a command result: it is kept in the command record (for
// redelivery dedupe) and in the unacked results bucket until AckResult.
func (s *Store) PutResult(res agentprotocol.ResultFrame) error {
	if res.CommandID == "" {
		return errors.New("result has no command id")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		cb := tx.Bucket(bucketCommands)
		rec := commandRecord{ReceivedAt: time.Now().UTC()}
		if v := cb.Get([]byte(res.CommandID)); v != nil {
			_ = json.Unmarshal(v, &rec)
		}
		rec.Result = &res
		data, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		if err := cb.Put([]byte(res.CommandID), data); err != nil {
			return err
		}
		rdata, err := json.Marshal(res)
		if err != nil {
			return err
		}
		return tx.Bucket(bucketResults).Put([]byte(res.CommandID), rdata)
	})
}

// UnackedResults returns results not yet acknowledged by the control plane.
func (s *Store) UnackedResults() ([]agentprotocol.ResultFrame, error) {
	out := []agentprotocol.ResultFrame{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketResults).ForEach(func(_, v []byte) error {
			var r agentprotocol.ResultFrame
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			out = append(out, r)
			return nil
		})
	})
	return out, err
}

// AckResult forgets an unacked result. The command record (and its result) is
// kept so a late redelivery of the same command is still deduplicated.
func (s *Store) AckResult(commandID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketResults).Delete([]byte(commandID))
	})
}

// PruneCommands deletes command records received before cutoff that have no
// pending unacked result.
func (s *Store) PruneCommands(cutoff time.Time) (int, error) {
	n := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		cb := tx.Bucket(bucketCommands)
		rb := tx.Bucket(bucketResults)
		var drop [][]byte
		err := cb.ForEach(func(k, v []byte) error {
			var rec commandRecord
			if json.Unmarshal(v, &rec) != nil || rec.ReceivedAt.Before(cutoff) {
				if rb.Get(k) == nil {
					drop = append(drop, append([]byte(nil), k...))
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, k := range drop {
			if err := cb.Delete(k); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// SaveRoutes persists the last-applied route table.
func (s *Store) SaveRoutes(routes []agentprotocol.Route) error {
	data, err := json.Marshal(routes)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRoutes).Put(keyRoutes, data)
	})
}

// LoadRoutes returns the persisted route table (nil when none was saved).
func (s *Store) LoadRoutes() ([]agentprotocol.Route, error) {
	var routes []agentprotocol.Route
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketRoutes).Get(keyRoutes)
		if v == nil {
			return nil
		}
		return json.Unmarshal(v, &routes)
	})
	return routes, err
}

var keyTableSeq = []byte("table_seq")

// SaveTableSeq persists the number of the newest whole route table applied.
func (s *Store) SaveTableSeq(n uint64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRoutes).Put(keyTableSeq, []byte(strconv.FormatUint(n, 10)))
	})
}

// LoadTableSeq returns the persisted number (0 when none was saved).
func (s *Store) LoadTableSeq() (uint64, error) {
	var n uint64
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketRoutes).Get(keyTableSeq)
		if len(v) == 0 {
			return nil
		}
		var err error
		n, err = strconv.ParseUint(string(v), 10, 64)
		return err
	})
	return n, err
}

var (
	keyACMEEmail        = []byte("acme_email")
	keyTrafficAnalytics = []byte("traffic_analytics")
)

// SaveTrafficAnalytics persists whether Traefik access logging was last applied on.
func (s *Store) SaveTrafficAnalytics(enabled bool) error {
	v := []byte{'0'}
	if enabled {
		v = []byte{'1'}
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRoutes).Put(keyTrafficAnalytics, v)
	})
}

// LoadTrafficAnalytics returns the persisted setting; ok is false when none was saved.
func (s *Store) LoadTrafficAnalytics() (enabled, ok bool, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketRoutes).Get(keyTrafficAnalytics)
		if len(v) == 0 {
			return nil
		}
		ok, enabled = true, v[0] == '1'
		return nil
	})
	return enabled, ok, err
}

// SaveACMEEmail persists the ACME contact email last pushed with the routes.
func (s *Store) SaveACMEEmail(email string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRoutes).Put(keyACMEEmail, []byte(email))
	})
}

// LoadACMEEmail returns the persisted ACME contact email ("" when none).
func (s *Store) LoadACMEEmail() (string, error) {
	var email string
	err := s.db.View(func(tx *bolt.Tx) error {
		email = string(tx.Bucket(bucketRoutes).Get(keyACMEEmail))
		return nil
	})
	return email, err
}
