package services

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ReleaseSource is the resolved, immutable source of a release
// (releases.source).
type ReleaseSource struct {
	Type string `json:"type"` // image | git | compose
	// Image sources.
	Image  string `json:"image,omitempty"`
	Digest string `json:"digest,omitempty"` // repo@sha256:… once known
	// Git sources.
	GitURL    string `json:"gitUrl,omitempty"`
	Branch    string `json:"branch,omitempty"`
	CommitSHA string `json:"commitSha,omitempty"`
	ImageID   string `json:"imageId,omitempty"` // local image ID of the build, once known
	// RegistryRef is repository@sha256:… of the image a build server pushed to a
	// registry; the target pulls it by digest. Releases built on their target
	// have ImageID instead, so both representations coexist.
	RegistryRef string `json:"registryRef,omitempty"`
	// RegistryID is the registry RegistryRef lives in.
	RegistryID *uuid.UUID `json:"registryId,omitempty"`
	// Platform is the platform of the pushed image (linux/amd64).
	Platform string `json:"platform,omitempty"`
	// BuiltOn is the build server that built the image.
	BuiltOn *uuid.UUID `json:"builtOn,omitempty"`
	// BuildHash identifies the build inputs (commit, build settings, platform,
	// build variables): a live registry image with the same hash is reused.
	BuildHash string `json:"buildHash,omitempty"`
	// Components maps each component of a compose stack to the image digest
	// ref (repo@sha256:…) it ran, recorded from the deploy result so a
	// rollback runs the same images.
	Components map[string]string `json:"components,omitempty"`
}

// Snapshot is the configuration frozen into a release (releases.config).
type Snapshot struct {
	Config   Config     `json:"config"`
	ServerID *uuid.UUID `json:"serverId,omitempty"`
	// EnvironmentID is the environment the release was deployed in. A service
	// moved to another environment shows the change as staged until redeployed.
	EnvironmentID *uuid.UUID       `json:"environmentId,omitempty"`
	Domains       []SnapshotDomain `json:"domains,omitempty"`
	Volumes       []SnapshotVolume `json:"volumes,omitempty"`
	// Networks are the team shared networks the release joined.
	Networks []SnapshotNetwork `json:"networks,omitempty"`
}

// SnapshotDomain is a hostname (plus its routing rule) that was attached at
// release time. Fields added by proxy v2 are omitted when zero, so older
// snapshots decode as plain serve domains.
type SnapshotDomain struct {
	Hostname      string `json:"hostname"`
	TargetPort    int    `json:"targetPort"`
	PathPrefix    string `json:"pathPrefix,omitempty"`
	StripPrefix   bool   `json:"stripPrefix,omitempty"`
	AddPrefix     string `json:"addPrefix,omitempty"`
	Kind          string `json:"kind,omitempty"`
	RedirectTo    string `json:"redirectTo,omitempty"`
	RedirectCode  int    `json:"redirectCode,omitempty"`
	PreservePath  bool   `json:"preservePath,omitempty"`
	TLSMode       string `json:"tlsMode,omitempty"`
	CertificateID string `json:"certificateId,omitempty"`
	// Options is the canonical compact RouteOptions JSON ("" = none).
	Options string `json:"options,omitempty"`
	// Component is the compose service a stack domain routes to, or the process
	// an app domain routes to ("" = the default process, and for other services).
	Component string `json:"component,omitempty"`
	// DNSResolver is the DNS-01 provider of the domain ("" = automatic).
	DNSResolver string `json:"dnsResolver,omitempty"`
	// AuthUsers are the basic-auth user names (never hashes).
	AuthUsers []string `json:"authUsers,omitempty"`
}

// SnapshotVolume is a volume that was attached at release time. DockerName and
// RestoreLabel are set only when the volume row pointed at a restored Docker
// volume (see db.Volume); empty means the volume derived from the name.
type SnapshotVolume struct {
	Name         string `json:"name"`
	MountPath    string `json:"mountPath"`
	DockerName   string `json:"dockerName,omitempty"`
	RestoreLabel string `json:"restoreLabel,omitempty"`
}

// EffectiveDockerName is the Docker volume the release mounted for this volume.
func (v SnapshotVolume) EffectiveDockerName(serviceID uuid.UUID) string {
	if v.DockerName != "" {
		return v.DockerName
	}
	return agentprotocol.VolumeName(serviceID.String(), v.Name)
}

// EnvSnapshot is the decrypted content of releases.env_enc.
type EnvSnapshot struct {
	// Vars is the fully resolved environment, platform variables included.
	Vars map[string]string `json:"vars"`
	// Secret lists the keys of Vars whose values are secret.
	Secret []string `json:"secret,omitempty"`
	// Raw holds the user-defined variables as written (references intact),
	// used to diff pending changes.
	Raw map[string]RawVar `json:"raw,omitempty"`
	// Scope holds the scope of variables that are not runtime-only
	// ("build" or "both"); Build-only keys are absent from the container env.
	Scope map[string]string `json:"scope,omitempty"`
	// Leaves are the secret values substituted into derived variables (see
	// Resolved.Leaves), kept so the log redactor still masks them after the
	// live variables changed.
	Leaves []string `json:"leaves,omitempty"`
	// Ext holds, for the variables that depend on an external secret manager,
	// the template that rebuilds the value at deploy time. The value itself is
	// never stored: Vars holds only the reference text for these keys, and
	// Materialize fetches the real values at send time.
	Ext map[string]ExtTpl `json:"ext,omitempty"`
}

// RawVar is a user variable as stored.
type RawVar struct {
	Value  string `json:"v"`
	Secret bool   `json:"s,omitempty"`
	// Scope is "build" or "both"; empty means runtime.
	Scope string `json:"c,omitempty"`
	// Gen describes the generators of a generated variable, e.g. "secret(32)".
	Gen string `json:"g,omitempty"`
}

// RegistryAuth is private-registry credentials for image pulls.
type RegistryAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// ServerRef is the server summary embedded in a service.
type ServerRef struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	LastSeenAt *time.Time `json:"lastSeenAt"`
}

// DeploymentRef is the deployment summary embedded in a service.
type DeploymentRef struct {
	ID            uuid.UUID  `json:"id"`
	Status        string     `json:"status"`
	Trigger       string     `json:"trigger"`
	Strategy      string     `json:"strategy"`
	StatusMessage string     `json:"statusMessage"`
	CreatedAt     time.Time  `json:"createdAt"`
	FinishedAt    *time.Time `json:"finishedAt"`
	ReleaseID     *uuid.UUID `json:"releaseId"`
	// FailedStep is the deploy step of the newest failed step event (build,
	// create, health, route, verify, …); null when the deployment did not fail
	// at a reported step.
	FailedStep *string `json:"failedStep"`
	// CurrentStep is the deploy step the agent is on right now (its newest
	// step event is "started"): pull, build, predeploy, create, health, … Only
	// set while the deployment is active.
	CurrentStep *string `json:"currentStep"`
	// Message is the optional note given when the deployment was requested.
	Message *string `json:"message"`
}

// ReleaseRef is the release summary embedded in a service.
type ReleaseRef struct {
	ID       uuid.UUID     `json:"id"`
	ImageRef string        `json:"imageRef"`
	Source   ReleaseSource `json:"source"`
}

// ObservedView is the last observed runtime state of a service.
type ObservedView struct {
	State        string     `json:"state"`
	Health       string     `json:"health"`
	ContainerID  string     `json:"containerId"`
	StartedAt    *time.Time `json:"startedAt"`
	ImageID      string     `json:"imageId"`
	DeploymentID string     `json:"deploymentId,omitempty"`
	// Error is set when automatic recovery gave up (reconcile limit reached).
	Error string `json:"error,omitempty"`
	// Replicas is the per-replica state (index order); ReplicasReady counts the
	// running, healthy ones and ReplicasDesired the replicas the agent was told
	// to run. State and Health above are aggregates over all replicas.
	Replicas        []ObservedReplicaView `json:"replicas"`
	ReplicasReady   int                   `json:"replicasReady"`
	ReplicasDesired int                   `json:"replicasDesired"`
	// Processes is the per-process state of a service with several processes
	// ([] for a single-process service). State and Health above stay aggregates
	// over every replica of every process.
	Processes []ObservedProcessView `json:"processes"`
}

// ObservedProcessView is the observed aggregate of one process.
type ObservedProcessView struct {
	Name            string `json:"name"`
	State           string `json:"state"`
	Health          string `json:"health"`
	ReplicasReady   int    `json:"replicasReady"`
	ReplicasDesired int    `json:"replicasDesired"`
}

// ProcessView is one process of a service with several processes: its resolved
// configuration, scale and observed state.
type ProcessView struct {
	Name    string   `json:"name"`
	Default bool     `json:"default"`
	Port    int      `json:"port"`
	Command []string `json:"command"`
	// Singleton: never two instances at once; scaling is off.
	Singleton bool `json:"singleton"`
	// StopGraceSeconds is the effective grace (the service's when the process sets none).
	StopGraceSeconds int `json:"stopGraceSeconds"`
	// Scaling is the replica and autoscaling state of the process (the default
	// process: the service's own columns) and whether it can scale.
	Scaling ScalingView `json:"scaling"`
	// Observed is the process' observed state; null until the agent reported it.
	Observed *ObservedProcessView `json:"observed"`
	// Pending: the process is in the staged config but not in the running release (added).
	Pending bool `json:"pending"`
	// Removed: the process is in the running release but no longer in the staged
	// config; it stays listed until the next deployment retires it.
	Removed bool `json:"removed"`
}

// ObservedReplicaView is the observed state of one replica container.
type ObservedReplicaView struct {
	Index        int        `json:"index"`
	ContainerID  string     `json:"containerId"`
	State        string     `json:"state"`
	Health       string     `json:"health"`
	StartedAt    *time.Time `json:"startedAt"`
	RestartCount int        `json:"restartCount"`
	// Component is the compose service of a stack's replica ("" for other services).
	Component string `json:"component"`
	// Process is the process of a service with several processes ("" for
	// single-process services); Index is then the replica index within the process.
	Process string `json:"process"`
}

// DomainView is a hostname attached to a service.
type DomainView struct {
	ID         uuid.UUID `json:"id"`
	Hostname   string    `json:"hostname"`
	TargetPort int       `json:"targetPort"`
	// Component is the compose service a stack domain routes to ("" for other services).
	Component string `json:"component"`
	// Process is the process of an app with several processes a domain routes to
	// ("" = the default process, and for services without processes).
	Process       string     `json:"process"`
	Generated     bool       `json:"generated"`
	Status        string     `json:"status"`
	StatusMessage string     `json:"statusMessage"`
	DNSOk         *bool      `json:"dnsOk"`
	DNSCheckedAt  *time.Time `json:"dnsCheckedAt"`
	// CreatedAt is when the domain was added; the dashboard says new names take a while to reach every DNS resolver for the first 30 minutes.
	CreatedAt time.Time `json:"createdAt"`
	// PathPrefix restricts the domain to a path ("" = whole host).
	PathPrefix  string `json:"pathPrefix"`
	StripPrefix bool   `json:"stripPrefix"`
	AddPrefix   string `json:"addPrefix"`
	// Kind is serve (route to the service) or redirect (answer with a redirect to RedirectTo).
	Kind         string `json:"kind" enum:"serve,redirect"`
	RedirectTo   string `json:"redirectTo"`
	RedirectCode int    `json:"redirectCode" enum:"301,302,307,308"`
	PreservePath bool   `json:"preservePath"`
	TLSMode      string `json:"tlsMode" enum:"auto,custom,none"`
	// CertificateID is the uploaded certificate pinned to the domain, if any.
	CertificateID *uuid.UUID `json:"certificateId"`
	// Options is the typed middleware set (headers, CORS, rate limit, IP allowlist, …).
	Options agentprotocol.RouteOptions `json:"options"`
	// AuthUsers are the basic-auth user names; hashes are never returned.
	AuthUsers []string `json:"authUsers"`
	// DNSResolver is the DNS-01 provider that obtains the certificate ("" = HTTP-01 or a covering wildcard certificate).
	DNSResolver string `json:"dnsResolver" doc:"The lego code of the DNS-01 provider that obtains the certificate; empty = HTTP-01 or a covering wildcard certificate"`
	// Cert is where the certificate comes from and its observed status and expiry.
	Cert *dns01.DomainCert `json:"cert"`
	// DNS is the Cloudflare DNS state of the domain; null while the team has no Cloudflare connection.
	DNS *DomainDNS `json:"dns"`
	// WWWPartner is the www/apex counterpart of a whole-host serve domain ("" when the domain has none: it is a redirect, has a path prefix, a wildcard, or no counterpart name exists).
	WWWPartner string `json:"wwwPartner" doc:"The counterpart hostname a www redirect would use (www.<host>, or the host without www.); empty when the domain cannot have one"`
	// WWWRedirect is true when the partner hostname exists on this service as a whole-host redirect to this domain.
	WWWRedirect bool `json:"wwwRedirect" doc:"A whole-host redirect from wwwPartner to this domain exists (both hostnames are routed and get certificates)"`
	// WWWRedirectFor names the serve domain this redirect domain is the www/apex counterpart of ("" otherwise).
	WWWRedirectFor string `json:"wwwRedirectFor" doc:"For the redirect domain of a www redirect: the hostname it redirects to; empty otherwise"`
}

// DomainDNS is the cached Cloudflare DNS state of a domain. The live status,
// with the records found in the zone, is GET /domains/{id}/dns.
type DomainDNS struct {
	Provider string `json:"provider" enum:"cloudflare"`
	// ZoneID and ZoneName name the connected zone the hostname falls into; empty when it is in none.
	ZoneID   string `json:"zoneId"`
	ZoneName string `json:"zoneName"`
	// Manage is read-only: NebulaCtrl keeps the records of a domain in a connected zone in sync, always.
	Manage  bool `json:"manage"`
	Proxied bool `json:"proxied" doc:"Orange cloud: traffic goes through Cloudflare. Tunnel records are always proxied."`
	// Status is managed (record created by NebulaCtrl), present (an existing record points to the server),
	// conflict (a record NebulaCtrl did not create differs or blocks), wrong_target, missing or not_in_zone; empty until the first evaluation.
	Status    string     `json:"status" enum:",managed,present,conflict,wrong_target,missing,not_in_zone"`
	Message   string     `json:"message"`
	Error     string     `json:"error" doc:"The last failed sync, empty when the last sync worked"`
	CheckedAt *time.Time `json:"checkedAt"`
}

// ServiceView is the API representation of a service.
type ServiceView struct {
	ID                  uuid.UUID      `json:"id"`
	EnvironmentID       uuid.UUID      `json:"environmentId"`
	ProjectID           uuid.UUID      `json:"projectId"`
	Name                string         `json:"name"`
	Slug                string         `json:"slug"`
	Kind                string         `json:"kind" enum:"app,worker,database,route,stack"`
	SourceType          string         `json:"sourceType" enum:"image,git,external,compose"`
	Config              Config         `json:"config"`
	ServerID            *uuid.UUID     `json:"serverId"`
	Server              *ServerRef     `json:"server"`
	Generation          int64          `json:"generation"`
	AppliedGeneration   int64          `json:"appliedGeneration"`
	HasStagedChanges    bool           `json:"hasStagedChanges"`
	CurrentDeploymentID *uuid.UUID     `json:"currentDeploymentId"`
	LatestDeployment    *DeploymentRef `json:"latestDeployment"`
	CurrentRelease      *ReleaseRef    `json:"currentRelease"`
	Observed            *ObservedView  `json:"observed"`
	ObservedAt          *time.Time     `json:"observedAt"`
	Stale               bool           `json:"stale"`
	Domains             []DomainView   `json:"domains"`
	PrivateHost         string         `json:"privateHost"`
	GitTokenSet         bool           `json:"gitTokenSet"`
	RegistryAuthSet     bool           `json:"registryAuthSet"`
	CreatedAt           time.Time      `json:"createdAt"`
	UpdatedAt           time.Time      `json:"updatedAt"`
	// Volumes are the volumes currently attached (staged until deployed).
	Volumes []VolumeRef `json:"volumes"`
	// DesiredState is "stopped" after a stop and "running" after a restart or deployment.
	DesiredState string `json:"desiredState" enum:"running,stopped"`
	// PendingChangeCounts splits the staged changes (see pending-changes) into
	// variables and settings (config, server, domains, volumes). A service
	// that was never deployed counts its first deployment as one setting.
	PendingChangeCounts PendingChangeCounts `json:"pendingChangeCounts"`
	// StagedKind is "new" for a service that was never successfully deployed
	// and has staged changes, "edited" for a deployed service with staged
	// changes and null otherwise.
	StagedKind *string `json:"stagedKind" doc:"new | edited | null"`
	// Scaling is the replica state: desired and ready counts, autoscaling,
	// the last scale event and why a scale-out was blocked.
	Scaling ScalingView `json:"scaling"`
	// Processes are the processes of a service with several processes, the
	// default process first ([] for a single-process service). Scaling above
	// describes the default process.
	Processes []ProcessView `json:"processes"`

	// Maintenance is the maintenance-mode state: while enabled the proxy
	// answers every domain of the service with a 503 maintenance page.
	// It is an operational setting, never staged and never rolled back.
	Maintenance MaintenanceView `json:"maintenance"`

	// Git is the repository link of a git service (null when it has none): the
	// connection, the repository and the auto-deploy settings. The branch is
	// config.gitBranch.
	Git *GitRef `json:"git"`

	// Tailnet is the observed node of the service's tailnet sidecar (null
	// until a deployment provisioned it). The desired side is config.tailnet.
	Tailnet *TailnetView `json:"tailnet"`

	// Networks are the team shared networks the service is attached to
	// (staged until deployed); the alias is its hostname on each.
	Networks []NetworkRef `json:"networks"`
}

// MaintenanceView is the maintenance mode of a service.
type MaintenanceView struct {
	Enabled bool `json:"enabled"`
	// Title and Message are the custom page text; empty shows the default copy.
	// They are kept while disabled so the next activation can start from them.
	Title   string `json:"title"`
	Message string `json:"message"`
	// ETA is when the service is expected back (shown on the page, sent as Retry-After).
	ETA *time.Time `json:"eta"`
	// Since is when maintenance was switched on (null while off).
	Since *time.Time `json:"since"`
}

// VolumeRef is a volume attached to a service.
type VolumeRef struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	MountPath string    `json:"mountPath"`
}

// PendingChangeCounts is the number of staged changes by kind.
type PendingChangeCounts struct {
	Variables int `json:"variables"`
	Settings  int `json:"settings"`
}

// Staged kinds of ServiceView.StagedKind.
const (
	StagedNew    = "new"
	StagedEdited = "edited"
)

// DecodeObserved parses services.observed.
func DecodeObserved(raw json.RawMessage) *ObservedView {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var o ObservedView
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil
	}
	if o.Processes == nil {
		o.Processes = []ObservedProcessView{}
	}
	return &o
}

// DecodeSource parses releases.source.
func DecodeSource(raw json.RawMessage) ReleaseSource {
	var s ReleaseSource
	_ = json.Unmarshal(raw, &s)
	return s
}

// DecodeSnapshot parses releases.config.
func DecodeSnapshot(raw json.RawMessage) Snapshot {
	var s Snapshot
	_ = json.Unmarshal(raw, &s)
	s.Config = s.Config.WithDefaults()
	return s
}
