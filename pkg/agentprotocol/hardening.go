package agentprotocol

// Host hardening: a Lynis audit of the server itself and a curated set of
// safe remediations (see internal/lynis for the catalog). The agent runs
// everything on the host: directly when it is installed on the host, through
// a short-lived privileged helper container (nsenter into PID 1's namespaces,
// the mechanism the host shell uses) when it runs in a container.

// KindHostHardening audits the host with Lynis, applies or rolls back
// remediations, and reports the parsed result. It carries no secrets.
const KindHostHardening = "host_hardening"

func init() { AllowedKinds[KindHostHardening] = true }

// CapHostHardening is advertised in Facts.Capabilities by agents that run
// host_hardening commands. A server that has never connected has no facts yet
// and is assumed to enroll with a current agent.
const CapHostHardening = "host.hardening"

// Hardening actions (HardeningPayload.Action).
const (
	// HardeningActionAudit installs Lynis when asked to, runs an audit and
	// reports the state of every fix.
	HardeningActionAudit = "audit"
	// HardeningActionApply applies the selected fixes (the default set when
	// none is selected), then audits again so the new index is known.
	HardeningActionApply = "apply"
	// HardeningActionRollback undoes the selected fixes, then audits again.
	HardeningActionRollback = "rollback"
)

// Hardening triggers (HardeningPayload.Trigger).
const (
	HardeningTriggerScheduled = "scheduled"
	HardeningTriggerManual    = "manual"
)

// Fix states (HardeningFixState.State).
const (
	// HardeningFixApplied: NebulaCtrl applied the fix and it still holds.
	HardeningFixApplied = "applied"
	// HardeningFixCompliant: the host already satisfies the fix; nothing to do.
	HardeningFixCompliant = "compliant"
	// HardeningFixPending: the fix would change something.
	HardeningFixPending = "pending"
	// HardeningFixUnavailable: the fix does not apply to this host (the message
	// says why: a container, no systemd, not enough memory ...).
	HardeningFixUnavailable = "unavailable"
)

// Fix outcomes of an apply or rollback (HardeningFixOutcome.Outcome).
const (
	HardeningOutcomeApplied    = "applied"
	HardeningOutcomeUnchanged  = "unchanged"
	HardeningOutcomeSkipped    = "skipped"
	HardeningOutcomeFailed     = "failed"
	HardeningOutcomeRolledBack = "rolled_back"
)

// HardeningPayload is the persisted payload of a host_hardening command.
type HardeningPayload struct {
	RunID   string `json:"runId"`
	Action  string `json:"action"`
	Trigger string `json:"trigger"`
	// FixIDs selects the fixes of an apply or rollback. An apply without ids
	// applies every default fix; a rollback needs ids.
	FixIDs []string `json:"fixIds,omitempty"`
	// InstallLynis lets an audit install Lynis from the distribution's package
	// repositories (or CISOfy's) when it is missing. Scheduled runs never do.
	InstallLynis bool `json:"installLynis,omitempty"`
}

// HardeningDetail is one detail line of a Lynis finding: for KRNL-6000 a sysctl
// key with its current and preferred value, for SSH-7408 an sshd option.
type HardeningDetail struct {
	Description string `json:"description,omitempty"`
	Field       string `json:"field"`
	Preferred   string `json:"preferred,omitempty"`
	Value       string `json:"value,omitempty"`
}

// HardeningFinding is one warning or suggestion of a Lynis report.
type HardeningFinding struct {
	// ID is the Lynis test ID, for example KRNL-6000.
	ID       string `json:"id"`
	Text     string `json:"text"`
	Details  string `json:"details,omitempty"`
	Solution string `json:"solution,omitempty"`
	// Fields are the per-key details Lynis reported for the test.
	Fields []HardeningDetail `json:"fields,omitempty"`
}

// HardeningReport is what NebulaCtrl keeps of /var/log/lynis-report.dat.
type HardeningReport struct {
	LynisVersion string `json:"lynisVersion"`
	Hostname     string `json:"hostname,omitempty"`
	OS           string `json:"os,omitempty"`
	// HardeningIndex is Lynis's 0..100 score.
	HardeningIndex int `json:"hardeningIndex"`
	TestsDone      int `json:"testsDone"`
	// FinishedAt is the report's end time as Lynis wrote it (host local time,
	// "2006-01-02 15:04:05").
	FinishedAt         string             `json:"finishedAt,omitempty"`
	Warnings           []HardeningFinding `json:"warnings"`
	Suggestions        []HardeningFinding `json:"suggestions"`
	VulnerablePackages int                `json:"vulnerablePackages,omitempty"`
}

// HardeningFixState is the state of one catalog fix on the host.
type HardeningFixState struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

// HardeningFixOutcome is what an apply or rollback did to one fix.
type HardeningFixOutcome struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome"`
	Message string `json:"message,omitempty"`
}

// HardeningResult is ResultFrame.Data of a host_hardening command.
type HardeningResult struct {
	Action string `json:"action"`
	// Mode is how the agent reached the host: "host" (the agent runs there) or
	// "helper" (through a privileged helper container).
	Mode string `json:"mode"`
	// LynisInstalled is false when Lynis is missing; Report is then nil.
	LynisInstalled bool   `json:"lynisInstalled"`
	LynisVersion   string `json:"lynisVersion,omitempty"`
	// LynisInstalledNow is true when this run installed Lynis.
	LynisInstalledNow bool             `json:"lynisInstalledNow,omitempty"`
	Report            *HardeningReport `json:"report,omitempty"`
	// Fixes is the state of every catalog fix after the run.
	Fixes []HardeningFixState `json:"fixes,omitempty"`
	// Outcomes lists what an apply or rollback did, per requested fix.
	Outcomes []HardeningFixOutcome `json:"outcomes,omitempty"`
	// RebootRequired is true when an applied fix only takes effect after a reboot.
	RebootRequired bool   `json:"rebootRequired,omitempty"`
	Message        string `json:"message,omitempty"`
}
