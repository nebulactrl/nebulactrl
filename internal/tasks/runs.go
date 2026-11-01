package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/networks"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// startInput describes a run to start inside a transaction.
type startInput struct {
	sc           services.Scope
	task         *db.ServiceTask
	name, source string
	command      []string
	timeout      int
	actor        string
	scheduledFor *time.Time
}

// blocker is why a run can not start now. Interactive callers get it as a 409,
// the cron scheduler records a skipped run with it.
type blocker struct {
	code, msg string
}

// runnable checks that a run of the service can be dispatched now: it has been
// deployed, sits on a server and that server is online. It returns the server
// the service's current release runs on.
func (s *Service) runnable(ctx context.Context, q *db.Queries, sc services.Scope) (db.GetServiceCurrentReleaseRow, *blocker, error) {
	var zero db.GetServiceCurrentReleaseRow
	if sc.Service.Kind == services.KindRoute {
		return zero, &blocker{"not_applicable", "a route service has no container to run commands in"}, nil
	}
	if sc.Service.Kind == services.KindStack {
		return zero, &blocker{"not_applicable", "Not available for Compose stacks"}, nil
	}
	cur, err := q.GetServiceCurrentRelease(ctx, sc.Service.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, &blocker{"not_deployed", "the service has not been deployed yet; deploy it first"}, nil
	}
	if err != nil {
		return zero, nil, fmt.Errorf("load current release: %w", err)
	}
	serverID := cur.DeploymentServerID
	if serverID == nil {
		serverID = sc.Service.ServerID
	}
	if serverID == nil {
		return zero, &blocker{"no_server", "the service is not placed on a server"}, nil
	}
	name, online, err := serverState(ctx, q, *serverID)
	if err != nil {
		return zero, nil, err
	}
	if !online {
		return zero, &blocker{"server_offline", "the server " + name + " is offline"}, nil
	}
	cur.DeploymentServerID = serverID
	return cur, nil, nil
}

func (b *blocker) err() error {
	switch b.code {
	case "not_applicable":
		return apierr.Unprocessable(b.code, b.msg)
	}
	return apierr.Conflict(b.code, b.msg)
}

// insertSkipped records a run without dispatching it (skipped runs).
func (s *Service) insertSkipped(ctx context.Context, q *db.Queries, in startInput, reason string) (db.TaskRun, error) {
	now := s.now()
	var taskID *uuid.UUID
	if in.task != nil {
		taskID = &in.task.ID
	}
	return q.InsertTaskRun(ctx, db.InsertTaskRunParams{
		WorkspaceID: in.sc.WorkspaceID, ServiceID: in.sc.Service.ID, TaskID: taskID, Name: in.name, Source: in.source,
		Command: argvJSON(in.command), Status: StatusSkipped, Error: reason, TimeoutSeconds: int32(in.timeout),
		ServerID: in.sc.Service.ServerID, ActorLabel: in.actor, ScheduledFor: in.scheduledFor, FinishedAt: &now,
	})
}

// startRun records a run and enqueues its run_task command inside tx. The
// payload names the release and the variable keys; their values are attached
// at send time (SecretsForCommand).
func (s *Service) startRun(ctx context.Context, tx pgx.Tx, in startInput) (db.TaskRun, error) {
	q := db.New(tx)
	cur, b, err := s.runnable(ctx, q, in.sc)
	if err != nil {
		return db.TaskRun{}, err
	}
	if b != nil {
		return db.TaskRun{}, b.err()
	}
	payload, err := s.payloadFor(in, cur)
	if err != nil {
		return db.TaskRun{}, err
	}
	snap := services.DecodeSnapshot(cur.Config)
	if cur.DeploymentServerID != nil {
		mem, err := networks.Resolve(ctx, q, in.sc.EnvironmentID, *cur.DeploymentServerID, in.sc.Service.ID, snap.Config.BlockEgress, snap.Networks)
		if err != nil {
			return db.TaskRun{}, err
		}
		payload.BlockEgress, payload.SharedNetworks, payload.EnvHasEgressBlocked = mem.BlockEgress, mem.SharedNetworks, mem.EnvHasEgressBlocked
	}
	var taskID *uuid.UUID
	if in.task != nil {
		taskID = &in.task.ID
	}
	run, err := q.InsertTaskRun(ctx, db.InsertTaskRunParams{
		WorkspaceID: in.sc.WorkspaceID, ServiceID: in.sc.Service.ID, TaskID: taskID, Name: in.name, Source: in.source,
		Command: argvJSON(in.command), Status: StatusQueued, TimeoutSeconds: int32(in.timeout),
		ServerID: cur.DeploymentServerID, ActorLabel: in.actor, ScheduledFor: in.scheduledFor,
	})
	if err != nil {
		return run, fmt.Errorf("insert task run: %w", err)
	}
	payload.RunID = run.ID.String()
	raw, err := json.Marshal(payload)
	if err != nil {
		return run, err
	}
	cmdID, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{
		ServerID: *cur.DeploymentServerID, Kind: agentprotocol.KindRunTask, Payload: raw, IdempotencyKey: "run_task:" + run.ID.String(),
	})
	if err != nil {
		return run, fmt.Errorf("enqueue run_task: %w", err)
	}
	if err := q.SetTaskRunCommand(ctx, db.SetTaskRunCommandParams{ID: run.ID, CommandID: &cmdID}); err != nil {
		return run, err
	}
	run.CommandID = &cmdID
	return run, nil
}

// payloadFor builds the run_task payload from the service's current release:
// its variable keys (never values), image and resource limits.
func (s *Service) payloadFor(in startInput, cur db.GetServiceCurrentReleaseRow) (agentprotocol.RunTaskPayload, error) {
	snap := services.DecodeSnapshot(cur.Config)
	src := services.DecodeSource(cur.Source)
	p := agentprotocol.RunTaskPayload{
		ServiceID: in.sc.Service.ID.String(), ServiceSlug: in.sc.Service.Slug, EnvironmentID: in.sc.EnvironmentID.String(),
		DeploymentID: cur.DeploymentID.String(), ReleaseID: cur.ID.String(), ImageRef: cur.ImageRef,
		HasRegistryAuth: len(in.sc.Service.RegistryAuthEnc) > 0 && src.Type == services.SourceImage,
		Command:         in.command, TimeoutSeconds: in.timeout, MemoryMB: snap.Config.MemoryMB, CPUs: snap.Config.CPUs,
	}
	if src.Type == services.SourceImage && src.Digest != "" {
		p.ImageRef = src.Digest
	}
	if len(cur.EnvEnc) > 0 {
		plain, err := s.box.Open(secretbox.ReleasesEnv, cur.ID.String(), cur.EnvEnc)
		if err != nil {
			return p, fmt.Errorf("decrypt release env: %w", err)
		}
		var env services.EnvSnapshot
		if err := json.Unmarshal(plain, &env); err != nil {
			return p, fmt.Errorf("decode release env: %w", err)
		}
		secret := make(map[string]bool, len(env.Secret))
		for _, k := range env.Secret {
			secret[k] = true
		}
		for k := range env.Vars {
			if env.Scope[k] == services.ScopeBuild {
				continue // build-time only: not part of the container environment
			}
			p.EnvKeys = append(p.EnvKeys, k)
			if secret[k] {
				p.SecretEnvKeys = append(p.SecretEnvKeys, k)
			}
		}
	}
	p.EnvKeys = append(p.EnvKeys, services.VarDeploymentID, services.VarReleaseID)
	sort.Strings(p.EnvKeys)
	sort.Strings(p.SecretEnvKeys)
	return p, nil
}

func normalizeTimeout(seconds int) int {
	switch {
	case seconds <= 0:
		return agentprotocol.DefaultTaskTimeoutSeconds
	case seconds > agentprotocol.MaxTaskTimeoutSeconds:
		return agentprotocol.MaxTaskTimeoutSeconds
	}
	return seconds
}

// RunTaskNow starts a task immediately ("Run now"), whatever its schedule and
// concurrency policy.
func (s *Service) RunTaskNow(ctx context.Context, id uuid.UUID) (RunView, error) {
	t, sc, err := s.taskScope(ctx, policy.ScopeDeploymentsWrite, id)
	if err != nil {
		return RunView{}, err
	}
	if err := guardRun(ctx, sc, guardRunTask, &t.ID, t.Name, decodeArgv(t.Command), int(t.TimeoutSeconds)); err != nil {
		return RunView{}, err
	}
	var run db.TaskRun
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		run, err = s.startRun(ctx, tx, startInput{
			sc: sc, task: &t, name: t.Name, source: SourceManual, command: decodeArgv(t.Command),
			timeout: int(t.TimeoutSeconds), actor: actorLabel(ctx),
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionTaskRun, ResourceType: "task_run", ResourceID: run.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "task": t.Name},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return RunView{}, err
		}
		return RunView{}, fmt.Errorf("run task: %w", err)
	}
	s.publishRun(sc, run.ID)
	return runView(run, StatusQueued), nil
}

// RunCommand runs a one-off command in a container of the service's current
// image (deployments:write). The command is argv without a shell; wrap it in
// ["sh","-c",…] for shell syntax. The audit entry records the command.
func (s *Service) RunCommand(ctx context.Context, serviceID uuid.UUID, command []string, timeoutSeconds int) (RunView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, serviceID)
	if err != nil {
		return RunView{}, err
	}
	if err := validCommand(command); err != nil {
		return RunView{}, err
	}
	timeout := normalizeTimeout(timeoutSeconds)
	if timeoutSeconds != 0 {
		if err := validTimeout(timeoutSeconds); err != nil {
			return RunView{}, err
		}
	}
	if err := guardRun(ctx, sc, guardRunCommand, nil, "", command, timeoutSeconds); err != nil {
		return RunView{}, err
	}
	var run db.TaskRun
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		run, err = s.startRun(ctx, tx, startInput{
			sc: sc, name: "one-off", source: SourceOneOff, command: command, timeout: timeout, actor: actorLabel(ctx),
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionRunCommand, ResourceType: "task_run", ResourceID: run.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "command": clip(strings.Join(command, " "))},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return RunView{}, err
		}
		return RunView{}, fmt.Errorf("run command: %w", err)
	}
	s.publishRun(sc, run.ID)
	return runView(run, StatusQueued), nil
}

// RunOneOff starts a one-off command inside the caller's transaction (preview
// seed commands, other internal features). It performs no authorization: the
// caller has already authorized the action. The service must have a deployed
// release on an online server, else a 409 error; the command is enqueued with
// tx and delivered once it commits.
func (s *Service) RunOneOff(ctx context.Context, tx pgx.Tx, serviceID uuid.UUID, command []string, opts RunOpts) (uuid.UUID, error) {
	if err := validCommand(command); err != nil {
		return uuid.Nil, err
	}
	sc, err := services.LoadScopeInternal(ctx, db.New(tx), serviceID)
	if err != nil {
		return uuid.Nil, err
	}
	source := opts.Source
	if source == "" {
		source = SourceOneOff
	}
	actor := opts.ActorLabel
	if actor == "" {
		actor = actorLabel(ctx)
	}
	timeout := agentprotocol.DefaultTaskTimeoutSeconds
	if opts.Timeout > 0 {
		timeout = normalizeTimeout(int(opts.Timeout / time.Second))
	}
	run, err := s.startRun(ctx, tx, startInput{
		sc: sc, name: "one-off", source: source, command: command, timeout: timeout, actor: actor,
	})
	if err != nil {
		return uuid.Nil, err
	}
	return run.ID, nil
}

// RunStatus reports a run's effective status and exit code without
// authorization (internal callers such as previews). errMsg explains a failed,
// timed-out or skipped run.
func (s *Service) RunStatus(ctx context.Context, runID uuid.UUID) (status string, exitCode *int, errMsg string, err error) {
	row, err := s.q.GetTaskRunViewInternal(ctx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, "", apierr.NotFound("task run")
	}
	if err != nil {
		return "", nil, "", fmt.Errorf("get task run: %w", err)
	}
	v := runView(row.TaskRun, row.EffectiveStatus)
	return v.Status, v.ExitCode, v.Error, nil
}

// ListRuns returns a service's runs, newest first; taskID narrows to one task.
func (s *Service) ListRuns(ctx context.Context, serviceID uuid.UUID, taskID *uuid.UUID, limit int) ([]RunView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = HistoryPerTask
	}
	rows, err := s.q.ListServiceTaskRuns(ctx, db.ListServiceTaskRunsParams{ServiceID: sc.Service.ID, TaskID: taskID, RowLimit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list task runs: %w", err)
	}
	out := make([]RunView, len(rows))
	for i, r := range rows {
		out[i] = runView(r.TaskRun, r.EffectiveStatus)
	}
	return out, nil
}

// runScope authorizes scope on a run's service.
func (s *Service) runScope(ctx context.Context, scope string, id uuid.UUID) (db.GetTaskRunViewRow, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.GetTaskRunViewRow{}, err
	}
	row, err := s.q.GetTaskRunView(ctx, db.GetTaskRunViewParams{ID: id, WorkspaceID: policy.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, apierr.NotFound("task run")
	}
	if err != nil {
		return row, fmt.Errorf("get task run: %w", err)
	}
	if _, err := services.LoadScope(ctx, s.q, scope, row.TaskRun.ServiceID); err != nil {
		return row, err
	}
	return row, nil
}

// GetRun returns one run.
func (s *Service) GetRun(ctx context.Context, id uuid.UUID) (RunView, error) {
	row, err := s.runScope(ctx, policy.ScopeProjectsRead, id)
	if err != nil {
		return RunView{}, err
	}
	return runView(row.TaskRun, row.EffectiveStatus), nil
}

// RunLogs returns stored output lines after the given line id.
func (s *Service) RunLogs(ctx context.Context, id uuid.UUID, afterID int64, limit int) ([]LogLineView, error) {
	if _, err := s.runScope(ctx, policy.ScopeLogsRead, id); err != nil {
		return nil, err
	}
	return s.readLines(ctx, id, afterID, limit)
}

func (s *Service) readLines(ctx context.Context, id uuid.UUID, afterID int64, limit int) ([]LogLineView, error) {
	rows, err := s.q.ListTaskRunLogs(ctx, db.ListTaskRunLogsParams{RunID: id, ID: afterID, Limit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list task run logs: %w", err)
	}
	out := make([]LogLineView, len(rows))
	for i, r := range rows {
		out[i] = LogLineView{ID: r.ID, Stream: r.Stream, Line: r.Line, CreatedAt: r.CreatedAt}
	}
	return out, nil
}

const logPollInterval = 500 * time.Millisecond

// FollowLogs authorizes access and returns the SSE body that streams a run's
// output until it finishes, then sends `event: end` with the final status. The
// database is the source of truth: lines are already redacted when stored.
func (s *Service) FollowLogs(ctx context.Context, id uuid.UUID) (*huma.StreamResponse, error) {
	if _, err := s.runScope(ctx, policy.ScopeLogsRead, id); err != nil {
		return nil, err
	}
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		rctx := hctx.Context()
		st, err := services.NewStream(hctx)
		if err != nil {
			return
		}
		tick := time.NewTicker(logPollInterval)
		defer tick.Stop()
		ping := time.NewTicker(15 * time.Second)
		defer ping.Stop()
		var last int64
		for {
			// Read the status first: lines written before a terminal status
			// are then guaranteed to be flushed below.
			cur, err := s.q.GetTaskRunViewInternal(rctx, id)
			if err != nil {
				return
			}
			finished := cur.TaskRun.FinishedAt != nil
			for {
				lines, err := s.readLines(rctx, id, last, 500)
				if err != nil {
					return
				}
				for _, l := range lines {
					last = l.ID
					if st.Data(struct {
						Stream string    `json:"stream"`
						Line   string    `json:"line"`
						TS     time.Time `json:"ts"`
					}{l.Stream, l.Line, l.CreatedAt}) != nil {
						return
					}
				}
				if len(lines) < 500 {
					break
				}
			}
			if finished {
				_ = st.Event("end", struct {
					Status   string `json:"status"`
					ExitCode *int   `json:"exitCode,omitempty"`
				}{Status: cur.EffectiveStatus, ExitCode: runView(cur.TaskRun, cur.EffectiveStatus).ExitCode})
				return
			}
			select {
			case <-rctx.Done():
				return
			case <-ping.C:
				if st.Comment("ping") != nil {
					return
				}
			case <-tick.C:
			}
		}
	}}, nil
}

// ---- output intake ----

// cleanLine makes an output line safe to store: no NUL bytes, valid UTF-8, bounded.
func cleanLine(s string) string {
	const maxLine = 16 << 10
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	if len(s) > maxLine {
		s = s[:maxLine] + "…"
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}

// HandleTaskLog stores the output frame of a run: redacted with the service's
// secret values (fail closed: without a redactor nothing is stored), capped at
// MaxRunLogLines. Frames of unknown, foreign or finished runs are ignored.
func (s *Service) HandleTaskLog(ctx context.Context, serverID uuid.UUID, lf agentprotocol.LogFrame) error {
	if len(lf.Lines) == 0 {
		return nil
	}
	runID, err := uuid.Parse(lf.TaskRunID)
	if err != nil {
		return nil //nolint:nilerr // the run id comes from the agent's log frame; a non-UUID one can never match a run, and an error would only be logged for every frame
	}
	run, err := s.q.GetTaskRunInternal(ctx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load run: %w", err)
	}
	if run.ServerID == nil || *run.ServerID != serverID || run.FinishedAt != nil {
		return nil
	}
	if s.redactor == nil {
		return errors.New("no log redactor configured; dropping task output")
	}
	redact, err := s.redactor.RedactorFor(ctx, run.ServiceID)
	if err != nil {
		return fmt.Errorf("build redactor: %w", err)
	}
	have := int(run.LogLines)
	room := max(MaxRunLogLines-have, 0)
	rows := make([][]any, 0, min(len(lf.Lines), room)+1)
	now := s.now()
	for i, l := range lf.Lines {
		if i >= room {
			break
		}
		ts := l.TS
		if ts.IsZero() {
			ts = now
		}
		rows = append(rows, []any{runID, "task", cleanLine(redact(l.Text)), ts})
	}
	stored := len(rows)
	if have < MaxRunLogLines && have+len(lf.Lines) > MaxRunLogLines {
		rows = append(rows, []any{runID, "task", LogTruncatedMarker, now})
		stored = MaxRunLogLines + 1 - have
	}
	if len(rows) == 0 {
		return nil
	}
	if _, err := s.pool.CopyFrom(ctx, pgx.Identifier{"task_run_logs"}, []string{"run_id", "stream", "line", "created_at"}, pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("persist task output: %w", err)
	}
	if err := s.q.SetTaskRunLogLines(ctx, db.SetTaskRunLogLinesParams{ID: runID, LogLines: int32(have + stored)}); err != nil {
		return fmt.Errorf("count task output: %w", err)
	}
	return nil
}
