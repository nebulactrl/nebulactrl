package catalog

// The nebula.toml project file. validate, plan, apply and export are one
// composite, sync_config (action plan includes the validation):
// they add the plan id, the stage-only rule and the production guard. The
// binding of an environment to a repository file carries `auto_apply`, which
// switches on staging from a git push; an agent neither reads nor sets it.
const whyConfigSource = "Binds an environment to a nebula.toml in a repository and decides whether a push stages it (auto-apply). That automation is a decision for a person in the web UI or the CLI; an agent neither reads nor changes it."

// configGenericEntries are the allowed project file operations without a curated tool.
var configGenericEntries = map[string]Generic{
	"getServiceConfigSource":     rd(tConfig),
	"releaseServiceConfigFields": st(tConfig),
}

// configDenyEntries are the project file operations never callable over MCP.
var configDenyEntries = map[string]Deny{
	"validateProjectConfig": {Reason: ReasonSuperseded, Why: "sync_config with action plan returns the same diagnostics, and the plan, in one call."},
	"planProjectConfig":     {Reason: ReasonSuperseded, Why: "sync_config with action plan returns the plan with masked values and the plan id that action apply requires."},
	"applyProjectConfig":    {Reason: ReasonSuperseded, Why: "sync_config with action apply only stages (never deploys, never prunes), requires the plan id and asks for approval before immediate changes in production."},
	"exportProjectConfig":   {Reason: ReasonSuperseded, Why: "sync_config with action export returns the file as text with its warnings; it is the starting point for editing."},

	"getEnvironmentConfigSource":    {Reason: ReasonInstanceAdmin, Why: whyConfigSource},
	"updateEnvironmentConfigSource": {Reason: ReasonInstanceAdmin, Why: whyConfigSource},
	"deleteEnvironmentConfigSource": {Reason: ReasonInstanceAdmin, Why: whyConfigSource},
	"checkEnvironmentConfigSource":  {Reason: ReasonInstanceAdmin, Why: whyConfigSource},
}
