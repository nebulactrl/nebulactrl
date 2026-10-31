package deployments

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// CodeStagedChangesPending is the 409 a git push of a production service gets
// when the service has staged changes a push cannot leave out.
const CodeStagedChangesPending = "staged_changes_pending"

// useAppliedRelease turns inputs gathered with services.AppliedScope (the
// configuration of the running release) into the state of that release: the
// variables come from the release's own environment snapshot, not from the live
// rows. A git push of a production service deploys the new commit with this
// state, so the service's staged changes (a repository or branch switch, a
// setting, a variable) are never shipped by a push and stay staged for a person
// to deploy.
//
// Staged domains, volumes, networks, a staged server move or source switch
// cannot be left out of a deployment, so a service that has any is refused
// (409 staged_changes_pending): deploy or discard them first.
func (s *Service) useAppliedRelease(ctx context.Context, q *db.Queries, in *inputs, rel db.Release) error {
	if len(rel.EnvEnc) == 0 {
		return apierr.Conflict(CodeStagedChangesPending,
			"The service has staged changes and the variables of its running release are no longer stored, so a push cannot deploy it without them. Deploy or discard the staged changes first.")
	}
	plain, err := s.Box.Open(secretbox.ReleasesEnv, rel.ID.String(), rel.EnvEnc)
	if err != nil {
		return fmt.Errorf("decrypt release env: %w", err)
	}
	var env services.EnvSnapshot
	err = json.Unmarshal(plain, &env)
	secretbox.Wipe(plain)
	if err != nil {
		return fmt.Errorf("decode release env: %w", err)
	}
	secret := make(map[string]bool, len(env.Secret))
	for _, k := range env.Secret {
		secret[k] = true
	}
	in.raw = env.Raw
	if in.raw == nil {
		in.raw = map[string]services.RawVar{}
	}
	in.resolved = services.Resolved{Vars: env.Vars, Secret: secret, Scope: env.Scope, Ext: env.Ext, Leaves: env.Leaves}
	in.resolveErr = nil
	in.emptySuggested = nil
	in.egressHints = nil

	left, err := s.diffAgainstRelease(ctx, q, in, rel)
	if err != nil {
		return err
	}
	if len(left) > 0 {
		fields := make([]string, 0, len(left))
		for _, c := range left {
			if len(fields) == 5 {
				fields = append(fields, "…")
				break
			}
			fields = append(fields, c.Field)
		}
		return apierr.Conflict(CodeStagedChangesPending,
			"The service has staged changes a push cannot leave out ("+strings.Join(fields, ", ")+"). Deploy or discard them first.")
	}
	return nil
}
