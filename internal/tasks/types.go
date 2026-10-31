// Package tasks implements scheduled (cron) commands, one-off commands and
// their run history. A run is an agent command (run_task): a throwaway
// container of the service's current image with the service's environment. The
// control plane stores the run, its redacted output and its exit code; variable
// values are attached at send time and never persisted.
//
// The pre-deploy command is a service config field (services.Config) executed
// by the agent as a step of the deploy command; this package only documents and
// exposes it through the run history's sibling, the deployment timeline.
package tasks

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// Run statuses.
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusTimedOut  = "timed_out"
	StatusSkipped   = "skipped"
)

// Run sources: what started a run.
const (
	SourceCron   = "cron"
	SourceManual = "manual"  // "Run now" on a task
	SourceOneOff = "one_off" // "Run command"
)

// Concurrency policies of a task.
const (
	ConcurrencyForbid = "forbid"
	ConcurrencyAllow  = "allow"
)

// Limits.
const (
	// HistoryPerTask is how many finished runs are kept per task (and for the
	// one-off runs of a service).
	HistoryPerTask = 50
	// MaxTasksPerService bounds the cron tasks of one service.
	MaxTasksPerService = 20
	// MaxRunLogLines caps the stored output of one run.
	MaxRunLogLines = 10000
	// LogTruncatedMarker is the last stored line once the cap is reached.
	LogTruncatedMarker = "… output truncated at 10000 lines"
	// MaxMessage bounds stored error messages.
	MaxMessage = 2000
)

// Audit actions.
const (
	ActionTaskCreate = "task.create"
	ActionTaskUpdate = "task.update"
	ActionTaskDelete = "task.delete"
	ActionTaskRun    = "task.run"
	ActionRunCommand = "service.run_command"
)

// Event types published on the bus (the web app invalidates its queries).
const (
	EventTaskUpdated    = "task.updated"
	EventTaskRunUpdated = "task.run.updated"
)

// TaskView is a cron task of a service.
type TaskView struct {
	ID             uuid.UUID  `json:"id"`
	ServiceID      uuid.UUID  `json:"serviceId"`
	Name           string     `json:"name"`
	Schedule       string     `json:"schedule" doc:"5-field cron expression or @macro, evaluated in timezone."`
	Timezone       string     `json:"timezone" doc:"IANA time zone the schedule is evaluated in (default UTC). A daylight-saving gap skips that day's run; a repeated hour runs once."`
	Command        []string   `json:"command" doc:"Argv, run without a shell."`
	Enabled        bool       `json:"enabled"`
	TimeoutSeconds int        `json:"timeoutSeconds"`
	Concurrency    string     `json:"concurrency" enum:"forbid,allow"`
	NextRunAt      *time.Time `json:"nextRunAt,omitempty"`
	LastRunAt      *time.Time `json:"lastRunAt,omitempty"`
	LastRun        *RunView   `json:"lastRun,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

// RunView is one execution of a task or a one-off command.
type RunView struct {
	ID             uuid.UUID  `json:"id"`
	ServiceID      uuid.UUID  `json:"serviceId"`
	TaskID         *uuid.UUID `json:"taskId,omitempty"`
	Name           string     `json:"name" doc:"The task's name, or \"one-off\" for a command."`
	Source         string     `json:"source" doc:"cron, manual (Run now), one_off (run command) or a feature such as preview_seed."`
	Command        []string   `json:"command"`
	Status         string     `json:"status" enum:"queued,running,succeeded,failed,timed_out,skipped"`
	ExitCode       *int       `json:"exitCode,omitempty"`
	Error          string     `json:"error,omitempty" doc:"Why the run failed, timed out or was skipped."`
	TimeoutSeconds int        `json:"timeoutSeconds"`
	ActorLabel     string     `json:"actorLabel,omitempty"`
	ScheduledFor   *time.Time `json:"scheduledFor,omitempty"`
	LogLines       int        `json:"logLines"`
	CreatedAt      time.Time  `json:"createdAt"`
	StartedAt      *time.Time `json:"startedAt,omitempty"`
	FinishedAt     *time.Time `json:"finishedAt,omitempty"`
}

// LogLineView is one stored output line.
type LogLineView struct {
	ID        int64     `json:"id"`
	Stream    string    `json:"stream"`
	Line      string    `json:"line"`
	CreatedAt time.Time `json:"createdAt"`
}

// SchedulePreview describes a cron expression for the editor.
type SchedulePreview struct {
	Valid       bool        `json:"valid"`
	Error       string      `json:"error,omitempty"`
	Description string      `json:"description,omitempty" doc:"Human-readable summary naming the time zone."`
	NextRuns    []time.Time `json:"nextRuns" doc:"The next runs as UTC instants."`
	Timezone    string      `json:"timezone,omitempty" doc:"The normalized zone the preview was computed in."`
}

// TaskInput creates a task.
type TaskInput struct {
	Name           string
	Schedule       string
	Timezone       string
	Command        []string
	Enabled        *bool
	TimeoutSeconds int
	Concurrency    string
}

// TaskPatch changes a task; nil fields stay as they are.
type TaskPatch struct {
	Name           *string
	Schedule       *string
	Timezone       *string
	Command        *[]string
	Enabled        *bool
	TimeoutSeconds *int
	Concurrency    *string
}

// RunOpts configures RunOneOff.
type RunOpts struct {
	// Source records what started the run (default "one_off"), e.g. "preview_seed".
	Source string
	// Timeout bounds the run (0 = 30 minutes).
	Timeout time.Duration
	// ActorLabel names who triggered it (default: the caller's label, else "system").
	ActorLabel string
}

func decodeArgv(raw json.RawMessage) []string {
	var out []string
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = []string{}
	}
	return out
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func taskView(t db.ServiceTask) TaskView {
	return TaskView{
		ID: t.ID, ServiceID: t.ServiceID, Name: t.Name, Schedule: t.Schedule, Timezone: t.Timezone, Command: decodeArgv(t.Command),
		Enabled: t.Enabled, TimeoutSeconds: int(t.TimeoutSeconds), Concurrency: t.Concurrency,
		NextRunAt: utc(t.NextRunAt), LastRunAt: utc(t.LastRunAt), CreatedAt: t.CreatedAt.UTC(), UpdatedAt: t.UpdatedAt.UTC(),
	}
}

func runView(r db.TaskRun, status string) RunView {
	v := RunView{
		ID: r.ID, ServiceID: r.ServiceID, TaskID: r.TaskID, Name: r.Name, Source: r.Source, Command: decodeArgv(r.Command),
		Status: status, Error: r.Error, TimeoutSeconds: int(r.TimeoutSeconds), ActorLabel: r.ActorLabel,
		ScheduledFor: utc(r.ScheduledFor), LogLines: int(r.LogLines), CreatedAt: r.CreatedAt.UTC(), StartedAt: utc(r.StartedAt), FinishedAt: utc(r.FinishedAt),
	}
	if r.ExitCode != nil {
		c := int(*r.ExitCode)
		v.ExitCode = &c
	}
	return v
}
