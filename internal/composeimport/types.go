package composeimport

import (
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- request ----

// ComposeRepoRef points at a Compose file in a git repository.
type ComposeRepoRef struct {
	// ConnectionID is a git connection of the team (private repositories).
	ConnectionID *uuid.UUID `json:"connectionId,omitempty"`
	// Repo is owner/name for a connection; for a public repository without a
	// connection use URL.
	Repo string `json:"repo,omitempty" maxLength:"300"`
	// URL is the https URL of a public GitHub or GitLab.com repository.
	URL    string `json:"url,omitempty" maxLength:"500"`
	Branch string `json:"branch,omitempty" maxLength:"250" doc:"Default: the repository's default branch"`
	Path   string `json:"path,omitempty" maxLength:"500" doc:"Path of the Compose file in the repository; default docker-compose.yml"`
}

// ComposeSource is the Compose project to import: pasted or fetched from git.
type ComposeSource struct {
	// Compose is the docker-compose.yml content. Leave empty to read it from Repo.
	Compose string `json:"compose,omitempty" maxLength:"262144"`
	// Env is the project's .env file, used for ${VAR} interpolation.
	Env string `json:"env,omitempty" maxLength:"262144"`
	// Files supplies the files the project references (env_file entries and
	// bind-mounted files) by the path written in the Compose file.
	Files map[string]string `json:"files,omitempty" doc:"Referenced files by path, e.g. {\"./nginx.conf\": \"...\"}"`
	// Name titles the canvas group; default: the Compose project name or file name.
	Name string          `json:"name,omitempty" maxLength:"120"`
	Repo *ComposeRepoRef `json:"repo,omitempty" doc:"Read the Compose file (and the files it references) from a git repository"`
}

// ComposeDomainChoice overrides a service's domain.
type ComposeDomainChoice struct {
	Enabled bool `json:"enabled"`
	// Host is an explicit hostname; empty with Enabled generates one.
	Host string `json:"host,omitempty" maxLength:"253"`
}

// ComposeEnvChoice overrides one variable.
type ComposeEnvChoice struct {
	Secret  *bool `json:"secret,omitempty" doc:"Store the value as a secret (encrypted, never shown again)"`
	Skip    bool  `json:"skip,omitempty" doc:"Do not import this variable"`
	Rewrite *bool `json:"rewrite,omitempty" doc:"false keeps the original host names in this variable"`
}

// ComposeServiceChoice is what the reviewer decided about one Compose service. Keys
// of Choices are the Compose service names.
type ComposeServiceChoice struct {
	Include *bool  `json:"include,omitempty"`
	Name    string `json:"name,omitempty" maxLength:"120"`
	// Source switches a service with a build between "git" and "image".
	Source string `json:"source,omitempty" enum:"image,git"`
	// Image replaces the image of the service (use with source image).
	Image string `json:"image,omitempty" maxLength:"500"`
	// GitURL/GitBranch build the service from this repository (https URL).
	GitURL    string `json:"gitUrl,omitempty" maxLength:"500"`
	GitBranch string `json:"gitBranch,omitempty" maxLength:"250"`
	// ConnectionID and Repo build from a connected repository.
	ConnectionID    *uuid.UUID                  `json:"connectionId,omitempty"`
	Repo            string                      `json:"repo,omitempty" maxLength:"300"`
	ManagedDatabase *bool                       `json:"managedDatabase,omitempty" doc:"Create a managed database instead of an image service"`
	Port            *int                        `json:"port,omitempty" minimum:"0" maximum:"65535"`
	Domain          *ComposeDomainChoice        `json:"domain,omitempty"`
	PublishPorts    []string                    `json:"publishPorts,omitempty" doc:"Ports to publish through TCP/UDP routes, as \"5432/tcp\" (container port)"`
	Env             map[string]ComposeEnvChoice `json:"env,omitempty"`
	ConvertBinds    []string                    `json:"convertBinds,omitempty" doc:"Container paths of directory bind mounts to turn into named volumes"`
}

// ComposeChoices are the reviewer's decisions.
type ComposeChoices struct {
	Services map[string]ComposeServiceChoice `json:"services,omitempty"`
	// Variables are the reviewer's decisions about the .env entries of a stack
	// (stack mode only; keyed by variable name).
	Variables map[string]ComposeEnvChoice `json:"variables,omitempty"`
}

// ---- plan ----

// ComposePlanBuild describes how a service with a Compose build is built here.
type ComposePlanBuild struct {
	// Context, Dockerfile and Target are as written in the Compose file.
	Context    string `json:"context"`
	Dockerfile string `json:"dockerfile,omitempty"`
	Target     string `json:"target,omitempty"`
	// ArgNames are build arguments; their values are not applied.
	ArgNames []string `json:"argNames"`
	// GitURL and GitBranch are where the service is built from; empty when the
	// reviewer still has to provide them.
	GitURL    string `json:"gitUrl,omitempty"`
	GitBranch string `json:"gitBranch,omitempty"`
	// BuildContext and DockerfilePath are relative to the repository root.
	BuildContext   string     `json:"buildContext,omitempty"`
	DockerfilePath string     `json:"dockerfilePath,omitempty"`
	ConnectionID   *uuid.UUID `json:"connectionId,omitempty"`
	Repo           string     `json:"repo,omitempty"`
}

// ComposePlanManaged says whether and how a service becomes a managed database.
type ComposePlanManaged struct {
	Eligible bool   `json:"eligible"`
	Enabled  bool   `json:"enabled"`
	Engine   string `json:"engine,omitempty"`
	Version  string `json:"version,omitempty"`
	Database string `json:"database,omitempty"`
	Username string `json:"username,omitempty"`
	// Reason says why the service is not eligible, or what changes when it is used.
	Reason string   `json:"reason,omitempty"`
	Notes  []string `json:"notes"`
}

// ComposePlanSource is where a service's code comes from.
type ComposePlanSource struct {
	Type  string            `json:"type" enum:"image,git,managed"`
	Image string            `json:"image,omitempty"`
	Build *ComposePlanBuild `json:"build,omitempty"`
}

// ComposePlanEnv is one variable of a service.
type ComposePlanEnv struct {
	Key string `json:"key"`
	// Value is empty for secrets: it is never returned.
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
	// SecretGuess is true when the secret flag came from the heuristic.
	SecretGuess bool   `json:"secretGuess"`
	Origin      string `json:"origin" enum:"environment,env_file,dotenv"`
	Rewritten   bool   `json:"rewritten"`
	Skipped     bool   `json:"skipped"`
	Note        string `json:"note,omitempty"`
	// raw is the stored value (secrets included); never serialized.
	raw string
}

// ComposePlanVolume is a named volume of a service.
type ComposePlanVolume struct {
	Name string `json:"name"`
	Path string `json:"path"`
	From string `json:"from" enum:"volume,anonymous,bind,managed"`
	Note string `json:"note,omitempty"`
}

// ComposePlanFile is a small bind-mounted file turned into a config file.
type ComposePlanFile struct {
	Path   string `json:"path"`
	Mode   string `json:"mode,omitempty"`
	Bytes  int    `json:"bytes"`
	Source string `json:"source" doc:"The path as written in the Compose file"`
	// Content is the file text; never serialized.
	content string
}

// ComposePlanBind is a bind mount that did not become a volume or file.
type ComposePlanBind struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Status string `json:"status" enum:"file,missing,rejected,ignored,converted"`
	Reason string `json:"reason,omitempty"`
	// CanConvert is true when the mount can be turned into a named volume.
	CanConvert bool `json:"canConvert"`
}

// ComposePlanPort is a published port of a service.
type ComposePlanPort struct {
	Port     int    `json:"port" doc:"Container port"`
	HostPort int    `json:"hostPort,omitempty" doc:"Port published on the host in the Compose file"`
	Protocol string `json:"protocol" enum:"tcp,udp"`
	// Role is http for the service port behind a domain, tcp/udp otherwise.
	Role string `json:"role" enum:"http,tcp,udp"`
	// Publish creates a TCP/UDP route on the server (opt-in).
	Publish bool `json:"publish"`
	// Listen is the public port of the route.
	Listen int `json:"listen,omitempty"`
}

// ComposePlanDomain is a hostname for a service.
type ComposePlanDomain struct {
	Host     string `json:"host,omitempty"`
	Generate bool   `json:"generate"`
	Enabled  bool   `json:"enabled"`
	Source   string `json:"source" enum:"traefik,suggested,choice"`
}

// ComposePlanHealth is the health check of a service.
type ComposePlanHealth struct {
	Type               string   `json:"type"`
	Command            []string `json:"command,omitempty"`
	IntervalSeconds    int      `json:"intervalSeconds,omitempty"`
	TimeoutSeconds     int      `json:"timeoutSeconds,omitempty"`
	StartPeriodSeconds int      `json:"startPeriodSeconds,omitempty"`
	DeadlineSeconds    int      `json:"deadlineSeconds,omitempty"`
	SuccessThreshold   int      `json:"successThreshold,omitempty"`
	FromCompose        bool     `json:"fromCompose"`
}

// ComposePlanService is one Compose service as it would be imported.
type ComposePlanService struct {
	Key  string `json:"key" doc:"The service name in the Compose file"`
	Name string `json:"name"`
	Slug string `json:"slug" doc:"The slug the service will get (references use it)"`
	// Include is false for services the reviewer must opt into (profiles).
	Include     bool              `json:"include"`
	IncludeNote string            `json:"includeNote,omitempty"`
	Kind        string            `json:"kind" enum:"app,worker,database"`
	Source      ComposePlanSource `json:"source"`
	// Managed is set when the image is an official database image.
	Managed *ComposePlanManaged `json:"managed,omitempty"`
	Command []string            `json:"command,omitempty"`
	Port    int                 `json:"port"`
	// PortHTTP says whether Port is treated as HTTP (domain target) or a plain TCP port.
	PortHTTP bool                `json:"portHttp"`
	Health   *ComposePlanHealth  `json:"health,omitempty"`
	Env      []ComposePlanEnv    `json:"env"`
	Volumes  []ComposePlanVolume `json:"volumes"`
	Files    []ComposePlanFile   `json:"files"`
	Binds    []ComposePlanBind   `json:"binds"`
	Ports    []ComposePlanPort   `json:"ports"`
	Domains  []ComposePlanDomain `json:"domains"`
	MemoryMB int                 `json:"memoryMb,omitempty"`
	CPUs     float64             `json:"cpus,omitempty"`
	Replicas int                 `json:"replicas"`
	// Docker are the allow-listed container options carried over (capabilities, ulimits, shm size, ...).
	Docker *agentprotocol.DockerOptions `json:"docker,omitempty"`
	// ReplicasNote explains why replicas were not applied.
	ReplicasNote     string   `json:"replicasNote,omitempty"`
	StopGraceSeconds int      `json:"stopGraceSeconds,omitempty"`
	OneShot          bool     `json:"oneShot"`
	DependsOn        []string `json:"dependsOn" doc:"Compose service names this service is deployed after"`
	Networks         []string `json:"networks"`
	// Notes are keys that were not imported, and why.
	Notes []ComposeNote `json:"notes"`
	// Needs lists the inputs the reviewer must still provide ("gitUrl", ...).
	Needs []string `json:"needs"`

	// internal
	hostNames    []string // names other services use to reach this one
	managedCreds managedCreds
}

// ComposeRewrite is one change made to a variable value so it keeps working here.
type ComposeRewrite struct {
	Service string `json:"service"`
	Key     string `json:"key"`
	Kind    string `json:"kind" enum:"host,password,auth"`
	Target  string `json:"target" doc:"The Compose service the reference points at"`
	From    string `json:"from"`
	To      string `json:"to"`
	Reason  string `json:"reason"`
}

// ComposePlanNetwork is a Compose network and how it is mapped.
type ComposePlanNetwork struct {
	Name     string   `json:"name"`
	External bool     `json:"external"`
	Services []string `json:"services"`
	// SharedNetworkID is the existing shared network the services attach to.
	SharedNetworkID *uuid.UUID `json:"sharedNetworkId,omitempty"`
	Note            string     `json:"note"`
}

// ComposeCheck is a preflight result.
type ComposeCheck struct {
	ID      string `json:"id"`
	Status  string `json:"status" enum:"ok,warn,fail"`
	Message string `json:"message"`
}

// Check statuses.
const (
	CheckOK   = "ok"
	CheckWarn = "warn"
	CheckFail = "fail"
)

// ComposePlan is the review of an import: nothing is written.
type ComposePlan struct {
	Name     string               `json:"name"`
	Services []ComposePlanService `json:"services"`
	Networks []ComposePlanNetwork `json:"networks"`
	Rewrites []ComposeRewrite     `json:"rewrites"`
	// Missing are ${VAR} references without a value or default (empty strings were used).
	Missing []string `json:"missing"`
	// NeededFiles are referenced files that were not supplied.
	NeededFiles []ComposeNeededFile `json:"neededFiles"`
	Notes       []ComposeNote       `json:"notes" doc:"Project-level remarks"`
	Warnings    []string            `json:"warnings"`
	Checks      []ComposeCheck      `json:"checks"`
	// Blockers must be fixed before the import can run.
	Blockers    []string `json:"blockers"`
	Installable bool     `json:"installable"`
	// Mode is how the project would be imported.
	Mode string `json:"mode" enum:"split,stack"`
	// Stack describes the stack when Mode is "stack"; Services is empty then.
	Stack *ComposeStackPlan `json:"stack,omitempty"`
	// Source describes where the Compose file came from.
	SourceKind string `json:"sourceKind" enum:"paste,repo"`
	Commit     string `json:"commit,omitempty"`
}
