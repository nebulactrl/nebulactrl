package agentprotocol

// DeployPayload.RestartPolicy values. An empty policy is RestartAlways.
const (
	RestartAlways    = "always"
	RestartOnFailure = "on-failure"
	RestartNever     = "never"
)

// DefaultRestartMaxRetries is the on-failure retry cap when none is set.
const DefaultRestartMaxRetries = 10
