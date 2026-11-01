package agentprotocol

import "math"

// Network isolation. App, task and tailnet containers never join the shared
// EdgeNetwork; each environment has its own network (EnvNetwork). A service that
// blocks outbound internet joins only the internal (no-egress) network of its
// environment, EnvInternalNetwork; when any service of the environment blocks
// egress, every other service also joins the internal network with the same
// alias so it can still reach the blocked one. Team shared networks
// (SharedNetworkName) are joined per service with an explicit alias.
// Traefik, the pages server and cloudflared stay on EdgeNetwork; Traefik joins
// the networks of its routes (Route.Networks).
const (
	// EnvInternalSuffix is appended to EnvNetwork for the internal network.
	EnvInternalSuffix = "-int"
	// SharedNetworkPrefix prefixes team shared network names.
	SharedNetworkPrefix = "nebulactrl-shared-"
	// LegacyEnvNetworkPrefix and LegacySharedNetworkPrefix are the names the
	// managed networks had before they were renamed to sort after EdgeNetwork.
	// Nothing creates them any more; the agent's sweep removes the empty ones.
	LegacyEnvNetworkPrefix    = "nc-env-"
	LegacySharedNetworkPrefix = "nc-shared-"
)

// SharedNetworkAttachment attaches a service to a team shared network.
type SharedNetworkAttachment struct {
	Name  string `json:"name"`  // docker network name nebulactrl-shared-<id8>
	Alias string `json:"alias"` // <slug>.<env-slug>.<project-slug>
}

// SharedNetworkName returns nebulactrl-shared-<id8> for a shared network ID.
func SharedNetworkName(id string) string { return SharedNetworkPrefix + Short(id) }

// EnvInternalNetwork returns nebulactrl-env-<environmentId8>-int, the internal
// (no outbound internet) network of an environment.
func EnvInternalNetwork(envID string) string { return EnvNetwork(envID) + EnvInternalSuffix }

// EdgeGwPriority is the gateway priority of the edge network on the containers
// that stay on it (Traefik, the pages server, cloudflared). Docker re-binds a
// container's published ports when its default gateway endpoint changes, so
// the edge network must remain the gateway while those containers join and
// leave environment networks. Engine 28 (API 1.48) honours the priority;
// older daemons pick by network name, and every managed network name sorts
// after EdgeNetwork.
const EdgeGwPriority = math.MaxInt32
