package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
)

// ---- types ----

type passkeyBody struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	Synced     bool       `json:"synced" doc:"The authenticator reports the passkey as backed up (it syncs between devices); false means it is bound to one device"`
	Transports []string   `json:"transports" doc:"How the authenticator connects (internal, usb, nfc, ble, hybrid)"`
}

func passkeyView(p auth.PasskeyInfo) passkeyBody {
	t := p.Transports
	if t == nil {
		t = []string{}
	}
	return passkeyBody{ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt, LastUsedAt: p.LastUsedAt, Synced: p.Synced, Transports: t}
}

// webauthnOptionsOutput carries WebAuthn options in the JSON form the browser's
// PublicKeyCredential.parse*OptionsFromJSON reads, under "publicKey".
type webauthnOptionsOutput struct {
	Body json.RawMessage `doc:"WebAuthn PublicKeyCredentialCreationOptions or PublicKeyCredentialRequestOptions wrapped as {publicKey: {...}}"`
}

type webauthnAssertionInput struct {
	Body json.RawMessage `doc:"The AuthenticationResponseJSON of the authenticator (PublicKeyCredential.toJSON())"`
}

type secondFactorMethodsOutput struct {
	Body struct {
		TOTP    bool `json:"totp" doc:"An authenticator-app or recovery code can finish the sign-in"`
		Passkey bool `json:"passkey" doc:"A passkey can finish the sign-in"`
	}
}

type passkeyRegisterStartInput struct {
	Body struct {
		Password string `json:"password,omitempty" maxLength:"256" doc:"Required when the account has a password"`
	}
}

type passkeyRegisterFinishInput struct {
	Body struct {
		Name       string          `json:"name" minLength:"1" maxLength:"60" doc:"What to call this passkey, for example 'MacBook' or 'YubiKey'"`
		Credential json.RawMessage `json:"credential" doc:"The RegistrationResponseJSON of the authenticator (PublicKeyCredential.toJSON())"`
	}
}

type passkeyRegisterFinishOutput struct {
	Body struct {
		Passkey       passkeyBody `json:"passkey"`
		RecoveryCodes []string    `json:"recoveryCodes,omitempty" doc:"Issued when the account had no unused recovery code left; single use, shown once"`
	}
}

type passkeysOutput struct {
	Body struct {
		Passkeys []passkeyBody `json:"passkeys"`
	}
}

type passkeyRenameInput struct {
	ID   uuid.UUID `path:"id"`
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"60"`
	}
}

type passkeyDeleteInput struct {
	ID   uuid.UUID `path:"id"`
	Body struct {
		Password string `json:"password,omitempty" maxLength:"256" doc:"Required when the account has a password"`
	}
}

// ---- registration ----

func (c *core) registerPasskeys(h huma.API) {
	c.registerPasskeyLogin(h)
	c.registerPasskeySecondFactor(h)
	c.registerPasskeyManagement(h)
}

func optionsOutput(v any) (*webauthnOptionsOutput, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode webauthn options: %w", err)
	}
	return &webauthnOptionsOutput{Body: raw}, nil
}

func (c *core) registerPasskeyLogin(h huma.API) {
	huma.Register(h, Public(huma.Operation{
		OperationID: "passkeyLoginOptions", Method: http.MethodPost, Path: "/auth/passkey/options", Tags: []string{"auth"},
		Summary:     "Start a passwordless sign-in with a passkey",
		Description: "Returns the WebAuthn request options for `navigator.credentials.get`; the answer goes to `POST /auth/passkey/login`. The options expire after 5 minutes. Limited to 10 attempts per minute per client IP.",
		Errors:      []int{http.StatusUnprocessableEntity, http.StatusTooManyRequests},
	}), func(ctx context.Context, _ *struct{}) (*webauthnOptionsOutput, error) {
		if !c.logins.Allow(ClientIP(ctx)) {
			return nil, apierr.TooManyRequests("too many attempts; try again in a minute")
		}
		a, err := c.auth.BeginPasskeyLogin(ctx, c.sessions, baseURL(ctx))
		if err != nil {
			return nil, err
		}
		return optionsOutput(a)
	})

	huma.Register(h, Public(huma.Operation{
		OperationID: "passkeyLogin", Method: http.MethodPost, Path: "/auth/passkey/login", Tags: []string{"auth"},
		Summary:     "Finish a passwordless sign-in with a passkey (sets the session cookie)",
		Description: "A passkey verified on the authenticator (PIN or biometric) counts as two factors: the session is second-factor verified and no code is asked for.",
		Errors:      []int{http.StatusUnauthorized, http.StatusUnprocessableEntity, http.StatusTooManyRequests},
	}), func(ctx context.Context, in *webauthnAssertionInput) (*meOutput, error) {
		if !c.logins.Allow(ClientIP(ctx)) {
			return nil, apierr.TooManyRequests("too many attempts; try again in a minute")
		}
		u, err := c.auth.FinishPasskeyLogin(ctx, c.sessions, baseURL(ctx), in.Body)
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

func (c *core) registerPasskeySecondFactor(h huma.API) {
	huma.Register(h, Public(huma.Operation{
		OperationID: "getSecondFactorMethods", Method: http.MethodGet, Path: "/auth/2fa", Tags: []string{"auth"},
		Summary: "What can finish the pending sign-in",
		Errors:  []int{http.StatusUnauthorized},
	}), func(ctx context.Context, _ *struct{}) (*secondFactorMethodsOutput, error) {
		totp, passkey, err := c.auth.SecondFactorMethods(ctx, c.sessions)
		if err != nil {
			return nil, err
		}
		out := &secondFactorMethodsOutput{}
		out.Body.TOTP, out.Body.Passkey = totp, passkey
		return out, nil
	})

	huma.Register(h, Public(huma.Operation{
		OperationID: "passkeySecondFactorOptions", Method: http.MethodPost, Path: "/auth/2fa/passkey/options", Tags: []string{"auth"},
		Summary:     "Ask the account's passkeys to finish a pending sign-in",
		Description: "Follows a login that answered 401 `two_factor_required`. Returns the WebAuthn request options for `navigator.credentials.get`; the answer goes to `POST /auth/2fa/passkey`.",
		Errors:      []int{http.StatusUnauthorized, http.StatusConflict, http.StatusTooManyRequests},
	}), func(ctx context.Context, _ *struct{}) (*webauthnOptionsOutput, error) {
		if !c.logins.Allow(ClientIP(ctx)) {
			return nil, apierr.TooManyRequests("too many attempts; try again in a minute")
		}
		a, err := c.auth.BeginPasskeySecondFactor(ctx, c.sessions, baseURL(ctx))
		if err != nil {
			return nil, err
		}
		return optionsOutput(a)
	})

	huma.Register(h, Public(huma.Operation{
		OperationID: "passkeySecondFactor", Method: http.MethodPost, Path: "/auth/2fa/passkey", Tags: []string{"auth"},
		Summary:     "Finish sign-in with a passkey as the second factor",
		Description: "The pending login expires after 5 minutes and after 5 wrong answers; limited to 10 attempts per minute per client IP.",
		Errors:      []int{http.StatusUnauthorized, http.StatusTooManyRequests},
	}), func(ctx context.Context, in *webauthnAssertionInput) (*meOutput, error) {
		if !c.logins.Allow(ClientIP(ctx)) {
			return nil, apierr.TooManyRequests("too many attempts; try again in a minute")
		}
		u, err := c.auth.CompletePasskeySecondFactor(ctx, c.sessions, baseURL(ctx), in.Body)
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

func (c *core) registerPasskeyManagement(h huma.API) {
	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "listPasskeys", Method: http.MethodGet, Path: "/me/passkeys", Tags: []string{"me"},
		Summary: "The signed-in user's passkeys",
		Errors:  []int{http.StatusUnauthorized, http.StatusForbidden},
	}), func(ctx context.Context, _ *struct{}) (*passkeysOutput, error) {
		p, err := c.sessionUser(ctx)
		if err != nil {
			return nil, err
		}
		items, err := c.auth.ListPasskeys(ctx, p.UserID)
		if err != nil {
			return nil, err
		}
		out := &passkeysOutput{}
		out.Body.Passkeys = make([]passkeyBody, 0, len(items))
		for _, i := range items {
			out.Body.Passkeys = append(out.Body.Passkeys, passkeyView(i))
		}
		return out, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "startPasskeyRegistration", Method: http.MethodPost, Path: "/me/passkeys/options", Tags: []string{"me"},
		Summary:     "Start adding a passkey",
		Description: "Returns the WebAuthn creation options for `navigator.credentials.create`; the answer goes to `POST /me/passkeys`. Needs the password again when the account has one. Passkeys are discoverable credentials with user verification required.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusTooManyRequests, http.StatusUnprocessableEntity},
	}), func(ctx context.Context, in *passkeyRegisterStartInput) (*webauthnOptionsOutput, error) {
		p, err := c.sessionUser(ctx)
		if err != nil {
			return nil, err
		}
		var cr *protocol.CredentialCreation
		err = c.confirmingPassword(p.UserID, func() error {
			var err error
			cr, err = c.auth.BeginPasskeyRegistration(ctx, c.sessions, p.UserID, baseURL(ctx), in.Body.Password)
			return err
		})
		if err != nil {
			return nil, err
		}
		return optionsOutput(cr)
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "finishPasskeyRegistration", Method: http.MethodPost, Path: "/me/passkeys", Tags: []string{"me"},
		Summary:       "Finish adding a passkey",
		Description:   "Counts as a second factor for the team 2FA requirement. When the account has no unused recovery code left, a fresh set is issued and returned once.",
		DefaultStatus: http.StatusCreated,
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
	}), func(ctx context.Context, in *passkeyRegisterFinishInput) (*passkeyRegisterFinishOutput, error) {
		p, err := c.sessionUser(ctx)
		if err != nil {
			return nil, err
		}
		var (
			info  *auth.PasskeyInfo
			codes []string
		)
		err = c.record(ctx, "auth.passkey.add", "user", p.UserID.String(), map[string]any{"name": in.Body.Name}, func(tx pgx.Tx) error {
			var err error
			info, codes, err = c.auth.FinishPasskeyRegistration(ctx, tx, c.sessions, p.UserID, baseURL(ctx), in.Body.Name, in.Body.Credential)
			return err
		})
		if err != nil {
			return nil, err
		}
		// A passkey verified just now is as good as a code for this session.
		c.sessions.Put(ctx, auth.SessionMFAKey, true)
		out := &passkeyRegisterFinishOutput{}
		out.Body.Passkey = passkeyView(*info)
		out.Body.RecoveryCodes = codes
		return out, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "renamePasskey", Method: http.MethodPatch, Path: "/me/passkeys/{id}", Tags: []string{"me"},
		Summary:       "Rename a passkey",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
	}), func(ctx context.Context, in *passkeyRenameInput) (*struct{}, error) {
		p, err := c.sessionUser(ctx)
		if err != nil {
			return nil, err
		}
		return nil, c.record(ctx, "auth.passkey.rename", "user", p.UserID.String(), map[string]any{"passkeyId": in.ID.String(), "name": in.Body.Name}, func(tx pgx.Tx) error {
			return c.auth.RenamePasskey(ctx, tx, p.UserID, in.ID, in.Body.Name)
		})
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "deletePasskey", Method: http.MethodDelete, Path: "/me/passkeys/{id}", Tags: []string{"me"},
		Summary:       "Remove a passkey (needs the password when the account has one)",
		Description:   "Fails with 409 `last_sign_in_method` when it is the account's only way to sign in.",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusTooManyRequests, http.StatusUnprocessableEntity},
	}), func(ctx context.Context, in *passkeyDeleteInput) (*struct{}, error) {
		p, err := c.sessionUser(ctx)
		if err != nil {
			return nil, err
		}
		return nil, c.confirmingPassword(p.UserID, func() error {
			return c.record(ctx, "auth.passkey.remove", "user", p.UserID.String(), map[string]any{"passkeyId": in.ID.String()}, func(tx pgx.Tx) error {
				return c.auth.DeletePasskey(ctx, tx, p.UserID, in.ID, in.Body.Password)
			})
		})
	})
}
