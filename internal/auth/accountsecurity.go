package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// ErrTwoFactorEnrollmentRequired is what a team's 2FA requirement answers to a
// user who has no second factor, for any action on that team.
var ErrTwoFactorEnrollmentRequired = apierr.New(403, "two_factor_enrollment_required",
	"this team requires two-factor authentication; set it up in Account settings → Security first")

// SecondFactorBlockedIn is SecondFactorBlocked for an action on teamID, which
// need not be the team the session is acting in: a team's 2FA requirement
// applies to everything done on that team, so a user cannot step around it by
// switching the session to another team first.
func (s *Service) SecondFactorBlockedIn(ctx context.Context, p *Principal, teamID uuid.UUID) (bool, error) {
	if p == nil || teamID == uuid.Nil || p.UserID == uuid.Nil {
		return false, nil
	}
	switch p.Type {
	case TypeUser, TypeToken, TypeMCP:
	default:
		return false, nil
	}
	required, has, err := s.TeamTwoFactorState(ctx, teamID, p.UserID)
	if err != nil || !required || has {
		return false, err
	}
	return !(p.Type == TypeUser && p.SessionMFA), nil
}

// RequireSecondFactorIn returns ErrTwoFactorEnrollmentRequired when
// SecondFactorBlockedIn says p may not act on teamID yet.
func (s *Service) RequireSecondFactorIn(ctx context.Context, p *Principal, teamID uuid.UUID) error {
	blocked, err := s.SecondFactorBlockedIn(ctx, p, teamID)
	if err != nil {
		return err
	}
	if blocked {
		return ErrTwoFactorEnrollmentRequired
	}
	return nil
}

// RevokeOtherUserSessions ends every session of the user except the one in
// ctx (the browser that just changed the password). Like RevokeUserSessions it
// also ends a sign-in parked at its second-factor step.
func RevokeOtherUserSessions(ctx context.Context, sm *scs.SessionManager, userID uuid.UUID) error {
	id := userID.String()
	keep := sm.Token(ctx)
	return sm.Iterate(context.Background(), func(sctx context.Context) error {
		if keep != "" && sm.Token(sctx) == keep {
			return nil
		}
		if sm.GetString(sctx, SessionUserKey) == id || sm.GetString(sctx, sessionPendingUser) == id {
			return sm.Destroy(sctx)
		}
		return nil
	})
}

// TokenSummary is an API token that still works, as shown after a password
// change or reset. It never carries the secret.
type TokenSummary struct {
	ID         uuid.UUID
	TeamID     uuid.UUID
	TeamName   string
	Name       string
	Prefix     string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
}

// LiveTokens lists the API tokens of the user that still work, in every team.
func (s *Service) LiveTokens(ctx context.Context, userID uuid.UUID) ([]TokenSummary, error) {
	rows, err := s.q.ListUserLiveAPITokens(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	out := make([]TokenSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, TokenSummary{
			ID: r.ID, TeamID: r.WorkspaceID, TeamName: r.WorkspaceName, Name: r.Name, Prefix: r.Prefix,
			CreatedAt: r.CreatedAt, LastUsedAt: r.LastUsedAt, ExpiresAt: r.ExpiresAt,
		})
	}
	return out, nil
}

// RevokeLiveTokens revokes every API token of the user, in every team, inside
// tx, and returns how many it ended. A password change or reset keeps tokens
// unless the person chooses this.
func (s *Service) RevokeLiveTokens(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (int, error) {
	n, err := s.q.WithTx(tx).RevokeUserAPITokens(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("revoke tokens: %w", err)
	}
	return int(n), nil
}

// CheckResetToken reports whether a reset link still works, without spending
// it: the reset page asks when it opens, so a dead link is explained before the
// person types a new password. It answers ErrInvalidResetToken otherwise.
func (s *Service) CheckResetToken(ctx context.Context, token string) error {
	token = trimResetToken(token)
	if token == "" {
		return ErrInvalidResetToken
	}
	_, err := s.q.PeekPasswordResetToken(ctx, db.PeekPasswordResetTokenParams{TokenHash: hashReset(token), ExpiresAt: s.methods.clock()})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidResetToken
	}
	if err != nil {
		return fmt.Errorf("check reset token: %w", err)
	}
	return nil
}

// ProviderLinkPending reports whether the provider flow in this session is a
// link started from Account → Security (read it before CompleteProviderLogin,
// which spends the flow state).
func ProviderLinkPending(ctx context.Context, sm *scs.SessionManager) bool {
	return sm.GetString(ctx, sessOIDCLink) != ""
}

// ConfirmPassword checks the password of an account that has one (422
// invalid_password). An account without a password passes: it signs in through
// a provider or a passkey.
func (s *Service) ConfirmPassword(ctx context.Context, userID uuid.UUID, password string) error {
	u, err := s.q.GetUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	return s.checkPassword(ctx, u, password)
}
