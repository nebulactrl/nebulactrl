package gitproviders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// pendingCommit is the newest push that arrived while a deployment of the
// service was running; it is deployed when that deployment ends.
type pendingCommit struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Author  string `json:"author"`
	Branch  string `json:"branch"`
}

func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }

// skipTokens in a commit message keep a push from deploying.
var skipTokens = []string{"[skip ci]", "[ci skip]", "[skip deploy]", "[skip nebula]", "[nebula skip]"}

func skipToken(msg string) string {
	l := strings.ToLower(msg)
	for _, t := range skipTokens {
		if strings.Contains(l, t) {
			return t
		}
	}
	return ""
}

// onPush deploys every auto-deploy service linked to the pushed repository and
// branch whose watch paths the push touches. It returns the deployments it
// started and a one-line summary for the delivery log. A service that is
// already deploying keeps the newest pushed commit queued instead. The push
// hook (project config) runs first, so that a deployment of this push sees the
// configuration the commit carries.
func (s *Service) onPush(ctx context.Context, conn db.GitConnection, ev PushEvent) ([]uuid.UUID, string, error) {
	s.runPushHook(ctx, ev)
	rows, err := s.q.ListServiceGitForRepo(ctx, db.ListServiceGitForRepoParams{ConnectionID: conn.ID, Lower: ev.RepoFullName})
	if err != nil {
		return nil, "", fmt.Errorf("find services of %s: %w", ev.RepoFullName, err)
	}
	head := shortSHA(ev.CommitSHA)
	if len(rows) == 0 {
		return nil, fmt.Sprintf("push %s to %s@%s: no service uses this repository", head, ev.RepoFullName, ev.Branch), nil
	}
	if tok := skipToken(ev.CommitMessage); tok != "" {
		return nil, fmt.Sprintf("push %s to %s@%s: skipped, the commit message contains %s", head, ev.RepoFullName, ev.Branch, tok), nil
	}
	var started []uuid.UUID
	var notes []string
	matched := 0
	for _, row := range rows {
		sg := row.ServiceGit
		name := s.serviceLabel(ctx, sg.ServiceID)
		cfg := services.DecodeConfig(row.ServiceConfig)
		if sc, err := services.LoadScopeInternal(ctx, s.q, sg.ServiceID); err == nil {
			if cfg, err = s.pushConfig(ctx, sc); err != nil {
				s.log.Error("load applied config", "service", sg.ServiceID.String(), "err", err)
				notes = append(notes, name+": not deployed (the applied configuration could not be read)")
				continue
			}
		}
		if cfg.GitBranch != ev.Branch {
			continue
		}
		matched++
		switch {
		case !sg.AutoDeploy:
			notes = append(notes, name+": auto-deploy is off")
			continue
		case len(sg.WatchPaths) > 0 && ev.PathsComplete && !MatchWatchPaths(sg.WatchPaths, ev.ChangedPaths):
			notes = append(notes, name+": no changed file matches the watch paths")
			continue
		}
		commit := &deployments.CommitInfo{SHA: ev.CommitSHA, Message: ev.CommitMessage, Author: ev.Author, Branch: ev.Branch}
		id, note := s.deployPush(ctx, sg.ServiceID, commit, name)
		if id != uuid.Nil {
			started = append(started, id)
		}
		notes = append(notes, note)
	}
	if matched == 0 {
		return nil, fmt.Sprintf("push %s to %s@%s: no service builds this branch", head, ev.RepoFullName, ev.Branch), nil
	}
	return started, fmt.Sprintf("push %s to %s@%s: %s", head, ev.RepoFullName, ev.Branch, strings.Join(notes, ", ")), nil
}

// pushConfig is the configuration a push is matched against and deployed
// with: the live one, except for a production service with staged changes,
// which a push deploys as it runs now (the staged branch, repository and
// settings are not applied by a push).
func (s *Service) pushConfig(ctx context.Context, sc services.Scope) (services.Config, error) {
	if !sc.ProductionHasStaged() {
		return sc.Config(), nil
	}
	applied, _, ok, err := services.AppliedScope(ctx, s.q, sc)
	if err != nil {
		return services.Config{}, err
	}
	if !ok {
		return sc.Config(), nil
	}
	return applied.Config(), nil
}

func (s *Service) serviceLabel(ctx context.Context, id uuid.UUID) string {
	if sc, err := services.LoadScopeInternal(ctx, s.q, id); err == nil {
		return sc.Service.Slug
	}
	return id.String()[:8]
}

// deployPush starts a git_push deployment. It returns the deployment id (nil
// when none was started) and a note for the delivery log.
func (s *Service) deployPush(ctx context.Context, serviceID uuid.UUID, commit *deployments.CommitInfo, label string) (uuid.UUID, string) {
	if s.dep == nil {
		return uuid.Nil, label + ": deployments are unavailable"
	}
	v, err := s.dep.DeployInternal(ctx, serviceID, deployments.TriggerGitPush, commit, "")
	if err == nil {
		s.enqueueStatus(ctx, v.ID)
		return v.ID, "deploying " + label
	}
	var ae *apierr.Error
	if errors.As(err, &ae) {
		if ae.Code == "deployment_in_progress" {
			raw, _ := json.Marshal(pendingCommit{SHA: commit.SHA, Message: commit.Message, Author: commit.Author, Branch: commit.Branch})
			if qerr := s.q.SetServiceGitPending(ctx, db.SetServiceGitPendingParams{ServiceID: serviceID, PendingCommit: raw}); qerr != nil {
				s.log.Error("queue push", "service", serviceID.String(), "err", qerr)
				return uuid.Nil, label + ": deployment in progress, the push could not be queued"
			}
			return uuid.Nil, label + ": deployment in progress, queued to deploy next"
		}
		return uuid.Nil, label + ": not deployed (" + ae.Message + ")"
	}
	s.log.Error("deploy push", "service", serviceID.String(), "err", err)
	return uuid.Nil, label + ": deployment could not be started"
}

// deployQueued deploys the queued push of every service whose deployments
// finished. It returns how many deployments it started.
func (s *Service) deployQueued(ctx context.Context) (int, error) {
	ids, err := s.q.ListServiceGitIdleWithPending(ctx)
	if err != nil {
		return 0, fmt.Errorf("list queued pushes: %w", err)
	}
	started := 0
	for _, id := range ids {
		raw, err := s.q.ClaimServiceGitPending(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return started, fmt.Errorf("claim queued push: %w", err)
		}
		var pc pendingCommit
		if err := json.Unmarshal(raw, &pc); err != nil || pc.SHA == "" {
			continue
		}
		sg, err := s.q.GetServiceGit(ctx, id)
		if err != nil {
			continue
		}
		sc, err := services.LoadScopeInternal(ctx, s.q, id)
		if err != nil || !sg.AutoDeploy {
			continue // unlinked or auto-deploy turned off meanwhile
		}
		if cfg, err := s.pushConfig(ctx, sc); err != nil || cfg.GitBranch != pc.Branch {
			continue // the branch changed meanwhile
		}
		depID, note := s.deployPush(ctx, id, &deployments.CommitInfo{SHA: pc.SHA, Message: pc.Message, Author: pc.Author, Branch: pc.Branch}, sc.Service.Slug)
		s.log.Info("queued push", "service", sc.Service.Slug, "commit", shortSHA(pc.SHA), "note", note)
		if depID != uuid.Nil {
			started++
		}
	}
	return started, nil
}
