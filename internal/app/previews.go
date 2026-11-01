package app

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/previews"
	"github.com/nebulactrl/nebulactrl/internal/tasks"
)

// The previews domain defines the narrow interfaces it needs; these adapters
// bridge them to the git providers, the deployments domain and the tasks.

type previewGit struct{ s *gitproviders.Service }

func (g previewGit) ServicesForRepo(ctx context.Context, connectionID uuid.UUID, repo string) ([]uuid.UUID, error) {
	return g.s.ServicesForRepo(ctx, connectionID, repo)
}

func (g previewGit) CopyServiceGitTx(ctx context.Context, tx pgx.Tx, from, to uuid.UUID) error {
	return g.s.CopyServiceGitTx(ctx, tx, from, to)
}

func (g previewGit) SetCommitStatus(ctx context.Context, connectionID uuid.UUID, repo, sha string, st previews.CommitStatus) error {
	return g.s.SetCommitStatus(ctx, connectionID, repo, sha, gitproviders.CommitStatus{
		State: st.State, TargetURL: st.TargetURL, Description: st.Description, Context: st.Context,
	})
}

func (g previewGit) UpsertPRComment(ctx context.Context, connectionID uuid.UUID, repo string, number int, marker, body string) error {
	return g.s.UpsertPRComment(ctx, connectionID, repo, number, marker, body)
}

// subscribe delivers verified pull-request events to the previews domain.
func (g previewGit) subscribe(p *previews.Service) {
	g.s.SubscribePullRequest(func(ctx context.Context, ev gitproviders.PullRequestEvent) {
		p.HandlePullRequest(ctx, previews.PullRequest{
			ConnectionID: ev.ConnectionID, Provider: string(ev.Provider), Repo: ev.RepoFullName, Action: ev.Action, Number: ev.Number,
			Title: ev.Title, Author: ev.Author, HeadBranch: ev.HeadBranch, HeadSHA: ev.HeadSHA, BaseBranch: ev.BaseBranch,
			HeadCloneURL: ev.HeadCloneURL, HTMLURL: ev.HTMLURL, FromFork: ev.FromFork,
		})
	})
}

type previewDeployer struct{ d *deployments.Service }

func (p previewDeployer) Deploy(ctx context.Context, serviceID uuid.UUID, c previews.Commit, message string) (uuid.UUID, error) {
	var commit *deployments.CommitInfo
	if c.SHA != "" {
		commit = &deployments.CommitInfo{SHA: c.SHA, Message: c.Message, Author: c.Author, Branch: c.Branch}
	}
	v, err := p.d.DeployInternal(ctx, serviceID, deployments.TriggerPreview, commit, message)
	return v.ID, err
}

func (p previewDeployer) Cancel(ctx context.Context, deploymentID uuid.UUID) error {
	_, err := p.d.Cancel(ctx, deploymentID)
	return err
}

type previewSeeder struct{ t *tasks.Service }

func (p previewSeeder) RunSeed(ctx context.Context, tx pgx.Tx, serviceID uuid.UUID, command []string) (uuid.UUID, error) {
	return p.t.RunOneOff(ctx, tx, serviceID, command, tasks.RunOpts{Source: "preview_seed", ActorLabel: "Previews"})
}

func (p previewSeeder) RunStatus(ctx context.Context, runID uuid.UUID) (string, *int, string, error) {
	return p.t.RunStatus(ctx, runID)
}
