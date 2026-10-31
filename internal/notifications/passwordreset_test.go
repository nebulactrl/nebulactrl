package notifications

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// plainBody undoes the quoted-printable encoding of a fake mail's text.
func plainBody(data string) string {
	data = strings.ReplaceAll(data, "=\r\n", "")
	data = strings.ReplaceAll(data, "=\n", "")
	return strings.ReplaceAll(data, "=3D", "=")
}

func resetMail() PasswordResetMail {
	return PasswordResetMail{
		To: "dev@example.com", Link: "https://ctrl.example.com/reset-password?token=ncr_abcdefghijklmnop",
		ExpiresAt: time.Date(2026, 10, 27, 14, 35, 0, 0, time.UTC),
	}
}

func relayTo(f *fakeSMTP) *MailRelay {
	return &MailRelay{Host: "127.0.0.1", Port: f.port(), TLS: TLSNone, From: "NebulaCtrl <no-reply@example.com>", InsecureSkipVerify: true}
}

func TestPasswordResetThroughTheInstanceRelay(t *testing.T) {
	f := newFixture(t)
	smtp := newFakeSMTP(t, nil)

	via, err := f.svc.SendPasswordReset(context.Background(), relayTo(smtp), nil, resetMail())
	require.NoError(t, err)
	require.Equal(t, "relay", via)

	mails := smtp.mails()
	require.Len(t, mails, 1)
	require.Equal(t, []string{"dev@example.com"}, mails[0].Rcpts, "to the account only")
	require.Equal(t, "no-reply@example.com", mails[0].From)
	body := plainBody(mails[0].Data)
	require.Contains(t, body, "Subject: [NebulaCtrl] Reset your NebulaCtrl password")
	require.Contains(t, body, "https://ctrl.example.com/reset-password?token=ncr_abcdefghijklmnop")
	require.Contains(t, body, "Choose a new password", "the button names the action")
	require.Contains(t, body, "expires at 14:35 UTC")
	require.NotContains(t, body, "subscribes", "a transactional mail is not a channel alert")
	require.NotContains(t, body, "Open in NebulaCtrl")
}

func TestPasswordResetFallsBackToATeamEmailChannel(t *testing.T) {
	f := newFixture(t)
	smtp := newFakeSMTP(t, nil)
	cfg := smtp.emailConfig(TLSNone)
	_, err := f.svc.CreateChannel(f.ctx, ChannelInput{Name: "ops mail", Kind: KindEmail, Config: cfg})
	require.NoError(t, err)

	via, err := f.svc.SendPasswordReset(context.Background(), nil, []uuid.UUID{uuid.New(), f.ws.ID}, resetMail())
	require.NoError(t, err)
	require.Equal(t, "channel", via, "the first of the account's teams with an email channel")
	mails := smtp.mails()
	require.Len(t, mails, 1)
	require.Equal(t, []string{"dev@example.com"}, mails[0].Rcpts, "the channel's own recipients are not mailed")
}

func TestPasswordResetPrefersTheInstanceRelayOverATeamChannel(t *testing.T) {
	f := newFixture(t)
	relaySMTP, channelSMTP := newFakeSMTP(t, nil), newFakeSMTP(t, nil)
	_, err := f.svc.CreateChannel(f.ctx, ChannelInput{Name: "ops mail", Kind: KindEmail, Config: channelSMTP.emailConfig(TLSNone)})
	require.NoError(t, err)

	via, err := f.svc.SendPasswordReset(context.Background(), relayTo(relaySMTP), []uuid.UUID{f.ws.ID}, resetMail())
	require.NoError(t, err)
	require.Equal(t, "relay", via)
	require.Len(t, relaySMTP.mails(), 1)
	require.Empty(t, channelSMTP.mails())
}

func TestPasswordResetWithoutAnyMailer(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.SendPasswordReset(context.Background(), nil, []uuid.UUID{f.ws.ID}, resetMail())
	require.ErrorIs(t, err, ErrNoMailer)
	_, err = f.svc.SendPasswordReset(context.Background(), &MailRelay{Host: "127.0.0.1"}, nil, resetMail())
	require.ErrorIs(t, err, ErrNoMailer, "a relay without a sender address is no relay")

	ok, err := f.svc.AnyEmailChannel(context.Background())
	require.NoError(t, err)
	require.False(t, ok)
	_, err = f.svc.CreateChannel(f.ctx, ChannelInput{Name: "ops mail", Kind: KindEmail, Config: newFakeSMTP(t, nil).emailConfig(TLSNone)})
	require.NoError(t, err)
	ok, err = f.svc.AnyEmailChannel(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
}

// An alert email keeps its generic button and subscription footer.
func TestAlertEmailsAreUnchangedByTransactionalMode(t *testing.T) {
	e := RenderEmail(fullMessage())
	require.Contains(t, e.HTML, "Open in NebulaCtrl")
	require.Contains(t, e.HTML, "You get this because an email channel subscribes to deploy.failed.")
	require.Contains(t, e.Text, "Open in NebulaCtrl: ")
}
