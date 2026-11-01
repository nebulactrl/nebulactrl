package app_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth/totptest"
)

// ---- helpers ----

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Now()} }
func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newBrowser(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// using returns a harness view that talks as another browser.
func (h *harness) using(b *http.Client) *harness {
	c := *h
	c.client = b
	return &c
}

func totpCode(t *testing.T, secretB32 string, at time.Time) string {
	t.Helper()
	b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretB32)
	require.NoError(t, err)
	return totptest.Code(b, at)
}

// enrolTOTP turns 2FA on for whoever h is signed in as and returns the base32
// secret and the recovery codes.
func (h *harness) enrolTOTP(clock *testClock) (string, []string) {
	h.t.Helper()
	s := h.do("POST", "/me/2fa/setup", "", nil)
	require.Equal(h.t, 200, s.Status, string(s.Raw))
	secret := s.JSON()["secret"].(string)
	require.Contains(h.t, s.JSON()["uri"], "otpauth://totp/")
	e := h.do("POST", "/me/2fa/enable", "", map[string]any{"code": totpCode(h.t, secret, clock.Now())})
	require.Equal(h.t, 200, e.Status, string(e.Raw))
	var codes []string
	for _, c := range e.JSON()["recoveryCodes"].([]any) {
		codes = append(codes, c.(string))
	}
	require.Len(h.t, codes, 10)
	return secret, codes
}

func (h *harness) login(email, password string) resp {
	return h.do("POST", "/auth/login", "", map[string]any{"email": email, "password": password})
}

// ---- TOTP ----

func TestTOTPLoginFlow(t *testing.T) {
	h := newHarness(t)
	clock := newTestClock()
	h.app.Auth.SetClock(clock.Now)
	h.setup()

	require.Equal(t, false, h.do("GET", "/me/security", "", nil).JSON()["totpEnabled"])
	secret, _ := h.enrolTOTP(clock)
	sec := h.do("GET", "/me/security", "", nil).JSON()
	require.Equal(t, true, sec["totpEnabled"])
	require.Equal(t, float64(10), sec["recoveryCodesLeft"])
	require.Equal(t, true, sec["sessionMfa"])
	// Enrolling twice is refused.
	require.Equal(t, 409, h.do("POST", "/me/2fa/setup", "", nil).Status)

	// Password alone no longer signs in.
	b2 := h.using(newBrowser(t))
	r := b2.login("admin@example.com", testPassword)
	require.Equal(t, 401, r.Status, string(r.Raw))
	require.Equal(t, "two_factor_required", r.JSON()["code"])
	require.Equal(t, 401, b2.do("GET", "/me", "", nil).Status, "a pending login is not a session")
	// The enrolment code's step is spent; the next step's code finishes the login.
	clock.Advance(30 * time.Second)
	ok := b2.do("POST", "/auth/2fa", "", map[string]any{"code": totpCode(t, secret, clock.Now())})
	require.Equal(t, 200, ok.Status, string(ok.Raw))
	require.Equal(t, "admin@example.com", ok.JSON()["email"])
	require.Equal(t, 200, b2.do("GET", "/me", "", nil).Status)
	require.Equal(t, true, b2.do("GET", "/me/security", "", nil).JSON()["sessionMfa"])

	// Wrong code, then replay of the code that was just used, then a fresh one.
	b3 := h.using(newBrowser(t))
	require.Equal(t, "two_factor_required", b3.login("admin@example.com", testPassword).JSON()["code"])
	bad := b3.do("POST", "/auth/2fa", "", map[string]any{"code": "000000"})
	require.Equal(t, 401, bad.Status)
	require.Equal(t, "invalid_two_factor_code", bad.JSON()["code"])
	replay := b3.do("POST", "/auth/2fa", "", map[string]any{"code": totpCode(t, secret, clock.Now())})
	require.Equal(t, 401, replay.Status, "an accepted code must not work twice")
	clock.Advance(30 * time.Second)
	require.Equal(t, 200, b3.do("POST", "/auth/2fa", "", map[string]any{"code": totpCode(t, secret, clock.Now())}).Status)

	// Disabling needs the password and a code.
	clock.Advance(30 * time.Second)
	wrongPw := h.do("POST", "/me/2fa/disable", "", map[string]any{"code": totpCode(t, secret, clock.Now()), "password": "not the password"})
	require.Equal(t, 422, wrongPw.Status)
	require.Equal(t, "invalid_password", wrongPw.JSON()["code"])
	off := h.do("POST", "/me/2fa/disable", "", map[string]any{"code": totpCode(t, secret, clock.Now()), "password": testPassword})
	require.Equal(t, 204, off.Status, string(off.Raw))
	require.Equal(t, false, h.do("GET", "/me/security", "", nil).JSON()["totpEnabled"])
	b4 := h.using(newBrowser(t))
	require.Equal(t, 200, b4.login("admin@example.com", testPassword).Status, "no 2FA step once disabled")

	var n int
	require.NoError(t, h.app.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE action IN ('auth.2fa.enable','auth.2fa.disable')`).Scan(&n))
	require.Equal(t, 2, n)
}

func TestRecoveryCodesAreSingleUse(t *testing.T) {
	h := newHarness(t)
	clock := newTestClock()
	h.app.Auth.SetClock(clock.Now)
	h.setup()
	_, codes := h.enrolTOTP(clock)

	b1 := h.using(newBrowser(t))
	require.Equal(t, "two_factor_required", b1.login("admin@example.com", testPassword).JSON()["code"])
	// Typed in upper case with spaces instead of dashes: still the same code.
	typed := strings.ToUpper(strings.ReplaceAll(codes[0], "-", " "))
	require.Equal(t, 200, b1.do("POST", "/auth/2fa", "", map[string]any{"code": typed}).Status)
	require.Equal(t, float64(9), h.do("GET", "/me/security", "", nil).JSON()["recoveryCodesLeft"])

	b2 := h.using(newBrowser(t))
	require.Equal(t, "two_factor_required", b2.login("admin@example.com", testPassword).JSON()["code"])
	reuse := b2.do("POST", "/auth/2fa", "", map[string]any{"code": codes[0]})
	require.Equal(t, 401, reuse.Status, "a recovery code works once")
	require.Equal(t, 200, b2.do("POST", "/auth/2fa", "", map[string]any{"code": codes[1]}).Status, "another code still works")

	// Regenerating invalidates every old code.
	clock.Advance(30 * time.Second)
	regen := h.do("POST", "/me/2fa/recovery-codes", "", map[string]any{"code": codes[2]})
	require.Equal(t, 200, regen.Status, string(regen.Raw))
	fresh := regen.JSON()["recoveryCodes"].([]any)
	require.Len(t, fresh, 10)
	require.Equal(t, float64(10), h.do("GET", "/me/security", "", nil).JSON()["recoveryCodesLeft"])
	b3 := h.using(newBrowser(t))
	require.Equal(t, "two_factor_required", b3.login("admin@example.com", testPassword).JSON()["code"])
	require.Equal(t, 401, b3.do("POST", "/auth/2fa", "", map[string]any{"code": codes[3]}).Status, "old codes are gone")
	require.Equal(t, 200, b3.do("POST", "/auth/2fa", "", map[string]any{"code": fresh[0].(string)}).Status)
}

func TestTOTPLockout(t *testing.T) {
	h := newHarness(t)
	clock := newTestClock()
	h.app.Auth.SetClock(clock.Now)
	h.setup()
	secret, _ := h.enrolTOTP(clock)
	clock.Advance(30 * time.Second)

	b := h.using(newBrowser(t))
	require.Equal(t, "two_factor_required", b.login("admin@example.com", testPassword).JSON()["code"])
	for i := 0; i < 5; i++ {
		require.Equal(t, 401, b.do("POST", "/auth/2fa", "", map[string]any{"code": "000000"}).Status)
	}
	// Five misses end this pending login ...
	locked := b.do("POST", "/auth/2fa", "", map[string]any{"code": totpCode(t, secret, clock.Now())})
	require.Equal(t, 429, locked.Status)
	require.Equal(t, "two_factor_locked", locked.JSON()["code"])
	// ... and the account's second-factor check is locked from any other session.
	b2 := h.using(newBrowser(t))
	require.Equal(t, "two_factor_required", b2.login("admin@example.com", testPassword).JSON()["code"])
	again := b2.do("POST", "/auth/2fa", "", map[string]any{"code": totpCode(t, secret, clock.Now())})
	require.Equal(t, 429, again.Status)
	require.Equal(t, "two_factor_locked", again.JSON()["code"])
}

func TestRequireTwoFactorForTeam(t *testing.T) {
	h := newHarness(t)
	clock := newTestClock()
	h.app.Auth.SetClock(clock.Now)
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

	dev := h.using(newBrowser(t))
	require.Equal(t, 200, dev.login("dev@example.com", testPassword).Status)
	devToken := dev.createToken([]string{"projects:read"}, nil)
	require.Equal(t, 200, h.do("GET", "/projects", devToken, nil).Status)

	// The person flipping the switch must have 2FA themselves.
	early := h.do("PUT", "/teams/"+ws.String()+"/security", "", map[string]any{"require2fa": true})
	require.Equal(t, 409, early.Status, string(early.Raw))
	require.Equal(t, "enrol_first", early.JSON()["code"])
	h.enrolTOTP(clock)
	on := h.do("PUT", "/teams/"+ws.String()+"/security", "", map[string]any{"require2fa": true})
	require.Equal(t, 200, on.Status, string(on.Raw))
	require.Equal(t, true, on.JSON()["require2fa"])
	require.Equal(t, float64(2), on.JSON()["members"])
	require.Equal(t, float64(1), on.JSON()["enrolled"])

	// The member without 2FA is blocked from acting, by session and by token ...
	blocked := dev.do("GET", "/projects", "", nil)
	require.Equal(t, 403, blocked.Status, string(blocked.Raw))
	require.Equal(t, "two_factor_enrollment_required", blocked.JSON()["code"])
	tokBlocked := h.do("GET", "/projects", devToken, nil)
	require.Equal(t, 403, tokBlocked.Status)
	require.Equal(t, "two_factor_enrollment_required", tokBlocked.JSON()["code"])
	// ... but can still reach what enrolment needs.
	require.Equal(t, 200, dev.do("GET", "/me", "", nil).Status)
	sec := dev.do("GET", "/me/security", "", nil).JSON()
	require.Equal(t, true, sec["enrollmentNeeded"])
	require.Len(t, sec["requiredByTeams"], 1)
	// The enrolled admin is unaffected.
	require.Equal(t, 200, h.do("GET", "/projects", "", nil).Status)

	// Enrolling lifts the block everywhere.
	dev.enrolTOTP(clock)
	require.Equal(t, 200, dev.do("GET", "/projects", "", nil).Status)
	require.Equal(t, 200, h.do("GET", "/projects", devToken, nil).Status)
	require.Equal(t, false, dev.do("GET", "/me/security", "", nil).JSON()["enrollmentNeeded"])

	// The switch only exists for the current team; another team's id is a 404.
	require.Equal(t, 404, h.do("PUT", "/teams/"+uuid.NewString()+"/security", "", map[string]any{"require2fa": false}).Status)
	off := h.do("PUT", "/teams/"+ws.String()+"/security", "", map[string]any{"require2fa": false})
	require.Equal(t, 200, off.Status)
	require.Equal(t, false, off.JSON()["require2fa"])

	var n int
	require.NoError(t, h.app.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'team.require_2fa'`).Scan(&n))
	require.Equal(t, 2, n)
}

// ---- fake identity provider ----

var (
	idpKeyOnce sync.Once
	idpKey     *rsa.PrivateKey
)

func testRSAKey(t *testing.T) *rsa.PrivateKey {
	idpKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		idpKey = k
	})
	return idpKey
}

type idpGrant struct {
	nonce       string
	challenge   string
	redirectURI string
	claims      map[string]any
}

// fakeIdP is an OpenID Connect issuer: discovery, JWKS and a token endpoint
// that signs RS256 ID tokens.
type fakeIdP struct {
	t            *testing.T
	srv          *httptest.Server
	key          *rsa.PrivateKey
	clientID     string
	clientSecret string
	mu           sync.Mutex
	grants       map[string]idpGrant
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	i := &fakeIdP{t: t, key: testRSAKey(t), clientID: "nebula-client", clientSecret: "idp-client-secret-xyz", grants: map[string]idpGrant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": i.srv.URL, "authorization_endpoint": i.srv.URL + "/authorize", "token_endpoint": i.srv.URL + "/token",
			"jwks_uri": i.srv.URL + "/jwks", "userinfo_endpoint": i.srv.URL + "/userinfo",
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": b64(i.key.N.Bytes()), "e": b64(big.NewInt(int64(i.key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	mux.HandleFunc("/token", i.token)
	i.srv = httptest.NewServer(mux)
	t.Cleanup(i.srv.Close)
	return i
}

func (i *fakeIdP) sign(claims map[string]any) string {
	hdr := b64([]byte(`{"alg":"RS256","kid":"k1","typ":"JWT"}`))
	pl, err := json.Marshal(claims)
	require.NoError(i.t, err)
	signing := hdr + "." + b64(pl)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, sum[:])
	require.NoError(i.t, err)
	return signing + "." + b64(sig)
}

func (i *fakeIdP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id, secret, hasBasic := r.BasicAuth()
	if !hasBasic {
		id, secret = r.FormValue("client_id"), r.FormValue("client_secret")
	} else { // oauth2 form-escapes Basic credentials
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	}
	i.mu.Lock()
	g, ok := i.grants[r.FormValue("code")]
	delete(i.grants, r.FormValue("code")) // single use
	i.mu.Unlock()
	fail := func(msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": msg})
	}
	switch {
	case !ok:
		fail("unknown code")
		return
	case id != i.clientID || secret != i.clientSecret:
		fail("bad client credentials")
		return
	case r.FormValue("redirect_uri") != g.redirectURI:
		fail("redirect_uri mismatch")
		return
	}
	sum := sha256.Sum256([]byte(r.FormValue("code_verifier")))
	if b64(sum[:]) != g.challenge {
		fail("PKCE verification failed")
		return
	}
	claims := map[string]any{
		"iss": i.srv.URL, "aud": i.clientID, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "nonce": g.nonce,
	}
	for k, v := range g.claims {
		claims[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "at-" + uuid.NewString(), "token_type": "Bearer", "expires_in": 3600, "id_token": i.sign(claims),
	})
}

// authorize plays the part of the user agent at the IdP: it takes the redirect
// the control plane produced, "authenticates" as claims and returns the callback
// path the IdP would redirect to.
func (i *fakeIdP) authorize(location string, claims map[string]any) string {
	i.t.Helper()
	u, err := url.Parse(location)
	require.NoError(i.t, err)
	require.True(i.t, strings.HasPrefix(location, i.srv.URL+"/authorize?"), location)
	q := u.Query()
	require.Equal(i.t, "code", q.Get("response_type"))
	require.Equal(i.t, i.clientID, q.Get("client_id"))
	require.Equal(i.t, "S256", q.Get("code_challenge_method"))
	require.NotEmpty(i.t, q.Get("state"))
	require.Contains(i.t, q.Get("scope"), "openid")
	code := "code-" + uuid.NewString()
	i.mu.Lock()
	i.grants[code] = idpGrant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirectURI: q.Get("redirect_uri"), claims: claims}
	i.mu.Unlock()
	cb, err := url.Parse(q.Get("redirect_uri"))
	require.NoError(i.t, err)
	return cb.Path + "?" + url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
}

// get performs a GET with b without following redirects.
func (h *harness) get(b *http.Client, path string) *http.Response {
	h.t.Helper()
	res, err := b.Get(h.srv.URL + path)
	require.NoError(h.t, err)
	_ = res.Body.Close()
	return res
}

// providerLogin walks the whole redirect dance as browser b and returns where
// the callback sent the browser.
func (h *harness) providerLogin(b *http.Client, idp *fakeIdP, providerID string, claims map[string]any, invite string) string {
	h.t.Helper()
	start := "/api/v1/auth/oidc/" + providerID + "/start"
	if invite != "" {
		start += "?invite=" + url.QueryEscape(invite)
	}
	res := h.get(b, start)
	require.Equal(h.t, http.StatusFound, res.StatusCode)
	cb := idp.authorize(res.Header.Get("Location"), claims)
	res = h.get(b, cb)
	require.Equal(h.t, http.StatusFound, res.StatusCode)
	return res.Header.Get("Location")
}

func (h *harness) createOIDCProvider(idp *fakeIdP, extra map[string]any) string {
	h.t.Helper()
	body := map[string]any{
		"kind": "oidc", "name": "Corp SSO", "issuerUrl": idp.srv.URL, "clientId": idp.clientID, "clientSecret": idp.clientSecret, "enabled": true,
	}
	for k, v := range extra {
		body[k] = v
	}
	r := h.do("POST", "/auth-providers", "", body)
	require.Equal(h.t, 201, r.Status, string(r.Raw))
	require.NotContains(h.t, string(r.Raw), idp.clientSecret, "the client secret is write-only")
	return r.JSON()["id"].(string)
}

func verified(sub, email string) map[string]any {
	return map[string]any{"sub": sub, "email": email, "email_verified": true, "name": "Someone"}
}

// ---- OIDC ----

func TestOIDCLoginLinksByVerifiedEmailOnly(t *testing.T) {
	idp := newFakeIdP(t)
	h := newHarness(t)
	h.setup()
	pid := h.createOIDCProvider(idp, nil)

	// Provider list: public view has no secrets; the admin view carries the redirect URI to register.
	pub := h.do("GET", "/auth/providers", "", nil)
	require.Equal(t, 200, pub.Status)
	require.Contains(t, string(pub.Raw), pid)
	require.NotContains(t, string(pub.Raw), idp.clientSecret)
	adminList := h.do("GET", "/auth-providers", "", nil)
	require.Equal(t, 200, adminList.Status)
	require.NotContains(t, string(adminList.Raw), idp.clientSecret)
	item := adminList.JSON()["items"].([]any)[0].(map[string]any)
	require.Equal(t, h.srv.URL+"/api/v1/auth/oidc/"+pid+"/callback", item["redirectUri"])

	// A verified email that matches an existing password account does NOT link
	// the identity or sign in: whoever registered that email first (an invite
	// sign-up takes any address) would share the account with its owner.
	b0 := newBrowser(t)
	require.Equal(t, "/login?error=account_exists", h.providerLogin(b0, idp, pid, verified("sub-1", "Admin@Example.com"), ""))
	require.Equal(t, 401, h.using(b0).do("GET", "/me", "", nil).Status)
	require.Empty(t, h.do("GET", "/me/security", "", nil).JSON()["identities"])

	// The owner links it from Account → Security, signed in.
	b := newBrowser(t)
	require.Equal(t, 200, h.using(b).login("admin@example.com", testPassword).Status)
	// ... with the account's password.
	noPw := h.using(b).do("POST", "/me/identities/link", "", map[string]any{"providerId": pid})
	require.Equal(t, 422, noPw.Status, string(noPw.Raw))
	require.Equal(t, "invalid_password", noPw.JSON()["code"])
	require.Equal(t, "/account/security#sso=linked", h.linkProvider(b, idp, pid, verified("sub-1", "Admin@Example.com")))
	me := h.using(b).do("GET", "/me", "", nil)
	require.Equal(t, 200, me.Status, string(me.Raw))
	require.Equal(t, "admin@example.com", me.JSON()["email"])
	ids := h.using(b).do("GET", "/me/security", "", nil).JSON()["identities"].([]any)
	require.Len(t, ids, 1)
	// Now the identity signs in on its own: the subject alone matches.
	b1 := newBrowser(t)
	require.Equal(t, "/", h.providerLogin(b1, idp, pid, map[string]any{"sub": "sub-1"}, ""))
	require.Equal(t, 200, h.using(b1).do("GET", "/me", "", nil).Status)
	// Linking it again changes nothing; another account cannot take the identity.
	require.Equal(t, "/account/security#sso=linked", h.linkProvider(b, idp, pid, verified("sub-1", "Admin@Example.com")))
	require.Len(t, h.using(b).do("GET", "/me/security", "", nil).JSON()["identities"], 1)
	// A link started by a signed-out browser is refused outright.
	out := h.using(newBrowser(t)).do("POST", "/me/identities/link", "", map[string]any{"providerId": pid, "password": testPassword})
	require.Equal(t, 401, out.Status, string(out.Raw))

	// An unverified email must never link to an account (account takeover).
	b2 := newBrowser(t)
	loc := h.providerLogin(b2, idp, pid, map[string]any{"sub": "sub-evil", "email": "admin@example.com", "email_verified": false}, "")
	require.Equal(t, "/login?error=email_not_verified", loc)
	require.Equal(t, 401, h.using(b2).do("GET", "/me", "", nil).Status)
	// Nor with no email claim at all.
	loc = h.providerLogin(newBrowser(t), idp, pid, map[string]any{"sub": "sub-none"}, "")
	require.Equal(t, "/login?error=email_not_verified", loc)
	// Microsoft Entra ID has no email_verified; its xms_edov claim says the
	// tenant admin verified the domain. Only that, with an email, counts: the
	// second login clears the verified-email check and then stops at the
	// existing account, which no provider login ever links to by email.
	loc = h.providerLogin(newBrowser(t), idp, pid, map[string]any{"sub": "sub-entra-unverified", "email": "admin@example.com", "xms_edov": false}, "")
	require.Equal(t, "/login?error=email_not_verified", loc)
	loc = h.providerLogin(newBrowser(t), idp, pid, map[string]any{"sub": "sub-entra", "email": "admin@example.com", "xms_edov": true}, "")
	require.Equal(t, "/login?error=account_exists", loc)

	// A state that is not the one we issued is refused.
	b3 := newBrowser(t)
	res := h.get(b3, "/api/v1/auth/oidc/"+pid+"/start")
	cb := idp.authorize(res.Header.Get("Location"), verified("sub-1", "admin@example.com"))
	forged := strings.Split(cb, "&state=")[0] + "&state=forged"
	res = h.get(b3, forged)
	require.Equal(t, "/login?error=invalid_oidc_state", res.Header.Get("Location"))

	// An ID token minted for another audience or bound to another nonce is refused.
	b4 := newBrowser(t)
	require.Equal(t, "/login?error=oidc_invalid_token", h.providerLogin(b4, idp, pid, map[string]any{"sub": "sub-1", "aud": "someone-else"}, ""))
	b5 := newBrowser(t)
	require.Equal(t, "/login?error=oidc_invalid_token", h.providerLogin(b5, idp, pid, map[string]any{"sub": "sub-1", "nonce": "replayed"}, ""))

	// Unlinking is refused for the last way in only; this account has a password.
	unlink := h.using(b).do("DELETE", "/me/identities/"+ids[0].(map[string]any)["id"].(string), "", nil)
	require.Equal(t, 204, unlink.Status, string(unlink.Raw))
}

func TestOIDCAllowedDomains(t *testing.T) {
	idp := newFakeIdP(t)
	h := newHarness(t)
	h.setup()
	pid := h.createOIDCProvider(idp, map[string]any{"allowedDomains": []string{"@Corp.Example"}})

	// admin@example.com is an existing user but not from an allowed domain.
	loc := h.providerLogin(newBrowser(t), idp, pid, verified("sub-1", "admin@example.com"), "")
	require.Equal(t, "/login?error=domain_not_allowed", loc)

	// Widen the list without re-entering the secret (empty secret keeps it).
	upd := h.do("PUT", "/auth-providers/"+pid, "", map[string]any{
		"kind": "oidc", "name": "Corp SSO", "issuerUrl": idp.srv.URL, "clientId": idp.clientID,
		"allowedDomains": []string{"example.com"}, "enabled": true,
	})
	require.Equal(t, 200, upd.Status, string(upd.Raw))
	// The domain rule also applies to linking: the owner links from Account → Security.
	b := newBrowser(t)
	require.Equal(t, 200, h.using(b).login("admin@example.com", testPassword).Status)
	require.Equal(t, "/account/security#sso=linked", h.linkProvider(b, idp, pid, verified("sub-1", "admin@example.com")))
	b2 := newBrowser(t)
	require.Equal(t, "/", h.providerLogin(b2, idp, pid, verified("sub-1", "admin@example.com"), ""))
	require.Equal(t, 200, h.using(b2).do("GET", "/me", "", nil).Status)

	// Disabled providers cannot be used.
	dis := h.do("PUT", "/auth-providers/"+pid, "", map[string]any{
		"kind": "oidc", "name": "Corp SSO", "issuerUrl": idp.srv.URL, "clientId": idp.clientID, "enabled": false,
	})
	require.Equal(t, 200, dis.Status)
	res := h.get(newBrowser(t), "/api/v1/auth/oidc/"+pid+"/start")
	require.Equal(t, "/login?error=oidc_failed", res.Header.Get("Location"))
	require.NotContains(t, string(h.do("GET", "/auth/providers", "", nil).Raw), pid)
}

func TestOIDCProviderAdminValidation(t *testing.T) {
	idp := newFakeIdP(t)
	h := newHarness(t)
	h.setup()
	// An issuer that serves no discovery document is rejected before saving.
	bad := h.do("POST", "/auth-providers", "", map[string]any{
		"kind": "oidc", "name": "Broken", "issuerUrl": idp.srv.URL + "/nope", "clientId": "x", "clientSecret": "y", "enabled": true,
	})
	require.Equal(t, 422, bad.Status)
	require.Equal(t, "issuer_discovery_failed", bad.JSON()["code"])
	// Plain http is only for localhost.
	plain := h.do("POST", "/auth-providers", "", map[string]any{
		"kind": "oidc", "name": "Plain", "issuerUrl": "http://idp.example.com", "clientId": "x", "clientSecret": "y", "enabled": true,
	})
	require.Equal(t, 422, plain.Status)
	require.Equal(t, "invalid_issuer", plain.JSON()["code"])
	// The secret is mandatory on create.
	nosecret := h.do("POST", "/auth-providers", "", map[string]any{
		"kind": "oidc", "name": "NoSecret", "issuerUrl": idp.srv.URL, "clientId": "x", "enabled": true,
	})
	require.Equal(t, 422, nosecret.Status)

	// Only the instance admin manages providers and sign-in settings.
	pid := h.createOIDCProvider(idp, nil)
	other := h.using(newBrowser(t))
	hash, err := argon2id.CreateHash(testPassword, argon2id.DefaultParams)
	require.NoError(t, err)
	var uid, ws uuid.UUID
	ctx := context.Background()
	require.NoError(t, h.app.Pool.QueryRow(ctx, `SELECT id FROM workspaces ORDER BY created_at LIMIT 1`).Scan(&ws))
	require.NoError(t, h.app.Pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ('second@example.com','Second',$1) RETURNING id`, hash).Scan(&uid))
	_, err = h.app.Pool.Exec(ctx, `INSERT INTO memberships (user_id, workspace_id, role) VALUES ($1,$2,'owner')`, uid, ws)
	require.NoError(t, err)
	require.Equal(t, 200, other.login("second@example.com", testPassword).Status)
	require.Equal(t, 403, other.do("GET", "/auth-providers", "", nil).Status)
	require.Equal(t, 403, other.do("DELETE", "/auth-providers/"+pid, "", nil).Status)
	require.Equal(t, 403, other.do("PUT", "/auth-settings", "", map[string]any{"allowOpenSignup": true}).Status)

	// Deleting a provider removes it.
	require.Equal(t, 204, h.do("DELETE", "/auth-providers/"+pid, "", nil).Status)
	require.Equal(t, 404, h.do("DELETE", "/auth-providers/"+pid, "", nil).Status)
}

func TestTestIssuerEndpointAndPreassignedProviderID(t *testing.T) {
	idp := newFakeIdP(t)
	h := newHarness(t)
	h.setup()

	ok := h.do("POST", "/instance/sign-in-providers/test", "", map[string]any{"issuerUrl": idp.srv.URL})
	require.Equal(t, 200, ok.Status, string(ok.Raw))
	require.Equal(t, true, ok.JSON()["ok"])
	require.Equal(t, idp.srv.URL, ok.JSON()["issuer"])
	require.Equal(t, idp.srv.URL+"/authorize", ok.JSON()["authorizationEndpoint"])

	// A wrong issuer is a 200 with a specific code, not a transport error.
	bad := h.do("POST", "/instance/sign-in-providers/test", "", map[string]any{"issuerUrl": idp.srv.URL + "/nope"})
	require.Equal(t, 200, bad.Status, string(bad.Raw))
	require.Equal(t, false, bad.JSON()["ok"])
	require.Equal(t, "discovery_not_found", bad.JSON()["code"])
	require.NotEmpty(t, bad.JSON()["message"])
	plain := h.do("POST", "/instance/sign-in-providers/test", "", map[string]any{"issuerUrl": "http://idp.example.com"})
	require.Equal(t, "invalid_issuer", plain.JSON()["code"])

	// A provider can be created under an ID chosen beforehand, so its callback
	// URL is known (and registered at the identity provider) before saving.
	id := uuid.New().String()
	created := h.do("POST", "/auth-providers", "", map[string]any{
		"id": id, "kind": "oidc", "name": "Corp SSO", "issuerUrl": idp.srv.URL, "clientId": idp.clientID, "clientSecret": idp.clientSecret, "enabled": true,
	})
	require.Equal(t, 201, created.Status, string(created.Raw))
	require.Equal(t, id, created.JSON()["id"])
	require.True(t, strings.HasSuffix(created.JSON()["redirectUri"].(string), "/api/v1/auth/oidc/"+id+"/callback"))
	again := h.do("POST", "/auth-providers", "", map[string]any{
		"id": id, "kind": "oidc", "name": "Again", "issuerUrl": idp.srv.URL, "clientId": "x", "clientSecret": "y", "enabled": true,
	})
	require.Equal(t, 409, again.Status, string(again.Raw))
	require.Equal(t, "provider_id_taken", again.JSON()["code"])

	// Only the instance admin may probe issuers: the server would otherwise fetch
	// URLs for anyone.
	other := h.using(newBrowser(t))
	hash, err := argon2id.CreateHash(testPassword, argon2id.DefaultParams)
	require.NoError(t, err)
	var uid, ws uuid.UUID
	ctx := context.Background()
	require.NoError(t, h.app.Pool.QueryRow(ctx, `SELECT id FROM workspaces ORDER BY created_at LIMIT 1`).Scan(&ws))
	require.NoError(t, h.app.Pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ('second@example.com','Second',$1) RETURNING id`, hash).Scan(&uid))
	_, err = h.app.Pool.Exec(ctx, `INSERT INTO memberships (user_id, workspace_id, role) VALUES ($1,$2,'owner')`, uid, ws)
	require.NoError(t, err)
	require.Equal(t, 200, other.login("second@example.com", testPassword).Status)
	require.Equal(t, 403, other.do("POST", "/instance/sign-in-providers/test", "", map[string]any{"issuerUrl": idp.srv.URL}).Status)
	anon := h.using(newBrowser(t))
	require.Equal(t, 401, anon.do("POST", "/instance/sign-in-providers/test", "", map[string]any{"issuerUrl": idp.srv.URL}).Status)
}

// signupHooks stands in for the teams module.
type signupHooks struct {
	pool     *pgxpool.Pool
	ws       uuid.UUID
	mu       sync.Mutex
	accepted []string
	open     []uuid.UUID
}

func (s *signupHooks) CheckInvite(_ context.Context, token, _ string) error {
	if token != "good-invite" {
		return errors.New("no such invite")
	}
	return nil
}

func (s *signupHooks) AcceptInvite(ctx context.Context, tx pgx.Tx, token string, userID uuid.UUID, _ string) error {
	if token != "good-invite" {
		return errors.New("no such invite")
	}
	s.mu.Lock()
	s.accepted = append(s.accepted, token)
	s.mu.Unlock()
	_, err := tx.Exec(ctx, `INSERT INTO memberships (user_id, workspace_id, role) VALUES ($1,$2,'developer')`, userID, s.ws)
	return err
}

func (s *signupHooks) OpenSignup(ctx context.Context, tx pgx.Tx, userID uuid.UUID, _ string) error {
	s.mu.Lock()
	s.open = append(s.open, userID)
	s.mu.Unlock()
	_, err := tx.Exec(ctx, `INSERT INTO memberships (user_id, workspace_id, role) VALUES ($1,$2,'viewer')`, userID, s.ws)
	return err
}

func TestOIDCSignupNeedsInviteOrOpenSignup(t *testing.T) {
	idp := newFakeIdP(t)
	h := newHarness(t)
	h.setup()
	ctx := context.Background()
	hooks := &signupHooks{pool: h.app.Pool}
	require.NoError(t, h.app.Pool.QueryRow(ctx, `SELECT id FROM workspaces ORDER BY created_at LIMIT 1`).Scan(&hooks.ws))
	h.app.Auth.SetSignupHooks(hooks)
	pid := h.createOIDCProvider(idp, nil)

	require.Equal(t, false, h.do("GET", "/auth-settings", "", nil).JSON()["allowOpenSignup"], "open sign-up defaults to off")
	// Off by default: a stranger is refused and no user is created.
	loc := h.providerLogin(newBrowser(t), idp, pid, verified("sub-new", "new@corp.example"), "")
	require.Equal(t, "/login?error=signup_disabled", loc)
	var n int
	require.NoError(t, h.app.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE email='new@corp.example'`).Scan(&n))
	require.Equal(t, 0, n)

	// An invite lets a new person in even while open sign-up is off; a bad one does not.
	b := newBrowser(t)
	require.Equal(t, "/", h.providerLogin(b, idp, pid, verified("sub-inv", "invited@corp.example"), "good-invite"))
	require.Equal(t, 200, h.using(b).do("GET", "/me", "", nil).Status)
	require.Equal(t, []string{"good-invite"}, hooks.accepted)
	require.Equal(t, "/login?error=invite_invalid", h.providerLogin(newBrowser(t), idp, pid, verified("sub-x", "x@corp.example"), "forged-invite"))

	// The new account has no password: it cannot be used with password login.
	var pw string
	require.NoError(t, h.app.Pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE email='invited@corp.example'`).Scan(&pw))
	require.Equal(t, "", pw)
	require.Equal(t, 401, h.using(newBrowser(t)).login("invited@corp.example", "").Status)
	require.Equal(t, 401, h.using(newBrowser(t)).login("invited@corp.example", "anything at all 123").Status)

	// An existing account that follows an invite link is sent to the accept page.
	loc = h.providerLogin(newBrowser(t), idp, pid, verified("sub-inv", "invited@corp.example"), "good-invite")
	require.Equal(t, "/invite/good-invite", loc)

	// With open sign-up on, a stranger gets an account.
	on := h.do("PUT", "/auth-settings", "", map[string]any{"allowOpenSignup": true})
	require.Equal(t, 200, on.Status, string(on.Raw))
	b2 := newBrowser(t)
	require.Equal(t, "/", h.providerLogin(b2, idp, pid, verified("sub-open", "open@corp.example"), ""))
	require.Len(t, hooks.open, 1)
	require.Equal(t, 200, h.using(b2).do("GET", "/me", "", nil).Status)
	// The provider's allowed-domain rule still applies to sign-up.
	upd := h.do("PUT", "/auth-providers/"+pid, "", map[string]any{
		"kind": "oidc", "name": "Corp SSO", "issuerUrl": idp.srv.URL, "clientId": idp.clientID, "allowedDomains": []string{"corp.example"}, "enabled": true,
	})
	require.Equal(t, 200, upd.Status)
	require.Equal(t, "/login?error=domain_not_allowed", h.providerLogin(newBrowser(t), idp, pid, verified("sub-o", "someone@elsewhere.example"), ""))
}

func TestOIDCTrustedMFASkipsTheSecondFactor(t *testing.T) {
	idp := newFakeIdP(t)
	h := newHarness(t)
	clock := newTestClock()
	h.app.Auth.SetClock(clock.Now)
	h.setup()
	secret, _ := h.enrolTOTP(clock)
	clock.Advance(30 * time.Second)
	pid := h.createOIDCProvider(idp, nil) // trustMfa defaults to off
	// The owner links the identity from Account → Security (a provider login never
	// links by email), signed in with the password and the authenticator code.
	bl := newBrowser(t)
	require.Equal(t, 401, h.using(bl).login("admin@example.com", testPassword).Status)
	require.Equal(t, 200, h.using(bl).do("POST", "/auth/2fa", "", map[string]any{"code": totpCode(t, secret, clock.Now())}).Status)
	require.Equal(t, "/account/security#sso=linked", h.linkProvider(bl, idp, pid, verified("sub-1", "admin@example.com")))
	clock.Advance(30 * time.Second) // the next code is a new step
	claims := func(amr ...string) map[string]any {
		c := verified("sub-1", "admin@example.com")
		if len(amr) > 0 {
			c["amr"] = amr
		}
		return c
	}
	provider := func(trust bool) map[string]any {
		return map[string]any{"kind": "oidc", "name": "Corp SSO", "issuerUrl": idp.srv.URL, "clientId": idp.clientID, "trustMfa": trust, "enabled": true}
	}

	// Not trusted (default): the IdP's amr is ignored and our own 2FA step applies.
	b1 := newBrowser(t)
	require.Equal(t, "/login?step=2fa", h.providerLogin(b1, idp, pid, claims("mfa"), ""))
	require.Equal(t, 401, h.using(b1).do("GET", "/me", "", nil).Status, "the login is pending, not a session")
	bad := h.using(b1).do("POST", "/auth/2fa", "", map[string]any{"code": "111111"})
	require.Equal(t, 401, bad.Status)
	ok := h.using(b1).do("POST", "/auth/2fa", "", map[string]any{"code": totpCode(t, secret, clock.Now())})
	require.Equal(t, 200, ok.Status, string(ok.Raw))
	require.Equal(t, 200, h.using(b1).do("GET", "/me", "", nil).Status)

	// Trusted, and the IdP asserts MFA: no local step, and the session counts as 2FA-verified.
	require.Equal(t, 200, h.do("PUT", "/auth-providers/"+pid, "", provider(true)).Status)
	b2 := newBrowser(t)
	require.Equal(t, "/", h.providerLogin(b2, idp, pid, claims("pwd", "mfa"), ""))
	require.Equal(t, 200, h.using(b2).do("GET", "/me", "", nil).Status)
	require.Equal(t, true, h.using(b2).do("GET", "/me/security", "", nil).JSON()["sessionMfa"])

	// Trusted but the IdP did not assert MFA: the local step still applies.
	b3 := newBrowser(t)
	require.Equal(t, "/login?step=2fa", h.providerLogin(b3, idp, pid, claims("pwd"), ""))
}

// ---- GitHub ----

func TestGitHubLogin(t *testing.T) {
	var mu sync.Mutex
	emails := []map[string]any{{"email": "admin@example.com", "primary": true, "verified": true}, {"email": "other@example.com", "primary": false, "verified": true}}
	codes := map[string]bool{}
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		_, secret, hasBasic := r.BasicAuth()
		if !hasBasic {
			secret = r.FormValue("client_secret")
		}
		ok := codes[r.FormValue("code")] && secret == "gh-secret"
		if ok {
			delete(codes, r.FormValue("code"))
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"gho_token","token_type":"bearer","scope":"read:user,user:email"}`))
	})
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer gho_token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			h(w, r)
		}
	}
	mux.HandleFunc("/api/v3/user", authed(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":4242,"login":"octo","name":"Octo Cat"}`))
	}))
	mux.HandleFunc("/api/v3/user/emails", authed(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(emails)
	}))
	gh := httptest.NewServer(mux)
	t.Cleanup(gh.Close)

	h := newHarness(t)
	h.setup()
	create := h.do("POST", "/auth-providers", "", map[string]any{
		"kind": "github", "name": "GitHub", "issuerUrl": gh.URL, "clientId": "gh-client", "clientSecret": "gh-secret", "enabled": true, "trustMfa": true,
	})
	require.Equal(t, 201, create.Status, string(create.Raw))
	require.Equal(t, false, create.JSON()["trustMfa"], "GitHub cannot assert MFA, so trust is never on")
	pid := create.JSON()["id"].(string)

	walk := func(b *http.Client, authorize string) string {
		loc, err := url.Parse(authorize)
		require.NoError(t, err)
		require.Equal(t, gh.URL+"/login/oauth/authorize", loc.Scheme+"://"+loc.Host+loc.Path)
		require.Contains(t, loc.Query().Get("scope"), "user:email")
		code := "gh-" + uuid.NewString()
		mu.Lock()
		codes[code] = true
		mu.Unlock()
		cb, err := url.Parse(loc.Query().Get("redirect_uri"))
		require.NoError(t, err)
		res := h.get(b, cb.Path+"?"+url.Values{"code": {code}, "state": {loc.Query().Get("state")}}.Encode())
		require.Equal(t, http.StatusFound, res.StatusCode)
		return res.Header.Get("Location")
	}

	login := func(b *http.Client) string {
		res := h.get(b, "/api/v1/auth/oidc/"+pid+"/start")
		require.Equal(t, http.StatusFound, res.StatusCode)
		return walk(b, res.Header.Get("Location"))
	}

	// GitHub's primary verified email matches the existing account, but signing in
	// with GitHub never links it by email.
	require.Equal(t, "/login?error=account_exists", login(newBrowser(t)))
	// The owner links it from Account → Security, signed in with the password.
	b := newBrowser(t)
	require.Equal(t, 200, h.using(b).login("admin@example.com", testPassword).Status)
	start := h.using(b).do("POST", "/me/identities/link", "", map[string]any{"providerId": pid, "password": testPassword})
	require.Equal(t, 200, start.Status, string(start.Raw))
	require.Equal(t, "/account/security#sso=linked", walk(b, start.JSON()["url"].(string)))
	bsso := newBrowser(t)
	require.Equal(t, "/", login(bsso), "the linked GitHub id signs in")
	require.Equal(t, 200, h.using(bsso).do("GET", "/me", "", nil).Status)
	me := h.using(b).do("GET", "/me", "", nil)
	require.Equal(t, 200, me.Status)
	require.Equal(t, "admin@example.com", me.JSON()["email"])
	ids := h.using(b).do("GET", "/me/security", "", nil).JSON()["identities"].([]any)
	require.Len(t, ids, 1)
	require.Equal(t, "github", ids[0].(map[string]any)["providerKind"])

	// A primary email that GitHub has not verified is not trusted: with the account
	// already linked by id the login works, but a fresh account would not link.
	mu.Lock()
	emails = []map[string]any{{"email": "victim@example.com", "primary": true, "verified": false}}
	mu.Unlock()
	require.Equal(t, "/", login(newBrowser(t)), "known GitHub id still signs in")
	_, err := h.app.Pool.Exec(context.Background(), `DELETE FROM user_identities`)
	require.NoError(t, err)
	require.Equal(t, "/login?error=email_not_verified", login(newBrowser(t)))
}

// linkProvider walks the Account → Security link flow as browser b (signed in),
// and returns where the callback sent the browser.
func (h *harness) linkProvider(b *http.Client, idp *fakeIdP, providerID string, claims map[string]any) string {
	h.t.Helper()
	start := h.using(b).do("POST", "/me/identities/link", "", map[string]any{"providerId": providerID, "password": testPassword})
	require.Equal(h.t, 200, start.Status, string(start.Raw))
	cb := idp.authorize(start.JSON()["url"].(string), claims)
	res := h.get(b, cb)
	require.Equal(h.t, http.StatusFound, res.StatusCode)
	return res.Header.Get("Location")
}
