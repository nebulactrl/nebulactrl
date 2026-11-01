package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/alexedwards/scs/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// Password reset: an emailed (or admin-generated) single-use link.
//
// The raw token exists only in the email or on the admin's screen; the database
// keeps its SHA-256. A link lives 30 minutes and works once. Asking for one
// answers the same whether or not the account exists, and accounts without a
// password (sign-in through an identity provider only) never get one.

const (
	// ResetTokenPrefix starts every reset token; it differs from the API token
	// prefix so one can never be mistaken for the other.
	ResetTokenPrefix = "ncr_"
	// ResetTTL is how long a reset link lives.
	ResetTTL = 30 * time.Minute
	// ResetMaxPerAccountPerHour caps the links one account can be mailed per
	// hour; further requests are silently dropped (the answer stays identical).
	ResetMaxPerAccountPerHour = 3
	// SessionUserKey is the session value holding the signed-in user's id.
	SessionUserKey = "user_id"

	resetSendTimeout = 30 * time.Second
)

// ErrResetUnavailable is returned when no email can be sent (no SMTP relay, no
// team email channel, or no public URL to put in the link).
var ErrResetUnavailable = apierr.New(503, "password_reset_unavailable",
	"Password reset by email is not set up on this instance. Ask an admin to reset your password.")

// ErrInvalidResetToken is returned for an unknown, expired or already used link.
var ErrInvalidResetToken = apierr.New(400, "invalid_reset_token", "This reset link is invalid, has expired or was already used. Ask for a new one.")

// ResetMail is a reset email to send.
type ResetMail struct {
	UserID    uuid.UUID
	To        string
	Name      string
	Link      string
	ExpiresAt time.Time
}

// ResetMailer sends reset emails. The app implements it with the instance SMTP
// relay first and a team email channel as the fallback.
type ResetMailer interface {
	// Available reports whether a reset email could be sent right now.
	Available(ctx context.Context) bool
	SendPasswordReset(ctx context.Context, m ResetMail) error
}

// SetResetMailer installs the sender of reset emails. Until it is set (and the
// control plane has a public URL) reset by email is unavailable.
func (s *Service) SetResetMailer(m ResetMailer) {
	s.methods.mu.Lock()
	defer s.methods.mu.Unlock()
	s.reset.mailer = m
}

type resetState struct {
	mailer ResetMailer
}

// ResetLink builds the absolute link of a token ("" when no public URL is set).
func (s *Service) ResetLink(raw string) string {
	base := strings.TrimRight(s.PublicURL(), "/")
	if base == "" {
		return ""
	}
	return base + "/reset-password?token=" + raw
}

// PasswordResetAvailable reports whether the login page may offer "Forgot
// password": a mailer that can send, and a public URL for the link.
func (s *Service) PasswordResetAvailable(ctx context.Context) bool {
	s.methods.mu.RLock()
	m := s.reset.mailer
	s.methods.mu.RUnlock()
	return m != nil && s.PublicURL() != "" && m.Available(ctx)
}

func newResetToken() (raw string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", nil, err
	}
	raw = ResetTokenPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
	return raw, hashReset(raw), nil
}

func hashReset(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}

// trimResetToken returns the token without surrounding spaces, or "" when it
// cannot be a reset token (wrong prefix or too long).
func trimResetToken(token string) string {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, ResetTokenPrefix) || len(token) > 128 {
		return ""
	}
	return token
}

// RequestPasswordReset mails a reset link to the account with this email. It
// returns nil whether or not the account exists, whether or not it can reset
// (no password, too many recent requests): the caller must answer identically
// in every case. The email is sent in the background so the response time does
// not tell the cases apart. It fails only when reset is unavailable.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	if !s.PasswordResetAvailable(ctx) {
		return ErrResetUnavailable
	}
	email = normalizeEmail(email)
	if _, err := mail.ParseAddress(email); err != nil {
		return nil //nolint:nilerr // an unparseable address gets the same nil answer as an unknown account, so the response cannot be used to probe for accounts
	}
	u, err := s.q.GetUserByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if u.PasswordHash == "" {
		return nil // signs in through an identity provider only
	}
	now := s.methods.clock()
	recent, err := s.q.CountSelfServiceResetTokensSince(ctx, db.CountSelfServiceResetTokensSinceParams{UserID: u.ID, CreatedAt: now.Add(-time.Hour)})
	if err != nil {
		return fmt.Errorf("count reset tokens: %w", err)
	}
	if recent >= ResetMaxPerAccountPerHour {
		return nil
	}
	raw, hash, err := newResetToken()
	if err != nil {
		return fmt.Errorf("generate reset token: %w", err)
	}
	expires := now.Add(ResetTTL)
	if _, err := s.q.InsertPasswordResetToken(ctx, db.InsertPasswordResetTokenParams{UserID: u.ID, TokenHash: hash, ExpiresAt: expires}); err != nil {
		return fmt.Errorf("insert reset token: %w", err)
	}
	_ = s.q.PruneExpiredPasswordResetTokens(ctx, now)

	s.methods.mu.RLock()
	mailer := s.reset.mailer
	s.methods.mu.RUnlock()
	m := ResetMail{UserID: u.ID, To: u.Email, Name: u.Name, Link: s.ResetLink(raw), ExpiresAt: expires}
	go func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resetSendTimeout)
		defer cancel()
		if err := mailer.SendPasswordReset(sctx, m); err != nil {
			slog.WarnContext(sctx, "send password reset email", "user_id", u.ID.String(), "error", err)
		}
	}()
	return nil
}

// CreateResetToken issues a reset link for userID inside tx, for an admin to
// hand over. createdBy is the admin. It refuses accounts without a password
// (409 no_password): they sign in through an identity provider.
func (s *Service) CreateResetToken(ctx context.Context, tx pgx.Tx, userID, createdBy uuid.UUID) (raw string, expires time.Time, err error) {
	return s.issueResetToken(ctx, tx, userID, &createdBy)
}

// issueResetToken inserts a reset link for userID; createdBy is nil for a link
// the operator generated on the host (nebulactrl-server reset-password).
func (s *Service) issueResetToken(ctx context.Context, tx pgx.Tx, userID uuid.UUID, createdBy *uuid.UUID) (raw string, expires time.Time, err error) {
	q := s.q.WithTx(tx)
	u, err := q.GetUser(ctx, userID)
	if err != nil {
		return "", time.Time{}, err
	}
	if u.PasswordHash == "" {
		return "", time.Time{}, apierr.Conflict("no_password", "this account signs in through an identity provider and has no password to reset")
	}
	raw, hash, err := newResetToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generate reset token: %w", err)
	}
	expires = s.methods.clock().Add(ResetTTL)
	if _, err := q.InsertPasswordResetToken(ctx, db.InsertPasswordResetTokenParams{UserID: userID, TokenHash: hash, CreatedBy: createdBy, ExpiresAt: expires}); err != nil {
		return "", time.Time{}, fmt.Errorf("insert reset token: %w", err)
	}
	return raw, expires, nil
}

// ConfirmPasswordReset consumes the link and sets the new password, inside tx.
// It returns the user and the teams they belong to (for the audit trail). The
// caller must then call RevokeUserSessions: every session of the account ends
// with the old password. The new password is validated before the link is
// spent, so a weak password does not burn it.
func (s *Service) ConfirmPasswordReset(ctx context.Context, tx pgx.Tx, token, password string) (*User, []uuid.UUID, error) {
	if err := ValidatePassword(password); err != nil {
		return nil, nil, err
	}
	token = trimResetToken(token)
	if token == "" {
		return nil, nil, ErrInvalidResetToken
	}
	hash, err := argon2id.CreateHash(password, s.params)
	if err != nil {
		return nil, nil, fmt.Errorf("hash password: %w", err)
	}
	q := s.q.WithTx(tx)
	now := s.methods.clock()
	userID, err := q.ConsumePasswordResetToken(ctx, db.ConsumePasswordResetTokenParams{TokenHash: hashReset(token), UsedAt: &now})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrInvalidResetToken
	}
	if err != nil {
		return nil, nil, fmt.Errorf("consume reset token: %w", err)
	}
	u, err := q.GetUser(ctx, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("get user: %w", err)
	}
	if u.PasswordHash == "" {
		return nil, nil, ErrInvalidResetToken // an identity-provider-only account has no password to reset
	}
	if err := q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{ID: userID, PasswordHash: hash}); err != nil {
		return nil, nil, fmt.Errorf("update password: %w", err)
	}
	if err := q.SpendPasswordResetTokens(ctx, db.SpendPasswordResetTokensParams{UserID: userID, UsedAt: &now}); err != nil {
		return nil, nil, fmt.Errorf("spend reset tokens: %w", err)
	}
	teams, err := q.ListUserTeamIDs(ctx, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("list teams: %w", err)
	}
	return userFrom(u), teams, nil
}

// RevokeUserSessions destroys every browser session of the user, including a
// sign-in parked at its second-factor step. It reads the whole session store,
// which is small on a self-hosted instance. It runs on a fresh context: the
// request's own context already carries a loaded session.
func RevokeUserSessions(sm *scs.SessionManager, userID uuid.UUID) error {
	id := userID.String()
	return sm.Iterate(context.Background(), func(sctx context.Context) error {
		if sm.GetString(sctx, SessionUserKey) == id || sm.GetString(sctx, sessionPendingUser) == id {
			return sm.Destroy(sctx)
		}
		return nil
	})
}
