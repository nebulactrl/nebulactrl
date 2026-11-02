package auth_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

var (
	idpKeyOnce sync.Once
	idpKeys    [2]*rsa.PrivateKey
)

func idpKey(t *testing.T, i int) *rsa.PrivateKey {
	t.Helper()
	idpKeyOnce.Do(func() {
		for n := range idpKeys {
			k, err := rsa.GenerateKey(rand.Reader, 2048)
			require.NoError(t, err)
			idpKeys[n] = k
		}
	})
	return idpKeys[i]
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// fakeIdP is an OpenID Connect provider: discovery, JWKS, token and userinfo
// endpoints. The token endpoint checks the client credentials and the PKCE
// verifier the flow sent.
type fakeIdP struct {
	t        *testing.T
	srv      *httptest.Server
	key      *rsa.PrivateKey
	clientID string
	secret   string

	mu          sync.Mutex
	nonce       string
	challenge   string
	claims      map[string]any // merged over the defaults; a nil value removes the claim
	signWith    *rsa.PrivateKey
	omitIDToken bool
	tokenStatus int
	userinfo    map[string]any
	tokenCalls  int
	verifierOK  bool
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	i := &fakeIdP{t: t, key: idpKey(t, 0), clientID: "nebula-client", secret: "nebula-secret", tokenStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": i.srv.URL, "authorization_endpoint": i.srv.URL + "/authorize", "token_endpoint": i.srv.URL + "/token",
			"jwks_uri": i.srv.URL + "/jwks", "userinfo_endpoint": i.srv.URL + "/userinfo",
			"id_token_signing_alg_values_supported": []string{"RS256"}, "response_types_supported": []string{"code"},
			"subject_types_supported": []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": b64(i.key.N.Bytes()), "e": b64(big.NewInt(int64(i.key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", i.token)
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || i.userinfo == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(i.userinfo)
	})
	i.srv = httptest.NewServer(mux)
	t.Cleanup(i.srv.Close)
	return i
}

func (i *fakeIdP) token(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.tokenCalls++
	_ = r.ParseForm()
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	i.verifierOK = b64(sum[:]) == i.challenge
	if i.tokenStatus != http.StatusOK || id != i.clientID || secret != i.secret || r.PostForm.Get("code") != "code-ok" || !i.verifierOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	claims := map[string]any{
		"iss": i.srv.URL, "sub": "sub-1", "aud": i.clientID, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"nonce": i.nonce, "email": "new.user@example.com", "email_verified": true, "name": "New User",
	}
	for k, v := range i.claims {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	out := map[string]any{"access_token": "at-1", "token_type": "Bearer", "expires_in": 3600}
	if !i.omitIDToken {
		key := i.key
		if i.signWith != nil {
			key = i.signWith
		}
		out["id_token"] = i.sign(claims, key)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (i *fakeIdP) sign(claims map[string]any, key *rsa.PrivateKey) string {
	hdr, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": "k1"})
	body, _ := json.Marshal(claims)
	signing := b64(hdr) + "." + b64(body)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	require.NoError(i.t, err)
	return signing + "." + b64(sig)
}

func (i *fakeIdP) set(claims map[string]any) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.claims = claims
}

// authorize reads what the flow asked the provider for (and asserts it is a
// well-formed authorization-code request with PKCE), returning the state.
func (i *fakeIdP) authorize(authURL string) string {
	i.t.Helper()
	u, err := url.Parse(authURL)
	require.NoError(i.t, err)
	require.Equal(i.t, i.srv.URL+"/authorize", u.Scheme+"://"+u.Host+u.Path)
	q := u.Query()
	require.Equal(i.t, "code", q.Get("response_type"))
	require.Equal(i.t, i.clientID, q.Get("client_id"))
	require.Contains(i.t, strings.Fields(q.Get("scope")), "openid")
	require.Contains(i.t, strings.Fields(q.Get("scope")), "email")
	require.Equal(i.t, "S256", q.Get("code_challenge_method"))
	require.NotEmpty(i.t, q.Get("code_challenge"))
	require.NotEmpty(i.t, q.Get("state"))
	require.NotEmpty(i.t, q.Get("nonce"))
	i.mu.Lock()
	defer i.mu.Unlock()
	i.nonce, i.challenge = q.Get("nonce"), q.Get("code_challenge")
	return q.Get("state")
}

// oidcProvider inserts an OIDC provider row that points at i.
func (r *gpaRig) oidcProvider(i *fakeIdP, o gpaProviderOpts) db.AuthProvider {
	r.t.Helper()
	o.Kind, o.IssuerURL = auth.KindOIDC, i.srv.URL
	return r.provider(o)
}

func (r *gpaRig) allowOpenSignup(on bool) {
	r.t.Helper()
	require.NoError(r.t, r.inTx(func(tx pgx.Tx) error {
		_, err := r.svc.SetAuthSettings(context.Background(), tx, auth.AuthSettings{AllowOpenSignup: on})
		return err
	}))
}

// login runs the whole provider flow in one session.
func (r *gpaRig) login(ctx context.Context, i *fakeIdP, p db.AuthProvider, invite string) (*auth.FederatedResult, error) {
	r.t.Helper()
	authURL, err := r.svc.BeginProviderLogin(ctx, r.sm, p.ID, gpaBase, invite)
	require.NoError(r.t, err)
	state := i.authorize(authURL)
	return r.svc.CompleteProviderLogin(ctx, r.sm, p.ID, gpaBase, state, "code-ok")
}

func (r *gpaRig) identityOf(p db.AuthProvider, subject string) (db.UserIdentity, bool) {
	r.t.Helper()
	row, err := r.q.GetUserIdentity(context.Background(), db.GetUserIdentityParams{ProviderID: p.ID, Subject: subject})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.UserIdentity{}, false
	}
	require.NoError(r.t, err)
	return row, true
}

func (r *gpaRig) exec(sql string, args ...any) {
	r.t.Helper()
	_, err := r.pool.Exec(context.Background(), sql, args...)
	require.NoError(r.t, err)
}

func (r *gpaRig) userCount() int {
	r.t.Helper()
	var n int
	require.NoError(r.t, r.pool.QueryRow(context.Background(), `SELECT count(*) FROM users`).Scan(&n))
	return n
}

func TestOIDCOpenSignupCreatesTheUserOnceAndLaterLoginsReuseIt(t *testing.T) {
	r := newGpaRig(t)
	idp := newFakeIdP(t)
	p := r.oidcProvider(idp, gpaProviderOpts{})
	r.allowOpenSignup(true)
	ctx := r.session()

	res, err := r.login(ctx, idp, p, "")
	require.NoError(t, err)
	require.True(t, res.Created)
	require.False(t, res.NeedsSecondFactor)
	require.False(t, res.TrustedMFA)
	require.False(t, res.LinkOnly)
	require.Equal(t, "new.user@example.com", res.User.Email)
	require.Equal(t, "New User", res.User.Name)
	require.Equal(t, []uuid.UUID{res.User.ID}, r.hooks.opened, "the team hook ran for the new user")
	require.Equal(t, []string{"New User"}, r.hooks.openName)
	link, ok := r.identityOf(p, "sub-1")
	require.True(t, ok)
	require.Equal(t, res.User.ID, link.UserID)
	require.True(t, idp.verifierOK, "the PKCE verifier matched the challenge")

	// The same identity again signs the same user in; nobody new, no new team.
	before := r.userCount()
	idp.set(map[string]any{"email": "renamed@example.com", "name": "Other"})
	res, err = r.login(r.session(), idp, p, "")
	require.NoError(t, err)
	require.False(t, res.Created)
	require.Equal(t, link.UserID, res.User.ID)
	require.Equal(t, "new.user@example.com", res.User.Email, "the account keeps its email; the provider cannot rename it")
	require.Equal(t, before, r.userCount())
	require.Len(t, r.hooks.opened, 1)
}

func TestOIDCSignupRules(t *testing.T) {
	r := newGpaRig(t)
	idp := newFakeIdP(t)
	p := r.oidcProvider(idp, gpaProviderOpts{})
	before := r.userCount()

	// Closed instance, no invite.
	_, err := r.login(r.session(), idp, p, "")
	gpaRequireAPIError(t, err, 403, "signup_disabled")

	// An invite lets a new account in even when sign-up is closed; the invite is checked, consumed and returned.
	res, err := r.login(r.session(), idp, p, "nci_token")
	require.NoError(t, err)
	require.True(t, res.Created)
	require.Equal(t, "nci_token", res.Invite)
	require.Equal(t, []string{"nci_token"}, r.hooks.checked)
	require.Equal(t, []string{"nci_token"}, r.hooks.accepted)
	require.Equal(t, []uuid.UUID{res.User.ID}, r.hooks.acceptBy)
	require.Empty(t, r.hooks.opened, "an invited user joins the invite's team instead of getting their own")
	require.Equal(t, before+1, r.userCount())

	// A refused invite creates nothing, and the error says the invite is the problem.
	r.hooks.checkErr = errors.New("expired")
	idp.set(map[string]any{"sub": "sub-2", "email": "second@example.com"})
	_, err = r.login(r.session(), idp, p, "nci_bad")
	gpaRequireAPIError(t, err, 403, "invite_invalid")
	r.hooks.checkErr = nil
	// ...also when consuming it fails at the end: the whole sign-up rolls back.
	r.hooks.acceptErr = errors.New("used up")
	_, err = r.login(r.session(), idp, p, "nci_race")
	gpaRequireAPIError(t, err, 403, "invite_invalid")
	_, found := r.identityOf(p, "sub-2")
	require.False(t, found)
	require.Equal(t, before+1, r.userCount(), "no half-created account")
	r.hooks.acceptErr = nil

	// A failing team hook rolls the sign-up back too.
	r.allowOpenSignup(true)
	r.hooks.openErr = errors.New("db hiccup")
	_, err = r.login(r.session(), idp, p, "")
	require.ErrorContains(t, err, "open sign-up")
	require.Equal(t, before+1, r.userCount())
	r.hooks.openErr = nil

	// Without sign-up hooks the control plane cannot place a new user.
	r.svc.SetSignupHooks(nil)
	_, err = r.login(r.session(), idp, p, "")
	gpaRequireAPIError(t, err, 403, "signup_unavailable")
}

func TestOIDCNeverLinksByEmail(t *testing.T) {
	r := newGpaRig(t)
	idp := newFakeIdP(t)
	p := r.oidcProvider(idp, gpaProviderOpts{})
	r.allowOpenSignup(true)
	idp.set(map[string]any{"email": "ada@example.com"}) // the existing owner's address

	_, err := r.login(r.session(), idp, p, "")
	gpaRequireAPIError(t, err, 409, "account_exists")
	_, found := r.identityOf(p, "sub-1")
	require.False(t, found, "an identity is only attached by the signed-in owner")
}

func TestOIDCEmailVerification(t *testing.T) {
	r := newGpaRig(t)
	idp := newFakeIdP(t)
	p := r.oidcProvider(idp, gpaProviderOpts{})
	r.allowOpenSignup(true)

	for name, claims := range map[string]map[string]any{
		"unverified":      {"email_verified": false},
		"flag missing":    {"email_verified": nil},
		"string false":    {"email_verified": "false"},
		"no email at all": {"email": nil},
		"not an address":  {"email": "just-a-name"},
	} {
		idp.set(claims)
		_, err := r.login(r.session(), idp, p, "")
		require.Error(t, err, name)
		gpaRequireAPIError(t, err, 403, "email_not_verified")
	}
	require.Equal(t, 1, r.userCount())

	// Some providers send the flag as a string.
	idp.set(map[string]any{"email_verified": "true", "sub": "s-str", "email": "str@example.com"})
	res, err := r.login(r.session(), idp, p, "")
	require.NoError(t, err)
	require.Equal(t, "str@example.com", res.User.Email)
	// Microsoft Entra ID has no email_verified but vouches for the domain owner.
	idp.set(map[string]any{"email_verified": nil, "xms_edov": true, "sub": "s-ms", "email": "ms@example.com"})
	res, err = r.login(r.session(), idp, p, "")
	require.NoError(t, err)
	require.Equal(t, "ms@example.com", res.User.Email)
	// A token without a name falls back to the preferred username, then the email's local part.
	idp.set(map[string]any{"name": nil, "preferred_username": "pref", "sub": "s-pref", "email": "pref@example.com"})
	res, err = r.login(r.session(), idp, p, "")
	require.NoError(t, err)
	require.Equal(t, "pref", res.User.Name)
	idp.set(map[string]any{"name": nil, "sub": "s-local", "email": "local.part@example.com"})
	res, err = r.login(r.session(), idp, p, "")
	require.NoError(t, err)
	require.Equal(t, "local.part", res.User.Name)
}

func TestOIDCFallsBackToTheUserInfoEndpointForTheEmail(t *testing.T) {
	r := newGpaRig(t)
	idp := newFakeIdP(t)
	p := r.oidcProvider(idp, gpaProviderOpts{})
	r.allowOpenSignup(true)

	idp.set(map[string]any{"email": nil, "email_verified": nil, "name": nil})
	idp.userinfo = map[string]any{"email": "Info.User@Example.com", "email_verified": true, "name": "From Userinfo"}
	res, err := r.login(r.session(), idp, p, "")
	require.NoError(t, err)
	require.Equal(t, "info.user@example.com", res.User.Email, "emails are lower-cased")
	require.Equal(t, "From Userinfo", res.User.Name)

	// When userinfo cannot be read there is no email to trust.
	idp.set(map[string]any{"email": nil, "email_verified": nil, "sub": "s-2"})
	idp.userinfo = nil
	_, err = r.login(r.session(), idp, p, "")
	gpaRequireAPIError(t, err, 403, "email_not_verified")
}

func TestOIDCAllowedDomains(t *testing.T) {
	r := newGpaRig(t)
	idp := newFakeIdP(t)
	p := r.oidcProvider(idp, gpaProviderOpts{Domains: []string{"corp.example"}})
	r.allowOpenSignup(true)

	idp.set(map[string]any{"email": "someone@gmail.example", "sub": "s-out"})
	_, err := r.login(r.session(), idp, p, "")
	gpaRequireAPIError(t, err, 403, "domain_not_allowed")
	idp.set(map[string]any{"email": "someone@corp.example", "email_verified": false, "sub": "s-unv"})
	_, err = r.login(r.session(), idp, p, "")
	gpaRequireAPIError(t, err, 403, "email_not_verified")
	idp.set(map[string]any{"email": "someone@evil-corp.example", "sub": "s-sub"})
	_, err = r.login(r.session(), idp, p, "")
	gpaRequireAPIError(t, err, 403, "domain_not_allowed")
	require.Equal(t, 1, r.userCount())

	idp.set(map[string]any{"email": "Someone@Corp.Example", "sub": "s-in"})
	res, err := r.login(r.session(), idp, p, "")
	require.NoError(t, err)
	require.True(t, res.Created)
}

func TestOIDCSecondFactorAndTrustedMFA(t *testing.T) {
	r := newGpaRig(t)
	idp := newFakeIdP(t)
	plain := r.oidcProvider(idp, gpaProviderOpts{Name: "plain"})
	trusted := r.oidcProvider(idp, gpaProviderOpts{Name: "trusted", TrustMFA: true})
	secret, _ := r.enrollTOTP(r.user)
	for _, p := range []db.AuthProvider{plain, trusted} {
		_, err := r.q.CreateUserIdentity(context.Background(), db.CreateUserIdentityParams{UserID: r.user.ID, ProviderID: p.ID, Subject: "ada-sub", Email: r.user.Email})
		require.NoError(t, err)
	}
	idp.set(map[string]any{"sub": "ada-sub", "amr": []string{"pwd", "MFA"}})

	// A provider that is trusted and asserted MFA counts as a second factor.
	ctx := r.session()
	res, err := r.login(ctx, idp, trusted, "")
	require.NoError(t, err)
	require.True(t, res.TrustedMFA)
	require.False(t, res.NeedsSecondFactor)
	require.Equal(t, r.user.ID, res.User.ID)

	// Trusted but no assertion, or asserted but not trusted: the app's code is needed.
	idp.set(map[string]any{"sub": "ada-sub", "amr": []string{"pwd"}})
	res, err = r.login(r.session(), idp, trusted, "")
	require.NoError(t, err)
	require.False(t, res.TrustedMFA)
	require.True(t, res.NeedsSecondFactor)
	idp.set(map[string]any{"sub": "ada-sub", "amr": []string{"hwk"}})
	ctx = r.session()
	res, err = r.login(ctx, idp, plain, "")
	require.NoError(t, err)
	require.False(t, res.TrustedMFA, "an untrusted provider's claim is ignored")
	require.True(t, res.NeedsSecondFactor)

	// The parked sign-in finishes with the app's code (wrong ones are counted).
	_, err = r.svc.CompleteSecondFactor(ctx, r.sm, "000000")
	gpaRequireAPIError(t, err, 401, "invalid_two_factor_code")
	u, err := r.svc.CompleteSecondFactor(ctx, r.sm, r.totpCode(secret))
	require.NoError(t, err)
	require.Equal(t, r.user.ID, u.ID)
	_, err = r.svc.CompleteSecondFactor(ctx, r.sm, r.totpCode(secret))
	gpaRequireAPIError(t, err, 401, "no_pending_login")
}

func TestOIDCStateIsCheckedAndSingleUse(t *testing.T) {
	r := newGpaRig(t)
	idp := newFakeIdP(t)
	p := r.oidcProvider(idp, gpaProviderOpts{})
	other := r.oidcProvider(idp, gpaProviderOpts{Name: "other"})
	r.allowOpenSignup(true)
	begin := func(ctx context.Context) string {
		authURL, err := r.svc.BeginProviderLogin(ctx, r.sm, p.ID, gpaBase, "")
		require.NoError(t, err)
		return idp.authorize(authURL)
	}
	complete := func(ctx context.Context, provider uuid.UUID, state, code string) error {
		_, err := r.svc.CompleteProviderLogin(ctx, r.sm, provider, gpaBase, state, code)
		return err
	}

	ctx := r.session()
	state := begin(ctx)
	gpaRequireAPIError(t, complete(ctx, p.ID, "forged", "code-ok"), 400, "invalid_oidc_state")
	// A failed attempt burns the flow: the right state no longer works.
	gpaRequireAPIError(t, complete(ctx, p.ID, state, "code-ok"), 400, "invalid_oidc_state")

	ctx = r.session()
	state = begin(ctx)
	gpaRequireAPIError(t, complete(ctx, other.ID, state, "code-ok"), 400, "invalid_oidc_state")

	ctx = r.session()
	state = begin(ctx)
	gpaRequireAPIError(t, complete(ctx, p.ID, state, ""), 400, "invalid_oidc_state")
	gpaRequireAPIError(t, complete(r.session(), p.ID, state, "code-ok"), 400, "invalid_oidc_state") // no flow in this session
	gpaRequireAPIError(t, complete(r.session(), p.ID, "", "code-ok"), 400, "invalid_oidc_state")

	// The flow expires after ten minutes.
	ctx = r.session()
	state = begin(ctx)
	r.advance(11 * time.Minute)
	gpaRequireAPIError(t, complete(ctx, p.ID, state, "code-ok"), 400, "invalid_oidc_state")
	r.advance(-11 * time.Minute)

	// A completed flow cannot be replayed.
	ctx = r.session()
	state = begin(ctx)
	require.NoError(t, complete(ctx, p.ID, state, "code-ok"))
	gpaRequireAPIError(t, complete(ctx, p.ID, state, "code-ok"), 400, "invalid_oidc_state")
}

func TestOIDCTokenProblems(t *testing.T) {
	r := newGpaRig(t)
	idp := newFakeIdP(t)
	p := r.oidcProvider(idp, gpaProviderOpts{})
	r.allowOpenSignup(true)
	reset := func() {
		idp.mu.Lock()
		idp.claims, idp.signWith, idp.omitIDToken, idp.tokenStatus = nil, nil, false, http.StatusOK
		idp.mu.Unlock()
	}
	expect := func(name string, status int, code string) {
		t.Helper()
		_, err := r.login(r.session(), idp, p, "")
		require.Error(t, err, name)
		gpaRequireAPIError(t, err, status, code)
		require.Equal(t, 1, r.userCount(), name+": nobody is signed up on a bad token")
	}

	idp.tokenStatus = http.StatusBadRequest
	expect("the provider rejects the code", 502, "oidc_exchange_failed")
	reset()
	idp.omitIDToken = true
	expect("no id_token", 502, "oidc_invalid_token")
	reset()
	idp.signWith = idpKey(t, 1)
	expect("signed by another key", 502, "oidc_invalid_token")
	reset()
	idp.set(map[string]any{"nonce": "replayed"})
	expect("nonce of another login", 502, "oidc_invalid_token")
	reset()
	idp.set(map[string]any{"aud": "someone-else"})
	expect("issued for another client", 502, "oidc_invalid_token")
	reset()
	idp.set(map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})
	expect("expired", 502, "oidc_invalid_token")
	reset()
	idp.set(map[string]any{"iss": "https://evil.example"})
	expect("other issuer", 502, "oidc_invalid_token")
	reset()

	// A wrong client secret at the provider is an exchange failure, not a login.
	idp.secret = "rotated"
	expect("client secret rotated at the provider", 502, "oidc_exchange_failed")
}

func TestOIDCFlowNeedsAnEnabledProviderAndASecretKey(t *testing.T) {
	r := newGpaRig(t)
	idp := newFakeIdP(t)
	off := r.oidcProvider(idp, gpaProviderOpts{Disabled: true})
	ctx := r.session()

	_, err := r.svc.BeginProviderLogin(ctx, r.sm, off.ID, gpaBase, "")
	gpaRequireAPIError(t, err, 404, "not_found")
	_, err = r.svc.BeginProviderLogin(ctx, r.sm, uuid.New(), gpaBase, "")
	gpaRequireAPIError(t, err, 404, "not_found")
	_, err = r.svc.CompleteProviderLogin(ctx, r.sm, off.ID, gpaBase, "s", "c")
	gpaRequireAPIError(t, err, 400, "invalid_oidc_state")

	// An issuer that went away.
	live := r.oidcProvider(idp, gpaProviderOpts{})
	idp.srv.Close()
	_, err = r.svc.BeginProviderLogin(ctx, r.sm, live.ID, gpaBase, "")
	require.ErrorContains(t, err, "oidc discovery")

	// Without a secret key nothing can be opened.
	bare := auth.NewService(r.pool, nil)
	_, err = bare.BeginProviderLogin(ctx, r.sm, live.ID, gpaBase, "")
	gpaRequireAPIError(t, err, 503, "secret_key_missing")
}

func TestOIDCClientSecretMustOpenUnderItsOwnRow(t *testing.T) {
	r := newGpaRig(t)
	idp := newFakeIdP(t)
	a := r.oidcProvider(idp, gpaProviderOpts{Name: "a"})
	b := r.oidcProvider(idp, gpaProviderOpts{Name: "b"})
	// b's sealed secret copied onto a: it was sealed for another row, so it must not open.
	r.exec(`UPDATE auth_providers SET client_secret_enc = (SELECT client_secret_enc FROM auth_providers WHERE id=$2) WHERE id=$1`, a.ID, b.ID)
	_, err := r.svc.BeginProviderLogin(r.session(), r.sm, a.ID, gpaBase, "")
	require.ErrorContains(t, err, "open client secret")
}

func TestLinkingAnIdentityFromAccountSecurity(t *testing.T) {
	r := newGpaRig(t)
	idp := newFakeIdP(t)
	p := r.oidcProvider(idp, gpaProviderOpts{})
	idp.set(map[string]any{"sub": "ada-sub", "email": "ada@corp.example"})

	link := func(ctx context.Context, userID uuid.UUID) (*auth.FederatedResult, error) {
		authURL, err := r.svc.BeginProviderLink(ctx, r.sm, p.ID, gpaBase, userID)
		if err != nil {
			return nil, err
		}
		return r.svc.CompleteProviderLogin(ctx, r.sm, p.ID, gpaBase, idp.authorize(authURL), "code-ok")
	}

	// A link can only start from the session of the user it is for.
	_, err := link(r.session(), r.user.ID)
	gpaRequireAPIError(t, err, 401, "unauthorized")
	bob := r.newUser("bob@example.com", gpaPassword)
	_, err = link(r.signedIn(bob), r.user.ID)
	gpaRequireAPIError(t, err, 401, "unauthorized")

	// The signed-in owner attaches the identity: no sign-in, no sign-up.
	before := r.userCount()
	res, err := link(r.signedIn(r.user), r.user.ID)
	require.NoError(t, err)
	require.True(t, res.LinkOnly)
	require.True(t, res.Linked)
	require.Equal(t, r.user.ID, res.User.ID)
	require.False(t, res.NeedsSecondFactor)
	require.Equal(t, before, r.userCount())
	row, ok := r.identityOf(p, "ada-sub")
	require.True(t, ok)
	require.Equal(t, r.user.ID, row.UserID)
	require.Equal(t, "ada@corp.example", row.Email, "the provider's address is recorded, the account's is untouched")

	// Linking again is harmless and says nothing was added.
	res, err = link(r.signedIn(r.user), r.user.ID)
	require.NoError(t, err)
	require.True(t, res.LinkOnly)
	require.False(t, res.Linked)

	// The identity now signs the owner in without a sign-up setting.
	res, err = r.login(r.session(), idp, p, "")
	require.NoError(t, err)
	require.Equal(t, r.user.ID, res.User.ID)

	// Someone else cannot claim an identity that is already linked.
	_, err = link(r.signedIn(bob), bob.ID)
	gpaRequireAPIError(t, err, 409, "identity_conflict")

	// The same identity cannot hang on two providers' rows of one user twice, and the email must be verified.
	idp.set(map[string]any{"sub": "bob-sub", "email": "bob@corp.example", "email_verified": false})
	_, err = link(r.signedIn(bob), bob.ID)
	gpaRequireAPIError(t, err, 403, "email_not_verified")
	_, found := r.identityOf(p, "bob-sub")
	require.False(t, found)

	// The provider's domain rule applies to links too.
	strict := r.oidcProvider(idp, gpaProviderOpts{Name: "strict", Domains: []string{"corp.example"}})
	idp.set(map[string]any{"sub": "bob-sub", "email": "bob@elsewhere.example"})
	ctx := r.signedIn(bob)
	authURL, err := r.svc.BeginProviderLink(ctx, r.sm, strict.ID, gpaBase, bob.ID)
	require.NoError(t, err)
	_, err = r.svc.CompleteProviderLogin(ctx, r.sm, strict.ID, gpaBase, idp.authorize(authURL), "code-ok")
	gpaRequireAPIError(t, err, 403, "domain_not_allowed")

	// If the signed-in user changed while the provider page was open, the link is void.
	idp.set(map[string]any{"sub": "bob-sub", "email": "bob@corp.example"})
	ctx = r.signedIn(bob)
	authURL, err = r.svc.BeginProviderLink(ctx, r.sm, p.ID, gpaBase, bob.ID)
	require.NoError(t, err)
	state := idp.authorize(authURL)
	r.sm.Put(ctx, auth.SessionUserKey, r.user.ID.String())
	_, err = r.svc.CompleteProviderLogin(ctx, r.sm, p.ID, gpaBase, state, "code-ok")
	gpaRequireAPIError(t, err, 400, "invalid_oidc_state")
	_, found = r.identityOf(p, "bob-sub")
	require.False(t, found)
}

func TestProviderAdministration(t *testing.T) {
	r := newGpaRig(t)
	idp := newFakeIdP(t)
	bg := context.Background()
	create := func(in auth.ProviderInput) (auth.Provider, error) {
		var p auth.Provider
		err := r.inTx(func(tx pgx.Tx) error {
			var err error
			p, err = r.svc.CreateProvider(bg, tx, in)
			return err
		})
		return p, err
	}
	update := func(id uuid.UUID, in auth.ProviderInput) (auth.Provider, error) {
		var p auth.Provider
		err := r.inTx(func(tx pgx.Tx) error {
			var err error
			p, err = r.svc.UpdateProvider(bg, tx, id, in)
			return err
		})
		return p, err
	}
	good := auth.ProviderInput{
		Kind: auth.KindOIDC, Name: "  Corp SSO ", IssuerURL: idp.srv.URL, ClientID: " cid ", ClientSecret: "shh",
		AllowedDomains: []string{" @Corp.Example ", "corp.example", "", "Other.Example"}, Scopes: []string{"groups"}, TrustMFA: true, Enabled: true,
	}

	// Validation: each refusal is a 422 with a reason and stores nothing.
	bad := func(mod func(*auth.ProviderInput)) error {
		in := good
		mod(&in)
		return errOnly(create(in))
	}
	gpaRequireAPIError(t, bad(func(in *auth.ProviderInput) { in.Kind = "saml" }), 422, "invalid_provider_kind")
	gpaRequireAPIError(t, bad(func(in *auth.ProviderInput) { in.Name = " " }), 422, "invalid_provider")
	gpaRequireAPIError(t, bad(func(in *auth.ProviderInput) { in.ClientID = "" }), 422, "invalid_provider")
	gpaRequireAPIError(t, bad(func(in *auth.ProviderInput) { in.ClientSecret = "  " }), 422, "invalid_provider")
	gpaRequireAPIError(t, bad(func(in *auth.ProviderInput) { in.IssuerURL = "" }), 422, "invalid_issuer")
	gpaRequireAPIError(t, bad(func(in *auth.ProviderInput) { in.IssuerURL = "idp.example/realm" }), 422, "invalid_issuer")
	gpaRequireAPIError(t, bad(func(in *auth.ProviderInput) { in.IssuerURL = "ftp://idp.example" }), 422, "invalid_issuer")
	gpaRequireAPIError(t, bad(func(in *auth.ProviderInput) { in.IssuerURL = "http://idp.example" }), 422, "invalid_issuer")
	gpaRequireAPIError(t, bad(func(in *auth.ProviderInput) { in.Kind, in.IssuerURL = auth.KindGitHub, "http://ghe.example" }), 422, "invalid_issuer")
	gpaRequireAPIError(t, bad(func(in *auth.ProviderInput) { in.Kind, in.IssuerURL = auth.KindGitHub, "https://" }), 422, "invalid_issuer")
	gpaRequireAPIError(t, bad(func(in *auth.ProviderInput) { in.IssuerURL = "http://localhost:1/" }), 422, "issuer_discovery_failed")
	list, err := r.svc.ListProviders(bg)
	require.NoError(t, err)
	require.Empty(t, list)

	// Create: values are tidied, the secret is sealed under the provider's own row, nothing secret is returned.
	id := uuid.New()
	good.ID = id
	p, err := create(good)
	require.NoError(t, err)
	require.Equal(t, id, p.ID, "the caller may pick the id, so the callback URL is known up front")
	require.Equal(t, "Corp SSO", p.Name)
	require.Equal(t, "cid", p.ClientID)
	require.Equal(t, idp.srv.URL, p.IssuerURL)
	require.Equal(t, []string{"corp.example", "other.example"}, p.AllowedDomains)
	require.Equal(t, []string{"groups"}, p.Scopes)
	require.True(t, p.TrustMFA)
	require.True(t, p.Enabled)
	require.False(t, p.CreatedAt.IsZero())
	row, err := r.q.GetAuthProvider(bg, id)
	require.NoError(t, err)
	require.NotContains(t, string(row.ClientSecretEnc), "shh")
	opened, err := r.box.OpenString(secretbox.AuthProvidersClientSecret, id.String(), row.ClientSecretEnc)
	require.NoError(t, err)
	require.Equal(t, "shh", opened)
	gpaRequireAPIError(t, errOnly(create(good)), 409, "provider_id_taken")

	// GitHub: MFA trust is never granted, the base URL is tidied, and no discovery is needed.
	gh, err := create(auth.ProviderInput{Kind: auth.KindGitHub, Name: "GitHub", ClientID: "gh", ClientSecret: "ghs", TrustMFA: true, Enabled: true})
	require.NoError(t, err)
	require.False(t, gh.TrustMFA)
	require.Empty(t, gh.IssuerURL)
	ghe, err := create(auth.ProviderInput{Kind: auth.KindGitHub, Name: "GHE", IssuerURL: " https://ghe.example.com/ ", ClientID: "gh", ClientSecret: "ghs", Enabled: false})
	require.NoError(t, err)
	require.Equal(t, "https://ghe.example.com", ghe.IssuerURL)
	require.Equal(t, []string{}, ghe.Scopes, "never null")
	require.Equal(t, []string{}, ghe.AllowedDomains)

	// The login page lists enabled providers only; administration lists all.
	pub, err := r.svc.PublicProviders(bg)
	require.NoError(t, err)
	names := []string{}
	for _, x := range pub {
		names = append(names, x.Name)
	}
	require.ElementsMatch(t, []string{"Corp SSO", "GitHub"}, names)
	all, err := r.svc.ListProviders(bg)
	require.NoError(t, err)
	require.Len(t, all, 3)

	// Update: an empty secret keeps the stored one (the provider still works), the kind is fixed.
	upd, err := update(id, auth.ProviderInput{Kind: auth.KindGitHub, Name: "Renamed", IssuerURL: idp.srv.URL, ClientID: "cid2", AllowedDomains: []string{"x.example"}, Enabled: false})
	require.NoError(t, err)
	require.Equal(t, auth.KindOIDC, upd.Kind, "the kind cannot change")
	require.Equal(t, "Renamed", upd.Name)
	require.Equal(t, "cid2", upd.ClientID)
	require.False(t, upd.Enabled)
	after, err := r.q.GetAuthProvider(bg, id)
	require.NoError(t, err)
	require.Equal(t, row.ClientSecretEnc, after.ClientSecretEnc, "an empty secret keeps the stored one")
	_, err = update(id, auth.ProviderInput{Name: "Renamed", IssuerURL: idp.srv.URL, ClientID: "cid2", ClientSecret: "new-secret", Enabled: true})
	require.NoError(t, err)
	after, err = r.q.GetAuthProvider(bg, id)
	require.NoError(t, err)
	require.NotEqual(t, row.ClientSecretEnc, after.ClientSecretEnc)
	gpaRequireAPIError(t, errOnly(update(uuid.New(), auth.ProviderInput{Name: "x", ClientID: "x"})), 404, "not_found")
	gpaRequireAPIError(t, errOnly(update(id, auth.ProviderInput{Name: "", ClientID: "x", IssuerURL: idp.srv.URL})), 422, "invalid_provider")

	// Delete removes the provider and its identities.
	_, err = r.q.CreateUserIdentity(bg, db.CreateUserIdentityParams{UserID: r.user.ID, ProviderID: id, Subject: "s", Email: r.user.Email})
	require.NoError(t, err)
	require.NoError(t, r.inTx(func(tx pgx.Tx) error { return r.svc.DeleteProvider(bg, tx, id) }))
	_, found := r.identityOf(db.AuthProvider{ID: id}, "s")
	require.False(t, found)
	gpaRequireAPIError(t, r.inTx(func(tx pgx.Tx) error { return r.svc.DeleteProvider(bg, tx, id) }), 404, "not_found")

	// Without a secret key nothing can be stored.
	bare := auth.NewService(r.pool, nil)
	err = bare.InTx(bg, func(tx pgx.Tx) error { _, err := bare.CreateProvider(bg, tx, good); return err })
	gpaRequireAPIError(t, err, 503, "secret_key_missing")
	err = bare.InTx(bg, func(tx pgx.Tx) error { _, err := bare.UpdateProvider(bg, tx, gh.ID, good); return err })
	gpaRequireAPIError(t, err, 503, "secret_key_missing")
}

func errOnly[T any](_ T, err error) error { return err }

func TestAuthSettingsRoundTrip(t *testing.T) {
	r := newGpaRig(t)
	got, err := r.svc.GetAuthSettings(context.Background())
	require.NoError(t, err)
	require.False(t, got.AllowOpenSignup, "sign-up is by invitation unless an admin opens it")
	r.allowOpenSignup(true)
	got, err = r.svc.GetAuthSettings(context.Background())
	require.NoError(t, err)
	require.True(t, got.AllowOpenSignup)
	r.allowOpenSignup(false)
	got, err = r.svc.GetAuthSettings(context.Background())
	require.NoError(t, err)
	require.False(t, got.AllowOpenSignup)
}

func TestRedirectURL(t *testing.T) {
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	require.Equal(t, "https://nebula.example.com/api/v1/auth/oidc/"+id.String()+"/callback", auth.RedirectURL("https://nebula.example.com/", id))
	require.Equal(t, "http://localhost:8080/api/v1/auth/oidc/"+id.String()+"/callback", auth.RedirectURL("http://localhost:8080", id))
}

// githubServer is GitHub (or Enterprise Server): the OAuth endpoints and the REST API.
type githubServer struct {
	srv         *httptest.Server
	user        map[string]any
	emails      []map[string]any
	userStatus  int
	emailStatus int
	tokenOK     bool
}

func newGithub(t *testing.T) *githubServer {
	t.Helper()
	g := &githubServer{userStatus: http.StatusOK, emailStatus: http.StatusOK, tokenOK: true,
		user:   map[string]any{"id": 4242, "login": "octo", "name": "Octo Cat"},
		emails: []map[string]any{{"email": "Octo@Corp.example", "primary": true, "verified": true}, {"email": "other@corp.example", "primary": false, "verified": true}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if !g.tokenOK || r.PostForm.Get("code") != "gh-code" || r.PostForm.Get("client_secret") == "" && r.Header.Get("Authorization") == "" {
			_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"gho_1","token_type":"bearer","scope":"read:user,user:email"}`))
	})
	mux.HandleFunc("/api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gho_1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if g.userStatus != http.StatusOK {
			w.WriteHeader(g.userStatus)
			return
		}
		_ = json.NewEncoder(w).Encode(g.user)
	})
	mux.HandleFunc("/api/v3/user/emails", func(w http.ResponseWriter, _ *http.Request) {
		if g.emailStatus != http.StatusOK {
			w.WriteHeader(g.emailStatus)
			return
		}
		_ = json.NewEncoder(w).Encode(g.emails)
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (r *gpaRig) githubLogin(g *githubServer, p db.AuthProvider) (*auth.FederatedResult, error) {
	r.t.Helper()
	ctx := r.session()
	authURL, err := r.svc.BeginProviderLogin(ctx, r.sm, p.ID, gpaBase, "")
	require.NoError(r.t, err)
	u, err := url.Parse(authURL)
	require.NoError(r.t, err)
	require.Equal(r.t, g.srv.URL+"/login/oauth/authorize", u.Scheme+"://"+u.Host+u.Path)
	require.Empty(r.t, u.Query().Get("nonce"), "GitHub is plain OAuth: no OpenID nonce")
	require.Contains(r.t, strings.Fields(u.Query().Get("scope")), "user:email")
	require.Equal(r.t, auth.RedirectURL(gpaBase, p.ID), u.Query().Get("redirect_uri"))
	return r.svc.CompleteProviderLogin(ctx, r.sm, p.ID, gpaBase, u.Query().Get("state"), "gh-code")
}

func TestGitHubSignInUsesThePrimaryVerifiedEmail(t *testing.T) {
	r := newGpaRig(t)
	g := newGithub(t)
	p := r.provider(gpaProviderOpts{Kind: auth.KindGitHub, IssuerURL: g.srv.URL})
	r.allowOpenSignup(true)

	res, err := r.githubLogin(g, p)
	require.NoError(t, err)
	require.True(t, res.Created)
	require.Equal(t, "octo@corp.example", res.User.Email)
	require.Equal(t, "Octo Cat", res.User.Name)
	require.False(t, res.TrustedMFA, "GitHub never asserts MFA")
	row, ok := r.identityOf(p, "4242")
	require.True(t, ok)
	require.Equal(t, res.User.ID, row.UserID)

	// The login name stands in for a missing display name.
	g.user = map[string]any{"id": 77, "login": "nameless"}
	g.emails = []map[string]any{{"email": "nameless@corp.example", "primary": true, "verified": true}}
	res, err = r.githubLogin(g, p)
	require.NoError(t, err)
	require.Equal(t, "nameless", res.User.Name)

	// Only a primary and verified address counts.
	g.user = map[string]any{"id": 78, "login": "unverified"}
	g.emails = []map[string]any{{"email": "a@corp.example", "primary": true, "verified": false}, {"email": "b@corp.example", "primary": false, "verified": true}}
	_, err = r.githubLogin(g, p)
	gpaRequireAPIError(t, err, 403, "email_not_verified")
}

func TestGitHubFailures(t *testing.T) {
	r := newGpaRig(t)
	g := newGithub(t)
	p := r.provider(gpaProviderOpts{Kind: auth.KindGitHub, IssuerURL: g.srv.URL})
	r.allowOpenSignup(true)

	g.tokenOK = false
	_, err := r.githubLogin(g, p)
	gpaRequireAPIError(t, err, 502, "oidc_exchange_failed")
	g.tokenOK = true

	g.userStatus = http.StatusInternalServerError
	_, err = r.githubLogin(g, p)
	gpaRequireAPIError(t, err, 502, "oidc_invalid_token")
	g.userStatus = http.StatusOK

	g.user = map[string]any{"login": "no-id"}
	_, err = r.githubLogin(g, p)
	gpaRequireAPIError(t, err, 502, "oidc_invalid_token")
	g.user = map[string]any{"id": 4242, "login": "octo"}

	g.emailStatus = http.StatusForbidden
	_, err = r.githubLogin(g, p)
	gpaRequireAPIError(t, err, 502, "oidc_invalid_token")
	require.Equal(t, 1, r.userCount())
}
