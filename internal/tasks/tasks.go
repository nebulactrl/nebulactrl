package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var taskNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.:/-]{0,63}$`)

func validName(name string) error {
	if !taskNameRe.MatchString(name) {
		return apierr.Unprocessable("invalid_name", "the task name must be 1-64 characters: letters, digits, spaces and _ . : / -")
	}
	return nil
}

func validTimeout(seconds int) error {
	if seconds < 1 || seconds > agentprotocol.MaxTaskTimeoutSeconds {
		return apierr.Unprocessable("invalid_timeout", fmt.Sprintf("timeoutSeconds must be between 1 and %d", agentprotocol.MaxTaskTimeoutSeconds))
	}
	return nil
}

func validConcurrency(c string) error {
	if c != ConcurrencyForbid && c != ConcurrencyAllow {
		return apierr.Unprocessable("invalid_concurrency", "concurrency must be forbid or allow")
	}
	return nil
}

func validCommand(argv []string) error {
	if err := agentprotocol.ValidTaskCommand(argv); err != nil {
		return apierr.Unprocessable("invalid_command", err.Error())
	}
	return nil
}

// errTaskHidden is the answer for the hidden schedule row of a cron service:
// it is managed through the service's config, never as a task.
func errTaskHidden() error {
	return apierr.New(http.StatusNotFound, "task_not_found", "task not found")
}

// validAction checks a task action against the service: restart does not apply
// to a one-shot, which has no running container to restart.
func validAction(sc services.Scope, action string) error {
	switch action {
	case ActionCommand:
		return nil
	case ActionRestart:
		if sc.Config().OneShot {
			return apierr.Unprocessable("restart_not_applicable", "a one-shot service has no running container to restart; schedule a command instead")
		}
		return nil
	}
	return apierr.Unprocessable("invalid_action", "action must be command or restart")
}

// validCommandFor validates the argv of a command task; a restart task runs none.
func validCommandFor(action string, argv []string) error {
	if action == ActionRestart {
		return nil
	}
	return validCommand(argv)
}

// taskScope loads a task and authorizes scope on its service.
func (s *Service) taskScope(ctx context.Context, scope string, id uuid.UUID) (db.ServiceTask, services.Scope, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.ServiceTask{}, services.Scope{}, err
	}
	t, err := s.q.GetServiceTask(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, services.Scope{}, apierr.NotFound("task")
	}
	if err != nil {
		return t, services.Scope{}, fmt.Errorf("get task: %w", err)
	}
	sc, err := services.LoadScope(ctx, s.q, scope, t.ServiceID)
	if err != nil {
		return t, sc, err
	}
	return t, sc, nil
}

// ListTasks returns a service's cron tasks with their latest run.
func (s *Service) ListTasks(ctx context.Context, serviceID uuid.UUID) ([]TaskView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListServiceTasks(ctx, sc.Service.ID)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	latest, err := s.q.ListLatestTaskRuns(ctx, sc.Service.ID)
	if err != nil {
		return nil, fmt.Errorf("list latest runs: %w", err)
	}
	byTask := make(map[uuid.UUID]RunView, len(latest))
	for _, l := range latest {
		if l.TaskRun.TaskID != nil {
			byTask[*l.TaskRun.TaskID] = runView(l.TaskRun, l.EffectiveStatus)
		}
	}
	out := make([]TaskView, len(rows))
	for i, t := range rows {
		out[i] = taskView(t)
		if r, ok := byTask[t.ID]; ok {
			out[i].LastRun = &r
		}
	}
	return out, nil
}

// GetTask returns one task.
func (s *Service) GetTask(ctx context.Context, id uuid.UUID) (TaskView, error) {
	t, _, err := s.taskScope(ctx, policy.ScopeProjectsRead, id)
	if err != nil {
		return TaskView{}, err
	}
	return taskView(t), nil
}

func argvJSON(argv []string) json.RawMessage {
	b, _ := json.Marshal(argv)
	return b
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// CreateTask adds a cron task to a service.
func (s *Service) CreateTask(ctx context.Context, serviceID uuid.UUID, in TaskInput) (TaskView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return TaskView{}, err
	}
	if sc.Service.Kind == services.KindRoute {
		return TaskView{}, apierr.Unprocessable("not_applicable", "a route service has no container to run tasks in")
	}
	if err := services.RequireNotStack(sc); err != nil {
		return TaskView{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Action == "" {
		in.Action = ActionCommand
	}
	if err := validAction(sc, in.Action); err != nil {
		return TaskView{}, err
	}
	if in.Action == ActionRestart {
		in.Command, in.TimeoutSeconds = []string{}, RestartTimeoutSeconds
	}
	if in.TimeoutSeconds == 0 {
		in.TimeoutSeconds = agentprotocol.DefaultTaskTimeoutSeconds
	}
	if in.Concurrency == "" {
		in.Concurrency = ConcurrencyForbid
	}
	enabled := in.Enabled == nil || *in.Enabled
	for _, err := range []error{validName(in.Name), validCommandFor(in.Action, in.Command), validTimeout(in.TimeoutSeconds), validConcurrency(in.Concurrency)} {
		if err != nil {
			return TaskView{}, err
		}
	}
	if in.Timezone, err = NormalizeTimezone(in.Timezone); err != nil {
		return TaskView{}, err
	}
	nextAt, err := nextRun(in.Schedule, in.Timezone, s.now())
	if err != nil {
		return TaskView{}, err
	}
	if err := requireRunScope(ctx, sc); err != nil {
		return TaskView{}, err
	}
	if err := guardTaskChange(ctx, sc, guardCreateTask, nil, in.Name, in.Action, strings.TrimSpace(in.Schedule), in.Timezone, in.Command, enabled, in.TimeoutSeconds, in.Concurrency); err != nil {
		return TaskView{}, err
	}
	var next *time.Time
	if enabled {
		next = &nextAt
	}
	var t db.ServiceTask
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		n, err := q.ListServiceTasks(ctx, sc.Service.ID)
		if err != nil {
			return err
		}
		if len(n) >= MaxTasksPerService {
			return apierr.Unprocessable("too_many_tasks", fmt.Sprintf("a service can have at most %d tasks", MaxTasksPerService))
		}
		t, err = q.InsertServiceTask(ctx, db.InsertServiceTaskParams{
			ServiceID: sc.Service.ID, Name: in.Name, Action: in.Action, Schedule: strings.TrimSpace(in.Schedule), Timezone: in.Timezone, Command: argvJSON(in.Command),
			Enabled: enabled, TimeoutSeconds: int32(in.TimeoutSeconds), Concurrency: in.Concurrency, NextRunAt: next,
		})
		if err != nil {
			if isUniqueViolation(err) {
				return apierr.Conflict("task_exists", "this service already has a task with that name")
			}
			return fmt.Errorf("insert task: %w", err)
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionTaskCreate, ResourceType: "task", ResourceID: t.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "name": t.Name, "schedule": t.Schedule, "timezone": t.Timezone},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return TaskView{}, err
		}
		return TaskView{}, fmt.Errorf("create task: %w", err)
	}
	s.publishTasks(sc)
	return taskView(t), nil
}

// UpdateTask changes a task. Editing the schedule (or enabling a disabled
// task) recomputes its next run from now.
func (s *Service) UpdateTask(ctx context.Context, id uuid.UUID, in TaskPatch) (TaskView, error) {
	cur, sc, err := s.taskScope(ctx, policy.ScopeProjectsWrite, id)
	if err != nil {
		return TaskView{}, err
	}
	if cur.Action == ActionService {
		return TaskView{}, errTaskHidden()
	}
	name, action, schedule, tz, command := cur.Name, cur.Action, cur.Schedule, cur.Timezone, decodeArgv(cur.Command)
	enabled, timeout, conc := cur.Enabled, int(cur.TimeoutSeconds), cur.Concurrency
	if in.Action != nil && *in.Action != cur.Action {
		action = *in.Action
		if err := validAction(sc, action); err != nil {
			return TaskView{}, err
		}
		if action == ActionCommand && in.TimeoutSeconds == nil {
			timeout = agentprotocol.DefaultTaskTimeoutSeconds
		}
	}
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if in.Schedule != nil {
		schedule = strings.TrimSpace(*in.Schedule)
	}
	if in.Command != nil {
		command = *in.Command
	}
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	if in.TimeoutSeconds != nil {
		timeout = *in.TimeoutSeconds
	}
	if in.Concurrency != nil {
		conc = *in.Concurrency
	}
	if in.Timezone != nil {
		if tz, err = NormalizeTimezone(*in.Timezone); err != nil {
			return TaskView{}, err
		}
	}
	if action == ActionRestart {
		command, timeout = []string{}, RestartTimeoutSeconds
	}
	for _, err := range []error{validName(name), validCommandFor(action, command), validTimeout(timeout), validConcurrency(conc)} {
		if err != nil {
			return TaskView{}, err
		}
	}
	if action != cur.Action || (in.Command != nil && !slices.Equal(*in.Command, decodeArgv(cur.Command))) {
		if err := requireRunScope(ctx, sc); err != nil {
			return TaskView{}, err
		}
	}
	if err := guardTaskChange(ctx, sc, guardUpdateTask, &id, name, action, schedule, tz, command, enabled, timeout, conc); err != nil {
		return TaskView{}, err
	}
	var next *time.Time
	if enabled {
		n, err := nextRun(schedule, tz, s.now())
		if err != nil {
			return TaskView{}, err
		}
		// Keep the pending tick of an unchanged schedule and zone.
		if schedule == cur.Schedule && tz == cur.Timezone && cur.Enabled && cur.NextRunAt != nil {
			n = *cur.NextRunAt
		}
		next = &n
	}
	var t db.ServiceTask
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		t, err = db.New(tx).UpdateServiceTask(ctx, db.UpdateServiceTaskParams{
			ID: id, Name: name, Action: action, Schedule: schedule, Timezone: tz, Command: argvJSON(command), Enabled: enabled,
			TimeoutSeconds: int32(timeout), Concurrency: conc, NextRunAt: next,
		})
		if err != nil {
			if isUniqueViolation(err) {
				return apierr.Conflict("task_exists", "this service already has a task with that name")
			}
			return fmt.Errorf("update task: %w", err)
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionTaskUpdate, ResourceType: "task", ResourceID: t.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "name": t.Name, "schedule": t.Schedule, "timezone": t.Timezone, "enabled": t.Enabled},
		})
	})
	if err != nil {
		return TaskView{}, err
	}
	s.publishTasks(sc)
	return taskView(t), nil
}

// DeleteTask removes a task and its run history.
func (s *Service) DeleteTask(ctx context.Context, id uuid.UUID) error {
	t, sc, err := s.taskScope(ctx, policy.ScopeProjectsWrite, id)
	if err != nil {
		return err
	}
	if t.Action == ActionService {
		return errTaskHidden()
	}
	if err := guardTaskChange(ctx, sc, guardDeleteTask, &id, t.Name, t.Action, "", "", nil, false, 0, ""); err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.New(tx).DeleteServiceTask(ctx, id); err != nil {
			return fmt.Errorf("delete task: %w", err)
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionTaskDelete, ResourceType: "task", ResourceID: id.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "name": t.Name},
		})
	})
	if err != nil {
		return err
	}
	s.publishTasks(sc)
	return nil
}
