package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

func newTasksCmd(c *cli) *cobra.Command {
	return group("tasks", "Cron tasks and their runs",
		newTasksLsCmd(c), newTasksCreateCmd(c), newTasksUpdateCmd(c), newTasksRunCmd(c), newTasksRmCmd(c),
		newTasksRunsCmd(c), newTasksLogsCmd(c))
}

// taskByRef finds a task of the service by id or (case-insensitive) name.
func (c *cli) taskByRef(ctx context.Context, serviceID, ref string) (apiclient.TaskView, error) {
	r, err := c.api.ListServiceTasksWithResponse(ctx, serviceID)
	if err := check(r, err); err != nil {
		return apiclient.TaskView{}, err
	}
	for _, t := range itemsOrEmpty(r.JSON200.Items) {
		if t.Id == ref || strings.EqualFold(t.Name, ref) {
			return t, nil
		}
	}
	return apiclient.TaskView{}, fmt.Errorf("task %q not found on this service (see `nebulactrl tasks ls`)", ref)
}

func runFinished(status string) bool {
	switch status {
	case "succeeded", "failed", "timed_out", "skipped":
		return true
	}
	return false
}

// commandArgs returns the words after `--`.
func commandArgs(cmd *cobra.Command, args []string) ([]string, error) {
	dash := cmd.ArgsLenAtDash()
	if dash < 0 || dash >= len(args) {
		return nil, usagef("give the command after `--`, e.g. %s -- rake db:migrate", cmd.CommandPath())
	}
	return args[dash:], nil
}

func newTasksLsCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "ls <service>", Short: "List a service's cron tasks", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				return err
			}
			r, err := c.api.ListServiceTasksWithResponse(ctx, id)
			if err := check(r, err); err != nil {
				return err
			}
			items := itemsOrEmpty(r.JSON200.Items)
			if c.asJSON {
				return c.printJSON(items)
			}
			rows := make([][]string, 0, len(items))
			for _, t := range items {
				last := "-"
				if t.LastRun != nil {
					last = string(t.LastRun.Status)
				}
				next := "-"
				if t.NextRunAt != nil && t.Enabled {
					next = inZone(*t.NextRunAt, t.Timezone).Format("2006-01-02 15:04 MST")
				}
				command := strings.Join(deref(t.Command), " ")
				if t.Action == apiclient.TaskViewActionRestart {
					command = "-"
				}
				rows = append(rows, []string{t.Name, string(t.Action), t.Schedule, t.Timezone, yesNo(t.Enabled), next, last, command, t.Id})
			}
			c.table([]string{"NAME", "ACTION", "SCHEDULE", "TIMEZONE", "ENABLED", "NEXT RUN", "LAST RUN", "COMMAND", "ID"}, rows)
			return nil
		},
	}
}

func newTasksCreateCmd(c *cli) *cobra.Command {
	var name, action, schedule, concurrency, timezone string
	var timeout time.Duration
	var disabled bool
	cmd := &cobra.Command{
		Use: "create <service> --name <name> --schedule <cron> [--action command|restart] [-- <command...>]", Short: "Schedule a recurring command or a restart", Args: cobra.ArbitraryArgs,
		Long: "With --action command (the default) runs the command (argv, no shell; use `-- sh -c '...'` for shell syntax) in a throwaway\n" +
			"container of the service's current image with its environment. With --action restart it restarts the service's running\n" +
			"container instead (brief outage with one replica; takes no command and not for one-shot services).\n" +
			"The schedule is a 5-field cron expression or @hourly/@daily/@weekly/@monthly,\n" +
			"evaluated in --timezone (an IANA name such as Europe/Berlin; default UTC). Daylight-saving changes keep the wall-clock\n" +
			"time: a run in the skipped hour is skipped for that day, a repeated hour runs once.",
		Example: "  nebulactrl tasks create acme/production/web --name nightly --schedule '0 3 * * *' -- rake cleanup\n" +
			"  nebulactrl tasks create acme/production/web --name weekly-restart --action restart --schedule '0 4 * * 0'",
		RunE: func(cmd *cobra.Command, args []string) error {
			if action != "command" && action != "restart" {
				return usagef("--action must be command or restart")
			}
			var argv []string
			if action == "restart" {
				if cmd.ArgsLenAtDash() >= 0 && cmd.ArgsLenAtDash() < len(args) {
					return usagef("a restart task takes no command; drop the arguments after `--`")
				}
				if len(args) != 1 {
					return usagef("expected exactly one <service>")
				}
				if timeout > 0 {
					return usagef("--timeout does not apply to a restart task")
				}
			} else {
				var err error
				if argv, err = commandArgs(cmd, args); err != nil {
					return err
				}
				if cmd.ArgsLenAtDash() != 1 {
					return usagef("expected exactly one <service> before `--`")
				}
			}
			if name == "" || schedule == "" {
				return usagef("--name and --schedule are required")
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				return err
			}
			act := apiclient.CreateTaskInputBodyAction(action)
			body := apiclient.CreateServiceTaskJSONRequestBody{Name: name, Action: &act, Schedule: schedule}
			if action == "command" {
				body.Command = &argv
			}
			if timezone != "" {
				body.Timezone = &timezone
			}
			if disabled {
				off := false
				body.Enabled = &off
			}
			if timeout > 0 {
				s := int64(timeout / time.Second)
				body.TimeoutSeconds = &s
			}
			if concurrency != "" {
				cc := apiclient.CreateTaskInputBodyConcurrency(concurrency)
				body.Concurrency = &cc
			}
			r, err := c.api.CreateServiceTaskWithResponse(ctx, id, &apiclient.CreateServiceTaskParams{IdempotencyKey: c.key()}, body)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON201)
			}
			c.info("Task %q created (%s)", r.JSON201.Name, r.JSON201.Id)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "task name, unique per service")
	cmd.Flags().StringVar(&action, "action", "command", "command (run the -- command in a new container) or restart (restart the service; no command)")
	cmd.Flags().StringVar(&schedule, "schedule", "", "cron expression, e.g. \"*/15 * * * *\" or @daily, evaluated in --timezone")
	cmd.Flags().StringVar(&timezone, "timezone", "", "IANA time zone of the schedule, e.g. Europe/Berlin (default UTC)")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "stop a run after this long (default 30m; not for restart)")
	cmd.Flags().StringVar(&concurrency, "concurrency", "", "forbid (default: skip a tick while the previous run is active) or allow")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "create the task disabled")
	return cmd
}

func newTasksRunCmd(c *cli) *cobra.Command {
	var detach bool
	return withDetach(&cobra.Command{
		Use: "run <service> [task]", Short: "Run a cron task (or a cron service) now and follow its output", Args: rangeArgs(1, 2),
		Long: "Starts a run immediately (whatever the schedule), prints its output and exits with the command's exit code. " +
			"A cron service (created with --cron) is run by naming only the service.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				cancel()
				return err
			}
			var taskID string
			if len(args) == 2 {
				t, err := c.taskByRef(ctx, id, args[1])
				if err != nil {
					cancel()
					return err
				}
				taskID = t.Id
			} else {
				svc, err := c.api.GetServiceWithResponse(ctx, id)
				if err := check(svc, err); err != nil {
					cancel()
					return err
				}
				if svc.JSON200.Cron == nil {
					cancel()
					return usagef("name the task to run: %s is not a cron service (see: nebulactrl tasks ls %s)", args[0], args[0])
				}
				taskID = svc.JSON200.Cron.TaskId
			}
			r, err := c.api.RunServiceTaskWithResponse(ctx, taskID, &apiclient.RunServiceTaskParams{IdempotencyKey: c.key()})
			cancel()
			if err := check(r, err); err != nil {
				return err
			}
			return c.followRun(cmd.Context(), r.JSON202.Id, detach)
		},
	}, &detach)
}

func withDetach(cmd *cobra.Command, v *bool) *cobra.Command {
	cmd.Flags().BoolVar(v, "detach", false, "start the run and return without waiting for it")
	return cmd
}

func newTasksRmCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "rm <service> <task>", Short: "Delete a cron task and its run history", Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				return err
			}
			t, err := c.taskByRef(ctx, id, args[1])
			if err != nil {
				return err
			}
			r, err := c.api.DeleteServiceTaskWithResponse(ctx, t.Id)
			if err := check(r, err); err != nil {
				return err
			}
			c.info("Task %q deleted", t.Name)
			return nil
		},
	}
}

func newTasksRunsCmd(c *cli) *cobra.Command {
	var task string
	var limit int
	cmd := &cobra.Command{
		Use: "runs <service>", Short: "Run history of a service's tasks and one-off commands", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				return err
			}
			params := &apiclient.ListServiceTaskRunsParams{}
			if task != "" {
				t, err := c.taskByRef(ctx, id, task)
				if err != nil {
					return err
				}
				tid, err := uuid.Parse(t.Id)
				if err != nil {
					return err
				}
				params.TaskId = &tid
			}
			if limit > 0 {
				l := int64(limit)
				params.Limit = &l
			}
			r, err := c.api.ListServiceTaskRunsWithResponse(ctx, id, params)
			if err := check(r, err); err != nil {
				return err
			}
			items := itemsOrEmpty(r.JSON200.Items)
			if c.asJSON {
				return c.printJSON(items)
			}
			rows := make([][]string, 0, len(items))
			for _, run := range items {
				exit := "-"
				if run.ExitCode != nil {
					exit = fmt.Sprint(*run.ExitCode)
				}
				rows = append(rows, []string{
					run.CreatedAt.UTC().Format(time.RFC3339), run.Name, string(run.Status), exit, run.Source, runDuration(run),
					run.Id, deref(run.Error),
				})
			}
			c.table([]string{"STARTED", "NAME", "STATUS", "EXIT", "SOURCE", "DURATION", "ID", "ERROR"}, rows)
			return nil
		},
	}
	cmd.Flags().StringVar(&task, "task", "", "only the runs of this task (name or id)")
	cmd.Flags().IntVar(&limit, "limit", 0, "how many runs (default 50)")
	return cmd
}

func runDuration(r apiclient.RunView) string {
	if r.StartedAt == nil || r.FinishedAt == nil {
		return "-"
	}
	return r.FinishedAt.Sub(*r.StartedAt).Round(time.Second).String()
}

func newTasksLogsCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "logs <run-id>", Short: "Print the output of a run (follows a run that is still active)", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			return c.followRun(cmd.Context(), args[0], false)
		},
	}
}

// newServicesRunCmd is `services run <service> -- <command...>`.
func newServicesRunCmd(c *cli) *cobra.Command {
	var timeout time.Duration
	var detach bool
	cmd := &cobra.Command{
		Use: "run <service> -- <command...>", Short: "Run a one-off command in a container of the service's image", Args: cobra.ArbitraryArgs,
		Long: "Runs the command (argv, no shell; use `-- sh -c '...'` for shell syntax) in a throwaway container of the service's\n" +
			"current image, with its environment, networks and resource limits, on its server. Prints the output and exits\n" +
			"with the command's exit code. Needs the deployments:write scope and is audited.",
		RunE: func(cmd *cobra.Command, args []string) error {
			argv, err := commandArgs(cmd, args)
			if err != nil {
				return err
			}
			if cmd.ArgsLenAtDash() != 1 {
				return usagef("expected exactly one <service> before `--`")
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				cancel()
				return err
			}
			body := apiclient.RunServiceCommandJSONRequestBody{Command: &argv}
			if timeout > 0 {
				s := int64(timeout / time.Second)
				body.TimeoutSeconds = &s
			}
			r, err := c.api.RunServiceCommandWithResponse(ctx, id, &apiclient.RunServiceCommandParams{IdempotencyKey: c.key()}, body)
			cancel()
			if err := check(r, err); err != nil {
				return err
			}
			return c.followRun(cmd.Context(), r.JSON202.Id, detach)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "stop the command after this long (default 30m)")
	return withDetach(cmd, &detach)
}

// followRun prints a run's output as it arrives and returns the command's
// outcome as the process exit code (0 only for a run that succeeded).
func (c *cli) followRun(parent context.Context, runID string, detach bool) error {
	if detach {
		if c.asJSON {
			return c.printJSON(map[string]string{"id": runID})
		}
		c.info("Run %s started", runID)
		return nil
	}
	f := true
	res, err := c.stream.GetTaskRunLogs(parent, runID, &apiclient.GetTaskRunLogsParams{Follow: &f})
	if err := checkRaw(res, err); err != nil {
		return err
	}
	defer res.Body.Close()
	var decodeErr error
	err = readSSE(res.Body, func(ev sseEvent) bool {
		switch ev.Name {
		case "end":
			return false
		case "message":
			if c.asJSON {
				fmt.Fprintln(c.out, ev.Data)
				return true
			}
			var l struct {
				Line string `json:"line"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &l); err != nil {
				decodeErr = fmt.Errorf("decode output event: %w", err)
				return false
			}
			fmt.Fprintln(c.out, l.Line)
		}
		return true
	})
	if decodeErr != nil {
		return decodeErr
	}
	if err != nil && parent.Err() == nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("output stream: %w", err)
	}
	if parent.Err() != nil {
		c.info("Interrupted; run %s keeps going on the server", runID)
		return nil
	}

	ctx, cancel := c.ctx(parent)
	defer cancel()
	r, err := c.api.GetTaskRunWithResponse(ctx, runID)
	if err := check(r, err); err != nil {
		return err
	}
	run := r.JSON200
	status := string(run.Status)
	if !runFinished(status) {
		return &exitError{1, fmt.Errorf("the output stream ended but run %s is still %s", runID, status)}
	}
	if status == "succeeded" {
		return nil
	}
	code := 1
	if run.ExitCode != nil && *run.ExitCode > 0 && *run.ExitCode < 256 {
		code = int(*run.ExitCode)
	}
	msg := fmt.Sprintf("run %s %s", runID, status)
	if e := deref(run.Error); e != "" {
		msg += ": " + e
	}
	return &exitError{code, errors.New(msg)}
}
