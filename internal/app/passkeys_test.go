package app_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth/passkeytest"
	"github.com/nebulactrl/nebulactrl/internal/config"
)

const passkeyOrigin = "http://localhost:5173"

func newPasskeyHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessCfg(t, func(c *config.Config) { c.PublicURL = passkeyOrigin })
}

// rawBody sends an already-encoded JSON document as the request body.
func (h *harness) rawBody(path string, body []byte) resp {
	h.t.Helper()
	var v any
	require.NoError(h.t, json.Unmarshal(body, &v))
	return h.do("POST", path, "", v)
}

// addPasskey registers a passkey for whoever h is signed in as and returns the
// response of POST /me/passkeys.
func (h *harness) addPasskey(a *passkeytest.Authenticator, name string) resp {
	h.t.Helper()
	opt := h.do("POST", "/me/passkeys/options", "", map[string]any{"password": testPassword})
	require.Equal(h.t, 200, opt.Status, string(opt.Raw))
	cred, err := a.Create(opt.Raw)
	require.NoError(h.t, err)
	return h.do("POST", "/me/passkeys", "", map[string]any{"name": name, "credential": json.RawMessage(cred)})
}

// passkeyLogin runs the passwordless sign-in and returns the final response.
func (h *harness) passkeyLogin(a *passkeytest.Authenticator) resp {
	h.t.Helper()
	opt := h.do("POST", "/auth/passkey/options", "", nil)
	require.Equal(h.t, 200, opt.Status, string(opt.Raw))
	assertion, err := a.Get(opt.Raw)
	require.NoError(h.t, err)
	return h.rawBody("/auth/passkey/login", assertion)
}

func codesOf(t *testing.T, r resp) []string {
	t.Helper()
	var out []string
	for _, c := range r.JSON()["recoveryCodes"].([]any) {
		out = append(out, c.(string))
	}
	return out
}

func TestPasskeyRegistrationAndPasswordlessLogin(t *testing.T) {
	h := newPasskeyHarness(t)
	h.setup()
	a := passkeytest.New(passkeyOrigin)

	// Adding a passkey needs the password again.
	wrong := h.do("POST", "/me/passkeys/options", "", map[string]any{"password": "not the password"})
	require.Equal(t, 422, wrong.Status, string(wrong.Raw))
	require.Equal(t, "invalid_password", wrong.JSON()["code"])

	// The creation options ask for a discoverable credential with user verification.
	opt := h.do("POST", "/me/passkeys/options", "", map[string]any{"password": testPassword})
	require.Equal(t, 200, opt.Status, string(opt.Raw))
	var parsed struct {
		PublicKey struct {
			RP                     struct{ ID string }                            `json:"rp"`
			AuthenticatorSelection struct{ ResidentKey, UserVerification string } `json:"authenticatorSelection"`
		} `json:"publicKey"`
	}
	require.NoError(t, json.Unmarshal(opt.Raw, &parsed))
	require.Equal(t, "localhost", parsed.PublicKey.RP.ID)
	require.Equal(t, "required", parsed.PublicKey.AuthenticatorSelection.ResidentKey)
	require.Equal(t, "required", parsed.PublicKey.AuthenticatorSelection.UserVerification)

	cred, err := a.Create(opt.Raw)
	require.NoError(t, err)
	done := h.do("POST", "/me/passkeys", "", map[string]any{"name": "  MacBook  ", "credential": json.RawMessage(cred)})
	require.Equal(t, 201, done.Status, string(done.Raw))
	require.Equal(t, "MacBook", done.JSON()["passkey"].(map[string]any)["name"])
	require.Len(t, codesOf(t, done), 10, "the first second factor issues recovery codes")

	// The ceremony is single use.
	again := h.do("POST", "/me/passkeys", "", map[string]any{"name": "dup", "credential": json.RawMessage(cred)})
	require.Equal(t, 401, again.Status, string(again.Raw))
	require.Equal(t, "no_pending_passkey", again.JSON()["code"])

	sec := h.do("GET", "/me/security", "", nil).JSON()
	require.Len(t, sec["passkeys"], 1)
	require.Equal(t, float64(10), sec["recoveryCodesLeft"])
	require.Equal(t, false, sec["totpEnabled"])
	require.Nil(t, sec["passkeysUnavailable"])

	// The same authenticator cannot register twice (excludeCredentials).
	opt2 := h.do("POST", "/me/passkeys/options", "", map[string]any{"password": testPassword})
	_, err = a.Create(opt2.Raw)
	require.ErrorContains(t, err, "already on this authenticator")

	// Sign in with the passkey from a fresh browser: no password, no code.
	b := h.using(newBrowser(t))
	require.Equal(t, 401, b.do("GET", "/me", "", nil).Status)
	in := b.passkeyLogin(a)
	require.Equal(t, 200, in.Status, string(in.Raw))
	require.Equal(t, "admin@example.com", in.JSON()["email"])
	require.Equal(t, 200, b.do("GET", "/me", "", nil).Status)
	require.Equal(t, true, b.do("GET", "/me/security", "", nil).JSON()["sessionMfa"])
	last := b.do("GET", "/me/passkeys", "", nil).JSON()["passkeys"].([]any)[0].(map[string]any)
	require.NotNil(t, last["lastUsedAt"])
}

// The sign-in endpoints share a 10-per-minute limiter per client IP, so each
// refusal below runs on its own control plane.

// keyedHarness is a control plane with one registered passkey and a fresh browser.
func keyedHarness(t *testing.T) (*harness, *harness, *passkeytest.Authenticator) {
	t.Helper()
	h := newPasskeyHarness(t)
	h.setup()
	a := passkeytest.New(passkeyOrigin)
	require.Equal(t, 201, h.addPasskey(a, "key").Status)
	b := h.using(newBrowser(t))
	return h, b, a
}

func TestPasskeyLoginNeedsItsOwnCeremony(t *testing.T) {
	h, b, a := keyedHarness(t)
	opt := b.do("POST", "/auth/passkey/options", "", nil)
	good, err := a.Get(opt.Raw)
	require.NoError(t, err)
	// Another browser never asked for a challenge: its finish call is refused.
	r := h.using(newBrowser(t)).rawBody("/auth/passkey/login", good)
	require.Equal(t, 401, r.Status, string(r.Raw))
	require.Equal(t, "no_pending_passkey", r.JSON()["code"])
	// The right browser can still finish it, once.
	require.Equal(t, 200, b.rawBody("/auth/passkey/login", good).Status)
}

func TestPasskeyLoginRefusesAWrongOrigin(t *testing.T) {
	_, b, a := keyedHarness(t)
	opt := b.do("POST", "/auth/passkey/options", "", nil)
	good, err := a.Get(opt.Raw)
	require.NoError(t, err)
	a.Origin = "https://evil.example" // a phishing page relays the challenge
	bad, err := a.Get(opt.Raw)
	require.NoError(t, err)
	r := b.rawBody("/auth/passkey/login", bad)
	require.Equal(t, 401, r.Status, string(r.Raw))
	require.Equal(t, "passkey_failed", r.JSON()["code"])
	// The refusal burns the ceremony: the genuine answer to it no longer works.
	require.Equal(t, 401, b.rawBody("/auth/passkey/login", good).Status)
	require.Equal(t, 401, b.do("GET", "/me", "", nil).Status)
}

func TestPasskeyLoginRefusesAnotherChallenge(t *testing.T) {
	_, b, a := keyedHarness(t)
	opt1 := b.do("POST", "/auth/passkey/options", "", nil)
	_ = b.do("POST", "/auth/passkey/options", "", nil) // replaces the ceremony of opt1
	stale, err := a.Get(opt1.Raw)
	require.NoError(t, err)
	r := b.rawBody("/auth/passkey/login", stale)
	require.Equal(t, 401, r.Status, string(r.Raw))
}

func TestPasskeyLoginNeedsUserVerification(t *testing.T) {
	_, b, a := keyedHarness(t)
	a.UserVerified = false
	opt := b.do("POST", "/auth/passkey/options", "", nil)
	noUV, err := a.Get(opt.Raw)
	require.NoError(t, err)
	r := b.rawBody("/auth/passkey/login", noUV)
	require.Equal(t, 401, r.Status, string(r.Raw))
	require.Equal(t, 401, b.do("GET", "/me", "", nil).Status)
}

func TestPasskeyLoginRefusesAClonedAuthenticator(t *testing.T) {
	_, b, a := keyedHarness(t)
	for range 2 {
		require.Equal(t, 200, b.passkeyLogin(a).Status)
	}
	a.SetCounter(0, 1) // the counter goes backwards
	r := b.passkeyLogin(a)
	require.Equal(t, 401, r.Status, string(r.Raw))
}

func TestPasskeyLoginRefusesAnUnknownPasskey(t *testing.T) {
	h, b, _ := keyedHarness(t)
	stranger := passkeytest.New(passkeyOrigin)
	sopt := h.do("POST", "/me/passkeys/options", "", map[string]any{"password": testPassword})
	_, err := stranger.Create(sopt.Raw) // created on the device, never submitted
	require.NoError(t, err)
	r := b.passkeyLogin(stranger)
	require.Equal(t, 401, r.Status, string(r.Raw))
	require.Equal(t, 401, b.do("GET", "/me", "", nil).Status, "no refusal left a session behind")
}

func TestPasskeyAsSecondFactor(t *testing.T) {
	h := newPasskeyHarness(t)
	h.setup()
	a := passkeytest.New(passkeyOrigin)
	reg := h.addPasskey(a, "key")
	require.Equal(t, 201, reg.Status, string(reg.Raw))
	_ = codesOf(t, reg)

	// Password first: the sign-in parks and asks for the second step.
	b := h.using(newBrowser(t))
	pw := b.login("admin@example.com", testPassword)
	require.Equal(t, 401, pw.Status, string(pw.Raw))
	require.Equal(t, "two_factor_required", pw.JSON()["code"])
	require.Equal(t, 401, b.do("GET", "/me", "", nil).Status)
	m := b.do("GET", "/auth/2fa", "", nil).JSON()
	require.Equal(t, true, m["passkey"])
	require.Equal(t, false, m["totp"])

	// A different authenticator cannot finish it; the right one can.
	opt := b.do("POST", "/auth/2fa/passkey/options", "", nil)
	require.Equal(t, 200, opt.Status, string(opt.Raw))
	var allow struct {
		PublicKey struct {
			AllowCredentials []struct{ ID string } `json:"allowCredentials"`
		} `json:"publicKey"`
	}
	require.NoError(t, json.Unmarshal(opt.Raw, &allow))
	require.Len(t, allow.PublicKey.AllowCredentials, 1, "the second factor names the account's credentials")
	stranger := passkeytest.New(passkeyOrigin)
	_, err := stranger.Get(opt.Raw)
	require.Error(t, err)
	good, err := a.Get(opt.Raw)
	require.NoError(t, err)
	done := b.rawBody("/auth/2fa/passkey", good)
	require.Equal(t, 200, done.Status, string(done.Raw))
	require.Equal(t, 200, b.do("GET", "/me", "", nil).Status)
	require.Equal(t, true, b.do("GET", "/me/security", "", nil).JSON()["sessionMfa"])

	// Tampering with the signed data fails and counts against the pending login.
	c := h.using(newBrowser(t))
	require.Equal(t, 401, c.login("admin@example.com", testPassword).Status)
	opt = c.do("POST", "/auth/2fa/passkey/options", "", nil)
	sig, err := a.Get(opt.Raw)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(sig, &doc))
	doc["response"].(map[string]any)["signature"] = "AAAA"
	broken, _ := json.Marshal(doc)
	r := c.rawBody("/auth/2fa/passkey", broken)
	require.Equal(t, 401, r.Status, string(r.Raw))
	require.Equal(t, "passkey_failed", r.JSON()["code"])
	require.Equal(t, 401, c.do("GET", "/me", "", nil).Status)

	// Passkey options without a pending password sign-in are refused.
	f := h.using(newBrowser(t))
	require.Equal(t, 401, f.do("POST", "/auth/2fa/passkey/options", "", nil).Status)
}

func TestRecoveryCodeForAPasskeyOnlyAccount(t *testing.T) {
	h := newPasskeyHarness(t)
	h.setup()
	a := passkeytest.New(passkeyOrigin)
	reg := h.addPasskey(a, "key")
	require.Equal(t, 201, reg.Status, string(reg.Raw))
	codes := codesOf(t, reg)

	// A recovery code also works for a passkey-only account, once.
	d := h.using(newBrowser(t))
	require.Equal(t, 401, d.login("admin@example.com", testPassword).Status)
	ok := d.do("POST", "/auth/2fa", "", map[string]any{"code": codes[0]})
	require.Equal(t, 200, ok.Status, string(ok.Raw))
	e := h.using(newBrowser(t))
	require.Equal(t, 401, e.login("admin@example.com", testPassword).Status)
	reuse := e.do("POST", "/auth/2fa", "", map[string]any{"code": codes[0]})
	require.Equal(t, 401, reuse.Status, string(reuse.Raw))
	require.Equal(t, "invalid_two_factor_code", reuse.JSON()["code"])
	// An authenticator-app code is not available to this account.
	nope := e.do("POST", "/auth/2fa", "", map[string]any{"code": "123456"})
	require.Equal(t, 401, nope.Status)

}

func TestPasskeySatisfiesRequiredTwoFactor(t *testing.T) {
	h := newPasskeyHarness(t)
	h.setup()
	ctx := context.Background()

	var ws uuid.UUID
	require.NoError(t, h.app.Pool.QueryRow(ctx, `SELECT id FROM workspaces ORDER BY created_at LIMIT 1`).Scan(&ws))
	hash, err := argon2id.CreateHash(testPassword, argon2id.DefaultParams)
	require.NoError(t, err)
	var member uuid.UUID
	require.NoError(t, h.app.Pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ('dev@example.com','Dev',$1) RETURNING id`, hash).Scan(&member))
	_, err = h.app.Pool.Exec(ctx, `INSERT INTO memberships (user_id, workspace_id, role) VALUES ($1,$2,'admin')`, member, ws)
	require.NoError(t, err)

	// The admin switches the requirement on with a passkey as their own second factor.
	adminKey := passkeytest.New(passkeyOrigin)
	early := h.do("PUT", "/teams/"+ws.String()+"/security", "", map[string]any{"require2fa": true})
	require.Equal(t, 409, early.Status, string(early.Raw))
	require.Equal(t, 201, h.addPasskey(adminKey, "admin key").Status)
	on := h.do("PUT", "/teams/"+ws.String()+"/security", "", map[string]any{"require2fa": true})
	require.Equal(t, 200, on.Status, string(on.Raw))
	require.Equal(t, float64(1), on.JSON()["enrolled"])

	// The member is blocked until they add a passkey; the passkey endpoints stay reachable.
	dev := h.using(newBrowser(t))
	require.Equal(t, 200, dev.login("dev@example.com", testPassword).Status)
	blocked := dev.do("GET", "/projects", "", nil)
	require.Equal(t, 403, blocked.Status, string(blocked.Raw))
	require.Equal(t, "two_factor_enrollment_required", blocked.JSON()["code"])
	devKey := passkeytest.New(passkeyOrigin)
	require.Equal(t, 201, dev.addPasskey(devKey, "dev key").Status)
	require.Equal(t, 200, dev.do("GET", "/projects", "", nil).Status)
	require.Equal(t, false, dev.do("GET", "/me/security", "", nil).JSON()["enrollmentNeeded"])

	// Signing in with the passkey alone satisfies the requirement in a new session.
	fresh := h.using(newBrowser(t))
	require.Equal(t, 200, fresh.passkeyLogin(devKey).Status)
	require.Equal(t, 200, fresh.do("GET", "/projects", "", nil).Status)

	// Removing the last passkey puts the block back.
	list := dev.do("GET", "/me/passkeys", "", nil).JSON()["passkeys"].([]any)
	id := list[0].(map[string]any)["id"].(string)
	require.Equal(t, 204, dev.do("DELETE", "/me/passkeys/"+id, "", map[string]any{"password": testPassword}).Status)
	var n int
	require.NoError(t, h.app.Pool.QueryRow(ctx, `SELECT count(*) FROM user_recovery_codes WHERE user_id = $1`, member).Scan(&n))
	require.Zero(t, n, "recovery codes go with the last second factor")
	other := h.using(newBrowser(t))
	require.Equal(t, 200, other.login("dev@example.com", testPassword).Status)
	require.Equal(t, 403, other.do("GET", "/projects", "", nil).Status)
}

func TestPasskeyManagement(t *testing.T) {
	h := newPasskeyHarness(t)
	clock := newTestClock()
	h.app.Auth.SetClock(clock.Now)
	h.setup()
	ctx := context.Background()
	k1, k2 := passkeytest.New(passkeyOrigin), passkeytest.New(passkeyOrigin)

	first := h.addPasskey(k1, "Laptop")
	require.Equal(t, 201, first.Status, string(first.Raw))
	second := h.addPasskey(k2, "Phone")
	require.Equal(t, 201, second.Status, string(second.Raw))
	require.Empty(t, second.JSON()["recoveryCodes"], "codes are only issued when none are left")
	id1 := first.JSON()["passkey"].(map[string]any)["id"].(string)
	id2 := second.JSON()["passkey"].(map[string]any)["id"].(string)

	// Both work for passwordless sign-in.
	require.Equal(t, 200, h.using(newBrowser(t)).passkeyLogin(k1).Status)
	require.Equal(t, 200, h.using(newBrowser(t)).passkeyLogin(k2).Status)

	// Rename (blank names are refused).
	require.Equal(t, 204, h.do("PATCH", "/me/passkeys/"+id2, "", map[string]any{"name": "Pixel"}).Status)
	require.Equal(t, 422, h.do("PATCH", "/me/passkeys/"+id2, "", map[string]any{"name": "   "}).Status)
	require.Equal(t, 404, h.do("PATCH", "/me/passkeys/"+uuid.NewString(), "", map[string]any{"name": "x"}).Status)
	list := h.do("GET", "/me/passkeys", "", nil).JSON()["passkeys"].([]any)
	require.Equal(t, "Laptop", list[0].(map[string]any)["name"])
	require.Equal(t, "Pixel", list[1].(map[string]any)["name"])

	// Removing needs the password and removes only that passkey.
	bad := h.do("DELETE", "/me/passkeys/"+id1, "", map[string]any{"password": "nope"})
	require.Equal(t, 422, bad.Status, string(bad.Raw))
	require.Equal(t, 204, h.do("DELETE", "/me/passkeys/"+id1, "", map[string]any{"password": testPassword}).Status)
	require.Equal(t, 404, h.do("DELETE", "/me/passkeys/"+id1, "", map[string]any{"password": testPassword}).Status)
	fresh := h.using(newBrowser(t))
	opt := fresh.do("POST", "/auth/passkey/options", "", nil)
	removed, err := k1.Get(opt.Raw)
	require.NoError(t, err)
	require.Equal(t, 401, fresh.rawBody("/auth/passkey/login", removed).Status, "a removed passkey no longer signs in")
	require.Equal(t, 200, h.using(newBrowser(t)).passkeyLogin(k2).Status)

	// Recovery codes remain while a passkey remains, and can be replaced with the
	// password (no authenticator app on this account).
	require.Equal(t, float64(10), h.do("GET", "/me/security", "", nil).JSON()["recoveryCodesLeft"])
	badRegen := h.do("POST", "/me/2fa/recovery-codes", "", map[string]any{"password": "nope"})
	require.Equal(t, 422, badRegen.Status, string(badRegen.Raw))
	regen := h.do("POST", "/me/2fa/recovery-codes", "", map[string]any{"password": testPassword})
	require.Equal(t, 200, regen.Status, string(regen.Raw))
	require.Len(t, codesOf(t, regen), 10)
	require.Equal(t, 422, h.do("POST", "/me/2fa/recovery-codes", "", map[string]any{}).Status)

	// TOTP and passkeys coexist; turning TOTP off keeps the passkey's codes.
	secret, _ := h.enrolTOTP(clock)
	require.Equal(t, 200, h.using(newBrowser(t)).passkeyLogin(k2).Status)
	clock.Advance(30 * time.Second)
	off := h.do("POST", "/me/2fa/disable", "", map[string]any{"code": totpCode(t, secret, clock.Now()), "password": testPassword})
	require.Equal(t, 204, off.Status, string(off.Raw))
	require.Equal(t, float64(10), h.do("GET", "/me/security", "", nil).JSON()["recoveryCodesLeft"])

	// Removing the last passkey removes the codes.
	require.Equal(t, 204, h.do("DELETE", "/me/passkeys/"+id2, "", map[string]any{"password": testPassword}).Status)
	require.Equal(t, float64(0), h.do("GET", "/me/security", "", nil).JSON()["recoveryCodesLeft"])

	// Every change is audited (names and ids, never key material).
	rows, err := h.app.Pool.Query(ctx, `SELECT action FROM audit_events WHERE action LIKE 'auth.passkey.%' ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var a string
		require.NoError(t, rows.Scan(&a))
		actions = append(actions, a)
	}
	require.Equal(t, []string{"auth.passkey.add", "auth.passkey.add", "auth.passkey.rename", "auth.passkey.remove", "auth.passkey.remove"}, actions)
}

func TestPasskeyOnlyAccountKeepsItsLastPasskey(t *testing.T) {
	h := newPasskeyHarness(t)
	h.setup()
	ctx := context.Background()
	a := passkeytest.New(passkeyOrigin)
	reg := h.addPasskey(a, "key")
	id := reg.JSON()["passkey"].(map[string]any)["id"].(string)

	// An account that has no password can only keep going with its passkey.
	_, err := h.app.Pool.Exec(ctx, `UPDATE users SET password_hash = '' WHERE email = 'admin@example.com'`)
	require.NoError(t, err)
	b := h.using(newBrowser(t))
	require.Equal(t, 200, b.passkeyLogin(a).Status)
	r := b.do("DELETE", "/me/passkeys/"+id, "", map[string]any{})
	require.Equal(t, 409, r.Status, string(r.Raw))
	require.Equal(t, "last_sign_in_method", r.JSON()["code"])
	// No password is asked for when there is none to ask for.
	opt := b.do("POST", "/me/passkeys/options", "", map[string]any{})
	require.Equal(t, 200, opt.Status, string(opt.Raw))
}

func TestPasswordConfirmationsShareThePerAccountWrongPasswordLimit(t *testing.T) {
	h := newPasskeyHarness(t)
	h.setup()
	for range 10 {
		r := h.do("POST", "/me/passkeys/options", "", map[string]any{"password": "not the password"})
		require.Equal(t, 422, r.Status, string(r.Raw))
	}
	// The account is locked out of every endpoint that re-checks the password,
	// even with the right one.
	for _, c := range []struct {
		method, path string
		body         map[string]any
	}{
		{"POST", "/me/passkeys/options", map[string]any{"password": testPassword}},
		{"DELETE", "/me/passkeys/" + uuid.NewString(), map[string]any{"password": testPassword}},
		{"POST", "/me/2fa/disable", map[string]any{"code": "123456", "password": testPassword}},
		{"POST", "/me/2fa/recovery-codes", map[string]any{"password": testPassword}},
	} {
		r := h.do(c.method, c.path, "", c.body)
		require.Equal(t, 429, r.Status, "%s %s: %s", c.method, c.path, string(r.Raw))
	}
}

func TestPasskeysNeedADomainAndHTTPS(t *testing.T) {
	// Without a public URL the request origin is 127.0.0.1: an IP address, which
	// WebAuthn does not accept as a relying party ID.
	h := newHarness(t)
	h.setup()
	r := h.do("POST", "/me/passkeys/options", "", map[string]any{"password": testPassword})
	require.Equal(t, 422, r.Status, string(r.Raw))
	require.Equal(t, "passkeys_unavailable", r.JSON()["code"])
	require.NotEmpty(t, h.do("GET", "/me/security", "", nil).JSON()["passkeysUnavailable"])
	require.Equal(t, 422, h.do("POST", "/auth/passkey/options", "", nil).Status)

	// Plain HTTP on a real domain is refused; HTTPS and localhost are fine.
	for url, want := range map[string]int{"http://nebula.example.com": 422, "https://nebula.example.com": 200, "http://localhost:8080": 200} {
		hh := newHarnessCfg(t, func(c *config.Config) { c.PublicURL = url })
		hh.setup()
		got := hh.do("POST", "/me/passkeys/options", "", map[string]any{"password": testPassword})
		require.Equal(t, want, got.Status, "%s: %s", url, string(got.Raw))
	}
}

func TestPasskeyManagementIsSessionOnly(t *testing.T) {
	h := newPasskeyHarness(t)
	h.setup()
	tok := h.createToken([]string{"admin"}, nil)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/me/passkeys", nil},
		{"POST", "/me/passkeys/options", map[string]any{}},
		{"PATCH", "/me/passkeys/" + uuid.NewString(), map[string]any{"name": "x"}},
		{"DELETE", "/me/passkeys/" + uuid.NewString(), map[string]any{}},
	} {
		r := h.do(c.method, c.path, tok, c.body)
		require.Equal(t, 403, r.Status, "%s %s: %s", c.method, c.path, string(r.Raw))
	}
}
