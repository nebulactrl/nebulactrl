package agentprotocol

// CapRestartPolicy is advertised in Facts.Capabilities by agents that apply
// DeployPayload.RestartPolicy. The control plane refuses to deploy a service
// with a non-default restart policy to a server whose facts lack it (409
// agent_too_old); a server that has never connected has no facts and is
// assumed to enroll with a current agent (same rule as CapProcesses).
const CapRestartPolicy = "service.restart_policy"

// DeployPayload.RestartPolicy values. An empty policy is RestartAlways.
const (
	RestartAlways    = "always"
	RestartOnFailure = "on-failure"
	RestartNever     = "never"
)

// DefaultRestartMaxRetries is the on-failure retry cap when none is set.
const DefaultRestartMaxRetries = 10
