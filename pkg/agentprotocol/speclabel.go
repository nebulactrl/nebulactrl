package agentprotocol

import "encoding/json"

// SpecLabel is the non-secret runtime spec stored on every managed container
// (LabelSpec). It lets the agent add replicas on its own, when a reconcile
// frame says more are wanted than run, without the deploy command, and lets a
// control plane adopt the containers of an earlier instance.
type SpecLabel struct {
	Slug             string          `json:"slug"`
	Port             int             `json:"port,omitempty"`
	Healthcheck      HealthcheckSpec `json:"healthcheck"`
	StopGraceSeconds int             `json:"stopGraceSeconds,omitempty"`
	// RestartPolicy and RestartMaxRetries are the service's restart policy
	// (replicas keep it).
	RestartPolicy     string `json:"restartPolicy,omitempty"`
	RestartMaxRetries int    `json:"restartMaxRetries,omitempty"`
	MemoryMB          int    `json:"memoryMb,omitempty"`
	// Docker holds the allow-listed container options (replicas keep them).
	Docker *DockerOptions `json:"docker,omitempty"`
	// DockerPolicy is the server policy the options were validated against.
	DockerPolicy *DockerPolicy `json:"dockerPolicy,omitempty"`
	CPUs         float64       `json:"cpus,omitempty"`
	// MeshAlias is the exposed-service alias (nc-svc-<id8>): a replica added
	// later carries it too.
	MeshAlias string `json:"meshAlias,omitempty"`
	// Network membership: a replica added later must join the same networks
	// as its siblings.
	BlockEgress         bool                      `json:"blockEgress,omitempty"`
	EnvHasEgressBlocked bool                      `json:"envHasEgressBlocked,omitempty"`
	SharedNetworks      []SharedNetworkAttachment `json:"sharedNetworks,omitempty"`
	// Process names the process of a multi-process deployment this container
	// runs ("" for a single-process service). The projected fields above
	// (Port, Healthcheck, StopGraceSeconds, MemoryMB, CPUs) are then the
	// process' own, so the label rebuilds that process alone.
	Process        string `json:"process,omitempty"`
	ProcessDefault bool   `json:"processDefault,omitempty"`
	Singleton      bool   `json:"singleton,omitempty"`
	// Image is the reference the deployment ran (a tag, repo@digest, or the
	// nc-build/ tag of a git build). The container itself is created from the
	// image ID, and an image can carry several tags (an old registry address,
	// another project's tag): a control plane that adopts the container takes
	// this one. Empty on containers of older agents.
	Image string `json:"image,omitempty"`
}

// ParseSpecLabel decodes a LabelSpec value. ok is false when it is not valid
// JSON or names no slug; the decoded fields are still returned for valid JSON.
func ParseSpecLabel(raw string) (sl SpecLabel, ok bool) {
	if err := json.Unmarshal([]byte(raw), &sl); err != nil {
		return SpecLabel{}, false
	}
	return sl, sl.Slug != ""
}
