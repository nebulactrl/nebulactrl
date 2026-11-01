// Package agentprotocol defines the versioned wire contract between the
// NebulaCtrl control plane and its per-host agents (ADR 0002).
//
// Every frame is a JSON object carrying an Envelope header ({"v":1,"type":...,"id":...})
// plus a type-specific body. Frames are exchanged over a single agent-initiated
// WebSocket at /agent/v1/connect. The one exception is the payload of instance
// service tunnels (localtunnel.go), which travels in binary WebSocket messages.
package agentprotocol

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// ProtocolVersion is the major protocol version. A peer receiving a frame with
// a different major version closes the socket with CloseUnsupportedVersion.
const ProtocolVersion = 1

// WebSocket close codes used by the protocol.
const (
	CloseUnsupportedVersion = 4400 // unknown major protocol version
	CloseRevoked            = 4401 // agent identity revoked
	CloseAuthFailed         = 4403 // handshake signature or nonce rejected
	CloseReplaced           = 4409 // a newer connection for the same server replaced this one
)

// NonceMaxAge bounds how old a challenge may be when the agent answers it.
const NonceMaxAge = 30 * time.Second

// HeartbeatInterval is how often the agent sends heartbeat frames.
const HeartbeatInterval = 10 * time.Second

// OfflineAfter is how long without a heartbeat before a server is offline and
// its observations are stale.
const OfflineAfter = 30 * time.Second

// Message types.
const (
	// Agent → control plane.
	TypeHello     = "hello"
	TypeAuth      = "auth"
	TypeHeartbeat = "heartbeat"
	TypeAck       = "ack"
	TypeResult    = "result"
	TypeEvent     = "event"
	TypeLog       = "log"
	TypeLogEnd    = "log_end"
	TypeStats     = "stats"
	TypeObserved  = "observed"
	TypeTraffic   = "traffic"

	// Control plane → agent.
	TypeChallenge  = "challenge"
	TypeCommand    = "command"
	TypeResultAck  = "result_ack"
	TypeReconcile  = "reconcile"
	TypeTrafficAck = "traffic_ack"
)

// Command kinds. The agent rejects any kind not in this set.
const (
	KindDeploy          = "deploy"
	KindScaleService    = "scale_service"
	KindRestartService  = "restart_service"
	KindStopService     = "stop_service"
	KindRemoveService   = "remove_service"
	KindSyncRoutes      = "sync_routes"
	KindLogsSubscribe   = "logs_subscribe"
	KindLogsUnsubscribe = "logs_unsubscribe"
)

// AllowedKinds lists every command kind an agent accepts.
var AllowedKinds = map[string]bool{
	KindDeploy: true, KindScaleService: true, KindRestartService: true, KindStopService: true, KindRemoveService: true,
	KindSyncRoutes: true, KindLogsSubscribe: true, KindLogsUnsubscribe: true,
}

// Deploy steps reported via EventFrame.Step.
const (
	StepPull   = "pull"
	StepBuild  = "build"
	StepCreate = "create"
	StepStart  = "start"
	StepHealth = "health"
	StepRoute  = "route"
	StepVerify = "verify"
	StepRetire = "retire"
)

// Step statuses reported via EventFrame.Status.
const (
	StepStarted   = "started"
	StepSucceeded = "succeeded"
	StepFailed    = "failed"
)

// Command outcomes reported via ResultFrame.Outcome.
const (
	OutcomeSucceeded      = "succeeded"
	OutcomeFailed         = "failed"
	OutcomeRolledBack     = "rolled_back"
	OutcomeRollbackFailed = "rollback_failed"
)

// Log streams.
const (
	StreamBuild   = "build"
	StreamDeploy  = "deploy"
	StreamRuntime = "runtime"
	// StreamDebug carries the engine's verbose trace of a deployment (docker
	// calls, probe attempts, route changes, timings). It is stored with the
	// deploy logs but hidden from the default log view and stream.
	StreamDebug = "debug"
)

// Deploy strategies.
const (
	StrategyRolling  = "rolling"
	StrategyRecreate = "recreate"
)

// Proxy status values in Heartbeat.Proxy.Status.
const (
	ProxyOK       = "ok"
	ProxyStarting = "starting"
	ProxyError    = "error"
)

// Observed container states.
const (
	StateRunning    = "running"
	StateExited     = "exited"
	StateRestarting = "restarting"
	StateMissing    = "missing"
	StateCreated    = "created"
)

// Observed health values.
const (
	HealthHealthy   = "healthy"
	HealthUnhealthy = "unhealthy"
	HealthDegraded  = "degraded"
	HealthStarting  = "starting"
	HealthUnknown   = "unknown"
)

// Envelope is the common header of every frame.
type Envelope struct {
	V    int    `json:"v"`
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

// Frame is a raw decoded frame: header plus the full JSON body for typed decoding.
type Frame struct {
	Envelope
	Raw json.RawMessage `json:"-"`
}

// Decode parses the header of a raw frame and keeps the body for later decoding.
func Decode(data []byte) (Frame, error) {
	var f Frame
	if err := json.Unmarshal(data, &f.Envelope); err != nil {
		return f, fmt.Errorf("decode envelope: %w", err)
	}
	f.Raw = data
	return f, nil
}

// Into decodes the frame body into v.
func (f Frame) Into(v any) error { return json.Unmarshal(f.Raw, v) }

// Encode marshals a typed frame, filling the envelope fields.
func Encode(typ, id string, body any) ([]byte, error) {
	m, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(m, &obj); err != nil {
		return nil, err
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	obj["v"], _ = json.Marshal(ProtocolVersion)
	obj["type"], _ = json.Marshal(typ)
	if id != "" {
		obj["id"], _ = json.Marshal(id)
	}
	return json.Marshal(obj)
}

// Facts describe host capabilities reported at enrollment and in hello.
type Facts struct {
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Kernel        string `json:"kernel"`
	CPUs          int    `json:"cpus"`
	MemTotal      uint64 `json:"memTotal"`
	DiskTotal     uint64 `json:"diskTotal"`
	DockerVersion string `json:"dockerVersion"`
	CgroupVersion string `json:"cgroupVersion"`
	Port80Free    bool   `json:"port80Free"`
	Port443Free   bool   `json:"port443Free"`
	// Capabilities lists optional protocol features the agent supports, e.g. CapProxyV2.
	Capabilities []string `json:"capabilities,omitempty"`
	// HostTailscale reports a Tailscale instance running on the host itself.
	HostTailscale *HostTailscale `json:"hostTailscale,omitempty"`
	// ComposeVersion is the `docker compose` plugin version ("" when absent;
	// CapCompose is then missing from Capabilities too).
	ComposeVersion string `json:"composeVersion,omitempty"`
	// Emulation lists the non-native platforms the host can run through
	// binfmt/QEMU (e.g. linux/arm64), used for cross-architecture builds.
	Emulation []string `json:"emulation,omitempty"`
	// HostSysctls are the current values of the host kernel parameters in
	// HostSysctlKeys, so a plan can warn when a service needs more (for example
	// vm.max_map_count for Elasticsearch). Missing keys could not be read.
	HostSysctls map[string]int64 `json:"hostSysctls,omitempty"`
	// TunDevice reports whether /dev/net/tun exists: mesh link nodes (kernel
	// tailscale) need it, server nodes (userspace) do not.
	TunDevice bool `json:"tunDevice,omitempty"`
	// AgentUpgrade is the agent's memory of its last self-upgrade (nil when it
	// never upgraded itself); see UpgradeReport.
	AgentUpgrade *UpgradeReport `json:"agentUpgrade,omitempty"`
}

// EnrollRequest is POSTed to /agent/v1/enroll.
type EnrollRequest struct {
	Token     string `json:"token"`
	PublicKey []byte `json:"publicKey"` // raw Ed25519 public key (base64 in JSON)
	Hostname  string `json:"hostname"`
	Facts     Facts  `json:"facts"`
}

// EnrollResponse is returned from /agent/v1/enroll.
type EnrollResponse struct {
	ServerID string `json:"serverId"`
	AgentID  string `json:"agentId"`
}

// Challenge is the first frame the server sends after the socket opens.
type Challenge struct {
	Nonce      []byte `json:"nonce"`
	ServerTime int64  `json:"serverTime"` // unix nanoseconds
}

// Auth answers a Challenge.
type Auth struct {
	AgentID   string `json:"agentId"`
	Signature []byte `json:"signature"`
}

// SigningPayload returns the bytes the agent signs: nonce || agentId || serverTime (decimal).
func SigningPayload(nonce []byte, agentID string, serverTime int64) []byte {
	out := make([]byte, 0, len(nonce)+len(agentID)+20)
	out = append(out, nonce...)
	out = append(out, agentID...)
	out = fmt.Appendf(out, "%d", serverTime)
	return out
}

// Hello is sent by the agent once authenticated.
type Hello struct {
	AgentVersion    string        `json:"agentVersion"`
	ProtocolVersion int           `json:"protocolVersion"`
	Facts           Facts         `json:"facts"`
	UnackedResults  []ResultFrame `json:"unackedResults"`
}

// ProxyHealth reports the managed Traefik state.
type ProxyHealth struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Ports is the observed port state of the proxy (nil from agents without
	// CapProxyPorts).
	Ports *ProxyPorts `json:"ports,omitempty"`
}

// Heartbeat is sent every HeartbeatInterval.
type Heartbeat struct {
	CPUPercent    float64     `json:"cpuPercent"`
	MemUsed       uint64      `json:"memUsed"`
	MemTotal      uint64      `json:"memTotal"`
	DiskUsed      uint64      `json:"diskUsed"`
	DiskTotal     uint64      `json:"diskTotal"`
	DockerVersion string      `json:"dockerVersion"`
	Proxy         ProxyHealth `json:"proxy"`
	// Tailnet is the observed per-server tailnet state (nil when off).
	Tailnet *TailnetStatus `json:"tailnet,omitempty"`
	// Tunnel is the observed Cloudflare Tunnel connector (nil when the host has none).
	Tunnel *TunnelStatus `json:"tunnel,omitempty"`
	// Mesh is the observed mesh of this server (nil when the agent manages none).
	Mesh *MeshStatus `json:"mesh,omitempty"`
	// AcmeCerts are the observed DNS-01 wildcard and domain certificates
	// (empty when the server has none).
	AcmeCerts []AcmeCertStatus `json:"acmeCerts,omitempty"`
	// Endpoints is the observed state of the instance service listeners (empty
	// from agents without CapInstanceTunnel and while none is requested).
	Endpoints []EndpointStatus `json:"endpoints,omitempty"`
	// Geo is the observed GeoIP database (nil while geolocation is off).
	Geo *GeoStatus `json:"geo,omitempty"`
}

// Ack acknowledges receipt of a command.
type Ack struct {
	CommandID string `json:"commandId"`
}

// ResultFrame reports command completion. Data is kind-specific (DeployResult for deploy).
type ResultFrame struct {
	CommandID string          `json:"commandId"`
	Outcome   string          `json:"outcome"`
	Data      json.RawMessage `json:"data,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// DeployResult is ResultFrame.Data for deploy commands.
type DeployResult struct {
	Outcome     string `json:"outcome"`
	ImageDigest string `json:"imageDigest,omitempty"`
	ContainerID string `json:"containerId,omitempty"`
	Message     string `json:"message,omitempty"`
	// CanceledAt is the deploy step that was running when a canceled deploy
	// (OutcomeCanceled) stopped: pull, build, predeploy, create, start, health,
	// route or verify; "queued" when it never started.
	CanceledAt string `json:"canceledAt,omitempty"`
	// Build describes how a git source was built (nil for image sources and
	// when the build was skipped).
	Build *BuildInfo `json:"build,omitempty"`
	// Tailnet reports the outcome of the tailnet step (nil when the deploy had
	// no tailnet spec or the app deploy ended before the step).
	Tailnet *ServiceTailnetStatus `json:"tailnet,omitempty"`
	// Stack reports a compose stack deploy (nil for ordinary services).
	Stack *StackResult `json:"stack,omitempty"`
	// Processes is the per-process outcome of a multi-process deploy
	// (DeployPayload.Processes); empty for a single-process deploy.
	Processes []ProcessResult `json:"processes,omitempty"`
}

// EventFrame reports deploy step progress.
type EventFrame struct {
	DeploymentID string `json:"deploymentId"`
	Step         string `json:"step"`
	Status       string `json:"status"`
	Message      string `json:"message,omitempty"`
}

// LogFrame carries batched log lines for a deployment or a subscription stream.
type LogFrame struct {
	StreamID     string `json:"streamId,omitempty"`
	DeploymentID string `json:"deploymentId,omitempty"`
	// TaskRunID routes the lines of a run_task container to the task run's log.
	TaskRunID string    `json:"taskRunId,omitempty"`
	ServiceID string    `json:"serviceId,omitempty"`
	Stream    string    `json:"stream"`
	Lines     []LogLine `json:"lines"`
}

// LogLine is one log line.
type LogLine struct {
	TS   time.Time `json:"ts"`
	Text string    `json:"text"`
}

// LogEnd marks the end of a log stream.
type LogEnd struct {
	StreamID     string `json:"streamId,omitempty"`
	DeploymentID string `json:"deploymentId,omitempty"`
	Error        string `json:"error,omitempty"`
}

// ContainerStats is one entry in Stats (one per replica container).
type ContainerStats struct {
	ServiceID  string  `json:"serviceId"`
	Replica    int     `json:"replica"`
	CPUPercent float64 `json:"cpuPercent"`
	MemBytes   uint64  `json:"memBytes"`
	MemLimit   uint64  `json:"memLimit"` // 0 = no memory limit configured
	// Process is the process of a multi-process deployment the container
	// belongs to (LabelProcess; "" otherwise); Replica is then the index
	// within the process.
	Process string `json:"process,omitempty"`
}

// Stats reports per-container resource usage.
type Stats struct {
	Containers []ContainerStats `json:"containers"`
}

// ObservedReplica is the observed state of one replica container.
type ObservedReplica struct {
	Index        int       `json:"index"`
	ContainerID  string    `json:"containerId,omitempty"`
	State        string    `json:"state"`
	Health       string    `json:"health"`
	StartedAt    time.Time `json:"startedAt,omitzero"`
	RestartCount int       `json:"restartCount,omitempty"`
	// Component is the compose service name of a stack component ("" for the
	// replicas of an ordinary service); Index is then the component index.
	Component string `json:"component,omitempty"`
	// Process is the process name in a multi-process deployment (default
	// process included, "" otherwise); Index is then the replica index
	// within the process.
	Process string `json:"process,omitempty"`
}

// ObservedService is the observed runtime state of one managed service.
// State and Health are aggregates over Replicas: State is running if any replica
// runs and missing if none exists; Health is healthy only when every desired
// replica is healthy, unhealthy when none is, otherwise degraded.
type ObservedService struct {
	ServiceID       string            `json:"serviceId"`
	DeploymentID    string            `json:"deploymentId"`
	ContainerID     string            `json:"containerId,omitempty"` // replica 0 (or first running)
	State           string            `json:"state"`
	Health          string            `json:"health"`
	StartedAt       time.Time         `json:"startedAt,omitzero"`
	ImageID         string            `json:"imageId,omitempty"`
	Replicas        []ObservedReplica `json:"replicas,omitempty"`
	ReplicasReady   int               `json:"replicasReady"`
	ReplicasDesired int               `json:"replicasDesired"`
	// Tailnet is the service's tailnet sidecar state (nil when it has none).
	Tailnet *ServiceTailnetStatus `json:"tailnet,omitempty"`
	// Replication is the replication status of a managed Postgres container
	// (nil when the service is not part of a replica group).
	Replication *ReplicationStatus `json:"replication,omitempty"`
	// Processes is the per-process aggregate of a multi-process deployment.
	// State, Health, ReplicasReady and ReplicasDesired above stay aggregates
	// over ALL replicas of ALL processes. Empty for a single-process service.
	Processes []ObservedProcess `json:"processes,omitempty"`
}

// Observed reports observed state for managed services.
type Observed struct {
	Services []ObservedService `json:"services"`
}

// Command is sent from the control plane to the agent. Secrets are attached at
// send time and never persisted by the control plane.
type Command struct {
	ID             string            `json:"id"`
	IdempotencyKey string            `json:"idempotencyKey"`
	Kind           string            `json:"kind"`
	Payload        json.RawMessage   `json:"payload"`
	Secrets        map[string]string `json:"secrets,omitempty"`
	ExpiresAt      time.Time         `json:"expiresAt"`
}

// ResultAck tells the agent it may forget a stored result.
type ResultAck struct {
	CommandID string `json:"commandId"`
}

// Route maps hostnames to the replica upstreams of one service on the edge network.
type Route struct {
	ServiceID string   `json:"serviceId"`
	Hosts     []string `json:"hosts"`
	// Upstreams lists "<containerName>:<port>", one per replica.
	Upstreams []string `json:"upstreams"`
	// HealthPath, when set, enables a Traefik health check on every upstream
	// (the service's http healthcheck path). Empty means no Traefik health check.
	HealthPath string `json:"healthPath,omitempty"`
	// Networks are the networks Traefik must join to reach this route's
	// upstreams: the environment network, or its internal network when the
	// service blocks egress. Traefik leaves networks no route needs.
	Networks []string `json:"networks,omitempty"`
	// Stack is the service ID of the compose stack this route belongs to
	// (empty for every other route). ServiceID is then StackRouteID.
	// For the http route of a NAMED process of a multi-process service, Stack
	// names the owning service too (ServiceID is then ProcessRouteID); the
	// ownership rules are the stack's (ownerOf, ownRoutes).
	Stack string `json:"stack,omitempty"`
	RouteExt
}

// DesiredService is one entry in Reconcile.
type DesiredService struct {
	ServiceID      string   `json:"serviceId"`
	DeploymentID   string   `json:"deploymentId"`
	Replicas       int      `json:"replicas"`
	ContainerNames []string `json:"containerNames"`
	// ScaledAt is when the control plane last changed the service's replica
	// count (the service's last_scale_at; zero when it never did, or from an
	// older control plane). It orders this frame against the scale_service
	// commands the agent ran: a frame built before a scale landed carries the
	// old count and an older ScaledAt, and must not undo that scale.
	ScaledAt time.Time `json:"scaledAt,omitzero"`
	// Processes are the processes of a multi-process deployment; Replicas,
	// ContainerNames and ScaledAt above then mirror the default process.
	// Empty for a single-process service.
	Processes []DesiredProcess `json:"processes,omitempty"`
	Routes    []Route          `json:"routes"`
	// Network membership of the service's replicas (see network.go).
	BlockEgress         bool                      `json:"blockEgress,omitempty"`
	SharedNetworks      []SharedNetworkAttachment `json:"sharedNetworks,omitempty"`
	EnvHasEgressBlocked bool                      `json:"envHasEgressBlocked,omitempty"`
	// Stack marks a compose stack (see stack.go): its containers are named
	// nc-<serviceId8>-<component> and found by the service and deployment
	// labels, so ContainerNames stays empty.
	Stack bool `json:"stack,omitempty"`
	// Fenced marks a database that must never run as a primary again (an old
	// primary after a promote): the agent stops it, sets its restart policy to
	// "no" and refuses to start it on deploy.
	Fenced bool `json:"fenced,omitempty"`
	// Replication tells the agent to poll the database's replication status.
	Replication *ReplicationDesired `json:"replication,omitempty"`
}

// Reconcile carries the full desired set for a server.
type Reconcile struct {
	Services []DesiredService `json:"services"`
	// TableSeq numbers the route table of this frame (see SyncRoutesPayload.TableSeq).
	TableSeq uint64 `json:"tableSeq,omitempty"`
	// Hello marks the frame the control plane sends when the agent connects,
	// as opposed to a later resync. The agent takes TableSeq of a hello frame
	// as its record of the newest table applied, whether that is lower or
	// higher than before: the control plane is the authority, also after its
	// database was restored from a backup (its numbers then go back).
	Hello bool `json:"hello,omitempty"`
	// ACMEEmail is the server's ACME contact email; the agent applies it to the
	// ACME resolver. Empty means no ACME resolver (default certificate only).
	ACMEEmail string `json:"acmeEmail,omitempty"`
	// TrafficAnalytics tells the agent whether to emit Traefik access logging
	// (traffic analytics). Nil leaves the agent's current setting unchanged.
	TrafficAnalytics *bool `json:"trafficAnalytics,omitempty"`
	// ExternalRoutes are the routes of route services (external upstreams,
	// tcp/udp): they have no container, so they are not DesiredServices.
	ExternalRoutes []Route `json:"externalRoutes,omitempty"`
	// Proxy replaces the server-level proxy configuration (nil = unchanged).
	Proxy *ProxyConfig `json:"proxy,omitempty"`
	// Geo is the server's request-origin geolocation setting (nil = unchanged).
	// The MaxMind credentials ride in Secrets (SecretGeoMaxMind* keys).
	Geo *GeoConfig `json:"geo,omitempty"`
	// Logs is the server's log history and drain setting (nil = unchanged).
	// Drain credentials ride in Secrets (LogDrainSecretKey keys).
	Logs *LogsConfig `json:"logs,omitempty"`
	// Mesh is the complete desired mesh set of this server (nil = unchanged).
	// It rides every hello reconcile so an expired sync_mesh command never
	// leaves the server unconverged. Node keys ride in Secrets (SecretMeshKeyPrefix).
	Mesh *SyncMeshPayload `json:"mesh,omitempty"`
	// Uptime is the server's uptime probing setting (nil from an older control
	// plane: probe every service).
	Uptime *UptimeSettings `json:"uptime,omitempty"`
	// Endpoints is the complete set of local instance service listeners the
	// agent should offer (nil = unchanged; empty Listen = none). Only sent to
	// agents that advertise CapInstanceTunnel.
	Endpoints *InstanceEndpoints `json:"endpoints,omitempty"`
	// Secrets holds the send-time secrets of the routes and proxy config
	// (SecretAuthPrefix / SecretCertPrefix keys). Reconcile is never persisted.
	Secrets map[string]string `json:"secrets,omitempty"`
}

// HealthcheckSpec mirrors services.Healthcheck for the agent.
type HealthcheckSpec struct {
	Type               string `json:"type"`
	Path               string `json:"path,omitempty"`
	Port               int    `json:"port,omitempty"`
	IntervalSeconds    int    `json:"intervalSeconds"`
	TimeoutSeconds     int    `json:"timeoutSeconds"`
	StartPeriodSeconds int    `json:"startPeriodSeconds"`
	DeadlineSeconds    int    `json:"deadlineSeconds"`
	SuccessThreshold   int    `json:"successThreshold"`
	// Command is the argv of an exec check (CMD / CMD-SHELL first): the agent
	// installs it as the container HEALTHCHECK and waits for Docker to report healthy.
	Command []string `json:"command,omitempty"`
}

// VolumeMount is a named volume attached to a service.
type VolumeMount struct {
	Name      string `json:"name"` // full docker volume name nc-vol-<serviceId8>-<name>
	MountPath string `json:"mountPath"`
}

// ImageSource describes an image to pull.
type ImageSource struct {
	Ref    string `json:"ref"`              // e.g. traefik/whoami:latest
	Digest string `json:"digest,omitempty"` // repo@sha256:... when known (rollback)
	// HasRegistryAuth indicates Secrets[SecretRegistryAuth] carries base64 auth JSON.
	HasRegistryAuth bool `json:"hasRegistryAuth,omitempty"`
	// BuiltFrom is set when the image was built by a build server and pushed to
	// a registry (Ref is then repo@digest).
	BuiltFrom *BuiltFrom `json:"builtFrom,omitempty"`
}

// GitSource describes a git build.
type GitSource struct {
	URL            string `json:"url"`
	CommitSHA      string `json:"commitSha"`
	DockerfilePath string `json:"dockerfilePath"`
	BuildContext   string `json:"buildContext"`
	// HasToken indicates Secrets[SecretGitToken] carries an HTTPS token.
	HasToken bool `json:"hasToken,omitempty"`
	// GitSSH carries the SSH deploy key flag and the pinned host keys (an ssh:// clone).
	GitSSH
	// ExistingImageID, when set, skips the build (rollback to a built image).
	ExistingImageID string `json:"existingImageId,omitempty"`
	// BuildType selects the build method (BuildAuto, BuildDockerfile,
	// BuildRailpack or "starter:<lang>"); empty means BuildAuto.
	BuildType string `json:"buildType,omitempty"`
	// NoCache builds this one deployment with --no-cache --pull (Dockerfile and
	// Railpack, on the target or on a build server). Needs CapBuildOptions.
	NoCache bool `json:"noCache,omitempty"`
	// Submodules is the submodule mode (SubmodulesAuto, SubmodulesOn,
	// SubmodulesOff); empty means SubmodulesAuto. Needs CapBuildOptions.
	Submodules string `json:"submodules,omitempty"`
}

// Reserved keys in Command.Secrets. Environment variables are attached as
// "env:<KEY>".
const (
	SecretGitToken     = "git_token"
	SecretRegistryAuth = "registry_auth"
	SecretEnvPrefix    = "env:"
)

// DeployPayload is the persisted payload of a deploy command. It never contains
// secret values; variables are attached at send time in Command.Secrets.
type DeployPayload struct {
	// Replicas is the desired replica count, at least 1. Recreate deployments
	// (volumes or no port) always run exactly 1; the control plane enforces it.
	Replicas         int             `json:"replicas"`
	DeploymentID     string          `json:"deploymentId"`
	ReleaseID        string          `json:"releaseId"`
	ServiceID        string          `json:"serviceId"`
	ServiceSlug      string          `json:"serviceSlug"`
	EnvironmentID    string          `json:"environmentId"`
	ProjectID        string          `json:"projectId,omitempty"` // keys Railpack's BuildKit caches per project; empty from an older control plane: keyed by service
	Strategy         string          `json:"strategy"`
	Image            *ImageSource    `json:"image,omitempty"`
	Git              *GitSource      `json:"git,omitempty"`
	Port             int             `json:"port,omitempty"`
	StartCommand     []string        `json:"startCommand,omitempty"`
	Healthcheck      HealthcheckSpec `json:"healthcheck"`
	StopGraceSeconds int             `json:"stopGraceSeconds"`
	MemoryMB         int             `json:"memoryMb,omitempty"`
	CPUs             float64         `json:"cpus,omitempty"`
	ImageRetention   int             `json:"imageRetention"`
	// Processes carries every process of a multi-process deployment in
	// rollout order (the control plane sorts: processes with a port first,
	// default first within each group, then by name). Empty is the legacy
	// single-process deploy, exactly the old code path. When non-empty
	// (always at least 2) the top-level Port, StartCommand, Healthcheck,
	// StopGraceSeconds, MemoryMB, CPUs, Replicas, Strategy, Hosts,
	// DomainRules and OriginLock MUST equal the default process' spec (the
	// control plane fills both from one struct); the agent uses Processes only.
	Processes []ProcessSpec `json:"processes,omitempty"`
	// Docker holds the service's allow-listed container options (nil = none).
	Docker *DockerOptions `json:"docker,omitempty"`
	// DockerPolicy is the target server's policy for Docker options. The agent
	// re-validates Docker against it; nil is a restricted server.
	DockerPolicy *DockerPolicy `json:"dockerPolicy,omitempty"`
	Volumes      []VolumeMount `json:"volumes,omitempty"`
	// Files are written into every container before it starts (not secret).
	Files []FileSpec `json:"files,omitempty"`
	// OneShot runs the container to completion: no restart policy, healthy means
	// exit code 0, no route.
	OneShot bool `json:"oneshot,omitempty"`
	// PreDeploy, when set, is run in a container of the new image after the
	// image is ready and before any container of the new release starts.
	PreDeploy *PreDeploySpec `json:"preDeploy,omitempty"`
	// PostDeploy, when set, is run in a container of the new image after the
	// new release is live and the previous one retired. Failure is warn-only.
	PostDeploy *PostDeploySpec `json:"postDeploy,omitempty"`
	// EnvKeys lists variable keys expected in Secrets as "env:<KEY>".
	EnvKeys []string `json:"envKeys,omitempty"`
	// SecretEnvKeys is the subset of EnvKeys whose values are secret. Only
	// these (plus git token / registry auth) are masked in logs; platform and
	// plain variables are not.
	SecretEnvKeys []string `json:"secretEnvKeys,omitempty"`
	// BuildArgKeys and BuildSecretKeys list the build-time variables (git
	// builds) expected in Secrets as "build:<KEY>": plain keys become
	// --build-arg, secret keys BuildKit secrets. Never values.
	BuildArgKeys    []string `json:"buildArgKeys,omitempty"`
	BuildSecretKeys []string `json:"buildSecretKeys,omitempty"`
	// Hosts are the service's hostnames; empty means no route.
	Hosts []string `json:"hosts,omitempty"`
	// Routes is the full route table for every other service on this server
	// (the agent merges the new target for this service before /load).
	Routes []Route `json:"routes,omitempty"`
	// ACMEEmail is the server's ACME contact email (see Reconcile.ACMEEmail).
	ACMEEmail string `json:"acmeEmail,omitempty"`
	// TrafficAnalytics: see Reconcile.TrafficAnalytics.
	TrafficAnalytics *bool `json:"trafficAnalytics,omitempty"`
	// DomainRules are the per-domain rules and options of this service's Hosts
	// (proxy v2). Hosts without a rule are served with defaults.
	DomainRules []DomainRule `json:"domainRules,omitempty"`
	// OriginLock is the Cloudflare origin lock data of the service's locked
	// DomainRules (cforigin.go); nil when none is locked.
	OriginLock *OriginLock `json:"originLock,omitempty"`
	// Proxy replaces the server-level proxy configuration (nil = unchanged).
	Proxy *ProxyConfig `json:"proxy,omitempty"`
	// Tailnet is the service's desired per-service tailnet node (nil = none and
	// nothing to remove). The auth key of a Provision spec rides in Secrets.
	Tailnet *ServiceTailnetSpec `json:"tailnet,omitempty"`
	// Network membership of this service's replicas (see network.go).
	// BlockEgress: join only the environment's internal network.
	// EnvHasEgressBlocked: another service of the environment blocks egress, so
	// this one also joins the internal network (alias on both).
	BlockEgress         bool                      `json:"blockEgress,omitempty"`
	SharedNetworks      []SharedNetworkAttachment `json:"sharedNetworks,omitempty"`
	EnvHasEgressBlocked bool                      `json:"envHasEgressBlocked,omitempty"`
	// Stack, when set, makes this a compose stack deploy: the agent runs
	// `docker compose` for it (see stack.go). Image and Git are then nil.
	Stack *StackSpec `json:"stack,omitempty"`
	// MeshAlias, when set (nc-svc-<serviceId8>), is an extra DNS alias the
	// service's containers carry on every network they join: the service is
	// exposed on the mesh (mesh_exposures), and the server node forwards to it.
	MeshAlias string `json:"meshAlias,omitempty"`
	// Database carries the replication role of a managed Postgres service.
	Database *DatabaseRuntime `json:"database,omitempty"`
}

// ServiceActionPayload targets an existing service (restart/stop/remove).
type ServiceActionPayload struct {
	ServiceID        string `json:"serviceId"`
	StopGraceSeconds int    `json:"stopGraceSeconds"`
	// DeleteVolumes applies to remove_service only.
	DeleteVolumes bool     `json:"deleteVolumes,omitempty"`
	VolumeNames   []string `json:"volumeNames,omitempty"`
	// Stack marks a compose stack: the agent acts on the whole compose
	// project (nc-<serviceId8>) instead of replica containers.
	Stack bool `json:"stack,omitempty"`
}

// ScaleServicePayload adds or removes replicas of a service's current
// deployment without rebuilding. Deploy is the current deployment's payload; the
// agent uses it to create new replicas (same image ref/digest or built image
// ID, env keys, healthcheck). Env secrets are attached as for deploy.
type ScaleServicePayload struct {
	ServiceID    string `json:"serviceId"`
	DeploymentID string `json:"deploymentId"` // the current deployment
	Replicas     int    `json:"replicas"`     // desired count, >= 1
	// Process is the process to scale: "" is the default process (legacy
	// payloads). Deploy is the current deployment's payload, Processes included.
	Process string `json:"process,omitempty"`
	// ScaledAt is when the control plane stored Replicas (see
	// DesiredService.ScaledAt); zero from an older control plane.
	ScaledAt time.Time     `json:"scaledAt,omitzero"`
	Deploy   DeployPayload `json:"deploy"`
}

// SyncRoutesPayload replaces the server's route table.
type SyncRoutesPayload struct {
	Routes []Route `json:"routes"`
	// ACMEEmail is the server's ACME contact email (see Reconcile.ACMEEmail).
	ACMEEmail string `json:"acmeEmail,omitempty"`
	// TrafficAnalytics: see Reconcile.TrafficAnalytics.
	TrafficAnalytics *bool `json:"trafficAnalytics,omitempty"`
	// Proxy replaces the server-level proxy configuration (nil = unchanged).
	Proxy *ProxyConfig `json:"proxy,omitempty"`
	// DeploymentID is set for a route-only deployment (a route service deploy):
	// the agent reports route/verify step events against it.
	DeploymentID string `json:"deploymentId,omitempty"`
	// TableSeq numbers this table among the whole tables the control plane
	// builds for the server (sync_routes, proxy_configure with routes, the
	// reconcile frame): higher is newer. An agent that has applied a higher
	// number does not apply this table (see Engine). 0 = unnumbered (an older
	// control plane): always applied.
	TableSeq uint64 `json:"tableSeq,omitempty"`
}

// LogsSubscribe requests runtime logs for a service.
type LogsSubscribe struct {
	StreamID  string `json:"streamId"`
	ServiceID string `json:"serviceId"`
	Tail      int    `json:"tail"`
	Follow    bool   `json:"follow"`
	// Component limits a stack's stream to one compose service ("" = all
	// components, lines prefixed with the component name).
	Component string `json:"component,omitempty"`
	// Process limits the stream to one process of a multi-process service
	// (LabelProcess); "" = every process. Mutually exclusive with Component
	// (the control plane enforces it).
	Process string `json:"process,omitempty"`
}

// LogsUnsubscribe ends a subscription.
type LogsUnsubscribe struct {
	StreamID string `json:"streamId"`
}

// Labels applied to every managed container.
const (
	LabelManaged     = "nebulactrl.managed"
	LabelService     = "nebulactrl.service"
	LabelDeployment  = "nebulactrl.deployment"
	LabelRelease     = "nebulactrl.release"
	LabelEnvironment = "nebulactrl.environment"
	LabelReplica     = "nebulactrl.replica"
	// LabelSpec carries the non-secret runtime spec (JSON) the agent needs to
	// add replicas without a command: slug, port, healthcheck, grace, limits.
	LabelSpec = "nebulactrl.spec"
)

// MaxReplicas is the agent's sanity bound on the replicas of a service or
// process; the control plane enforces the much lower product limit.
const MaxReplicas = 100

// Well-known infrastructure names on each host.
const (
	EdgeNetwork      = "nebulactrl-edge"
	ProxyContainer   = "nebulactrl-traefik"
	ProxyACMEVolume  = "nebulactrl-traefik-acme"
	ProxyProbePort   = 8099
	ProxyAdminPort   = 9000
	EnvNetworkPrefix = "nebulactrl-env-"
	ContainerPrefix  = "nc-"
	VolumePrefix     = "nc-vol-"
	BuildImagePrefix = "nc-build/"
)

// Short returns the first 8 characters of an ID (UUID string).
func Short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// ContainerName returns nc-<slug>-<deploymentId8>-<replica>; replica counts from 0.
func ContainerName(slug, deploymentID string, replica int) string {
	return ContainerPrefix + slug + "-" + Short(deploymentID) + "-" + strconv.Itoa(replica)
}

// ContainerNames returns the container names of replicas 0..n-1 (n < 1 counts
// as 1, n > MaxReplicas as MaxReplicas: the count arrives in a command and
// sizes the slice).
func ContainerNames(slug, deploymentID string, n int) []string {
	n = max(n, 1)
	if n > MaxReplicas {
		n = MaxReplicas
	}
	names := make([]string, n)
	for i := range names {
		names[i] = ContainerName(slug, deploymentID, i)
	}
	return names
}

// EnvNetwork returns nebulactrl-env-<environmentId8>. Docker picks a container's
// default gateway network by name order, so every managed network name must
// sort after EdgeNetwork: Traefik's published ports are then not re-bound when
// it joins an environment network.
func EnvNetwork(environmentID string) string { return EnvNetworkPrefix + Short(environmentID) }

// VolumeName returns nc-vol-<serviceId8>-<name>.
func VolumeName(serviceID, name string) string { return VolumePrefix + Short(serviceID) + "-" + name }
