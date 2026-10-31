package auth_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/alexedwards/scs/pgxstore"
	"github.com/alexedwards/scs/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

const (
	oldPassword = "correct horse battery"
	newPassword = "a brand new passphrase"
)

// recMailer records the reset emails the service asks it to send.
type recMailer struct {
	mu        sync.Mutex
	unavail   bool
	sent      []auth.ResetMail
	sendError error
}

func (m *recMailer) Available(context.Context) bool { return !m.unavail }

func (m *recMailer) SendPasswordReset(_ context.Context, rm auth.ResetMail) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, rm)
	return m.sendError
}

func (m *recMailer) mails() []auth.ResetMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]auth.ResetMail(nil), m.sent...)
}

// tokenOf extracts the raw token from an emailed link.
func tokenOf(t *testing.T, link string) string {
	t.Helper()
	_, tok, ok := strings.Cut(link, "?token=")
	require.True(t, ok, link)
	require.True(t, strings.HasPrefix(tok, auth.ResetTokenPrefix), tok)
	return tok
}

type resetRig struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	svc    *auth.Service
	mailer *recMailer
	clock  *time.Time
	user   *auth.User
}

func newResetRig(t *testing.T) *resetRig {
	t.Helper()
	pool := testutil.NewPool(t)
	svc := auth.NewService(pool, policy.ScopesForRole)
	r := &resetRig{t: t, pool: pool, q: db.New(pool), svc: svc, mailer: &recMailer{}}
	now := time.Now()
	r.clock = &now
	svc.SetClock(func() time.Time { return *r.clock })
	svc.Configure(nil, "https://ctrl.example.com")
	svc.SetResetMailer(r.mailer)
	u, err := svc.Setup(context.Background(), "Ada@Example.com", oldPassword, "Ada", "")
	require.NoError(t, err)
	r.user = u
	return r
}

// account adds a user with a password ("" for a sign-in-through-a-provider-only account).
func (r *resetRig) account(email, password string) db.User {
	r.t.Helper()
	hash := ""
	if password != "" {
		var err error
		hash, err = argon2id.CreateHash(password, argon2id.DefaultParams)
		require.NoError(r.t, err)
	}
	u, err := r.q.InsertUser(context.Background(), db.InsertUserParams{Email: email, Name: "X", PasswordHash: hash})
	require.NoError(r.t, err)
	return u
}

// confirm runs ConfirmPasswordReset in its own transaction.
func (r *resetRig) confirm(token, password string) (*auth.User, []uuid.UUID, error) {
	var u *auth.User
	var teams []uuid.UUID
	err := r.svc.InTx(context.Background(), func(tx pgx.Tx) error {
		var err error
		u, teams, err = r.svc.ConfirmPasswordReset(context.Background(), tx, token, password)
		return err
	})
	return u, teams, err
}

// requestAndLink asks for a reset and returns the link that was mailed.
func (r *resetRig) requestAndLink(email string) string {
	r.t.Helper()
	before := len(r.mailer.mails())
	require.NoError(r.t, r.svc.RequestPasswordReset(context.Background(), email))
	require.Eventually(r.t, func() bool { return len(r.mailer.mails()) == before+1 }, 3*time.Second, 5*time.Millisecond)
	return r.mailer.mails()[before].Link
}

func (r *resetRig) noMailFor(d time.Duration) {
	r.t.Helper()
	time.Sleep(d)
	require.Empty(r.t, r.mailer.mails())
}

func TestResetEmailGoesToTheAccountWithAWorkingLink(t *testing.T) {
	r := newResetRig(t)
	link := r.requestAndLink("  ADA@example.com ")
	m := r.mailer.mails()[0]
	require.Equal(t, "ada@example.com", m.To, "the stored, normalised address")
	require.Equal(t, r.user.ID, m.UserID)
	require.True(t, strings.HasPrefix(link, "https://ctrl.example.com/reset-password?token="))
	require.WithinDuration(t, r.clock.Add(auth.ResetTTL), m.ExpiresAt, time.Second, "30 minutes")
	require.Equal(t, 30*time.Minute, auth.ResetTTL)

	u, teams, err := r.confirm(tokenOf(t, link), newPassword)
	require.NoError(t, err)
	require.Equal(t, r.user.ID, u.ID)
	require.Len(t, teams, 1, "the first team, for the audit trail")

	_, err = r.svc.Login(context.Background(), r.user.Email, newPassword)
	require.NoError(t, err, "the new password works")
	_, err = r.svc.Login(context.Background(), r.user.Email, oldPassword)
	require.ErrorIs(t, err, auth.ErrInvalidCredentials, "the old one does not")
}

// The token is never stored: the table keeps a hash.
func TestResetTokenIsStoredHashedOnly(t *testing.T) {
	r := newResetRig(t)
	tok := tokenOf(t, r.requestAndLink(r.user.Email))
	var n int
	require.NoError(t, r.pool.QueryRow(context.Background(), `SELECT count(*) FROM password_reset_tokens WHERE position($1 in encode(token_hash, 'escape')) > 0`, tok).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, r.pool.QueryRow(context.Background(), `SELECT count(*) FROM password_reset_tokens WHERE created_by IS NULL AND used_at IS NULL`).Scan(&n))
	require.Equal(t, 1, n)
}

func TestResetTokenIsSingleUse(t *testing.T) {
	r := newResetRig(t)
	tok := tokenOf(t, r.requestAndLink(r.user.Email))

	_, _, err := r.confirm(tok, newPassword)
	require.NoError(t, err)
	_, _, err = r.confirm(tok, "yet another passphrase")
	require.ErrorIs(t, err, auth.ErrInvalidResetToken)
	_, err = r.svc.Login(context.Background(), r.user.Email, newPassword)
	require.NoError(t, err, "the second use changed nothing")
}

func TestResetTokenIsSingleUseUnderConcurrency(t *testing.T) {
	r := newResetRig(t)
	tok := tokenOf(t, r.requestAndLink(r.user.Email))

	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, results[i] = r.confirm(tok, newPassword)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		if err == nil {
			ok++
		} else {
			require.ErrorIs(t, err, auth.ErrInvalidResetToken)
		}
	}
	require.Equal(t, 1, ok, "exactly one of eight racing requests wins")
}

func TestResetTokenExpiresAfterThirtyMinutes(t *testing.T) {
	r := newResetRig(t)
	tok := tokenOf(t, r.requestAndLink(r.user.Email))

	*r.clock = r.clock.Add(auth.ResetTTL - time.Minute)
	live := tokenOf(t, r.requestAndLink(r.user.Email)) // a newer link is unaffected
	*r.clock = r.clock.Add(2 * time.Minute)            // the first is now past 30 minutes
	_, _, err := r.confirm(tok, newPassword)
	require.ErrorIs(t, err, auth.ErrInvalidResetToken)
	_, _, err = r.confirm(live, newPassword)
	require.NoError(t, err)
}

func TestResetRejectsGarbageTokens(t *testing.T) {
	r := newResetRig(t)
	for _, tok := range []string{"", "nope", auth.ResetTokenPrefix + "unknown", strings.Repeat("x", 500), "nct_" + strings.Repeat("a", 52)} {
		_, _, err := r.confirm(tok, newPassword)
		require.ErrorIs(t, err, auth.ErrInvalidResetToken, tok)
	}
}

func TestResetWithAWeakPasswordKeepsTheLink(t *testing.T) {
	r := newResetRig(t)
	tok := tokenOf(t, r.requestAndLink(r.user.Email))
	_, _, err := r.confirm(tok, "short")
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "weak_password", ae.Code)
	_, _, err = r.confirm(tok, newPassword)
	require.NoError(t, err, "a rejected password does not burn the link")
}

func TestResetKillsEveryOtherLinkOfTheAccount(t *testing.T) {
	r := newResetRig(t)
	first := tokenOf(t, r.requestAndLink(r.user.Email))
	second := tokenOf(t, r.requestAndLink(r.user.Email))
	_, _, err := r.confirm(first, newPassword)
	require.NoError(t, err)
	_, _, err = r.confirm(second, "yet another passphrase")
	require.ErrorIs(t, err, auth.ErrInvalidResetToken)
}

// Asking answers the same for every address: no error, and a mail only when an
// account with a password exists.
func TestResetRequestIsIdenticalForUnknownAndProviderOnlyAccounts(t *testing.T) {
	r := newResetRig(t)
	r.account("sso@example.com", "") // signs in through an identity provider only

	for _, email := range []string{"nobody@example.com", "sso@example.com", "not an address", "", strings.Repeat("a", 300) + "@example.com"} {
		require.NoError(t, r.svc.RequestPasswordReset(context.Background(), email), email)
	}
	r.noMailFor(150 * time.Millisecond)
	var n int
	require.NoError(t, r.pool.QueryRow(context.Background(), `SELECT count(*) FROM password_reset_tokens`).Scan(&n))
	require.Zero(t, n, "no token is created for them either")

	require.NoError(t, r.svc.RequestPasswordReset(context.Background(), r.user.Email))
	require.Eventually(t, func() bool { return len(r.mailer.mails()) == 1 }, 3*time.Second, 5*time.Millisecond)
}

func TestResetIsRefusedForProviderOnlyAccounts(t *testing.T) {
	r := newResetRig(t)
	sso := r.account("sso@example.com", "")

	// An admin cannot mint a link for one …
	err := r.svc.InTx(context.Background(), func(tx pgx.Tx) error {
		_, _, err := r.svc.CreateResetToken(context.Background(), tx, sso.ID, r.user.ID)
		return err
	})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 409, ae.Status)
	require.Equal(t, "no_password", ae.Code)

	// … and a token that somehow exists cannot set a password on it.
	raw := auth.ResetTokenPrefix + strings.Repeat("a", 52)
	require.NoError(t, r.insertToken(sso.ID, raw, nil, time.Now().Add(time.Hour)))
	_, _, err = r.confirm(raw, newPassword)
	require.ErrorIs(t, err, auth.ErrInvalidResetToken)
	got, err := r.q.GetUser(context.Background(), sso.ID)
	require.NoError(t, err)
	require.Empty(t, got.PasswordHash, "the account still has no password")
}

func (r *resetRig) insertToken(userID uuid.UUID, raw string, createdBy *uuid.UUID, expires time.Time) error {
	_, err := r.pool.Exec(context.Background(),
		`INSERT INTO password_reset_tokens (user_id, token_hash, created_by, expires_at) VALUES ($1, sha256($2::bytea), $3, $4)`,
		userID, raw, createdBy, expires)
	return err
}

// The per-account cap holds across requests and is silent.
func TestResetRequestsPerAccountAreCappedSilently(t *testing.T) {
	r := newResetRig(t)
	for range auth.ResetMaxPerAccountPerHour + 3 {
		require.NoError(t, r.svc.RequestPasswordReset(context.Background(), r.user.Email))
	}
	require.Eventually(t, func() bool { return len(r.mailer.mails()) >= auth.ResetMaxPerAccountPerHour }, 3*time.Second, 5*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	require.Len(t, r.mailer.mails(), auth.ResetMaxPerAccountPerHour, "three an hour")

	*r.clock = r.clock.Add(61 * time.Minute)
	r.requestAndLink(r.user.Email) // the window moved on
}

// A link an admin generated does not use up the account's own allowance.
func TestAdminLinksDoNotCountAgainstTheEmailCap(t *testing.T) {
	r := newResetRig(t)
	for range 4 {
		require.NoError(t, r.insertToken(r.user.ID, auth.ResetTokenPrefix+uuid.NewString(), &r.user.ID, time.Now().Add(time.Hour)))
	}
	r.requestAndLink(r.user.Email)
}

func TestAdminLinkWorksOnceAndNeedsNoMailer(t *testing.T) {
	r := newResetRig(t)
	r.svc.SetResetMailer(nil)
	member := r.account("member@example.com", oldPassword)

	var raw string
	err := r.svc.InTx(context.Background(), func(tx pgx.Tx) error {
		var exp time.Time
		var err error
		raw, exp, err = r.svc.CreateResetToken(context.Background(), tx, member.ID, r.user.ID)
		require.WithinDuration(t, r.clock.Add(auth.ResetTTL), exp, time.Second)
		return err
	})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(raw, auth.ResetTokenPrefix))
	require.Equal(t, "https://ctrl.example.com/reset-password?token="+raw, r.svc.ResetLink(raw))

	u, _, err := r.confirm(raw, newPassword)
	require.NoError(t, err)
	require.Equal(t, member.ID, u.ID)
	_, _, err = r.confirm(raw, newPassword)
	require.ErrorIs(t, err, auth.ErrInvalidResetToken)
}

func TestResetIsUnavailableWithoutAMailerOrAPublicURL(t *testing.T) {
	r := newResetRig(t)
	ctx := context.Background()
	require.True(t, r.svc.PasswordResetAvailable(ctx))

	r.mailer.unavail = true
	require.False(t, r.svc.PasswordResetAvailable(ctx), "no SMTP relay or team email channel")
	require.ErrorIs(t, r.svc.RequestPasswordReset(ctx, r.user.Email), auth.ErrResetUnavailable)

	r.mailer.unavail = false
	r.svc.SetResetMailer(nil)
	require.False(t, r.svc.PasswordResetAvailable(ctx))
	require.ErrorIs(t, r.svc.RequestPasswordReset(ctx, r.user.Email), auth.ErrResetUnavailable)

	r.svc.SetResetMailer(r.mailer)
	r.svc.Configure(nil, "")
	require.False(t, r.svc.PasswordResetAvailable(ctx), "a link needs the public URL")
	require.Equal(t, "", r.svc.ResetLink("ncr_x"))
	r.noMailFor(50 * time.Millisecond)

	var ae *apierr.Error
	require.ErrorAs(t, auth.ErrResetUnavailable, &ae)
	require.Equal(t, 503, ae.Status)
}

// ---- sessions ----

func sessionFor(t *testing.T, sm *scs.SessionManager, kv map[string]string) string {
	t.Helper()
	ctx, err := sm.Load(context.Background(), "")
	require.NoError(t, err)
	for k, v := range kv {
		sm.Put(ctx, k, v)
	}
	tok, _, err := sm.Commit(ctx)
	require.NoError(t, err)
	return tok
}

func sessionAlive(t *testing.T, sm *scs.SessionManager, token string) bool {
	t.Helper()
	ctx, err := sm.Load(context.Background(), token)
	require.NoError(t, err)
	return sm.GetString(ctx, auth.SessionUserKey) != "" || sm.GetString(ctx, "2fa_user") != ""
}

func TestResetRevokesEverySessionOfTheAccountAndOnlyThose(t *testing.T) {
	r := newResetRig(t)
	other := r.account("other@example.com", oldPassword)
	sm := scs.New()
	store := pgxstore.New(r.pool)
	t.Cleanup(store.StopCleanup)
	sm.Store = store

	mine := r.user.ID.String()
	laptop := sessionFor(t, sm, map[string]string{auth.SessionUserKey: mine, "teamId": uuid.NewString()})
	phone := sessionFor(t, sm, map[string]string{auth.SessionUserKey: mine})
	parked := sessionFor(t, sm, map[string]string{"2fa_user": mine}) // sign-in waiting for a TOTP code
	theirs := sessionFor(t, sm, map[string]string{auth.SessionUserKey: other.ID.String()})
	sessionFor(t, sm, map[string]string{"csrf": "x"}) // an anonymous session
	for _, s := range []string{laptop, phone, parked, theirs} {
		require.True(t, sessionAlive(t, sm, s))
	}

	tok := tokenOf(t, r.requestAndLink(r.user.Email))
	_, _, err := r.confirm(tok, newPassword)
	require.NoError(t, err)
	require.NoError(t, auth.RevokeUserSessions(sm, r.user.ID))

	require.False(t, sessionAlive(t, sm, laptop), "signed out everywhere")
	require.False(t, sessionAlive(t, sm, phone))
	require.False(t, sessionAlive(t, sm, parked), "including a sign-in parked at its second factor")
	require.True(t, sessionAlive(t, sm, theirs), "other accounts are untouched")
	var n int
	require.NoError(t, r.pool.QueryRow(context.Background(), `SELECT count(*) FROM sessions`).Scan(&n))
	require.Equal(t, 2, n, "their session and the anonymous one remain in the store")
}
