package fleet

import (
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/deployments"
)

// Drain move statuses.
const (
	MovePending   = "pending"
	MoveMoving    = "moving" // placement is being changed
	MoveDeploying = "deploying"
	MoveDone      = "done"
	MoveFailed    = "failed"
)

// Drain run states (server_drains.state).
const (
	RunRunning   = "running"
	RunCompleted = "completed"
	RunFailed    = "failed"
	RunCanceled  = "canceled"
)

// Move is one service's journey off the drained server. It is stored in
// server_drains.moves and returned by the API.
type Move struct {
	ServiceID        uuid.UUID  `json:"serviceId"`
	ServiceName      string     `json:"serviceName"`
	ProjectName      string     `json:"projectName"`
	EnvironmentName  string     `json:"environmentName"`
	SourceServerID   uuid.UUID  `json:"sourceServerId"`
	TargetServerID   uuid.UUID  `json:"targetServerId"`
	TargetServerName string     `json:"targetServerName"`
	Stateful         bool       `json:"stateful"`
	Status           string     `json:"status" enum:"pending,moving,deploying,done,failed"`
	DeploymentID     *uuid.UUID `json:"deploymentId"`
	// DeploymentStatus is the live status of the move's deployment (read time).
	DeploymentStatus string `json:"deploymentStatus,omitempty"`
	Message          string `json:"message"`
}

// storedMove is a Move as persisted: it also remembers the service generations
// around the placement change, so a failed move can put the service back
// exactly as it was.
type storedMove struct {
	Move
	GenerationBefore int64 `json:"generationBefore"`
	GenerationAfter  int64 `json:"generationAfter"`
}

// DrainView is the API view of a drain run.
type DrainView struct {
	ID              uuid.UUID  `json:"id"`
	ServerID        uuid.UUID  `json:"serverId"`
	State           string     `json:"state" enum:"running,completed,failed,canceled"`
	Message         string     `json:"message"`
	CancelRequested bool       `json:"cancelRequested"`
	ActorLabel      string     `json:"actorLabel"`
	Moves           []Move     `json:"moves"`
	CreatedAt       time.Time  `json:"createdAt"`
	FinishedAt      *time.Time `json:"finishedAt"`
}

// PlanMove is one proposed move of a drain plan.
type PlanMove struct {
	ServiceID        uuid.UUID                  `json:"serviceId"`
	ServiceName      string                     `json:"serviceName"`
	ProjectName      string                     `json:"projectName"`
	EnvironmentName  string                     `json:"environmentName"`
	TargetServerID   *uuid.UUID                 `json:"targetServerId"`
	TargetServerName string                     `json:"targetServerName"`
	Stateful         bool                       `json:"stateful" doc:"The service has volumes or is a database"`
	Warnings         []string                   `json:"warnings"`
	Blocking         []deployments.PlanBlocking `json:"blocking"`
}

// Capacity summarizes whether the targets can hold the planned moves.
type Capacity struct {
	TargetFits bool     `json:"targetFits"`
	Reasons    []string `json:"reasons"`
}

// DrainPlan is the dry-run result of POST /servers/{id}/drain/plan.
type DrainPlan struct {
	Moves    []PlanMove `json:"moves"`
	Capacity Capacity   `json:"capacity"`
}

// MoveInput selects where one service goes.
type MoveInput struct {
	ServiceID      uuid.UUID `json:"serviceId"`
	TargetServerID uuid.UUID `json:"targetServerId"`
}

// StartInput starts a drain.
type StartInput struct {
	Moves           []MoveInput
	ConfirmStateful bool
}

// FleetService is one service row of a server's column in the fleet overview.
type FleetService struct {
	ID              uuid.UUID `json:"id"`
	Name            string    `json:"name"`
	Slug            string    `json:"slug"`
	ProjectID       uuid.UUID `json:"projectId"`
	ProjectName     string    `json:"projectName"`
	EnvironmentID   uuid.UUID `json:"environmentId"`
	EnvironmentName string    `json:"environmentName"`
	EnvironmentSlug string    `json:"environmentSlug"`
	Kind            string    `json:"kind" enum:"app,worker,database"`
	Status          string    `json:"status" enum:"undeployed,deploying,running,unhealthy,stopped,missing,failed,unknown" doc:"Latest deployment while one is active or failed, otherwise the observed container state"`
	CPUPercent      float64   `json:"cpuPercent"`
	MemBytes        int64     `json:"memBytes"`
	MemLimitBytes   int64     `json:"memLimitBytes" doc:"Configured memory limit, 0 when unlimited"`
	Stateful        bool      `json:"stateful"`
}

// FleetServer is one server column of the fleet overview.
type FleetServer struct {
	ID                uuid.UUID      `json:"id"`
	Name              string         `json:"name"`
	Status            string         `json:"status" enum:"pending,online,offline,revoked"`
	DrainState        string         `json:"drainState" enum:"none,draining,drained,failed"`
	DrainMessage      string         `json:"drainMessage"`
	CPUs              int            `json:"cpus"`
	MemTotalBytes     int64          `json:"memTotalBytes"`
	DiskTotalBytes    int64          `json:"diskTotalBytes"`
	CPUPercent        float64        `json:"cpuPercent"`
	MemUsedBytes      int64          `json:"memUsedBytes"`
	DiskUsedBytes     int64          `json:"diskUsedBytes"`
	CommittedMemBytes int64          `json:"committedMemBytes" doc:"Sum of the memory limits of the services placed on the server"`
	UnlimitedServices int            `json:"unlimitedServices" doc:"Services on the server without a memory limit"`
	Overcommitted     bool           `json:"overcommitted" doc:"Committed limits exceed the host's memory"`
	Services          []FleetService `json:"services"`
}

// FleetTotals sums the fleet.
type FleetTotals struct {
	Servers           int   `json:"servers"`
	Services          int   `json:"services"`
	MemTotalBytes     int64 `json:"memTotalBytes"`
	MemUsedBytes      int64 `json:"memUsedBytes"`
	CommittedMemBytes int64 `json:"committedMemBytes"`
}

// Overview is the response of GET /fleet.
type Overview struct {
	Servers []FleetServer `json:"servers"`
	Totals  FleetTotals   `json:"totals"`
}
