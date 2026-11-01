package selfupgrade

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// stateFile is the agent's memory of its last self-upgrade, in the state dir.
const stateFile = "upgrade.json"

// state is the persisted upgrade record (upgrade.json). State uses the
// agentprotocol.Upgrade* values.
type state struct {
	State string `json:"state"`
	From  string `json:"from"`
	To    string `json:"to"`
	// Exe is the replaced binary, Prev the copy of the binary it replaced.
	Exe  string `json:"exe"`
	Prev string `json:"prev"`
	// Starts counts the starts of the new binary that did not reach "connected"
	// (UpgradeSwapped only).
	Starts  int       `json:"starts"`
	At      time.Time `json:"at"`
	Message string    `json:"message,omitempty"`
	// FailedVersion is the version that was rolled back at FailedAt: it is not
	// installed again without Force, so a bad release cannot loop.
	FailedVersion string    `json:"failedVersion,omitempty"`
	FailedAt      time.Time `json:"failedAt,omitempty"`
}

func (c *Controller) statePath() string { return filepath.Join(c.StateDir, stateFile) }

// load reads the record; a missing file is the zero state.
func (c *Controller) load() (state, error) {
	b, err := os.ReadFile(c.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return state{}, nil
	}
	if err != nil {
		return state{}, err
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		// A torn or foreign file must not block the agent: start fresh.
		return state{}, nil //nolint:nilerr // a torn or foreign state file is discarded in favor of a fresh record so a bad file cannot keep the agent from starting
	}
	return st, nil
}

// save writes the record atomically (temp file, fsync, rename).
func (c *Controller) save(st state) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.StateDir, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(c.statePath(), b, 0o600)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

// Report is what travels in Facts: nil until the agent upgraded itself once.
func (c *Controller) Report() *agentprotocol.UpgradeReport {
	st, err := c.load()
	if err != nil || st.State == "" {
		return nil
	}
	return &agentprotocol.UpgradeReport{State: st.State, From: st.From, To: st.To, Message: st.Message, At: st.At}
}
