package tasks

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

type serviceIDInput struct {
	ID uuid.UUID `path:"serviceId"`
}

type taskIDInput struct {
	ID uuid.UUID `path:"taskId"`
}

type runIDInput struct {
	ID uuid.UUID `path:"runId"`
}

type taskOutput struct{ Body TaskView }
type runOutput struct{ Body RunView }

type taskListOutput struct {
	Body struct {
		Items []TaskView `json:"items"`
	}
}

type runListOutput struct {
	Body struct {
		Items []RunView `json:"items"`
	}
}

type createTaskInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		Name           string   `json:"name" minLength:"1" maxLength:"64"`
		Schedule       string   `json:"schedule" doc:"5-field cron expression (minute hour day-of-month month day-of-week) or @hourly/@daily/@weekly/@monthly/@yearly, evaluated in timezone." example:"*/15 * * * *"`
		Timezone       string   `json:"timezone,omitempty" maxLength:"64" doc:"IANA time zone the schedule is evaluated in, e.g. Europe/Berlin. Default UTC." example:"Europe/Berlin"`
		Command        []string `json:"command" minItems:"1" doc:"Argv, run without a shell in a container of the service's current image. Wrap in [\"sh\",\"-c\",…] for shell syntax."`
		Enabled        *bool    `json:"enabled,omitempty" doc:"Default true."`
		TimeoutSeconds int      `json:"timeoutSeconds,omitempty" minimum:"0" maximum:"86400" doc:"Default 1800 (30 minutes)."`
		Concurrency    string   `json:"concurrency,omitempty" enum:"forbid,allow" doc:"forbid (default) skips a tick while the previous run is still active."`
	}
}

type patchTaskInput struct {
	ID   uuid.UUID `path:"taskId"`
	Body struct {
		Name           *string   `json:"name,omitempty" minLength:"1" maxLength:"64"`
		Schedule       *string   `json:"schedule,omitempty"`
		Timezone       *string   `json:"timezone,omitempty" maxLength:"64" doc:"IANA zone; changing it recomputes the next run."`
		Command        *[]string `json:"command,omitempty"`
		Enabled        *bool     `json:"enabled,omitempty"`
		TimeoutSeconds *int      `json:"timeoutSeconds,omitempty" minimum:"1" maximum:"86400"`
		Concurrency    *string   `json:"concurrency,omitempty" enum:"forbid,allow"`
	}
}

type runCommandInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		Command        []string `json:"command" minItems:"1" doc:"Argv, run without a shell. Wrap in [\"sh\",\"-c\",…] for shell syntax."`
		TimeoutSeconds int      `json:"timeoutSeconds,omitempty" minimum:"0" maximum:"86400" doc:"Default 1800 (30 minutes)."`
	}
}

type listRunsInput struct {
	ID     uuid.UUID `path:"serviceId"`
	TaskID string    `query:"taskId" format:"uuid" required:"false" doc:"Only the runs of this task."`
	Limit  int       `query:"limit" minimum:"0" maximum:"200" default:"50"`
}

type runLogsInput struct {
	ID     uuid.UUID `path:"runId"`
	Follow bool      `query:"follow" doc:"Stream lines as server-sent events until the run finishes"`
}

type previewInput struct {
	Body struct {
		Schedule string `json:"schedule" maxLength:"200"`
		Timezone string `json:"timezone,omitempty" maxLength:"64" doc:"IANA zone the schedule is evaluated in (default UTC)."`
		Count    int    `json:"count,omitempty" minimum:"0" maximum:"20" doc:"How many upcoming runs to return (default 5)."`
	}
}

type previewOutput struct{ Body SchedulePreview }

// Register attaches the task endpoints.
func Register(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}
	tag := []string{"tasks"}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listServiceTasks", Method: http.MethodGet, Path: "/services/{serviceId}/tasks", Tags: tag,
		Summary: "List a service's cron tasks", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceIDInput) (*taskListOutput, error) {
		items, err := svc.ListTasks(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &taskListOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createServiceTask", Method: http.MethodPost, Path: "/services/{serviceId}/tasks", Tags: tag,
		Summary: "Schedule a recurring command", DefaultStatus: http.StatusCreated,
		Description: "A cron task runs its command in a throwaway container of the service's current image, with the service's " +
			"environment and networks, on the service's server. The schedule is evaluated in `timezone` (IANA name, default UTC); one run at most per minute. Keeps the last 50 runs.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}), policy.ScopeProjectsWrite), func(ctx context.Context, in *createTaskInput) (*taskOutput, error) {
		v, err := svc.CreateTask(ctx, in.ID, TaskInput(in.Body))
		if err != nil {
			return nil, err
		}
		return &taskOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateServiceTask", Method: http.MethodPatch, Path: "/tasks/{taskId}", Tags: tag,
		Summary: "Update a cron task", Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *patchTaskInput) (*taskOutput, error) {
		v, err := svc.UpdateTask(ctx, in.ID, TaskPatch(in.Body))
		if err != nil {
			return nil, err
		}
		return &taskOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteServiceTask", Method: http.MethodDelete, Path: "/tasks/{taskId}", Tags: tag,
		Summary: "Delete a cron task and its run history", DefaultStatus: http.StatusNoContent, Errors: errs,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *taskIDInput) (*struct{}, error) {
		return nil, svc.DeleteTask(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "runServiceTask", Method: http.MethodPost, Path: "/tasks/{taskId}/run", Tags: tag,
		Summary: "Run a cron task now", DefaultStatus: http.StatusAccepted,
		Description: "Starts a run immediately, whatever the schedule and concurrency policy. Returns 202 with the queued run; follow it " +
			"through `GET /task-runs/{runId}/logs?follow=true`. 409 `not_deployed` / `server_offline` when it can not start.",
		Errors: append(errs, http.StatusConflict),
	}), policy.ScopeDeploymentsWrite), func(ctx context.Context, in *taskIDInput) (*runOutput, error) {
		v, err := svc.RunTaskNow(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &runOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "runServiceCommand", Method: http.MethodPost, Path: "/services/{serviceId}/run", Tags: tag,
		Summary: "Run a one-off command", DefaultStatus: http.StatusAccepted,
		Description: "Runs the command (argv, no shell) in a throwaway container of the service's current image with its environment, " +
			"on the service's server, with the service's resource limits and a timeout (default 30 minutes). Requires `deployments:write`; " +
			"audited. Returns 202 with the queued run; follow the output through `GET /task-runs/{runId}/logs?follow=true`.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}), policy.ScopeDeploymentsWrite), func(ctx context.Context, in *runCommandInput) (*runOutput, error) {
		v, err := svc.RunCommand(ctx, in.ID, in.Body.Command, in.Body.TimeoutSeconds)
		if err != nil {
			return nil, err
		}
		return &runOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listServiceTaskRuns", Method: http.MethodGet, Path: "/services/{serviceId}/task-runs", Tags: tag,
		Summary: "Run history of a service's tasks and one-off commands", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *listRunsInput) (*runListOutput, error) {
		var taskID *uuid.UUID
		if in.TaskID != "" {
			id, err := uuid.Parse(in.TaskID)
			if err != nil {
				return nil, apierr.Unprocessable("invalid_task_id", "taskId must be a UUID")
			}
			taskID = &id
		}
		items, err := svc.ListRuns(ctx, in.ID, taskID, in.Limit)
		if err != nil {
			return nil, err
		}
		out := &runListOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getTaskRun", Method: http.MethodGet, Path: "/task-runs/{runId}", Tags: tag,
		Summary: "Get a task run", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *runIDInput) (*runOutput, error) {
		v, err := svc.GetRun(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &runOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getTaskRunLogs", Method: http.MethodGet, Path: "/task-runs/{runId}/logs", Tags: []string{"logs", "tasks"},
		Summary: "Output of a task run",
		Description: "Without `follow` returns the stored lines as `{items:[{id,stream,line,createdAt}]}` (redacted, at most 10,000 lines). " +
			"With `follow=true` streams `data: {stream,line,ts}` until the run finishes, then `event: end` with `{status,exitCode}`.",
		Responses: services.StreamResponses(), Errors: errs,
	}, policy.ScopeLogsRead), func(ctx context.Context, in *runLogsInput) (*huma.StreamResponse, error) {
		if in.Follow {
			return svc.FollowLogs(ctx, in.ID)
		}
		lines, err := svc.RunLogs(ctx, in.ID, 0, MaxRunLogLines+1)
		if err != nil {
			return nil, err
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			services.WriteJSON(hctx, struct {
				Items []LogLineView `json:"items"`
			}{Items: lines})
		}}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "previewTaskSchedule", Method: http.MethodPost, Path: "/task-schedules/preview", Tags: tag,
		Summary: "Describe a cron expression",
		Description: "Validates the expression and returns a plain-English summary (naming the time zone) and the next runs as UTC instants, " +
			"computed in `timezone` (default UTC). An invalid expression or zone answers 200 with `valid: false` and the reason in `error`.",
		Errors: errs[:2],
	}), func(ctx context.Context, in *previewInput) (*previewOutput, error) {
		count := in.Body.Count
		if count <= 0 {
			count = 5
		}
		return &previewOutput{Body: PreviewSchedule(in.Body.Schedule, in.Body.Timezone, time.Now(), count)}, nil
	})
}
