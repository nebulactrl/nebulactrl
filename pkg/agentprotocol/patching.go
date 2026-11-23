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

// CapHostPatchingAuto is advertised by agents that understand the scheduled
// apply of automatic updates: held packages, the disk preflight and the
// hand-over of the host's own automatic updates. A scheduled apply is never sent
// to an agent without it (an older agent would ignore the holds and upgrade
// Docker).
const CapHostPatchingAuto = "host.patching.auto"

// Patching actions (PatchingPayload.Action).
const (
	// PatchingActionCheck refreshes the package lists and reports the pending
	// updates and whether a reboot is required. It changes no package.
	PatchingActionCheck = "check"
	// PatchingActionApply upgrades the packages (security ones only when
	// asked), keeps the existing configuration files, then checks again. A
	// scheduled apply (trigger scheduled) also leaves the held packages alone and
	// skips when the disk is nearly full.
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

// Native automatic updates (PatchingPayload.Native, PatchingResult.NativeApplied):
// what to do with the host's own mechanism (unattended-upgrades, dnf-automatic).
const (
	// PatchNativeDisable switches it off so the schedule is the only one patching:
	// an apt drop-in with APT::Periodic::Unattended-Upgrade "0", or the dnf
	// automatic timers disabled (remembered in the state directory).
	PatchNativeDisable = "disable"
	// PatchNativeRestore undoes PatchNativeDisable: the drop-in is removed and the
	// timers it disabled are enabled again.
	PatchNativeRestore = "restore"
)

// Native automatic update mechanisms found active (PatchState.NativeAutoUpdates).
const (
	PatchNativeUnattendedUpgrades = "unattended-upgrades"
	PatchNativeDNFAutomatic       = "dnf-automatic"
)

// MaxPatchHolds bounds PatchingPayload.HoldPackages.
const MaxPatchHolds = 20

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
	// HoldPackages are package names a scheduled apply leaves alone, extra to the
	// agent's built-in holds (Docker and containerd packages). Ignored by manual
	// applies, which upgrade what the person reviewed in the plan.
	HoldPackages []string `json:"holdPackages,omitempty"`
	// Native asks a check or apply to first switch the host's own automatic updates
	// off (PatchNativeDisable) or back on (PatchNativeRestore). Empty: leave them.
	Native string `json:"native,omitempty"`
	// CheckContainers asks a scheduled apply to report the restart policies of the
	// running containers, so the control plane can decide on a reboot.
	CheckContainers bool `json:"checkContainers,omitempty"`
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
	// NativeAutoUpdates is the host's own automatic update mechanism that is
	// active ("unattended-upgrades", "dnf-automatic"), empty when none is.
	NativeAutoUpdates string `json:"nativeAutoUpdates,omitempty"`
}

// PatchContainers is the restart policy of the containers running on the host
// before a scheduled reboot: they only come back by themselves when every one of
// them restarts "always" or "unless-stopped".
type PatchContainers struct {
	// Known is false when the agent could not read them (no Docker CLI on the host,
	// the daemon did not answer); Reason says why.
	Known  bool   `json:"known"`
	Reason string `json:"reason,omitempty"`
	// Running counts the running containers; Unsafe names those whose restart
	// policy is neither "always" nor "unless-stopped" (at most 20).
	Running int      `json:"running,omitempty"`
	Unsafe  []string `json:"unsafe,omitempty"`
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
	// NativeApplied echoes PatchingPayload.Native once the host's own automatic
	// updates were switched; empty when nothing was asked or it did not work.
	NativeApplied string `json:"nativeApplied,omitempty"`
	// Containers is set on a scheduled apply that asked for it.
	Containers *PatchContainers `json:"containers,omitempty"`
}
