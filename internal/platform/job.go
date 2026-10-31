package platform

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// Poll intervals and deadlines of the provisioning job.
const (
	awaitPoll = 5 * time.Second
	wirePoll  = 10 * time.Second
	// deployDeadline bounds the deployments of one platform service (a first
	// pull of the largest image takes minutes on a slow link).
	deployDeadline = 30 * time.Minute
	// wireDeadline bounds the wait for the public URL to answer: certificates
	// and DNS can take a few minutes.
	wireDeadline = 15 * time.Minute
)

// ProvisionArgs drives one platform service through configure, deploy, await and
// wire. All state lives in platform_services, so any worker can continue it.
type ProvisionArgs struct {
	ID uuid.UUID `json:"id"`
}

// Kind implements river.JobArgs.
func (ProvisionArgs) Kind() string { return "platform_provision" }

// InsertOpts implements river.JobArgsWithInsertOpts: one live job per service.
func (ProvisionArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 5,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			},
		},
	}
}

type provisionWorker struct {
	river.WorkerDefaults[ProvisionArgs]
	svc *Service
}

func (w *provisionWorker) Work(ctx context.Context, job *river.Job[ProvisionArgs]) error {
	wait, done, err := w.svc.advance(ctx, job.Args.ID)
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	return river.JobSnooze(wait)
}

// RegisterJobs registers the provisioning worker.
func RegisterJobs(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &provisionWorker{svc: svc}) })
}

// retryError asks the job to try the same step again later.
type retryError struct{ msg string }

func (e *retryError) Error() string { return e.msg }

func retryLater(format string, a ...any) error { return &retryError{msg: fmt.Sprintf(format, a...)} }

// fatalError fails the platform service with a message for the user.
type fatalError struct{ msg string }

func (e *fatalError) Error() string { return e.msg }

func fatalf(format string, a ...any) error { return &fatalError{msg: fmt.Sprintf(format, a...)} }

// Advance moves a platform service one step (the body of the provisioning job; exported
// for tests that drive the steps by hand).
func (s *Service) Advance(ctx context.Context, id uuid.UUID) (time.Duration, bool, error) {
	return s.advance(ctx, id)
}

// advance moves a platform service one step. It reports how long to wait before
// the next call, or done when provisioning ended (ready, failed or removed).
func (s *Service) advance(ctx context.Context, id uuid.UUID) (time.Duration, bool, error) {
	row, err := s.q.GetPlatformServiceInternal(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	if row.Status != StatusProvisioning {
		return 0, true, nil
	}
	sys := sysCtx(ctx, row.WorkspaceID)
	opts := decodeJSON[options](row.Options)
	since := s.stepSince(&opts, row)

	var next string
	var wait time.Duration
	var stepErr error
	switch row.Step {
	case StepConfigure:
		next = StepDeploy
	case StepDeploy:
		var ids []uuid.UUID
		ids, stepErr = s.deploy(sys, row)
		if stepErr == nil {
			row, err = s.q.SetPlatformProgress(ctx, db.SetPlatformProgressParams{ID: id, Status: StatusProvisioning, Step: StepAwait, StatusMessage: "Deploying", DeploymentIds: ids})
			if err != nil {
				return 0, false, err
			}
			s.touchStep(ctx, row, opts)
			s.publish(row)
			return 0, false, nil
		}
	case StepAwait:
		done, m, err := s.await(ctx, row, since)
		stepErr = err
		if err == nil && !done {
			if m != row.StatusMessage {
				s.progress(ctx, row, StepAwait, m)
			}
			return awaitPoll, false, nil
		}
		next = StepWire
	case StepWire:
		stepErr = s.wire(sys, row, &opts)
		if stepErr == nil {
			next = StepDone
		}
	default:
		return 0, true, nil
	}

	var re *retryError
	var fe *fatalError
	switch {
	case stepErr == nil:
		status, message := StatusProvisioning, ""
		if next == StepDone {
			status = StatusReady
		}
		cur, err := s.q.SetPlatformProgress(ctx, db.SetPlatformProgressParams{ID: id, Status: status, Step: next, StatusMessage: message, DeploymentIds: row.DeploymentIds})
		if err != nil {
			return 0, false, err
		}
		if next != StepDone {
			s.touchStep(ctx, cur, opts)
		}
		s.publish(cur)
		return 0, next == StepDone, nil
	case errors.As(stepErr, &re):
		limit := wireDeadline
		if row.Step == StepDeploy || row.Step == StepAwait {
			limit = deployDeadline
		}
		if s.Now().Sub(since) > limit {
			s.fail(ctx, row, fmt.Sprintf("Timed out: %s", re.msg))
			return 0, true, nil
		}
		if re.msg != row.StatusMessage {
			s.progress(ctx, row, row.Step, re.msg)
		}
		wait = wirePoll
		return wait, false, nil
	case errors.As(stepErr, &fe):
		s.fail(ctx, row, fe.msg)
		return 0, true, nil
	default:
		// Anything else is unexpected (a database error, a bug): let River retry,
		// and say so after the last attempt.
		s.Logger.ErrorContext(ctx, "platform: step failed", "platform_service", id.String(), "step", row.Step, "err", stepErr)
		if apiMsg := userMessage(stepErr); apiMsg != "" {
			s.fail(ctx, row, apiMsg)
			return 0, true, nil
		}
		return 0, false, stepErr
	}
}

func (s *Service) stepSince(o *options, row db.PlatformService) time.Time {
	if o.StepSince != nil {
		return *o.StepSince
	}
	return row.UpdatedAt
}

// touchStep stamps the start of the new step.
func (s *Service) touchStep(ctx context.Context, row db.PlatformService, o options) {
	now := s.Now()
	o.StepSince = &now
	if _, err := s.q.SetPlatformOptions(ctx, db.SetPlatformOptionsParams{ID: row.ID, Options: encodeJSON(o)}); err != nil {
		s.Logger.WarnContext(ctx, "platform: record step start", "platform_service", row.ID.String(), "err", err)
	}
}

func (s *Service) progress(ctx context.Context, row db.PlatformService, step, msg string) {
	cur, err := s.q.SetPlatformProgress(ctx, db.SetPlatformProgressParams{ID: row.ID, Status: StatusProvisioning, Step: step, StatusMessage: msg, DeploymentIds: row.DeploymentIds})
	if err == nil {
		s.publish(cur)
	}
}

func (s *Service) fail(ctx context.Context, row db.PlatformService, msg string) {
	cur, err := s.q.SetPlatformProgress(ctx, db.SetPlatformProgressParams{ID: row.ID, Status: StatusFailed, Step: row.Step, StatusMessage: msg, DeploymentIds: row.DeploymentIds})
	if err != nil {
		s.Logger.ErrorContext(ctx, "platform: record failure", "platform_service", row.ID.String(), "err", err)
		return
	}
	s.Logger.WarnContext(ctx, "platform: provisioning failed", "platform_service", row.ID.String(), "kind", row.Kind, "step", row.Step, "message", msg)
	s.publish(cur)
	// A failed redeploy or wiring of a migrating Headscale keeps the mesh paused
	// and says why (a no-op when no migration is running).
	s.afterHeadscaleWire(ctx, row.WorkspaceID, fatalf("%s", msg))
}

// deploy starts the deployments of the staged services.
func (s *Service) deploy(ctx context.Context, row db.PlatformService) ([]uuid.UUID, error) {
	if row.EnvironmentID == nil {
		return nil, fatalf("The platform environment no longer exists.")
	}
	res, err := s.Deploy.DeployStaged(ctx, *row.EnvironmentID, row.ServiceIds, "Platform service: "+row.Kind)
	if err != nil {
		if ae, ok := apierr.As(err); ok && ae.Code == "deployment_in_progress" {
			return nil, retryLater("A deployment is already running; waiting for it to finish")
		}
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(res.Items))
	for _, d := range res.Items {
		ids = append(ids, d.ID)
	}
	if len(ids) == 0 {
		return nil, fatalf("Nothing could be deployed: the service has no staged changes or its server is not usable.")
	}
	return ids, nil
}

// await waits for the deployments to finish.
func (s *Service) await(ctx context.Context, row db.PlatformService, since time.Time) (done bool, msg string, err error) {
	if len(row.DeploymentIds) == 0 {
		return true, "", nil
	}
	deps, err := s.q.ListDeploymentsByIDs(ctx, row.DeploymentIds)
	if err != nil {
		return false, "", err
	}
	running := 0
	for _, d := range deps {
		st := deployments.Status(d.Status)
		switch {
		case st == deployments.Succeeded:
		case st.IsTerminal():
			m := d.StatusMessage
			if m == "" {
				m = "see the deployment log in the NebulaCtrl platform project"
			}
			return false, "", fatalf("The deployment %s: %s", d.Status, m)
		default:
			running++
		}
	}
	if running > 0 {
		if s.Now().Sub(since) > deployDeadline {
			return false, "", fatalf("The deployment did not finish within %s.", deployDeadline)
		}
		return false, "Deploying", nil
	}
	return true, "", nil
}

// userMessage is the message of an API error (validation and conflict errors
// from the services the job calls); "" for anything else.
func userMessage(err error) string {
	if ae, ok := apierr.As(err); ok && ae.Status < 500 {
		return ae.Error()
	}
	return ""
}
