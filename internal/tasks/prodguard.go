package tasks

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Guard operation names (prodguard approvals are bound to them).
const (
	guardRunCommand = "run_command"
	guardRunTask    = "run_task"

	guardCreateTask = "createServiceTask"
	guardUpdateTask = "updateServiceTask"
	guardDeleteTask = "deleteServiceTask"
)

// guardRun asks the production guard before a command runs in a production
// service's container. The approver sees the full argv (clipped), which is also
// what the audit row records.
func guardRun(ctx context.Context, sc services.Scope, op string, taskID *uuid.UUID, name string, command []string, timeoutSeconds int) error {
	if !prodguard.Applies(ctx, sc.IsProduction) {
		return nil
	}
	shown := clip(strings.Join(command, " "))
	args := []any{sc.Service.ID, timeoutSeconds}
	for _, c := range command {
		args = append(args, c)
	}
	what := "Runs the command `" + shown + "` in a new container of " + sc.Service.Name + "'s current image, with its variables and networks."
	summary := fmt.Sprintf("Run a one-off command in %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName)
	if taskID != nil {
		args = []any{*taskID}
		what = "Runs the saved task " + name + " (`" + shown + "`) in a new container of " + sc.Service.Name + "'s current image."
		summary = fmt.Sprintf("Run the task %s of %s in production (%s / %s)", name, sc.Service.Name, sc.ProjectName, sc.EnvName)
	}
	return prodguard.Check(ctx, sc.ProdScope(summary, false, what,
		"The command can read and change anything the service can reach, including its data."), op, prodguard.Digest(op, args...))
}

// requireRunScope demands deployments:write for a task change that sets or
// changes the command of a task in production: the scheduler runs that command
// in the service's container with its secrets, the same power as run_command.
func requireRunScope(ctx context.Context, sc services.Scope) error {
	if !sc.IsProduction {
		return nil
	}
	return policy.Require(ctx, policy.ScopeDeploymentsWrite, &sc.ProjectID)
}

// guardTaskChange asks the production guard before a task of a production
// service is created, changed or deleted. The approver sees the command and
// schedule; the digest binds the approval to every field.
func guardTaskChange(ctx context.Context, sc services.Scope, op string, taskID *uuid.UUID, name, schedule, timezone string, command []string, enabled bool, timeoutSeconds int, concurrency string) error {
	if !prodguard.Applies(ctx, sc.IsProduction) {
		return nil
	}
	var summary string
	var effects []string
	irreversible := false
	switch op {
	case guardDeleteTask:
		summary = fmt.Sprintf("Delete the task %s of %s in production (%s / %s)", name, sc.Service.Name, sc.ProjectName, sc.EnvName)
		effects = []string{"The task and its run history are removed."}
		irreversible = true
	case guardUpdateTask:
		summary = fmt.Sprintf("Change the task %s of %s in production (%s / %s)", name, sc.Service.Name, sc.ProjectName, sc.EnvName)
		effects = []string{"The new command, schedule or enabled flag applies from the next tick."}
	default:
		summary = fmt.Sprintf("Schedule the task %s in %s in production (%s / %s)", name, sc.Service.Name, sc.ProjectName, sc.EnvName)
		effects = []string{"The command runs in the running container on every tick, with the service's secrets in its environment."}
	}
	if op != guardDeleteTask {
		effects = append(effects,
			"Command: `"+clip(strings.Join(command, " "))+"`.",
			fmt.Sprintf("Schedule: %s (%s), enabled: %t.", schedule, timezone, enabled))
	}
	args := []any{sc.Service.ID}
	if taskID != nil {
		args = []any{*taskID}
	}
	if op != guardDeleteTask {
		args = append(args, name, schedule, timezone, enabled, timeoutSeconds, concurrency)
		for _, c := range command {
			args = append(args, c)
		}
	}
	return prodguard.Check(ctx, sc.ProdScope(summary, irreversible, effects...), op, prodguard.Digest(op, args...))
}
