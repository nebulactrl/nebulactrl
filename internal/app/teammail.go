package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/teams"
)

// teamMailer sends team invite emails through the notifications module: the
// team's own email channel when it has one, else the instance SMTP relay.
type teamMailer struct{ n *notifications.Service }

var _ teams.Mailer = teamMailer{}

func (m teamMailer) SendInvite(ctx context.Context, teamID uuid.UUID, relay *teams.Relay, im teams.InviteMail) error {
	var r *notifications.MailRelay
	if relay != nil {
		r = &notifications.MailRelay{
			Host: relay.Host, Port: relay.Port, TLS: relay.TLS, Username: relay.Username, Password: relay.Password,
			From: relay.From, InsecureSkipVerify: relay.InsecureSkipVerify,
		}
	}
	inviter := im.InviterName
	if inviter == "" {
		inviter = "Someone"
	}
	_, err := m.n.SendMail(ctx, teamID, r, im.To, notifications.Payload{
		Type:     "team.invite",
		Severity: "info",
		Title:    fmt.Sprintf("%s invited you to %s on NebulaCtrl", inviter, im.TeamName),
		Body: fmt.Sprintf("You can join %s as %s. The invite is valid until %s.",
			im.TeamName, im.Role, im.ExpiresAt.UTC().Format("2 Jan 2006 15:04 MST")),
		URL: im.Link,
		Fields: []notifications.MessageField{
			{Name: "Team", Value: im.TeamName},
			{Name: "Role", Value: im.Role},
		},
	})
	if errors.Is(err, notifications.ErrNoMailer) {
		return teams.ErrNoMailer
	}
	return err
}
