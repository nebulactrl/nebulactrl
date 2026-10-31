package notifications

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// sendError is the failure of one delivery attempt. Message never holds a
// secret (see scrubError). Permanent errors are not retried.
type sendError struct {
	Message   string
	Code      int // HTTP status or SMTP reply code; 0 when there was none
	Permanent bool
}

func (e *sendError) Error() string { return e.Message }

const (
	httpTimeout   = 15 * time.Second
	smtpTimeout   = 45 * time.Second
	dialTimeout   = 10 * time.Second
	maxErrorBody  = 200
	userAgent     = "NebulaCtrl-Notifier/1"
	defaultTGBase = "https://api.telegram.org"
)

// senders carries what channel senders need; tests swap the HTTP client and the
// Telegram base URL.
type senders struct {
	http         *http.Client
	telegramBase string
	helloName    string
	// smtpDial opens the SMTP TCP connection (tests redirect it).
	smtpDial func(ctx context.Context, addr string) (net.Conn, error)
}

// blockedAddr reports addresses a notification must never reach: link-local
// ranges, which hold the cloud metadata services (169.254.169.254,
// fd00:ec2::254). LAN and loopback targets stay allowed: homelab receivers live
// there, and channels can only be configured by admins.
func blockedAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip == netip.MustParseAddr("fd00:ec2::254")
}

func guardedDialer() *net.Dialer {
	return &net.Dialer{
		Timeout: dialTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip, err := netip.ParseAddr(host); err == nil && blockedAddr(ip) {
				return errors.New("address is link-local or a cloud metadata service and is not allowed")
			}
			return nil
		},
	}
}

func newSenders(publicHost string) *senders {
	d := guardedDialer()
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         d.DialContext,
		TLSHandshakeTimeout: dialTimeout,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     30 * time.Second,
	}
	return &senders{
		http: &http.Client{
			Transport: tr,
			Timeout:   httpTimeout,
			// A redirect would send the body (and signature) somewhere the admin
			// did not configure.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		telegramBase: defaultTGBase,
		helloName:    helloHost(publicHost),
		smtpDial: func(ctx context.Context, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp", addr)
		},
	}
}

func helloHost(publicURL string) string {
	if u, err := url.Parse(publicURL); err == nil && u.Hostname() != "" && !strings.ContainsAny(u.Hostname(), ":[]") {
		return u.Hostname()
	}
	return "nebulactrl.local"
}

// --- HTTP channels -----------------------------------------------------------

// postJSON posts body and classifies the response. okBody, when set, must hold
// for a 2xx answer to count (Telegram answers 200 with ok:false for some errors).
func (s *senders) postJSON(ctx context.Context, endpoint string, body []byte, header http.Header, okBody func([]byte) error) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, &sendError{Message: "the endpoint URL is invalid", Permanent: true}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := s.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // never prints the URL: it holds the credential
		}
		return 0, &sendError{Message: "request failed: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	code := resp.StatusCode
	if code >= 200 && code < 300 {
		if okBody != nil {
			if err := okBody(raw); err != nil {
				return code, &sendError{Message: err.Error(), Code: code, Permanent: true}
			}
		}
		return code, nil
	}
	msg := fmt.Sprintf("HTTP %d", code)
	if snippet := bodySnippet(raw); snippet != "" {
		msg += ": " + snippet
	}
	// 4xx means the request itself is wrong (bad URL, revoked token, malformed
	// body): retrying repeats it. Timeouts and rate limits are the exceptions.
	perm := code >= 300 && code < 500 && code != http.StatusRequestTimeout && code != http.StatusTooEarly && code != http.StatusTooManyRequests
	return code, &sendError{Message: msg, Code: code, Permanent: perm}
}

func bodySnippet(raw []byte) string {
	s := strings.TrimSpace(cleanText(string(raw), false))
	return truncate(s, maxErrorBody)
}

func (s *senders) sendSlack(ctx context.Context, sec secrets, m Message) (int, error) {
	body, err := FormatSlack(m)
	if err != nil {
		return 0, &sendError{Message: "render failed: " + err.Error(), Permanent: true}
	}
	return s.postJSON(ctx, sec.URL, body, nil, nil)
}

func (s *senders) sendDiscord(ctx context.Context, sec secrets, m Message) (int, error) {
	body, err := FormatDiscord(m)
	if err != nil {
		return 0, &sendError{Message: "render failed: " + err.Error(), Permanent: true}
	}
	return s.postJSON(ctx, sec.URL, body, nil, nil)
}

func (s *senders) sendTelegram(ctx context.Context, cfg ChannelConfig, sec secrets, m Message) (int, error) {
	body, err := FormatTelegram(m, cfg.ChatID)
	if err != nil {
		return 0, &sendError{Message: "render failed: " + err.Error(), Permanent: true}
	}
	endpoint := strings.TrimRight(s.telegramBase, "/") + "/bot" + sec.BotToken + "/sendMessage"
	return s.postJSON(ctx, endpoint, body, nil, func(raw []byte) error {
		var r struct {
			OK          bool   `json:"ok"`
			Description string `json:"description"`
		}
		if json.Unmarshal(raw, &r) == nil && !r.OK {
			return fmt.Errorf("Telegram rejected the message: %s", truncate(cleanText(r.Description, false), maxErrorBody))
		}
		return nil
	})
}

func (s *senders) sendWebhook(ctx context.Context, sec secrets, m Message, now time.Time) (int, error) {
	body, err := FormatWebhook(m)
	if err != nil {
		return 0, &sendError{Message: "render failed: " + err.Error(), Permanent: true}
	}
	h := http.Header{}
	h.Set(SignatureHeader, Sign(sec.SigningSecret, body, now))
	h.Set(EventHeader, m.Type)
	h.Set(DeliveryHeader, m.ID)
	return s.postJSON(ctx, sec.URL, body, h, nil)
}

// --- Email ---------------------------------------------------------------------

// smtpAuth authenticates with PLAIN or, when the server only offers it, LOGIN.
// Unlike smtp.PlainAuth it does not refuse an unencrypted connection: the
// admin chose tls=none for that relay.
type smtpAuth struct {
	user, pass string
	login      bool
}

func (a *smtpAuth) Start(si *smtp.ServerInfo) (string, []byte, error) {
	hasPlain, hasLogin := false, false
	for _, m := range si.Auth {
		switch strings.ToUpper(m) {
		case "PLAIN":
			hasPlain = true
		case "LOGIN":
			hasLogin = true
		}
	}
	switch {
	case hasPlain:
		return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
	case hasLogin:
		a.login = true
		return "LOGIN", nil, nil
	}
	return "", nil, errors.New("the server offers neither PLAIN nor LOGIN authentication")
}

func (a *smtpAuth) Next(from []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	if !a.login {
		return nil, errors.New("unexpected server challenge")
	}
	switch strings.ToLower(strings.TrimSpace(string(from))) {
	case "username:":
		return []byte(a.user), nil
	case "password:":
		return []byte(a.pass), nil
	}
	return nil, errors.New("unexpected server challenge")
}

func smtpFailure(err error, what string) *sendError {
	var te *textproto.Error
	if errors.As(err, &te) {
		return &sendError{Message: fmt.Sprintf("%s: %d %s", what, te.Code, truncate(cleanText(te.Msg, false), maxErrorBody)), Code: te.Code, Permanent: te.Code >= 500}
	}
	return &sendError{Message: what + ": " + truncate(cleanText(err.Error(), false), maxErrorBody)}
}

// sendEmail delivers one message over SMTP: dial (plain or implicit TLS),
// EHLO, optional STARTTLS, optional AUTH, MAIL/RCPT/DATA, QUIT. Only the
// standard library speaks the protocol.
func (s *senders) sendEmail(ctx context.Context, cfg ChannelConfig, sec secrets, m Message, now time.Time) (int, error) {
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return 0, &sendError{Message: "the sender address is invalid", Permanent: true}
	}
	if from.Name == "" {
		from.Name = "NebulaCtrl"
	}
	domain := "nebulactrl.local"
	if _, d, ok := strings.Cut(from.Address, "@"); ok && d != "" {
		domain = d
	}
	raw := BuildMIME(*from, cfg.To, RenderEmail(m), now, m.ID+"@"+domain, "nebulactrl-"+strings.ReplaceAll(m.ID, "-", ""))

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.InsecureSkipVerify} //nolint:gosec // opt-in per channel
	conn, err := s.smtpDial(ctx, addr)
	if err != nil {
		return 0, &sendError{Message: "could not connect to " + addr + ": " + truncate(cleanText(err.Error(), false), maxErrorBody)}
	}
	// net/smtp has no context: closing the connection aborts a blocked call.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(smtpTimeout))

	if cfg.TLS == TLSImplicit {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return 0, &sendError{Message: "TLS handshake failed: " + truncate(cleanText(err.Error(), false), maxErrorBody)}
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return 0, smtpFailure(err, "the server did not greet")
	}
	defer c.Close()
	if err := c.Hello(s.helloName); err != nil {
		return 0, smtpFailure(err, "EHLO rejected")
	}
	if cfg.TLS == TLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return 0, &sendError{Message: "the server does not offer STARTTLS; choose another connection security", Permanent: true}
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return 0, smtpFailure(err, "STARTTLS failed")
		}
	}
	if cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return 0, &sendError{Message: "the server does not offer authentication", Permanent: true}
		}
		if err := c.Auth(&smtpAuth{user: cfg.Username, pass: sec.Password}); err != nil {
			return 0, smtpFailure(err, "authentication failed")
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return 0, smtpFailure(err, "sender rejected")
	}
	for _, rcpt := range cfg.To {
		if err := c.Rcpt(rcpt); err != nil {
			return 0, smtpFailure(err, "recipient "+rcpt+" rejected")
		}
	}
	w, err := c.Data()
	if err != nil {
		return 0, smtpFailure(err, "DATA rejected")
	}
	if _, err := w.Write(raw); err != nil {
		return 0, smtpFailure(err, "sending the message failed")
	}
	if err := w.Close(); err != nil {
		return 0, smtpFailure(err, "the server did not accept the message")
	}
	_ = c.Quit()
	return 250, nil
}

// --- dispatch --------------------------------------------------------------------

// send renders and delivers m on a channel of the given kind.
func (s *senders) send(ctx context.Context, kind string, cfg ChannelConfig, sec secrets, m Message, now time.Time) (int, error) {
	switch kind {
	case KindEmail:
		return s.sendEmail(ctx, cfg, sec, m, now)
	case KindSlack:
		return s.sendSlack(ctx, sec, m)
	case KindDiscord:
		return s.sendDiscord(ctx, sec, m)
	case KindTelegram:
		return s.sendTelegram(ctx, cfg, sec, m)
	case KindWebhook:
		return s.sendWebhook(ctx, sec, m, now)
	}
	return 0, &sendError{Message: "unknown channel kind " + kind, Permanent: true}
}

// scrubError removes every secret (and anything shaped like a URL) from an
// error message before it is stored or shown.
func scrubError(msg string, sec secrets) string {
	for _, v := range sec.all() {
		msg = strings.ReplaceAll(msg, v, mask)
	}
	// A stray URL may still hold a token the secret list did not cover (a
	// redirect target, a proxy message): keep scheme and host only.
	out := make([]string, 0, 8)
	for _, w := range strings.Fields(msg) {
		trimmed := strings.TrimRight(w, `",.;:)'`)
		if u, err := url.Parse(trimmed); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && (u.Path != "" && u.Path != "/" || u.RawQuery != "") {
			w = u.Scheme + "://" + u.Host + "/" + mask + w[len(trimmed):]
		}
		out = append(out, w)
	}
	return truncate(strings.Join(out, " "), 400)
}
