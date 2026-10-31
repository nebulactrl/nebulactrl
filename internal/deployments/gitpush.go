package deployments

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// CommitInfo is the git commit a deployment was started for.
type CommitInfo struct {
	SHA     string
	Message string
	Author  string
	Branch  string
}

// GitAuthSource mints the clone credentials of a service that is linked to a
// git connection (a GitHub App installation token, an OAuth token, ...). The
// credentials are short-lived and only ever attached to agent commands at send
// time; they are never stored. They are minted only for gitURL being the
// linked repository: any other URL is refused (409 git_link_mismatch).
type GitAuthSource interface {
	CloneAuth(ctx context.Context, serviceID uuid.UUID, gitURL string) (agentprotocol.GitAuth, error)
}

// SetGitAuth wires the source of clone credentials for services linked to a
// git connection. Without one such services clone with their own git token.
func (s *Service) SetGitAuth(src GitAuthSource) { s.gitAuth = src }

// InternalOptions refine an internal deployment.
type InternalOptions struct {
	// Commit pins a git service's build to one commit (see DeployInternal).
	Commit *CommitInfo
	// ImageRef deploys this image reference (repo:tag or repo@sha256:…) instead
	// of the service's configured image. It is a one-off override of one
	// release: the reference is recorded in the release source and the
	// configuration (and so the generation) stays as it is. Image services
	// only; the caller must have checked that the reference belongs to the
	// service's repository.
	ImageRef string
	Message  string
}

// DeployInternal deploys the service's current configuration on behalf of the
// system (a git push, a pull request, a cron job): it performs no
// authorization, so callers must have verified the event themselves. trigger is
// one of the Trigger* constants. A non-empty commit.SHA pins a git service's
// build to that full 40-character commit instead of the branch head, and the
// commit is shown on the deployment. A service that already has an active
// deployment gets 409 deployment_in_progress.
func (s *Service) DeployInternal(ctx context.Context, serviceID uuid.UUID, trigger string, commit *CommitInfo, message string) (DeploymentView, error) {
	return s.DeployWith(ctx, serviceID, trigger, InternalOptions{Commit: commit, Message: message})
}

// DeployWith is DeployInternal with options.
func (s *Service) DeployWith(ctx context.Context, serviceID uuid.UUID, trigger string, opts InternalOptions) (DeploymentView, error) {
	commit := opts.Commit
	message, err := cleanMessage(opts.Message)
	if err != nil {
		return DeploymentView{}, err
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, serviceID)
	if err != nil {
		return DeploymentView{}, err
	}
	if opts.ImageRef != "" && sc.Service.SourceType != services.SourceImage {
		return DeploymentView{}, apierr.Unprocessable("validation_failed", "an image reference applies to image services only")
	}
	sha := ""
	if commit != nil && commit.SHA != "" {
		if sc.Service.SourceType != services.SourceGit {
			return DeploymentView{}, apierr.Unprocessable("validation_failed", "a commit applies to git services only")
		}
		sha = strings.ToLower(commit.SHA)
		if !fullSHA.MatchString(sha) {
			return DeploymentView{}, apierr.Unprocessable("validation_failed", "commit sha must be a full 40-character hexadecimal commit SHA")
		}
		c := *commit
		c.SHA = sha
		commit = &c
	} else {
		commit = nil
	}
	// A push of a production service with staged changes ships the new commit
	// with the configuration of the running release; the staged changes stay
	// staged (applied generation unchanged), for a person to deploy.
	gatherScope, generation := sc, sc.Service.Generation
	var baseline *db.Release
	if trigger == TriggerGitPush && sc.ProductionHasStaged() {
		applied, rel, ok, err := services.AppliedScope(ctx, s.q, sc)
		if err != nil {
			return DeploymentView{}, err
		}
		if ok {
			gatherScope, generation, baseline = applied, sc.Service.AppliedGeneration, &rel
		}
	}
	in, err := s.gather(ctx, s.q, gatherScope)
	if err != nil {
		return DeploymentView{}, err
	}
	if baseline != nil {
		if err := s.useAppliedRelease(ctx, s.q, in, *baseline); err != nil {
			return DeploymentView{}, err
		}
	}
	if err := blockingError(in.blocking()); err != nil {
		return DeploymentView{}, err
	}
	strategy, _ := ChoosePlanStrategy(in.cfg, len(in.volumes))
	meta := map[string]any{}
	if commit != nil {
		meta["commit"] = commit.SHA
		meta["branch"] = commit.Branch
	}
	if opts.ImageRef != "" {
		meta["image"] = opts.ImageRef
	}
	dep, err := s.create(ctx, createParams{
		sc: sc, trigger: trigger, serverID: in.server.ID, strategy: strategy, generation: generation,
		auditAction: "deployment.create", auditMeta: meta, commitSHA: sha, message: message, commit: commit, imageRef: opts.ImageRef,
	})
	if err != nil {
		return DeploymentView{}, err
	}
	return s.oneView(ctx, dep)
}
