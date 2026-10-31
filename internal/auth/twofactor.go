package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// Issuer is the name authenticator apps show for the account.
const Issuer = "NebulaCtrl"

// VerifySecondFactor checks a TOTP code or a recovery code for userID. A TOTP
// step is accepted once; a recovery code is burned on use. Repeated failures lock
// the account's second-factor check for a few minutes, across sessions.
func (s *Service) VerifySecondFactor(ctx context.Context, userID uuid.UUID, code string) error {
	row, err := s.q.GetUserTOTP(ctx, userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("get totp: %w", err)
	}
	if err != nil || row.EnabledAt == nil {
		// No authenticator app: a passkey-only account can still use a recovery
		// code. There is no lockout row to count failures on; an 80-bit code is not
		// guessable, and the pending login and the client IP are rate limited.
		return s.verifyRecoveryCodeOnly(ctx, userID, code)
	}
	now := s.methods.clock()
	if row.LockedUntil != nil && row.LockedUntil.After(now) {
		return ErrTwoFactorLocked
	}
	ok := false
	if looksLikeTOTP(code) {
		box, err := s.secretBox()
		if err != nil {
			return err
		}
		secret, err := box.Open(secretbox.UserTOTPSecret, userID.String(), row.SecretEnc)
		if err != nil {
			return fmt.Errorf("open totp secret: %w", err)
		}
		if step, match := VerifyTOTP(secret, code, now, row.LastStep); match {
			n, err := s.q.AcceptTOTPStep(ctx, db.AcceptTOTPStepParams{UserID: userID, LastStep: step})
			if err != nil {
				return fmt.Errorf("accept totp step: %w", err)
			}
			ok = n == 1
		}
	} else if normalized := normalizeRecoveryCode(code); len(normalized) == 16 {
		n, err := s.q.UseRecoveryCode(ctx, db.UseRecoveryCodeParams{UserID: userID, CodeHash: HashRecoveryCode(code)})
		if err != nil {
			return fmt.Errorf("use recovery code: %w", err)
		}
		ok = n == 1
	}
	if ok {
		return nil
	}
	if _, err := s.q.RecordTOTPFailure(ctx, db.RecordTOTPFailureParams{UserID: userID, MaxFailures: totpMaxFailures, LockSeconds: totpLockSeconds}); err != nil {
		return fmt.Errorf("record totp failure: %w", err)
	}
	return ErrInvalidTwoFactor
}

func (s *Service) verifyRecoveryCodeOnly(ctx context.Context, userID uuid.UUID, code string) error {
	if len(normalizeRecoveryCode(code)) != 16 {
		return ErrInvalidTwoFactor
	}
	n, err := s.q.UseRecoveryCode(ctx, db.UseRecoveryCodeParams{UserID: userID, CodeHash: HashRecoveryCode(code)})
	if err != nil {
		return fmt.Errorf("use recovery code: %w", err)
	}
	if n != 1 {
		return ErrInvalidTwoFactor
	}
	return nil
}

// TOTPEnrolment is what the user needs to add the account to an authenticator.
type TOTPEnrolment struct {
	Secret string // base32, for manual entry
	URI    string // otpauth:// URI
}

// StartTOTPEnrolment creates (or replaces) the pending TOTP secret of the user.
// It fails with 409 when TOTP is already enabled.
func (s *Service) StartTOTPEnrolment(ctx context.Context, u *User) (*TOTPEnrolment, error) {
	box, err := s.secretBox()
	if err != nil {
		return nil, err
	}
	if has, err := s.q.UserHasTOTP(ctx, u.ID); err != nil {
		return nil, fmt.Errorf("check totp: %w", err)
	} else if has {
		return nil, apierr.Conflict("two_factor_already_enabled", "two-factor authentication is already enabled")
	}
	secret, err := GenerateTOTPSecret()
	if err != nil {
		return nil, err
	}
	enc, err := box.Seal(secretbox.UserTOTPSecret, u.ID.String(), secret)
	if err != nil {
		return nil, fmt.Errorf("seal totp secret: %w", err)
	}
	if err := s.q.UpsertPendingTOTP(ctx, db.UpsertPendingTOTPParams{UserID: u.ID, SecretEnc: enc}); err != nil {
		return nil, fmt.Errorf("store totp secret: %w", err)
	}
	return &TOTPEnrolment{Secret: EncodeTOTPSecret(secret), URI: TOTPProvisioningURI(Issuer, u.Email, secret)}, nil
}

// EnableTOTP confirms the pending secret with a code from the app, switches 2FA
// on and returns the freshly generated recovery codes (shown once).
func (s *Service) EnableTOTP(ctx context.Context, tx pgx.Tx, userID uuid.UUID, code string) ([]string, error) {
	box, err := s.secretBox()
	if err != nil {
		return nil, err
	}
	q := s.q.WithTx(tx)
	row, err := q.GetUserTOTP(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.Conflict("two_factor_not_pending", "start the enrolment first")
	}
	if err != nil {
		return nil, fmt.Errorf("get totp: %w", err)
	}
	if row.EnabledAt != nil {
		return nil, apierr.Conflict("two_factor_already_enabled", "two-factor authentication is already enabled")
	}
	secret, err := box.Open(secretbox.UserTOTPSecret, userID.String(), row.SecretEnc)
	if err != nil {
		return nil, fmt.Errorf("open totp secret: %w", err)
	}
	step, ok := VerifyTOTP(secret, code, s.methods.clock(), 0)
	if !ok {
		return nil, apierr.Unprocessable("invalid_two_factor_code", "that code is not valid; check the time on your device and try again")
	}
	if n, err := q.EnableTOTP(ctx, db.EnableTOTPParams{UserID: userID, LastStep: step}); err != nil {
		return nil, fmt.Errorf("enable totp: %w", err)
	} else if n != 1 {
		return nil, apierr.Conflict("two_factor_already_enabled", "two-factor authentication is already enabled")
	}
	return s.replaceRecoveryCodes(ctx, q, userID)
}

func (s *Service) replaceRecoveryCodes(ctx context.Context, q *db.Queries, userID uuid.UUID) ([]string, error) {
	codes, err := NewRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if err := q.DeleteRecoveryCodes(ctx, userID); err != nil {
		return nil, fmt.Errorf("clear recovery codes: %w", err)
	}
	for _, c := range codes {
		if err := q.InsertRecoveryCode(ctx, db.InsertRecoveryCodeParams{UserID: userID, CodeHash: HashRecoveryCode(c)}); err != nil {
			return nil, fmt.Errorf("store recovery code: %w", err)
		}
	}
	return codes, nil
}

// RegenerateRecoveryCodes replaces every recovery code after the user proves a
// second factor: a current code, or, for an account without an authenticator app
// (passkeys only), the password.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, tx pgx.Tx, userID uuid.UUID, code, password string) ([]string, error) {
	if err := s.requireSecondFactor(ctx, userID); err != nil {
		return nil, err
	}
	hasTOTP, err := s.q.UserHasTOTP(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("check totp: %w", err)
	}
	if !hasTOTP && code == "" && password != "" {
		u, err := s.q.GetUser(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("get user: %w", err)
		}
		if u.PasswordHash == "" {
			return nil, apierr.Unprocessable("invalid_two_factor_code", "enter an unused recovery code")
		}
		if err := s.checkPassword(ctx, u, password); err != nil {
			return nil, err
		}
	} else if err := s.verifyForAction(ctx, userID, code); err != nil {
		return nil, err
	}
	return s.replaceRecoveryCodes(ctx, s.q.WithTx(tx), userID)
}

// DisableTOTP turns 2FA off after the user proves a second factor and, when the
// account has a password, the password.
func (s *Service) DisableTOTP(ctx context.Context, tx pgx.Tx, userID uuid.UUID, code, password string) error {
	if err := s.requireEnabled(ctx, userID); err != nil {
		return err
	}
	u, err := s.q.GetUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if u.PasswordHash != "" {
		ok, err := argon2id.ComparePasswordAndHash(password, u.PasswordHash)
		if err != nil || !ok {
			return apierr.Unprocessable("invalid_password", "the password is incorrect")
		}
	}
	if err := s.verifyForAction(ctx, userID, code); err != nil {
		return err
	}
	q := s.q.WithTx(tx)
	if err := q.DeleteUserTOTP(ctx, userID); err != nil {
		return fmt.Errorf("delete totp: %w", err)
	}
	// Passkeys are still a second factor; the recovery codes stay for them.
	if n, err := q.CountUserPasskeys(ctx, userID); err != nil {
		return fmt.Errorf("count passkeys: %w", err)
	} else if n == 0 {
		if err := q.DeleteRecoveryCodes(ctx, userID); err != nil {
			return fmt.Errorf("delete recovery codes: %w", err)
		}
	}
	return nil
}

func (s *Service) requireSecondFactor(ctx context.Context, userID uuid.UUID) error {
	has, err := s.q.UserHasSecondFactor(ctx, userID)
	if err != nil {
		return fmt.Errorf("check second factor: %w", err)
	}
	if !has {
		return apierr.Conflict("two_factor_not_enabled", "two-factor authentication is not enabled")
	}
	return nil
}

func (s *Service) requireEnabled(ctx context.Context, userID uuid.UUID) error {
	has, err := s.q.UserHasTOTP(ctx, userID)
	if err != nil {
		return fmt.Errorf("check totp: %w", err)
	}
	if !has {
		return apierr.Conflict("two_factor_not_enabled", "two-factor authentication is not enabled")
	}
	return nil
}

// verifyForAction is VerifySecondFactor for an already signed-in user: a wrong
// code is a 422 (the session itself is fine).
func (s *Service) verifyForAction(ctx context.Context, userID uuid.UUID, code string) error {
	err := s.VerifySecondFactor(ctx, userID, code)
	if errors.Is(err, ErrInvalidTwoFactor) {
		return apierr.Unprocessable("invalid_two_factor_code", "that code is not valid")
	}
	return err
}

// IdentityInfo is a linked identity-provider account.
type IdentityInfo struct {
	ID           uuid.UUID
	ProviderID   uuid.UUID
	ProviderName string
	ProviderKind string
	Email        string
	LinkedAt     time.Time
	LastLoginAt  *time.Time
}

// RequiredTeam is a team that demands 2FA of the user.
type RequiredTeam struct {
	ID   uuid.UUID
	Name string
}

// SecurityStatus is the signed-in user's sign-in security picture.
type SecurityStatus struct {
	TOTPEnabled       bool
	TOTPPending       bool
	RecoveryCodesLeft int
	Passkeys          []PasskeyInfo
	HasPassword       bool
	Identities        []IdentityInfo
	RequiredByTeams   []RequiredTeam
}

// SecurityStatus loads the status for userID.
func (s *Service) SecurityStatus(ctx context.Context, userID uuid.UUID) (*SecurityStatus, error) {
	u, err := s.q.GetUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	out := &SecurityStatus{HasPassword: u.PasswordHash != "", Identities: []IdentityInfo{}, RequiredByTeams: []RequiredTeam{}}
	if out.Passkeys, err = s.ListPasskeys(ctx, userID); err != nil {
		return nil, err
	}
	row, err := s.q.GetUserTOTP(ctx, userID)
	switch {
	case err == nil:
		out.TOTPEnabled = row.EnabledAt != nil
		out.TOTPPending = row.EnabledAt == nil
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("get totp: %w", err)
	}
	if out.TOTPEnabled || len(out.Passkeys) > 0 {
		n, err := s.q.CountRecoveryCodesLeft(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("count recovery codes: %w", err)
		}
		out.RecoveryCodesLeft = int(n)
	}
	ids, err := s.q.ListUserIdentities(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list identities: %w", err)
	}
	for _, i := range ids {
		out.Identities = append(out.Identities, IdentityInfo{
			ID: i.ID, ProviderID: i.ProviderID, ProviderName: i.ProviderName, ProviderKind: i.ProviderKind, Email: i.Email, LinkedAt: i.CreatedAt, LastLoginAt: i.LastLoginAt,
		})
	}
	teams, err := s.q.ListUserRequiredTwoFactorTeams(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list required teams: %w", err)
	}
	for _, t := range teams {
		out.RequiredByTeams = append(out.RequiredByTeams, RequiredTeam{ID: t.ID, Name: t.Name})
	}
	return out, nil
}

// UnlinkIdentity removes a linked identity. The user must keep another way to
// sign in (a password or another identity).
func (s *Service) UnlinkIdentity(ctx context.Context, tx pgx.Tx, userID, identityID uuid.UUID) error {
	q := s.q.WithTx(tx)
	u, err := q.GetUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	ids, err := q.ListUserIdentities(ctx, userID)
	if err != nil {
		return fmt.Errorf("list identities: %w", err)
	}
	if u.PasswordHash == "" && len(ids) <= 1 {
		return apierr.Conflict("last_sign_in_method", "this is your only way to sign in; set a password or link another account first")
	}
	n, err := q.DeleteUserIdentity(ctx, db.DeleteUserIdentityParams{ID: identityID, UserID: userID})
	if err != nil {
		return fmt.Errorf("delete identity: %w", err)
	}
	if n == 0 {
		return apierr.NotFound("identity")
	}
	return nil
}

// HasSecondFactor reports whether the user has TOTP enabled or a passkey.
func (s *Service) HasSecondFactor(ctx context.Context, userID uuid.UUID) (bool, error) {
	has, err := s.q.UserHasSecondFactor(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("check second factor: %w", err)
	}
	return has, nil
}

// TeamSecurity is a team's 2FA requirement and how many members meet it.
type TeamSecurity struct {
	Require2FA bool
	Members    int
	Enrolled   int
}

// GetTeamSecurity loads the 2FA requirement of a team.
func (s *Service) GetTeamSecurity(ctx context.Context, teamID uuid.UUID) (TeamSecurity, error) {
	st, err := s.q.GetTeamTwoFactorState(ctx, db.GetTeamTwoFactorStateParams{ID: teamID, UserID: uuid.Nil})
	if errors.Is(err, pgx.ErrNoRows) {
		return TeamSecurity{}, apierr.NotFound("team")
	}
	if err != nil {
		return TeamSecurity{}, fmt.Errorf("team 2fa state: %w", err)
	}
	c, err := s.q.CountTeamTwoFactor(ctx, teamID)
	if err != nil {
		return TeamSecurity{}, fmt.Errorf("count team 2fa: %w", err)
	}
	return TeamSecurity{Require2FA: st.RequireTwoFactor, Members: int(c.Members), Enrolled: int(c.Enrolled)}, nil
}

// SetTeamRequireTwoFactor switches the 2FA requirement of a team. Callers must
// first make sure the person flipping the switch satisfies it themselves, so they
// are not locked out.
func (s *Service) SetTeamRequireTwoFactor(ctx context.Context, tx pgx.Tx, teamID uuid.UUID, require bool) error {
	n, err := s.q.WithTx(tx).SetTeamRequireTwoFactor(ctx, db.SetTeamRequireTwoFactorParams{ID: teamID, RequireTwoFactor: require})
	if err != nil {
		return fmt.Errorf("set team 2fa: %w", err)
	}
	if n == 0 {
		return apierr.NotFound("team")
	}
	return nil
}
