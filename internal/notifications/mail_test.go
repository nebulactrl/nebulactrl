package notifications

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func inviteMail() Payload {
	return Payload{Type: "team.invite", Severity: SeverityInfo, Title: "You were invited", Body: "Join the team.", URL: "https://ctrl.example.com/invite", Transactional: true}
}

func TestSendMailWithoutAServiceHasNoMailer(t *testing.T) {
	var s *Service
	_, err := s.SendMail(context.Background(), uuid.New(), nil, "x@example.com", inviteMail())
	require.ErrorIs(t, err, ErrNoMailer)
	ok, err := s.AnyEmailChannel(context.Background())
	require.NoError(t, err)
	require.False(t, ok)
}

func TestSendMailUsesTheFirstEnabledEmailChannelAndMailsOnlyTheRecipient(t *testing.T) {
	f := newFixture(t)
	hookURL, _ := hook(t, 200, "ok")
	f.slack("chat", hookURL, nil) // not an email channel: skipped
	disabledSMTP := newFakeSMTP(t, nil)
	off, err := f.svc.CreateChannel(f.ctx, ChannelInput{Name: "disabled mail", Kind: KindEmail, Config: disabledSMTP.emailConfig(TLSNone), Enabled: new(false)})
	require.NoError(t, err)
	require.False(t, off.Channel.Enabled)
	liveSMTP := newFakeSMTP(t, nil)
	_, err = f.svc.CreateChannel(f.ctx, ChannelInput{Name: "live mail", Kind: KindEmail, Config: liveSMTP.emailConfig(TLSNone)})
	require.NoError(t, err)

	// The relay is ignored while a channel exists.
	relaySMTP := newFakeSMTP(t, nil)
	via, err := f.svc.SendMail(context.Background(), f.ws.ID, relayTo(relaySMTP), "new@example.com", inviteMail())
	require.NoError(t, err)
	require.Equal(t, "channel", via)
	require.Empty(t, disabledSMTP.mails(), "a disabled channel is never used")
	require.Empty(t, relaySMTP.mails())
	mails := liveSMTP.mails()
	require.Len(t, mails, 1)
	require.Equal(t, []string{"new@example.com"}, mails[0].Rcpts, "the channel's own recipient list is replaced")
	body := plainBody(mails[0].Data)
	require.Contains(t, body, "Subject: [NebulaCtrl] You were invited")
	require.Contains(t, body, "https://ctrl.example.com/invite")

	// Another workspace's channels are not ours: with no relay, there is no mailer.
	_, err = f.svc.SendMail(context.Background(), uuid.New(), nil, "new@example.com", inviteMail())
	require.ErrorIs(t, err, ErrNoMailer)
	require.Len(t, liveSMTP.mails(), 1)
}

func TestSendMailFallsBackToTheRelay(t *testing.T) {
	f := newFixture(t)
	relaySMTP := newFakeSMTP(t, nil)
	via, err := f.svc.SendMail(context.Background(), f.ws.ID, relayTo(relaySMTP), "new@example.com", inviteMail())
	require.NoError(t, err)
	require.Equal(t, "relay", via)
	require.Len(t, relaySMTP.mails(), 1)
	require.Equal(t, "no-reply@example.com", relaySMTP.mails()[0].From)

	// A relay with a blank host or sender is no relay.
	for _, r := range []*MailRelay{
		{Host: "  ", From: "a@example.com"},
		{Host: "127.0.0.1", From: ""},
	} {
		_, err = f.svc.SendMail(context.Background(), f.ws.ID, r, "new@example.com", inviteMail())
		require.ErrorIs(t, err, ErrNoMailer)
	}
}

func TestSendMailRelayAuthFailureNamesNoPassword(t *testing.T) {
	f := newFixture(t)
	smtp := newFakeSMTP(t, func(s *fakeSMTP) {
		s.starttls, s.auth, s.user, s.pass = true, []string{"PLAIN"}, "mailer", "right-password"
	})
	relay := relayTo(smtp)
	relay.TLS = TLSStartTLS
	relay.Username, relay.Password = "mailer", "wrong-password-xyz"

	via, err := f.svc.SendMail(context.Background(), f.ws.ID, relay, "new@example.com", inviteMail())
	require.Error(t, err)
	require.Equal(t, "relay", via, "the failed transport is still reported")
	require.Contains(t, err.Error(), "authentication failed")
	require.NotContains(t, err.Error(), "wrong-password-xyz")
	require.Empty(t, smtp.mails())

	// The right credentials deliver.
	relay.Password = "right-password"
	_, err = f.svc.SendMail(context.Background(), f.ws.ID, relay, "new@example.com", inviteMail())
	require.NoError(t, err)
	require.Equal(t, []string{"mailer:right-password"}, smtp.authed)
	require.Len(t, smtp.mails(), 1)
}

func TestSendMailUnreachableServerIsReportedWithoutLeakingTheChannelPassword(t *testing.T) {
	f := newFixture(t)
	smtp := newFakeSMTP(t, nil)
	cfg := smtp.emailConfig(TLSNone)
	cfg.Username = "mailer"
	require.NoError(t, smtp.ln.Close())
	_, err := f.svc.CreateChannel(f.ctx, ChannelInput{Name: "dead mail", Kind: KindEmail, Config: cfg, Secrets: ChannelSecretsInput{Password: "hunter2-hunter2"}})
	require.NoError(t, err)

	via, err := f.svc.SendMail(context.Background(), f.ws.ID, nil, "new@example.com", inviteMail())
	require.Equal(t, "channel", via)
	require.ErrorContains(t, err, "could not connect")
	require.NotContains(t, err.Error(), "hunter2")
}

func TestPasswordResetStopsAtAFailingTeamChannel(t *testing.T) {
	f := newFixture(t)
	dead := newFakeSMTP(t, nil)
	cfg := dead.emailConfig(TLSNone)
	require.NoError(t, dead.ln.Close())
	_, err := f.svc.CreateChannel(f.ctx, ChannelInput{Name: "dead mail", Kind: KindEmail, Config: cfg})
	require.NoError(t, err)

	// A team with a broken channel is an error to report; only "no mailer"
	// moves on to the next team.
	via, err := f.svc.SendPasswordReset(context.Background(), nil, []uuid.UUID{f.ws.ID, uuid.New()}, resetMail())
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNoMailer)
	require.Equal(t, "channel", via)
}

func TestHelloHostFallsBackForUnusablePublicURLs(t *testing.T) {
	require.Equal(t, "ctrl.example.com", helloHost("https://ctrl.example.com:8443"))
	for _, u := range []string{"", "not a url", "http://[::1]:8080", "/relative"} {
		require.Equal(t, "nebulactrl.local", helloHost(u), u)
	}
}

func TestMailTransportPicksAResendRelay(t *testing.T) {
	f := newFixture(t)
	cfg, sec, via, err := f.svc.mailTransport(context.Background(), f.ws.ID, &MailRelay{
		Provider: ProviderResend, APIKey: "re_relay_key", From: "NebulaCtrl <no-reply@example.com>",
		// SMTP settings left over on the relay are not part of a Resend transport.
		Host: "smtp.example.com", Port: 587, Username: "u", Password: "p",
	})
	require.NoError(t, err)
	require.Equal(t, "relay", via)
	require.Equal(t, ChannelConfig{Provider: ProviderResend, From: "NebulaCtrl <no-reply@example.com>"}, cfg)
	require.Equal(t, secrets{APIKey: "re_relay_key"}, sec)

	// An smtp relay stays SMTP, with an empty provider meaning smtp too.
	for _, provider := range []string{"", ProviderSMTP} {
		cfg, sec, via, err = f.svc.mailTransport(context.Background(), f.ws.ID, &MailRelay{
			Provider: provider, Host: "smtp.example.com", Port: 587, TLS: TLSStartTLS, Username: "u", Password: "p", From: "no-reply@example.com",
		})
		require.NoError(t, err)
		require.Equal(t, "relay", via)
		require.Equal(t, ProviderSMTP, cfg.Provider)
		require.Equal(t, "smtp.example.com", cfg.Host)
		require.Equal(t, secrets{Password: "p"}, sec)
	}
}

func TestMailRelayConfigured(t *testing.T) {
	for name, tc := range map[string]struct {
		relay *MailRelay
		want  bool
	}{
		"nil":                  {nil, false},
		"smtp":                 {&MailRelay{Host: "smtp.example.com", From: "a@example.com"}, true},
		"smtp without host":    {&MailRelay{Host: " ", From: "a@example.com"}, false},
		"smtp without from":    {&MailRelay{Host: "smtp.example.com"}, false},
		"smtp ignores the key": {&MailRelay{APIKey: "re_k", From: "a@example.com"}, false},
		"resend":               {&MailRelay{Provider: ProviderResend, APIKey: "re_k", From: "a@example.com"}, true},
		"resend without key":   {&MailRelay{Provider: ProviderResend, From: "a@example.com"}, false},
		"resend without from":  {&MailRelay{Provider: ProviderResend, APIKey: "re_k"}, false},
		"resend ignores host":  {&MailRelay{Provider: ProviderResend, Host: "smtp.example.com", From: "a@example.com"}, false},
	} {
		require.Equal(t, tc.want, tc.relay.Configured(), name)
	}
}

func TestSendMailThroughAResendRelay(t *testing.T) {
	f := newFixture(t)
	url, c := hook(t, 200, `{"id":"49a3999c"}`)
	f.svc.send.resendBase = url
	relay := &MailRelay{Provider: ProviderResend, APIKey: "re_relay_key", From: "NebulaCtrl <no-reply@example.com>"}

	via, err := f.svc.SendMail(context.Background(), f.ws.ID, relay, "new@example.com", inviteMail())
	require.NoError(t, err)
	require.Equal(t, "relay", via)
	got := c.all()
	require.Len(t, got, 1)
	require.Equal(t, "POST", got[0].Method)
	require.Equal(t, "/emails", got[0].Path)
	require.Equal(t, "Bearer re_relay_key", got[0].Header.Get("Authorization"))
	var body struct {
		To      []string `json:"to"`
		Subject string   `json:"subject"`
	}
	require.NoError(t, json.Unmarshal(got[0].Body, &body))
	require.Equal(t, []string{"new@example.com"}, body.To)
	require.Equal(t, "[NebulaCtrl] You were invited", body.Subject)

	// Password resets take the same relay.
	via, err = f.svc.SendPasswordReset(context.Background(), relay, nil, resetMail())
	require.NoError(t, err)
	require.Equal(t, "relay", via)
	require.Len(t, c.all(), 2)

	// A Resend relay without a key is no relay.
	_, err = f.svc.SendMail(context.Background(), f.ws.ID, &MailRelay{Provider: ProviderResend, From: "a@example.com"}, "new@example.com", inviteMail())
	require.ErrorIs(t, err, ErrNoMailer)
	require.Len(t, c.all(), 2)
}

func TestSendMailPrefersAResendChannelOverTheRelay(t *testing.T) {
	f := newFixture(t)
	url, c := hook(t, 200, `{"id":"x"}`)
	f.svc.send.resendBase = url
	_, err := f.svc.CreateChannel(f.ctx, ChannelInput{Name: "resend", Kind: KindEmail,
		Config:  ChannelConfig{Provider: ProviderResend, From: "alerts@example.com", To: []string{"ops@example.com"}},
		Secrets: ChannelSecretsInput{APIKey: "re_channel_key"},
	})
	require.NoError(t, err)
	relaySMTP := newFakeSMTP(t, nil)

	via, err := f.svc.SendMail(context.Background(), f.ws.ID, relayTo(relaySMTP), "new@example.com", inviteMail())
	require.NoError(t, err)
	require.Equal(t, "channel", via)
	require.Empty(t, relaySMTP.mails())
	got := c.all()
	require.Len(t, got, 1)
	require.Equal(t, "Bearer re_channel_key", got[0].Header.Get("Authorization"))
	var body struct {
		To []string `json:"to"`
	}
	require.NoError(t, json.Unmarshal(got[0].Body, &body))
	require.Equal(t, []string{"new@example.com"}, body.To, "the channel's own recipients are replaced")
}

func TestSendMailResendRejectionNamesNoKey(t *testing.T) {
	f := newFixture(t)
	const key = "re_leaky_key_value"
	url, _ := hook(t, 401, `{"statusCode":401,"name":"restricted_api_key","message":"The key `+key+` is restricted"}`)
	f.svc.send.resendBase = url

	via, err := f.svc.SendMail(context.Background(), f.ws.ID, &MailRelay{Provider: ProviderResend, APIKey: key, From: "a@example.com"}, "new@example.com", inviteMail())
	require.Equal(t, "relay", via)
	require.ErrorContains(t, err, "HTTP 401")
	require.ErrorContains(t, err, "is restricted")
	require.NotContains(t, err.Error(), key)
}
