package agentprotocol

import "time"

// KindAgentUpgrade replaces the agent's own binary with the one its control
// plane serves, then restarts the agent (systemd or the container restart
// policy brings it back). The command carries no secrets: the binary is fetched
// from the control plane the agent is already connected to, and checked against
// the SHA-256 in this (authenticated) command.
const KindAgentUpgrade = "agent_upgrade"

func init() { AllowedKinds[KindAgentUpgrade] = true }

// CapAgentUpgrade is advertised in Facts.Capabilities by agents that run
// agent_upgrade commands and can replace their own binary (Linux, and the
// directory of the running binary is writable). Older agents are upgraded once
// by hand (re-run install-agent.sh).
const CapAgentUpgrade = "agent.upgrade"

// UpgradePayload is the persisted payload of an agent_upgrade command.
type UpgradePayload struct {
	// Version is the control plane's version, which the new binary must report
	// from `--version`.
	Version string `json:"version"`
	// SHA256 is the lower-case hex digest the downloaded binary must have.
	SHA256 string `json:"sha256"`
	// Size is the expected size in bytes (0: not checked).
	Size int64 `json:"size,omitempty"`
	// Force lets the agent retry a version whose earlier start it rolled back.
	Force bool `json:"force,omitempty"`
}

// UpgradeResult is the Data of a succeeded agent_upgrade result: the binary is
// in place and the agent restarts as soon as its other commands are done.
type UpgradeResult struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Message string `json:"message,omitempty"`
}

// Upgrade report states (UpgradeReport.State).
const (
	// UpgradeSwapped: the new binary is installed and has not proven itself yet.
	UpgradeSwapped = "swapped"
	// UpgradeCommitted: the new binary started and stayed connected.
	UpgradeCommitted = "committed"
	// UpgradeRolledBack: the new binary did not start or connect and the
	// previous one was put back.
	UpgradeRolledBack = "rolled_back"
	// UpgradeAbandoned: the swap never took effect (the container was recreated,
	// or the binary was replaced by hand).
	UpgradeAbandoned = "abandoned"
)

// UpgradeReport is what the agent remembers about its last self-upgrade. It
// travels in Facts so the control plane learns about a rollback it was not
// connected for.
type UpgradeReport struct {
	State string `json:"state"`
	From  string `json:"from"`
	To    string `json:"to"`
	// Message explains a rollback or an abandoned swap.
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at"`
}
