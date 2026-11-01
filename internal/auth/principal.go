// Package auth authenticates requests (browser sessions and API tokens) and
// carries the resulting Principal through the request context.
package auth

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Principal types.
const (
	TypeUser  = "user"
	TypeToken = "token"
	TypeMCP   = "mcp"
	// TypeSystem is the control plane acting on its own behalf (webhook-driven
	// previews and auto-deploys, schedulers). It never comes from a request.
	TypeSystem = "system"
)

// Team roles, from most to least privileged. A role is per membership: the same
// user can be an owner of one team and a viewer of another.
const (
	RoleOwner     = "owner"
	RoleAdmin     = "admin"
	RoleDeveloper = "developer"
	RoleViewer    = "viewer"
)

// ValidRole reports whether r is a known role.
func ValidRole(r string) bool { return RoleRank(r) > 0 }

// RoleRank orders roles: owner 4, admin 3, developer 2, viewer 1, anything
// else (including the system principal's empty role) 0.
func RoleRank(r string) int {
	switch r {
	case RoleOwner:
		return 4
	case RoleAdmin:
		return 3
	case RoleDeveloper:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// System returns a principal for the control plane itself, limited to one
// project and the given scopes. Audit rows and deployments it creates are
// attributed to "system" with label.
func System(workspaceID, projectID uuid.UUID, label string, scopes ...string) *Principal {
	return &Principal{Type: TypeSystem, WorkspaceID: workspaceID, Label: label, Scopes: scopes, ProjectIDs: []uuid.UUID{projectID}}
}

// Token kinds. An MCP token works only on /mcp, a REST token (REST API, CLI,
// CI) only on /api/v1; each is refused on the other.
const (
	TokenKindMCP  = "mcp"
	TokenKindREST = "rest"
)

// ValidTokenKind reports whether k is a known token kind.
func ValidTokenKind(k string) bool { return k == TokenKindMCP || k == TokenKindREST }

// Principal is the authenticated caller of a request.
type Principal struct {
	Type        string // "user" | "token" | "mcp"
	UserID      uuid.UUID
	TokenID     *uuid.UUID
	WorkspaceID uuid.UUID // the current team
	// Role is the user's role in the current team (the creator's, for tokens);
	// empty for the system principal and for users without a team.
	Role       string
	Label      string
	Scopes     []string
	ProjectIDs []uuid.UUID // nil = all projects
	// TokenKind is the kind of the token behind a token or MCP principal
	// (TokenKindMCP or TokenKindREST); empty for sessions and the system.
	TokenKind string
	// SessionMFA is set on a browser-session principal whose session proved a
	// second factor (a TOTP or recovery code, a passkey, or a provider trusted
	// to assert MFA). A team's 2FA requirement counts it as enrolled.
	SessionMFA bool
	// TokenExpiresAt is when the token behind this principal expires, nil when
	// it never does. A token cannot mint a token that outlives it.
	TokenExpiresAt *time.Time
	// AllowProductionWithoutApproval is set on tokens an instance admin or team
	// admin exempted from the production guard at creation.
	// RequireProductionApproval is the opposite on every other token: the
	// authentication sets it on each token that holds no exemption. See
	// internal/prodguard.
	AllowProductionWithoutApproval bool
	RequireProductionApproval      bool
}

// ID is the stable identifier of the credential: the token ID for tokens,
// otherwise the user ID. It keys idempotency records and audit actors.
func (p *Principal) ID() uuid.UUID {
	if p.TokenID != nil {
		return *p.TokenID
	}
	return p.UserID
}

// Restricted reports whether the principal is limited to a project subset.
func (p *Principal) Restricted() bool { return p.ProjectIDs != nil }

// CanSeeProject reports whether the principal may access the project.
func (p *Principal) CanSeeProject(id uuid.UUID) bool {
	if p.ProjectIDs == nil {
		return true
	}
	for _, pid := range p.ProjectIDs {
		if pid == id {
			return true
		}
	}
	return false
}

type principalKey struct{}

// WithPrincipal returns ctx carrying p.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal stored in ctx.
func PrincipalFrom(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(*Principal)
	return p, ok && p != nil
}
