package gitproviders

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
)

// SetCommitStatus shows a status next to a commit on the provider (a GitHub
// commit status, a GitLab pipeline status, a Gitea status). It performs no
// authorization: callers are NebulaCtrl's own deployment and preview code.
func (s *Service) SetCommitStatus(ctx context.Context, connectionID uuid.UUID, repo, sha string, st CommitStatus) error {
	c, err := s.loadConnectionInternal(ctx, connectionID)
	if err != nil {
		return err
	}
	switch st.State {
	case StatePending, StateSuccess, StateFailure, StateError:
	default:
		return fmt.Errorf("invalid commit status state %q", st.State)
	}
	drv, cl, err := s.api(ctx, c)
	if err != nil {
		return err
	}
	if err := drv.setStatus(ctx, cl, repo, sha, st); err != nil {
		return fmt.Errorf("set commit status on %s@%s: %w", repo, shortSHA(sha), err)
	}
	return nil
}

// UpsertPRComment creates or updates the one comment of a pull request that
// contains marker (a sticky comment such as the preview URL list). body is
// Markdown and must contain marker.
func (s *Service) UpsertPRComment(ctx context.Context, connectionID uuid.UUID, repo string, number int, marker, body string) error {
	if marker == "" || !strings.Contains(body, marker) {
		return errors.New("the comment body must contain the marker")
	}
	c, err := s.loadConnectionInternal(ctx, connectionID)
	if err != nil {
		return err
	}
	drv, cl, err := s.api(ctx, c)
	if err != nil {
		return err
	}
	if err := drv.upsertComment(ctx, cl, repo, number, marker, body); err != nil {
		return fmt.Errorf("comment on %s#%d: %w", repo, number, err)
	}
	return nil
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// StateForDeployment maps a deployment status to the commit status state.
// Keep in sync with ListDeploymentsNeedingGitStatus.
func StateForDeployment(status string) string {
	switch deployments.Status(status) {
	case deployments.Succeeded:
		return StateSuccess
	case deployments.Failed, deployments.RolledBack, deployments.RollbackFailed:
		return StateFailure
	case deployments.Canceled:
		return StateError
	}
	return StatePending
}

func describeDeployment(status, message string) string {
	switch deployments.Status(status) {
	case deployments.Succeeded:
		return "Deployed"
	case deployments.Failed:
		return orDefault(message, "Deployment failed")
	case deployments.RolledBack:
		return "Rolled back: " + orDefault(message, "the new release did not become healthy")
	case deployments.RollbackFailed:
		return "Rollback failed: " + orDefault(message, "manual recovery is needed")
	case deployments.Canceled:
		return "Deployment canceled"
	case deployments.Building:
		return "Building"
	case deployments.Releasing, deployments.Verifying:
		return "Deploying"
	}
	return "Queued"
}

func orDefault(s, def string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return def
}

// deploymentURL is the UI page of a deployment ("" without a public URL).
func (s *Service) deploymentURL(projectID uuid.UUID, envSlug string, serviceID, deploymentID uuid.UUID) string {
	if s.publicURL() == "" {
		return ""
	}
	q := url.Values{"service": {serviceID.String()}, "tab": {"deployments"}, "deployment": {deploymentID.String()}}
	return s.publicURL() + "/p/" + projectID.String() + "/" + url.PathEscape(envSlug) + "?" + q.Encode()
}

// reportDeployment brings the provider's commit status of a deployment up to
// date: pending while it runs, success or failure when it ends, each state sent
// once. It is a no-op for deployments of unlinked services.
func (s *Service) reportDeployment(ctx context.Context, deploymentID uuid.UUID) error {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	sub, err := s.q.GetGitStatusSubject(ctx, deploymentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load deployment for commit status: %w", err)
	}
	dep := sub.Deployment
	if dep.CommitSha == "" {
		return nil
	}
	state := StateForDeployment(dep.Status)
	if sub.ReportedState == state {
		return nil
	}
	c, err := s.loadConnectionInternal(ctx, sub.ConnectionID)
	if err != nil {
		return err
	}
	drv := driverFor(Provider(c.Provider))
	if drv == nil || !drv.hasAPI() || c.AuthType == AuthWebhookOnly {
		// Nothing to report to (Bitbucket): remember it so the sweep moves on.
		return s.q.MarkGitStatusReported(ctx, db.MarkGitStatusReportedParams{DeploymentID: deploymentID, State: state})
	}
	st := CommitStatus{
		State: state, Description: describeDeployment(dep.Status, dep.StatusMessage), Context: "nebulactrl/" + sub.ServiceSlug,
		TargetURL: s.deploymentURL(sub.ProjectID, sub.EnvironmentSlug, dep.ServiceID, dep.ID),
	}
	if err := s.SetCommitStatus(ctx, sub.ConnectionID, sub.RepoFullName, dep.CommitSha, st); err != nil {
		s.log.Warn("commit status not reported", "deployment", deploymentID.String(), "repo", sub.RepoFullName, "err", err)
		if merr := s.q.MarkGitStatusFailed(ctx, db.MarkGitStatusFailedParams{DeploymentID: deploymentID, Error: truncate(err.Error(), 500)}); merr != nil {
			return merr
		}
		return nil
	}
	return s.q.MarkGitStatusReported(ctx, db.MarkGitStatusReportedParams{DeploymentID: deploymentID, State: state})
}
