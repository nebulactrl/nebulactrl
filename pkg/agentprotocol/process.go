package agentprotocol

import (
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// Processes. One service can run several processes from one build, one image and
// one release: the service's own runtime fields are the default process, the
// others are named. A multi-process deploy is one DeployPayload with Processes;
// a payload without them is the legacy single-process deploy.

// CapProcesses is advertised in Facts.Capabilities by agents that deploy
// DeployPayload.Processes. The control plane refuses a multi-process deploy to a
// server whose facts lack it (422 agent_too_old). A server that has never
// connected has no facts and is assumed to enroll with a current agent (same
// rule as CapCompose).
const CapProcesses = "service.processes"

const (
	// LabelProcess is set on EVERY container of a multi-process deployment
	// (default process included, value = its name). Containers of
	// single-process deployments and of releases older than this feature carry
	// no label; "" means "the default process" everywhere.
	LabelProcess = "nebulactrl.process"
	// MaxProcesses bounds the processes of one service, the default included.
	MaxProcesses = 10
	// DefaultProcessName names the default process unless the service says otherwise.
	DefaultProcessName = "web"
)

// ProcessNamePattern is the shape of a process name: DNS-label-safe, 1..31
// characters (it becomes part of container names and of a DNS alias). It is
// identical to services.ProcessNamePattern.
var ProcessNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,29}[a-z0-9])?$`)

// ProcessSpec is one process of a multi-process DeployPayload.
type ProcessSpec struct {
	Name    string `json:"name"`
	Default bool   `json:"default,omitempty"` // exactly one entry
	// Replicas is already clamped by the control plane (1..10; 1 for a singleton or with volumes).
	Replicas         int             `json:"replicas"`
	Strategy         string          `json:"strategy"` // StrategyRolling | StrategyRecreate (per process)
	Port             int             `json:"port,omitempty"`
	StartCommand     []string        `json:"startCommand,omitempty"`
	Healthcheck      HealthcheckSpec `json:"healthcheck"`
	StopGraceSeconds int             `json:"stopGraceSeconds"` // effective (> 0)
	Singleton        bool            `json:"singleton,omitempty"`
	MemoryMB         int             `json:"memoryMb,omitempty"`
	CPUs             float64         `json:"cpus,omitempty"`
	Hosts            []string        `json:"hosts,omitempty"` // empty = no route
	DomainRules      []DomainRule    `json:"domainRules,omitempty"`
	OriginLock       *OriginLock     `json:"originLock,omitempty"`
}

// ProcessResult is the outcome of one process of a multi-process deploy
// (DeployResult.Processes). Outcome is succeeded, failed, skipped (never
// started because an earlier process failed) or rolled_back.
type ProcessResult struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"` // succeeded | failed | skipped | rolled_back
	Message string `json:"message,omitempty"`
}

// DesiredProcess is one process of a DesiredService (Reconcile frame).
type DesiredProcess struct {
	Name           string    `json:"name"`
	Default        bool      `json:"default,omitempty"`
	Replicas       int       `json:"replicas"`
	ContainerNames []string  `json:"containerNames"`
	ScaledAt       time.Time `json:"scaledAt,omitzero"`
}

// ObservedProcess is the per-process aggregate of an ObservedService.
type ObservedProcess struct {
	Name            string `json:"name"`
	State           string `json:"state"`
	Health          string `json:"health"`
	ReplicasReady   int    `json:"replicasReady"`
	ReplicasDesired int    `json:"replicasDesired"`
}

// ProcessContainerName names a replica container of a process. The DEFAULT
// process keeps today's name so routes, route services and old containers keep
// working: nc-<slug>-<deploymentId8>-<replica>. A named process is
// nc-<slug>-<deploymentId8>-<process>-<replica>. Replica counts from 0 within
// the process.
func ProcessContainerName(slug, deploymentID, process string, isDefault bool, replica int) string {
	if isDefault {
		return ContainerName(slug, deploymentID, replica)
	}
	return ContainerPrefix + slug + "-" + Short(deploymentID) + "-" + process + "-" + strconv.Itoa(replica)
}

// ProcessContainerNames returns the container names of replicas 0..n-1 of a
// process (n < 1 counts as 1).
func ProcessContainerNames(slug, deploymentID, process string, isDefault bool, n int) []string {
	n = max(n, 1)
	names := make([]string, n)
	for i := range names {
		names[i] = ProcessContainerName(slug, deploymentID, process, isDefault, i)
	}
	return names
}

// ProcessRouteID is the Route.ServiceID of the http route of a NAMED process: a
// UUID derived from the service ID and the process name. Traefik objects are
// named after Route.ServiceID and one service ID carries only one http route,
// so every routed named process gets its own ID (Route.Stack then names the
// owning service, as for the routes of a compose stack). The default process
// routes under the service's own ID.
func ProcessRouteID(serviceID, process string) string {
	ns, err := uuid.Parse(serviceID)
	if err != nil {
		ns = uuid.NameSpaceOID
	}
	return uuid.NewSHA1(ns, []byte("process:"+process)).String()
}

// ProcessAlias is the DNS alias of a process' containers on every network they
// join, next to the service alias the default process carries: the default
// process is the slug itself, a named process "<process>.<slug>".
func ProcessAlias(slug, process string, isDefault bool) string {
	if isDefault {
		return slug
	}
	return process + "." + slug
}
