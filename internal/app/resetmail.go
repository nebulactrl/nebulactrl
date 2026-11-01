package app

import (
	"context"
	"errors"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/teams"
)

// resetMailer sends password reset emails through the instance SMTP relay, and
// through the account's team email channel when the instance has no relay.
type resetMailer struct {
	n *notifications.Service
	t *teams.Service
	q *db.Queries
}

var _ auth.ResetMailer = resetMailer{}

func (m resetMailer) relay(ctx context.Context) (*notifications.MailRelay, error) {
	r, err := m.t.InstanceRelay(ctx)
	if err != nil || r == nil {
		return nil, err
	}
	return &notifications.MailRelay{
		Host: r.Host, Port: r.Port, TLS: r.TLS, Username: r.Username, Password: r.Password, From: r.From, InsecureSkipVerify: r.InsecureSkipVerify,
	}, nil
}

// Available reports whether the instance has an SMTP relay or some team has an
// enabled email channel.
func (m resetMailer) Available(ctx context.Context) bool {
	if r, err := m.relay(ctx); err == nil && r != nil {
		return true
	}
	ok, err := m.n.AnyEmailChannel(ctx)
	return err == nil && ok
}

func (m resetMailer) SendPasswordReset(ctx context.Context, rm auth.ResetMail) error {
	relay, err := m.relay(ctx)
	if err != nil {
		return err
	}
	teamIDs, err := m.q.ListUserTeamIDs(ctx, rm.UserID)
	if err != nil {
		return err
	}
	_, err = m.n.SendPasswordReset(ctx, relay, teamIDs, notifications.PasswordResetMail{To: rm.To, Link: rm.Link, ExpiresAt: rm.ExpiresAt})
	if errors.Is(err, notifications.ErrNoMailer) {
		return errors.New("no SMTP relay or team email channel can reach this account")
	}
	return err
}
