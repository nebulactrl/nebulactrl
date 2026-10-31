// Package policy performs authorization. Domain services call Require at the
// top of every method; handlers never do.
package policy

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
)

// Scopes understood by the API.
const (
	ScopeProjectsRead = "projects:read"
	// ScopeLogsRead gates reading application and build log text (runtime,
	// deployment and task-run logs, log history). Log lines are untrusted
	// application output and may hold sensitive data, so it is separate from
	// projects:read.
	ScopeLogsRead            = "logs:read"
	ScopeProjectsWrite       = "projects:write"
	ScopeDeploymentsWrite    = "deployments:write"
	ScopeDeploymentsRollback = "deployments:rollback"
	ScopeServersRead         = "servers:read"
	ScopeServersWrite        = "servers:write"
	// ScopeProxyWrite guards everything that publishes or exposes traffic through
	// the proxy: route services, TCP/UDP ports, custom certificates, the upstream
	// policy and tailnet egress. Tokens must hold it explicitly.
	ScopeProxyWrite = "proxy:write"
	ScopeAdmin      = "admin"
)

// AllScopes lists every valid scope. Browser sessions hold all of them.
var AllScopes = []string{
	ScopeProjectsRead,
	ScopeLogsRead,
	ScopeProjectsWrite,
	ScopeDeploymentsWrite,
	ScopeDeploymentsRollback,
	ScopeServersRead,
	ScopeServersWrite,
	ScopeProxyWrite,
	ScopeAdmin,
}

// ValidScope reports whether s is a known scope.
func ValidScope(s string) bool { return slices.Contains(AllScopes, s) }

// ScopesForRole returns the scopes a team role grants to browser sessions and
// the ceiling of the API tokens its members create:
//
//	owner, admin: every scope (owner-only actions are checked by role, not scope)
//	developer:    read/write projects (variables, tasks, services), read logs,
//	              deploy, roll back, read servers; no servers:write, proxy:write
//	              or admin
//	viewer:       projects:read, logs:read and servers:read
//
// An unknown role has no scopes.
func ScopesForRole(role string) []string {
	switch role {
	case auth.RoleOwner, auth.RoleAdmin:
		return slices.Clone(AllScopes)
	case auth.RoleDeveloper:
		return []string{ScopeProjectsRead, ScopeLogsRead, ScopeProjectsWrite, ScopeDeploymentsWrite, ScopeDeploymentsRollback, ScopeServersRead}
	case auth.RoleViewer:
		return []string{ScopeProjectsRead, ScopeLogsRead, ScopeServersRead}
	}
	return []string{}
}

// RoleAtLeast reports whether p's team role is min or more privileged.
func RoleAtLeast(p *auth.Principal, min string) bool {
	return auth.RoleRank(p.Role) >= auth.RoleRank(min) && auth.RoleRank(min) > 0
}

// RequireRole authorizes the principal in ctx by role: 401 without a
// principal, 403 `forbidden` when its role is not one of roles, and 404
// `not_found` when projectID (non-nil) is outside its project restriction.
// Use it for actions that scopes cannot express (terminal, owner management).
func RequireRole(ctx context.Context, projectID *uuid.UUID, roles ...string) error {
	p, err := Authenticated(ctx)
	if err != nil {
		return err
	}
	if p.Role == "" || !slices.Contains(roles, p.Role) {
		return apierr.Forbidden()
	}
	if projectID != nil && !p.CanSeeProject(*projectID) {
		return apierr.NotFound("resource")
	}
	return nil
}

// Authenticated returns the principal or a 401.
func Authenticated(ctx context.Context) (*auth.Principal, error) {
	p, ok := auth.PrincipalFrom(ctx)
	if !ok {
		return nil, apierr.Unauthorized()
	}
	return p, nil
}

// HasScope reports whether p holds scope (admin implies every scope).
func HasScope(p *auth.Principal, scope string) bool {
	return slices.Contains(p.Scopes, scope) || slices.Contains(p.Scopes, ScopeAdmin)
}

// Require authorizes the principal in ctx for scope, and for projectID when
// non-nil. It returns 401 without a principal, 403 `forbidden` when the scope
// is missing, and 404 `not_found` when the project is outside the principal's
// project restriction.
func Require(ctx context.Context, scope string, projectID *uuid.UUID) error {
	p, err := Authenticated(ctx)
	if err != nil {
		return err
	}
	if !HasScope(p, scope) {
		return apierr.Forbidden()
	}
	if projectID != nil && !p.CanSeeProject(*projectID) {
		return apierr.NotFound("resource")
	}
	return nil
}

// WorkspaceID returns the caller's workspace, or uuid.Nil when unauthenticated.
func WorkspaceID(ctx context.Context) uuid.UUID {
	if p, ok := auth.PrincipalFrom(ctx); ok {
		return p.WorkspaceID
	}
	return uuid.Nil
}
