package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// ---- middleware: public base URL and the team 2FA gate ----

const baseURLKey ctxKey = 2

// baseURL is the externally visible origin of this server: NEBULACTRL_PUBLIC_URL
// when set, else derived from the request (development only; production must set
// the public URL so redirect URIs cannot be steered by a forged Host header).
func baseURL(ctx context.Context) string {
	s, _ := ctx.Value(baseURLKey).(string)
	return s
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// twoFactorGate records the base URL in the request context and, for principals
// of a team that requires 2FA who have not satisfied it, answers 403
// `two_factor_enrollment_required` for everything except what is needed to
// enrol, sign out or switch to another team.
func twoFactorGate(cfg *config.Config, svc *auth.Service, sessions *scs.SessionManager) func(http.Handler) http.Handler {
	configured := ""
	if cfg != nil {
		configured = cfg.PublicURL
	}
	return func(next http.Handler) http.Handler {
		if svc == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			base := configured
			if base == "" {
				base = requestOrigin(r)
			}
			ctx = context.WithValue(ctx, baseURLKey, base)
			r = r.WithContext(ctx)
			p, ok := auth.PrincipalFrom(ctx)
			if !ok || twoFactorExempt(r.Method, strings.TrimPrefix(r.URL.Path, BasePath)) {
				next.ServeHTTP(w, r)
				return
			}
			blocked, err := svc.SecondFactorBlocked(ctx, sessions, p)
			if err != nil {
				slog.ErrorContext(ctx, "second factor gate", "error", err)
				apierr.Write(w, apierr.Internal())
				return
			}
			if blocked {
				apierr.Write(w, apierr.New(http.StatusForbidden, "two_factor_enrollment_required",
					"this team requires two-factor authentication; set it up in Account settings → Security first"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// twoFactorExempt lists what stays reachable while enrolment is outstanding.
func twoFactorExempt(method, path string) bool {
	switch {
	case path == "/me" || strings.HasPrefix(path, "/me/"),
		strings.HasPrefix(path, "/auth/"),
		path == "/setup",
		strings.HasPrefix(path, "/invites/"):
		return true
	case path == "/teams" && method == http.MethodGet:
		return true
	case method == http.MethodPost && strings.HasPrefix(path, "/teams/") && strings.HasSuffix(path, "/switch"):
		return true
	}
	return false
}

// ---- the password-login hook ----

// beginSecondFactor runs after a correct password. When the user has TOTP it
// parks the login in the session and answers 401 `two_factor_required`; the
// client then posts the code to /auth/2fa.
func (c *core) beginSecondFactor(ctx context.Context, u *auth.User) error {
	c.sessions.Remove(ctx, auth.SessionMFAKey)
	pending, err := c.auth.BeginSecondFactor(ctx, c.sessions, u)
	if err != nil {
		return err
	}
	if pending {
		return auth.ErrTwoFactorRequired
	}
	return nil
}

// ---- types ----

type publicProviderBody struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Kind string    `json:"kind" enum:"oidc,github"`
}

type providersOutput struct {
	Body struct {
		Providers []publicProviderBody `json:"providers"`
	}
}

type redirectOutput struct {
	Status       int
	Location     string `header:"Location"`
	CacheControl string `header:"Cache-Control"`
}

type providerStartInput struct {
	ProviderID uuid.UUID `path:"providerId"`
	Invite     string    `query:"invite" maxLength:"200" doc:"Team invite token that authorises sign-up of a new account"`
}

type providerCallbackInput struct {
	ProviderID    uuid.UUID `path:"providerId"`
	Code          string    `query:"code" maxLength:"4096"`
	State         string    `query:"state" maxLength:"512"`
	ProviderError string    `query:"error" maxLength:"200"`
}

type secondFactorInput struct {
	Body struct {
		Code string `json:"code" minLength:"6" maxLength:"40" doc:"A 6-digit authenticator code or a recovery code"`
	}
}

type identityBody struct {
	ID           uuid.UUID  `json:"id"`
	ProviderID   uuid.UUID  `json:"providerId" doc:"The provider this account is at (see the sign-in providers)"`
	ProviderName string     `json:"providerName"`
	ProviderKind string     `json:"providerKind"`
	Email        string     `json:"email"`
	LinkedAt     time.Time  `json:"linkedAt"`
	LastLoginAt  *time.Time `json:"lastLoginAt,omitempty"`
}

type requiredTeamBody struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// SecurityBody is the signed-in user's sign-in security picture.
type SecurityBody struct {
	TOTPEnabled         bool               `json:"totpEnabled"`
	TOTPPending         bool               `json:"totpPending" doc:"A secret was generated but not confirmed yet"`
	RecoveryCodesLeft   int                `json:"recoveryCodesLeft"`
	Passkeys            []passkeyBody      `json:"passkeys"`
	PasskeysUnavailable string             `json:"passkeysUnavailable,omitempty" doc:"Why passkeys cannot be used at this address (it needs HTTPS and a domain name); absent when they can"`
	HasPassword         bool               `json:"hasPassword"`
	SessionMFA          bool               `json:"sessionMfa" doc:"This session proved a second factor"`
	EnrollmentNeeded    bool               `json:"enrollmentNeeded" doc:"The current team requires 2FA and this account has not enrolled; everything else is blocked until it does"`
	RequiredByTeams     []requiredTeamBody `json:"requiredByTeams"`
	Identities          []identityBody     `json:"identities"`
	IsInstanceAdmin     bool               `json:"isInstanceAdmin"`
}

type securityOutput struct{ Body SecurityBody }

type totpSetupOutput struct {
	Body struct {
		Secret string `json:"secret" doc:"Base32 secret for manual entry"`
		URI    string `json:"uri" doc:"otpauth:// URI for authenticator apps"`
	}
}

type totpEnableInput struct {
	Body struct {
		Code string `json:"code" minLength:"6" maxLength:"10"`
	}
}

type regenerateRecoveryCodesInput struct {
	Body struct {
		Code     string `json:"code,omitempty" maxLength:"40" doc:"A current authenticator code or an unused recovery code"`
		Password string `json:"password,omitempty" maxLength:"256" doc:"Instead of a code, for an account that uses passkeys and no authenticator app"`
	}
}

type recoveryCodesOutput struct {
	Body struct {
		RecoveryCodes []string `json:"recoveryCodes" doc:"Single-use codes; shown once"`
	}
}

type totpDisableInput struct {
	Body struct {
		Code     string `json:"code" minLength:"6" maxLength:"40" doc:"A current authenticator code or a recovery code"`
		Password string `json:"password,omitempty" maxLength:"256" doc:"Required when the account has a password"`
	}
}

type unlinkIdentityInput struct {
	ID uuid.UUID `path:"id"`
}

type startIdentityLinkInput struct {
	Body struct {
		ProviderID uuid.UUID `json:"providerId" doc:"The provider to link, from the enabled sign-in providers"`
		Password   string    `json:"password,omitempty" maxLength:"256" doc:"The account's password; required when the account has one"`
	}
}

type startIdentityLinkOutput struct {
	Body struct {
		URL string `json:"url" doc:"Where to send the browser: the provider's sign-in page"`
	}
}

type providerBody struct {
	ID             uuid.UUID `json:"id"`
	Kind           string    `json:"kind" enum:"oidc,github"`
	Name           string    `json:"name"`
	IssuerURL      string    `json:"issuerUrl" doc:"OIDC issuer; for GitHub an optional Enterprise Server base URL"`
	ClientID       string    `json:"clientId"`
	Scopes         []string  `json:"scopes"`
	AllowedDomains []string  `json:"allowedDomains" doc:"Empty allows any verified email"`
	TrustMFA       bool      `json:"trustMfa" doc:"Skip the local 2FA step when the provider asserts MFA (amr claim)"`
	Enabled        bool      `json:"enabled"`
	RedirectURI    string    `json:"redirectUri" doc:"Register this callback URL at the provider"`
	CreatedAt      time.Time `json:"createdAt"`
}

type providerInputBody struct {
	ID             *uuid.UUID `json:"id,omitempty" required:"false" doc:"Create only: the ID the provider gets, so its callback URL (/api/v1/auth/oidc/{id}/callback) can be registered at the identity provider before saving. Generated when omitted; 409 provider_id_taken when in use"`
	Kind           string     `json:"kind" enum:"oidc,github"`
	Name           string     `json:"name" minLength:"1" maxLength:"100"`
	IssuerURL      string     `json:"issuerUrl,omitempty" maxLength:"500"`
	ClientID       string     `json:"clientId" minLength:"1" maxLength:"500"`
	ClientSecret   string     `json:"clientSecret,omitempty" maxLength:"2000" doc:"Write-only. Required on create; empty on update keeps the stored secret"`
	Scopes         []string   `json:"scopes,omitempty"`
	AllowedDomains []string   `json:"allowedDomains,omitempty"`
	TrustMFA       bool       `json:"trustMfa,omitempty" required:"false"`
	Enabled        bool       `json:"enabled,omitempty" required:"false"`
}

func (b providerInputBody) toInput() auth.ProviderInput {
	in := auth.ProviderInput{
		Kind: b.Kind, Name: b.Name, IssuerURL: b.IssuerURL, ClientID: b.ClientID, ClientSecret: b.ClientSecret,
		Scopes: b.Scopes, AllowedDomains: b.AllowedDomains, TrustMFA: b.TrustMFA, Enabled: b.Enabled,
	}
	if b.ID != nil {
		in.ID = *b.ID
	}
	return in
}

type adminProvidersOutput struct {
	Body struct {
		Items []providerBody `json:"items"`
	}
}

type providerOutput struct{ Body providerBody }

type createProviderInput struct{ Body providerInputBody }

type updateProviderInput struct {
	ID   uuid.UUID `path:"id"`
	Body providerInputBody
}

type providerIDInput struct {
	ID uuid.UUID `path:"id"`
}

type testIssuerInput struct {
	Body struct {
		IssuerURL string `json:"issuerUrl" minLength:"1" maxLength:"500" doc:"The OIDC issuer to read the discovery document of"`
	}
}

type testIssuerBody struct {
	OK                    bool   `json:"ok" doc:"The issuer serves a usable OpenID configuration"`
	Issuer                string `json:"issuer,omitempty" doc:"The issuer the provider reported (on success)"`
	AuthorizationEndpoint string `json:"authorizationEndpoint,omitempty" doc:"Where people are sent to sign in (on success)"`
	Code                  string `json:"code,omitempty" enum:"invalid_issuer,host_not_found,unreachable,timeout,tls_invalid,discovery_not_found,discovery_invalid,issuer_mismatch,discovery_incomplete" doc:"Why the issuer cannot be used (on failure)"`
	Message               string `json:"message,omitempty" doc:"What to fix (on failure)"`
}

type testIssuerOutput struct{ Body testIssuerBody }

// AuthSettingsBody is the instance sign-in settings.
type AuthSettingsBody struct {
	AllowOpenSignup bool `json:"allowOpenSignup" doc:"Let anyone create an account through an identity provider without an invite (default off)"`
}

type authSettingsOutput struct{ Body AuthSettingsBody }

type authSettingsInput struct{ Body AuthSettingsBody }

type teamSecurityInput struct {
	TeamID uuid.UUID `path:"teamId"`
}

// TeamSecurityBody is a team's 2FA requirement.
type TeamSecurityBody struct {
	Require2FA bool `json:"require2fa" doc:"Members must enrol 2FA before acting in this team"`
	Members    int  `json:"members"`
	Enrolled   int  `json:"enrolled" doc:"Members that have 2FA enabled"`
}

type teamSecurityOutput struct{ Body TeamSecurityBody }

type setTeamSecurityInput struct {
	TeamID uuid.UUID `path:"teamId"`
	Body   struct {
		Require2FA bool `json:"require2fa"`
	}
}

// ---- helpers ----

func (c *core) sessionUser(ctx context.Context) (*auth.Principal, error) {
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return nil, err
	}
	if p.Type != auth.TypeUser {
		return nil, apierr.Forbidden()
	}
	return p, nil
}

// confirmingPassword runs fn, which re-checks the signed-in user's password,
// under the per-account wrong-password limit: once 10 wrong passwords have
// landed in the window the user is refused before any hashing, so a stolen
// session cannot guess the password through these endpoints.
func (c *core) confirmingPassword(userID uuid.UUID, fn func() error) error {
	key := "user:" + userID.String()
	if !c.accountLogins.Peek(key) {
		return apierr.TooManyRequests("too many wrong passwords; try again in a few minutes")
	}
	err := fn()
	if ae, ok := apierr.As(err); ok && ae.Code == "invalid_password" {
		c.accountLogins.Allow(key)
	}
	return err
}

func (c *core) instanceAdmin(ctx context.Context) (*auth.Principal, error) {
	p, err := c.sessionUser(ctx)
	if err != nil {
		return nil, err
	}
	ok, err := c.auth.IsInstanceAdmin(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apierr.Forbidden()
	}
	return p, nil
}

func (c *core) providerView(ctx context.Context, p auth.Provider) providerBody {
	return providerBody{
		ID: p.ID, Kind: p.Kind, Name: p.Name, IssuerURL: p.IssuerURL, ClientID: p.ClientID, Scopes: p.Scopes,
		AllowedDomains: p.AllowedDomains, TrustMFA: p.TrustMFA, Enabled: p.Enabled,
		RedirectURI: auth.RedirectURL(baseURL(ctx), p.ID), CreatedAt: p.CreatedAt,
	}
}

func (c *core) record(ctx context.Context, action, resourceType, resourceID string, meta map[string]any, fn func(tx pgx.Tx) error) error {
	return c.auth.InTx(ctx, func(tx pgx.Tx) error {
		if fn != nil {
			if err := fn(tx); err != nil {
				return err
			}
		}
		return recordAudit(ctx, tx, action, resourceType, resourceID, meta)
	})
}

// recordAudit writes an audit row in tx for the principal in ctx. It mirrors
// audit.Record, which this package cannot import (audit registers its HTTP
// endpoints through this package).
func recordAudit(ctx context.Context, tx pgx.Tx, action, resourceType, resourceID string, meta map[string]any) error {
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return err
	}
	actor := "token"
	switch p.Type {
	case auth.TypeUser:
		actor = "user"
	case auth.TypeMCP:
		actor = "mcp"
	}
	if meta == nil {
		meta = map[string]any{}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return db.New(tx).InsertAuditEvent(ctx, db.InsertAuditEventParams{
		WorkspaceID: p.WorkspaceID, ActorType: actor, ActorID: p.ID().String(), ActorLabel: p.Label,
		Action: action, ResourceType: resourceType, ResourceID: resourceID, Metadata: raw,
	})
}

func loginRedirect(code string) *redirectOutput {
	return &redirectOutput{Status: http.StatusFound, Location: "/login?error=" + url.QueryEscape(code), CacheControl: "no-store"}
}

// linkRedirect sends the browser back to Account → Security after a provider
// link; the page reads the outcome from the fragment (`sso=linked` or
// `sso-error=<code>`), which the router leaves alone.
func linkRedirect(fragment string) *redirectOutput {
	return &redirectOutput{Status: http.StatusFound, Location: "/account/security#" + fragment, CacheControl: "no-store"}
}

// ---- registration ----

func (c *core) registerLoginMethods(h huma.API) {
	c.registerSecondFactorLogin(h)
	c.registerProviderLogin(h)
	c.registerSecurity(h)
	c.registerPasskeys(h)
	c.registerAuthAdmin(h)
}

func (c *core) registerSecondFactorLogin(h huma.API) {
	huma.Register(h, Public(huma.Operation{
		OperationID: "verifySecondFactor", Method: http.MethodPost, Path: "/auth/2fa", Tags: []string{"auth"},
		Summary:     "Finish sign-in with a TOTP or recovery code",
		Description: "Follows a login that answered 401 `two_factor_required` (or a provider login that redirected to `/login?step=2fa`). The pending login expires after 5 minutes and after 5 wrong codes; limited to 10 attempts per minute per client IP.",
		Errors:      []int{http.StatusUnauthorized, http.StatusTooManyRequests},
	}), func(ctx context.Context, in *secondFactorInput) (*meOutput, error) {
		if !c.logins.Allow(ClientIP(ctx)) {
			return nil, apierr.TooManyRequests("too many attempts; try again in a minute")
		}
		u, err := c.auth.CompleteSecondFactor(ctx, c.sessions, in.Body.Code)
		if err != nil {
			return nil, err
		}
		body, err := c.startSession(ctx, u)
		if err != nil {
			return nil, err
		}
		c.sessions.Put(ctx, auth.SessionMFAKey, true)
		return &meOutput{Body: body}, nil
	})
}

func (c *core) registerProviderLogin(h huma.API) {
	huma.Register(h, Public(huma.Operation{
		OperationID: "listLoginProviders", Method: http.MethodGet, Path: "/auth/providers", Tags: []string{"auth"},
		Summary: "Enabled single-sign-on providers for the login page",
	}), func(ctx context.Context, _ *struct{}) (*providersOutput, error) {
		items, err := c.auth.PublicProviders(ctx)
		if err != nil {
			return nil, err
		}
		out := &providersOutput{}
		out.Body.Providers = make([]publicProviderBody, 0, len(items))
		for _, p := range items {
			out.Body.Providers = append(out.Body.Providers, publicProviderBody{ID: p.ID, Name: p.Name, Kind: p.Kind})
		}
		return out, nil
	})

	huma.Register(h, Public(huma.Operation{
		OperationID: "startProviderLogin", Method: http.MethodGet, Path: "/auth/oidc/{providerId}/start", Tags: []string{"auth"},
		Summary:       "Redirect to the identity provider",
		DefaultStatus: http.StatusFound,
		Errors:        []int{http.StatusNotFound, http.StatusTooManyRequests},
	}), func(ctx context.Context, in *providerStartInput) (*redirectOutput, error) {
		if !c.logins.Allow(ClientIP(ctx)) {
			return loginRedirect("rate_limited"), nil
		}
		dest, err := c.auth.BeginProviderLogin(ctx, c.sessions, in.ProviderID, baseURL(ctx), in.Invite)
		if err != nil {
			slog.WarnContext(ctx, "begin provider login", "provider", in.ProviderID, "error", err)
			return loginRedirect("oidc_failed"), nil
		}
		return &redirectOutput{Status: http.StatusFound, Location: dest, CacheControl: "no-store"}, nil
	})

	huma.Register(h, Public(huma.Operation{
		OperationID: "providerLoginCallback", Method: http.MethodGet, Path: "/auth/oidc/{providerId}/callback", Tags: []string{"auth"},
		Summary:       "OAuth/OIDC redirect target of the identity provider",
		Description:   "Completes the sign-in and redirects to `/` (or `/login?step=2fa` / `/login?error=<code>`). A provider never links itself to an existing account by email (`account_exists`): a flow the signed-in user started in Account → Security attaches the identity to them and redirects to `/account/security#sso=linked` (or `#sso-error=<code>`).",
		DefaultStatus: http.StatusFound,
	}), func(ctx context.Context, in *providerCallbackInput) (*redirectOutput, error) {
		linking := auth.ProviderLinkPending(ctx, c.sessions)
		refuse := func(code string) *redirectOutput {
			if linking {
				return linkRedirect("sso-error=" + url.QueryEscape(code))
			}
			return loginRedirect(code)
		}
		if in.ProviderError != "" {
			return refuse("oidc_denied"), nil
		}
		res, err := c.auth.CompleteProviderLogin(ctx, c.sessions, in.ProviderID, baseURL(ctx), in.State, in.Code)
		if err != nil {
			if ae, ok := apierr.As(err); ok {
				slog.InfoContext(ctx, "provider login refused", "provider", in.ProviderID, "code", ae.Code, "detail", err.Error())
				return refuse(ae.Code), nil
			}
			slog.ErrorContext(ctx, "provider login", "provider", in.ProviderID, "error", err)
			return refuse("oidc_failed"), nil
		}
		if res.LinkOnly {
			if res.Linked {
				if p, ok := auth.PrincipalFrom(ctx); ok && p.WorkspaceID != uuid.Nil {
					if err := c.record(ctx, "auth.identity.link", "user", res.User.ID.String(), map[string]any{"providerId": in.ProviderID.String()}, nil); err != nil {
						slog.ErrorContext(ctx, "audit identity link", "error", err)
					}
				}
			}
			return linkRedirect("sso=linked"), nil
		}
		if res.NeedsSecondFactor {
			return &redirectOutput{Status: http.StatusFound, Location: "/login?step=2fa", CacheControl: "no-store"}, nil
		}
		if _, err := c.startSession(ctx, res.User); err != nil {
			slog.ErrorContext(ctx, "provider login: start session", "error", err)
			return loginRedirect("login_failed"), nil
		}
		// A session that was already signed in as someone else must not carry its
		// second-factor mark over to this person.
		c.sessions.Remove(ctx, auth.SessionMFAKey)
		if res.TrustedMFA {
			c.sessions.Put(ctx, auth.SessionMFAKey, true)
		}
		dest := "/"
		if res.Invite != "" && !res.Created {
			dest = "/invite/" + url.PathEscape(res.Invite)
		}
		return &redirectOutput{Status: http.StatusFound, Location: dest, CacheControl: "no-store"}, nil
	})
}

func (c *core) registerSecurity(h huma.API) {
	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "getMySecurity", Method: http.MethodGet, Path: "/me/security", Tags: []string{"me"},
		Summary: "Two-factor and linked-account status of the signed-in user",
		Errors:  []int{http.StatusUnauthorized, http.StatusForbidden},
	}), func(ctx context.Context, _ *struct{}) (*securityOutput, error) {
		p, err := c.sessionUser(ctx)
		if err != nil {
			return nil, err
		}
		st, err := c.auth.SecurityStatus(ctx, p.UserID)
		if err != nil {
			return nil, err
		}
		blocked, err := c.auth.SecondFactorBlocked(ctx, c.sessions, p)
		if err != nil {
			return nil, err
		}
		admin, err := c.auth.IsInstanceAdmin(ctx, p.UserID)
		if err != nil {
			return nil, err
		}
		b := SecurityBody{
			TOTPEnabled: st.TOTPEnabled, TOTPPending: st.TOTPPending, RecoveryCodesLeft: st.RecoveryCodesLeft,
			HasPassword: st.HasPassword, SessionMFA: c.sessions.GetBool(ctx, auth.SessionMFAKey), EnrollmentNeeded: blocked,
			RequiredByTeams: make([]requiredTeamBody, 0, len(st.RequiredByTeams)), Identities: make([]identityBody, 0, len(st.Identities)),
			IsInstanceAdmin: admin, Passkeys: make([]passkeyBody, 0, len(st.Passkeys)),
			PasskeysUnavailable: c.auth.PasskeyUnavailable(baseURL(ctx)),
		}
		for _, k := range st.Passkeys {
			b.Passkeys = append(b.Passkeys, passkeyView(k))
		}
		for _, t := range st.RequiredByTeams {
			b.RequiredByTeams = append(b.RequiredByTeams, requiredTeamBody{ID: t.ID, Name: t.Name})
		}
		for _, i := range st.Identities {
			b.Identities = append(b.Identities, identityBody{ID: i.ID, ProviderID: i.ProviderID, ProviderName: i.ProviderName, ProviderKind: i.ProviderKind, Email: i.Email, LinkedAt: i.LinkedAt, LastLoginAt: i.LastLoginAt})
		}
		return &securityOutput{Body: b}, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "startTotpEnrolment", Method: http.MethodPost, Path: "/me/2fa/setup", Tags: []string{"me"},
		Summary:     "Generate a TOTP secret to add to an authenticator app",
		Description: "Replaces any unconfirmed secret. Confirm it with POST /me/2fa/enable. Returns 409 when 2FA is already on.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict},
	}), func(ctx context.Context, _ *struct{}) (*totpSetupOutput, error) {
		p, err := c.sessionUser(ctx)
		if err != nil {
			return nil, err
		}
		u, err := c.auth.UserByID(ctx, p.UserID)
		if err != nil {
			return nil, apierr.Unauthorized()
		}
		e, err := c.auth.StartTOTPEnrolment(ctx, u)
		if err != nil {
			return nil, err
		}
		out := &totpSetupOutput{}
		out.Body.Secret, out.Body.URI = e.Secret, e.URI
		return out, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "enableTotp", Method: http.MethodPost, Path: "/me/2fa/enable", Tags: []string{"me"},
		Summary:     "Confirm the authenticator and turn 2FA on",
		Description: "Returns the 10 single-use recovery codes once. This session counts as second-factor verified afterwards.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
	}), func(ctx context.Context, in *totpEnableInput) (*recoveryCodesOutput, error) {
		p, err := c.sessionUser(ctx)
		if err != nil {
			return nil, err
		}
		var codes []string
		err = c.record(ctx, "auth.2fa.enable", "user", p.UserID.String(), nil, func(tx pgx.Tx) error {
			var err error
			codes, err = c.auth.EnableTOTP(ctx, tx, p.UserID, in.Body.Code)
			return err
		})
		if err != nil {
			return nil, err
		}
		c.sessions.Put(ctx, auth.SessionMFAKey, true)
		out := &recoveryCodesOutput{}
		out.Body.RecoveryCodes = codes
		return out, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "disableTotp", Method: http.MethodPost, Path: "/me/2fa/disable", Tags: []string{"me"},
		Summary:       "Turn 2FA off (needs a code, and the password when the account has one)",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusTooManyRequests, http.StatusUnprocessableEntity},
	}), func(ctx context.Context, in *totpDisableInput) (*struct{}, error) {
		p, err := c.sessionUser(ctx)
		if err != nil {
			return nil, err
		}
		err = c.confirmingPassword(p.UserID, func() error {
			return c.record(ctx, "auth.2fa.disable", "user", p.UserID.String(), nil, func(tx pgx.Tx) error {
				return c.auth.DisableTOTP(ctx, tx, p.UserID, in.Body.Code, in.Body.Password)
			})
		})
		if err != nil {
			return nil, err
		}
		c.sessions.Remove(ctx, auth.SessionMFAKey)
		return nil, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "regenerateRecoveryCodes", Method: http.MethodPost, Path: "/me/2fa/recovery-codes", Tags: []string{"me"},
		Summary:     "Replace the recovery codes (needs a current code)",
		Description: "Invalidates every previous code and returns 10 new ones, shown once.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusTooManyRequests, http.StatusUnprocessableEntity},
	}), func(ctx context.Context, in *regenerateRecoveryCodesInput) (*recoveryCodesOutput, error) {
		p, err := c.sessionUser(ctx)
		if err != nil {
			return nil, err
		}
		var codes []string
		err = c.confirmingPassword(p.UserID, func() error {
			return c.record(ctx, "auth.recovery_codes.regenerate", "user", p.UserID.String(), nil, func(tx pgx.Tx) error {
				var err error
				codes, err = c.auth.RegenerateRecoveryCodes(ctx, tx, p.UserID, in.Body.Code, in.Body.Password)
				return err
			})
		})
		if err != nil {
			return nil, err
		}
		out := &recoveryCodesOutput{}
		out.Body.RecoveryCodes = codes
		return out, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "startIdentityLink", Method: http.MethodPost, Path: "/me/identities/link", Tags: []string{"me"},
		Summary: "Start linking a single-sign-on account to the signed-in user",
		Description: "Returns the identity provider's sign-in URL; the browser goes there and comes back to `/account/security`. " +
			"The identity is attached to the signed-in user only when they start it here: signing in with a provider never links it to an existing account by email. " +
			"An account with a password must send it (422 `invalid_password`); wrong passwords are limited to 10 per 10 minutes per user.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests, http.StatusUnprocessableEntity},
	}), func(ctx context.Context, in *startIdentityLinkInput) (*startIdentityLinkOutput, error) {
		p, err := c.sessionUser(ctx)
		if err != nil {
			return nil, err
		}
		if err := c.confirmingPassword(p.UserID, func() error {
			return c.auth.ConfirmPassword(ctx, p.UserID, in.Body.Password)
		}); err != nil {
			return nil, err
		}
		dest, err := c.auth.BeginProviderLink(ctx, c.sessions, in.Body.ProviderID, baseURL(ctx), p.UserID)
		if err != nil {
			return nil, err
		}
		out := &startIdentityLinkOutput{}
		out.Body.URL = dest
		return out, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "unlinkIdentity", Method: http.MethodDelete, Path: "/me/identities/{id}", Tags: []string{"me"},
		Summary: "Unlink a single-sign-on account", DefaultStatus: http.StatusNoContent,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
	}), func(ctx context.Context, in *unlinkIdentityInput) (*struct{}, error) {
		p, err := c.sessionUser(ctx)
		if err != nil {
			return nil, err
		}
		return nil, c.record(ctx, "auth.identity.unlink", "user", p.UserID.String(), map[string]any{"identityId": in.ID.String()}, func(tx pgx.Tx) error {
			return c.auth.UnlinkIdentity(ctx, tx, p.UserID, in.ID)
		})
	})
}

func (c *core) registerAuthAdmin(h huma.API) {
	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "listAuthProviders", Method: http.MethodGet, Path: "/auth-providers", Tags: []string{"auth"},
		Summary: "Configured single-sign-on providers (instance admin)",
		Errors:  []int{http.StatusUnauthorized, http.StatusForbidden},
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*adminProvidersOutput, error) {
		if _, err := c.instanceAdmin(ctx); err != nil {
			return nil, err
		}
		items, err := c.auth.ListProviders(ctx)
		if err != nil {
			return nil, err
		}
		out := &adminProvidersOutput{}
		out.Body.Items = make([]providerBody, 0, len(items))
		for _, p := range items {
			out.Body.Items = append(out.Body.Items, c.providerView(ctx, p))
		}
		return out, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "createAuthProvider", Method: http.MethodPost, Path: "/auth-providers", Tags: []string{"auth"},
		Summary:       "Add a single-sign-on provider (instance admin)",
		Description:   "OIDC issuers are checked through discovery (`/.well-known/openid-configuration`) before saving.",
		DefaultStatus: http.StatusCreated,
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity},
	}, policy.ScopeAdmin), func(ctx context.Context, in *createProviderInput) (*providerOutput, error) {
		if _, err := c.instanceAdmin(ctx); err != nil {
			return nil, err
		}
		var created auth.Provider
		err := c.auth.InTx(ctx, func(tx pgx.Tx) error {
			var err error
			created, err = c.auth.CreateProvider(ctx, tx, in.Body.toInput())
			if err != nil {
				return err
			}
			return recordAudit(ctx, tx, "auth.provider.create", "auth_provider", created.ID.String(),
				map[string]any{"name": created.Name, "kind": created.Kind})
		})
		if err != nil {
			return nil, err
		}
		return &providerOutput{Body: c.providerView(ctx, created)}, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "updateAuthProvider", Method: http.MethodPut, Path: "/auth-providers/{id}", Tags: []string{"auth"},
		Summary: "Replace a single-sign-on provider's settings (instance admin)",
		Errors:  []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, policy.ScopeAdmin), func(ctx context.Context, in *updateProviderInput) (*providerOutput, error) {
		if _, err := c.instanceAdmin(ctx); err != nil {
			return nil, err
		}
		var updated auth.Provider
		err := c.auth.InTx(ctx, func(tx pgx.Tx) error {
			var err error
			updated, err = c.auth.UpdateProvider(ctx, tx, in.ID, in.Body.toInput())
			if err != nil {
				return err
			}
			return recordAudit(ctx, tx, "auth.provider.update", "auth_provider", updated.ID.String(),
				map[string]any{"name": updated.Name, "secretChanged": in.Body.ClientSecret != ""})
		})
		if err != nil {
			return nil, err
		}
		return &providerOutput{Body: c.providerView(ctx, updated)}, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "deleteAuthProvider", Method: http.MethodDelete, Path: "/auth-providers/{id}", Tags: []string{"auth"},
		Summary: "Remove a single-sign-on provider and its linked identities (instance admin)", DefaultStatus: http.StatusNoContent,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, policy.ScopeAdmin), func(ctx context.Context, in *providerIDInput) (*struct{}, error) {
		if _, err := c.instanceAdmin(ctx); err != nil {
			return nil, err
		}
		return nil, c.record(ctx, "auth.provider.delete", "auth_provider", in.ID.String(), nil, func(tx pgx.Tx) error {
			return c.auth.DeleteProvider(ctx, tx, in.ID)
		})
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "testAuthProvider", Method: http.MethodPost, Path: "/instance/sign-in-providers/test", Tags: []string{"auth"},
		Summary: "Test an OpenID Connect issuer without saving it (instance admin)",
		Description: "Reads `/.well-known/openid-configuration` of the issuer the way saving a provider does and checks that the document names the same issuer and has " +
			"the authorization endpoint, token endpoint and key set. Answers 200 either way: `ok` is false with a specific `code` and `message` when the issuer cannot be used.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden},
	}, policy.ScopeAdmin), func(ctx context.Context, in *testIssuerInput) (*testIssuerOutput, error) {
		if _, err := c.instanceAdmin(ctx); err != nil {
			return nil, err
		}
		res, err := c.auth.TestIssuer(ctx, in.Body.IssuerURL)
		out := &testIssuerOutput{}
		var ip *auth.IssuerProblem
		switch {
		case err == nil:
			out.Body = testIssuerBody{OK: true, Issuer: res.Issuer, AuthorizationEndpoint: res.AuthorizationEndpoint}
		case errors.As(err, &ip):
			out.Body = testIssuerBody{Code: ip.Code, Message: ip.Message}
		default:
			return nil, err
		}
		return out, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "getAuthSettings", Method: http.MethodGet, Path: "/auth-settings", Tags: []string{"auth"},
		Summary: "Instance sign-in settings (instance admin)",
		Errors:  []int{http.StatusUnauthorized, http.StatusForbidden},
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*authSettingsOutput, error) {
		if _, err := c.instanceAdmin(ctx); err != nil {
			return nil, err
		}
		s, err := c.auth.GetAuthSettings(ctx)
		if err != nil {
			return nil, err
		}
		return &authSettingsOutput{Body: AuthSettingsBody{AllowOpenSignup: s.AllowOpenSignup}}, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "updateAuthSettings", Method: http.MethodPut, Path: "/auth-settings", Tags: []string{"auth"},
		Summary: "Change instance sign-in settings (instance admin)",
		Errors:  []int{http.StatusUnauthorized, http.StatusForbidden},
	}, policy.ScopeAdmin), func(ctx context.Context, in *authSettingsInput) (*authSettingsOutput, error) {
		if _, err := c.instanceAdmin(ctx); err != nil {
			return nil, err
		}
		var out auth.AuthSettings
		err := c.record(ctx, "auth.settings.update", "instance", "auth", map[string]any{"allowOpenSignup": in.Body.AllowOpenSignup}, func(tx pgx.Tx) error {
			var err error
			out, err = c.auth.SetAuthSettings(ctx, tx, auth.AuthSettings{AllowOpenSignup: in.Body.AllowOpenSignup})
			return err
		})
		if err != nil {
			return nil, err
		}
		return &authSettingsOutput{Body: AuthSettingsBody{AllowOpenSignup: out.AllowOpenSignup}}, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "getTeamSecurity", Method: http.MethodGet, Path: "/teams/{teamId}/security", Tags: []string{"teams"},
		Summary: "A team's two-factor requirement",
		Errors:  []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, policy.ScopeAdmin), func(ctx context.Context, in *teamSecurityInput) (*teamSecurityOutput, error) {
		if err := c.teamAdmin(ctx, in.TeamID); err != nil {
			return nil, err
		}
		s, err := c.auth.GetTeamSecurity(ctx, in.TeamID)
		if err != nil {
			return nil, err
		}
		return &teamSecurityOutput{Body: TeamSecurityBody{Require2FA: s.Require2FA, Members: s.Members, Enrolled: s.Enrolled}}, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "setTeamSecurity", Method: http.MethodPut, Path: "/teams/{teamId}/security", Tags: []string{"teams"},
		Summary:     "Require two-factor authentication of every member of the team",
		Description: "Members without 2FA are blocked from acting in the team until they enrol. The caller must have 2FA themselves (409 `enrol_first`).",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
	}, policy.ScopeAdmin), func(ctx context.Context, in *setTeamSecurityInput) (*teamSecurityOutput, error) {
		if err := c.teamAdmin(ctx, in.TeamID); err != nil {
			return nil, err
		}
		p, _ := policy.Authenticated(ctx)
		if in.Body.Require2FA && p.Type == auth.TypeUser {
			has, err := c.auth.HasSecondFactor(ctx, p.UserID)
			if err != nil {
				return nil, err
			}
			if !has && !c.sessions.GetBool(ctx, auth.SessionMFAKey) {
				return nil, apierr.Conflict("enrol_first", "set up two-factor authentication on your own account before requiring it of the team")
			}
		}
		err := c.record(ctx, "team.require_2fa", "team", in.TeamID.String(), map[string]any{"enabled": in.Body.Require2FA}, func(tx pgx.Tx) error {
			return c.auth.SetTeamRequireTwoFactor(ctx, tx, in.TeamID, in.Body.Require2FA)
		})
		if err != nil {
			return nil, err
		}
		s, err := c.auth.GetTeamSecurity(ctx, in.TeamID)
		if err != nil {
			return nil, err
		}
		return &teamSecurityOutput{Body: TeamSecurityBody{Require2FA: s.Require2FA, Members: s.Members, Enrolled: s.Enrolled}}, nil
	})
}

// teamAdmin authorises an owner/admin of the current team acting on that team.
func (c *core) teamAdmin(ctx context.Context, teamID uuid.UUID) error {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return err
	}
	if policy.WorkspaceID(ctx) != teamID {
		return apierr.NotFound("team")
	}
	return nil
}
