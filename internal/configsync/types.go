// Package configsync turns a nebula.toml project file into staged and
// immediate platform changes: it validates, plans and applies the file to one
// environment, tracks which fields the file owns (and when the dashboard has
// drifted away from it) and exports live state back into a file.
//
// The file format itself (strict parsing, merge, static validation, rendering)
// lives in internal/projectconfig; this package adds everything that needs the
// database or the platform use cases.
package configsync

import (
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
)

// Diagnostic types are the shared diagnostics of the file format.
type (
	// Diagnostic is one finding about the file or the plan.
	Diagnostic = projectconfig.Diagnostic
	// Diagnostics is a list of findings.
	Diagnostics = projectconfig.Diagnostics
)

// Source kinds of a file.
const (
	SourceInline = "inline"
	SourceGit    = "git"
)

// Origins of an apply, recorded in config_sources.origin.
const (
	OriginGit = "git"
	OriginCLI = "cli"
	OriginAPI = "api"
	OriginMCP = "mcp"
	OriginUI  = "ui"
)

// Plan actions of a service.
const (
	ActionCreate    = "create"
	ActionUpdate    = "update"
	ActionUnchanged = "unchanged"
	ActionNotInFile = "not_in_file"
	ActionBlocked   = "blocked"
)

// Change operations.
const (
	OpSet     = "set"
	OpAdd     = "add"
	OpRemove  = "remove"
	OpKeep    = "keep"
	OpRelease = "release"
)

// When a change takes effect.
const (
	AppliesStaged    = "staged"
	AppliesImmediate = "immediate"
	AppliesDeferred  = "deferred"
)

// Why a change is in the plan.
const (
	ReasonNew         = "new"
	ReasonAdopt       = "adopt"
	ReasonFileChanged = "file_changed"
	ReasonDrift       = "drift"
	ReasonConflict    = "conflict"
)

// Drift states of a service or field (worst first: conflict > drift > file_changed > in_sync).
const (
	StateInSync      = "in_sync"
	StateFileChanged = "file_changed"
	StateDrift       = "drift"
	StateConflict    = "conflict"
	StateNew         = "new"
	StateAdopt       = "adopt"
)

// Apply statuses and step statuses.
const (
	StatusStaged  = "staged"
	StatusPartial = "partial"
	StatusFailed  = "failed"
	StatusNoop    = "noop"

	StepApplied      = "applied"
	StepUnchanged    = "unchanged"
	StepFailed       = "failed"
	StepNotAttempted = "not_attempted"
	StepDeferred     = "deferred"
)

// Environment statuses (environment_config_settings.status).
const (
	EnvUnknown     = "unknown"
	EnvInSync      = "in_sync"
	EnvDiffers     = "differs"
	EnvInvalid     = "invalid"
	EnvNotFound    = "not_found"
	EnvUnreachable = "unreachable"
	EnvError       = "error"
)

// Guard values of Plan.Guard.ProductionApproval.
const (
	ApprovalRequired    = "required"
	ApprovalNotRequired = "not_required"
)

// Mask replaces secret and generated values in plans.
const Mask = "••••••"

// SourceInfo says where a file came from.
type SourceInfo struct {
	Kind   string `json:"kind" enum:"inline,git" doc:"inline for a file sent with the request, git for a file read through the environment's repository binding"`
	Repo   string `json:"repo,omitempty" doc:"Repository (owner/name) of a git file"`
	Branch string `json:"branch,omitempty" doc:"Branch of a git file"`
	Path   string `json:"path,omitempty" doc:"Path of the file in the repository"`
	Commit string `json:"commit,omitempty" doc:"Commit the file was read at"`
	// Origin is who applies the file: git, cli, api, mcp or ui. Empty derives it from the caller.
	Origin string `json:"origin,omitempty" enum:"git,cli,api,mcp,ui" doc:"Where an apply comes from; empty derives it from the caller"`
}

// Input is a nebula.toml text plus the files it refers to.
type Input struct {
	File        string            `json:"file" doc:"The nebula.toml text"`
	Attachments map[string]string `json:"attachments,omitempty" doc:"Files the file refers to (compose_file, files.*.source), keyed by their relative path"`
	Source      SourceInfo        `json:"source"`
}

// PlanRequest is the body of a plan.
type PlanRequest struct {
	Environment string   `json:"environment" doc:"Environment slug (or id) to plan against; may be empty when the project has exactly one standard environment"`
	Input       Input    `json:"input"`
	Only        []string `json:"only,omitempty" doc:"Restrict the plan to these file key paths (prefixes) or field ids"`
	Prune       bool     `json:"prune,omitempty" doc:"Also delete sub-resources the file owned before and no longer lists"`
}

// ApplyRequest is the body of an apply.
type ApplyRequest struct {
	PlanRequest
	PlanID string `json:"planId,omitempty" doc:"Plan id from a previous plan; the apply fails with plan_stale when anything changed since"`
	Deploy bool   `json:"deploy,omitempty" doc:"Deploy the services this apply touched that now have staged changes"`
}

// ValidateService is one service of a validated file.
type ValidateService struct {
	Slug string `json:"slug"`
	Kind string `json:"kind" enum:"git,image,database,route,stack"`
}

// ValidateResult is the answer to a validate call (HTTP 200 even when invalid).
type ValidateResult struct {
	Valid        bool              `json:"valid"`
	Schema       int               `json:"schema"`
	Diagnostics  Diagnostics       `json:"diagnostics"`
	Environments []string          `json:"environments" doc:"Environment blocks of the file"`
	Services     []ValidateService `json:"services"`
}

// PlanProject names the project of a plan.
type PlanProject struct {
	ID   uuid.UUID `json:"id"`
	Slug string    `json:"slug"`
}

// PlanEnvironment names the environment of a plan.
type PlanEnvironment struct {
	ID           uuid.UUID `json:"id"`
	Slug         string    `json:"slug"`
	IsProduction bool      `json:"isProduction"`
}

// PlanSource describes the file a plan was computed from.
type PlanSource struct {
	Kind       string `json:"kind" enum:"inline,git"`
	Repo       string `json:"repo,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Path       string `json:"path,omitempty"`
	Commit     string `json:"commit,omitempty"`
	FileSHA256 string `json:"fileSha256"`
	Bytes      int    `json:"bytes"`
}

// PlanSummary counts what a plan does.
type PlanSummary struct {
	Create         int      `json:"create"`
	Update         int      `json:"update"`
	Unchanged      int      `json:"unchanged"`
	NotInFile      int      `json:"notInFile"`
	Adopted        int      `json:"adopted"`
	Staged         int      `json:"staged"`
	Immediate      int      `json:"immediate"`
	Deferred       int      `json:"deferred"`
	Pruned         int      `json:"pruned"`
	Errors         int      `json:"errors"`
	Warnings       int      `json:"warnings"`
	RequiresDeploy []string `json:"requiresDeploy" doc:"Slugs of services whose staged changes need a deploy"`
}

// PlanChange is one field the apply would write.
type PlanChange struct {
	Path    string `json:"path" doc:"File key path of the change"`
	Line    int    `json:"line,omitempty" doc:"1-based line of the key in the file"`
	Field   string `json:"field" doc:"Field id (the namespace of pending-changes)"`
	Op      string `json:"op" enum:"set,add,remove,keep,release"`
	Applies string `json:"applies" enum:"staged,immediate,deferred"`
	Reason  string `json:"reason" enum:"new,adopt,file_changed,drift,conflict"`
	From    string `json:"from"`
	To      string `json:"to"`
	Masked  bool   `json:"masked" doc:"True when from and to are masked because the field is secret or generated"`

	// Fingerprints of the base (last applied), live and desired values; never serialised.
	baseFP, liveFP, desiredFP string
}

// PlanService is the plan of one service.
type PlanService struct {
	Slug       string         `json:"slug"`
	Kind       string         `json:"kind,omitempty" enum:"git,image,database,route,stack"`
	Action     string         `json:"action" enum:"create,update,unchanged,not_in_file,blocked"`
	ServiceID  *uuid.UUID     `json:"serviceId,omitempty"`
	Generation int64          `json:"generation,omitempty"`
	Managed    bool           `json:"managed"`
	State      string         `json:"state,omitempty" enum:"in_sync,file_changed,drift,conflict,new,adopt"`
	Note       string         `json:"note,omitempty"`
	Changes    []PlanChange   `json:"changes"`
	Released   []string       `json:"released,omitempty" doc:"File key paths the file no longer lists; they stop being owned and keep their live value"`
	Unmanaged  map[string]int `json:"unmanaged,omitempty" doc:"Live members of collections that the file does not mention (counts only)"`
	Blockers   []string       `json:"blockers,omitempty" doc:"Codes of the errors that block this service"`
}

// SharedPlan holds the changes of the shared variable layers.
type SharedPlan struct {
	Project     []PlanChange `json:"project"`
	Environment []PlanChange `json:"environment"`
}

// Guard tells whether an apply of the plan needs a human approval from a guarded principal.
type Guard struct {
	ProductionApproval string `json:"productionApproval" enum:"required,not_required"`
	Reason             string `json:"reason,omitempty"`
}

// Plan is what apply would do to one environment.
type Plan struct {
	PlanID         string          `json:"planId"`
	Schema         int             `json:"schema"`
	Valid          bool            `json:"valid"`
	Project        PlanProject     `json:"project"`
	Environment    PlanEnvironment `json:"environment"`
	Source         PlanSource      `json:"source"`
	Summary        PlanSummary     `json:"summary"`
	Diagnostics    Diagnostics     `json:"diagnostics"`
	Services       []PlanService   `json:"services"`
	Shared         SharedPlan      `json:"shared"`
	RequiredScopes []string        `json:"requiredScopes"`
	Guard          Guard           `json:"guard"`
}

// ApplyStep is one step of an apply.
type ApplyStep struct {
	Step    string `json:"step" doc:"service.create:web, config:web, variables:web, domains:web, processes:web, volumes:web, networks:web, links:web, shared:project, shared:environment, scale:web, scale:web:worker, git:web, uptime:web, tasks:web:digest, backups:db:nightly, dns:web:shop.acme.com, ownership:web or deploy"`
	Status  string `json:"status" enum:"applied,unchanged,failed,not_attempted,deferred"`
	Message string `json:"message,omitempty"`
}

// AppliedService is a service an apply created or changed.
type AppliedService struct {
	Slug       string    `json:"slug"`
	ServiceID  uuid.UUID `json:"serviceId"`
	Action     string    `json:"action" enum:"create,update,unchanged"`
	Generation int64     `json:"generation"`
}

// ApplyResult is the outcome of an apply.
type ApplyResult struct {
	PlanID      string           `json:"planId"`
	Status      string           `json:"status" enum:"staged,partial,failed,noop"`
	Steps       []ApplyStep      `json:"steps"`
	Services    []AppliedService `json:"services"`
	Deployments []uuid.UUID      `json:"deployments"`
	Plan        Plan             `json:"plan"`
}

// ExportResult is a generated nebula.toml.
type ExportResult struct {
	File     string      `json:"file" doc:"The nebula.toml text"`
	Warnings Diagnostics `json:"warnings" doc:"Things the file cannot express and findings the first apply would report"`
}

// FieldState is the drift state of one owned field.
type FieldState struct {
	Field string `json:"field"`
	State string `json:"state" enum:"in_sync,file_changed,drift,conflict"`
}

// ServiceConfigSource says whether a service is managed by a file and how far it drifted.
type ServiceConfigSource struct {
	Managed    bool         `json:"managed"`
	State      string       `json:"state,omitempty" enum:"in_sync,file_changed,drift,conflict"`
	Origin     string       `json:"origin,omitempty" enum:"git,cli,api,mcp,ui"`
	Repo       string       `json:"repo,omitempty"`
	Branch     string       `json:"branch,omitempty"`
	Path       string       `json:"path,omitempty"`
	Commit     string       `json:"commit,omitempty"`
	FileSHA256 string       `json:"fileSha256,omitempty"`
	AppliedAt  *time.Time   `json:"appliedAt,omitempty"`
	AppliedBy  string       `json:"appliedBy,omitempty"`
	Fields     []FieldState `json:"fields"`
	Pending    []string     `json:"pending" doc:"Immediate changes that could not run yet (the service is not deployed)"`
}

// LastPlan is the slim form of a plan stored in environment_config_settings.last_plan:
// per service the action, state and the desired fingerprint of every changed field (no values).
type LastPlan struct {
	// Generations is a digest of the environment's service generations, set by the git integration.
	Generations string                     `json:"generations,omitempty"`
	Services    map[string]LastPlanService `json:"services"`
}

// LastPlanService is one service of a LastPlan.
type LastPlanService struct {
	Action  string            `json:"action"`
	State   string            `json:"state"`
	Changes int               `json:"changes"`
	Fields  map[string]string `json:"fields"`
}

// NewLastPlan builds the stored form of p.
func NewLastPlan(p Plan) LastPlan {
	lp := LastPlan{Services: make(map[string]LastPlanService, len(p.Services))}
	for _, s := range p.Services {
		e := LastPlanService{Action: s.Action, State: s.State, Changes: len(s.Changes), Fields: make(map[string]string, len(s.Changes))}
		for _, c := range s.Changes {
			e.Fields[c.Field] = c.desiredFP
		}
		lp.Services[s.Slug] = e
	}
	return lp
}
