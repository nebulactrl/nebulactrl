package notifications

import (
	"encoding/json"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// Channel kinds.
const (
	KindEmail    = "email"
	KindSlack    = "slack"
	KindDiscord  = "discord"
	KindTelegram = "telegram"
	KindWebhook  = "webhook"
)

// SMTP connection security.
const (
	TLSImplicit = "implicit" // TLS from the first byte (port 465)
	TLSStartTLS = "starttls" // upgrade after EHLO (port 587)
	TLSNone     = "none"     // plain text (port 25; trusted networks only)
)

// maxRecipients bounds an email channel's To list.
const maxRecipients = 20

// ChannelConfig holds the settings of a channel that are not secret. Which
// fields apply depends on the kind: email uses everything except ChatID,
// Telegram only ChatID, the others none.
type ChannelConfig struct {
	Host               string   `json:"host,omitempty" doc:"Email: SMTP server host name."`
	Port               int      `json:"port,omitempty" minimum:"0" maximum:"65535" doc:"Email: SMTP port. Default: 465 (implicit TLS), 587 (STARTTLS), 25 (none)."`
	TLS                string   `json:"tls,omitempty" enum:"implicit,starttls,none" doc:"Email: connection security. Default starttls."`
	Username           string   `json:"username,omitempty" doc:"Email: SMTP user name; empty sends without authentication."`
	From               string   `json:"from,omitempty" doc:"Email: sender, \"alerts@example.com\" or \"NebulaCtrl <alerts@example.com>\"."`
	To                 []string `json:"to,omitempty" maxItems:"20" doc:"Email: recipients."`
	InsecureSkipVerify bool     `json:"insecureSkipVerify,omitempty" doc:"Email: accept any server certificate (self-signed relays). The connection is still encrypted."`
	ChatID             string   `json:"chatId,omitempty" doc:"Telegram: chat id (-100123...) or @channelname the bot posts to."`
}

// secrets are the credentials of a channel; they are sealed as one JSON object.
type secrets struct {
	URL           string `json:"url,omitempty"`           // slack, discord, webhook
	BotToken      string `json:"botToken,omitempty"`      // telegram
	Password      string `json:"password,omitempty"`      // email
	SigningSecret string `json:"signingSecret,omitempty"` // webhook
}

// ChannelSecretsInput carries write-only credentials. On update an empty field keeps
// the stored value.
type ChannelSecretsInput struct {
	URL           string `json:"url,omitempty" doc:"Slack, Discord and webhook: the endpoint URL (it is the credential for Slack and Discord)."`
	BotToken      string `json:"botToken,omitempty" doc:"Telegram: the bot token from @BotFather."`
	Password      string `json:"password,omitempty" doc:"Email: the SMTP password."`
	SigningSecret string `json:"signingSecret,omitempty" doc:"Webhook: the HMAC signing secret. Omit on create to have one generated."`
}

func (in ChannelSecretsInput) merge(cur secrets) secrets {
	if in.URL != "" {
		cur.URL = in.URL
	}
	if in.BotToken != "" {
		cur.BotToken = in.BotToken
	}
	if in.Password != "" {
		cur.Password = in.Password
	}
	if in.SigningSecret != "" {
		cur.SigningSecret = in.SigningSecret
	}
	return cur
}

func openSecrets(box *secretbox.Box, id uuid.UUID, enc []byte) (secrets, error) {
	var s secrets
	if len(enc) == 0 {
		return s, nil
	}
	raw, err := box.Open(secretbox.NotificationChannelsSecrets, id.String(), enc)
	if err != nil {
		return s, fmt.Errorf("decrypt channel secrets: %w", err)
	}
	defer secretbox.Wipe(raw)
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("decode channel secrets: %w", err)
	}
	return s, nil
}

func sealSecrets(box *secretbox.Box, id uuid.UUID, s secrets) ([]byte, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	defer secretbox.Wipe(raw)
	return box.Seal(secretbox.NotificationChannelsSecrets, id.String(), raw)
}

// all returns every non-empty secret value (for scrubbing errors).
func (s secrets) all() []string {
	var out []string
	for _, v := range []string{s.URL, s.BotToken, s.Password, s.SigningSecret} {
		if v != "" {
			out = append(out, v)
		}
	}
	// The pieces of a URL secret are credentials too (Slack's token path, Discord's token).
	if u, err := url.Parse(s.URL); err == nil && s.URL != "" {
		if p := strings.Trim(u.Path, "/"); len(p) >= 6 {
			out = append(out, p)
			for _, seg := range strings.Split(p, "/") {
				if len(seg) >= 12 {
					out = append(out, seg)
				}
			}
		}
		if u.RawQuery != "" {
			out = append(out, u.RawQuery)
		}
	}
	return out
}

const mask = "\u2022\u2022\u2022\u2022\u2022\u2022"

// hints describes which secrets are set without revealing them: a URL shows its
// scheme and host, everything else a mask.
func (s secrets) hints() map[string]string {
	out := map[string]string{}
	if s.URL != "" {
		out["url"] = maskURL(s.URL)
	}
	if s.BotToken != "" {
		out["botToken"] = mask
	}
	if s.Password != "" {
		out["password"] = mask
	}
	if s.SigningSecret != "" {
		out["signingSecret"] = mask
	}
	return out
}

func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return mask
	}
	return u.Scheme + "://" + u.Host + "/" + mask
}

var (
	telegramTokenRe = regexp.MustCompile(`^\d{3,}:[A-Za-z0-9_-]{20,}$`)
	telegramChatRe  = regexp.MustCompile(`^(@[A-Za-z][A-Za-z0-9_]{3,}|-?\d{1,20})$`)
	hostRe          = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
)

func bad(msg string) error { return apierr.Unprocessable("validation_failed", msg) }

func validEndpoint(raw string, allowHTTP bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return bad("the URL must be an absolute http(s) URL without credentials")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowHTTP {
			return bad("the URL must use https")
		}
	default:
		return bad("the URL must start with https://")
	}
	if len(raw) > 2048 {
		return bad("the URL is too long")
	}
	return nil
}

// normalizeConfig validates cfg for kind and fills defaults.
func normalizeConfig(kind string, cfg ChannelConfig) (ChannelConfig, error) {
	switch kind {
	case KindEmail:
		cfg.Host = strings.TrimSpace(cfg.Host)
		if cfg.Host == "" || !hostRe.MatchString(cfg.Host) && net.ParseIP(strings.Trim(cfg.Host, "[]")) == nil {
			return cfg, bad("host must be the SMTP server's host name or address")
		}
		if cfg.TLS == "" {
			cfg.TLS = TLSStartTLS
		}
		if cfg.TLS != TLSImplicit && cfg.TLS != TLSStartTLS && cfg.TLS != TLSNone {
			return cfg, bad("tls must be implicit, starttls or none")
		}
		if cfg.Port == 0 {
			cfg.Port = map[string]int{TLSImplicit: 465, TLSStartTLS: 587, TLSNone: 25}[cfg.TLS]
		}
		if cfg.Port < 1 || cfg.Port > 65535 {
			return cfg, bad("port must be between 1 and 65535")
		}
		cfg.Username = strings.TrimSpace(cfg.Username)
		from, err := mail.ParseAddress(strings.TrimSpace(cfg.From))
		if err != nil {
			return cfg, bad("from must be an email address, e.g. alerts@example.com")
		}
		cfg.From = from.String()
		if len(cfg.To) == 0 || len(cfg.To) > maxRecipients {
			return cfg, bad(fmt.Sprintf("add between 1 and %d recipients", maxRecipients))
		}
		to := make([]string, 0, len(cfg.To))
		seen := map[string]bool{}
		for _, t := range cfg.To {
			a, err := mail.ParseAddress(strings.TrimSpace(t))
			if err != nil {
				return cfg, bad("recipient " + strconv.Quote(t) + " is not an email address")
			}
			if !seen[strings.ToLower(a.Address)] {
				seen[strings.ToLower(a.Address)] = true
				to = append(to, a.Address)
			}
		}
		cfg.To = to
		cfg.ChatID = ""
	case KindTelegram:
		cfg.ChatID = strings.TrimSpace(cfg.ChatID)
		if !telegramChatRe.MatchString(cfg.ChatID) {
			return cfg, bad("chatId must be a numeric chat id (-1001234567890) or @channelname")
		}
		cfg = ChannelConfig{ChatID: cfg.ChatID}
	case KindSlack, KindDiscord, KindWebhook:
		cfg = ChannelConfig{}
	default:
		return cfg, bad("kind must be email, slack, discord, telegram or webhook")
	}
	return cfg, nil
}

// validateSecrets checks the credentials a kind needs.
func validateSecrets(kind string, cfg ChannelConfig, s secrets) error {
	switch kind {
	case KindEmail:
		if s.Password != "" && cfg.Username == "" {
			return bad("a password needs a username")
		}
		if cfg.Username != "" && s.Password == "" {
			return bad("a username needs a password")
		}
	case KindSlack:
		if s.URL == "" {
			return bad("the Slack incoming webhook URL is required")
		}
		return validEndpoint(s.URL, false)
	case KindDiscord:
		if s.URL == "" {
			return bad("the Discord webhook URL is required")
		}
		return validEndpoint(s.URL, false)
	case KindWebhook:
		if s.URL == "" {
			return bad("the webhook URL is required")
		}
		if len(s.SigningSecret) < 16 {
			return bad("the signing secret must be at least 16 characters")
		}
		return validEndpoint(s.URL, true)
	case KindTelegram:
		if !telegramTokenRe.MatchString(s.BotToken) {
			return bad("botToken must be the token @BotFather gave you (123456:ABC...)")
		}
	}
	return nil
}
