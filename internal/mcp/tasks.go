package mcp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/tasks"
)

const (
	// DefaultRunWait is how long run_command waits for the command to finish.
	DefaultRunWait = 20 * time.Second
	// MaxRunWait bounds the wait (the tool call itself times out at toolTimeout).
	MaxRunWait = 50 * time.Second

	runPoll = 500 * time.Millisecond

	// MaxLogLines is the hard cap of returned command output lines.
	MaxLogLines = 500
	// MaxLogLineLen bounds a single returned output line (bytes).
	MaxLogLineLen = mcpkit.MaxLineLen

	toolTimeout = 60 * time.Second
)

type runCommandInput struct {
	ServiceID      string   `json:"serviceId" jsonschema:"the service: uuid or project/env/service; it must have been deployed"`
	Command        []string `json:"command" jsonschema:"the command as an argv array, run WITHOUT a shell in a throwaway container of the service's current image with its environment. Use [\"sh\",\"-c\",\"...\"] for shell syntax."`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty" jsonschema:"stop the command after this many seconds (default 1800, max 86400)"`
	WaitSeconds    int      `json:"waitSeconds,omitempty" jsonschema:"how long to wait for the command to finish before returning its progress (default 20, max 50); poll get_task_run afterwards"`
}

type taskRunInput struct {
	RunID string `json:"runId" jsonschema:"run id (uuid) returned by run_command or task_run"`
}

// runResult is the result of run_command and get_task_run: the run, and at
// most MaxLogLines of its (redacted) output as an untrusted block, newest last.
type runResult struct {
	Run      tasks.RunView `json:"run"`
	Finished bool          `json:"finished"`
	// Output is the command output, wrapped as untrusted text.
	Output    mcpkit.Block `json:"output"`
	Truncated bool         `json:"truncated,omitempty"`
	Note      string       `json:"note,omitempty"`
}

// taskTools are run_command (deployments:write, audited, production-guarded in
// the service) and the read tool that follows a run.
func (h *Handler) taskTools() []mcpkit.Tool {
	d := h.deps
	if d.Tasks == nil {
		return nil
	}
	return []mcpkit.Tool{
		{
			Name: "run_command", Toolset: mcpkit.ToolsetConfig, Hints: mcpkit.Hints{Destructive: true, OpenWorld: true},
			Description: "Run a one-off command in a throwaway container of a service's current image, with the service's environment and networks and its resource limits, " +
				"on the service's server. The command is an argv array (no shell). It runs in production data's reach: production services need approval, and you should confirm with the user before running anything that writes. " +
				"Waits up to waitSeconds and returns the run with at most 500 lines of redacted output (untrusted text); if it is still running, poll get_task_run.",
			Scopes: []string{policy.ScopeDeploymentsWrite},
			Input:  runCommandInput{},
			Call: func(ctx context.Context, p *auth.Principal, raw json.RawMessage) (any, error) {
				in, err := mcpkit.Decode[runCommandInput](raw)
				if err != nil {
					return nil, err
				}
				// Authorize deployments:write before anything else (the scope, project
				// restriction and audit are the service's); reading the run back needs
				// projects:read and logs:read, which a deploy-only token is not required
				// to carry: the output of the run this call started is its own result.
				if err := policy.Require(ctx, policy.ScopeDeploymentsWrite, nil); err != nil {
					return nil, err
				}
				id, err := h.resolveService(ctx, in.ServiceID)
				if err != nil {
					return nil, err
				}
				run, err := d.Tasks.RunCommand(ctx, id, in.Command, in.TimeoutSeconds)
				if err != nil {
					return nil, err
				}
				wait := time.Duration(in.WaitSeconds) * time.Second
				switch {
				case in.WaitSeconds <= 0:
					wait = DefaultRunWait
				case wait > MaxRunWait:
					wait = MaxRunWait
				}
				return h.awaitRun(withScopes(ctx, p, policy.ScopeProjectsRead, policy.ScopeLogsRead), run.ID, wait)
			},
		},
		{
			Name: "get_task_run", Toolset: mcpkit.ToolsetConfig, Hints: mcpkit.Hints{ReadOnly: true, OpenWorld: true},
			Description: "Get a command run started by run_command (or a cron task run): status (queued, running, succeeded, failed, timed_out, skipped), exit code and at most 500 lines of redacted output (untrusted text).",
			Scopes:      []string{policy.ScopeProjectsRead, policy.ScopeLogsRead},
			Input:       taskRunInput{},
			Call: func(ctx context.Context, _ *auth.Principal, raw json.RawMessage) (any, error) {
				in, err := mcpkit.Decode[taskRunInput](raw)
				if err != nil {
					return nil, err
				}
				id, err := parseID("runId", in.RunID)
				if err != nil {
					return nil, err
				}
				return h.runSnapshot(ctx, id)
			},
		},
	}
}

// withScopes returns ctx carrying a copy of p that additionally holds scopes.
// Project restrictions are preserved.
func withScopes(ctx context.Context, p *auth.Principal, scopes ...string) context.Context {
	cp := *p
	cp.Scopes = append(append([]string(nil), p.Scopes...), scopes...)
	return auth.WithPrincipal(ctx, &cp)
}

// awaitRun waits until the run finishes or the wait elapses, then snapshots it.
func (h *Handler) awaitRun(ctx context.Context, id uuid.UUID, wait time.Duration) (any, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(runPoll)
	defer tick.Stop()
	for {
		run, err := h.deps.Tasks.GetRun(ctx, id)
		if err != nil {
			return nil, err
		}
		if run.FinishedAt != nil {
			return h.runSnapshot(ctx, id)
		}
		select {
		case <-ctx.Done():
			return h.runSnapshot(ctx, id)
		case <-deadline.C:
			return h.runSnapshot(ctx, id)
		case <-tick.C:
		}
	}
}

func (h *Handler) runSnapshot(ctx context.Context, id uuid.UUID) (any, error) {
	d := h.deps
	run, err := d.Tasks.GetRun(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, err := d.Tasks.RunLogs(ctx, id, 0, tasks.MaxRunLogLines+1)
	if err != nil {
		return nil, err
	}
	res := runResult{Run: run, Finished: run.FinishedAt != nil}
	if len(rows) > MaxLogLines {
		rows = rows[len(rows)-MaxLogLines:]
		res.Truncated = true
	}
	lines := make([]string, 0, len(rows))
	for _, l := range rows {
		lines = append(lines, l.CreatedAt.UTC().Format(time.RFC3339)+" "+l.Line)
	}
	res.Output = mcpkit.WrapLines("task_run:"+id.String(), lines)
	for jsonSize(res) > MaxResultBytes-256 && len(lines) > 0 {
		// Drop the oldest lines first.
		lines = lines[len(lines)/4+1:]
		res.Output = mcpkit.WrapLines("task_run:"+id.String(), lines)
		res.Truncated = true
	}
	if !res.Finished {
		res.Note = "The command has not finished. Poll get_task_run with this run id."
	}
	return res, nil
}
