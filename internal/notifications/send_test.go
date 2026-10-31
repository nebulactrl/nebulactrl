package notifications

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// capture records the requests a test server received.
type capture struct {
	mu   sync.Mutex
	reqs []capturedReq
}

type capturedReq struct {
	Path   string
	Header http.Header
	Body   []byte
}

func (c *capture) handler(status int, respBody string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.reqs = append(c.reqs, capturedReq{Path: r.URL.RequestURI(), Header: r.Header.Clone(), Body: b})
		c.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}
}

func (c *capture) all() []capturedReq {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedReq(nil), c.reqs...)
}

func testSenders() *senders {
	s := newSenders("https://ctrl.example.com")
	return s
}

func asSendError(t *testing.T, err error) *sendError {
	t.Helper()
	var se *sendError
	require.ErrorAs(t, err, &se)
	return se
}

func TestSendSlackPostsBlockKit(t *testing.T) {
	var cap capture
	srv := httptest.NewServer(cap.handler(200, "ok"))
	defer srv.Close()
	code, err := testSenders().sendSlack(context.Background(), secrets{URL: srv.URL + "/services/T000/B000/XXXXXXXX"}, fullMessage())
	require.NoError(t, err)
	require.Equal(t, 200, code)
	got := cap.all()
	require.Len(t, got, 1)
	require.Equal(t, "/services/T000/B000/XXXXXXXX", got[0].Path)
	require.Equal(t, "application/json", got[0].Header.Get("Content-Type"))
	var body struct {
		Attachments []struct {
			Blocks []struct{ Type string } `json:"blocks"`
		} `json:"attachments"`
	}
	require.NoError(t, json.Unmarshal(got[0].Body, &body))
	require.Equal(t, "header", body.Attachments[0].Blocks[0].Type)
}

func TestSendDiscordAccepts204(t *testing.T) {
	var cap capture
	srv := httptest.NewServer(cap.handler(204, ""))
	defer srv.Close()
	code, err := testSenders().sendDiscord(context.Background(), secrets{URL: srv.URL + "/api/webhooks/1/token"}, fullMessage())
	require.NoError(t, err)
	require.Equal(t, 204, code)
	var body struct {
		Embeds []struct{ Title string } `json:"embeds"`
	}
	require.NoError(t, json.Unmarshal(cap.all()[0].Body, &body))
	require.Equal(t, "api_v2 failed to deploy", body.Embeds[0].Title)
}

func TestSendTelegramUsesBotEndpoint(t *testing.T) {
	var cap capture
	srv := httptest.NewServer(cap.handler(200, `{"ok":true,"result":{}}`))
	defer srv.Close()
	s := testSenders()
	s.telegramBase = srv.URL
	code, err := s.sendTelegram(context.Background(), ChannelConfig{ChatID: "-100123"}, secrets{BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ"}, fullMessage())
	require.NoError(t, err)
	require.Equal(t, 200, code)
	got := cap.all()[0]
	require.Equal(t, "/bot123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ/sendMessage", got.Path)
	var body struct {
		ChatID    string `json:"chat_id"`
		ParseMode string `json:"parse_mode"`
	}
	require.NoError(t, json.Unmarshal(got.Body, &body))
	require.Equal(t, "-100123", body.ChatID)
	require.Equal(t, "MarkdownV2", body.ParseMode)
}

func TestTelegramOKFalseIsAFailure(t *testing.T) {
	srv := httptest.NewServer((&capture{}).handler(200, `{"ok":false,"description":"Bad Request: chat not found"}`))
	defer srv.Close()
	s := testSenders()
	s.telegramBase = srv.URL
	_, err := s.sendTelegram(context.Background(), ChannelConfig{ChatID: "1"}, secrets{BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ"}, minimalMessage())
	se := asSendError(t, err)
	require.True(t, se.Permanent)
	require.Contains(t, se.Message, "chat not found")
}

func TestSendWebhookSignsTheBody(t *testing.T) {
	var cap capture
	srv := httptest.NewServer(cap.handler(200, ""))
	defer srv.Close()
	const secret = "whsec_0123456789abcdef"
	now := time.Unix(1_700_000_000, 0)
	_, err := testSenders().sendWebhook(context.Background(), secrets{URL: srv.URL + "/hook", SigningSecret: secret}, fullMessage(), now)
	require.NoError(t, err)
	got := cap.all()[0]
	require.Equal(t, TypeDeployFailed, got.Header.Get(EventHeader))
	require.Equal(t, fixtureDelivery, got.Header.Get(DeliveryHeader))
	// The receiver verifies the raw bytes it got against the header.
	require.NoError(t, VerifySignature(secret, got.Header.Get(SignatureHeader), got.Body, now, 0))
	require.Error(t, VerifySignature("whsec_wrong_wrong_wrong", got.Header.Get(SignatureHeader), got.Body, now, 0))
	require.NotContains(t, string(got.Body), secret, "the secret never travels in the body")
}

func TestHTTPFailureClassification(t *testing.T) {
	cases := []struct {
		status    int
		permanent bool
	}{
		{400, true}, {401, true}, {403, true}, {404, true}, {410, true}, {422, true},
		{408, false}, {425, false}, {429, false},
		{500, false}, {502, false}, {503, false}, {504, false},
		{301, true}, {302, true},
	}
	for _, tc := range cases {
		srv := httptest.NewServer((&capture{}).handler(tc.status, "no_service"))
		_, err := testSenders().sendSlack(context.Background(), secrets{URL: srv.URL + "/hook"}, minimalMessage())
		srv.Close()
		se := asSendError(t, err)
		require.Equal(t, tc.permanent, se.Permanent, "status %d", tc.status)
		require.Equal(t, tc.status, se.Code)
		require.Contains(t, se.Message, "no_service", "the response body explains the failure")
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var target capture
	dest := httptest.NewServer(target.handler(200, "ok"))
	defer dest.Close()
	src := httptest.NewServer(http.RedirectHandler(dest.URL, http.StatusFound))
	defer src.Close()
	_, err := testSenders().sendWebhook(context.Background(), secrets{URL: src.URL, SigningSecret: "whsec_0123456789abcdef"}, minimalMessage(), time.Now())
	require.Error(t, err)
	require.Empty(t, target.all(), "a redirect would carry the signed body somewhere the admin did not choose")
}

func TestNetworkErrorsNeverContainTheURL(t *testing.T) {
	// A closed port: the client error normally prints the full URL.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/services/T000/B000/SECRETTOKENVALUE"
	srv.Close()
	_, err := testSenders().sendSlack(context.Background(), secrets{URL: url}, minimalMessage())
	se := asSendError(t, err)
	require.False(t, se.Permanent, "an unreachable host is retried")
	require.NotContains(t, se.Message, "SECRETTOKENVALUE")
	require.NotContains(t, se.Message, "http://")
}

func TestMetadataAddressesAreBlocked(t *testing.T) {
	for _, a := range []string{"169.254.169.254", "169.254.0.1", "fe80::1", "fd00:ec2::254", "::ffff:169.254.169.254"} {
		require.True(t, blockedAddr(netip.MustParseAddr(a)), a)
	}
	for _, a := range []string{"127.0.0.1", "10.0.0.5", "192.168.1.10", "8.8.8.8", "::1"} {
		require.False(t, blockedAddr(netip.MustParseAddr(a)), a)
	}
	_, err := testSenders().sendWebhook(context.Background(), secrets{URL: "http://169.254.169.254/latest/meta-data", SigningSecret: "whsec_0123456789abcdef"}, minimalMessage(), time.Now())
	se := asSendError(t, err)
	require.Contains(t, se.Message, "not allowed")
	require.NotContains(t, se.Message, "169.254.169.254/latest")
}

func TestScrubErrorMasksSecretsAndURLs(t *testing.T) {
	sec := secrets{URL: "https://hooks.slack.com/services/T000/B000/XXXXXXXXXXXXXXXX", Password: "hunter2hunter2", BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ", SigningSecret: "whsec_abcdefghijklmnop"}
	msg := `Post "https://hooks.slack.com/services/T000/B000/XXXXXXXXXXXXXXXX": dial failed; password hunter2hunter2; ` +
		`bot 123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ; secret whsec_abcdefghijklmnop; also https://other.example/a/b?token=zzz.`
	out := scrubError(msg, sec)
	for _, leak := range []string{"XXXXXXXXXXXXXXXX", "hunter2hunter2", "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "whsec_abcdefghijklmnop", "token=zzz", "/a/b"} {
		require.NotContains(t, out, leak)
	}
	require.Contains(t, out, mask)
	require.Contains(t, out, "dial failed")
}

// --- SMTP ---------------------------------------------------------------------

func TestSMTPModes(t *testing.T) {
	cases := []struct {
		name    string
		tlsMode string
		server  func(*fakeSMTP)
	}{
		{"starttls", TLSStartTLS, func(f *fakeSMTP) { f.starttls = true }},
		{"implicit", TLSImplicit, func(f *fakeSMTP) { f.implicit = true }},
		{"none", TLSNone, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSMTP(t, tc.server)
			code, err := testSenders().sendEmail(context.Background(), f.emailConfig(tc.tlsMode), secrets{}, fullMessage(), fixtureTime)
			require.NoError(t, err)
			require.Equal(t, 250, code)
			mails := f.mails()
			require.Len(t, mails, 1)
			require.Equal(t, "alerts@example.com", mails[0].From)
			require.Equal(t, []string{"ops@example.com", "dev@example.com"}, mails[0].Rcpts)
			require.Contains(t, mails[0].Data, "Subject: [NebulaCtrl] api_v2 failed to deploy")
			require.Contains(t, mails[0].Data, "multipart/alternative")
			require.Contains(t, mails[0].Data, "Message-ID: <"+fixtureDelivery+"@example.com>")
			require.Equal(t, tc.tlsMode != TLSNone, f.sawTLS[0], "the message travels encrypted in tls modes")
			require.Equal(t, []string{"ctrl.example.com"}, f.hellos[:1], "EHLO names the control plane")
		})
	}
}

func TestSMTPAuth(t *testing.T) {
	for _, mech := range [][]string{{"PLAIN"}, {"LOGIN"}, {"LOGIN", "PLAIN"}} {
		f := newFakeSMTP(t, func(f *fakeSMTP) { f.starttls, f.auth, f.user, f.pass = true, mech, "mailer", "s3cret-pass" })
		cfg := f.emailConfig(TLSStartTLS)
		cfg.Username = "mailer"
		_, err := testSenders().sendEmail(context.Background(), cfg, secrets{Password: "s3cret-pass"}, minimalMessage(), fixtureTime)
		require.NoError(t, err, mech)
		require.Equal(t, []string{"mailer:s3cret-pass"}, f.authed, mech)
		require.Len(t, f.mails(), 1)
	}
}

func TestSMTPAuthFailureIsPermanentAndMasked(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) {
		f.starttls, f.auth, f.user, f.pass = true, []string{"PLAIN"}, "mailer", "right-password"
	})
	cfg := f.emailConfig(TLSStartTLS)
	cfg.Username = "mailer"
	_, err := testSenders().sendEmail(context.Background(), cfg, secrets{Password: "wrong-password"}, minimalMessage(), fixtureTime)
	se := asSendError(t, err)
	require.True(t, se.Permanent)
	require.Equal(t, 535, se.Code)
	require.Empty(t, f.mails())
	require.NotContains(t, scrubError(se.Message, secrets{Password: "wrong-password"}), "wrong-password")
}

func TestSMTPAuthRequiredButNoCredentials(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.starttls, f.auth, f.user, f.pass = true, []string{"PLAIN"}, "mailer", "x" })
	_, err := testSenders().sendEmail(context.Background(), f.emailConfig(TLSStartTLS), secrets{}, minimalMessage(), fixtureTime)
	se := asSendError(t, err)
	require.True(t, se.Permanent)
	require.Equal(t, 530, se.Code)
}

func TestSMTPSTARTTLSRequiredButNotOffered(t *testing.T) {
	f := newFakeSMTP(t, nil) // no STARTTLS advertised
	_, err := testSenders().sendEmail(context.Background(), f.emailConfig(TLSStartTLS), secrets{}, minimalMessage(), fixtureTime)
	se := asSendError(t, err)
	require.True(t, se.Permanent)
	require.Contains(t, se.Message, "STARTTLS")
	require.Empty(t, f.mails(), "nothing is sent in clear text when encryption was required")
}

func TestSMTPRejections(t *testing.T) {
	t.Run("recipient 550 is permanent", func(t *testing.T) {
		f := newFakeSMTP(t, func(f *fakeSMTP) { f.rejectRcpt["dev@example.com"] = "550 5.1.1 no such user" })
		_, err := testSenders().sendEmail(context.Background(), f.emailConfig(TLSNone), secrets{}, minimalMessage(), fixtureTime)
		se := asSendError(t, err)
		require.True(t, se.Permanent)
		require.Equal(t, 550, se.Code)
		require.Contains(t, se.Message, "dev@example.com")
		require.Empty(t, f.mails())
	})
	t.Run("4xx at the end of DATA is retried", func(t *testing.T) {
		f := newFakeSMTP(t, func(f *fakeSMTP) { f.failData = "451 4.3.0 try again later" })
		_, err := testSenders().sendEmail(context.Background(), f.emailConfig(TLSNone), secrets{}, minimalMessage(), fixtureTime)
		se := asSendError(t, err)
		require.False(t, se.Permanent)
		require.Equal(t, 451, se.Code)
	})
}

func TestSMTPUnreachableHostIsRetried(t *testing.T) {
	f := newFakeSMTP(t, nil)
	cfg := f.emailConfig(TLSNone)
	require.NoError(t, f.ln.Close())
	_, err := testSenders().sendEmail(context.Background(), cfg, secrets{}, minimalMessage(), fixtureTime)
	se := asSendError(t, err)
	require.False(t, se.Permanent)
	require.True(t, strings.HasPrefix(se.Message, "could not connect"))
}

func TestSMTPContextCancelAborts(t *testing.T) {
	f := newFakeSMTP(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := testSenders().sendEmail(ctx, f.emailConfig(TLSNone), secrets{}, minimalMessage(), fixtureTime)
	require.Error(t, err)
	require.Empty(t, f.mails())
}
