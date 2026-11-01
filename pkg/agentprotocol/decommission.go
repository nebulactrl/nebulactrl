package agentprotocol

// KindDecommission removes everything NebulaCtrl put on a host and then the
// agent itself: the control plane sends it when a server is removed with the
// "clean" mode. The agent
//
//   - stops and removes every container, network and volume NebulaCtrl
//     manages (they carry the nebulactrl.managed label), the proxy and the
//     helper containers it runs (by their fixed names) and the volumes the
//     agent named itself (nc-vol-*, nebulactrl-*);
//   - leaves Docker, its images and every container, network and volume that
//     is not NebulaCtrl's untouched;
//   - reports the result, then deletes its own identity, its systemd unit, its
//     environment file, its binary and its state directory (what
//     install-agent.sh --uninstall --purge does) and exits for good.
//
// The command is idempotent: a second run finds nothing left to remove.
const KindDecommission = "decommission"

func init() { AllowedKinds[KindDecommission] = true }

// CapDecommission is advertised in Facts.Capabilities by agents that run
// decommission commands. An older agent is not asked to: the control plane
// refuses the clean mode for it (409 agent_cannot_decommission) and the person
// uses the forget mode or upgrades the agent first.
const CapDecommission = "agent.decommission"

// DecommissionPayload is the persisted payload of a decommission command.
type DecommissionPayload struct {
	// ServerID is the server being removed; the agent refuses a command meant
	// for another identity.
	ServerID string `json:"serverId"`
}

// DecommissionStep is one thing a decommission removed (or failed to remove).
type DecommissionStep struct {
	// Kind is container, network, volume or compose.
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Error is empty when the resource is gone.
	Error string `json:"error,omitempty"`
}

// DecommissionResult is the Data of a decommission result.
type DecommissionResult struct {
	Containers int `json:"containers"`
	Networks   int `json:"networks"`
	Volumes    int `json:"volumes"`
	// Failed lists what could not be removed; the outcome is failed when it is
	// not empty, and the control plane keeps the server.
	Failed []DecommissionStep `json:"failed,omitempty"`
	// Message summarizes the run.
	Message string `json:"message,omitempty"`
}
