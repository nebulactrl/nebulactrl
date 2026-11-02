package auth_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/auth/passkeytest"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// register adds a passkey held by a to u through the full ceremony.
func (r *gpaRig) register(a *passkeytest.Authenticator, u *auth.User, name, password string) (*auth.PasskeyInfo, []string, error) {
	r.t.Helper()
	ctx := r.signedIn(u)
	creation, err := r.svc.BeginPasskeyRegistration(ctx, r.sm, u.ID, gpaBase, password)
	if err != nil {
		return nil, nil, err
	}
	opts, err := json.Marshal(creation)
	require.NoError(r.t, err)
	resp, err := a.Create(opts)
	require.NoError(r.t, err)
	var info *auth.PasskeyInfo
	var codes []string
	err = r.inTx(func(tx pgx.Tx) error {
		var err error
		info, codes, err = r.svc.FinishPasskeyRegistration(ctx, tx, r.sm, u.ID, gpaBase, name, resp)
		return err
	})
	return info, codes, err
}

// mustRegister is register for a ceremony that has to work.
func (r *gpaRig) mustRegister(a *passkeytest.Authenticator, u *auth.User, name string) (*auth.PasskeyInfo, []string) {
	r.t.Helper()
	info, codes, err := r.register(a, u, name, gpaPassword)
	require.NoError(r.t, err)
	return info, codes
}

func (r *gpaRig) passkeyLogin(ctx context.Context, a *passkeytest.Authenticator) (*auth.User, error) {
	r.t.Helper()
	assertion, err := r.svc.BeginPasskeyLogin(ctx, r.sm, gpaBase)
	require.NoError(r.t, err)
	opts, err := json.Marshal(assertion)
	require.NoError(r.t, err)
	resp, err := a.Get(opts)
	require.NoError(r.t, err)
	return r.svc.FinishPasskeyLogin(ctx, r.sm, gpaBase, resp)
}

func TestPasskeysNeedAWebAuthnCompatibleAddress(t *testing.T) {
	r := newGpaRig(t)
	for base, want := range map[string]string{
		"":                           "NEBULACTRL_PUBLIC_URL",
		"nebula.example.com":         "NEBULACTRL_PUBLIC_URL",
		"https://203.0.113.7":        "domain name",
		"https://[2001:db8::1]:8443": "domain name",
		"http://nebula.example.com":  "HTTPS",
		"ftp://nebula.example.com":   "HTTPS",
	} {
		require.Contains(t, r.svc.PasskeyUnavailable(base), want, base)
	}
	for _, base := range []string{"https://nebula.example.com", "https://nebula.example.com:8443/", "http://localhost:8080", "http://app.localhost"} {
		require.Empty(t, r.svc.PasskeyUnavailable(base), base)
	}
	_, err := r.svc.BeginPasskeyLogin(r.session(), r.sm, "http://203.0.113.7")
	gpaRequireAPIError(t, err, 422, "passkeys_unavailable")
	_, err = r.svc.BeginPasskeyRegistration(r.signedIn(r.user), r.sm, r.user.ID, "", gpaPassword)
	gpaRequireAPIError(t, err, 422, "passkeys_unavailable")
	_, err = r.svc.BeginPasskeySecondFactor(r.session(), r.sm, "")
	gpaRequireAPIError(t, err, 401, "no_pending_login")
}

func TestPasskeyRegistration(t *testing.T) {
	r := newGpaRig(t)
	laptop := passkeytest.New(gpaBase)

	// The password is asked again, so a stolen session cannot plant a passkey.
	_, _, err := r.register(laptop, r.user, "Laptop", "wrong password")
	gpaRequireAPIError(t, err, 422, "invalid_password")
	_, err = r.svc.ListPasskeys(context.Background(), r.user.ID)
	require.NoError(t, err)

	info, codes, err := r.register(laptop, r.user, "  Laptop  ", gpaPassword)
	require.NoError(t, err)
	require.Equal(t, "Laptop", info.Name)
	require.NotEqual(t, uuid.Nil, info.ID)
	require.Equal(t, []string{"internal"}, info.Transports)
	require.False(t, info.Synced)
	require.Nil(t, info.LastUsedAt)
	require.Len(t, codes, 10, "an account with no recovery code left gets a fresh set")
	require.Equal(t, 10, r.recoveryLeft(r.user))

	// A second passkey keeps the codes it already has.
	phone := passkeytest.New(gpaBase)
	_, codes = r.mustRegister(phone, r.user, "Phone")
	require.Empty(t, codes)
	list, err := r.svc.ListPasskeys(context.Background(), r.user.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, []string{"Laptop", "Phone"}, []string{list[0].Name, list[1].Name}, "oldest first")

	// The browser is told which credentials already exist.
	creation, err := r.svc.BeginPasskeyRegistration(r.signedIn(r.user), r.sm, r.user.ID, gpaBase, gpaPassword)
	require.NoError(t, err)
	opts, err := json.Marshal(creation)
	require.NoError(t, err)
	_, err = laptop.Create(opts)
	require.ErrorContains(t, err, "already on this authenticator")
	require.Equal(t, auth.Issuer, creation.Response.RelyingParty.Name)
	require.Equal(t, "nebula.example.com", creation.Response.RelyingParty.ID)
	require.Equal(t, "required", string(creation.Response.AuthenticatorSelection.UserVerification))
	require.Equal(t, "required", string(creation.Response.AuthenticatorSelection.ResidentKey))
	require.Len(t, creation.Response.CredentialExcludeList, 2)
}

func TestPasskeyRegistrationRefusals(t *testing.T) {
	r := newGpaRig(t)
	a := passkeytest.New(gpaBase)
	finish := func(ctx context.Context, name string, resp []byte) error {
		return r.inTx(func(tx pgx.Tx) error {
			_, _, err := r.svc.FinishPasskeyRegistration(ctx, tx, r.sm, r.user.ID, gpaBase, name, resp)
			return err
		})
	}
	begin := func(ctx context.Context) []byte {
		creation, err := r.svc.BeginPasskeyRegistration(ctx, r.sm, r.user.ID, gpaBase, gpaPassword)
		require.NoError(t, err)
		opts, _ := json.Marshal(creation)
		resp, err := a.Create(opts)
		require.NoError(t, err)
		return resp
	}

	// Names: 1-60 characters.
	ctx := r.signedIn(r.user)
	resp := begin(ctx)
	gpaRequireAPIError(t, finish(ctx, "   ", resp), 422, "invalid_passkey_name")
	gpaRequireAPIError(t, finish(ctx, strings.Repeat("n", 61), resp), 422, "invalid_passkey_name")

	// No ceremony in the session, or one of another kind, or one that expired.
	gpaRequireAPIError(t, finish(r.signedIn(r.user), "Key", resp), 401, "no_pending_passkey")
	ctx = r.signedIn(r.user)
	_, err := r.svc.BeginPasskeyLogin(ctx, r.sm, gpaBase)
	require.NoError(t, err)
	gpaRequireAPIError(t, finish(ctx, "Key", resp), 401, "no_pending_passkey")
	ctx = r.signedIn(r.user)
	resp = begin(ctx)
	r.advance(6 * time.Minute)
	gpaRequireAPIError(t, finish(ctx, "Key", resp), 401, "no_pending_passkey")
	r.advance(-6 * time.Minute)

	// A ceremony is single use: a failed attempt burns it.
	ctx = r.signedIn(r.user)
	resp = begin(ctx)
	gpaRequireAPIError(t, finish(ctx, "Key", []byte("not json")), 422, "passkey_invalid")
	gpaRequireAPIError(t, finish(ctx, "Key", resp), 401, "no_pending_passkey")

	// An answer from another origin (a phishing page) is rejected.
	phish := passkeytest.New("https://nebula.example.com.evil.example")
	ctx = r.signedIn(r.user)
	creation, err := r.svc.BeginPasskeyRegistration(ctx, r.sm, r.user.ID, gpaBase, gpaPassword)
	require.NoError(t, err)
	opts, _ := json.Marshal(creation)
	bad, err := phish.Create(opts)
	require.NoError(t, err)
	gpaRequireAPIError(t, finish(ctx, "Key", bad), 422, "passkey_rejected")
	// A passkey without user verification is not accepted either.
	weak := passkeytest.New(gpaBase)
	weak.UserVerified = false
	ctx = r.signedIn(r.user)
	creation, err = r.svc.BeginPasskeyRegistration(ctx, r.sm, r.user.ID, gpaBase, gpaPassword)
	require.NoError(t, err)
	opts, _ = json.Marshal(creation)
	bad, err = weak.Create(opts)
	require.NoError(t, err)
	gpaRequireAPIError(t, finish(ctx, "Key", bad), 422, "passkey_rejected")
	list, err := r.svc.ListPasskeys(context.Background(), r.user.ID)
	require.NoError(t, err)
	require.Empty(t, list, "nothing was stored by a refused ceremony")

	// At most 20 passkeys.
	for i := range auth.MaxPasskeys {
		r.insertPasskey(r.user, "key "+string(rune('a'+i)))
	}
	_, err = r.svc.BeginPasskeyRegistration(r.signedIn(r.user), r.sm, r.user.ID, gpaBase, gpaPassword)
	gpaRequireAPIError(t, err, 409, "passkey_limit")
}

func TestPasswordlessAccountsRegisterWithoutAPassword(t *testing.T) {
	r := newGpaRig(t)
	u := r.newUser("sso@example.com", "")
	info, codes, err := r.register(passkeytest.New(gpaBase), u, "Key", "")
	require.NoError(t, err)
	require.Equal(t, "Key", info.Name)
	require.Len(t, codes, 10, "a passkey-only account always has recovery codes")
}

func TestPasswordlessPasskeySignIn(t *testing.T) {
	r := newGpaRig(t)
	a := passkeytest.New(gpaBase)
	info, _ := r.mustRegister(a, r.user, "Laptop")

	u, err := r.passkeyLogin(r.session(), a)
	require.NoError(t, err)
	require.Equal(t, r.user.ID, u.ID)
	list, err := r.svc.ListPasskeys(context.Background(), r.user.ID)
	require.NoError(t, err)
	require.Equal(t, info.ID, list[0].ID)
	require.NotNil(t, list[0].LastUsedAt, "a sign-in is recorded")

	// Counters keep going up across sign-ins.
	_, err = r.passkeyLogin(r.session(), a)
	require.NoError(t, err)

	// A counter that went backwards means the authenticator was cloned.
	a.SetCounter(0, 1)
	_, err = r.passkeyLogin(r.session(), a)
	gpaRequireAPIError(t, err, 401, "passkey_failed")

	// Synced passkeys report a counter of zero: that is not a clone.
	b := passkeytest.New(gpaBase)
	b.NoCounter = true
	other := r.newUser("other@example.com", gpaPassword)
	r.mustRegister2(b, other, "Synced")
	for range 2 {
		got, err := r.passkeyLogin(r.session(), b)
		require.NoError(t, err)
		require.Equal(t, other.ID, got.ID)
	}
}

func (r *gpaRig) mustRegister2(a *passkeytest.Authenticator, u *auth.User, name string) {
	r.t.Helper()
	_, _, err := r.register(a, u, name, gpaPassword)
	require.NoError(r.t, err)
}

func TestPasskeySignInRefusals(t *testing.T) {
	r := newGpaRig(t)
	a := passkeytest.New(gpaBase)
	r.mustRegister(a, r.user, "Laptop")
	finish := func(ctx context.Context, resp []byte) error {
		_, err := r.svc.FinishPasskeyLogin(ctx, r.sm, gpaBase, resp)
		return err
	}
	get := func(ctx context.Context, who *passkeytest.Authenticator) []byte {
		assertion, err := r.svc.BeginPasskeyLogin(ctx, r.sm, gpaBase)
		require.NoError(t, err)
		opts, _ := json.Marshal(assertion)
		resp, err := who.Get(opts)
		require.NoError(t, err)
		return resp
	}

	// No prompt open, or an expired one, or garbage.
	gpaRequireAPIError(t, finish(r.session(), []byte("{}")), 401, "no_pending_passkey")
	ctx := r.session()
	resp := get(ctx, a)
	r.advance(6 * time.Minute)
	gpaRequireAPIError(t, finish(ctx, resp), 401, "no_pending_passkey")
	r.advance(-6 * time.Minute)
	ctx = r.session()
	_ = get(ctx, a)
	gpaRequireAPIError(t, finish(ctx, []byte("nope")), 422, "passkey_invalid")

	// A prompt cannot be answered twice.
	ctx = r.session()
	resp = get(ctx, a)
	require.NoError(t, finish(ctx, resp))
	gpaRequireAPIError(t, finish(ctx, resp), 401, "no_pending_passkey")

	// Another origin, and a missing user verification, are refused.
	ctx = r.session()
	assertion, err := r.svc.BeginPasskeyLogin(ctx, r.sm, gpaBase)
	require.NoError(t, err)
	opts, _ := json.Marshal(assertion)
	a.Origin = "https://evil.example"
	resp, err = a.Get(opts)
	require.NoError(t, err)
	a.Origin = gpaBase
	gpaRequireAPIError(t, finish(ctx, resp), 401, "passkey_failed")
	ctx = r.session()
	assertion, err = r.svc.BeginPasskeyLogin(ctx, r.sm, gpaBase)
	require.NoError(t, err)
	opts, _ = json.Marshal(assertion)
	a.UserVerified = false
	resp, err = a.Get(opts)
	require.NoError(t, err)
	a.UserVerified = true
	gpaRequireAPIError(t, finish(ctx, resp), 401, "passkey_failed")

	// A credential whose account was deleted cannot sign in.
	ctx = r.session()
	resp = get(ctx, a)
	r.exec(`DELETE FROM user_passkeys WHERE user_id=$1`, r.user.ID)
	gpaRequireAPIError(t, finish(ctx, resp), 401, "passkey_failed")
}

func TestPasskeyAsSecondFactor(t *testing.T) {
	r := newGpaRig(t)
	a := passkeytest.New(gpaBase)
	r.mustRegister(a, r.user, "Key")

	park := func() context.Context {
		ctx := r.session()
		parked, err := r.svc.BeginSecondFactor(ctx, r.sm, r.user)
		require.NoError(t, err)
		require.True(t, parked, "an account with a passkey needs a second step after the password")
		return ctx
	}
	assertion := func(ctx context.Context) []byte {
		creation, err := r.svc.BeginPasskeySecondFactor(ctx, r.sm, gpaBase)
		require.NoError(t, err)
		opts, _ := json.Marshal(creation)
		// The prompt names this account's credentials.
		require.Len(t, creation.Response.AllowedCredentials, 1)
		resp, err := a.Get(opts)
		require.NoError(t, err)
		return resp
	}

	// Nothing parked: no methods, no prompt.
	_, _, err := r.svc.SecondFactorMethods(r.session(), r.sm)
	gpaRequireAPIError(t, err, 401, "no_pending_login")
	_, err = r.svc.CompletePasskeySecondFactor(r.session(), r.sm, gpaBase, []byte("{}"))
	gpaRequireAPIError(t, err, 401, "no_pending_login")

	ctx := park()
	totp, passkey, err := r.svc.SecondFactorMethods(ctx, r.sm)
	require.NoError(t, err)
	require.False(t, totp)
	require.True(t, passkey)
	u, err := r.svc.CompletePasskeySecondFactor(ctx, r.sm, gpaBase, assertion(ctx))
	require.NoError(t, err)
	require.Equal(t, r.user.ID, u.ID)
	_, _, err = r.svc.SecondFactorMethods(ctx, r.sm)
	gpaRequireAPIError(t, err, 401, "no_pending_login")

	// Answering without a prompt or with garbage is refused and counted; a correct answer still works afterwards.
	ctx = park()
	_, err = r.svc.CompletePasskeySecondFactor(ctx, r.sm, gpaBase, []byte("{}"))
	gpaRequireAPIError(t, err, 401, "no_pending_passkey")
	_ = assertion(ctx)
	_, err = r.svc.CompletePasskeySecondFactor(ctx, r.sm, gpaBase, []byte("garbage"))
	gpaRequireAPIError(t, err, 422, "passkey_invalid")
	require.Equal(t, 1, r.sm.GetInt(ctx, "2fa_tries"), "a missing prompt is not a wrong answer; garbage is")
	_, err = r.svc.CompletePasskeySecondFactor(ctx, r.sm, gpaBase, assertion(ctx))
	require.NoError(t, err)

	// Five wrong answers lock the pending sign-in.
	ctx = park()
	for range 5 {
		_ = assertion(ctx)
		_, err = r.svc.CompletePasskeySecondFactor(ctx, r.sm, gpaBase, []byte("garbage"))
		gpaRequireAPIError(t, err, 422, "passkey_invalid")
	}
	_, err = r.svc.BeginPasskeySecondFactor(ctx, r.sm, gpaBase)
	gpaRequireAPIError(t, err, 429, "two_factor_locked")

	// A security key without a PIN still works as a second factor: verification is preferred, not forced.
	ctx = park()
	creation, err := r.svc.BeginPasskeySecondFactor(ctx, r.sm, gpaBase)
	require.NoError(t, err)
	opts, _ := json.Marshal(creation)
	a.UserVerified = false
	resp, err := a.Get(opts)
	require.NoError(t, err)
	a.UserVerified = true
	got, err := r.svc.CompletePasskeySecondFactor(ctx, r.sm, gpaBase, resp)
	require.NoError(t, err)
	require.Equal(t, r.user.ID, got.ID)

	// An answer under the wrong origin fails and is counted.
	ctx = park()
	creation, err = r.svc.BeginPasskeySecondFactor(ctx, r.sm, gpaBase)
	require.NoError(t, err)
	opts, _ = json.Marshal(creation)
	a.Origin = "https://evil.example"
	resp, err = a.Get(opts)
	require.NoError(t, err)
	a.Origin = gpaBase
	_, err = r.svc.CompletePasskeySecondFactor(ctx, r.sm, gpaBase, resp)
	gpaRequireAPIError(t, err, 401, "passkey_failed")
	require.Equal(t, 1, r.sm.GetInt(ctx, "2fa_tries"))
}

func TestPasskeySecondFactorNeedsAPasskey(t *testing.T) {
	r := newGpaRig(t)
	r.enrollTOTP(r.user)
	ctx := r.session()
	parked, err := r.svc.BeginSecondFactor(ctx, r.sm, r.user)
	require.NoError(t, err)
	require.True(t, parked)
	totp, passkey, err := r.svc.SecondFactorMethods(ctx, r.sm)
	require.NoError(t, err)
	require.True(t, totp)
	require.False(t, passkey)
	_, err = r.svc.BeginPasskeySecondFactor(ctx, r.sm, gpaBase)
	gpaRequireAPIError(t, err, 409, "no_passkeys")

	// An account with no second factor is not parked at all.
	bob := r.newUser("bob@example.com", gpaPassword)
	parked, err = r.svc.BeginSecondFactor(r.session(), r.sm, bob)
	require.NoError(t, err)
	require.False(t, parked)
}

func TestRenameAndDeletePasskeys(t *testing.T) {
	r := newGpaRig(t)
	bg := context.Background()
	first := r.insertPasskey(r.user, "One")
	second := r.insertPasskey(r.user, "Two")
	bob := r.newUser("bob@example.com", gpaPassword)
	bobs := r.insertPasskey(bob, "Bob's")

	rename := func(user *auth.User, id uuid.UUID, name string) error {
		return r.inTx(func(tx pgx.Tx) error { return r.svc.RenamePasskey(bg, tx, user.ID, id, name) })
	}
	remove := func(user *auth.User, id uuid.UUID, password string) error {
		return r.inTx(func(tx pgx.Tx) error { return r.svc.DeletePasskey(bg, tx, user.ID, id, password) })
	}
	nameOf := func(user *auth.User, id uuid.UUID) string {
		list, err := r.svc.ListPasskeys(bg, user.ID)
		require.NoError(t, err)
		for _, p := range list {
			if p.ID == id {
				return p.Name
			}
		}
		return "<gone>"
	}

	require.NoError(t, rename(r.user, first.ID, "  Renamed "))
	require.Equal(t, "Renamed", nameOf(r.user, first.ID))
	gpaRequireAPIError(t, rename(r.user, first.ID, " "), 422, "invalid_passkey_name")
	gpaRequireAPIError(t, rename(r.user, first.ID, strings.Repeat("x", 61)), 422, "invalid_passkey_name")
	gpaRequireAPIError(t, rename(r.user, uuid.New(), "x"), 404, "not_found")
	gpaRequireAPIError(t, rename(r.user, bobs.ID, "stolen"), 404, "not_found")
	require.Equal(t, "Bob's", nameOf(bob, bobs.ID))

	// Deleting needs the password, and only your own passkey.
	gpaRequireAPIError(t, remove(r.user, first.ID, "wrong password"), 422, "invalid_password")
	require.Equal(t, "Renamed", nameOf(r.user, first.ID))
	gpaRequireAPIError(t, remove(r.user, bobs.ID, gpaPassword), 404, "not_found")
	gpaRequireAPIError(t, remove(r.user, uuid.New(), gpaPassword), 404, "not_found")
	require.NoError(t, remove(r.user, first.ID, gpaPassword))
	require.Equal(t, "<gone>", nameOf(r.user, first.ID))
	require.Equal(t, "Two", nameOf(r.user, second.ID))

	// Removing the last second factor drops the recovery codes with it.
	r.seedRecoveryCodes(r.user)
	require.Positive(t, r.recoveryLeft(r.user))
	require.NoError(t, remove(r.user, second.ID, gpaPassword))
	require.Zero(t, r.recoveryLeft(r.user))

	// With another second factor left the codes stay.
	r.enrollTOTP(r.user)
	third := r.insertPasskey(r.user, "Three")
	left := r.recoveryLeft(r.user)
	require.Positive(t, left)
	require.NoError(t, remove(r.user, third.ID, gpaPassword))
	require.Equal(t, left, r.recoveryLeft(r.user))
}

func TestTheOnlySignInMethodCannotBeRemoved(t *testing.T) {
	r := newGpaRig(t)
	bg := context.Background()
	u := r.newUser("sso@example.com", "")
	only := r.insertPasskey(u, "Only")
	remove := func(id uuid.UUID) error {
		return r.inTx(func(tx pgx.Tx) error { return r.svc.DeletePasskey(bg, tx, u.ID, id, "") })
	}

	gpaRequireAPIError(t, remove(only.ID), 409, "last_sign_in_method")
	list, err := r.svc.ListPasskeys(bg, u.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)

	// With a second passkey, or a linked provider account, it may go.
	second := r.insertPasskey(u, "Second")
	require.NoError(t, remove(second.ID))
	p := r.provider(gpaProviderOpts{Kind: auth.KindGitHub})
	_, err = r.q.CreateUserIdentity(bg, db.CreateUserIdentityParams{UserID: u.ID, ProviderID: p.ID, Subject: "gh-1", Email: u.Email})
	require.NoError(t, err)
	require.NoError(t, remove(only.ID))
}

func TestListPasskeysReportsTransportsAndSyncState(t *testing.T) {
	r := newGpaRig(t)
	require.Empty(t, mustList(t, r, r.user))
	_, err := r.q.CreateUserPasskey(context.Background(), db.CreateUserPasskeyParams{UserID: r.user.ID, Name: "Synced", CredentialID: []byte("c1"), Credential: []byte(`{"flags":{"backupState":true},"transport":["internal","hybrid"]}`)})
	require.NoError(t, err)
	_, err = r.q.CreateUserPasskey(context.Background(), db.CreateUserPasskeyParams{UserID: r.user.ID, Name: "Broken", CredentialID: []byte("c2"), Credential: []byte(`"not an object"`)})
	require.NoError(t, err)
	list := mustList(t, r, r.user)
	require.Len(t, list, 2)
	require.True(t, list[0].Synced)
	require.Equal(t, []string{"internal", "hybrid"}, list[0].Transports)
	require.False(t, list[1].Synced, "an unreadable credential is listed without details instead of failing the list")
	require.Equal(t, []string{}, list[1].Transports)
}

func mustList(t *testing.T, r *gpaRig, u *auth.User) []auth.PasskeyInfo {
	t.Helper()
	list, err := r.svc.ListPasskeys(context.Background(), u.ID)
	require.NoError(t, err)
	return list
}
