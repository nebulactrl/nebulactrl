package agentprotocol

// KindUpdateControlPlane is sent to the agent on the control plane's own host
// (the server the installer enrolled, servers.is_control_plane). The agent
// downloads the installer and its minisign signature, refuses it unless the
// signature verifies under the release key compiled into the agent, and starts it detached
// from the agent and from the control plane's container as a transient systemd
// unit (`systemd-run`), so it survives both being restarted. The result says
// only that the installer started (or why it did not): the outcome of the
// update is observed by the control plane when the new version starts.
//
// The command carries no secrets. The installer reads the installation's own
// settings (install.conf, .env) on the host.
const KindUpdateControlPlane = "update_control_plane"

func init() { AllowedKinds[KindUpdateControlPlane] = true }

// CapUpdateControlPlane is advertised in Facts.Capabilities by agents that run
// update_control_plane commands: Linux hosts running systemd.
const CapUpdateControlPlane = "controlplane.update"

// UpdateControlPlanePayload is the persisted payload of an update_control_plane
// command.
type UpdateControlPlanePayload struct {
	// Version is the release to install ("0.1.2"): it is passed to the
	// installer as --update-to, which installs it for this run without pinning it.
	Version string `json:"version"`
	// InstallerURL is where the installer is downloaded from: https only, no
	// credentials in the URL. The signature is at InstallerURL + ".minisig".
	InstallerURL string `json:"installerUrl"`
}

// UpdateControlPlaneResult is the Data of a succeeded update_control_plane
// result: the installer runs as the systemd unit Unit.
type UpdateControlPlaneResult struct {
	// Unit is the transient unit name; `journalctl -u <Unit>` shows the installer's output.
	Unit    string `json:"unit"`
	Message string `json:"message,omitempty"`
}
