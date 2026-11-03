package gitproviders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Wait-for-CI timing.
const (
	// ciCheckInterval is how often a parked push's checks are read.
	ciCheckInterval = 30 * time.Second
	// ciNoneGrace is how long a commit without any check is waited for before it deploys.
	ciNoneGrace = 2 * time.Minute
	// ciMaxWait is how long a push may stay parked before it is dropped.
	ciMaxWait = 2 * time.Hour
)

const ciPermissionReason = "NebulaCtrl cannot read check runs: grant the GitHub App the 'Checks: read' permission and accept it on the installation."

// ciWait is the push parked by a service that waits for CI (service_git.ci_wait).
type ciWait struct {
	SHA       string    `json:"sha"`
	Message   string    `json:"message"`
	Author    string    `json:"author"`
	Branch    string    `json:"branch"`
	Note      string    `json:"note,omitempty"` // how the commit was found when it was not pushed (the deployment's message)
	Since     time.Time `json:"since"`
	CheckedAt time.Time `json:"checkedAt"`
}

// parkForCI parks a push until its checks pass; the newest push replaces an older one.
func (s *Service) parkForCI(ctx context.Context, serviceID uuid.UUID, ev PushEvent, label, note string) string {
	raw, _ := json.Marshal(ciWait{SHA: ev.CommitSHA, Message: ev.CommitMessage, Author: ev.Author, Branch: ev.Branch, Note: note, Since: s.now().UTC()})
	if err := s.q.SetServiceGitCIWait(ctx, db.SetServiceGitCIWaitParams{ServiceID: serviceID, CiWait: raw}); err != nil {
		s.log.Error("park push for CI", "service", serviceID.String(), "err", err)
		return label + ": not deployed (the push could not be parked to wait for CI)"
	}
	return label + ": waiting for CI"
}

// checkCI reads the checks of every parked push that is due and deploys, drops
// or keeps it (see resolveCIWait). A failure on one service never stops the others.
func (s *Service) checkCI(ctx context.Context) error {
	rows, err := s.q.ListServiceGitWaitingCI(ctx)
	if err != nil {
		return fmt.Errorf("list pushes waiting for CI: %w", err)
	}
	for _, row := range rows {
		sg := row.ServiceGit
		var w ciWait
		if err := json.Unmarshal(sg.CiWait, &w); err != nil || w.SHA == "" {
			s.log.Warn("unreadable CI wait", "service", sg.ServiceID.String(), "err", err)
			continue
		}
		if s.now().Sub(w.CheckedAt) < ciCheckInterval {
			continue
		}
		if err := s.resolveCIWait(ctx, sg, w); err != nil {
			s.log.Warn("check CI", "service", sg.ServiceID.String(), "commit", shortSHA(w.SHA), "err", err)
		}
	}
	return nil
}

// resolveCIWait moves one parked push forward:
//   - success, or no check for ciNoneGrace: deploy it;
//   - failure, a permission error or ciMaxWait elapsed: drop it and notify;
//   - pending or any other error: keep waiting.
//
// A push whose service no longer auto-deploys it (auto-deploy or wait-for-CI
// turned off, another branch) is dropped without a deployment.
func (s *Service) resolveCIWait(ctx context.Context, sg db.ServiceGit, w ciWait) error {
	take := func() (bool, error) {
		n, err := s.q.ClearServiceGitCIWait(ctx, db.ClearServiceGitCIWaitParams{ServiceID: sg.ServiceID, Sha: w.SHA})
		return n == 1, err
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, sg.ServiceID)
	if err != nil {
		return fmt.Errorf("load service: %w", err)
	}
	cfg, err := s.pushConfig(ctx, sc)
	if err != nil {
		return fmt.Errorf("load applied config: %w", err)
	}
	// A commit found by checking (Note) deploys on the check's own setting, not on the link's auto-deploy.
	if (!sg.AutoDeploy && w.Note == "") || !sg.WaitForCi || cfg.GitBranch != w.Branch {
		_, err := take()
		return err
	}
	c, err := s.loadConnectionInternal(ctx, sg.ConnectionID)
	if err != nil {
		return err
	}
	now := s.now()
	if err := s.q.TouchServiceGitCIWait(ctx, db.TouchServiceGitCIWaitParams{ServiceID: sg.ServiceID, Sha: w.SHA, CheckedAt: now.UTC().Format(time.RFC3339Nano)}); err != nil {
		return fmt.Errorf("record CI check: %w", err)
	}

	drv := driverFor(Provider(c.Provider))
	var res CIResult
	switch {
	case drv == nil || !drv.hasAPI() || c.AuthType == AuthWebhookOnly:
		err = errCINotSupported
	default:
		var cl *apiClient
		if _, cl, err = s.api(ctx, c); err == nil {
			res, err = drv.readCI(ctx, cl, sg.RepoFullName, w.SHA)
		}
	}
	age := now.Sub(w.Since)
	deploy := func() error {
		ok, err := take()
		if err != nil || !ok {
			return err
		}
		commit := &deployments.CommitInfo{SHA: w.SHA, Message: w.Message, Author: w.Author, Branch: w.Branch}
		_, note := s.deployPush(ctx, sg.ServiceID, commit, sc.Service.Slug, w.Note)
		s.log.Info("push deployed after CI", "service", sc.Service.Slug, "commit", shortSHA(w.SHA), "note", note)
		return nil
	}
	block := func(reason string) error {
		ok, err := take()
		if err != nil || !ok {
			return err
		}
		s.notifyBlockedByCI(ctx, sc, w, reason)
		return nil
	}

	switch {
	case errors.Is(err, errCIPermission):
		return block(ciPermissionReason)
	case errors.Is(err, errCINotSupported):
		return block("This connection cannot read CI results.")
	case err != nil:
		if age >= ciMaxWait {
			return block("CI results could not be read for 2 hours: " + err.Error())
		}
		return fmt.Errorf("read CI of %s@%s: %w", sg.RepoFullName, shortSHA(w.SHA), err)
	}
	switch res.State {
	case ciSuccess:
		return deploy()
	case ciNone:
		if age >= ciNoneGrace {
			return deploy()
		}
	case ciFailure:
		return block("CI failed: " + orDefault(res.Summary, "a check failed"))
	default: // pending
		if age >= ciMaxWait {
			return block("CI still pending after 2 hours")
		}
	}
	return nil
}

func (s *Service) notifyBlockedByCI(ctx context.Context, sc services.Scope, w ciWait, reason string) {
	s.log.Info("push not deployed: CI", "service", sc.Service.Slug, "commit", shortSHA(w.SHA), "reason", reason)
	projectID, serviceID := sc.ProjectID, sc.Service.ID
	notifications.Emit(ctx, s.ntf, notifications.Event{
		Type: notifications.TypeDeployBlockedByCI, ProjectID: &projectID, ServiceID: &serviceID, Severity: notifications.SeverityWarning,
		Title:  "Push to " + sc.Service.Name + " was not deployed",
		Body:   reason,
		Fields: map[string]string{"Commit": shortSHA(w.SHA), "Reason": reason},
	})
}
