package combine

import (
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// States of a combine (service_combines.state).
const (
	// StateStaged: the target carries the processes and variables of the
	// sources as staged changes; nothing was deployed, nothing was deleted.
	StateStaged = "staged"
	// StateDeploying: the deployment of the target is running.
	StateDeploying = "deploying"
	// StateCutover: the target's release serves every process; domains, tasks
	// and the route table move and the sources are deleted in one transaction.
	StateCutover = "cutover"
	// StateDone: the sources are gone, the target runs every process.
	StateDone = "done"
	// StateFailed: the deployment failed (or the cutover gave up). Nothing was
	// deleted and no domain moved; retry or abort.
	StateFailed = "failed"
	// StateAborted: the staged changes were taken back off the target.
	StateAborted = "aborted"
)

// Blocking codes of a plan. A start refuses with the first one.
const (
	CodeNotCombinable        = "not_combinable"
	CodeSourceMismatch       = "source_mismatch"
	CodeServerMismatch       = "server_mismatch"
	CodeAgentTooOld          = "agent_too_old"
	CodeServerUnavailable    = "server_unavailable"
	CodeDeploymentInProgress = "deployment_in_progress"
	CodeCombineInProgress    = "combine_in_progress"
	CodeSourceHasVolumes     = "source_has_volumes"
	CodeAlreadyHasProcesses  = "already_has_processes"
	CodeInvalidConfig        = "invalid_config"
	CodePortConflict         = "port_conflict"
	CodeTaskNameConflict     = "task_name_conflict"
	CodeSourceReferenced     = "source_referenced"
	// CodePlanStale is returned by a start whose fingerprint no longer matches.
	CodePlanStale = "plan_stale"
	// CodeVariableChoiceRequired is returned by a start that leaves a variable
	// conflict unresolved.
	CodeVariableChoiceRequired = "variable_choice_required"
	// CodeInvalidProcessNames is returned by a start whose process names are
	// missing, malformed or colliding.
	CodeInvalidProcessNames = "invalid_process_names"
	// CodeInvalidState is returned by an abort or retry the state does not allow.
	CodeInvalidState = "invalid_state"
)

// CombinePlanRequest asks for the plan of a combine.
type CombinePlanRequest struct {
	ServiceIDs      []uuid.UUID `json:"serviceIds" minItems:"2" maxItems:"10" doc:"The services to merge, 2 to 10 of one environment"`
	TargetServiceID *uuid.UUID  `json:"targetServiceId,omitempty" doc:"The service that keeps running and gains the others as processes. Default: the member with the most domains, then the oldest"`
}

// DomainRef names a domain of a member.
type DomainRef struct {
	ID       uuid.UUID `json:"id"`
	Hostname string    `json:"hostname"`
}

// Member is one service of a combine plan.
type Member struct {
	ServiceID            uuid.UUID   `json:"serviceId"`
	Slug                 string      `json:"slug"`
	Name                 string      `json:"name"`
	Kind                 string      `json:"kind"`
	Port                 int         `json:"port"`
	Command              []string    `json:"command"`
	Domains              []DomainRef `json:"domains"`
	Generation           int64       `json:"generation"`
	HasStagedChanges     bool        `json:"hasStagedChanges"`
	SuggestedProcessName string      `json:"suggestedProcessName" doc:"The process name a source gets by default; the target's own process keeps its name"`
	IsTarget             bool        `json:"isTarget"`
}

// Reference is something outside the combine that points at a source.
type Reference struct {
	FromServiceID uuid.UUID `json:"fromServiceId" doc:"The referencing service (the nil id for something that is not a service)"`
	Kind          string    `json:"kind" enum:"variable,link,route,status_page,mesh,tailnet"`
	Detail        string    `json:"detail"`
}

// CombineBlocker is a reason the combine cannot start.
type CombineBlocker struct {
	Code       string      `json:"code"`
	ServiceID  *uuid.UUID  `json:"serviceId,omitempty"`
	Message    string      `json:"message"`
	Fields     []string    `json:"fields,omitempty" doc:"source_mismatch: the settings that differ"`
	References []Reference `json:"references,omitempty" doc:"source_referenced: what still points at the service"`
}

// VariableOption is one value a conflicting variable has among the members.
type VariableOption struct {
	ServiceID uuid.UUID   `json:"serviceId" doc:"The first member that holds this value; pass it in variableChoices to keep this value"`
	Slug      string      `json:"slug"`
	Preview   string      `json:"preview" doc:"The value; empty for a secret"`
	SameAs    []uuid.UUID `json:"sameAs" doc:"The other members that hold the same value"`
}

// VariableConflict is a variable the members set to different values.
type VariableConflict struct {
	Key     string           `json:"key"`
	Secret  bool             `json:"secret"`
	Scope   string           `json:"scope" enum:"runtime,build,both"`
	Options []VariableOption `json:"options"`
}

// VariableOnly is a variable only one source has: it is copied to the target.
type VariableOnly struct {
	Key       string    `json:"key"`
	ServiceID uuid.UUID `json:"serviceId"`
	Secret    bool      `json:"secret"`
}

// VariablePlan classifies the variables of the members.
type VariablePlan struct {
	SameCount int                `json:"sameCount" doc:"Variables that every member holding them sets to the same value"`
	OnlyIn    []VariableOnly     `json:"onlyIn"`
	Conflicts []VariableConflict `json:"conflicts"`
}

// IgnoredSetting is a setting of a source the target's value replaces.
type IgnoredSetting struct {
	ServiceID uuid.UUID `json:"serviceId"`
	Field     string    `json:"field"`
	Note      string    `json:"note"`
}

// DomainMove is a domain that moves to the target at the cutover.
type DomainMove struct {
	DomainID      uuid.UUID `json:"domainId"`
	Hostname      string    `json:"hostname"`
	FromServiceID uuid.UUID `json:"fromServiceId"`
	Process       string    `json:"process" doc:"The process of the target the domain routes to"`
}

// TaskMove is a scheduled task that moves to the target at the cutover.
type TaskMove struct {
	TaskID        uuid.UUID `json:"taskId"`
	Name          string    `json:"name"`
	FromServiceID uuid.UUID `json:"fromServiceId"`
}

// CombinePlan is what a combine of the given services would do.
type CombinePlan struct {
	Fingerprint     string           `json:"fingerprint" doc:"Covers the members' generations, configs and variables: a start with another fingerprint is refused with 409 plan_stale"`
	TargetServiceID uuid.UUID        `json:"targetServiceId"`
	Members         []Member         `json:"members"`
	Blocking        []CombineBlocker `json:"blocking" doc:"Empty when the combine can start"`
	Warnings        []string         `json:"warnings"`
	Variables       VariablePlan     `json:"variables"`
	SettingsIgnored []IgnoredSetting `json:"settingsIgnored"`
	DomainMoves     []DomainMove     `json:"domainMoves"`
	TaskMoves       []TaskMove       `json:"taskMoves"`
}

// CombineRequest starts a combine.
type CombineRequest struct {
	Fingerprint        string            `json:"fingerprint" minLength:"1" doc:"The fingerprint of the plan the caller reviewed"`
	TargetServiceID    uuid.UUID         `json:"targetServiceId"`
	ServiceIDs         []uuid.UUID       `json:"serviceIds" minItems:"2" maxItems:"10" doc:"Every member, the target included"`
	ProcessNames       map[string]string `json:"processNames" doc:"The process name of every source, keyed by service id"`
	DefaultProcessName string            `json:"defaultProcessName,omitempty" doc:"Renames the target's own process; default: it keeps its name (web)"`
	VariableChoices    map[string]string `json:"variableChoices,omitempty" doc:"For every conflicting variable: the id of the member whose value is kept"`
	StopSourcesFirst   bool              `json:"stopSourcesFirst,omitempty" doc:"Stop the sources that have no domain before the deployment, so a worker or scheduler never overlaps with its successor"`
	Message            string            `json:"message,omitempty" maxLength:"200" doc:"A note shown on the deployment"`
}

// CombineView is a combine as the API returns it.
type CombineView struct {
	ID               uuid.UUID   `json:"id"`
	EnvironmentID    uuid.UUID   `json:"environmentId"`
	TargetServiceID  uuid.UUID   `json:"targetServiceId"`
	SourceServiceIDs []uuid.UUID `json:"sourceServiceIds"`
	State            string      `json:"state" enum:"staged,deploying,cutover,done,failed,aborted"`
	DeploymentID     *uuid.UUID  `json:"deploymentId"`
	Error            string      `json:"error"`
	CreatedAt        time.Time   `json:"createdAt"`
	FinishedAt       *time.Time  `json:"finishedAt"`
}

func viewOf(c db.ServiceCombine) CombineView {
	src := c.SourceServiceIds
	if src == nil {
		src = []uuid.UUID{}
	}
	return CombineView{
		ID: c.ID, EnvironmentID: c.EnvironmentID, TargetServiceID: c.TargetServiceID, SourceServiceIDs: src,
		State: c.State, DeploymentID: c.DeploymentID, Error: c.Error, CreatedAt: c.CreatedAt, FinishedAt: c.FinishedAt,
	}
}
