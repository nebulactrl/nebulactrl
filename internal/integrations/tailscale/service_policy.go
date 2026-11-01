package tailscale

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// ErrPolicyUnsupported is returned by PolicyAPI when the credential cannot
// manage a policy: a Headscale pre-auth key has no API access.
var ErrPolicyUnsupported = errors.New("this credential cannot read or write the tailnet policy")

// PolicyAPI is the policy surface of a workspace's integration, the same for
// Headscale (policy.mode: database) and Tailscale (policy file API).
type PolicyAPI interface {
	// Provider is "headscale" or "tailscale".
	Provider() string
	// Get reads the stored policy. A Headscale without a stored policy answers
	// ErrPolicyNotFound.
	Get(ctx context.Context) (Policy, error)
	// Check validates a document without storing it.
	Check(ctx context.Context, doc []byte) error
	// Set stores a document. version is the Version of the Get it was derived
	// from (Tailscale uses it as If-Match; Headscale ignores it).
	Set(ctx context.Context, doc []byte, version string) (Policy, error)
}

type headscalePolicyAPI struct{ c *HeadscaleClient }

func (headscalePolicyAPI) Provider() string                          { return "headscale" }
func (a headscalePolicyAPI) Get(ctx context.Context) (Policy, error) { return a.c.GetPolicy(ctx) }
func (a headscalePolicyAPI) Check(ctx context.Context, doc []byte) error {
	return a.c.CheckPolicy(ctx, string(doc))
}
func (a headscalePolicyAPI) Set(ctx context.Context, doc []byte, _ string) (Policy, error) {
	return a.c.SetPolicy(ctx, string(doc))
}

type tailscalePolicyAPI struct{ c *Client }

func (tailscalePolicyAPI) Provider() string                          { return "tailscale" }
func (a tailscalePolicyAPI) Get(ctx context.Context) (Policy, error) { return a.c.GetPolicy(ctx) }
func (a tailscalePolicyAPI) Check(ctx context.Context, doc []byte) error {
	return a.c.ValidatePolicy(ctx, doc)
}
func (a tailscalePolicyAPI) Set(ctx context.Context, doc []byte, version string) (Policy, error) {
	return a.c.SetPolicy(ctx, doc, version)
}

// PolicyAPI returns the policy client of ws's integration (ErrNotConfigured,
// ErrPolicyUnsupported). System-level: no scope check; callers authorize.
func (s *Service) PolicyAPI(ctx context.Context, ws uuid.UUID) (PolicyAPI, error) {
	r, err := s.q.GetTailnetIntegration(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotConfigured
	}
	if err != nil {
		return nil, fmt.Errorf("get tailnet integration: %w", err)
	}
	if r.Provider == ProviderHeadscale {
		if r.HeadscaleCredential != HeadscaleAPIKey {
			return nil, ErrPolicyUnsupported
		}
		hc, _, err := s.headscaleClient(r)
		if err != nil {
			return nil, err
		}
		return headscalePolicyAPI{hc}, nil
	}
	_, c, err := s.load(ctx, ws)
	if err != nil {
		return nil, err
	}
	return tailscalePolicyAPI{c}, nil
}

// CoordinatorHealth probes the coordinator of ws's integration: GET /health of
// a Headscale, a token exchange for Tailscale. A nil error means reachable.
func (s *Service) CoordinatorHealth(ctx context.Context, ws uuid.UUID) error {
	r, err := s.q.GetTailnetIntegration(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotConfigured
	}
	if err != nil {
		return fmt.Errorf("get tailnet integration: %w", err)
	}
	if r.Provider == ProviderHeadscale {
		return HealthCheck(ctx, s.hc, r.LoginServer)
	}
	_, c, err := s.load(ctx, ws)
	if err != nil {
		return err
	}
	return c.CheckToken(ctx)
}

// SharedTailnet reports whether another workspace's integration points at the
// same tailnet (same provider and tailnet or login server). Managed ACL pushes
// are refused then: one team would rewrite the policy of another.
func (s *Service) SharedTailnet(ctx context.Context, ws uuid.UUID) (bool, error) {
	r, err := s.q.GetTailnetIntegration(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotConfigured
	}
	if err != nil {
		return false, fmt.Errorf("get tailnet integration: %w", err)
	}
	n, err := s.q.CountOtherWorkspacesOnTailnet(ctx, db.CountOtherWorkspacesOnTailnetParams{
		WorkspaceID: ws, Provider: r.Provider, Tailnet: r.Tailnet, LoginServer: r.LoginServer,
	})
	if err != nil {
		return false, fmt.Errorf("count integrations on the tailnet: %w", err)
	}
	return n > 0, nil
}
