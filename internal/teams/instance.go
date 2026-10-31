package teams

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// ErrNoMailer is returned by a Mailer when the team has no email channel and
// the instance has no SMTP relay.
var ErrNoMailer = errors.New("no email channel or SMTP relay is configured")

// Relay is the instance-level SMTP relay.
type Relay struct {
	Host               string
	Port               int
	TLS                string
	Username           string
	Password           string
	From               string
	InsecureSkipVerify bool
}

// InviteMail is the content of an invite email.
type InviteMail struct {
	To          string
	TeamName    string
	InviterName string
	Role        string
	Link        string
	ExpiresAt   time.Time
}

// Mailer delivers invite emails: through the team's email notification channel
// if it has one, else through relay (nil when the instance has no SMTP
// settings), else it returns ErrNoMailer.
type Mailer interface {
	SendInvite(ctx context.Context, teamID uuid.UUID, relay *Relay, m InviteMail) error
}

// SMTP connection security values (match the notification channels).
var smtpTLS = []string{"implicit", "starttls", "none"}

func (s *Service) relayFrom(set db.InstanceSetting) *Relay {
	if strings.TrimSpace(set.SmtpHost) == "" || strings.TrimSpace(set.SmtpFrom) == "" {
		return nil
	}
	r := &Relay{
		Host: set.SmtpHost, Port: int(set.SmtpPort), TLS: set.SmtpTls, Username: set.SmtpUsername,
		From: set.SmtpFrom, InsecureSkipVerify: set.SmtpInsecureSkipVerify,
	}
	if len(set.SmtpPasswordEnc) > 0 && s.box != nil {
		if pw, err := s.box.Open(secretbox.InstanceSettingsSMTPPassword, secretbox.InstanceRow, set.SmtpPasswordEnc); err == nil {
			r.Password = string(pw)
			secretbox.Wipe(pw)
		} else {
			s.log.Warn("instance SMTP password could not be decrypted (was the secret key changed?)")
		}
	}
	return r
}

// InstanceRelay returns the instance SMTP relay, or nil when the instance has
// none configured. Password reset emails go through it.
func (s *Service) InstanceRelay(ctx context.Context) (*Relay, error) {
	set, err := s.q.GetInstanceSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("instance settings: %w", err)
	}
	return s.relayFrom(set), nil
}

// InstanceSettings is the instance-wide settings. SMTP fields are only returned to the
// instance admin; the password is write-only.
type InstanceSettings struct {
	AllowTeamCreation bool `json:"allowTeamCreation" doc:"Whether any user may create a team (the instance admin always may)"`
	CanManage         bool `json:"canManage" doc:"The caller is the instance admin and may change these settings"`
	SMTPConfigured    bool `json:"smtpConfigured" doc:"Invites can be emailed through the instance relay"`

	SMTPHost               *string `json:"smtpHost,omitempty"`
	SMTPPort               *int    `json:"smtpPort,omitempty"`
	SMTPTLS                *string `json:"smtpTls,omitempty" enum:"implicit,starttls,none"`
	SMTPUsername           *string `json:"smtpUsername,omitempty"`
	SMTPPasswordSet        *bool   `json:"smtpPasswordSet,omitempty"`
	SMTPFrom               *string `json:"smtpFrom,omitempty"`
	SMTPInsecureSkipVerify *bool   `json:"smtpInsecureSkipVerify,omitempty"`
}

func (s *Service) instanceView(set db.InstanceSetting, admin bool) InstanceSettings {
	out := InstanceSettings{AllowTeamCreation: set.AllowTeamCreation, CanManage: admin, SMTPConfigured: s.relayFrom(set) != nil}
	if admin {
		port, pwSet, skip := int(set.SmtpPort), len(set.SmtpPasswordEnc) > 0, set.SmtpInsecureSkipVerify
		out.SMTPHost, out.SMTPPort, out.SMTPTLS, out.SMTPUsername = &set.SmtpHost, &port, &set.SmtpTls, &set.SmtpUsername
		out.SMTPPasswordSet, out.SMTPFrom, out.SMTPInsecureSkipVerify = &pwSet, &set.SmtpFrom, &skip
	}
	return out
}

// instanceAdmin reports whether the caller is the instance admin: a user who is
// the first account (tokens count through their creator, with the admin scope).
func (s *Service) instanceAdmin(ctx context.Context, p *auth.Principal) (bool, error) {
	if p.Type != auth.TypeUser && !policy.HasScope(p, policy.ScopeAdmin) {
		return false, nil
	}
	return s.auth.IsInstanceAdmin(ctx, p.UserID)
}

// GetInstance returns the instance settings (any signed-in user; SMTP details
// only for the instance admin).
func (s *Service) GetInstance(ctx context.Context) (InstanceSettings, error) {
	p, err := principal(ctx)
	if err != nil {
		return InstanceSettings{}, err
	}
	set, err := s.q.GetInstanceSettings(ctx)
	if err != nil {
		return InstanceSettings{}, fmt.Errorf("instance settings: %w", err)
	}
	admin, err := s.instanceAdmin(ctx, p)
	if err != nil {
		return InstanceSettings{}, err
	}
	return s.instanceView(set, admin), nil
}

// InstancePatch is a partial update of the instance settings. SMTPPassword
// is write-only: nil keeps it, an empty string clears it.
type InstancePatch struct {
	AllowTeamCreation      *bool
	SMTPHost               *string
	SMTPPort               *int
	SMTPTLS                *string
	SMTPUsername           *string
	SMTPPassword           *string
	SMTPFrom               *string
	SMTPInsecureSkipVerify *bool
}

// UpdateInstance changes the instance settings (instance admin only).
func (s *Service) UpdateInstance(ctx context.Context, patch InstancePatch) (InstanceSettings, error) {
	p, err := principal(ctx)
	if err != nil {
		return InstanceSettings{}, err
	}
	admin, err := s.instanceAdmin(ctx, p)
	if err != nil {
		return InstanceSettings{}, err
	}
	if !admin {
		return InstanceSettings{}, apierr.Forbidden()
	}
	cur, err := s.q.GetInstanceSettings(ctx)
	if err != nil {
		return InstanceSettings{}, fmt.Errorf("instance settings: %w", err)
	}
	arg := db.UpdateInstanceSettingsParams{
		AllowTeamCreation: cur.AllowTeamCreation, SmtpHost: cur.SmtpHost, SmtpPort: cur.SmtpPort, SmtpTls: cur.SmtpTls,
		SmtpUsername: cur.SmtpUsername, SmtpFrom: cur.SmtpFrom, SmtpInsecureSkipVerify: cur.SmtpInsecureSkipVerify,
	}
	if patch.AllowTeamCreation != nil {
		arg.AllowTeamCreation = *patch.AllowTeamCreation
	}
	if patch.SMTPHost != nil {
		arg.SmtpHost = strings.TrimSpace(*patch.SMTPHost)
	}
	if patch.SMTPPort != nil {
		if *patch.SMTPPort < 1 || *patch.SMTPPort > 65535 {
			return InstanceSettings{}, apierr.Unprocessable("invalid_port", "SMTP port must be 1-65535")
		}
		arg.SmtpPort = int32(*patch.SMTPPort)
	}
	if patch.SMTPTLS != nil {
		ok := false
		for _, v := range smtpTLS {
			ok = ok || v == *patch.SMTPTLS
		}
		if !ok {
			return InstanceSettings{}, apierr.Unprocessable("invalid_tls", "TLS must be implicit, starttls or none")
		}
		arg.SmtpTls = *patch.SMTPTLS
	}
	if patch.SMTPUsername != nil {
		arg.SmtpUsername = strings.TrimSpace(*patch.SMTPUsername)
	}
	if patch.SMTPFrom != nil {
		from := strings.TrimSpace(*patch.SMTPFrom)
		if from != "" {
			if _, err := mail.ParseAddress(from); err != nil {
				return InstanceSettings{}, apierr.Unprocessable("invalid_email", "the sender must be an email address such as \"NebulaCtrl <no-reply@example.com>\"")
			}
		}
		arg.SmtpFrom = from
	}
	if patch.SMTPInsecureSkipVerify != nil {
		arg.SmtpInsecureSkipVerify = *patch.SMTPInsecureSkipVerify
	}
	clearPassword := false
	if patch.SMTPPassword != nil {
		if *patch.SMTPPassword == "" {
			clearPassword = true
		} else {
			if s.box == nil {
				return InstanceSettings{}, apierr.Unprocessable("no_secret_key", "the control plane has no secret key to store the password with")
			}
			enc, err := s.box.SealString(secretbox.InstanceSettingsSMTPPassword, secretbox.InstanceRow, *patch.SMTPPassword)
			if err != nil {
				return InstanceSettings{}, fmt.Errorf("seal smtp password: %w", err)
			}
			arg.SmtpPasswordEnc = enc
		}
	}
	var row db.InstanceSetting
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		row, err = q.UpdateInstanceSettings(ctx, arg)
		if err != nil {
			return err
		}
		if clearPassword {
			if err := q.ClearInstanceSMTPPassword(ctx); err != nil {
				return err
			}
			row.SmtpPasswordEnc = nil
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "instance.update", ResourceType: "instance", ResourceID: "instance", WorkspaceID: p.WorkspaceID,
			Metadata: map[string]any{
				"allowTeamCreation": row.AllowTeamCreation, "smtpHost": row.SmtpHost, "smtpFrom": row.SmtpFrom,
				"smtpPasswordChanged": patch.SMTPPassword != nil,
			},
		})
	})
	if err != nil {
		return InstanceSettings{}, fmt.Errorf("update instance settings: %w", err)
	}
	s.publish("instance.updated", p.WorkspaceID)
	return s.instanceView(row, true), nil
}
