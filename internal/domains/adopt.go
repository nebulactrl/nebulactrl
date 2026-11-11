package domains

import (
	"context"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// CheckNewDomain runs the checks a new domain passes on create that do not need
// its service: the host name's shape, the dashboard's and status pages' own
// names, names another team claims (including Cloudflare zones), the rule
// fields, DNS-01 and the permissions proxy fields and wildcards need. It
// serves the review of a server adoption, where the service does not exist yet;
// create itself runs the same checks again when the domain is written.
func (s *Service) CheckNewDomain(ctx context.Context, workspaceID uuid.UUID, srv *db.Server, hostname string, in RuleInput) error {
	host, err := ValidateDomainHost(hostname)
	if err != nil {
		return err
	}
	if in.Any() || agentprotocol.IsWildcardHost(host) {
		if err := policy.Require(ctx, policy.ScopeProxyWrite, nil); err != nil {
			return err
		}
		if err := RequireProxyV2(srv); err != nil {
			return err
		}
	}
	sp, err := defaultSpec(host).apply(in)
	if err != nil {
		return err
	}
	return s.validate(ctx, s.q, services.Scope{WorkspaceID: workspaceID}, srv, sp, uuid.Nil)
}
