package deployments

import (
	"errors"
	"sort"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// prunedReleaseEnv builds the environment of a rollback to a release whose own
// was pruned (internal/releases): the service's current variables, with the
// release-scoped platform variables (git commit, image) of the release itself.
// The same shape a new release freezes at deploy time. It fails, and so does
// the rollback, when the current variables do not resolve: starting the old
// image with an empty environment would be worse than refusing.
func prunedReleaseEnv(in *inputs, cfg services.Config, source services.ReleaseSource, rel db.Release) (services.EnvSnapshot, error) {
	if in.resolveErr != nil {
		msg := in.resolveErr.Error()
		var ae *apierr.Error
		if errors.As(in.resolveErr, &ae) {
			msg = ae.Message
		}
		return services.EnvSnapshot{}, userErrorf("The variable values of this release were removed after the rollback window, "+
			"and the service's current variables cannot be resolved (%s). Fix the variables first.", msg)
	}
	secrets := make([]string, 0, len(in.resolved.Secret))
	for k := range in.resolved.Secret {
		secrets = append(secrets, k)
	}
	sort.Strings(secrets)
	vars := make(map[string]string, len(in.resolved.Vars)+3)
	for k, v := range in.resolved.Vars {
		vars[k] = v
	}
	vars[services.VarGitCommitSHA], vars[services.VarGitBranch], vars[services.VarImage] = "", "", cfg.Image
	if rel.ImageRef != "" {
		vars[services.VarImage] = rel.ImageRef
	}
	if source.Type == services.SourceGit {
		vars[services.VarGitCommitSHA], vars[services.VarGitBranch] = source.CommitSHA, source.Branch
	}
	return services.EnvSnapshot{Vars: vars, Secret: secrets, Raw: in.raw, Scope: in.resolved.Scope, Leaves: in.resolved.Leaves, Ext: in.resolved.Ext}, nil
}
