package agentprotocol

import (
	"regexp"
	"strconv"

	"github.com/google/uuid"
)

// Compose stacks. A service of kind "stack" is one Docker Compose project: the
// agent runs `docker compose` for it with a generated override (see
// pkg/composestack) instead of creating containers itself. The stack is one
// service for deploys, releases, staged changes, rollback and generations; its
// compose services are "components" (observed replicas, one metrics replica
// each, one log source each).

// CapCompose is advertised in Facts.Capabilities by agents that can run
// `docker compose` (the plugin is installed and answers `docker compose
// version`). The control plane refuses stack deploys on servers without it.
const CapCompose = "compose"

// Labels and names of stack containers. Every container of a stack carries the
// managed labels of a service container (LabelService = the stack's service
// ID, LabelDeployment, LabelRelease, LabelEnvironment) plus LabelComponent and
// LabelReplica (the component's index in StackSpec.Components).
const (
	// LabelComponent is the compose service name of a stack container.
	LabelComponent = "nebulactrl.component"
	// StackProjectPrefix starts the compose project name: nc-<serviceId8>.
	StackProjectPrefix = "nc-"
	// StackDirName is the directory (below the agent state dir) holding the
	// compose files of each stack: <state-dir>/stacks/<serviceId8>/.
	StackDirName = "stacks"
)

// ComponentNamePattern is what a compose service name must match to run as a
// stack component: it becomes a container name, a DNS alias and an upstream
// host.
var ComponentNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// StackProject returns the compose project name of a stack: nc-<serviceId8>.
func StackProject(serviceID string) string { return StackProjectPrefix + Short(serviceID) }

// StackContainerName returns the fixed container name of a component:
// nc-<serviceId8>-<component>. It is also a DNS name on the environment
// network, so routes use it as the upstream host.
func StackContainerName(serviceID, component string) string {
	return StackProject(serviceID) + "-" + component
}

// StackRouteID returns the Route.ServiceID of one http route of a stack: a
// UUID derived from the stack's service ID, the component and the container
// port. Traefik objects are named after Route.ServiceID and one service ID may
// carry only one http route, so every routed (component, port) gets its own ID
// (and Route.Stack names the stack it belongs to).
func StackRouteID(serviceID, component string, port int) string {
	ns, err := uuid.Parse(serviceID)
	if err != nil {
		ns = uuid.NameSpaceOID
	}
	return uuid.NewSHA1(ns, []byte("component:"+component+":"+strconv.Itoa(port))).String()
}

// StackComponent is one compose service of a stack.
type StackComponent struct {
	Name string `json:"name"`
	// Index is the component's position in StackSpec.Components (sorted by
	// name); it is the metrics replica index and the LabelReplica value.
	Index int `json:"index"`
	// Image is the image reference as written in the compose file ("" for a
	// service that is only built, which stacks do not support).
	Image string `json:"image,omitempty"`
	// Ports are the container ports the component declares (ports target and
	// expose), sorted; domains may point at any port.
	Ports []int `json:"ports,omitempty"`
	// Profiles are the compose profiles of the component; a component with
	// profiles is not started by a default deploy.
	Profiles []string `json:"profiles,omitempty"`
}

// StackResources are per-component resource limits that the override applies
// when the compose file sets none of its own (deploy.resources.limits).
type StackResources struct {
	MemoryMB int     `json:"memoryMb,omitempty"`
	CPUs     float64 `json:"cpus,omitempty"`
}

// StackSpec is DeployPayload.Stack: what the agent needs to run a compose
// stack. It never contains variable values; compose interpolation reads the
// service's variables from Command.Secrets ("env:<KEY>") at run time.
type StackSpec struct {
	// Compose is the compose file as written by the user.
	Compose string `json:"compose"`
	// Components lists the stack's services, sorted by name, with their
	// indexes. The agent re-derives them from the normalised compose model and
	// refuses a deploy whose set differs.
	Components []StackComponent `json:"components"`
	// Unrestricted is true when the server's "Unrestricted" switch is on: the
	// allow-list is not applied (see composestack.Check).
	Unrestricted bool `json:"unrestricted,omitempty"`
	// Resources are per-component limits (key = component name).
	Resources map[string]StackResources `json:"resources,omitempty"`
	// Images pins components to image references for a rollback or a
	// reconcile redeploy: component name -> repo@sha256:digest. Empty for a
	// fresh deploy, which pulls what the compose file says.
	Images map[string]string `json:"images,omitempty"`
	// Routes are this stack's http routes, one per (component, port) with
	// domains, upstream "<StackContainerName>:<port>". Each Route.ServiceID is
	// StackRouteID and Route.Stack is the stack's service ID.
	Routes []Route `json:"routes,omitempty"`
	// WaitSeconds bounds `docker compose up --wait` (default 300, max 3600).
	WaitSeconds int `json:"waitSeconds,omitempty"`
}

// DefaultStackWaitSeconds is StackSpec.WaitSeconds when it is zero.
const DefaultStackWaitSeconds = 300

// StackResult is DeployResult.Stack: what the agent learned from a stack deploy.
type StackResult struct {
	// Images is the resolved image per component, repo@sha256:digest (the
	// local image ID when the image has no repo digest). The control plane
	// records it in the release so a rollback runs the same images.
	Images map[string]string `json:"images,omitempty"`
	// Components is the observed state of each component right after the deploy.
	Components []ObservedReplica `json:"components,omitempty"`
	// Notes are warnings about the compose file (ports that were not
	// published, profiles that did not start, ...).
	Notes []string `json:"notes,omitempty"`
}
