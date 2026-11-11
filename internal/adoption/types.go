package adoption

import "github.com/google/uuid"

// ---- request ----

// AdoptionChoices are the reviewer's decisions. Keys are the IDs found in the
// labels of the running containers: environments by the environment label,
// services by the service label.
type AdoptionChoices struct {
	Environments map[uuid.UUID]AdoptionEnvironmentChoice `json:"environments,omitempty" doc:"Keyed by the environment ID found on the containers"`
	Services     map[uuid.UUID]AdoptionServiceChoice     `json:"services,omitempty" doc:"Keyed by the service ID found on the containers"`
}

// AdoptionEnvironmentChoice decides where a rebuilt environment goes.
type AdoptionEnvironmentChoice struct {
	// ProjectID adopts into an existing project of the team.
	ProjectID *uuid.UUID `json:"projectId,omitempty"`
	// ProjectName creates (or shares, between environments with the same name) a
	// new project; ignored with ProjectID.
	ProjectName string `json:"projectName,omitempty"`
	Name        string `json:"name,omitempty"`
	Production  *bool  `json:"production,omitempty"`
}

// AdoptionServiceChoice decides what happens to one service.
type AdoptionServiceChoice struct {
	// Action is adopt (rebuild the service around the running containers),
	// leave (keep them running, unmanaged) or remove (the containers are removed
	// once the review is applied; volumes stay on the host).
	Action string `json:"action,omitempty" enum:"adopt,leave,remove"`
	Name   string `json:"name,omitempty"`
	// Secret forces the secret flag of a variable on or off, by key.
	Secret map[string]bool `json:"secret,omitempty"`
	// SkipHosts lists domains not to adopt.
	SkipHosts []string `json:"skipHosts,omitempty"`
}

// AdoptionPlanRequest is the body of a plan.
type AdoptionPlanRequest struct {
	Choices AdoptionChoices `json:"choices,omitempty"`
}

// AdoptionApplyRequest is the body of an adoption. The plan is recomputed from
// the host, so the result never depends on a stale review.
type AdoptionApplyRequest struct {
	Choices AdoptionChoices `json:"choices,omitempty"`
}

// ---- plan ----

// AdoptionPlan is what adopting a server would do. It carries no variable value.
type AdoptionPlan struct {
	ServerID     uuid.UUID             `json:"serverId"`
	Held         bool                  `json:"held" doc:"True while the server is held for adoption: nothing on it is changed until the review is applied"`
	Environments []AdoptionEnvironment `json:"environments"`
	Warnings     []string              `json:"warnings"`
	Blockers     []string              `json:"blockers"`
	Totals       AdoptionTotals        `json:"totals"`
}

// AdoptionTotals counts the services by action (reattach counts as adopt).
type AdoptionTotals struct {
	Adopt      int `json:"adopt"`
	Leave      int `json:"leave"`
	Remove     int `json:"remove"`
	Containers int `json:"containers"`
}

// AdoptionEnvironment is one environment found on the host. Services whose
// containers lack the environment label are listed under the nil UUID, action none.
type AdoptionEnvironment struct {
	ID            uuid.UUID         `json:"id" doc:"The environment ID found on the containers; the rebuilt environment keeps it"`
	Action        string            `json:"action" enum:"create,existing,none"`
	ProjectAction string            `json:"projectAction" enum:"create,existing,none"`
	ProjectID     *uuid.UUID        `json:"projectId,omitempty"`
	ProjectName   string            `json:"projectName"`
	ProjectSlug   string            `json:"projectSlug,omitempty"`
	Name          string            `json:"name"`
	Slug          string            `json:"slug"`
	Production    bool              `json:"production"`
	Services      []AdoptionService `json:"services"`
}

// AdoptionService is one service found on the host.
type AdoptionService struct {
	ID           uuid.UUID  `json:"id" doc:"The service ID found on the containers; the rebuilt service keeps it"`
	DeploymentID *uuid.UUID `json:"deploymentId,omitempty"`
	ReleaseID    *uuid.UUID `json:"releaseId,omitempty"`
	Slug         string     `json:"slug"`
	Name         string     `json:"name"`
	Kind         string     `json:"kind" enum:"app,worker,database,stack,route"`
	SourceType   string     `json:"sourceType"`
	Image        string     `json:"image,omitempty"`
	Action       string     `json:"action" enum:"adopt,reattach,leave,remove"`
	Adoptable    bool       `json:"adoptable"`
	// Reason says why the service cannot be adopted.
	Reason     string              `json:"reason,omitempty"`
	Replicas   int                 `json:"replicas"`
	Processes  []string            `json:"processes"`
	Containers []AdoptionContainer `json:"containers"`
	Volumes    []AdoptionVolume    `json:"volumes"`
	Domains    []AdoptionDomain    `json:"domains"`
	Env        []AdoptionVariable  `json:"env"`
	Database   *AdoptionDatabase   `json:"database,omitempty"`
	Notes      []string            `json:"notes"`

	// Kept for the apply, never serialised.
	rec *serviceRec
}

// AdoptionContainer is one container of a service.
type AdoptionContainer struct {
	Name  string `json:"name"`
	State string `json:"state"`
	// Leftover marks a container of an older deployment: it is removed after adoption, as after any deploy.
	Leftover bool `json:"leftover,omitempty"`
}

// AdoptionVolume is a Docker volume mounted by the service.
type AdoptionVolume struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
	// DockerName is the volume's name on the host when it is not the one NebulaCtrl derives.
	DockerName string `json:"dockerName,omitempty"`
}

// AdoptionDomain is one host name of the service.
type AdoptionDomain struct {
	Host       string `json:"host"`
	PathPrefix string `json:"pathPrefix,omitempty"`
	TargetPort int    `json:"targetPort,omitempty"`
	Include    bool   `json:"include"`
	Note       string `json:"note,omitempty"`

	rule *domainRec
}

// AdoptionVariable is one variable, by key: values are never part of a plan.
type AdoptionVariable struct {
	Key    string `json:"key"`
	Secret bool   `json:"secret"`
	// SecretReason says why the variable is a secret: name, url credentials, database password, choice.
	SecretReason string `json:"secretReason,omitempty"`

	value string // never serialised, never logged
}

// AdoptionDatabase is the managed database a database container becomes.
type AdoptionDatabase struct {
	Engine       string `json:"engine"`
	Version      string `json:"version"`
	Flavor       string `json:"flavor,omitempty"`
	DatabaseName string `json:"databaseName,omitempty"`
	Username     string `json:"username,omitempty"`
}

// ---- result ----

// AdoptionProject is a created project.
type AdoptionProject struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Slug string    `json:"slug"`
}

// AdoptedService is an adopted service.
type AdoptedService struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Slug          string    `json:"slug"`
	EnvironmentID uuid.UUID `json:"environmentId"`
	EnvSlug       string    `json:"envSlug"`
	ProjectID     uuid.UUID `json:"projectId"`
	ProjectSlug   string    `json:"projectSlug"`
}

// AdoptionResult is what an adoption did.
type AdoptionResult struct {
	Projects   []AdoptionProject `json:"projects"`
	Services   []AdoptedService  `json:"services"`
	Left       int               `json:"left"`
	Removed    int               `json:"removed"`
	Reattached int               `json:"reattached"`
	Warnings   []string          `json:"warnings"`
}
