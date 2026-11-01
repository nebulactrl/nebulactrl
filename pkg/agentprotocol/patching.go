package agentprotocol

// Server patching: pending operating system updates, reboot-required, the
// package upgrade and the reboot. The agent runs everything on the host the way
// host hardening does: directly when it is installed on the host, through a
// short-lived privileged helper container (nsenter into PID 1's namespaces)
// when it runs in a container. Supported package managers are apt (Debian,
// Ubuntu) and dnf (the RHEL family).

// KindHostPatching checks the host for pending updates, upgrades its packages
// or schedules its reboot. It carries no secrets.
const KindHostPatching = "host_patching"

func init() { AllowedKinds[KindHostPatching] = true }

// CapHostPatching is advertised in Facts.Capabilities by agents that run
// host_patching commands. A server that has never connected has no facts yet
// and is assumed to enroll with a current agent.
const CapHostPatching = "host.patching"

// Patching actions (PatchingPayload.Action).
const (
	// PatchingActionCheck refreshes the package lists and reports the pending
	// updates and whether a reboot is required. It changes no package.
	PatchingActionCheck = "check"
	// PatchingActionApply upgrades the packages (security ones only when
	// asked), keeps the existing configuration files, then checks again.
	PatchingActionApply = "apply"
	// PatchingActionReboot schedules a reboot of the host a few seconds ahead
	// so the agent can still answer, and reports the boot id it will replace.
	PatchingActionReboot = "reboot"
)

// Patching triggers (PatchingPayload.Trigger).
const (
	PatchingTriggerScheduled = "scheduled"
	PatchingTriggerManual    = "manual"
)

// Package managers (PatchState.Manager).
const (
	PatchManagerAPT = "apt"
	PatchManagerDNF = "dnf"
)

// PatchingPayload is the persisted payload of a host_patching command.
type PatchingPayload struct {
	RunID   string `json:"runId"`
	Action  string `json:"action"`
	Trigger string `json:"trigger"`
	// SecurityOnly limits an apply to security updates.
	SecurityOnly bool `json:"securityOnly,omitempty"`
}

// PatchPackage is one pending package upgrade.
type PatchPackage struct {
	Name string `json:"name"`
	// From is the installed version (empty when the manager does not say).
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Security is true when the update comes from a security repository.
	Security bool `json:"security,omitempty"`
}

// PatchState is what a check found on the host.
type PatchState struct {
	// Manager is "apt", "dnf", or empty when the host has neither.
	Manager string `json:"manager"`
	// Supported is false when the host has no supported package manager.
	Supported bool `json:"supported"`
	// Updates counts the pending upgrades, Security the ones from a security
	// repository (a subset of Updates).
	Updates  int `json:"updates"`
	Security int `json:"security"`
	// Packages lists the pending upgrades, at most MaxPatchPackages, security first.
	Packages []PatchPackage `json:"packages,omitempty"`
	// RebootRequired is true when the host asks for a reboot
	// (/var/run/reboot-required, needs-restarting -r, or a newer kernel installed).
	RebootRequired bool `json:"rebootRequired"`
	// RebootPackages are the packages that asked for it, when the host says.
	RebootPackages []string `json:"rebootPackages,omitempty"`
	// BootID is /proc/sys/kernel/random/boot_id; it changes with every boot.
	BootID string `json:"bootId,omitempty"`
	// Message is a caution from the check, for example that the package lists
	// could not be refreshed and are the last ones the host downloaded.
	Message string `json:"message,omitempty"`
}

// MaxPatchPackages bounds PatchState.Packages.
const MaxPatchPackages = 200

// PatchingResult is ResultFrame.Data of a host_patching command.
type PatchingResult struct {
	Action string `json:"action"`
	// Mode is how the agent reached the host: "host" or "helper".
	Mode  string     `json:"mode"`
	State PatchState `json:"state"`
	// ExitCode is the upgrade's exit status (apply only); non-zero means it failed.
	ExitCode int `json:"exitCode,omitempty"`
	// Log is the tail of what the upgrade printed (apply only), bounded.
	Log     string `json:"log,omitempty"`
	Message string `json:"message,omitempty"`
}
