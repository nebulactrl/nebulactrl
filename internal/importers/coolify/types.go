package coolify

import (
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- request ----

// CoolifySource is the Coolify instance to read. The token is used for this request
// only: it is held in memory, sent as a bearer token to the instance, and never
// stored, logged or returned.
type CoolifySource struct {
	URL   string `json:"url" minLength:"1" doc:"Address of the Coolify instance, for example https://coolify.example.com"`
	Token string `json:"token" minLength:"1" doc:"Coolify API token with the read and read:sensitive abilities. Never stored or returned."`
}

// CoolifyRegistryAuth is the pull credential of one registry host (Coolify never exposes its own).
type CoolifyRegistryAuth struct {
	Username string `json:"username"`
	Password string `json:"password" doc:"Write-only"`
}

// CoolifyEnvChoice overrides one variable of a resource.
type CoolifyEnvChoice struct {
	// Value supplies a value the API could not read (a locked variable) or replaces the Coolify one.
	Value *string `json:"value,omitempty" doc:"Write-only"`
	// Secret forces the secret flag on or off.
	Secret *bool `json:"secret,omitempty"`
	// Skip leaves the variable out.
	Skip *bool `json:"skip,omitempty"`
	// Rewrite false keeps host names, ports and credentials of Coolify resources as they are.
	Rewrite *bool `json:"rewrite,omitempty"`
}

// CoolifyDomainChoice overrides one domain of an application, keyed by the Coolify host name.
type CoolifyDomainChoice struct {
	Enabled *bool `json:"enabled,omitempty"`
	// Host replaces the host name (for example to use another domain during the migration).
	Host *string `json:"host,omitempty"`
}

// CoolifyResourceChoice is what the reviewer decided about one application or database.
type CoolifyResourceChoice struct {
	// Include false leaves the resource out. For an engine with a source-available
	// licence (Redis, MongoDB) an explicit true is the acceptance of the licence.
	Include *bool   `json:"include,omitempty"`
	Name    *string `json:"name,omitempty"`
	// Port overrides the service port; 0 makes the application a worker without a port.
	Port *int `json:"port,omitempty" minimum:"0" maximum:"65535"`
	// Kind overrides app or worker.
	Kind *string `json:"kind,omitempty" enum:"app,worker"`
	// EngineVersion overrides the managed database version.
	EngineVersion *string                        `json:"engineVersion,omitempty"`
	Domains       map[string]CoolifyDomainChoice `json:"domains,omitempty"`
	Env           map[string]CoolifyEnvChoice    `json:"env,omitempty"`
}

// CoolifyEnvironmentChoice is the decision about one Coolify environment, keyed by its name.
type CoolifyEnvironmentChoice struct {
	Skip *bool `json:"skip,omitempty"`
	// Name renames the environment.
	Name *string `json:"name,omitempty"`
}

// CoolifyProjectChoice is the decision about one Coolify project.
type CoolifyProjectChoice struct {
	Skip *bool `json:"skip,omitempty"`
	// Name renames the new project (the default on a collision adds " (Coolify)").
	Name *string `json:"name,omitempty"`
	// ExistingProjectID imports into this project instead of creating one; its
	// environments with the same name are reused.
	ExistingProjectID *uuid.UUID                          `json:"existingProjectId,omitempty"`
	Environments      map[string]CoolifyEnvironmentChoice `json:"environments,omitempty"`
}

// CoolifyChoices are the reviewer's decisions. Keys of Projects and Resources are Coolify UUIDs.
type CoolifyChoices struct {
	Projects  map[string]CoolifyProjectChoice  `json:"projects,omitempty"`
	Resources map[string]CoolifyResourceChoice `json:"resources,omitempty"`
	// Registries holds pull credentials by registry host (ghcr.io, registry.example.com);
	// they apply to every image service of that registry.
	Registries map[string]CoolifyRegistryAuth `json:"registries,omitempty"`
	// GitConnectionID links git services to a connected GitHub/GitLab/Gitea account
	// so builds can clone private repositories.
	GitConnectionID *uuid.UUID `json:"gitConnectionId,omitempty"`
}

// CoolifyPlanRequest is the body of a plan.
type CoolifyPlanRequest struct {
	Source  CoolifySource  `json:"source"`
	Choices CoolifyChoices `json:"choices"`
	// ServerID adds checks against the server the services would run on and
	// decides which Docker options it accepts.
	ServerID *uuid.UUID `json:"serverId,omitempty"`
}

// CoolifyApplyRequest is the body of an import.
type CoolifyApplyRequest struct {
	Source   CoolifySource  `json:"source"`
	Choices  CoolifyChoices `json:"choices"`
	ServerID uuid.UUID      `json:"serverId"`
	// Deploy deploys the new services right away (needs deployments:write);
	// otherwise they stay staged.
	Deploy bool `json:"deploy,omitempty"`
}

// ---- plan ----

// CoolifyPlanDomain is one host name of an application.
type CoolifyPlanDomain struct {
	Host string `json:"host"`
	// Path is the path prefix of a Coolify domain with a path.
	Path       string `json:"path,omitempty"`
	Port       int    `json:"port,omitempty"`
	Enabled    bool   `json:"enabled"`
	ForceHTTPS bool   `json:"forceHttps"`
	Noindex    bool   `json:"noindex,omitempty"`
	Compress   bool   `json:"compress,omitempty"`
	// Generate means a generated host name replaces an auto-generated Coolify one (sslip.io).
	Generate bool `json:"generate,omitempty"`
	// Conflict is true when the host name is already used by a NebulaCtrl service.
	Conflict bool   `json:"conflict,omitempty"`
	Note     string `json:"note,omitempty"`
}

// CoolifyPlanHealth is the health check of a service.
type CoolifyPlanHealth struct {
	Type               string `json:"type" enum:"http,tcp,docker,exec,none"`
	Path               string `json:"path,omitempty"`
	Port               int    `json:"port,omitempty"`
	IntervalSeconds    int    `json:"intervalSeconds,omitempty"`
	TimeoutSeconds     int    `json:"timeoutSeconds,omitempty"`
	StartPeriodSeconds int    `json:"startPeriodSeconds,omitempty"`
	DeadlineSeconds    int    `json:"deadlineSeconds,omitempty"`

	command []string // exec health check argv
}

// CoolifyPlanGit is how a git service is built.
type CoolifyPlanGit struct {
	URL            string `json:"url"`
	Branch         string `json:"branch"`
	BuildType      string `json:"buildType"`
	DockerfilePath string `json:"dockerfilePath,omitempty"`
	BuildContext   string `json:"buildContext,omitempty"`
	// Repo is owner/name for a GitHub-style repository (linked through a git connection).
	Repo string `json:"repo,omitempty"`
}

// CoolifyPlanManaged is a database that becomes a managed database.
type CoolifyPlanManaged struct {
	Engine   string `json:"engine"`
	Version  string `json:"version"`
	Flavor   string `json:"flavor,omitempty"`
	Database string `json:"database,omitempty"`
	Username string `json:"username,omitempty"`
	// From is the Coolify image the choice was derived from.
	From  string   `json:"from"`
	Notes []string `json:"notes"`
}

// CoolifyPlanSource is where a service comes from.
type CoolifyPlanSource struct {
	Type string `json:"type" enum:"image,git,managed"`
	// Image is the image reference of an image service.
	Image string `json:"image,omitempty"`
	// Registry is the registry host of a non-Docker-Hub image.
	Registry string              `json:"registry,omitempty"`
	Git      *CoolifyPlanGit     `json:"git,omitempty"`
	Managed  *CoolifyPlanManaged `json:"managed,omitempty"`
}

// CoolifyPlanEnv is one variable, by key: values are never part of a plan.
type CoolifyPlanEnv struct {
	Key    string `json:"key"`
	Scope  string `json:"scope" enum:"runtime,build,both"`
	Secret bool   `json:"secret"`
	// SecretReason says why the variable is a secret: locked, name, value, reference, choice.
	SecretReason string `json:"secretReason,omitempty"`
	// Rewritten is true when the value points at a Coolify resource and was turned into a reference.
	Rewritten bool `json:"rewritten,omitempty"`
	// Skipped variables are not imported; Note says why.
	Skipped bool `json:"skipped,omitempty"`
	// NeedsValue is true when Coolify did not return the value (a locked variable):
	// supply it in the choices or set it after the import.
	NeedsValue bool   `json:"needsValue,omitempty"`
	Note       string `json:"note,omitempty"`

	value string // the stored text, references included
}

// CoolifyPlanVolume is a persistent volume (its data is not copied).
type CoolifyPlanVolume struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

// CoolifyPlanRewrite is one value turned into a reference. The old value is never shown.
type CoolifyPlanRewrite struct {
	Resource string `json:"resource"`
	Key      string `json:"key"`
	Kind     string `json:"kind" enum:"host,password,user,port,database,url"`
	To       string `json:"to" doc:"The reference the value now uses, for example ${{db.DATABASE_HOST}}"`
}

// CoolifyPlanResource is one Coolify application or database as it would be imported.
type CoolifyPlanResource struct {
	UUID string `json:"uuid"`
	// Kind is app, worker or database.
	Kind string `json:"kind" enum:"app,worker,database"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	// Include is false for resources that are not imported; IncludeNote says why.
	Include     bool   `json:"include"`
	IncludeNote string `json:"includeNote,omitempty"`

	Source    CoolifyPlanSource   `json:"source"`
	Port      int                 `json:"port,omitempty"`
	Health    *CoolifyPlanHealth  `json:"health,omitempty"`
	Domains   []CoolifyPlanDomain `json:"domains"`
	Volumes   []CoolifyPlanVolume `json:"volumes"`
	MemoryMB  int                 `json:"memoryMb,omitempty"`
	CPUs      float64             `json:"cpus,omitempty"`
	Command   []string            `json:"command,omitempty"`
	PreDeploy []string            `json:"preDeploy,omitempty"`
	// PostDeploy runs after the new release is live.
	PostDeploy []string `json:"postDeploy,omitempty"`
	// Docker are the typed container options that were applied.
	Docker *agentprotocol.DockerOptions `json:"docker,omitempty"`
	// NeedsUnrestricted names the options that need an unrestricted server.
	NeedsUnrestricted []string         `json:"needsUnrestricted"`
	Env               []CoolifyPlanEnv `json:"env"`
	// DependsOn lists the slugs of services this one references (deploy order).
	DependsOn []string `json:"dependsOn"`
	// Unsupported lists what Coolify has that could not be imported.
	Unsupported []string      `json:"unsupported"`
	Notes       []CoolifyNote `json:"notes"`
	// Followup tells how to copy the data of a database.
	Followup string `json:"followup,omitempty"`

	// internal
	app          *Application
	dbRec        *Database
	choice       CoolifyResourceChoice
	registryHost string
	licence      bool
	// hosts are the names other resources reach this one by (lower case).
	hosts []string
	// primary is the export a full connection URL of a database becomes.
	engine string
	// volumes of a database the managed database replaces (data is not copied).
	referenced bool
	rewrites   []CoolifyPlanRewrite
	// secretValues are the locked variables whose value Coolify did not return.
	secretValues []string
	// railpack holds build settings carried over as RAILPACK_* variables.
}

// CoolifyPlanEnvironment is one Coolify environment.
type CoolifyPlanEnvironment struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
	// Action is create, existing (an environment of the target project is reused) or skip.
	Action     string                `json:"action" enum:"create,existing,skip"`
	Production bool                  `json:"production"`
	Resources  []CoolifyPlanResource `json:"resources"`

	existingID *uuid.UUID
	coolifyID  int
}

// CoolifyPlanProject is one Coolify project.
type CoolifyPlanProject struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	// Action is create, existing (an existing project is used) or skip.
	Action string `json:"action" enum:"create,existing,skip"`
	// Collision is true when a project with the same name already exists.
	Collision    bool                     `json:"collision"`
	ExistingID   *uuid.UUID               `json:"existingId,omitempty"`
	Environments []CoolifyPlanEnvironment `json:"environments"`
}

// CoolifyPlanUnsupported is something in Coolify that is not imported at all.
type CoolifyPlanUnsupported struct {
	Kind    string `json:"kind" enum:"service,database,application,other"`
	Name    string `json:"name"`
	Project string `json:"project,omitempty"`
	Reason  string `json:"reason"`
}

// CoolifyPlanRegistry is a registry that needs a pull credential.
type CoolifyPlanRegistry struct {
	Host string `json:"host"`
	// Username is the user Coolify's server is logged in as, a hint for the credential.
	Username string   `json:"username,omitempty"`
	Services []string `json:"services"`
	// Provided is true when the choices carry a credential for the host.
	Provided bool `json:"provided"`
}

// CoolifyCheck is a preflight result.
type CoolifyCheck struct {
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

// CoolifyPlan is the review of an import: nothing is written.
type CoolifyPlan struct {
	// CoolifyVersion is the version the instance reports.
	CoolifyVersion string                   `json:"coolifyVersion,omitempty"`
	Projects       []CoolifyPlanProject     `json:"projects"`
	Unsupported    []CoolifyPlanUnsupported `json:"unsupported"`
	Registries     []CoolifyPlanRegistry    `json:"registries"`
	Rewrites       []CoolifyPlanRewrite     `json:"rewrites"`
	Checks         []CoolifyCheck           `json:"checks"`
	Warnings       []string                 `json:"warnings"`
	Blockers       []string                 `json:"blockers"`
	// Followups are the manual steps after the import (copy data, credentials, DNS).
	Followups []string `json:"followups"`
	// Installable is true when the plan has no blockers.
	Installable bool          `json:"installable"`
	Totals      CoolifyTotals `json:"totals"`

	// general are the followups that do not depend on service slugs.
	general []string
}

// CoolifyTotals counts what a plan would create.
type CoolifyTotals struct {
	Projects  int `json:"projects"`
	Services  int `json:"services"`
	Databases int `json:"databases"`
	Variables int `json:"variables"`
	Secrets   int `json:"secrets"`
	Domains   int `json:"domains"`
}

// ---- result ----

// CoolifyAppliedService is a service the import created.
type CoolifyAppliedService struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Slug          string    `json:"slug"`
	Kind          string    `json:"kind"`
	ProjectID     uuid.UUID `json:"projectId"`
	ProjectSlug   string    `json:"projectSlug"`
	EnvironmentID uuid.UUID `json:"environmentId"`
	EnvSlug       string    `json:"envSlug"`
	CoolifyUUID   string    `json:"coolifyUuid"`
}

// CoolifyAppliedProject is a project the import created or filled.
type CoolifyAppliedProject struct {
	ID           uuid.UUID                   `json:"id"`
	Name         string                      `json:"name"`
	Slug         string                      `json:"slug"`
	Created      bool                        `json:"created"`
	Environments []CoolifyAppliedEnvironment `json:"environments"`
}

// CoolifyAppliedEnvironment is an environment the import created or filled.
type CoolifyAppliedEnvironment struct {
	ID      uuid.UUID `json:"id"`
	Name    string    `json:"name"`
	Slug    string    `json:"slug"`
	Created bool      `json:"created"`
}

// CoolifyApplyResult is the outcome of an import.
type CoolifyApplyResult struct {
	Projects []CoolifyAppliedProject `json:"projects"`
	Services []CoolifyAppliedService `json:"services"`
	// Deployments are the ids of the deployments started with deploy: true.
	Deployments []uuid.UUID          `json:"deployments"`
	Rewrites    []CoolifyPlanRewrite `json:"rewrites"`
	Warnings    []string             `json:"warnings"`
	Followups   []string             `json:"followups"`
}
