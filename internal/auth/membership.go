package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/alexedwards/argon2id"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// Membership is a user's place in one team.
type Membership struct {
	WorkspaceID uuid.UUID
	Role        string
	ProjectIDs  []uuid.UUID // nil = every project of the team
}

func membershipFrom(m db.Membership) *Membership {
	return &Membership{WorkspaceID: m.WorkspaceID, Role: m.Role, ProjectIDs: m.ProjectIds}
}

// MembershipIn returns the user's membership of a team, or nil when the user
// is not a member.
func (s *Service) MembershipIn(ctx context.Context, userID, teamID uuid.UUID) (*Membership, error) {
	m, err := s.q.GetMembership(ctx, db.GetMembershipParams{UserID: userID, WorkspaceID: teamID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get membership: %w", err)
	}
	return membershipFrom(m), nil
}

// membershipFor resolves the membership to act under: the requested team when
// the user is a member, else the user's oldest team, else nil.
func (s *Service) membershipFor(ctx context.Context, userID uuid.UUID, teamID *uuid.UUID) (*Membership, error) {
	if teamID != nil && *teamID != uuid.Nil {
		m, err := s.MembershipIn(ctx, userID, *teamID)
		if err != nil || m != nil {
			return m, err
		}
	}
	m, err := s.q.FirstMembership(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("first membership: %w", err)
	}
	return membershipFrom(m), nil
}

// EventWorkspace returns the team owning a project, environment, service,
// server or API token id; ok is false for unknown ids.
func (s *Service) EventWorkspace(ctx context.Context, id uuid.UUID) (uuid.UUID, bool) {
	ws, err := s.q.EventWorkspace(ctx, id)
	if err != nil {
		return uuid.Nil, false
	}
	return ws, true
}

// HashPassword validates and hashes a new password (used when an account is
// created through an invite).
func (s *Service) HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	hash, err := argon2id.CreateHash(password, s.params)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return hash, nil
}

// SwitchTeam checks that the user belongs to the team they want to act in.
// A non-member gets nil (the caller answers 404, never revealing the team).
func (s *Service) SwitchTeam(ctx context.Context, userID, teamID uuid.UUID) (*Membership, error) {
	return s.MembershipIn(ctx, userID, teamID)
}

// IsInstanceAdmin reports whether the user is the instance admin (the first
// user ever created).
func (s *Service) IsInstanceAdmin(ctx context.Context, userID uuid.UUID) (bool, error) {
	first, err := s.q.FirstUserID(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("first user: %w", err)
	}
	return first == userID, nil
}

func (s *Service) scopesFor(role string) []string {
	if s.roleScopes == nil {
		return []string{}
	}
	return slices.Clone(s.roleScopes(role))
}

// EffectiveScopes intersects a token's scopes with the scopes of its creator's
// role. The admin scope stands for every scope the role holds.
func EffectiveScopes(token, role []string) []string {
	out := make([]string, 0, len(token))
	if slices.Contains(token, "admin") {
		return slices.Clone(role)
	}
	roleAdmin := slices.Contains(role, "admin")
	for _, s := range token {
		if roleAdmin || slices.Contains(role, s) {
			out = append(out, s)
		}
	}
	return out
}

// EffectiveProjects combines a token's project list with the creator's
// restriction (owners and admins have none). nil means every project.
func EffectiveProjects(token []uuid.UUID, m *Membership) []uuid.UUID {
	member := m.ProjectIDs
	if m.Role == RoleOwner || m.Role == RoleAdmin {
		member = nil
	}
	switch {
	case token == nil:
		return member
	case member == nil:
		return token
	}
	out := make([]uuid.UUID, 0, len(token))
	for _, id := range token {
		if slices.Contains(member, id) {
			out = append(out, id)
		}
	}
	return out
}
