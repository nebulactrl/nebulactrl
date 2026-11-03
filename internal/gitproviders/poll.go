package gitproviders

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Check for new commits: a service without a working webhook asks the remote
// for the head of its branch on a schedule (service_git_poll) and, when asked
// to, deploys a commit that is not running yet.
const (
	// gitPollBatch is how many due checks one sweep takes.
	gitPollBatch = 20
	// gitPollParallel is how many of them run at once.
	gitPollParallel = 5
	// gitPollTimeout bounds one head check.
	gitPollTimeout = 20 * time.Second
	// gitPollMaxError is the longest error text that is stored.
	gitPollMaxError = 300

	guardSetGitPoll = "setServiceGitPoll"
)

// GitPollInput is the body of PutGitPoll.
type GitPollInput struct {
	Enabled bool
	// IntervalMinutes is 1, 5, 15 or 60 (default 5).
	IntervalMinutes *int
	// Deploy deploys a new commit found by checking (default true).
	Deploy *bool
}

// pollGit runs the checks that are due: it claims up to gitPollBatch of them
// (each claim moves the next check one interval ahead, so concurrent sweeps
// never check the same service) and reads, records and acts on each head. A
// failure of one service never stops the others.
func (s *Service) pollGit(ctx context.Context) error {
	rows, err := s.q.ClaimDueGitPolls(ctx, db.ClaimDueGitPollsParams{Now: s.now().UTC(), UnitSeconds: s.pollUnit.Seconds(), RowLimit: gitPollBatch})
	if err != nil {
		return fmt.Errorf("claim due checks: %w", err)
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, gitPollParallel)
	for _, p := range rows {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			if err := s.pollOne(ctx, p); err != nil {
				s.log.Warn("check for new commits", "service", p.ServiceID.String(), "err", err)
			}
		}()
	}
	wg.Wait()
	return nil
}

// readHead resolves the branch head of a service with the check's timeout.
func (s *Service) readHead(ctx context.Context, serviceID uuid.UUID) (sha, branch string, err error) {
	if s.dep == nil {
		return "", "", errors.New("deployments are unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, gitPollTimeout)
	defer cancel()
	return s.dep.ResolveHead(ctx, serviceID)
}

// pollError is the stored text of a failed check.
func pollError(err error) string {
	if ae, ok := apierr.As(err); ok {
		return truncate(ae.Message, gitPollMaxError)
	}
	return truncate(err.Error(), gitPollMaxError)
}

// recordHead stores the outcome of a check and reports whether it changed what the service shows.
func (s *Service) recordHead(ctx context.Context, p db.ServiceGitPoll, sha, branch string, herr error) (changed bool, err error) {
	now := s.now().UTC()
	params := db.SetGitPollResultParams{ServiceID: p.ServiceID, CheckedAt: &now}
	if herr != nil {
		params.LastError = pollError(herr)
	} else {
		params.HeadSha, params.HeadBranch = &sha, &branch
	}
	if err := s.q.SetGitPollResult(ctx, params); err != nil {
		return false, fmt.Errorf("record check: %w", err)
	}
	headSame := herr != nil || (p.HeadSha != nil && *p.HeadSha == sha && p.HeadBranch != nil && *p.HeadBranch == branch)
	return !headSame || p.LastError != params.LastError, nil
}

// pollOne checks one claimed service and, when its poll deploys and the head
// is a commit that is not running and was not handled yet, deploys it.
func (s *Service) pollOne(ctx context.Context, p db.ServiceGitPoll) error {
	sc, err := services.LoadScopeInternal(ctx, s.q, p.ServiceID)
	if err != nil {
		return fmt.Errorf("load service: %w", err)
	}
	sha, branch, herr := s.readHead(ctx, p.ServiceID)
	changed, err := s.recordHead(ctx, p, sha, branch, herr)
	if err != nil {
		return err
	}
	if changed {
		services.PublishService(s.bus, services.EventServiceUpdated, sc)
	}
	if herr != nil {
		// The error stays on the service (no notification: a remote that is down would repeat it every interval).
		return fmt.Errorf("read the head: %w", herr)
	}
	if !p.Deploy {
		return nil
	}
	note, err := s.pollDeploy(ctx, sc, p, sha, branch)
	if err != nil {
		return err
	}
	if note != "" {
		s.log.Info("new commit found by checking", "service", sc.Service.Slug, "commit", shortSHA(sha), "note", note)
	}
	return nil
}

// runningCommit is the commit the service's current release was built from ("" when it runs none).
func (s *Service) runningCommit(ctx context.Context, serviceID uuid.UUID) (string, error) {
	rel, err := s.q.GetServiceCurrentRelease(ctx, serviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load the running release: %w", err)
	}
	return services.DecodeSource(rel.Source).CommitSHA, nil
}

// pollNote is the deployment message of a commit found by checking.
func pollNote(branch string, minutes int32) string {
	every := fmt.Sprintf("%d min", minutes)
	switch minutes {
	case 1:
		every = "minute"
	case 60:
		every = "hour"
	}
	return truncate(fmt.Sprintf("New commit found by checking %s every %s", branch, every), deployments.MaxMessageLen)
}

// pollDeploy deploys head when the service may take it now. It does nothing
// (empty note) for a service that is stopped, a cron service, a preview, one
// that runs no commit yet, one already running head, and a commit that
// already has a deployment after the running release or waits to deploy (a
// failed commit is never retried, a rollback is not undone). The deployment
// goes the way a push goes after its gates: through wait-for-CI when the
// service's link asks for it, else straight to a git_push deployment.
func (s *Service) pollDeploy(ctx context.Context, sc services.Scope, p db.ServiceGitPoll, head, branch string) (string, error) {
	if sc.Service.DesiredState == services.DesiredStopped || !sc.SupportsGitPoll() {
		return "", nil
	}
	env, err := s.q.GetEnvironmentInternal(ctx, sc.EnvironmentID)
	if err != nil {
		return "", fmt.Errorf("load environment: %w", err)
	}
	if env.Environment.Kind == "preview" {
		return "", nil
	}
	cfg, err := s.pushConfig(ctx, sc)
	if err != nil {
		return "", fmt.Errorf("load applied config: %w", err)
	}
	if cfg.Cron != nil || cfg.GitBranch != branch {
		return "", nil
	}
	running, err := s.runningCommit(ctx, sc.Service.ID)
	if err != nil {
		return "", err
	}
	if running == "" || running == head {
		return "", nil
	}
	handled, err := s.q.GitPollCommitHandled(ctx, db.GitPollCommitHandledParams{ServiceID: sc.Service.ID, Sha: head})
	if err != nil {
		return "", fmt.Errorf("look up deployments of the commit: %w", err)
	}
	if handled {
		return "", nil
	}
	label, note := sc.Service.Slug, pollNote(branch, p.IntervalMinutes)
	sg, err := s.q.GetServiceGit(ctx, sc.Service.ID)
	switch {
	case err == nil && sg.WaitForCi:
		return s.parkForCI(ctx, sc.Service.ID, PushEvent{CommitSHA: head, Branch: branch}, label, note), nil
	case errors.Is(err, pgx.ErrNoRows):
		// Without a git link there is no queue for a busy service: look again soon instead.
		busy, err := s.q.CountActiveDeployments(ctx, sc.Service.ID)
		if err != nil {
			return "", fmt.Errorf("count active deployments: %w", err)
		}
		if busy > 0 {
			err := s.q.RetryGitPollSoon(ctx, db.RetryGitPollSoonParams{ServiceID: sc.Service.ID, At: s.now().UTC().Add(s.pollUnit)})
			return label + ": deployment in progress, will check again soon", err
		}
	case err != nil:
		return "", fmt.Errorf("load git link: %w", err)
	}
	_, msg := s.deployPush(ctx, sc.Service.ID, &deployments.CommitInfo{SHA: head, Branch: branch}, label, note)
	return msg, nil
}

// pollView renders the "check for new commits" state of a service.
func (s *Service) pollView(ctx context.Context, sc services.Scope) (services.GitPollView, error) {
	p, err := s.q.GetGitPoll(ctx, sc.Service.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return services.OffGitPollView(), nil
	}
	if err != nil {
		return services.GitPollView{}, fmt.Errorf("load check: %w", err)
	}
	running, err := s.runningCommit(ctx, sc.Service.ID)
	if err != nil {
		return services.GitPollView{}, err
	}
	return services.NewGitPollView(p, running), nil
}

// GetGitPoll returns the "check for new commits" state of a service (enabled false when it does not check).
func (s *Service) GetGitPoll(ctx context.Context, serviceID uuid.UUID) (services.GitPollView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return services.GitPollView{}, err
	}
	return s.pollView(ctx, sc)
}

// PutGitPoll turns checking for new commits on or off and sets its interval
// and whether a new commit deploys. Enabling schedules the first check for the
// next sweep. It is an operational setting: applied at once, never staged.
func (s *Service) PutGitPoll(ctx context.Context, serviceID uuid.UUID, in GitPollInput) (services.GitPollView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return services.GitPollView{}, err
	}
	minutes, deploy := services.GitPollDefaultInterval, true
	if in.IntervalMinutes != nil {
		minutes = *in.IntervalMinutes
	}
	if in.Deploy != nil {
		deploy = *in.Deploy
	}
	if in.Enabled {
		if !sc.SupportsGitPoll() {
			return services.GitPollView{}, apierr.Unprocessable("git_poll_not_applicable", "only an app or worker built from a git repository can check for new commits")
		}
		if !services.ValidGitPollInterval(minutes) {
			return services.GitPollView{}, apierr.Unprocessable("invalid_git_poll_interval", "intervalMinutes must be 1, 5, 15 or 60")
		}
		if err := sc.Guard(ctx, guardSetGitPoll, fmt.Sprintf("Check for new commits of %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), false,
			[]string{"With deploy on, a new commit found on the branch deploys the service by itself, as a push does.", fmt.Sprintf("Every %d min, deploy: %t.", minutes, deploy)},
			minutes, deploy); err != nil {
			return services.GitPollView{}, err
		}
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		meta := map[string]any{"slug": sc.Service.Slug, "enabled": in.Enabled}
		if in.Enabled {
			if _, err := q.UpsertGitPoll(ctx, db.UpsertGitPollParams{ServiceID: serviceID, IntervalMinutes: int32(minutes), Deploy: deploy, NextCheckAt: s.now().UTC()}); err != nil { //nolint:gosec // minutes is 1, 5, 15 or 60
				return err
			}
			meta["intervalMinutes"], meta["deploy"] = minutes, deploy
		} else if err := q.DeleteGitPoll(ctx, serviceID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "service.git_poll.update", ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID, Metadata: meta,
		})
	})
	if err != nil {
		return services.GitPollView{}, fmt.Errorf("save check for new commits: %w", err)
	}
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
	return s.pollView(ctx, sc)
}

// CheckGitPoll reads the branch head now (it never deploys) and returns the
// view. The result is stored when the service checks; for one that does not,
// the view carries the head that was read and nothing is stored.
func (s *Service) CheckGitPoll(ctx context.Context, serviceID uuid.UUID) (services.GitPollView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return services.GitPollView{}, err
	}
	if !sc.SupportsGitPoll() {
		return services.GitPollView{}, apierr.Unprocessable("git_poll_not_applicable", "only an app or worker built from a git repository can check for new commits")
	}
	sha, branch, herr := s.readHead(ctx, serviceID)
	p, err := s.q.GetGitPoll(ctx, serviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		now := s.now().UTC()
		off := db.ServiceGitPoll{ServiceID: serviceID, IntervalMinutes: services.GitPollDefaultInterval, Deploy: true, CheckedAt: &now}
		if herr != nil {
			off.LastError = pollError(herr)
		} else {
			off.HeadSha, off.HeadBranch = &sha, &branch
		}
		running, err := s.runningCommit(ctx, serviceID)
		if err != nil {
			return services.GitPollView{}, err
		}
		v := services.NewGitPollView(off, running)
		v.Enabled = false
		return v, nil
	}
	if err != nil {
		return services.GitPollView{}, fmt.Errorf("load check: %w", err)
	}
	changed, err := s.recordHead(ctx, p, sha, branch, herr)
	if err != nil {
		return services.GitPollView{}, err
	}
	if changed {
		services.PublishService(s.bus, services.EventServiceUpdated, sc)
	}
	return s.pollView(ctx, sc)
}
