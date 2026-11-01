package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// Operator recovery: what `nebulactrl-server reset-password` and `reset-2fa`
// do to an account. They run on the control-plane host with database access,
// outside any request, so they take a transaction from the caller (who records
// the audit rows in it) and never read a principal. Whoever can run them can
// already read the database; they exist so a lost password or lost second
// factor of the only Owner does not need hand-written SQL.

// Errors of the operator recovery commands.
var (
	// ErrNoAccount is returned when no account has the email.
	ErrNoAccount = errors.New("no account with that email address")
	// ErrSetupPending is returned when the instance has no accounts at all: the
	// first-run setup (and its one-time setup token) has not happened yet.
	ErrSetupPending = errors.New("this instance has no accounts yet: first-run setup is still pending; open the setup URL the installer printed instead")
	// ErrNoPassword is returned when a reset link is asked for an account that has
	// no password (it signs in through a passkey or an identity provider).
	ErrNoPassword = errors.New("this account has no password, so a reset link cannot work; use --generate to give it one")
	// ErrOnlySignInMethod is returned when clearing the passkeys would leave an
	// account with no way to sign in.
	ErrOnlySignInMethod = errors.New("this account has no password and no linked sign-in provider, so its passkeys are its only way in; run reset-password --generate first")
)

// OperatorAccount is an account an operator command acts on.
type OperatorAccount struct {
	ID          uuid.UUID
	Email       string
	Name        string
	HasPassword bool
	// Teams are the teams the account belongs to; audit rows are written in each.
	Teams []uuid.UUID
}

// SecondFactorSummary says what second-factor material an account holds.
type SecondFactorSummary struct {
	TOTP          bool
	Passkeys      int
	RecoveryCodes int
}

// None reports whether the account holds no second-factor material at all.
func (s SecondFactorSummary) None() bool {
	return !s.TOTP && s.Passkeys == 0 && s.RecoveryCodes == 0
}

// OperatorFind looks an account up by email inside tx.
func (s *Service) OperatorFind(ctx context.Context, tx pgx.Tx, email string) (*OperatorAccount, error) {
	q := s.q.WithTx(tx)
	u, err := q.GetUserByEmail(ctx, normalizeEmail(email))
	if errors.Is(err, pgx.ErrNoRows) {
		n, cerr := q.CountUsers(ctx)
		if cerr != nil {
			return nil, fmt.Errorf("count users: %w", cerr)
		}
		if n == 0 {
			return nil, ErrSetupPending
		}
		return nil, ErrNoAccount
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	teams, err := q.ListUserTeamIDs(ctx, u.ID)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	return &OperatorAccount{ID: u.ID, Email: u.Email, Name: u.Name, HasPassword: u.PasswordHash != "", Teams: teams}, nil
}

// OperatorResetLink issues a single-use reset link (30 minutes) for the account.
// It returns the raw token, which exists nowhere else; the caller builds the URL
// with ResetLink. Accounts without a password get ErrNoPassword.
func (s *Service) OperatorResetLink(ctx context.Context, tx pgx.Tx, acct *OperatorAccount) (raw string, expires time.Time, err error) {
	if !acct.HasPassword {
		return "", time.Time{}, ErrNoPassword
	}
	return s.issueResetToken(ctx, tx, acct.ID, nil)
}

// OperatorSetPassword sets the account's password directly. Every outstanding
// reset link of the account dies with it. The caller ends the account's sessions
// once the transaction has committed (RevokeUserSessions).
func (s *Service) OperatorSetPassword(ctx context.Context, tx pgx.Tx, acct *OperatorAccount, password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := argon2id.CreateHash(password, s.params)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	q := s.q.WithTx(tx)
	if err := q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{ID: acct.ID, PasswordHash: hash}); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	now := s.methods.clock()
	if err := q.SpendPasswordResetTokens(ctx, db.SpendPasswordResetTokensParams{UserID: acct.ID, UsedAt: &now}); err != nil {
		return fmt.Errorf("spend reset tokens: %w", err)
	}
	return nil
}

// OperatorSecondFactors reports what second-factor material the account holds.
func (s *Service) OperatorSecondFactors(ctx context.Context, tx pgx.Tx, acct *OperatorAccount) (SecondFactorSummary, error) {
	q := s.q.WithTx(tx)
	var out SecondFactorSummary
	has, err := q.UserHasTOTP(ctx, acct.ID)
	if err != nil {
		return out, fmt.Errorf("check totp: %w", err)
	}
	out.TOTP = has
	n, err := q.CountUserPasskeys(ctx, acct.ID)
	if err != nil {
		return out, fmt.Errorf("count passkeys: %w", err)
	}
	out.Passkeys = int(n)
	left, err := q.CountRecoveryCodesLeft(ctx, acct.ID)
	if err != nil {
		return out, fmt.Errorf("count recovery codes: %w", err)
	}
	out.RecoveryCodes = int(left)
	return out, nil
}

// OperatorClearSecondFactors removes the account's authenticator app (including
// a half-finished enrolment and its lockout counter), every passkey and every
// recovery code, and reports what was held. It refuses when that would leave the
// account with no way to sign in (no password, no linked provider, passkeys
// only). The caller ends the account's sessions after the commit.
func (s *Service) OperatorClearSecondFactors(ctx context.Context, tx pgx.Tx, acct *OperatorAccount) (SecondFactorSummary, error) {
	held, err := s.OperatorSecondFactors(ctx, tx, acct)
	if err != nil {
		return held, err
	}
	q := s.q.WithTx(tx)
	if held.Passkeys > 0 && !acct.HasPassword {
		ids, err := q.ListUserIdentities(ctx, acct.ID)
		if err != nil {
			return held, fmt.Errorf("list identities: %w", err)
		}
		if len(ids) == 0 {
			return held, ErrOnlySignInMethod
		}
	}
	if err := q.DeleteUserTOTP(ctx, acct.ID); err != nil {
		return held, fmt.Errorf("delete totp: %w", err)
	}
	keys, err := q.ListUserPasskeys(ctx, acct.ID)
	if err != nil {
		return held, fmt.Errorf("list passkeys: %w", err)
	}
	for _, k := range keys {
		if _, err := q.DeleteUserPasskey(ctx, db.DeleteUserPasskeyParams{ID: k.ID, UserID: acct.ID}); err != nil {
			return held, fmt.Errorf("delete passkey: %w", err)
		}
	}
	if err := q.DeleteRecoveryCodes(ctx, acct.ID); err != nil {
		return held, fmt.Errorf("delete recovery codes: %w", err)
	}
	return held, nil
}
