package engine

import "github.com/nebulactrl/nebulactrl/pkg/agentprotocol"

// dockerRestartPolicy maps a deploy payload's restart policy to the Docker
// restart policy name and its retry cap. The empty policy and "always" keep the
// historical behaviour (unless-stopped); an unknown value is treated as
// "always" rather than leaving the service without any restart. The cap only
// exists for on-failure, where 0 means agentprotocol.DefaultRestartMaxRetries.
func dockerRestartPolicy(policy string, maxRetries int) (name string, retries int) {
	switch policy {
	case agentprotocol.RestartNever:
		return "no", 0
	case agentprotocol.RestartOnFailure:
		if maxRetries <= 0 {
			maxRetries = agentprotocol.DefaultRestartMaxRetries
		}
		return "on-failure", maxRetries
	default:
		return "unless-stopped", 0
	}
}
