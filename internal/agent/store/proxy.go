package store

import (
	"encoding/json"

	bolt "go.etcd.io/bbolt"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var (
	keyProxyAdminSecret = []byte("proxy_admin_secret")
	keyProxyConfig      = []byte("proxy_config")
)

// SaveProxyAdminSecret persists the secret that guards the proxy's admin entrypoint.
func (s *Store) SaveProxyAdminSecret(secret string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRoutes).Put(keyProxyAdminSecret, []byte(secret))
	})
}

// LoadProxyAdminSecret returns the persisted proxy admin secret ("" when none).
func (s *Store) LoadProxyAdminSecret() (string, error) {
	var secret string
	err := s.db.View(func(tx *bolt.Tx) error {
		secret = string(tx.Bucket(bucketRoutes).Get(keyProxyAdminSecret))
		return nil
	})
	return secret, err
}

// SaveProxyConfig persists the server-level proxy configuration (uploaded
// certificates including their resolved private keys, upstream policy,
// tailnet spec) next to the route table, so the proxy converges after a
// restart while the control plane is unreachable.
func (s *Store) SaveProxyConfig(cfg agentprotocol.ProxyConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRoutes).Put(keyProxyConfig, data)
	})
}

// LoadProxyConfig returns the persisted proxy configuration; ok is false when
// none was saved.
func (s *Store) LoadProxyConfig() (cfg agentprotocol.ProxyConfig, ok bool, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketRoutes).Get(keyProxyConfig)
		if v == nil {
			return nil
		}
		ok = true
		return json.Unmarshal(v, &cfg)
	})
	return cfg, ok, err
}
