package tasks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Handles reports whether a command kind belongs to this package.
func Handles(kind string) bool { return kind == agentprotocol.KindRunTask }

// SecretsForCommand attaches the send-time secrets of a run_task command: the
// release's variable values (`env:<KEY>`) and the registry credentials of an
// image source. They are never persisted and never part of the payload.
func (s *Service) SecretsForCommand(ctx context.Context, kind string, payload json.RawMessage) (map[string]string, error) {
	if kind != agentprotocol.KindRunTask {
		return nil, nil
	}
	var p agentprotocol.RunTaskPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("decode run_task payload: %w", err)
	}
	relID, err := uuid.Parse(p.ReleaseID)
	if err != nil {
		return nil, fmt.Errorf("run_task release id: %w", err)
	}
	rel, err := s.q.GetReleaseByID(ctx, relID)
	if err != nil {
		return nil, fmt.Errorf("get release: %w", err)
	}
	out := map[string]string{}
	if len(rel.EnvEnc) > 0 {
		plain, err := s.box.Open(secretbox.ReleasesEnv, rel.ID.String(), rel.EnvEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt release env: %w", err)
		}
		var env services.EnvSnapshot
		if err := json.Unmarshal(plain, &env); err != nil {
			return nil, fmt.Errorf("decode release env: %w", err)
		}
		var fetcher services.ExtFetcher
		extSvc := uuid.Nil
		if len(env.Ext) > 0 {
			if extSvc, err = uuid.Parse(p.ServiceID); err != nil {
				return nil, fmt.Errorf("run_task service id: %w", err)
			}
			sc, err := services.LoadScopeInternal(ctx, s.q, extSvc)
			if err != nil {
				return nil, fmt.Errorf("load service: %w", err)
			}
			xs := sc.ExtScope(services.ExtPurposeTask)
			xs.DeploymentID = p.DeploymentID
			fetcher = s.ext.Fetcher(xs)
		}
		vars, leaves, err := env.Materialize(ctx, fetcher)
		if err != nil {
			return nil, err
		}
		s.ext.Remember(extSvc, leaves)
		for _, k := range p.EnvKeys {
			if v, ok := vars[k]; ok {
				out[agentprotocol.SecretEnvPrefix+k] = v
			}
		}
	}
	for _, k := range p.EnvKeys {
		switch k {
		case services.VarDeploymentID:
			out[agentprotocol.SecretEnvPrefix+k] = p.DeploymentID
		case services.VarReleaseID:
			out[agentprotocol.SecretEnvPrefix+k] = p.ReleaseID
		}
	}
	if p.HasRegistryAuth {
		svcID, err := uuid.Parse(p.ServiceID)
		if err != nil {
			return nil, fmt.Errorf("run_task service id: %w", err)
		}
		sc, err := services.LoadScopeInternal(ctx, s.q, svcID)
		if err != nil {
			return nil, fmt.Errorf("load service: %w", err)
		}
		if len(sc.Service.RegistryAuthEnc) > 0 {
			raw, err := s.box.Open(secretbox.ServicesRegistryAuth, svcID.String(), sc.Service.RegistryAuthEnc)
			if err != nil {
				return nil, fmt.Errorf("decrypt registry auth: %w", err)
			}
			out[agentprotocol.SecretRegistryAuth] = base64.URLEncoding.EncodeToString(raw)
			secretbox.Wipe(raw)
		}
	}
	return out, nil
}

// scrub masks the service's secret values in an agent-provided message.
func (s *Service) scrub(ctx context.Context, serviceID uuid.UUID, msg string) string {
	if s.redactor != nil {
		if redact, err := s.redactor.RedactorFor(ctx, serviceID); err == nil {
			msg = redact(msg)
		} else {
			msg = "" // fail closed: a message that can not be redacted is dropped
		}
	}
	return clip(msg)
}

// HandleResult applies the result of a run_task command. Commands of other
// kinds are ignored; duplicates are no-ops.
func (s *Service) HandleResult(ctx context.Context, commandID uuid.UUID, res agentprotocol.ResultFrame) error {
	run, err := s.q.GetTaskRunByCommand(ctx, &commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load run: %w", err)
	}
	var data agentprotocol.RunTaskResult
	_ = json.Unmarshal(res.Data, &data)
	msg := res.Error
	if msg == "" {
		msg = data.Message
	}
	status := StatusFailed
	var exit *int32
	if !data.StartedAt.IsZero() || res.Outcome == agentprotocol.OutcomeSucceeded {
		c := int32(data.ExitCode) // the container ran: its exit code is real
		exit = &c
	}
	switch {
	case data.TimedOut:
		status = StatusTimedOut
	case res.Outcome == agentprotocol.OutcomeSucceeded:
		status = StatusSucceeded
		msg = ""
	case msg == "":
		msg = "the agent reported a failed run"
	}
	var started, finished *time.Time
	if !data.StartedAt.IsZero() {
		started = &data.StartedAt
	}
	if !data.FinishedAt.IsZero() {
		finished = &data.FinishedAt
	}
	return s.finish(ctx, run, status, exit, s.scrub(ctx, run.ServiceID, msg), started, finished)
}

// HandleUndelivered fails the run of a run_task command that never reached
// its agent (expired or canceled before being acknowledged).
func (s *Service) HandleUndelivered(ctx context.Context, commandID uuid.UUID, msg string) error {
	run, err := s.q.GetTaskRunByCommand(ctx, &commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load run: %w", err)
	}
	return s.finish(ctx, run, StatusFailed, nil, clip(msg), nil, nil)
}

// finish records a run's outcome (once), prunes history, announces the change
// and notifies about failures of unattended runs.
func (s *Service) finish(ctx context.Context, run db.TaskRun, status string, exit *int32, msg string, started, finished *time.Time) error {
	done, err := s.q.FinishTaskRun(ctx, db.FinishTaskRunParams{
		ID: run.ID, Status: status, ExitCode: exit, Error: msg, StartedAt: started, FinishedAt: finished,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already finished: a duplicate result
	}
	if err != nil {
		return fmt.Errorf("finish run: %w", err)
	}
	if _, err := s.q.PruneTaskRuns(ctx, db.PruneTaskRunsParams{ServiceID: done.ServiceID, TaskID: done.TaskID, Keep: HistoryPerTask}); err != nil {
		s.log.WarnContext(ctx, "prune task run history", "service_id", done.ServiceID, "err", err)
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, done.ServiceID)
	if err != nil {
		if apierr.IsNotFound(err) {
			return nil // the service was deleted after the run finished; the result is stored, there is nobody left to notify
		}
		return fmt.Errorf("load service: %w", err)
	}
	s.publishRun(sc, done.ID)
	s.notifyFailure(ctx, sc, done)
	return nil
}

// notifyFailure emits task.failed for a failed or timed-out run that nobody is
// watching: cron runs and runs started by features (preview seeds). A person
// who started a run from the UI, CLI or MCP sees the result directly.
func (s *Service) notifyFailure(ctx context.Context, sc services.Scope, run db.TaskRun) {
	if run.Status != StatusFailed && run.Status != StatusTimedOut {
		return
	}
	if run.Source == SourceManual || run.Source == SourceOneOff {
		return
	}
	s.emitFailure(ctx, sc, run)
}

func (s *Service) emitFailure(ctx context.Context, sc services.Scope, run db.TaskRun) {
	if s.notifier == nil {
		return
	}
	title := fmt.Sprintf("Task %q failed on %s", run.Name, sc.Service.Name)
	body := run.Error
	if run.Status == StatusTimedOut {
		body = "The task timed out and was stopped. " + body
	}
	fields := map[string]string{"Task": run.Name, "Source": run.Source}
	if run.ExitCode != nil {
		fields["Exit code"] = strconv.Itoa(int(*run.ExitCode))
	}
	if run.StartedAt != nil && run.FinishedAt != nil {
		fields["Duration"] = run.FinishedAt.Sub(*run.StartedAt).Round(time.Second).String()
	}
	projectID, serviceID := sc.ProjectID, sc.Service.ID
	notifications.Emit(ctx, s.notifier, notifications.Event{
		Type: notifications.TypeTaskFailed, ProjectID: &projectID, ServiceID: &serviceID, ServerID: run.ServerID,
		Severity: notifications.SeverityError, Title: title, Body: strings.TrimSpace(body), Fields: fields,
	})
}
