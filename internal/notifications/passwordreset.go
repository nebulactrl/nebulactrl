package notifications

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TypePasswordReset is the type of a password reset email. It is not an event
// type: it is never in the catalogue and no channel can subscribe to it.
const TypePasswordReset = "auth.password_reset"

// PasswordResetMail is the content of a reset email.
type PasswordResetMail struct {
	To        string
	Link      string // absolute
	ExpiresAt time.Time
}

// PasswordResetPayload renders the reset email's content.
func PasswordResetPayload(m PasswordResetMail) Payload {
	return Payload{
		Type:     TypePasswordReset,
		Severity: SeverityInfo,
		Title:    "Reset your NebulaCtrl password",
		Body: fmt.Sprintf("Someone asked to reset the password of %s. The link works once and expires at %s. "+
			"If it was not you, ignore this email: your password stays as it is.", m.To, m.ExpiresAt.UTC().Format("15:04 MST")),
		URL:           m.Link,
		Transactional: true,
		LinkLabel:     "Choose a new password",
	}
}

// SendPasswordReset mails a reset link. It uses the instance SMTP relay when
// there is one and falls back to the first of teamIDs that has an enabled
// email channel. via names the transport used ("relay" or "channel").
// ErrNoMailer means neither exists.
func (s *Service) SendPasswordReset(ctx context.Context, relay *MailRelay, teamIDs []uuid.UUID, m PasswordResetMail) (via string, err error) {
	p := PasswordResetPayload(m)
	if relay != nil && strings.TrimSpace(relay.Host) != "" && strings.TrimSpace(relay.From) != "" {
		// No team: SendMail finds no channel and uses the relay.
		return s.SendMail(ctx, uuid.Nil, relay, m.To, p)
	}
	for _, id := range teamIDs {
		via, err := s.SendMail(ctx, id, nil, m.To, p)
		if err == ErrNoMailer {
			continue
		}
		return via, err
	}
	return "", ErrNoMailer
}

// AnyEmailChannel reports whether some team has an enabled email channel, so a
// reset email could be sent through it.
func (s *Service) AnyEmailChannel(ctx context.Context) (bool, error) {
	if s == nil || s.q == nil {
		return false, nil
	}
	return s.q.AnyEmailChannelEnabled(ctx)
}
