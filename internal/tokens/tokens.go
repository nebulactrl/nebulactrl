// Package tokens manages API tokens (Authorization: Bearer nct_…).
package tokens

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// Token is the API view of an API token. The secret is never part of it.
type Token struct {
	ID         uuid.UUID   `json:"id"`
	Name       string      `json:"name"`
	Prefix     string      `json:"prefix" doc:"First 8 characters after nct_, for identification"`
	Scopes     []string    `json:"scopes"`
	ProjectIDs []uuid.UUID `json:"projectIds,omitempty" doc:"Absent when the token can access all projects"`
	ExpiresAt  *time.Time  `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time  `json:"lastUsedAt,omitempty"`
	RevokedAt  *time.Time  `json:"revokedAt,omitempty"`
	CreatedAt  time.Time   `json:"createdAt"`
	// Kind: mcp tokens work only on /mcp, rest tokens only on /api/v1.
	Kind string `json:"kind" enum:"mcp,rest" doc:"mcp: works only on /mcp (agents). rest: works only on /api/v1 (REST API, CLI, CI)."`
	// AllowProductionWithoutApproval: calls with this token on production need no human approval.
	AllowProductionWithoutApproval bool `json:"allowProductionWithoutApproval" doc:"Calls with this token skip the production approval. Every token needs one unless an instance admin or team admin switched it off at creation."`
}

// CreateInput describes a new token.
type CreateInput struct {
	Name       string
	Kind       string // auth.TokenKindMCP or auth.TokenKindREST; empty means REST
	Scopes     []string
	ProjectIDs []uuid.UUID // nil = all projects
	ExpiresAt  *time.Time
	// AllowProductionWithoutApproval exempts the token from the production
	// guard. Only the instance admin or a team Owner/Admin signed in with a
	// session may set it; a token never can.
	AllowProductionWithoutApproval bool
}

// Service manages tokens: the admin scope manages the whole team's, any member
// with a session manages their own.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
}

// NewService returns a Service (pool may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus) *Service {
	return &Service{pool: pool, q: db.New(pool), bus: bus}
}

func toToken(t db.ApiToken) Token {
	scopes := t.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return Token{
		ID: t.ID, Name: t.Name, Prefix: t.Prefix, Scopes: scopes, ProjectIDs: t.ProjectIds,
		ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt, RevokedAt: t.RevokedAt, CreatedAt: t.CreatedAt,
		Kind: t.Kind, AllowProductionWithoutApproval: t.AllowProductionWithoutApproval,
	}
}

// manage authorizes token management and says whether the caller sees every
// token of the team. Callers holding the admin scope manage all tokens; any
// member signed in with a session manages only their own (a developer's tokens
// are theirs alone). Tokens that lack admin cannot manage tokens.
func manage(ctx context.Context) (p *auth.Principal, all bool, err error) {
	p, err = policy.Authenticated(ctx)
	if err != nil {
		return nil, false, err
	}
	if policy.HasScope(p, policy.ScopeAdmin) {
		return p, true, nil
	}
	if p.Type == auth.TypeUser && p.WorkspaceID != uuid.Nil && auth.RoleRank(p.Role) > 0 {
		return p, false, nil
	}
	return nil, false, apierr.Forbidden()
}

// List returns the team's tokens (every token for admins, otherwise the
// caller's own), newest first.
func (s *Service) List(ctx context.Context) ([]Token, error) {
	p, all, err := manage(ctx)
	if err != nil {
		return nil, err
	}
	arg := db.ListAPITokensParams{WorkspaceID: p.WorkspaceID}
	if !all {
		arg.UserID = &p.UserID
	}
	rows, err := s.q.ListAPITokens(ctx, arg)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	out := make([]Token, 0, len(rows))
	for _, r := range rows {
		out = append(out, toToken(r))
	}
	return out, nil
}

// Create validates and stores a token, returning it with the raw secret, which
// is shown exactly once.
func (s *Service) Create(ctx context.Context, in CreateInput) (Token, string, error) {
	p, _, err := manage(ctx)
	if err != nil {
		return Token{}, "", err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		return Token{}, "", apierr.Unprocessable("validation_failed", "name must be 1-100 characters")
	}
	if len(in.Scopes) == 0 {
		return Token{}, "", apierr.Unprocessable("invalid_scope", "at least one scope is required")
	}
	scopes := slices.Clone(in.Scopes)
	slices.Sort(scopes)
	scopes = slices.Compact(scopes)
	for _, sc := range scopes {
		if !policy.ValidScope(sc) {
			return Token{}, "", apierr.Unprocessable("invalid_scope", fmt.Sprintf("unknown scope %q", sc))
		}
	}
	// A token never exceeds its creator: scopes are intersected with the
	// creator's own (their role's) scopes, and re-checked on every use.
	scopes = auth.EffectiveScopes(scopes, p.Scopes)
	if len(scopes) == 0 {
		return Token{}, "", apierr.Forbidden()
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now()) {
		return Token{}, "", apierr.Unprocessable("invalid_expiry", "expiresAt must be in the future")
	}
	if in.Kind == "" {
		in.Kind = auth.TokenKindREST
	}
	if !auth.ValidTokenKind(in.Kind) {
		return Token{}, "", apierr.Unprocessable("invalid_kind", `kind must be "mcp" or "rest"`)
	}
	// A token never gets broader rights than the token that made it, in time
	// as in scope: it cannot outlive its creator.
	if p.Type != auth.TypeUser && p.TokenExpiresAt != nil && (in.ExpiresAt == nil || in.ExpiresAt.After(*p.TokenExpiresAt)) {
		return Token{}, "", apierr.Unprocessable("invalid_expiry", "a token cannot create a token that outlives it: set expiresAt no later than this token's own expiry")
	}
	// Switching the production guard off is for the instance admin and team
	// Owners/Admins signed in with a session. A token never mints a token that
	// skips approvals, whatever its own rights.
	if in.AllowProductionWithoutApproval {
		if p.Type != auth.TypeUser {
			return Token{}, "", apierr.New(http.StatusForbidden, "forbidden", "a token cannot create a token that skips production approvals: sign in and create it from the dashboard")
		}
		if auth.RoleRank(p.Role) < auth.RoleRank(auth.RoleAdmin) {
			first, ferr := s.q.FirstUserID(ctx)
			if ferr != nil && !errors.Is(ferr, pgx.ErrNoRows) {
				return Token{}, "", fmt.Errorf("check instance admin: %w", ferr)
			}
			if ferr != nil || first != p.UserID {
				return Token{}, "", apierr.New(http.StatusForbidden, "forbidden", "only the instance admin or a team Owner or Admin can let a token skip production approvals")
			}
		}
	}
	projectIDs := in.ProjectIDs
	if p.Restricted() && projectIDs == nil {
		return Token{}, "", apierr.Forbidden()
	}
	for _, id := range projectIDs {
		if !p.CanSeeProject(id) {
			return Token{}, "", apierr.NotFound("")
		}
		if _, err := s.q.GetProject(ctx, db.GetProjectParams{ID: id, WorkspaceID: p.WorkspaceID}); errors.Is(err, pgx.ErrNoRows) {
			return Token{}, "", apierr.Unprocessable("unknown_project", fmt.Sprintf("project %s does not exist", id))
		} else if err != nil {
			return Token{}, "", fmt.Errorf("check project: %w", err)
		}
	}

	raw, hash, prefix, err := auth.GenerateToken()
	if err != nil {
		return Token{}, "", fmt.Errorf("generate token: %w", err)
	}
	var row db.ApiToken
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).CreateAPIToken(ctx, db.CreateAPITokenParams{
			WorkspaceID: p.WorkspaceID, UserID: p.UserID, Name: in.Name, TokenHash: hash, Prefix: prefix,
			Scopes: scopes, ProjectIds: projectIDs, ExpiresAt: in.ExpiresAt,
			AllowProductionWithoutApproval: in.AllowProductionWithoutApproval, Kind: in.Kind,
		})
		if err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{
			Action: "token.create", ResourceType: "api_token", ResourceID: row.ID.String(),
			Metadata: map[string]any{
				"name": in.Name, "scopes": scopes, "prefix": prefix, "kind": in.Kind,
				"allowProductionWithoutApproval": in.AllowProductionWithoutApproval,
				"createdByType":                  p.Type,
			},
		}); err != nil {
			return err
		}
		if !in.AllowProductionWithoutApproval {
			return nil
		}
		// Switching the production guard off for a token is its own audit event.
		return audit.Record(ctx, tx, audit.Entry{
			Action: "token.production_guard.disabled", ResourceType: "api_token", ResourceID: row.ID.String(),
			Metadata: map[string]any{"name": in.Name, "prefix": prefix, "scopes": scopes},
		})
	})
	if err != nil {
		return Token{}, "", fmt.Errorf("create token: %w", err)
	}
	s.publish(events.TypeTokenCreated, p.WorkspaceID, row.ID)
	return toToken(row), raw, nil
}

// Revoke marks a token revoked. Revoking twice is a no-op.
func (s *Service) Revoke(ctx context.Context, id uuid.UUID) error {
	p, all, err := manage(ctx)
	if err != nil {
		return err
	}
	arg := db.RevokeAPITokenParams{ID: id, WorkspaceID: p.WorkspaceID}
	if !all {
		arg.UserID = &p.UserID
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := db.New(tx).RevokeAPIToken(ctx, arg)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("")
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "token.revoke", ResourceType: "api_token", ResourceID: id.String(),
			Metadata: map[string]any{"name": t.Name, "prefix": t.Prefix},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("revoke token: %w", err)
	}
	s.publish(events.TypeTokenRevoked, p.WorkspaceID, id)
	return nil
}

func (s *Service) publish(typ string, workspaceID, id uuid.UUID) {
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: typ, ResourceID: id.String(), WorkspaceID: workspaceID.String()})
	}
}
