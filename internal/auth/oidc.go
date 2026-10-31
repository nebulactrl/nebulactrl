package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/oauth2"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// Provider kinds.
const (
	KindOIDC   = "oidc"
	KindGitHub = "github"
)

// Session keys of an in-flight provider login.
const (
	sessOIDCState    = "oidc_state"
	sessOIDCNonce    = "oidc_nonce"
	sessOIDCVerifier = "oidc_verifier"
	sessOIDCProvider = "oidc_provider"
	sessOIDCInvite   = "oidc_invite"
	sessOIDCUntil    = "oidc_until"
	sessOIDCLink     = "oidc_link" // the user id a link started from Account → Security belongs to

	oidcFlowTTL = 10 * time.Minute
)

// Errors of the provider login flow. Their codes are shown on the login page.
var (
	ErrInvalidOIDCState   = apierr.BadRequest("invalid_oidc_state", "the sign-in attempt expired or did not start here; try again")
	ErrOIDCExchange       = apierr.New(http.StatusBadGateway, "oidc_exchange_failed", "the identity provider rejected the sign-in")
	ErrOIDCToken          = apierr.New(http.StatusBadGateway, "oidc_invalid_token", "the identity provider returned an invalid token")
	ErrEmailNotVerified   = apierr.New(http.StatusForbidden, "email_not_verified", "the identity provider has no verified email for this account")
	ErrDomainNotAllowed   = apierr.New(http.StatusForbidden, "domain_not_allowed", "your email domain is not allowed to sign in with this provider")
	ErrSignupDisabled     = apierr.New(http.StatusForbidden, "signup_disabled", "sign-up is by invitation only; ask a team owner for an invite")
	ErrSignupUnavailable  = apierr.New(http.StatusForbidden, "signup_unavailable", "sign-up through this provider is not available")
	ErrInviteInvalid      = apierr.New(http.StatusForbidden, "invite_invalid", "the invite is invalid, expired or for a different email")
	ErrIdentityConflict   = apierr.Conflict("identity_conflict", "this account is already linked to a different identity at that provider")
	ErrAccountExists      = apierr.Conflict("account_exists", "an account with this email already exists; sign in with your password, then link your single sign-on account in Account → Security")
	ErrProviderNotFound   = apierr.NotFound("provider")
	errProviderDiscovery  = "issuer_discovery_failed"
	mfaAuthenticationRefs = []string{"mfa", "otp", "hwk"}
)

// Provider is the API view of an identity provider (never its secret).
type Provider struct {
	ID             uuid.UUID
	Kind           string
	Name           string
	IssuerURL      string
	ClientID       string
	Scopes         []string
	AllowedDomains []string
	TrustMFA       bool
	Enabled        bool
	CreatedAt      time.Time
}

func providerFrom(p db.AuthProvider) Provider {
	return Provider{
		ID: p.ID, Kind: p.Kind, Name: p.Name, IssuerURL: p.IssuerUrl, ClientID: p.ClientID, Scopes: nonNil(p.Scopes),
		AllowedDomains: nonNil(p.AllowedDomains), TrustMFA: p.TrustMfa, Enabled: p.Enabled, CreatedAt: p.CreatedAt,
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// PublicProvider is what the login page shows.
type PublicProvider struct {
	ID   uuid.UUID
	Name string
	Kind string
}

// ProviderInput creates or updates a provider. On update an empty ClientSecret
// keeps the stored one.
type ProviderInput struct {
	// ID is the ID a new provider gets, so its callback URL can be registered at
	// the identity provider before the provider is saved. Zero picks a random
	// one. Ignored on update.
	ID             uuid.UUID
	Kind           string
	Name           string
	IssuerURL      string
	ClientID       string
	ClientSecret   string
	Scopes         []string
	AllowedDomains []string
	TrustMFA       bool
	Enabled        bool
}

// AuthSettings are the instance-wide sign-in settings.
type AuthSettings struct {
	AllowOpenSignup bool
}

// ---- settings and provider administration ----

// GetAuthSettings returns the instance sign-in settings.
func (s *Service) GetAuthSettings(ctx context.Context) (AuthSettings, error) {
	r, err := s.q.GetAuthSettings(ctx)
	if err != nil {
		return AuthSettings{}, fmt.Errorf("get auth settings: %w", err)
	}
	return AuthSettings{AllowOpenSignup: r.AllowOpenSignup}, nil
}

// SetAuthSettings updates the instance sign-in settings.
func (s *Service) SetAuthSettings(ctx context.Context, tx pgx.Tx, in AuthSettings) (AuthSettings, error) {
	r, err := s.q.WithTx(tx).SetAuthSettings(ctx, in.AllowOpenSignup)
	if err != nil {
		return AuthSettings{}, fmt.Errorf("set auth settings: %w", err)
	}
	return AuthSettings{AllowOpenSignup: r.AllowOpenSignup}, nil
}

// PublicProviders lists the enabled providers for the login page.
func (s *Service) PublicProviders(ctx context.Context) ([]PublicProvider, error) {
	rows, err := s.q.ListEnabledAuthProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	out := make([]PublicProvider, 0, len(rows))
	for _, r := range rows {
		out = append(out, PublicProvider{ID: r.ID, Name: r.Name, Kind: r.Kind})
	}
	return out, nil
}

// ListProviders lists every provider (administration view).
func (s *Service) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.q.ListAuthProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	out := make([]Provider, 0, len(rows))
	for _, r := range rows {
		out = append(out, providerFrom(r))
	}
	return out, nil
}

func normalizeDomains(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, d := range in {
		d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "@"))
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

func validateIssuerURL(kind, raw string) (string, error) {
	// An OIDC issuer is matched byte for byte against the discovery document
	// (Auth0 and others end it with "/"), so only a GitHub base URL is tidied.
	raw = strings.TrimSpace(raw)
	if kind == KindGitHub {
		raw = strings.TrimRight(raw, "/")
	}
	if raw == "" {
		if kind == KindOIDC {
			return "", apierr.Unprocessable("invalid_issuer", "an issuer URL is required for OpenID Connect")
		}
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", apierr.Unprocessable("invalid_issuer", "the issuer URL must be an absolute http(s) URL")
	}
	if u.Scheme == "http" {
		h := u.Hostname()
		if h != "localhost" && h != "127.0.0.1" && h != "::1" {
			return "", apierr.Unprocessable("invalid_issuer", "the issuer URL must use https (http is only allowed for localhost)")
		}
	}
	return raw, nil
}

func (s *Service) validateProviderInput(ctx context.Context, in *ProviderInput) error {
	in.Name = strings.TrimSpace(in.Name)
	in.ClientID = strings.TrimSpace(in.ClientID)
	if in.Kind != KindOIDC && in.Kind != KindGitHub {
		return apierr.Unprocessable("invalid_provider_kind", "kind must be oidc or github")
	}
	if in.Name == "" || in.ClientID == "" {
		return apierr.Unprocessable("invalid_provider", "name and client ID are required")
	}
	issuer, err := validateIssuerURL(in.Kind, in.IssuerURL)
	if err != nil {
		return err
	}
	in.IssuerURL = issuer
	in.AllowedDomains = normalizeDomains(in.AllowedDomains)
	if in.Scopes == nil {
		in.Scopes = []string{}
	}
	if in.Kind == KindGitHub {
		in.TrustMFA = false // GitHub does not assert MFA in a verifiable way
	}
	if in.Kind == KindOIDC {
		if _, err := oidc.NewProvider(oidc.ClientContext(ctx, s.methods.client()), in.IssuerURL); err != nil {
			return apierr.Unprocessable(errProviderDiscovery, "could not read the OpenID configuration of that issuer: "+err.Error())
		}
	}
	return nil
}

// CreateProvider adds an identity provider.
func (s *Service) CreateProvider(ctx context.Context, tx pgx.Tx, in ProviderInput) (Provider, error) {
	box, err := s.secretBox()
	if err != nil {
		return Provider{}, err
	}
	if strings.TrimSpace(in.ClientSecret) == "" {
		return Provider{}, apierr.Unprocessable("invalid_provider", "a client secret is required")
	}
	if err := s.validateProviderInput(ctx, &in); err != nil {
		return Provider{}, err
	}
	id := in.ID
	if id == uuid.Nil {
		id = uuid.New()
	}
	enc, err := box.SealString(secretbox.AuthProvidersClientSecret, id.String(), in.ClientSecret)
	if err != nil {
		return Provider{}, fmt.Errorf("seal client secret: %w", err)
	}
	row, err := s.q.WithTx(tx).CreateAuthProvider(ctx, db.CreateAuthProviderParams{
		ID: id, Kind: in.Kind, Name: in.Name, IssuerUrl: in.IssuerURL, ClientID: in.ClientID, ClientSecretEnc: enc,
		Scopes: in.Scopes, AllowedDomains: in.AllowedDomains, TrustMfa: in.TrustMFA, Enabled: in.Enabled,
	})
	if isUniqueViolation(err) {
		return Provider{}, apierr.Conflict("provider_id_taken", "a provider with that ID already exists")
	}
	if err != nil {
		return Provider{}, fmt.Errorf("create provider: %w", err)
	}
	return providerFrom(row), nil
}

// UpdateProvider changes a provider. The kind cannot change.
func (s *Service) UpdateProvider(ctx context.Context, tx pgx.Tx, id uuid.UUID, in ProviderInput) (Provider, error) {
	box, err := s.secretBox()
	if err != nil {
		return Provider{}, err
	}
	cur, err := s.q.GetAuthProvider(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Provider{}, ErrProviderNotFound
	}
	if err != nil {
		return Provider{}, fmt.Errorf("get provider: %w", err)
	}
	in.Kind = cur.Kind
	if err := s.validateProviderInput(ctx, &in); err != nil {
		return Provider{}, err
	}
	var enc []byte
	if strings.TrimSpace(in.ClientSecret) != "" {
		if enc, err = box.SealString(secretbox.AuthProvidersClientSecret, id.String(), in.ClientSecret); err != nil {
			return Provider{}, fmt.Errorf("seal client secret: %w", err)
		}
	}
	row, err := s.q.WithTx(tx).UpdateAuthProvider(ctx, db.UpdateAuthProviderParams{
		ID: id, Name: in.Name, IssuerUrl: in.IssuerURL, ClientID: in.ClientID, ClientSecretEnc: enc,
		Scopes: in.Scopes, AllowedDomains: in.AllowedDomains, TrustMfa: in.TrustMFA, Enabled: in.Enabled,
	})
	if err != nil {
		return Provider{}, fmt.Errorf("update provider: %w", err)
	}
	return providerFrom(row), nil
}

// DeleteProvider removes a provider and every identity linked through it.
func (s *Service) DeleteProvider(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	n, err := s.q.WithTx(tx).DeleteAuthProvider(ctx, id)
	if err != nil {
		return fmt.Errorf("delete provider: %w", err)
	}
	if n == 0 {
		return ErrProviderNotFound
	}
	return nil
}

// ---- the login flow ----

// RedirectURL is the callback URL to register at the provider.
func RedirectURL(baseURL string, providerID uuid.UUID) string {
	return strings.TrimRight(baseURL, "/") + "/api/v1/auth/oidc/" + providerID.String() + "/callback"
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// providerEndpoints holds what the flow needs from a provider, resolved from the
// stored row (and, for OIDC, the issuer's discovery document).
type providerEndpoints struct {
	row     db.AuthProvider
	oauth   *oauth2.Config
	oidc    *oidc.Provider // nil for GitHub
	apiBase string         // GitHub API base
}

func (s *Service) endpoints(ctx context.Context, id uuid.UUID, baseURL string) (*providerEndpoints, error) {
	box, err := s.secretBox()
	if err != nil {
		return nil, err
	}
	row, err := s.q.GetAuthProvider(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !row.Enabled) {
		return nil, ErrProviderNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get provider: %w", err)
	}
	secret, err := box.OpenString(secretbox.AuthProvidersClientSecret, row.ID.String(), row.ClientSecretEnc)
	if err != nil {
		return nil, fmt.Errorf("open client secret: %w", err)
	}
	ep := &providerEndpoints{row: row}
	cfg := &oauth2.Config{ClientID: row.ClientID, ClientSecret: secret, RedirectURL: RedirectURL(baseURL, row.ID)}
	switch row.Kind {
	case KindOIDC:
		p, err := oidc.NewProvider(oidc.ClientContext(ctx, s.methods.client()), row.IssuerUrl)
		if err != nil {
			return nil, fmt.Errorf("oidc discovery: %w", err)
		}
		ep.oidc = p
		cfg.Endpoint = p.Endpoint()
		cfg.Scopes = append([]string{oidc.ScopeOpenID, "email", "profile"}, row.Scopes...)
	case KindGitHub:
		web, api := "https://github.com", "https://api.github.com"
		if row.IssuerUrl != "" {
			web, api = row.IssuerUrl, row.IssuerUrl+"/api/v3"
		}
		ep.apiBase = api
		cfg.Endpoint = oauth2.Endpoint{AuthURL: web + "/login/oauth/authorize", TokenURL: web + "/login/oauth/access_token"}
		cfg.Scopes = append([]string{"read:user", "user:email"}, row.Scopes...)
	default:
		return nil, ErrProviderNotFound
	}
	ep.oauth = cfg
	return ep, nil
}

// BeginProviderLogin stores the flow state in the session and returns the
// provider's authorisation URL. invite is an optional team invite token that
// authorises sign-up of a new account.
func (s *Service) BeginProviderLogin(ctx context.Context, sm *scs.SessionManager, providerID uuid.UUID, baseURL, invite string) (string, error) {
	return s.beginProvider(ctx, sm, providerID, baseURL, invite, "")
}

// BeginProviderLink starts linking the signed-in user's account to an identity
// at the provider (Account → Security). The session must be signed in as
// userID; the callback attaches the identity to that user and signs nobody in.
func (s *Service) BeginProviderLink(ctx context.Context, sm *scs.SessionManager, providerID uuid.UUID, baseURL string, userID uuid.UUID) (string, error) {
	if sm.GetString(ctx, SessionUserKey) != userID.String() {
		return "", apierr.Unauthorized()
	}
	return s.beginProvider(ctx, sm, providerID, baseURL, "", userID.String())
}

func (s *Service) beginProvider(ctx context.Context, sm *scs.SessionManager, providerID uuid.UUID, baseURL, invite, linkUser string) (string, error) {
	ep, err := s.endpoints(ctx, providerID, baseURL)
	if err != nil {
		return "", err
	}
	state, err := randomToken()
	if err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{}
	if ep.oidc != nil {
		nonce, err := randomToken()
		if err != nil {
			return "", err
		}
		verifier := oauth2.GenerateVerifier()
		sm.Put(ctx, sessOIDCNonce, nonce)
		sm.Put(ctx, sessOIDCVerifier, verifier)
		opts = append(opts, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	}
	sm.Put(ctx, sessOIDCState, state)
	sm.Put(ctx, sessOIDCProvider, providerID.String())
	sm.Put(ctx, sessOIDCInvite, invite)
	sm.Put(ctx, sessOIDCLink, linkUser)
	sm.Put(ctx, sessOIDCUntil, s.methods.clock().Add(oidcFlowTTL).Unix())
	return ep.oauth.AuthCodeURL(state, opts...), nil
}

// FederatedResult is the outcome of a provider login.
type FederatedResult struct {
	User *User
	// NeedsSecondFactor: the user has TOTP and the provider did not assert MFA;
	// the session holds a pending login (see CompleteSecondFactor).
	NeedsSecondFactor bool
	// TrustedMFA: the provider is trusted and asserted MFA; the session counts as
	// second-factor verified.
	TrustedMFA bool
	Created    bool
	// LinkOnly: the flow attached an identity to the signed-in user who started
	// it (Account → Security); nobody was signed in by it. Linked says whether a
	// new identity was added (false: it was already linked).
	LinkOnly bool
	Linked   bool
	// Invite is the team invite token the login carried ("" when none).
	Invite string
}

// identity is what a provider asserts about a person.
type identity struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	MFA           bool
}

type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case bool:
		*b = flexBool(t)
	case string:
		*b = flexBool(strings.EqualFold(t, "true"))
	default:
		*b = false
	}
	return nil
}

type oidcClaims struct {
	Email         string   `json:"email"`
	EmailVerified flexBool `json:"email_verified"`
	// EmailDomainOwnerVerified is Microsoft Entra ID's xms_edov optional claim.
	EmailDomainOwnerVerified flexBool `json:"xms_edov"`
	Name                     string   `json:"name"`
	PreferredUsername        string   `json:"preferred_username"`
	AMR                      []string `json:"amr"`
}

// CompleteProviderLogin finishes the authorization-code flow: it checks the
// state, exchanges the code, verifies the identity, applies the allowed-domain
// rule and resolves (links or creates) the local user.
func (s *Service) CompleteProviderLogin(ctx context.Context, sm *scs.SessionManager, providerID uuid.UUID, baseURL, state, code string) (*FederatedResult, error) {
	wantState := sm.GetString(ctx, sessOIDCState)
	nonce := sm.GetString(ctx, sessOIDCNonce)
	verifier := sm.GetString(ctx, sessOIDCVerifier)
	wantProvider := sm.GetString(ctx, sessOIDCProvider)
	invite := sm.GetString(ctx, sessOIDCInvite)
	link := sm.GetString(ctx, sessOIDCLink)
	until := sm.GetInt64(ctx, sessOIDCUntil)
	for _, k := range []string{sessOIDCState, sessOIDCNonce, sessOIDCVerifier, sessOIDCProvider, sessOIDCInvite, sessOIDCLink, sessOIDCUntil} {
		sm.Remove(ctx, k) // single use
	}
	if wantState == "" || state == "" || subtle.ConstantTimeCompare([]byte(wantState), []byte(state)) != 1 ||
		wantProvider != providerID.String() || s.methods.clock().Unix() > until || code == "" {
		return nil, ErrInvalidOIDCState
	}
	if link != "" && sm.GetString(ctx, SessionUserKey) != link {
		return nil, ErrInvalidOIDCState // the signed-in user changed since the link started
	}
	ep, err := s.endpoints(ctx, providerID, baseURL)
	if err != nil {
		return nil, err
	}
	hc := oidc.ClientContext(ctx, s.methods.client())
	var exchange []oauth2.AuthCodeOption
	if verifier != "" {
		exchange = append(exchange, oauth2.VerifierOption(verifier))
	}
	tok, err := ep.oauth.Exchange(hc, code, exchange...)
	if err != nil {
		return nil, errors.Join(ErrOIDCExchange, err)
	}
	var ident *identity
	if ep.oidc != nil {
		ident, err = s.oidcIdentity(hc, ep, tok, nonce)
	} else {
		ident, err = s.githubIdentity(hc, ep, tok)
	}
	if err != nil {
		return nil, err
	}
	if link != "" {
		// A provider link started from Account → Security: attach the identity to
		// that user; no sign-in, no second factor, the session stays as it is.
		linkUser, err := uuid.Parse(link)
		if err != nil {
			return nil, ErrInvalidOIDCState
		}
		return s.completeLink(ctx, ep.row, ident, linkUser)
	}
	res, err := s.resolveFederated(ctx, ep.row, ident, invite)
	if err != nil {
		return nil, err
	}
	res.Invite = invite
	res.TrustedMFA = ep.row.TrustMfa && ident.MFA
	if !res.TrustedMFA {
		if res.NeedsSecondFactor, err = s.BeginSecondFactor(ctx, sm, res.User); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func (s *Service) oidcIdentity(ctx context.Context, ep *providerEndpoints, tok *oauth2.Token, nonce string) (*identity, error) {
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return nil, ErrOIDCToken
	}
	idt, err := ep.oidc.Verifier(&oidc.Config{ClientID: ep.row.ClientID}).Verify(ctx, raw)
	if err != nil {
		return nil, errors.Join(ErrOIDCToken, err)
	}
	if nonce == "" || subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(nonce)) != 1 {
		return nil, errors.Join(ErrOIDCToken, errors.New("nonce mismatch"))
	}
	var c oidcClaims
	if err := idt.Claims(&c); err != nil {
		return nil, errors.Join(ErrOIDCToken, err)
	}
	// Microsoft Entra ID never sends email_verified. Its optional claim xms_edov
	// says the tenant admin has verified the domain of this email, which is the
	// same guarantee (https://learn.microsoft.com/en-us/entra/identity-platform/optional-claims-reference).
	if c.Email != "" && c.EmailDomainOwnerVerified {
		c.EmailVerified = true
	}
	if c.Email == "" {
		ui, err := ep.oidc.UserInfo(ctx, oauth2.StaticTokenSource(tok))
		if err == nil {
			var uc oidcClaims
			if ui.Claims(&uc) == nil {
				c.Email, c.EmailVerified = uc.Email, uc.EmailVerified
				if c.Name == "" {
					c.Name = uc.Name
				}
			}
		}
	}
	name := c.Name
	if name == "" {
		name = c.PreferredUsername
	}
	mfa := false
	for _, a := range c.AMR {
		for _, want := range mfaAuthenticationRefs {
			if strings.EqualFold(a, want) {
				mfa = true
			}
		}
	}
	return &identity{Subject: idt.Subject, Email: normalizeEmail(c.Email), EmailVerified: bool(c.EmailVerified), Name: name, MFA: mfa}, nil
}

func (s *Service) githubIdentity(ctx context.Context, ep *providerEndpoints, tok *oauth2.Token) (*identity, error) {
	hc := ep.oauth.Client(ctx, tok)
	get := func(path string, v any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.apiBase+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		res, err := hc.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("github %s: status %d", path, res.StatusCode)
		}
		return json.NewDecoder(res.Body).Decode(v)
	}
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := get("/user", &user); err != nil || user.ID == 0 {
		return nil, errors.Join(ErrOIDCToken, err)
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := get("/user/emails", &emails); err != nil {
		return nil, errors.Join(ErrOIDCToken, err)
	}
	id := &identity{Subject: fmt.Sprint(user.ID), Name: user.Name}
	if id.Name == "" {
		id.Name = user.Login
	}
	for _, e := range emails {
		if e.Primary && e.Verified {
			id.Email, id.EmailVerified = normalizeEmail(e.Email), true
			break
		}
	}
	return id, nil
}

func emailDomain(email string) string {
	i := strings.LastIndexByte(email, '@')
	if i < 0 {
		return ""
	}
	return strings.ToLower(email[i+1:])
}

// resolveFederated maps an asserted identity to a local user: by linked
// identity, or by sign-up. It never links a provider to an existing account by
// email: linking is the signed-in user's own act (BeginProviderLink).
func (s *Service) resolveFederated(ctx context.Context, p db.AuthProvider, id *identity, invite string) (*FederatedResult, error) {
	if !strings.Contains(id.Email, "@") {
		id.Email, id.EmailVerified = "", false
	}
	if err := checkProviderDomain(p, id); err != nil {
		return nil, err
	}
	res := &FederatedResult{}
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		link, err := q.GetUserIdentity(ctx, db.GetUserIdentityParams{ProviderID: p.ID, Subject: id.Subject})
		if err == nil {
			u, err := q.GetUser(ctx, link.UserID)
			if err != nil {
				return fmt.Errorf("get user: %w", err)
			}
			res.User = userFrom(u)
			return q.TouchUserIdentity(ctx, link.ID)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("get identity: %w", err)
		}
		// New to this provider: only a verified email may link or create.
		if id.Email == "" || !id.EmailVerified {
			return ErrEmailNotVerified
		}
		if _, err := q.GetUserByEmail(ctx, id.Email); err == nil {
			// An account with this email exists and this provider is not linked to
			// it. Never link here: whoever registered that email first (an invite
			// sign-up takes any address) would share the account with the real
			// owner. The owner links from Account → Security, signed in.
			return ErrAccountExists
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("get user by email: %w", err)
		}
		return s.signUpFederated(ctx, tx, p, id, invite, res)
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// checkProviderDomain applies the provider's allowed-domain rule to an asserted
// identity: with a rule, a verified email inside it is required.
func checkProviderDomain(p db.AuthProvider, id *identity) error {
	if len(p.AllowedDomains) == 0 {
		return nil
	}
	if id.Email == "" || !id.EmailVerified {
		return ErrEmailNotVerified
	}
	d := emailDomain(id.Email)
	for _, a := range p.AllowedDomains {
		if a == d {
			return nil
		}
	}
	return ErrDomainNotAllowed
}

// completeLink attaches the identity the provider asserted to the signed-in user
// who started the link from Account → Security. It is the only way an identity
// joins an account that was not created through the provider.
func (s *Service) completeLink(ctx context.Context, p db.AuthProvider, id *identity, userID uuid.UUID) (*FederatedResult, error) {
	if !strings.Contains(id.Email, "@") {
		id.Email, id.EmailVerified = "", false
	}
	if err := checkProviderDomain(p, id); err != nil {
		return nil, err
	}
	if id.Email == "" || !id.EmailVerified {
		return nil, ErrEmailNotVerified
	}
	res := &FederatedResult{LinkOnly: true}
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		u, err := q.GetUser(ctx, userID)
		if err != nil {
			return fmt.Errorf("get user: %w", err)
		}
		res.User = userFrom(u)
		existing, err := q.GetUserIdentity(ctx, db.GetUserIdentityParams{ProviderID: p.ID, Subject: id.Subject})
		switch {
		case err == nil && existing.UserID == userID:
			return q.TouchUserIdentity(ctx, existing.ID) // already linked: nothing to do
		case err == nil:
			return ErrIdentityConflict // someone else's identity
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("get identity: %w", err)
		}
		if _, err := q.CreateUserIdentity(ctx, db.CreateUserIdentityParams{UserID: userID, ProviderID: p.ID, Subject: id.Subject, Email: id.Email}); err != nil {
			if isUniqueViolation(err) {
				return ErrIdentityConflict
			}
			return fmt.Errorf("link identity: %w", err)
		}
		res.Linked = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Service) signUpFederated(ctx context.Context, tx pgx.Tx, p db.AuthProvider, id *identity, invite string, res *FederatedResult) error {
	hooks := s.signupHooks()
	q := s.q.WithTx(tx)
	if invite == "" {
		st, err := q.GetAuthSettings(ctx)
		if err != nil {
			return fmt.Errorf("get auth settings: %w", err)
		}
		if !st.AllowOpenSignup {
			return ErrSignupDisabled
		}
	}
	if hooks == nil {
		return ErrSignupUnavailable
	}
	if invite != "" {
		if err := hooks.CheckInvite(ctx, invite, id.Email); err != nil {
			return errors.Join(ErrInviteInvalid, err)
		}
	}
	name := strings.TrimSpace(id.Name)
	if name == "" {
		name = id.Email[:strings.IndexByte(id.Email, '@')]
	}
	u, err := q.CreateFederatedUser(ctx, db.CreateFederatedUserParams{Email: id.Email, Name: name})
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	if _, err := q.CreateUserIdentity(ctx, db.CreateUserIdentityParams{UserID: u.ID, ProviderID: p.ID, Subject: id.Subject, Email: id.Email}); err != nil {
		return fmt.Errorf("link identity: %w", err)
	}
	if invite != "" {
		if err := hooks.AcceptInvite(ctx, tx, invite, u.ID, id.Email); err != nil {
			return errors.Join(ErrInviteInvalid, err)
		}
	} else if err := hooks.OpenSignup(ctx, tx, u.ID, name); err != nil {
		return fmt.Errorf("open sign-up: %w", err)
	}
	res.User, res.Created = userFrom(u), true
	return nil
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}
