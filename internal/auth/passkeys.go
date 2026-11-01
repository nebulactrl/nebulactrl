package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/alexedwards/scs/v2"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// Passkeys are WebAuthn discoverable credentials. They work two ways:
//
//   - passwordless sign-in: the browser offers the account's passkeys, the user
//     verifies on the authenticator (PIN, biometric) and the session starts. A
//     verified passkey is already two factors, so the session counts as
//     second-factor verified;
//   - second factor: after a correct password the login parks in the session
//     (see BeginSecondFactor) and a passkey assertion finishes it, like a TOTP or
//     recovery code does.
//
// The relying party ID and origin come from the public URL (or, in development
// without one, the request's origin). WebAuthn needs a domain name over HTTPS, or
// localhost.

const (
	// MaxPasskeys is the number of passkeys one account may register.
	MaxPasskeys = 20

	// sessionPasskeyCeremony holds the state of an in-flight registration or
	// sign-in.
	sessionPasskeyCeremony = "webauthn_ceremony"

	passkeyCeremonyTTL = 5 * time.Minute

	ceremonyRegister = "register"
	ceremonyLogin    = "login"
	ceremonySecond   = "second"
)

// Errors of the passkey flows.
var (
	ErrPasskeyFailed  = apierr.New(http.StatusUnauthorized, "passkey_failed", "that passkey could not be verified; try again or use another way to sign in")
	ErrNoCeremony     = apierr.New(http.StatusUnauthorized, "no_pending_passkey", "the passkey prompt expired; start again")
	ErrPasskeyInvalid = apierr.Unprocessable("passkey_invalid", "the browser's passkey response could not be read")
)

// ceremony is the state between the begin and finish call of one WebAuthn
// ceremony. It lives in the (server-side) session and is single use.
type ceremony struct {
	Kind  string               `json:"k"`
	Until int64                `json:"u"`
	Data  webauthn.SessionData `json:"d"`
}

// PasskeyInfo is a registered passkey as the user sees it.
type PasskeyInfo struct {
	ID         uuid.UUID
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	// Synced is true when the authenticator reports the credential as backed up
	// (a synced passkey) rather than bound to one device.
	Synced     bool
	Transports []string
}

// waUser adapts an account to webauthn.User.
type waUser struct {
	id    []byte
	name  string
	disp  string
	creds []webauthn.Credential
}

func (u *waUser) WebAuthnID() []byte                         { return u.id }
func (u *waUser) WebAuthnName() string                       { return u.name }
func (u *waUser) WebAuthnDisplayName() string                { return u.disp }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// userHandle is the opaque WebAuthn user handle: the 16 raw bytes of the user ID
// (random, no personal data, within the 64-byte limit).
func userHandle(id uuid.UUID) []byte { return id[:] }

func (s *Service) relyingParty(base string) (*webauthn.WebAuthn, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" {
		return nil, apierr.Unprocessable("passkeys_unavailable", "passkeys need NEBULACTRL_PUBLIC_URL to be set to the address you open the dashboard at")
	}
	host := strings.ToLower(u.Hostname())
	local := host == "localhost" || strings.HasSuffix(host, ".localhost")
	switch {
	case net.ParseIP(host) != nil:
		return nil, apierr.Unprocessable("passkeys_unavailable", "passkeys need a domain name; the dashboard is opened by IP address, which browsers do not allow")
	case u.Scheme != "https" && !(u.Scheme == "http" && local):
		return nil, apierr.Unprocessable("passkeys_unavailable", "passkeys need the dashboard served over HTTPS")
	}
	t := webauthn.TimeoutConfig{Enforce: true, Timeout: passkeyCeremonyTTL, TimeoutUVD: passkeyCeremonyTTL}
	rk := protocol.ResidentKeyRequired()
	wa, err := webauthn.New(&webauthn.Config{
		RPID:                  host,
		RPDisplayName:         Issuer,
		RPOrigins:             []string{u.Scheme + "://" + u.Host},
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			RequireResidentKey: rk,
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			UserVerification:   protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{Login: t, Registration: t},
	})
	if err != nil {
		return nil, apierr.Unprocessable("passkeys_unavailable", "passkeys are not available at this address: "+err.Error())
	}
	return wa, nil
}

func (s *Service) putCeremony(ctx context.Context, sm *scs.SessionManager, kind string, data *webauthn.SessionData) error {
	raw, err := json.Marshal(ceremony{Kind: kind, Until: s.methods.clock().Add(passkeyCeremonyTTL).Unix(), Data: *data})
	if err != nil {
		return fmt.Errorf("encode ceremony: %w", err)
	}
	sm.Put(ctx, sessionPasskeyCeremony, string(raw))
	return nil
}

// takeCeremony returns the stored ceremony of the given kind and always clears it.
func (s *Service) takeCeremony(ctx context.Context, sm *scs.SessionManager, kind string) (*webauthn.SessionData, error) {
	raw := sm.PopString(ctx, sessionPasskeyCeremony)
	if raw == "" {
		return nil, ErrNoCeremony
	}
	var c ceremony
	if err := json.Unmarshal([]byte(raw), &c); err != nil || c.Kind != kind || s.methods.clock().Unix() > c.Until {
		return nil, ErrNoCeremony
	}
	return &c.Data, nil
}

// loadPasskeyUser loads the account and its credentials in WebAuthn form.
func (s *Service) loadPasskeyUser(ctx context.Context, q *db.Queries, userID uuid.UUID) (*waUser, error) {
	u, err := q.GetUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	rows, err := q.ListUserPasskeys(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	wu := &waUser{id: userHandle(u.ID), name: u.Email, disp: u.Name, creds: make([]webauthn.Credential, 0, len(rows))}
	if wu.disp == "" {
		wu.disp = u.Email
	}
	for _, r := range rows {
		var c webauthn.Credential
		if err := json.Unmarshal(r.Credential, &c); err != nil {
			return nil, fmt.Errorf("decode passkey %s: %w", r.ID, err)
		}
		wu.creds = append(wu.creds, c)
	}
	return wu, nil
}

func descriptors(creds []webauthn.Credential) []protocol.CredentialDescriptor {
	out := make([]protocol.CredentialDescriptor, 0, len(creds))
	for _, c := range creds {
		out = append(out, c.Descriptor())
	}
	return out
}

func passkeyInfo(r db.UserPasskey) PasskeyInfo {
	info := PasskeyInfo{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, LastUsedAt: r.LastUsedAt, Transports: []string{}}
	var c webauthn.Credential
	if err := json.Unmarshal(r.Credential, &c); err == nil {
		info.Synced = c.Flags.BackupState
		for _, t := range c.Transport {
			info.Transports = append(info.Transports, string(t))
		}
	}
	return info
}

// ListPasskeys returns the user's passkeys, oldest first.
func (s *Service) ListPasskeys(ctx context.Context, userID uuid.UUID) ([]PasskeyInfo, error) {
	rows, err := s.q.ListUserPasskeys(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	out := make([]PasskeyInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, passkeyInfo(r))
	}
	return out, nil
}

func (s *Service) checkPassword(u db.User, password string) error {
	if u.PasswordHash == "" {
		return nil
	}
	ok, err := argon2id.ComparePasswordAndHash(password, u.PasswordHash)
	if err != nil || !ok {
		return apierr.Unprocessable("invalid_password", "the password is incorrect")
	}
	return nil
}

// ---- registration ----

// BeginPasskeyRegistration starts adding a passkey to the signed-in user's
// account. When the account has a password it must be supplied again, so a
// stolen session cannot plant a passkey of its own.
func (s *Service) BeginPasskeyRegistration(ctx context.Context, sm *scs.SessionManager, userID uuid.UUID, base, password string) (*protocol.CredentialCreation, error) {
	wa, err := s.relyingParty(base)
	if err != nil {
		return nil, err
	}
	u, err := s.q.GetUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if err := s.checkPassword(u, password); err != nil {
		return nil, err
	}
	wu, err := s.loadPasskeyUser(ctx, s.q, userID)
	if err != nil {
		return nil, err
	}
	if len(wu.creds) >= MaxPasskeys {
		return nil, apierr.Conflict("passkey_limit", fmt.Sprintf("an account can have at most %d passkeys; remove one first", MaxPasskeys))
	}
	creation, data, err := wa.BeginRegistration(wu, webauthn.WithExclusions(descriptors(wu.creds)))
	if err != nil {
		return nil, fmt.Errorf("begin passkey registration: %w", err)
	}
	if err := s.putCeremony(ctx, sm, ceremonyRegister, data); err != nil {
		return nil, err
	}
	return creation, nil
}

// FinishPasskeyRegistration verifies the authenticator's answer and stores the
// passkey. When the account has no unused recovery code left it also issues a
// fresh set (returned once), so a passkey-only account is never without a way in.
func (s *Service) FinishPasskeyRegistration(ctx context.Context, tx pgx.Tx, sm *scs.SessionManager, userID uuid.UUID, base, name string, response []byte) (*PasskeyInfo, []string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 60 {
		return nil, nil, apierr.Unprocessable("invalid_passkey_name", "give the passkey a name of up to 60 characters")
	}
	wa, err := s.relyingParty(base)
	if err != nil {
		return nil, nil, err
	}
	data, err := s.takeCeremony(ctx, sm, ceremonyRegister)
	if err != nil {
		return nil, nil, err
	}
	q := s.q.WithTx(tx)
	wu, err := s.loadPasskeyUser(ctx, q, userID)
	if err != nil {
		return nil, nil, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return nil, nil, ErrPasskeyInvalid
	}
	cred, err := wa.CreateCredential(wu, *data, parsed)
	if err != nil {
		slog.InfoContext(ctx, "passkey registration refused", "user", userID, "error", err)
		return nil, nil, apierr.Unprocessable("passkey_rejected", "the passkey could not be verified; try again")
	}
	raw, err := marshalCredential(cred)
	if err != nil {
		return nil, nil, err
	}
	row, err := q.CreateUserPasskey(ctx, db.CreateUserPasskeyParams{UserID: userID, Name: name, CredentialID: cred.ID, Credential: raw})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, nil, apierr.Conflict("passkey_exists", "this passkey is already registered")
		}
		return nil, nil, fmt.Errorf("store passkey: %w", err)
	}
	var codes []string
	left, err := q.CountRecoveryCodesLeft(ctx, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("count recovery codes: %w", err)
	}
	if left == 0 {
		if codes, err = s.replaceRecoveryCodes(ctx, q, userID); err != nil {
			return nil, nil, err
		}
	}
	info := passkeyInfo(row)
	return &info, codes, nil
}

// marshalCredential serialises a credential without the attestation blob, which
// is only needed during registration.
func marshalCredential(c *webauthn.Credential) ([]byte, error) {
	cp := *c
	cp.Attestation = webauthn.CredentialAttestation{}
	raw, err := json.Marshal(cp)
	if err != nil {
		return nil, fmt.Errorf("encode credential: %w", err)
	}
	return raw, nil
}

// ---- management ----

// RenamePasskey renames one of the user's passkeys.
func (s *Service) RenamePasskey(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 60 {
		return apierr.Unprocessable("invalid_passkey_name", "give the passkey a name of up to 60 characters")
	}
	n, err := s.q.WithTx(tx).RenameUserPasskey(ctx, db.RenameUserPasskeyParams{ID: id, UserID: userID, Name: name})
	if err != nil {
		return fmt.Errorf("rename passkey: %w", err)
	}
	if n == 0 {
		return apierr.NotFound("passkey")
	}
	return nil
}

// DeletePasskey removes a passkey after the password check (when the account has
// a password). The last passkey of an account with no other way to sign in stays.
// Removing the final second factor also drops the recovery codes.
func (s *Service) DeletePasskey(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, password string) error {
	q := s.q.WithTx(tx)
	u, err := q.GetUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if err := s.checkPassword(u, password); err != nil {
		return err
	}
	if _, err := q.GetUserPasskey(ctx, db.GetUserPasskeyParams{ID: id, UserID: userID}); errors.Is(err, pgx.ErrNoRows) {
		return apierr.NotFound("passkey")
	} else if err != nil {
		return fmt.Errorf("get passkey: %w", err)
	}
	count, err := q.CountUserPasskeys(ctx, userID)
	if err != nil {
		return fmt.Errorf("count passkeys: %w", err)
	}
	if count == 1 && u.PasswordHash == "" {
		ids, err := q.ListUserIdentities(ctx, userID)
		if err != nil {
			return fmt.Errorf("list identities: %w", err)
		}
		if len(ids) == 0 {
			return apierr.Conflict("last_sign_in_method", "this passkey is your only way to sign in; set a password or link another account first")
		}
	}
	if n, err := q.DeleteUserPasskey(ctx, db.DeleteUserPasskeyParams{ID: id, UserID: userID}); err != nil {
		return fmt.Errorf("delete passkey: %w", err)
	} else if n == 0 {
		return apierr.NotFound("passkey")
	}
	if has, err := q.UserHasSecondFactor(ctx, userID); err != nil {
		return fmt.Errorf("check second factor: %w", err)
	} else if !has {
		if err := q.DeleteRecoveryCodes(ctx, userID); err != nil {
			return fmt.Errorf("delete recovery codes: %w", err)
		}
	}
	return nil
}

// ---- passwordless sign-in ----

// BeginPasskeyLogin starts a sign-in with no account named up front: the browser
// shows the passkeys it holds for this site.
func (s *Service) BeginPasskeyLogin(ctx context.Context, sm *scs.SessionManager, base string) (*protocol.CredentialAssertion, error) {
	wa, err := s.relyingParty(base)
	if err != nil {
		return nil, err
	}
	assertion, data, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, fmt.Errorf("begin passkey login: %w", err)
	}
	if err := s.putCeremony(ctx, sm, ceremonyLogin, data); err != nil {
		return nil, err
	}
	return assertion, nil
}

// FinishPasskeyLogin verifies the assertion and returns the account. The caller
// starts the session and marks it second-factor verified: the assertion was
// user-verified on the authenticator, which is a possession plus a PIN or
// biometric.
func (s *Service) FinishPasskeyLogin(ctx context.Context, sm *scs.SessionManager, base string, response []byte) (*User, error) {
	wa, err := s.relyingParty(base)
	if err != nil {
		return nil, err
	}
	data, err := s.takeCeremony(ctx, sm, ceremonyLogin)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return nil, ErrPasskeyInvalid
	}
	var owner *waUser
	handler := func(_, handle []byte) (webauthn.User, error) {
		id, err := uuid.FromBytes(handle)
		if err != nil {
			return nil, errors.New("unknown user handle")
		}
		wu, err := s.loadPasskeyUser(ctx, s.q, id)
		if err != nil {
			return nil, err
		}
		owner = wu
		return wu, nil
	}
	_, cred, err := wa.ValidatePasskeyLogin(handler, *data, parsed)
	if err != nil || owner == nil {
		slog.InfoContext(ctx, "passkey sign-in refused", "error", err)
		return nil, ErrPasskeyFailed
	}
	return s.acceptAssertion(ctx, s.q, owner, cred)
}

// acceptAssertion persists the updated credential (sign counter, backup state)
// and rejects a credential whose counter went backwards (a cloned authenticator).
func (s *Service) acceptAssertion(ctx context.Context, q *db.Queries, owner *waUser, cred *webauthn.Credential) (*User, error) {
	userID, err := uuid.FromBytes(owner.id)
	if err != nil {
		return nil, ErrPasskeyFailed
	}
	if cred.Authenticator.CloneWarning {
		slog.WarnContext(ctx, "passkey sign-in refused: sign counter went backwards", "user", userID)
		return nil, ErrPasskeyFailed
	}
	row, err := q.GetPasskeyByCredentialID(ctx, cred.ID)
	if err != nil || row.UserID != userID {
		return nil, ErrPasskeyFailed
	}
	raw, err := marshalCredential(cred)
	if err != nil {
		return nil, err
	}
	if err := q.TouchUserPasskey(ctx, db.TouchUserPasskeyParams{ID: row.ID, Credential: raw}); err != nil {
		return nil, fmt.Errorf("touch passkey: %w", err)
	}
	return s.UserByID(ctx, userID)
}

// ---- second factor ----

// SecondFactorMethods says what can finish the pending sign-in in sm.
func (s *Service) SecondFactorMethods(ctx context.Context, sm *scs.SessionManager) (totp, passkey bool, err error) {
	id, err := s.pendingUserID(ctx, sm)
	if err != nil {
		return false, false, err
	}
	if totp, err = s.q.UserHasTOTP(ctx, id); err != nil {
		return false, false, fmt.Errorf("check totp: %w", err)
	}
	n, err := s.q.CountUserPasskeys(ctx, id)
	if err != nil {
		return false, false, fmt.Errorf("count passkeys: %w", err)
	}
	return totp, n > 0, nil
}

// BeginPasskeySecondFactor asks the pending user's passkeys to confirm the sign-in.
func (s *Service) BeginPasskeySecondFactor(ctx context.Context, sm *scs.SessionManager, base string) (*protocol.CredentialAssertion, error) {
	id, err := s.pendingUserID(ctx, sm)
	if err != nil {
		return nil, err
	}
	wa, err := s.relyingParty(base)
	if err != nil {
		return nil, err
	}
	wu, err := s.loadPasskeyUser(ctx, s.q, id)
	if err != nil {
		return nil, err
	}
	if len(wu.creds) == 0 {
		return nil, apierr.Conflict("no_passkeys", "this account has no passkeys")
	}
	// The password already proved the first factor; the passkey is the second, so
	// user verification is requested but not forced (a security key without a PIN
	// still works, as it does for U2F).
	assertion, data, err := wa.BeginLogin(wu, webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		return nil, fmt.Errorf("begin passkey second factor: %w", err)
	}
	if err := s.putCeremony(ctx, sm, ceremonySecond, data); err != nil {
		return nil, err
	}
	return assertion, nil
}

// CompletePasskeySecondFactor verifies a passkey assertion for the user parked by
// BeginSecondFactor. Wrong answers count against the pending login's tries.
func (s *Service) CompletePasskeySecondFactor(ctx context.Context, sm *scs.SessionManager, base string, response []byte) (*User, error) {
	id, err := s.pendingUserID(ctx, sm)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*User, error) {
		sm.Put(ctx, sessionPendingTries, sm.GetInt(ctx, sessionPendingTries)+1)
		return nil, err
	}
	wa, err := s.relyingParty(base)
	if err != nil {
		return nil, err
	}
	data, err := s.takeCeremony(ctx, sm, ceremonySecond)
	if err != nil {
		return nil, err
	}
	wu, err := s.loadPasskeyUser(ctx, s.q, id)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return fail(ErrPasskeyInvalid)
	}
	cred, err := wa.ValidateLogin(wu, *data, parsed)
	if err != nil {
		slog.InfoContext(ctx, "passkey second factor refused", "user", id, "error", err)
		return fail(ErrPasskeyFailed)
	}
	u, err := s.acceptAssertion(ctx, s.q, wu, cred)
	if err != nil {
		return fail(err)
	}
	clearPending(ctx, sm)
	return u, nil
}

// PasskeyUnavailable explains why passkeys cannot be used at base, or "" when
// they can.
func (s *Service) PasskeyUnavailable(base string) string {
	if _, err := s.relyingParty(base); err != nil {
		if ae, ok := apierr.As(err); ok {
			return ae.Message
		}
		return err.Error()
	}
	return ""
}
