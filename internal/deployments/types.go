package deployments

import (
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/buildplacement"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
	"github.com/nebulactrl/nebulactrl/pkg/composestack"
)

// Deployment triggers.
const (
	TriggerManual     = "manual"
	TriggerRedeploy   = "redeploy"
	TriggerRollback   = "rollback"
	TriggerStaged     = "staged"
	TriggerReconcile  = "reconcile"
	TriggerDrain      = "drain"
	TriggerGitPush    = "git_push"
	TriggerPreview    = "preview"
	TriggerCron       = "cron"
	TriggerDeployHook = "deploy_hook"
)

// RollbackDisclaimer is shown wherever a rollback is offered or confirmed.
const RollbackDisclaimer = "Application image and configuration only. Databases, volumes and external services are not rolled back."

// DeploymentView is the API representation of a deployment.
type DeploymentView struct {
	ID        uuid.UUID  `json:"id"`
	ServiceID uuid.UUID  `json:"serviceId"`
	ReleaseID *uuid.UUID `json:"releaseId"`
	ServerID  *uuid.UUID `json:"serverId"`
	Trigger   string     `json:"trigger" enum:"manual,redeploy,rollback,staged,reconcile,drain,git_push,preview,cron,deploy_hook"`
	ActorType string     `json:"actorType"`
	// ActorLabel is who started the deployment, ready to display: the user's
	// name (or email), the API token's name, "MCP · <token name>" or "System".
	ActorLabel     string       `json:"actorLabel"`
	Status         string       `json:"status"`
	Strategy       string       `json:"strategy" enum:"rolling,recreate,route_only"`
	StatusMessage  string       `json:"statusMessage"`
	RolledBackFrom *uuid.UUID   `json:"rolledBackFrom"`
	Generation     int64        `json:"generation"`
	CreatedAt      time.Time    `json:"createdAt"`
	StartedAt      *time.Time   `json:"startedAt"`
	FinishedAt     *time.Time   `json:"finishedAt"`
	Release        *ReleaseView `json:"release"`
	// Message is the optional note given when the deployment was requested.
	Message    *string     `json:"message"`
	WaitingFor *WaitingFor `json:"waitingFor"`
	// Build describes how a git deployment was built (null for images).
	Build *agentprotocol.BuildInfo `json:"build"`
	// Commit is the git commit the deployment builds (null for image services
	// and deployments created before the commit was known).
	Commit *CommitView `json:"commit"`
	// CancelRequestedAt is when someone asked to cancel a deployment the agent
	// already runs (null otherwise). The status changes when the agent has
	// aborted; a request that came too late leaves the deployment to finish.
	CancelRequestedAt *time.Time `json:"cancelRequestedAt"`
	// CancelRequestedBy is who asked, ready to display.
	CancelRequestedBy string `json:"cancelRequestedBy"`
	// CanceledAtStep is the deploy step the agent was in when a canceled
	// deployment stopped (pull, build, predeploy, create, start, health, route,
	// verify or stop; "queued" before it started).
	CanceledAtStep string `json:"canceledAtStep"`
	// BuildServerID is the build server that built the release's image (null:
	// built on the target, or nothing was built); BuildServerName is its name.
	BuildServerID   *uuid.UUID `json:"buildServerId"`
	BuildServerName string     `json:"buildServerName,omitempty"`
	// NoCache is true when the deployment was requested without build cache
	// (--no-cache --pull, no reuse of an earlier build server image).
	NoCache bool `json:"noCache,omitempty"`
}

// CommitView is the commit a git deployment ships. Message and author are only
// known for deployments triggered by a push (git_push) or a pull request.
type CommitView struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Author  string `json:"author"`
	Branch  string `json:"branch"`
}

// ReleaseView summarizes the release a deployment ships.
type ReleaseView struct {
	ID        uuid.UUID              `json:"id"`
	ImageRef  string                 `json:"imageRef"`
	Source    services.ReleaseSource `json:"source"`
	CreatedAt time.Time              `json:"createdAt"`
}

// WaitingFor is set while an in-flight deployment depends on an offline server.
type WaitingFor struct {
	ServerID     uuid.UUID  `json:"serverId"`
	ServerName   string     `json:"serverName"`
	OfflineSince *time.Time `json:"offlineSince"`
}

// RollbackView is the response of a rollback.
type RollbackView struct {
	DeploymentView
	Disclaimer string `json:"disclaimer"`
}

// EventView is one entry of a deployment's timeline.
type EventView struct {
	ID        int64     `json:"id"`
	Step      string    `json:"step"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

// LogLineView is one persisted build/deploy log line.
type LogLineView struct {
	ID        int64     `json:"id"`
	Stream    string    `json:"stream"`
	Line      string    `json:"line"`
	CreatedAt time.Time `json:"createdAt"`
}

// PlanBlocking is a problem that prevents deploying.
type PlanBlocking struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Violations are the compose problems of an `invalid_compose` blocker.
	Violations []composestack.Violation `json:"violations,omitempty"`
}

// PlanSource describes what a deployment would ship.
type PlanSource struct {
	Type      string `json:"type"`
	Image     string `json:"image,omitempty"`
	GitURL    string `json:"gitUrl,omitempty"`
	Branch    string `json:"branch,omitempty"`
	CommitSHA string `json:"commitSha,omitempty"`
}

// Plan is the dry-run result of POST /services/{id}/deployments/plan.
type Plan struct {
	// Strategy is rolling when any process rolls, else recreate (a single-process
	// service has one).
	Strategy string                 `json:"strategy" enum:"rolling,recreate,route_only"`
	Reasons  []string               `json:"reasons"`
	Warnings []string               `json:"warnings"`
	Changes  []services.FieldChange `json:"changes"`
	Source   PlanSource             `json:"source"`
	Server   *services.ServerRef    `json:"server"`
	Blocking []PlanBlocking         `json:"blocking"`
	// Placement says where a git source is built (null for other sources).
	Placement *PlacementView `json:"placement"`
	// Mesh is what reaching providers on other servers needs: the links, the
	// tailnet devices a deploy adds (deploying is the confirmation) and blockers.
	Mesh services.MeshPlan `json:"mesh"`
	// Processes lists the processes of a service with several in rollout order;
	// empty for a single-process service.
	Processes []PlanProcess `json:"processes"`
}

// PlacementView is the resolved build placement of a plan.
type PlacementView struct {
	Location        string     `json:"location" enum:"target,build_server"`
	BuildServerID   *uuid.UUID `json:"buildServerId"`
	BuildServerName string     `json:"buildServerName"`
	RegistryID      *uuid.UUID `json:"registryId"`
	RegistryHost    string     `json:"registryHost"`
	Platform        string     `json:"platform"`
	// Reason is the human text for how the placement was chosen.
	Reason string `json:"reason"`
	// Sticky is true when the build server was chosen by the sticky hash, not pinned.
	Sticky   bool `json:"sticky"`
	Emulated bool `json:"emulated"`
	Fallback bool `json:"fallback"`
	// Reuse is true when a live registry image of the same inputs exists: no build runs.
	Reuse         bool `json:"reuse"`
	MaxConcurrent int  `json:"maxConcurrent"`
	// ActiveBuilds is the number of builds running or queued on the build server.
	ActiveBuilds int                      `json:"activeBuilds"`
	Warnings     []buildplacement.Warning `json:"warnings"`
}

// MaxLogLines mirrors the hub's per-deployment persistence cap.
const MaxLogLines = 10000

// MaxDebugLogLines mirrors the hub's per-deployment cap of debug-stream lines.
const MaxDebugLogLines = 500

// EnvDeploymentView is a deployment in an environment-wide list.
type EnvDeploymentView struct {
	DeploymentView
	ServiceName string `json:"serviceName"`
	ServiceSlug string `json:"serviceSlug"`
}

// StagedResult is the response of deploy-staged: the deployments created and
// the order the services were started in (providers of references first).
type StagedResult struct {
	Items []DeploymentView `json:"items"`
	Order []uuid.UUID      `json:"order"`
}

// MaxMessageLen bounds the optional deployment message.
const MaxMessageLen = 200
