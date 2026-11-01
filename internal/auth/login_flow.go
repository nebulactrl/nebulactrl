package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// Session keys of the two-step login. A password (or identity-provider) success
// for a user with TOTP stores only the pending user; the session becomes a real
// login when the second factor is verified.
const (
	// SessionMFAKey marks a session that proved a second factor (TOTP, recovery
	// code or an identity provider trusted to assert MFA).
	SessionMFAKey       = "mfa"
	sessionPendingUser  = "2fa_user"
	sessionPendingUntil = "2fa_until"
	sessionPendingTries = "2fa_tries"

	pendingTTL      = 5 * time.Minute
	maxPendingTries = 5

	// A user's account locks for totpLockSeconds after totpMaxFailures wrong
	// codes in a row, whatever session they came from.
	totpMaxFailures = 5
	totpLockSeconds = 300
)

// Errors of the second-factor step.
var (
	ErrTwoFactorRequired = apierr.New(http.StatusUnauthorized, "two_factor_required", "a verification code is required to finish signing in")
	ErrInvalidTwoFactor  = apierr.New(http.StatusUnauthorized, "invalid_two_factor_code", "invalid verification or recovery code")
	ErrTwoFactorLocked   = apierr.New(http.StatusTooManyRequests, "two_factor_locked", "too many wrong codes; try again in a few minutes")
	ErrNoPendingLogin    = apierr.New(http.StatusUnauthorized, "no_pending_login", "sign in with your password first")
)

// BeginSecondFactor is called after a first factor (password or identity
// provider) succeeded. When the user has a second factor (TOTP or a passkey) it
// parks the user in the session and reports true: the caller must answer
// "two_factor_required" and not start the session.
func (s *Service) BeginSecondFactor(ctx context.Context, sm *scs.SessionManager, u *User) (bool, error) {
	has, err := s.q.UserHasSecondFactor(ctx, u.ID)
	if err != nil {
		return false, fmt.Errorf("check second factor: %w", err)
	}
	if !has {
		return false, nil
	}
	if err := sm.RenewToken(ctx); err != nil {
		return false, fmt.Errorf("renew session: %w", err)
	}
	sm.Put(ctx, sessionPendingUser, u.ID.String())
	sm.Put(ctx, sessionPendingUntil, s.methods.clock().Add(pendingTTL).Unix())
	sm.Put(ctx, sessionPendingTries, 0)
	return true, nil
}

func clearPending(ctx context.Context, sm *scs.SessionManager) {
	sm.Remove(ctx, sessionPendingUser)
	sm.Remove(ctx, sessionPendingUntil)
	sm.Remove(ctx, sessionPendingTries)
}

// pendingUserID returns the user parked by BeginSecondFactor. An expired or
// used-up pending login is cleared and reported as ErrNoPendingLogin /
// ErrTwoFactorLocked.
func (s *Service) pendingUserID(ctx context.Context, sm *scs.SessionManager) (uuid.UUID, error) {
	raw := sm.GetString(ctx, sessionPendingUser)
	id, err := uuid.Parse(raw)
	if raw == "" || err != nil || s.methods.clock().Unix() > sm.GetInt64(ctx, sessionPendingUntil) {
		clearPending(ctx, sm)
		return uuid.Nil, ErrNoPendingLogin
	}
	if sm.GetInt(ctx, sessionPendingTries) >= maxPendingTries {
		clearPending(ctx, sm)
		return uuid.Nil, ErrTwoFactorLocked
	}
	return id, nil
}

// CompleteSecondFactor verifies a TOTP or recovery code for the user parked by
// BeginSecondFactor and returns the user. The caller then starts the session
// and marks it with SessionMFAKey. A passkey finishes the same step through
// CompletePasskeySecondFactor.
func (s *Service) CompleteSecondFactor(ctx context.Context, sm *scs.SessionManager, code string) (*User, error) {
	id, err := s.pendingUserID(ctx, sm)
	if err != nil {
		return nil, err
	}
	if err := s.VerifySecondFactor(ctx, id, code); err != nil {
		if errors.Is(err, ErrInvalidTwoFactor) {
			sm.Put(ctx, sessionPendingTries, sm.GetInt(ctx, sessionPendingTries)+1)
		}
		return nil, err
	}
	clearPending(ctx, sm)
	return s.UserByID(ctx, id)
}

// TeamTwoFactorState reports whether the team requires 2FA and whether the user
// has a second factor (TOTP or a passkey).
func (s *Service) TeamTwoFactorState(ctx context.Context, workspaceID, userID uuid.UUID) (required, hasSecondFactor bool, err error) {
	row, err := s.q.GetTeamTwoFactorState(ctx, db.GetTeamTwoFactorStateParams{ID: workspaceID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("team 2fa state: %w", err)
	}
	return row.RequireTwoFactor, row.UserHasSecondFactor, nil
}

// SecondFactorBlocked reports whether p may not act yet because its team
// requires 2FA and the user has neither enrolled TOTP or a passkey nor signed in
// through a provider trusted to assert MFA. Only user, token and MCP principals
// are subject to it.
func (s *Service) SecondFactorBlocked(ctx context.Context, sm *scs.SessionManager, p *Principal) (bool, error) {
	if p == nil || p.WorkspaceID == uuid.Nil || p.UserID == uuid.Nil {
		return false, nil
	}
	switch p.Type {
	case TypeUser, TypeToken, TypeMCP:
	default:
		return false, nil
	}
	required, has, err := s.TeamTwoFactorState(ctx, p.WorkspaceID, p.UserID)
	if err != nil || !required || has {
		return false, err
	}
	if p.Type == TypeUser && sm != nil && sm.GetBool(ctx, SessionMFAKey) {
		return false, nil
	}
	return true, nil
}
