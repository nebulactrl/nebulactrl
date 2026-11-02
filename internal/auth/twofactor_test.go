package auth_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/auth/totptest"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// gpaWrongCode returns a 6-digit code that is valid for none of the steps the
// server accepts around the clock.
func gpaWrongCode(secret []byte, now time.Time) string {
	for i := 0; ; i++ {
		c := fmt.Sprintf("%06d", i)
		ok := true
		for _, d := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
			if totptest.Code(secret, now.Add(d)) == c {
				ok = false
			}
		}
		if ok {
			return c
		}
	}
}

func (r *gpaRig) failures(u *auth.User) int32 {
	r.t.Helper()
	row, err := r.q.GetUserTOTP(context.Background(), u.ID)
	require.NoError(r.t, err)
	return row.Failures
}

func TestTOTPEnrolmentStoresASealedSecretAndReplacesAPendingOne(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()

	first, err := r.svc.StartTOTPEnrolment(ctx, r.user)
	require.NoError(t, err)
	// The enrolment shows exactly the secret that is stored, in base32 and in the
	// otpauth URI an authenticator app imports.
	require.Equal(t, auth.EncodeTOTPSecret(r.totpSecret(r.user)), first.Secret)
	require.True(t, strings.HasPrefix(first.URI, "otpauth://totp/"), first.URI)
	require.Contains(t, first.URI, "secret="+first.Secret)
	require.Contains(t, first.URI, "issuer="+auth.Issuer)
	row, err := r.q.GetUserTOTP(ctx, r.user.ID)
	require.NoError(t, err)
	require.Nil(t, row.EnabledAt, "enrolment stays pending until a code confirms it")
	require.NotContains(t, string(row.SecretEnc), first.Secret, "the secret is sealed, not stored in clear")
	has, err := r.svc.HasSecondFactor(ctx, r.user.ID)
	require.NoError(t, err)
	require.False(t, has, "a pending secret is not a second factor")

	// Starting again replaces the pending secret.
	second, err := r.svc.StartTOTPEnrolment(ctx, r.user)
	require.NoError(t, err)
	require.NotEqual(t, first.Secret, second.Secret)
	require.Equal(t, auth.EncodeTOTPSecret(r.totpSecret(r.user)), second.Secret)
}

func TestTOTPEnrolmentNeedsTheSecretKey(t *testing.T) {
	r := newGpaRig(t)
	bare := auth.NewService(r.pool, policy.ScopesForRole) // never Configure'd: no secret box
	ctx := context.Background()

	_, err := bare.StartTOTPEnrolment(ctx, r.user)
	gpaRequireAPIError(t, err, 503, "secret_key_missing")
	err = r.inTx(func(tx pgx.Tx) error {
		_, err := bare.EnableTOTP(ctx, tx, r.user.ID, "123456")
		return err
	})
	gpaRequireAPIError(t, err, 503, "secret_key_missing")

	// A TOTP account cannot be verified without the key either, and the failure
	// is not mistaken for a wrong code (which would count towards the lockout).
	r.enrollTOTP(r.user)
	err = bare.VerifySecondFactor(ctx, r.user.ID, "123456")
	gpaRequireAPIError(t, err, 503, "secret_key_missing")
	require.Zero(t, r.failures(r.user))
}

func TestEnableTOTPChecksTheCodeAndIssuesRecoveryCodes(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()

	// Nothing started yet.
	err := r.inTx(func(tx pgx.Tx) error { _, err := r.svc.EnableTOTP(ctx, tx, r.user.ID, "123456"); return err })
	gpaRequireAPIError(t, err, 409, "two_factor_not_pending")

	_, err = r.svc.StartTOTPEnrolment(ctx, r.user)
	require.NoError(t, err)
	secret := r.totpSecret(r.user)

	// A wrong code is a 422 and leaves the enrolment pending, with no codes.
	err = r.inTx(func(tx pgx.Tx) error {
		_, err := r.svc.EnableTOTP(ctx, tx, r.user.ID, gpaWrongCode(secret, *r.clock))
		return err
	})
	gpaRequireAPIError(t, err, 422, "invalid_two_factor_code")
	up, err := r.q.UserHasTOTP(ctx, r.user.ID)
	require.NoError(t, err)
	require.False(t, up)
	require.Zero(t, r.recoveryLeft(r.user))

	code := totptest.Code(secret, *r.clock)
	var codes []string
	err = r.inTx(func(tx pgx.Tx) error {
		var err error
		codes, err = r.svc.EnableTOTP(ctx, tx, r.user.ID, " "+code[:3]+" "+code[3:]+" ") // spaces as typed from an app
		return err
	})
	require.NoError(t, err)
	require.Len(t, codes, auth.RecoveryCodeCount)
	require.Len(t, map[string]bool{codes[0]: true, codes[1]: true, codes[2]: true}, 3)
	require.Equal(t, auth.RecoveryCodeCount, r.recoveryLeft(r.user))
	has, err := r.svc.HasSecondFactor(ctx, r.user.ID)
	require.NoError(t, err)
	require.True(t, has)

	// The confirming code is spent: it cannot be replayed to sign in.
	require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, r.user.ID, code), auth.ErrInvalidTwoFactor)

	// Enabled already: neither enable nor a new enrolment is allowed.
	err = r.inTx(func(tx pgx.Tx) error {
		_, err := r.svc.EnableTOTP(ctx, tx, r.user.ID, totptest.Code(secret, r.clock.Add(30*time.Second)))
		return err
	})
	gpaRequireAPIError(t, err, 409, "two_factor_already_enabled")
	_, err = r.svc.StartTOTPEnrolment(ctx, r.user)
	gpaRequireAPIError(t, err, 409, "two_factor_already_enabled")
}

func TestEnableTOTPRollsBackWithItsTransaction(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	_, err := r.svc.StartTOTPEnrolment(ctx, r.user)
	require.NoError(t, err)
	secret := r.totpSecret(r.user)

	boom := fmt.Errorf("audit insert failed")
	err = r.inTx(func(tx pgx.Tx) error {
		if _, err := r.svc.EnableTOTP(ctx, tx, r.user.ID, totptest.Code(secret, *r.clock)); err != nil {
			return err
		}
		return boom
	})
	require.ErrorIs(t, err, boom)
	up, err := r.q.UserHasTOTP(ctx, r.user.ID)
	require.NoError(t, err)
	require.False(t, up, "2FA must not switch on when the surrounding transaction fails")
	require.Zero(t, r.recoveryLeft(r.user))
}

func TestVerifySecondFactorAcceptsEachTOTPStepOnce(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	secret, _ := r.enrollTOTP(r.user)

	code := r.totpCode(secret)
	require.NoError(t, r.svc.VerifySecondFactor(ctx, r.user.ID, code))
	// The same code, and a code from the step before it, are replays.
	require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, r.user.ID, code), auth.ErrInvalidTwoFactor)
	require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, r.user.ID, totptest.Code(secret, r.clock.Add(-30*time.Second))), auth.ErrInvalidTwoFactor)
	// The next step works, typed with a space in the middle.
	next := r.totpCode(secret)
	require.NoError(t, r.svc.VerifySecondFactor(ctx, r.user.ID, next[:3]+" "+next[3:]))
	// A code is not for another account: it is not bound to userID by luck.
	other := r.newUser("other@example.com", gpaPassword)
	require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, other.ID, r.totpCode(secret)), auth.ErrInvalidTwoFactor)
}

func TestVerifySecondFactorBurnsRecoveryCodes(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	_, codes := r.enrollTOTP(r.user)

	// Case and separators do not matter; a code works once.
	require.NoError(t, r.svc.VerifySecondFactor(ctx, r.user.ID, strings.ToUpper(codes[0])))
	require.Equal(t, auth.RecoveryCodeCount-1, r.recoveryLeft(r.user))
	require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, r.user.ID, codes[0]), auth.ErrInvalidTwoFactor)
	require.NoError(t, r.svc.VerifySecondFactor(ctx, r.user.ID, strings.ReplaceAll(codes[1], "-", " ")))
	require.Equal(t, auth.RecoveryCodeCount-2, r.recoveryLeft(r.user))

	// A well-formed code that was never issued is refused.
	require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, r.user.ID, "aaaa-bbbb-cccc-dddd"), auth.ErrInvalidTwoFactor)
	require.Equal(t, auth.RecoveryCodeCount-2, r.recoveryLeft(r.user))
}

func TestVerifySecondFactorLocksAfterRepeatedFailures(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	secret, codes := r.enrollTOTP(r.user)

	// Garbage that is neither a TOTP nor a recovery code counts as a failure too.
	require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, r.user.ID, "not a code"), auth.ErrInvalidTwoFactor)
	require.EqualValues(t, 1, r.failures(r.user))
	require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, r.user.ID, gpaWrongCode(secret, *r.clock)), auth.ErrInvalidTwoFactor)
	require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, r.user.ID, "aaaa-bbbb-cccc-dddd"), auth.ErrInvalidTwoFactor)
	require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, r.user.ID, ""), auth.ErrInvalidTwoFactor)
	require.EqualValues(t, 4, r.failures(r.user))

	// A success clears the count: four more misses still do not lock.
	require.NoError(t, r.svc.VerifySecondFactor(ctx, r.user.ID, r.totpCode(secret)))
	require.Zero(t, r.failures(r.user))
	for range 4 {
		require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, r.user.ID, "not a code"), auth.ErrInvalidTwoFactor)
	}
	require.NoError(t, r.svc.VerifySecondFactor(ctx, r.user.ID, r.totpCode(secret)))

	// Five misses in a row lock the account: even the right code and a recovery
	// code are refused with 429 until the lock runs out.
	for range 5 {
		require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, r.user.ID, "not a code"), auth.ErrInvalidTwoFactor)
	}
	err := r.svc.VerifySecondFactor(ctx, r.user.ID, r.totpCode(secret))
	require.ErrorIs(t, err, auth.ErrTwoFactorLocked)
	gpaRequireAPIError(t, err, 429, "two_factor_locked")
	require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, r.user.ID, codes[0]), auth.ErrTwoFactorLocked)
	require.Equal(t, auth.RecoveryCodeCount, r.recoveryLeft(r.user), "a locked check must not burn a recovery code")

	r.advance(10 * time.Minute)
	require.NoError(t, r.svc.VerifySecondFactor(ctx, r.user.ID, r.totpCode(secret)))
}

func TestVerifySecondFactorRefusesASecretItCannotOpen(t *testing.T) {
	r := newGpaRig(t)
	secret, _ := r.enrollTOTP(r.user)

	// The same schema read with another secret key (a rotated or wrong key).
	other, err := secretbox.New([]byte(strings.Repeat("k", secretbox.KeySize)))
	require.NoError(t, err)
	wrongKey := auth.NewService(r.pool, policy.ScopesForRole)
	wrongKey.Configure(other, gpaBase)
	wrongKey.SetClock(func() time.Time { return r.clock.Add(time.Minute) })

	err = wrongKey.VerifySecondFactor(context.Background(), r.user.ID, totptest.Code(secret, r.clock.Add(time.Minute)))
	require.ErrorContains(t, err, "open totp secret")
	require.NotErrorIs(t, err, auth.ErrInvalidTwoFactor, "an operator problem is not a wrong code")
	require.Zero(t, r.failures(r.user))
}

func TestVerifySecondFactorForAnAccountWithoutAnApp(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	u := r.newUser("keys@example.com", "")
	codes := r.seedRecoveryCodes(u)

	// A passkey-only account has no TOTP row: a recovery code is the way in.
	require.NoError(t, r.svc.VerifySecondFactor(ctx, u.ID, codes[0]))
	require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, u.ID, codes[0]), auth.ErrInvalidTwoFactor, "burned")
	require.Equal(t, auth.RecoveryCodeCount-1, r.recoveryLeft(u))
	// A 6-digit TOTP-shaped code has nothing to match; so do wrong lengths.
	require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, u.ID, "123456"), auth.ErrInvalidTwoFactor)
	require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, u.ID, codes[1][:9]), auth.ErrInvalidTwoFactor)
	require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, u.ID, "aaaa-bbbb-cccc-dddd"), auth.ErrInvalidTwoFactor)
	require.Equal(t, auth.RecoveryCodeCount-1, r.recoveryLeft(u))

	// A pending (not yet enabled) enrolment does not turn TOTP checks on.
	_, err := r.svc.StartTOTPEnrolment(ctx, u)
	require.NoError(t, err)
	require.NoError(t, r.svc.VerifySecondFactor(ctx, u.ID, codes[2]))
}

func TestRegenerateRecoveryCodes(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	regen := func(u *auth.User, code, password string) ([]string, error) {
		var out []string
		err := r.inTx(func(tx pgx.Tx) error {
			var err error
			out, err = r.svc.RegenerateRecoveryCodes(ctx, tx, u.ID, code, password)
			return err
		})
		return out, err
	}

	// Without any second factor there is nothing to regenerate.
	_, err := regen(r.user, "123456", gpaPassword)
	gpaRequireAPIError(t, err, 409, "two_factor_not_enabled")

	secret, old := r.enrollTOTP(r.user)
	// With an authenticator app a current code is required; the password alone is not.
	_, err = regen(r.user, "", gpaPassword)
	gpaRequireAPIError(t, err, 422, "invalid_two_factor_code")
	_, err = regen(r.user, gpaWrongCode(secret, r.clock.Add(30*time.Second)), "")
	gpaRequireAPIError(t, err, 422, "invalid_two_factor_code")
	require.NoError(t, r.svc.VerifySecondFactor(ctx, r.user.ID, old[0]), "the old set is still valid after refused requests")

	fresh, err := regen(r.user, r.totpCode(secret), "")
	require.NoError(t, err)
	require.Len(t, fresh, auth.RecoveryCodeCount)
	require.Equal(t, auth.RecoveryCodeCount, r.recoveryLeft(r.user))
	// The old set is dead: the first code (valid just above) and the last one.
	// Checking all ten would trip the lock after five wrong codes.
	for _, c := range []string{old[0], old[len(old)-1]} {
		require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, r.user.ID, c), auth.ErrInvalidTwoFactor)
	}
	require.NoError(t, r.svc.VerifySecondFactor(ctx, r.user.ID, fresh[0]))

	// A recovery code proves the factor as well, and is burned doing it.
	again, err := regen(r.user, fresh[1], "")
	require.NoError(t, err)
	require.Len(t, again, auth.RecoveryCodeCount)
	require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, r.user.ID, fresh[2]), auth.ErrInvalidTwoFactor)
}

func TestRegenerateRecoveryCodesForAPasskeyOnlyAccount(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	regen := func(u *auth.User, code, password string) ([]string, error) {
		var out []string
		err := r.inTx(func(tx pgx.Tx) error {
			var err error
			out, err = r.svc.RegenerateRecoveryCodes(ctx, tx, u.ID, code, password)
			return err
		})
		return out, err
	}

	// An account with a password but no app proves itself with the password.
	withPw := r.newUser("pw@example.com", gpaPassword)
	r.insertPasskey(withPw, "laptop")
	_, err := regen(withPw, "", "wrong password")
	gpaRequireAPIError(t, err, 422, "invalid_password")
	codes, err := regen(withPw, "", gpaPassword)
	require.NoError(t, err)
	require.Len(t, codes, auth.RecoveryCodeCount)
	require.Equal(t, auth.RecoveryCodeCount, r.recoveryLeft(withPw))
	// Neither a missing password nor a missing code is enough.
	_, err = regen(withPw, "", "")
	gpaRequireAPIError(t, err, 422, "invalid_two_factor_code")

	// A passwordless account can only use an unused recovery code.
	bare := r.newUser("bare@example.com", "")
	r.insertPasskey(bare, "key")
	_, err = regen(bare, "", "anything")
	gpaRequireAPIError(t, err, 422, "invalid_two_factor_code")
	held := r.seedRecoveryCodes(bare)
	next, err := regen(bare, held[0], "")
	require.NoError(t, err)
	require.Len(t, next, auth.RecoveryCodeCount)
	require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, bare.ID, held[1]), auth.ErrInvalidTwoFactor, "the replaced set is gone")
	require.NoError(t, r.svc.VerifySecondFactor(ctx, bare.ID, next[0]))
}

func TestDisableTOTP(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	disable := func(u *auth.User, code, password string) error {
		return r.inTx(func(tx pgx.Tx) error { return r.svc.DisableTOTP(ctx, tx, u.ID, code, password) })
	}

	gpaRequireAPIError(t, disable(r.user, "123456", gpaPassword), 409, "two_factor_not_enabled")

	secret, _ := r.enrollTOTP(r.user)
	// Wrong password: refused before the code is even looked at (no failure counted).
	gpaRequireAPIError(t, disable(r.user, r.totpCode(secret), "wrong password"), 422, "invalid_password")
	require.Zero(t, r.failures(r.user))
	// Right password, wrong code: 422 (the session itself is fine) and counted.
	gpaRequireAPIError(t, disable(r.user, gpaWrongCode(secret, *r.clock), gpaPassword), 422, "invalid_two_factor_code")
	require.EqualValues(t, 1, r.failures(r.user))
	up, err := r.q.UserHasTOTP(ctx, r.user.ID)
	require.NoError(t, err)
	require.True(t, up, "still enabled after the refused attempts")

	require.NoError(t, disable(r.user, r.totpCode(secret), gpaPassword))
	up, err = r.q.UserHasTOTP(ctx, r.user.ID)
	require.NoError(t, err)
	require.False(t, up)
	_, err = r.q.GetUserTOTP(ctx, r.user.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows, "the secret is deleted, not just disabled")
	require.Zero(t, r.recoveryLeft(r.user), "without a second factor the recovery codes go too")
	// It can be enrolled again afterwards.
	r.enrollTOTP(r.user)
}

func TestDisableTOTPKeepsRecoveryCodesWhenAPasskeyRemains(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	secret, _ := r.enrollTOTP(r.user)
	r.insertPasskey(r.user, "phone")

	err := r.inTx(func(tx pgx.Tx) error { return r.svc.DisableTOTP(ctx, tx, r.user.ID, r.totpCode(secret), gpaPassword) })
	require.NoError(t, err)
	require.Equal(t, auth.RecoveryCodeCount, r.recoveryLeft(r.user), "the passkey is still a second factor and needs its recovery codes")
	has, err := r.svc.HasSecondFactor(ctx, r.user.ID)
	require.NoError(t, err)
	require.True(t, has)
}

func TestDisableTOTPOfAPasswordlessAccountNeedsOnlyTheCode(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	u := r.newUser("sso@example.com", "")
	_, codes := r.enrollTOTP(u)

	// No password exists to check, so whatever is passed is ignored; the code is not.
	err := r.inTx(func(tx pgx.Tx) error { return r.svc.DisableTOTP(ctx, tx, u.ID, "aaaa-bbbb-cccc-dddd", "ignored") })
	gpaRequireAPIError(t, err, 422, "invalid_two_factor_code")
	err = r.inTx(func(tx pgx.Tx) error { return r.svc.DisableTOTP(ctx, tx, u.ID, codes[0], "") })
	require.NoError(t, err, "a recovery code proves the factor")
	up, err := r.q.UserHasTOTP(ctx, u.ID)
	require.NoError(t, err)
	require.False(t, up)
}

func TestDisableTOTPWhileLockedIsRefusedWithoutBurningAnything(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	secret, codes := r.enrollTOTP(r.user)
	for range 5 {
		require.ErrorIs(t, r.svc.VerifySecondFactor(ctx, r.user.ID, "not a code"), auth.ErrInvalidTwoFactor)
	}
	err := r.inTx(func(tx pgx.Tx) error { return r.svc.DisableTOTP(ctx, tx, r.user.ID, r.totpCode(secret), gpaPassword) })
	gpaRequireAPIError(t, err, 429, "two_factor_locked")
	err = r.inTx(func(tx pgx.Tx) error { return r.svc.DisableTOTP(ctx, tx, r.user.ID, codes[0], gpaPassword) })
	gpaRequireAPIError(t, err, 429, "two_factor_locked")
	up, err := r.q.UserHasTOTP(ctx, r.user.ID)
	require.NoError(t, err)
	require.True(t, up)
	require.Equal(t, auth.RecoveryCodeCount, r.recoveryLeft(r.user))
}

func TestSecurityStatusReflectsEveryFactor(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()

	st, err := r.svc.SecurityStatus(ctx, r.user.ID)
	require.NoError(t, err)
	require.True(t, st.HasPassword)
	require.False(t, st.TOTPEnabled)
	require.False(t, st.TOTPPending)
	require.Zero(t, st.RecoveryCodesLeft)
	// Lists are empty, never nil (they are serialised as JSON arrays).
	require.NotNil(t, st.Passkeys)
	require.Empty(t, st.Passkeys)
	require.NotNil(t, st.Identities)
	require.Empty(t, st.Identities)
	require.NotNil(t, st.RequiredByTeams)
	require.Empty(t, st.RequiredByTeams)

	_, err = r.svc.StartTOTPEnrolment(ctx, r.user)
	require.NoError(t, err)
	st, err = r.svc.SecurityStatus(ctx, r.user.ID)
	require.NoError(t, err)
	require.True(t, st.TOTPPending)
	require.False(t, st.TOTPEnabled)
	require.Zero(t, st.RecoveryCodesLeft, "recovery codes exist only once a factor is on")

	_, codes := r.enrollTOTP(r.user)
	require.NoError(t, r.svc.VerifySecondFactor(ctx, r.user.ID, codes[0]))
	st, err = r.svc.SecurityStatus(ctx, r.user.ID)
	require.NoError(t, err)
	require.True(t, st.TOTPEnabled)
	require.False(t, st.TOTPPending)
	require.Equal(t, auth.RecoveryCodeCount-1, st.RecoveryCodesLeft)

	// A team that demands 2FA shows up; so do linked identities and passkeys.
	p, err := r.svc.PrincipalForSession(ctx, r.user, nil)
	require.NoError(t, err)
	require.NoError(t, r.inTx(func(tx pgx.Tx) error { return r.svc.SetTeamRequireTwoFactor(ctx, tx, p.WorkspaceID, true) }))
	prov := r.provider(gpaProviderOpts{Kind: auth.KindOIDC, Name: "Okta"})
	_, err = r.q.CreateUserIdentity(ctx, db.CreateUserIdentityParams{UserID: r.user.ID, ProviderID: prov.ID, Subject: "sub-1", Email: "ada@corp.example"})
	require.NoError(t, err)
	r.insertPasskey(r.user, "laptop")

	st, err = r.svc.SecurityStatus(ctx, r.user.ID)
	require.NoError(t, err)
	require.Equal(t, []auth.RequiredTeam{{ID: p.WorkspaceID, Name: auth.DefaultTeamName}}, st.RequiredByTeams)
	require.Len(t, st.Identities, 1)
	require.Equal(t, prov.ID, st.Identities[0].ProviderID)
	require.Equal(t, "Okta", st.Identities[0].ProviderName)
	require.Equal(t, auth.KindOIDC, st.Identities[0].ProviderKind)
	require.Equal(t, "ada@corp.example", st.Identities[0].Email)
	require.Nil(t, st.Identities[0].LastLoginAt)
	require.False(t, st.Identities[0].LinkedAt.IsZero())
	require.Len(t, st.Passkeys, 1)
	require.Equal(t, "laptop", st.Passkeys[0].Name)

	// Passkeys alone also count as a factor that has recovery codes.
	keys := r.newUser("keys@example.com", "")
	r.insertPasskey(keys, "key")
	r.seedRecoveryCodes(keys)
	st, err = r.svc.SecurityStatus(ctx, keys.ID)
	require.NoError(t, err)
	require.False(t, st.HasPassword)
	require.False(t, st.TOTPEnabled)
	require.Equal(t, auth.RecoveryCodeCount, st.RecoveryCodesLeft)

	_, err = r.svc.SecurityStatus(ctx, uuid.New())
	require.ErrorIs(t, err, pgx.ErrNoRows)
}

func TestUnlinkIdentity(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	unlink := func(u *auth.User, id uuid.UUID) error {
		return r.inTx(func(tx pgx.Tx) error { return r.svc.UnlinkIdentity(ctx, tx, u.ID, id) })
	}
	link := func(u *auth.User, p db.AuthProvider, sub string) db.UserIdentity {
		id, err := r.q.CreateUserIdentity(ctx, db.CreateUserIdentityParams{UserID: u.ID, ProviderID: p.ID, Subject: sub, Email: u.Email})
		require.NoError(t, err)
		return id
	}
	okta := r.provider(gpaProviderOpts{Kind: auth.KindOIDC, Name: "Okta"})
	gh := r.provider(gpaProviderOpts{Kind: auth.KindGitHub, Name: "GitHub"})

	// A password keeps the account reachable, so the only identity may go.
	withPw := r.newUser("pw@example.com", gpaPassword)
	oktaID := link(withPw, okta, "pw-1")
	gpaRequireAPIError(t, unlink(withPw, uuid.New()), 404, "not_found")
	other := r.newUser("other@example.com", gpaPassword)
	gpaRequireAPIError(t, unlink(other, oktaID.ID), 404, "not_found") // someone else's identity
	ids, err := r.q.ListUserIdentities(ctx, withPw.ID)
	require.NoError(t, err)
	require.Len(t, ids, 1, "another account cannot remove it")
	require.NoError(t, unlink(withPw, oktaID.ID))
	ids, err = r.q.ListUserIdentities(ctx, withPw.ID)
	require.NoError(t, err)
	require.Empty(t, ids)

	// Without a password the last identity is the only way in.
	sso := r.newUser("sso@example.com", "")
	only := link(sso, okta, "sso-1")
	gpaRequireAPIError(t, unlink(sso, only.ID), 409, "last_sign_in_method")
	ids, err = r.q.ListUserIdentities(ctx, sso.ID)
	require.NoError(t, err)
	require.Len(t, ids, 1)
	// A second identity frees the first.
	link(sso, gh, "sso-gh")
	require.NoError(t, unlink(sso, only.ID))
	ids, err = r.q.ListUserIdentities(ctx, sso.ID)
	require.NoError(t, err)
	require.Len(t, ids, 1)
	require.Equal(t, gh.ID, ids[0].ProviderID)
}

func TestTeamTwoFactorRequirement(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	p, err := r.svc.PrincipalForSession(ctx, r.user, nil)
	require.NoError(t, err)
	team := p.WorkspaceID
	set := func(id uuid.UUID, v bool) error {
		return r.inTx(func(tx pgx.Tx) error { return r.svc.SetTeamRequireTwoFactor(ctx, tx, id, v) })
	}

	_, err = r.svc.GetTeamSecurity(ctx, uuid.New())
	gpaRequireAPIError(t, err, 404, "not_found")
	gpaRequireAPIError(t, set(uuid.New(), true), 404, "not_found")

	// One member (the owner), not enrolled, no requirement.
	got, err := r.svc.GetTeamSecurity(ctx, team)
	require.NoError(t, err)
	require.Equal(t, auth.TeamSecurity{Require2FA: false, Members: 1, Enrolled: 0}, got)

	// A second member with a passkey counts as enrolled; so does TOTP.
	dev := r.newUser("dev@example.com", gpaPassword)
	_, err = r.q.InsertMembership(ctx, db.InsertMembershipParams{UserID: dev.ID, WorkspaceID: team, Role: auth.RoleDeveloper})
	require.NoError(t, err)
	r.insertPasskey(dev, "key")
	got, err = r.svc.GetTeamSecurity(ctx, team)
	require.NoError(t, err)
	require.Equal(t, auth.TeamSecurity{Members: 2, Enrolled: 1}, got)
	r.enrollTOTP(r.user)
	got, err = r.svc.GetTeamSecurity(ctx, team)
	require.NoError(t, err)
	require.Equal(t, auth.TeamSecurity{Members: 2, Enrolled: 2}, got)

	require.NoError(t, set(team, true))
	got, err = r.svc.GetTeamSecurity(ctx, team)
	require.NoError(t, err)
	require.True(t, got.Require2FA)
	require.NoError(t, set(team, false))
	got, err = r.svc.GetTeamSecurity(ctx, team)
	require.NoError(t, err)
	require.False(t, got.Require2FA)
}
