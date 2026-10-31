package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/alexedwards/scs/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

const (
	recoverOldPassword = "correct horse battery"
	recoverPublicURL   = "https://ctrl.example.com"
)

// runRecover executes one recovery command the way an operator would: the
// server's environment, the database from the DSN, stdin as typed.
func runRecover(t *testing.T, newCmd func() *cobra.Command, dsn, publicURL, stdin string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("NEBULACTRL_DATABASE_URL", dsn)
	t.Setenv("NEBULACTRL_SECRET_KEY", base64.StdEncoding.EncodeToString(newTestKey(t)))
	t.Setenv("NEBULACTRL_SECRET_KEY_FILE", "")
	t.Setenv("NEBULACTRL_SECRET_KEY_PREVIOUS", "")
	t.Setenv("NEBULACTRL_SECRET_KEY_PREVIOUS_FILE", "")
	t.Setenv("NEBULACTRL_PUBLIC_URL", publicURL)
	cmd := newCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

type recoverRig struct {
	t    *testing.T
	pool *pgxpool.Pool
	dsn  string
	svc  *auth.Service
	user *auth.User
	sm   *scs.SessionManager
}

func newRecoverRig(t *testing.T) *recoverRig {
	t.Helper()
	pool := testutil.NewPool(t)
	svc := auth.NewService(pool, policy.ScopesForRole)
	u, err := svc.Setup(context.Background(), "Alex@Example.com", recoverOldPassword, "Alex", "")
	require.NoError(t, err)
	sm := scs.New()
	sm.Store = pgxstore.NewWithCleanupInterval(pool, 0)
	return &recoverRig{t: t, pool: pool, dsn: pool.Config().ConnString(), svc: svc, user: u, sm: sm}
}

// session stores a signed-in browser session of the user and returns its token.
func (r *recoverRig) session(userID uuid.UUID) string {
	r.t.Helper()
	ctx, err := r.sm.Load(context.Background(), "")
	require.NoError(r.t, err)
	r.sm.Put(ctx, auth.SessionUserKey, userID.String())
	tok, _, err := r.sm.Commit(ctx)
	require.NoError(r.t, err)
	return tok
}

func (r *recoverRig) sessionAlive(tok string) bool {
	r.t.Helper()
	ctx, err := r.sm.Load(context.Background(), tok)
	require.NoError(r.t, err)
	return r.sm.GetString(ctx, auth.SessionUserKey) != ""
}

type auditRow struct{ actor, label, action, meta string }

func (r *recoverRig) auditRows(action string) []auditRow {
	r.t.Helper()
	rows, err := r.pool.Query(context.Background(),
		`SELECT actor_type, actor_label, action, metadata::text FROM audit_events WHERE action = $1 ORDER BY id`, action)
	require.NoError(r.t, err)
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var a auditRow
		require.NoError(r.t, rows.Scan(&a.actor, &a.label, &a.action, &a.meta))
		out = append(out, a)
	}
	require.NoError(r.t, rows.Err())
	return out
}

func (r *recoverRig) exec(sql string, args ...any) {
	r.t.Helper()
	_, err := r.pool.Exec(context.Background(), sql, args...)
	require.NoError(r.t, err)
}

func (r *recoverRig) count(sql string, args ...any) int {
	r.t.Helper()
	var n int
	require.NoError(r.t, r.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

// withSecondFactors gives the user an enabled authenticator app, two passkeys
// and three recovery codes (one already used).
func (r *recoverRig) withSecondFactors(userID uuid.UUID) {
	r.t.Helper()
	r.exec(`INSERT INTO user_totp (user_id, secret_enc, enabled_at, failures, locked_until) VALUES ($1, '\x01', now(), 4, now() + interval '5 minutes')`, userID)
	for i, name := range []string{"laptop", "phone"} {
		r.exec(`INSERT INTO user_passkeys (user_id, name, credential_id, credential) VALUES ($1, $2, $3, '{}')`, userID, name, []byte{byte(i + 1)})
	}
	for i := range 3 {
		r.exec(`INSERT INTO user_recovery_codes (user_id, code_hash, used_at) VALUES ($1, $2, CASE WHEN $3 = 0 THEN now() END)`, userID, strings.Repeat("a", i+1), i)
	}
}

func tokenFromOutput(t *testing.T, out string) string {
	t.Helper()
	_, rest, ok := strings.Cut(out, "?token=")
	require.True(t, ok, out)
	tok, _, _ := strings.Cut(rest, "\n")
	tok = strings.TrimSpace(tok)
	require.True(t, strings.HasPrefix(tok, auth.ResetTokenPrefix), tok)
	return tok
}

func TestResetPasswordPrintsALinkThatWorksOnceAndEndsSessions(t *testing.T) {
	r := newRecoverRig(t)
	sess := r.session(r.user.ID)
	require.True(t, r.sessionAlive(sess))

	out, err := runRecover(t, resetPasswordCmd, r.dsn, recoverPublicURL, "", " ALEX@example.com")
	require.NoError(t, err, out)
	require.Contains(t, out, recoverPublicURL+"/reset-password?token=ncr_")
	tok := tokenFromOutput(t, out)
	require.False(t, r.sessionAlive(sess), "every session of the account ends")

	// Only the hash is stored, created by nobody (the operator), living 30 minutes.
	require.Zero(t, r.count(`SELECT count(*) FROM password_reset_tokens WHERE position($1 in encode(token_hash, 'escape')) > 0`, tok))
	require.Equal(t, 1, r.count(`SELECT count(*) FROM password_reset_tokens WHERE user_id = $1 AND created_by IS NULL AND used_at IS NULL AND expires_at > now() + interval '29 minutes' AND expires_at <= now() + interval '31 minutes'`, r.user.ID))

	// The password is unchanged until the link is used.
	_, err = r.svc.Login(context.Background(), r.user.Email, recoverOldPassword)
	require.NoError(t, err)

	const newPassword = "a brand new passphrase"
	require.NoError(t, r.svc.InTx(context.Background(), func(tx pgx.Tx) error {
		_, _, err := r.svc.ConfirmPasswordReset(context.Background(), tx, tok, newPassword)
		return err
	}))
	_, err = r.svc.Login(context.Background(), r.user.Email, newPassword)
	require.NoError(t, err)
	err = r.svc.InTx(context.Background(), func(tx pgx.Tx) error {
		_, _, err := r.svc.ConfirmPasswordReset(context.Background(), tx, tok, newPassword+"x")
		return err
	})
	require.ErrorIs(t, err, auth.ErrInvalidResetToken, "single use")

	// Audited as a system action, with no token in it.
	rows := r.auditRows("operator.password_reset_link")
	require.Len(t, rows, 1)
	require.Equal(t, "system", rows[0].actor)
	require.Equal(t, operatorLabel, rows[0].label)
	require.Contains(t, rows[0].meta, "alex@example.com")
	require.NotContains(t, rows[0].meta, tok)
	require.NotContains(t, rows[0].meta, "ncr_")
}

func TestResetPasswordWithoutPublicURLShowsTheTokenForYouToPrefix(t *testing.T) {
	r := newRecoverRig(t)
	out, err := runRecover(t, resetPasswordCmd, r.dsn, "", "", r.user.Email)
	require.NoError(t, err, out)
	require.Contains(t, out, "<your dashboard URL>/reset-password?token=ncr_")
	require.Contains(t, out, "NEBULACTRL_PUBLIC_URL is not set")
}

func TestResetPasswordGenerateNeedsTheTypedEmailAndShowsThePasswordOnce(t *testing.T) {
	r := newRecoverRig(t)
	sess := r.session(r.user.ID)

	out, err := runRecover(t, resetPasswordCmd, r.dsn, recoverPublicURL, "someone@else.test\n", r.user.Email, "--generate")
	require.ErrorContains(t, err, "does not match")
	require.NotContains(t, out, "New password")
	_, err = r.svc.Login(context.Background(), r.user.Email, recoverOldPassword)
	require.NoError(t, err, "a wrong confirmation changes nothing")
	require.True(t, r.sessionAlive(sess))
	require.Empty(t, r.auditRows("operator.password_set"))

	out, err = runRecover(t, resetPasswordCmd, r.dsn, recoverPublicURL, "alex@example.com\n", r.user.Email, "--generate")
	require.NoError(t, err, out)
	_, after, ok := strings.Cut(out, "shown once; change it in Account settings -> Security after signing in):\n\n  ")
	require.True(t, ok, out)
	password, _, _ := strings.Cut(after, "\n")
	require.GreaterOrEqual(t, len(password), auth.MinPasswordLength)

	_, err = r.svc.Login(context.Background(), r.user.Email, password)
	require.NoError(t, err, "the printed password works")
	_, err = r.svc.Login(context.Background(), r.user.Email, recoverOldPassword)
	require.ErrorIs(t, err, auth.ErrInvalidCredentials)
	require.False(t, r.sessionAlive(sess))

	rows := r.auditRows("operator.password_set")
	require.Len(t, rows, 1)
	require.Equal(t, "system", rows[0].actor)
	require.NotContains(t, rows[0].meta, password)

	// --confirm replaces the prompt and every live reset link dies with the change.
	_, err = runRecover(t, resetPasswordCmd, r.dsn, recoverPublicURL, "", r.user.Email)
	require.NoError(t, err)
	_, err = runRecover(t, resetPasswordCmd, r.dsn, recoverPublicURL, "", r.user.Email, "--generate", "--confirm", "ALEX@example.com")
	require.NoError(t, err)
	require.Zero(t, r.count(`SELECT count(*) FROM password_reset_tokens WHERE user_id = $1 AND used_at IS NULL`, r.user.ID))
}

func TestResetPasswordGenerateGivesAPasswordlessAccountOneButLinkRefuses(t *testing.T) {
	r := newRecoverRig(t)
	var id uuid.UUID
	require.NoError(t, r.pool.QueryRow(context.Background(), `INSERT INTO users (email, name, password_hash) VALUES ('sso@example.com', 'SSO', '') RETURNING id`).Scan(&id))

	_, err := runRecover(t, resetPasswordCmd, r.dsn, recoverPublicURL, "", "sso@example.com")
	require.ErrorIs(t, err, auth.ErrNoPassword)
	require.Zero(t, r.count(`SELECT count(*) FROM password_reset_tokens WHERE user_id = $1`, id))

	out, err := runRecover(t, resetPasswordCmd, r.dsn, recoverPublicURL, "", "sso@example.com", "--generate", "--confirm", "sso@example.com")
	require.NoError(t, err, out)
	require.Equal(t, 1, r.count(`SELECT count(*) FROM users WHERE id = $1 AND password_hash <> ''`, id))
	// A user without a team is audited in the oldest team.
	require.Len(t, r.auditRows("operator.password_set"), 1)
}

func TestRecoveryCommandsRefuseUnknownAccountsAndPendingSetup(t *testing.T) {
	r := newRecoverRig(t)
	for _, c := range []struct {
		name string
		cmd  func() *cobra.Command
		args []string
	}{
		{"reset-password", resetPasswordCmd, []string{"nobody@example.com"}},
		{"reset-2fa", reset2FACmd, []string{"nobody@example.com", "--confirm", "nobody@example.com"}},
	} {
		_, err := runRecover(t, c.cmd, r.dsn, recoverPublicURL, "", c.args...)
		require.ErrorIs(t, err, auth.ErrNoAccount, c.name)
	}

	// An instance with no accounts is still in first-run setup.
	r.exec(`DELETE FROM audit_events`)
	r.exec(`DELETE FROM users`)
	_, err := runRecover(t, resetPasswordCmd, r.dsn, recoverPublicURL, "", "alex@example.com")
	require.ErrorIs(t, err, auth.ErrSetupPending)
	require.ErrorContains(t, err, "setup URL")
	_, err = runRecover(t, reset2FACmd, r.dsn, recoverPublicURL, "", "alex@example.com")
	require.ErrorIs(t, err, auth.ErrSetupPending)
	require.Zero(t, r.count(`SELECT count(*) FROM audit_events WHERE action LIKE 'operator.%'`))
}

func TestReset2FARemovesEverySecondFactorAfterTheTypedEmail(t *testing.T) {
	r := newRecoverRig(t)
	r.withSecondFactors(r.user.ID)
	sess := r.session(r.user.ID)

	// A wrong confirmation, and no confirmation at all, change nothing.
	out, err := runRecover(t, reset2FACmd, r.dsn, recoverPublicURL, "yes\n", r.user.Email)
	require.ErrorContains(t, err, "does not match")
	require.Contains(t, out, "authenticator app yes, 2 passkey(s), 2 unused recovery code(s)")
	_, err = runRecover(t, reset2FACmd, r.dsn, recoverPublicURL, "", r.user.Email)
	require.ErrorContains(t, err, "no confirmation typed")
	require.Equal(t, 1, r.count(`SELECT count(*) FROM user_totp WHERE user_id = $1`, r.user.ID))
	require.Equal(t, 2, r.count(`SELECT count(*) FROM user_passkeys WHERE user_id = $1`, r.user.ID))
	require.Equal(t, 3, r.count(`SELECT count(*) FROM user_recovery_codes WHERE user_id = $1`, r.user.ID))
	require.True(t, r.sessionAlive(sess))
	require.Empty(t, r.auditRows("operator.2fa_reset"))

	out, err = runRecover(t, reset2FACmd, r.dsn, recoverPublicURL, "Alex@Example.com\n", r.user.Email)
	require.NoError(t, err, out)
	require.Contains(t, out, "Removed: authenticator app yes, 2 passkey(s), 2 recovery code(s)")
	require.Zero(t, r.count(`SELECT count(*) FROM user_totp WHERE user_id = $1`, r.user.ID))
	require.Zero(t, r.count(`SELECT count(*) FROM user_passkeys WHERE user_id = $1`, r.user.ID))
	require.Zero(t, r.count(`SELECT count(*) FROM user_recovery_codes WHERE user_id = $1`, r.user.ID))
	has, err := r.svc.HasSecondFactor(context.Background(), r.user.ID)
	require.NoError(t, err)
	require.False(t, has)
	require.False(t, r.sessionAlive(sess))

	// The password still works, and the account can enrol again.
	_, err = r.svc.Login(context.Background(), r.user.Email, recoverOldPassword)
	require.NoError(t, err)

	rows := r.auditRows("operator.2fa_reset")
	require.Len(t, rows, 1)
	require.Equal(t, "system", rows[0].actor)
	require.Equal(t, operatorLabel, rows[0].label)
	require.Contains(t, rows[0].meta, `"passkeys": 2`)

	// Nothing left: says so and writes no second audit row.
	out, err = runRecover(t, reset2FACmd, r.dsn, recoverPublicURL, "", r.user.Email)
	require.NoError(t, err, out)
	require.Contains(t, out, "nothing to reset")
	require.Len(t, r.auditRows("operator.2fa_reset"), 1)
}

func TestReset2FAAcceptsConfirmFlagAndLiftsALockedAuthenticator(t *testing.T) {
	r := newRecoverRig(t)
	// An enabled authenticator app that is locked after too many wrong codes.
	r.exec(`INSERT INTO user_totp (user_id, secret_enc, enabled_at, failures, locked_until) VALUES ($1, '\x01', now(), 5, now() + interval '5 minutes')`, r.user.ID)
	out, err := runRecover(t, reset2FACmd, r.dsn, recoverPublicURL, "", r.user.Email, "--confirm", "alex@example.com")
	require.NoError(t, err, out)
	require.NotContains(t, out, "Type the account email")
	require.Zero(t, r.count(`SELECT count(*) FROM user_totp WHERE user_id = $1`, r.user.ID), "the secret and its lockout counter are gone")
}

// A half-finished enrolment is not a second factor: there is nothing to reset.
func TestReset2FAIgnoresAnUnconfirmedEnrolment(t *testing.T) {
	r := newRecoverRig(t)
	r.exec(`INSERT INTO user_totp (user_id, secret_enc, enabled_at) VALUES ($1, '\x01', NULL)`, r.user.ID)
	out, err := runRecover(t, reset2FACmd, r.dsn, recoverPublicURL, "", r.user.Email)
	require.NoError(t, err, out)
	require.Contains(t, out, "nothing to reset")
	require.Empty(t, r.auditRows("operator.2fa_reset"))
}

func TestReset2FARefusesToLeaveAPasskeyOnlyAccountWithNoWayIn(t *testing.T) {
	r := newRecoverRig(t)
	var id uuid.UUID
	require.NoError(t, r.pool.QueryRow(context.Background(), `INSERT INTO users (email, name, password_hash) VALUES ('keys@example.com', 'Keys', '') RETURNING id`).Scan(&id))
	r.withSecondFactors(id)

	_, err := runRecover(t, reset2FACmd, r.dsn, recoverPublicURL, "", "keys@example.com", "--confirm", "keys@example.com")
	require.ErrorIs(t, err, auth.ErrOnlySignInMethod)
	require.Equal(t, 2, r.count(`SELECT count(*) FROM user_passkeys WHERE user_id = $1`, id), "nothing was removed")
	require.Equal(t, 1, r.count(`SELECT count(*) FROM user_totp WHERE user_id = $1`, id), "the transaction rolled back as a whole")

	// Giving it a password first unblocks the reset.
	_, err = runRecover(t, resetPasswordCmd, r.dsn, recoverPublicURL, "", "keys@example.com", "--generate", "--confirm", "keys@example.com")
	require.NoError(t, err)
	_, err = runRecover(t, reset2FACmd, r.dsn, recoverPublicURL, "", "keys@example.com", "--confirm", "keys@example.com")
	require.NoError(t, err)
	require.Zero(t, r.count(`SELECT count(*) FROM user_passkeys WHERE user_id = $1`, id))
}

// The prompt logic needs no database.
func TestConfirmEmail(t *testing.T) {
	acct := &auth.OperatorAccount{Email: "alex@example.com"}
	for _, c := range []struct {
		name, stdin, flag string
		ok                bool
	}{
		{"typed exactly", "alex@example.com\n", "", true},
		{"typed with other case and spaces", "  Alex@Example.com \r\n", "", true},
		{"typed without newline at EOF", "alex@example.com", "", true},
		{"typed wrong", "alex@example.org\n", "", false},
		{"typed yes", "yes\n", "", false},
		{"nothing typed", "", "", false},
		{"flag", "", "alex@example.com", true},
		{"flag wrong", "", "other@example.com", false},
		{"flag wins over stdin", "other@example.com\n", "alex@example.com", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			e := &recoverEnv{in: strings.NewReader(c.stdin), out: &out}
			err := e.confirmEmail(acct, c.flag)
			if c.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			if c.flag != "" {
				require.Empty(t, out.String(), "no prompt when --confirm is given")
			}
		})
	}
}

func TestRecoveryCommandsAreRegisteredAndNeedOneEmailArgument(t *testing.T) {
	root := newRootCmd()
	for _, name := range []string{"reset-password", "reset-2fa"} {
		c, _, err := root.Find([]string{name})
		require.NoError(t, err)
		require.Equal(t, name, c.Name())
		require.Error(t, c.Args(c, nil), "the email is required")
		require.Error(t, c.Args(c, []string{"a@b.c", "extra"}))
		require.NoError(t, c.Args(c, []string{"a@b.c"}))
	}
}
