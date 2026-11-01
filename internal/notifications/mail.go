package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ErrNoMailer is returned by SendMail when the team has no enabled email
// channel and no relay was given.
var ErrNoMailer = errors.New("no email channel or SMTP relay is configured")

// MailRelay is an explicit SMTP relay (the instance-level setting).
type MailRelay struct {
	Host               string
	Port               int
	TLS                string // implicit, starttls or none
	Username           string
	Password           string
	From               string
	InsecureSkipVerify bool
}

// SendMail sends one transactional email (such as a team invite) to a single
// recipient. It uses the team's first enabled email channel (its SMTP server,
// credentials and sender; the channel's own recipient list is ignored) and
// falls back to relay. via names the transport used: "channel" or "relay".
func (s *Service) SendMail(ctx context.Context, workspaceID uuid.UUID, relay *MailRelay, to string, p Payload) (via string, err error) {
	if s == nil || s.q == nil {
		return "", ErrNoMailer
	}
	cfg, sec, via, err := s.mailTransport(ctx, workspaceID, relay)
	if err != nil {
		return "", err
	}
	cfg.To = []string{to}
	p.At = s.now()
	msg := message(p, uuid.New(), s.publicURL)
	if _, err := s.send.sendEmail(ctx, cfg, sec, msg, s.now()); err != nil {
		var se *sendError
		if errors.As(err, &se) {
			return via, errors.New(scrubError(se.Message, sec))
		}
		return via, errors.New(scrubError(err.Error(), sec))
	}
	return via, nil
}

func (s *Service) mailTransport(ctx context.Context, workspaceID uuid.UUID, relay *MailRelay) (ChannelConfig, secrets, string, error) {
	chans, err := s.q.ListEnabledNotificationChannels(ctx, workspaceID)
	if err != nil {
		return ChannelConfig{}, secrets{}, "", fmt.Errorf("list channels: %w", err)
	}
	for _, ch := range chans {
		if ch.Kind != KindEmail {
			continue
		}
		var cfg ChannelConfig
		if err := json.Unmarshal(ch.Config, &cfg); err != nil {
			continue
		}
		sec, err := openSecrets(s.box, ch.ID, ch.SecretsEnc)
		if err != nil {
			continue
		}
		return cfg, sec, "channel", nil
	}
	if relay == nil || strings.TrimSpace(relay.Host) == "" || strings.TrimSpace(relay.From) == "" {
		return ChannelConfig{}, secrets{}, "", ErrNoMailer
	}
	cfg := ChannelConfig{
		Host: relay.Host, Port: relay.Port, TLS: relay.TLS, Username: relay.Username, From: relay.From,
		InsecureSkipVerify: relay.InsecureSkipVerify,
	}
	return cfg, secrets{Password: relay.Password}, "relay", nil
}
