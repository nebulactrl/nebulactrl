package coolify

import "strings"

// Reasons a variable is a secret (PlanEnv.SecretReason).
const (
	SecretLocked    = "locked"
	SecretName      = "name"
	SecretValue     = "value"
	SecretReference = "reference"
	SecretChoice    = "choice"
)

// coolifyInjected are variables Coolify adds to containers itself; they are not
// part of the application's configuration.
func coolifyInjected(key string) bool {
	switch {
	case strings.HasPrefix(key, "COOLIFY_"):
		return true
	case strings.HasPrefix(key, "SERVICE_FQDN_"), strings.HasPrefix(key, "SERVICE_URL_"), strings.HasPrefix(key, "SERVICE_NAME_"):
		return true
	case key == "SOURCE_COMMIT":
		return true
	}
	return false
}
