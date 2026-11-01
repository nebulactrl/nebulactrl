package secretmgr

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

const (
	requestTimeout = 15 * time.Second
	maxBody        = 1 << 20
	maxMessage     = 200
)

// provider reads secrets from one external manager. It holds its login token
// and the secrets it already read in memory, for the lifetime of one session.
type provider interface {
	fetch(ctx context.Context, ref varexpr.ExtRef) (string, error)
	// check authenticates without reading a secret.
	check(ctx context.Context) error
}

func newProvider(kind string, cfg SecretManagerConfig, secret string) (provider, error) {
	hc, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	switch kind {
	case KindVault:
		return &vaultProvider{cfg: cfg, secret: secret, hc: hc}, nil
	case KindInfisical:
		return &infisicalProvider{cfg: cfg, secret: secret, hc: hc}, nil
	case KindDoppler:
		return &dopplerProvider{cfg: cfg, secret: secret, hc: hc}, nil
	}
	return nil, fmt.Errorf("unknown integration kind %q", kind)
}

// newClient builds an HTTP client that never follows redirects (a standby node
// would otherwise receive the token header) and trusts cfg.CACert in addition
// to the system roots.
func newClient(cfg SecretManagerConfig) (*http.Client, error) {
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        2,
		IdleConnTimeout:     30 * time.Second,
	}
	if cfg.CACert != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(cfg.CACert)) {
			return nil, fmt.Errorf("the CA certificate is not valid PEM")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{
		Timeout:   requestTimeout,
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// call sends one request and returns the status and the (size-limited) body.
// A transport failure is returned as an error that never contains the request
// headers or body.
func call(ctx context.Context, hc *http.Client, method, rawURL string, hdr map[string]string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("encode request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := hc.Do(req)
	if err != nil {
		// url.Error repeats the URL (path and query hold no secret value, but
		// keep the message short and uniform).
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, nil, fmt.Errorf("cannot reach %s: %w", hostOf(rawURL), err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return 0, nil, fmt.Errorf("read response from %s: %w", hostOf(rawURL), err)
	}
	if len(data) > maxBody {
		return 0, nil, fmt.Errorf("response from %s is larger than %d bytes", hostOf(rawURL), maxBody)
	}
	return resp.StatusCode, data, nil
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return "the server"
}

// statusMessage describes a failed response: its meaning plus the server's own
// short message when it sent one (Vault `errors`, Infisical `message`, Doppler
// `messages`).
func statusMessage(status int, body []byte) string {
	var what string
	switch {
	case status == http.StatusUnauthorized:
		what = "authentication failed (invalid or expired credential)"
	case status == http.StatusForbidden:
		what = "permission denied"
	case status == http.StatusNotFound:
		what = "not found"
	case status == http.StatusTooManyRequests:
		what = "rate limited by the secret manager"
	case status >= 300 && status < 400:
		what = "redirected: point the integration at the active node's address"
	case status >= 500:
		what = fmt.Sprintf("the secret manager failed (HTTP %d)", status)
	default:
		what = fmt.Sprintf("unexpected response (HTTP %d)", status)
	}
	if m := serverMessage(body); m != "" {
		return what + ": " + m
	}
	return what
}

func serverMessage(body []byte) string {
	var v struct {
		Errors   []string `json:"errors"`
		Messages []string `json:"messages"`
		Message  string   `json:"message"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	m := strings.Join(v.Errors, "; ")
	if m == "" {
		m = strings.Join(v.Messages, "; ")
	}
	if m == "" {
		m = v.Message
	}
	m = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, m)
	m = strings.TrimSpace(m)
	if len(m) > maxMessage {
		m = m[:maxMessage] + "…"
	}
	return m
}

// escapePath escapes each segment of a slash separated path.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
